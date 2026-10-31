// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package rdptest

import (
	"bytes"
	"crypto/hmac"
	"crypto/md5" // #nosec G501 -- NTLM is defined on MD5
	"crypto/rand"
	"crypto/rc4" // #nosec G503 -- NTLM seals with RC4
	"crypto/sha256"
	"crypto/x509"
	"encoding/asn1"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf16"

	"golang.org/x/crypto/md4" //nolint:gosec,staticcheck // NTOWFv2 is defined on MD4
)

// The server's side of CredSSP ([MS-CSSP]) with NTLMv2 ([MS-NLMP]), for
// an Account the server knows: it challenges the client with both
// NetBIOS names and a timestamp, checks the NTLMv2 response and the MIC
// against the account's password, checks the public key the client
// binds, proves its own, and takes the credentials the client delegates.
// It is written from the specifications, apart from internal/rdp's
// client, so that each checks the other.

// Account is an account the server takes through NLA.
type Account struct {
	// Domain, if not empty, is the domain the client must name; the
	// user name is compared without case, as Windows compares it.
	Domain, Username, Password string
}

// StatusLogonFailure is the NTSTATUS a server answers a wrong password
// with.
const StatusLogonFailure = 0xc000006d

// ErrLogonFailure is returned by Accept when the client's NTLMv2
// response does not match the account's password, after the server has
// answered StatusLogonFailure.
var ErrLogonFailure = errors.New("rdptest: logon failure")

// NTLM negotiate flags, [MS-NLMP] 2.2.2.5.
const (
	ntlmOEM        = 0x00000002
	ntlmTargetInfo = 0x00800000
	ntlmTargetSrv  = 0x00020000
	ntlmKeyExch    = 0x40000000
	ntlmSealFlag   = 0x00000020
)

// NTLMSeal is the NTLM negotiate flag for sealing, which a test may take
// out of the challenge (Server.NTLMFlags).
const NTLMSeal = ntlmSealFlag

// AV pair IDs of [MS-NLMP] 2.2.2.1.
const (
	avEOL            = 0
	avNbComputerName = 1
	avNbDomainName   = 2
	avDNSComputer    = 3
	avFlags          = 6
	avTimestamp      = 7
	avFlagMIC        = 0x2
)

var ntlmSignature = []byte("NTLMSSP\x00")

// tsRequest is [MS-CSSP] 2.2.1 TSRequest.
type tsRequest struct {
	Version     int         `asn1:"explicit,tag:0"`
	NegoTokens  []negoToken `asn1:"optional,explicit,tag:1"`
	AuthInfo    []byte      `asn1:"optional,explicit,tag:2"`
	PubKeyAuth  []byte      `asn1:"optional,explicit,tag:3"`
	ErrorCode   int64       `asn1:"optional,explicit,tag:4"`
	ClientNonce []byte      `asn1:"optional,explicit,tag:5"`
}

type negoToken struct {
	Token []byte `asn1:"explicit,tag:0"`
}

type tsCredentials struct {
	CredType    int    `asn1:"explicit,tag:0"`
	Credentials []byte `asn1:"explicit,tag:1"`
}

type tsPasswordCreds struct {
	DomainName []byte `asn1:"explicit,tag:0"`
	UserName   []byte `asn1:"explicit,tag:1"`
	Password   []byte `asn1:"explicit,tag:2"`
}

// errStalled is returned by Accept when the server stopped answering in
// CredSSP (Server.StallNLA) and the client closed the connection.
var errStalled = errors.New("rdptest: CredSSP stalled, and the client hung up")

// credSSP runs the server's side of CredSSP for s.Account over c's TLS
// connection, whose certificate is cert.
func (s *Server) credSSP(c *Conn, cert *x509.Certificate) error {
	version := s.CredSSPVersion
	if version == 0 {
		version = 6
	}
	pub, err := subjectPublicKey(cert)
	if err != nil {
		return err
	}
	req, err := c.readTSRequest()
	if err != nil {
		return err
	}
	c.TSRequest, err = asn1.Marshal(*req)
	if err != nil {
		return err
	}
	if s.StallNLA == 1 {
		return c.stall()
	}
	if len(req.NegoTokens) != 1 {
		return errors.New("rdptest: CredSSP without an NTLM negotiate")
	}
	n := &ntlmServer{account: *s.Account, flags: s.NTLMFlags}
	challenge, err := n.challengeMessage(req.NegoTokens[0].Token)
	if err != nil {
		return err
	}
	if err := c.writeTSRequest(&tsRequest{Version: version, NegoTokens: []negoToken{{challenge}}}); err != nil {
		return err
	}

	if req, err = c.readTSRequest(); err != nil {
		return err
	}
	c.Authenticates += len(req.NegoTokens)
	if s.StallNLA == 2 {
		return c.stall()
	}
	if len(req.NegoTokens) != 1 {
		return errors.New("rdptest: CredSSP without an NTLM authenticate")
	}
	sess, err := n.verify(req.NegoTokens[0].Token)
	if errors.Is(err, ErrLogonFailure) {
		if werr := c.writeTSRequest(&tsRequest{Version: version, ErrorCode: StatusLogonFailure}); werr != nil {
			return werr
		}
		return err
	}
	if err != nil {
		return err
	}
	// [MS-CSSP] 3.1.5: from version 5 the client binds the hash of the
	// public key with its nonce.
	if len(req.ClientNonce) != 32 {
		return fmt.Errorf("rdptest: CredSSP client nonce of %d bytes", len(req.ClientNonce))
	}
	got, err := sess.unseal(req.PubKeyAuth)
	if err != nil {
		return err
	}
	if !hmac.Equal(got, bindingHash("CredSSP Client-To-Server Binding Hash\x00", req.ClientNonce, pub)) {
		return errors.New("rdptest: the client bound another public key")
	}
	proved := pub
	if s.WrongProof {
		proved = bytes.Clone(pub)
		proved[len(proved)-1] ^= 1
	}
	proof := sess.seal(bindingHash("CredSSP Server-To-Client Binding Hash\x00", req.ClientNonce, proved))
	if err := c.writeTSRequest(&tsRequest{Version: version, PubKeyAuth: proof}); err != nil {
		return err
	}

	if req, err = c.readTSRequest(); err != nil {
		return err
	}
	b, err := sess.unseal(req.AuthInfo)
	if err != nil {
		return err
	}
	var ts tsCredentials
	if _, err := asn1.Unmarshal(b, &ts); err != nil {
		return fmt.Errorf("rdptest: TSCredentials: %w", err)
	}
	var pw tsPasswordCreds
	if _, err := asn1.Unmarshal(ts.Credentials, &pw); ts.CredType != 1 || err != nil {
		return fmt.Errorf("rdptest: TSPasswordCreds of type %d: %w", ts.CredType, err)
	}
	c.Delegated = Credentials{
		Domain:   fromUTF16(pw.DomainName),
		Username: fromUTF16(pw.UserName),
		Password: fromUTF16(pw.Password),
	}
	return nil
}

// stall reads until the client closes the connection.
func (c *Conn) stall() error {
	_, _ = io.Copy(io.Discard, c.r)
	return errStalled
}

func (c *Conn) readTSRequest() (*tsRequest, error) {
	b, err := readTSRequest(c.r)
	if err != nil {
		return nil, err
	}
	var req tsRequest
	if _, err := asn1.Unmarshal(b, &req); err != nil {
		return nil, fmt.Errorf("rdptest: TSRequest: %w", err)
	}
	return &req, nil
}

func (c *Conn) writeTSRequest(req *tsRequest) error {
	b, err := asn1.Marshal(*req)
	if err != nil {
		return err
	}
	return c.Write(b)
}

// bindingHash is the public key hash of [MS-CSSP] 3.1.5 for versions 5
// and later.
func bindingHash(magic string, nonce, pub []byte) []byte {
	h := sha256.New()
	h.Write([]byte(magic))
	h.Write(nonce)
	h.Write(pub)
	return h.Sum(nil)
}

// subjectPublicKey returns the bits of a certificate's SubjectPublicKey.
func subjectPublicKey(cert *x509.Certificate) ([]byte, error) {
	var spki struct {
		Algorithm asn1.RawValue
		PublicKey asn1.BitString
	}
	if _, err := asn1.Unmarshal(cert.RawSubjectPublicKeyInfo, &spki); err != nil {
		return nil, err
	}
	return spki.PublicKey.Bytes, nil
}

// ntlmServer is the server's side of NTLMv2.
type ntlmServer struct {
	account Account
	flags   uint32 // if not 0, the challenge's flags

	negotiate, challenge, serverChallenge []byte
}

// challengeMessage answers a NEGOTIATE_MESSAGE with a CHALLENGE_MESSAGE,
// [MS-NLMP] 2.2.1.2, whose target info holds both NetBIOS names, the
// server's DNS name and a timestamp.
func (n *ntlmServer) challengeMessage(negotiate []byte) ([]byte, error) {
	if len(negotiate) < 16 || !bytes.Equal(negotiate[:8], ntlmSignature) || binary.LittleEndian.Uint32(negotiate[8:]) != 1 {
		return nil, errors.New("rdptest: not an NTLM negotiate")
	}
	n.negotiate = negotiate
	n.serverChallenge = make([]byte, 8)
	if _, err := rand.Read(n.serverChallenge); err != nil {
		return nil, err
	}
	flags := n.flags
	if flags == 0 {
		flags = binary.LittleEndian.Uint32(negotiate[12:])&^ntlmOEM | ntlmTargetInfo | ntlmTargetSrv
	}
	domain := n.account.Domain
	if domain == "" {
		domain = "RDPTEST"
	}
	var info w
	for _, p := range []struct {
		id    uint16
		value []byte
	}{
		{avNbDomainName, utf16le(strings.ToUpper(domain))},
		{avNbComputerName, utf16le("SERVER")},
		{avDNSComputer, utf16le("rdp.test")},
		{avTimestamp, fileTime(time.Now())},
	} {
		info.u16(p.id)
		info.u16(n16(len(p.value)))
		info.bytes(p.value)
	}
	info.u16(avEOL)
	info.u16(0)

	const header = 56
	name := utf16le(strings.ToUpper(domain))
	var b w
	b.bytes(ntlmSignature)
	b.u32(2)
	b.u16(n16(len(name)))
	b.u16(n16(len(name)))
	b.u32(header)
	b.u32(flags)
	b.bytes(n.serverChallenge)
	b.bytes(make([]byte, 8))
	b.u16(n16(len(info)))
	b.u16(n16(len(info)))
	b.u32(uint32(header + len(name))) // #nosec G115 -- short
	b.bytes([]byte{10, 0, 0x61, 0x4a, 0, 0, 0, 0x0f})
	b.bytes(name)
	b.bytes(info)
	n.challenge = b
	return b, nil
}

// authField returns the payload of the field at off of an
// AUTHENTICATE_MESSAGE.
func authField(msg []byte, off int) ([]byte, error) {
	if off+8 > len(msg) {
		return nil, errors.New("rdptest: short NTLM authenticate")
	}
	l := int(binary.LittleEndian.Uint16(msg[off:]))
	at := int(binary.LittleEndian.Uint32(msg[off+4:]))
	if at < 0 || at+l > len(msg) {
		return nil, errors.New("rdptest: NTLM field out of range")
	}
	return msg[at : at+l], nil
}

// verify checks an AUTHENTICATE_MESSAGE, [MS-NLMP] 3.2.5.1.2: the user
// and domain, the NTLMv2 response against the account's password, and
// the MIC that the timestamp asks for. It returns the session that seals
// with the keys the client established.
func (n *ntlmServer) verify(msg []byte) (*ntlmSession, error) {
	const micOffset = 72
	if len(msg) < 88 || !bytes.Equal(msg[:8], ntlmSignature) || binary.LittleEndian.Uint32(msg[8:]) != 3 {
		return nil, errors.New("rdptest: not an NTLM authenticate")
	}
	var f [6][]byte
	for i := range f {
		b, err := authField(msg, 12+8*i)
		if err != nil {
			return nil, err
		}
		f[i] = b
	}
	nt, domain, user, encKey := f[1], fromUTF16(f[2]), fromUTF16(f[3]), f[5]
	flags := binary.LittleEndian.Uint32(msg[60:])
	if !strings.EqualFold(user, n.account.Username) ||
		n.account.Domain != "" && !strings.EqualFold(domain, n.account.Domain) || len(nt) < 16+28 {
		return nil, ErrLogonFailure
	}
	key := ntowfv2(n.account.Password, user, domain)
	proof, temp := nt[:16], nt[16:]
	if !hmac.Equal(hmacMD5(key, n.serverChallenge, temp), proof) {
		return nil, ErrLogonFailure
	}
	exported := hmacMD5(key, proof)
	if flags&ntlmKeyExch != 0 {
		if len(encKey) != 16 {
			return nil, errors.New("rdptest: no encrypted session key")
		}
		exported = rc4k(exported, encKey)
	}
	// The timestamp the server sent makes the client send a MIC, which
	// MsvAvFlags in the response announces.
	mic := false
	for info := temp[28:]; len(info) >= 4; {
		id, l := binary.LittleEndian.Uint16(info), int(binary.LittleEndian.Uint16(info[2:]))
		if id == avEOL || 4+l > len(info) {
			break
		}
		if id == avFlags && l == 4 && binary.LittleEndian.Uint32(info[4:])&avFlagMIC != 0 {
			mic = true
		}
		info = info[4+l:]
	}
	if !mic {
		return nil, errors.New("rdptest: no MIC in answer to the timestamp")
	}
	zeroed := bytes.Clone(msg)
	clear(zeroed[micOffset : micOffset+16])
	if !hmac.Equal(hmacMD5(exported, n.negotiate, n.challenge, zeroed), msg[micOffset:micOffset+16]) {
		return nil, errors.New("rdptest: bad MIC")
	}
	return newNTLMSession(exported, flags), nil
}

// ntowfv2 is NTOWFv2 of [MS-NLMP] 3.3.2.
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

func md5Of(msg ...[]byte) []byte {
	h := md5.New() // #nosec G401 -- NTLM's key derivation
	for _, m := range msg {
		h.Write(m)
	}
	return h.Sum(nil)
}

func rc4k(key, data []byte) []byte {
	c, err := rc4.NewCipher(key) // #nosec G405 -- NTLM's key exchange
	if err != nil {
		panic(err) // 16-byte keys
	}
	out := make([]byte, len(data))
	c.XORKeyStream(out, data)
	return out
}

// ntlmSession seals for the client and unseals what the client sealed,
// with extended session security, [MS-NLMP] 3.4.
type ntlmSession struct {
	keyExch            bool
	sendSign, recvSign []byte
	send, recv         *rc4.Cipher
	sendSeq, recvSeq   uint32
}

// newNTLMSession derives the server's keys: it sends with the
// server-to-client keys and receives with the client-to-server ones.
func newNTLMSession(exported []byte, flags uint32) *ntlmSession {
	s := &ntlmSession{
		keyExch:  flags&ntlmKeyExch != 0,
		sendSign: md5Of(exported, []byte("session key to server-to-client signing key magic constant\x00")),
		recvSign: md5Of(exported, []byte("session key to client-to-server signing key magic constant\x00")),
	}
	s.send, _ = rc4.NewCipher(md5Of(exported, []byte("session key to server-to-client sealing key magic constant\x00"))) // #nosec G405 -- NTLM sealing
	s.recv, _ = rc4.NewCipher(md5Of(exported, []byte("session key to client-to-server sealing key magic constant\x00"))) // #nosec G405 -- NTLM sealing
	return s
}

func (s *ntlmSession) seal(msg []byte) []byte {
	out := make([]byte, 16+len(msg))
	s.send.XORKeyStream(out[16:], msg)
	copy(out, s.signature(s.send, s.sendSign, s.sendSeq, msg))
	s.sendSeq++
	return out
}

func (s *ntlmSession) unseal(msg []byte) ([]byte, error) {
	if len(msg) < 16 {
		return nil, errors.New("rdptest: short sealed message")
	}
	data := make([]byte, len(msg)-16)
	s.recv.XORKeyStream(data, msg[16:])
	if !hmac.Equal(s.signature(s.recv, s.recvSign, s.recvSeq, data), msg[:16]) {
		return nil, errors.New("rdptest: sealed message with a bad signature")
	}
	s.recvSeq++
	return data, nil
}

// signature is NTLMSSP_MESSAGE_SIGNATURE with extended session security.
func (s *ntlmSession) signature(h *rc4.Cipher, key []byte, seq uint32, msg []byte) []byte {
	sq := binary.LittleEndian.AppendUint32(nil, seq)
	chk := hmacMD5(key, sq, msg)[:8]
	if s.keyExch {
		h.XORKeyStream(chk, chk)
	}
	return append(append([]byte{1, 0, 0, 0}, chk...), sq...)
}

func utf16le(s string) []byte {
	var b w
	for _, u := range utf16.Encode([]rune(s)) {
		b.u16(u)
	}
	return b
}

func fromUTF16(b []byte) string {
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = binary.LittleEndian.Uint16(b[2*i:])
	}
	return string(utf16.Decode(u))
}

// fileTime encodes t as a FILETIME.
func fileTime(t time.Time) []byte {
	v := (t.Unix()+11644473600)*1e7 + int64(t.Nanosecond()/100)
	return binary.LittleEndian.AppendUint64(nil, uint64(v)) // #nosec G115 -- after 1601
}

// earlyUserAuthResult is the Early User Authorization Result PDU,
// [MS-RDPBCGR] 2.2.10.2, that follows CredSSP under PROTOCOL_HYBRID_EX:
// AUTHZ_SUCCESS.
func earlyUserAuthResult() []byte { return []byte{0, 0, 0, 0} }
