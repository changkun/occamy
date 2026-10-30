// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package rdp

import (
	"bytes"
	"crypto/hmac"
	"crypto/md5" // #nosec G501 -- NTLM is defined on MD5
	"crypto/rand"
	"crypto/rc4" // #nosec G503 -- NTLM seals with RC4
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/md4" //nolint:gosec,staticcheck // NTOWFv2 is defined on MD4
)

// The client's side of NTLMv2 ([MS-NLMP]) as CredSSP uses it: the three
// messages, then signing and sealing with extended session security in
// connection-oriented mode. NTLMv1, LM and anonymous logons are not
// implemented, and the server's challenge is parsed with every length
// checked, so that a hostile server gets an error, not a panic.

// NTLM negotiate flags, [MS-NLMP] 2.2.2.5.
const (
	ntlmUnicode       = 0x00000001
	ntlmOEM           = 0x00000002
	ntlmRequestTarget = 0x00000004
	ntlmSign          = 0x00000010
	ntlmSeal          = 0x00000020
	ntlmNTLM          = 0x00000200
	ntlmAlwaysSign    = 0x00008000
	ntlmESS           = 0x00080000 // extended session security
	ntlmTargetInfo    = 0x00800000
	ntlmVersion       = 0x02000000
	ntlm128           = 0x20000000
	ntlmKeyExch       = 0x40000000
	ntlm56            = 0x80000000

	// ntlmClientFlags are the flags the client asks for.
	ntlmClientFlags = ntlm56 | ntlmKeyExch | ntlm128 | ntlmVersion | ntlmESS | ntlmAlwaysSign |
		ntlmNTLM | ntlmSeal | ntlmSign | ntlmRequestTarget | ntlmOEM | ntlmUnicode
	// ntlmRequiredFlags are the flags the server must agree to: Unicode
	// names, the signatures of extended session security, and 128-bit
	// signing and sealing, which is what CredSSP needs.
	ntlmRequiredFlags = ntlmUnicode | ntlmESS | ntlmSign | ntlmSeal | ntlm128
)

// AV pair IDs of [MS-NLMP] 2.2.2.1.
const (
	avEOL       = 0x0000
	avFlags     = 0x0006
	avTimestamp = 0x0007

	// avFlagMIC is the MsvAvFlags bit that tells the server the
	// AUTHENTICATE_MESSAGE carries a MIC.
	avFlagMIC = 0x00000002
)

var ntlmSignature = []byte("NTLMSSP\x00")

// ntlmVersionField is the VERSION structure of [MS-NLMP] 2.2.2.10 that
// the client sends: Windows 10.0.19041, NTLM revision 15.
var ntlmVersionField = []byte{10, 0, 0x61, 0x4a, 0, 0, 0, 0x0f}

// The lengths of the fixed parts of the messages.
const (
	ntlmNegotiateLen    = 40 // with the version
	ntlmChallengeMinLen = 48 // without the version
	ntlmAuthenticateLen = 88 // with the version and the MIC
	ntlmMICOffset       = 72
)

// ntlmClient authenticates one user with NTLMv2.
type ntlmClient struct {
	domain, user, password string

	// The tests fix these; zero values select random ones and the clock.
	clientChallenge []byte
	exportedKey     []byte
	now             func() time.Time

	negotiate []byte // the NEGOTIATE_MESSAGE sent, which the MIC covers
}

// negotiateMessage returns the NEGOTIATE_MESSAGE, [MS-NLMP] 2.2.1.1.
func (c *ntlmClient) negotiateMessage() []byte {
	var w writer
	w.bytes(ntlmSignature)
	w.u32(1)
	w.u32(ntlmClientFlags)
	for range 2 { // DomainNameFields and WorkstationFields, empty
		w.u16(0)
		w.u16(0)
		w.u32(ntlmNegotiateLen)
	}
	w.bytes(ntlmVersionField)
	c.negotiate = w.b
	return w.b
}

// avPair is an AV pair of the server's target info, [MS-NLMP] 2.2.2.1.
type avPair struct {
	id    uint16
	value []byte
}

// ntlmChallenge is what the client takes from a CHALLENGE_MESSAGE.
type ntlmChallenge struct {
	flags           uint32
	serverChallenge []byte
	targetInfo      []avPair // without the MsvAvEOL that ends it
	timestamp       []byte   // the MsvAvTimestamp value, if the server sent one
}

// parseChallenge parses a CHALLENGE_MESSAGE, [MS-NLMP] 2.2.1.2.
func parseChallenge(msg []byte) (*ntlmChallenge, error) {
	if len(msg) < ntlmChallengeMinLen || !bytes.Equal(msg[:8], ntlmSignature) ||
		binary.LittleEndian.Uint32(msg[8:]) != 2 {
		return nil, errors.New("rdp: not an NTLM challenge")
	}
	ch := &ntlmChallenge{
		flags:           binary.LittleEndian.Uint32(msg[20:]),
		serverChallenge: bytes.Clone(msg[24:32]),
	}
	info, err := ntlmField(msg, 40)
	if err != nil {
		return nil, fmt.Errorf("rdp: NTLM challenge target info: %w", err)
	}
	if ch.targetInfo, err = parseAVPairs(info); err != nil {
		return nil, err
	}
	for _, p := range ch.targetInfo {
		if p.id == avTimestamp {
			ch.timestamp = p.value
		}
	}
	return ch, nil
}

// parseAVPairs parses AV pairs up to the MsvAvEOL that must end them, and
// checks the lengths of those the client reads.
func parseAVPairs(b []byte) ([]avPair, error) {
	var pairs []avPair
	r := reader{b: b}
	for {
		id := r.u16()
		v := r.take(int(r.u16()))
		if r.err != nil {
			return nil, errors.New("rdp: NTLM target info is truncated")
		}
		if id == avEOL {
			return pairs, nil
		}
		switch {
		case id == avTimestamp && len(v) != 8:
			return nil, fmt.Errorf("rdp: NTLM timestamp of %d bytes", len(v))
		case id == avFlags && len(v) != 4:
			return nil, fmt.Errorf("rdp: NTLM AV flags of %d bytes", len(v))
		}
		pairs = append(pairs, avPair{id, v})
	}
}

// ntlmField returns the payload that the field at off of an NTLM message
// points to: its length, maximum length and offset, [MS-NLMP] 2.2.
func ntlmField(msg []byte, off int) ([]byte, error) {
	if off+8 > len(msg) {
		return nil, errShort
	}
	n := int(binary.LittleEndian.Uint16(msg[off:]))
	at := int64(binary.LittleEndian.Uint32(msg[off+4:]))
	if at+int64(n) > int64(len(msg)) {
		return nil, fmt.Errorf("field of %d bytes at %d in %d", n, at, len(msg))
	}
	return msg[at : at+int64(n)], nil
}

// authenticate answers the server's CHALLENGE_MESSAGE. It returns the
// AUTHENTICATE_MESSAGE, [MS-NLMP] 2.2.1.3 and 3.1.5.1.2, and the session
// that signs and seals with the keys it establishes.
func (c *ntlmClient) authenticate(challenge []byte) ([]byte, *ntlmSession, error) {
	ch, err := parseChallenge(challenge)
	if err != nil {
		return nil, nil, err
	}
	if ch.flags&ntlmRequiredFlags != ntlmRequiredFlags {
		return nil, nil, fmt.Errorf("rdp: the server's NTLM challenge lacks flags %#08x", ntlmRequiredFlags&^ch.flags)
	}
	// The client confirms the server's flags, as FreeRDP does.
	flags := ch.flags

	clientChallenge, err := c.random(c.clientChallenge, 8)
	if err != nil {
		return nil, nil, err
	}
	// With the server's timestamp the client sends a MIC, says so in
	// MsvAvFlags, and leaves the LMv2 response empty; without it, the
	// target info goes back unchanged ([MS-NLMP] 3.1.5.1.2).
	pairs := ch.targetInfo
	stamp := ch.timestamp
	if stamp == nil {
		now := time.Now
		if c.now != nil {
			now = c.now
		}
		stamp = fileTime(now())
	} else {
		pairs = withMICFlag(pairs)
	}

	key := ntowfv2(c.password, c.user, c.domain)
	var temp writer
	temp.bytes([]byte{1, 1}) // RespType, HiRespType
	temp.zero(6)
	temp.bytes(stamp)
	temp.bytes(clientChallenge)
	temp.zero(4)
	temp.bytes(encodeAVPairs(pairs))
	temp.zero(4)
	proof := hmacMD5(key, ch.serverChallenge, temp.b)
	nt := append(proof, temp.b...)
	lm := make([]byte, 24)
	if ch.timestamp == nil {
		lm = append(hmacMD5(key, ch.serverChallenge, clientChallenge), clientChallenge...)
	}

	// For NTLMv2 the key exchange key is the session base key
	// ([MS-NLMP] 3.4.5.1); with key exchange the client picks the session
	// key and sends it encrypted under it.
	exported := hmacMD5(key, proof)
	var encKey []byte
	if flags&ntlmKeyExch != 0 {
		kxk := exported
		if exported, err = c.random(c.exportedKey, 16); err != nil {
			return nil, nil, err
		}
		encKey = rc4k(kxk, exported)
	}

	// The payload holds domain, user, workstation, LM and NT responses
	// and the encrypted session key; the header lists them with the
	// responses first.
	payload := [][]byte{utf16le(c.domain), utf16le(c.user), nil, lm, nt, encKey}
	offsets := make([]int, len(payload))
	at := ntlmAuthenticateLen
	for i, p := range payload {
		offsets[i] = at
		at += len(p)
	}
	var w writer
	w.bytes(ntlmSignature)
	w.u32(3)
	for _, i := range []int{3, 4, 0, 1, 2, 5} {
		w.u16(len(payload[i]))
		w.u16(len(payload[i]))
		w.u32(uint32(offsets[i])) // #nosec G115 -- a few hundred bytes
	}
	w.u32(flags)
	w.bytes(ntlmVersionField)
	w.zero(16) // MIC, below
	for _, p := range payload {
		w.bytes(p)
	}
	msg := w.b
	if ch.timestamp != nil {
		copy(msg[ntlmMICOffset:], hmacMD5(exported, c.negotiate, challenge, msg))
	}
	return msg, newNTLMSession(exported, flags, true), nil
}

// random returns fixed if it is set, else n random bytes.
func (c *ntlmClient) random(fixed []byte, n int) ([]byte, error) {
	if fixed != nil {
		return fixed, nil
	}
	b := make([]byte, n)
	_, err := rand.Read(b)
	return b, err
}

// withMICFlag returns pairs with the MIC bit set in MsvAvFlags, which is
// added if the server sent none.
func withMICFlag(pairs []avPair) []avPair {
	out := make([]avPair, 0, len(pairs)+1)
	found := false
	for _, p := range pairs {
		if p.id == avFlags {
			v := binary.LittleEndian.AppendUint32(nil, binary.LittleEndian.Uint32(p.value)|avFlagMIC)
			p = avPair{avFlags, v}
			found = true
		}
		out = append(out, p)
	}
	if !found {
		out = append(out, avPair{avFlags, binary.LittleEndian.AppendUint32(nil, avFlagMIC)})
	}
	return out
}

// encodeAVPairs encodes pairs and the MsvAvEOL that ends them.
func encodeAVPairs(pairs []avPair) []byte {
	var w writer
	for _, p := range pairs {
		w.u16(int(p.id))
		w.u16(len(p.value))
		w.bytes(p.value)
	}
	w.u16(avEOL)
	w.u16(0)
	return w.b
}

// fileTime encodes t as a FILETIME: 100 ns intervals since 1601.
func fileTime(t time.Time) []byte {
	const epochDelta = 11644473600 // 1601 to 1970 in seconds
	v := (t.Unix()+epochDelta)*1e7 + int64(t.Nanosecond()/100)
	return binary.LittleEndian.AppendUint64(nil, uint64(v)) // #nosec G115 -- positive after 1601
}

// ntowfv2 is NTOWFv2 of [MS-NLMP] 3.3.2: the user name is upper-cased,
// the domain is not.
func ntowfv2(password, user, domain string) []byte {
	h := md4.New() // #nosec G406 -- NTOWFv2 is defined on MD4
	h.Write(utf16le(password))
	return hmacMD5(h.Sum(nil), utf16le(strings.ToUpper(user)+domain))
}

func hmacMD5(key []byte, msg ...[]byte) []byte {
	h := hmac.New(md5.New, key)
	for _, m := range msg {
		h.Write(m)
	}
	return h.Sum(nil)
}

func md5Sum(msg ...[]byte) []byte {
	h := md5.New() // #nosec G401 -- NTLM's key derivation
	for _, m := range msg {
		h.Write(m)
	}
	return h.Sum(nil)
}

// rc4k is RC4K of [MS-NLMP] 6: RC4 of data under key, from a fresh state.
func rc4k(key, data []byte) []byte {
	c, err := rc4.NewCipher(key) // #nosec G405 -- NTLM's key exchange
	if err != nil {
		panic(err) // keys are 16 bytes
	}
	out := make([]byte, len(data))
	c.XORKeyStream(out, data)
	return out
}

// ntlmSession signs and seals messages in one direction and checks and
// unseals them in the other, with extended session security and
// 128-bit keys ([MS-NLMP] 3.4.3, 3.4.4.2, 3.4.5).
type ntlmSession struct {
	keyExch          bool
	sendSign         []byte
	recvSign         []byte
	send, recv       *rc4.Cipher
	sendSeq, recvSeq uint32
}

// The magic constants of SIGNKEY and SEALKEY, [MS-NLMP] 3.4.5.2, 3.4.5.3.
var (
	clientSignMagic = []byte("session key to client-to-server signing key magic constant\x00")
	serverSignMagic = []byte("session key to server-to-client signing key magic constant\x00")
	clientSealMagic = []byte("session key to client-to-server sealing key magic constant\x00")
	serverSealMagic = []byte("session key to server-to-client sealing key magic constant\x00")
)

// newNTLMSession derives the session's keys from the exported session
// key, for the client's side or the server's.
func newNTLMSession(exported []byte, flags uint32, client bool) *ntlmSession {
	s := &ntlmSession{
		keyExch:  flags&ntlmKeyExch != 0,
		sendSign: md5Sum(exported, clientSignMagic),
		recvSign: md5Sum(exported, serverSignMagic),
	}
	sendSeal, recvSeal := md5Sum(exported, clientSealMagic), md5Sum(exported, serverSealMagic)
	if !client {
		s.sendSign, s.recvSign = s.recvSign, s.sendSign
		sendSeal, recvSeal = recvSeal, sendSeal
	}
	s.send, _ = rc4.NewCipher(sendSeal) // #nosec G405 -- NTLM sealing; 16-byte keys never fail
	s.recv, _ = rc4.NewCipher(recvSeal) // #nosec G405 -- as above
	return s
}

// seal is GSS_WrapEx: the 16-byte signature, then the sealed message.
func (s *ntlmSession) seal(msg []byte) []byte {
	out := make([]byte, 16+len(msg))
	s.send.XORKeyStream(out[16:], msg)
	copy(out, s.signature(s.send, s.sendSign, s.sendSeq, msg))
	s.sendSeq++
	return out
}

// unseal is GSS_UnwrapEx, which checks the signature and the sequence
// number.
func (s *ntlmSession) unseal(msg []byte) ([]byte, error) {
	if len(msg) < 16 {
		return nil, errors.New("rdp: sealed message too short")
	}
	data := make([]byte, len(msg)-16)
	s.recv.XORKeyStream(data, msg[16:])
	if !hmac.Equal(s.signature(s.recv, s.recvSign, s.recvSeq, data), msg[:16]) {
		return nil, errors.New("rdp: sealed message has a bad signature")
	}
	s.recvSeq++
	return data, nil
}

// signature is the NTLMSSP_MESSAGE_SIGNATURE of extended session security,
// [MS-NLMP] 2.2.2.9.1 and 3.4.4.2; with key exchange its checksum is
// sealed after the message under the same RC4 state.
func (s *ntlmSession) signature(h *rc4.Cipher, key []byte, seq uint32, msg []byte) []byte {
	sq := binary.LittleEndian.AppendUint32(nil, seq)
	chk := hmacMD5(key, sq, msg)[:8]
	if s.keyExch {
		h.XORKeyStream(chk, chk)
	}
	sig := []byte{1, 0, 0, 0}
	sig = append(sig, chk...)
	return append(sig, sq...)
}
