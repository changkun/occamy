// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package rdp

import (
	"bufio"
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/asn1"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
)

// statusLogonFailure is the NTSTATUS of a wrong password.
const statusLogonFailure = 0xc000006d

func unhex(t testing.TB, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// The example of [MS-NLMP] 4.2.4: user "User", domain "Domain", password
// "Password", server challenge 0123456789abcdef, client challenge aa…,
// time 0, random session key 55…, and the flags and target info of the
// example's CHALLENGE_MESSAGE (4.2.4.3), which has no timestamp.
const (
	vectorFlags           = 0xe28a8233
	vectorServerChallenge = "0123456789abcdef"
)

func vectorChallenge(t testing.TB) []byte {
	return challengeMessage(vectorFlags, unhex(t, vectorServerChallenge), "Server", vectorNames())
}

// vectorNames are the example's AV pairs, the NetBIOS domain and computer
// names.
func vectorNames() []avPair {
	return []avPair{{avNbDomainName, utf16le("Domain")}, {avNbComputerName, utf16le("Server")}}
}

func vectorClient() *ntlmClient {
	return &ntlmClient{
		domain: "Domain", user: "User", password: "Password",
		clientChallenge: bytes.Repeat([]byte{0xaa}, 8),
		exportedKey:     bytes.Repeat([]byte{0x55}, 16),
		now:             func() time.Time { return time.Date(1601, 1, 1, 0, 0, 0, 0, time.UTC) },
	}
}

// authField returns a field of an AUTHENTICATE_MESSAGE by its offset in
// the header: 12 the LM response, 20 the NT response, 28 the domain, 36
// the user, 44 the workstation, 52 the encrypted session key.
func authField(t testing.TB, msg []byte, off int) []byte {
	t.Helper()
	b, err := ntlmField(msg, off)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestNTLMv2Vectors(t *testing.T) {
	// 4.2.4.1.1 NTOWFv2.
	key := ntowfv2("Password", "User", "Domain")
	if want := unhex(t, "0c868a403bfd7a93a3001ef22ef02e3f"); !bytes.Equal(key, want) {
		t.Errorf("NTOWFv2 %x, want %x", key, want)
	}

	c := vectorClient()
	c.negotiateMessage()
	msg, sess, err := c.authenticate(vectorChallenge(t))
	if err != nil {
		t.Fatal(err)
	}
	// 4.2.4.2.1 LMv2 response, sent since the server sent no timestamp.
	if got, want := authField(t, msg, 12), unhex(t, "86c35097ac9cec102554764a57cccc19aaaaaaaaaaaaaaaa"); !bytes.Equal(got, want) {
		t.Errorf("LMv2 response %x, want %x", got, want)
	}
	// 4.2.4.2.2 NTLMv2 response: NTProofStr, then temp with the server's
	// target info unchanged.
	nt := authField(t, msg, 20)
	if want := unhex(t, "68cd0ab851e51c96aabc927bebef6a1c"); !bytes.Equal(nt[:16], want) {
		t.Errorf("NTProofStr %x, want %x", nt[:16], want)
	}
	temp := unhex(t, "0101000000000000"+"0000000000000000"+"aaaaaaaaaaaaaaaa"+"00000000"+
		"02000c00"+hex.EncodeToString(utf16le("Domain"))+"01000c00"+hex.EncodeToString(utf16le("Server"))+"00000000"+"00000000")
	if !bytes.Equal(nt[16:], temp) {
		t.Errorf("temp %x, want %x", nt[16:], temp)
	}
	// 4.2.4.1.2 and 4.2.4.2.3: the session base key, here the key
	// exchange key, encrypts the random session key.
	if got := hmacMD5(key, nt[:16]); !bytes.Equal(got, unhex(t, "8de40ccadbc14a82f15cb0ad0de95ca3")) {
		t.Errorf("session base key %x", got)
	}
	if got, want := authField(t, msg, 52), unhex(t, "c5dad2544fc9799094ce1ce90bc9d03e"); !bytes.Equal(got, want) {
		t.Errorf("encrypted random session key %x, want %x", got, want)
	}
	if got, want := authField(t, msg, 36), utf16le("User"); !bytes.Equal(got, want) {
		t.Errorf("user %x, want %x", got, want)
	}
	if got := binary.LittleEndian.Uint32(msg[60:]); got != vectorFlags {
		t.Errorf("flags %#08x, want %#08x", got, uint32(vectorFlags))
	}
	if !bytes.Equal(msg[ntlmMICOffset:ntlmMICOffset+16], make([]byte, 16)) {
		t.Error("a MIC without the server's timestamp")
	}

	// 4.2.4.4: the session seals "Plaintext" with sequence number 0.
	sealed := sess.seal(utf16le("Plaintext"))
	if want := unhex(t, "54e50165bf1936dc996020c1811b0f06fb5f"); !bytes.Equal(sealed[16:], want) {
		t.Errorf("sealed data %x, want %x", sealed[16:], want)
	}
	if want := unhex(t, "010000007fb38ec5c55d497600000000"); !bytes.Equal(sealed[:16], want) {
		t.Errorf("signature %x, want %x", sealed[:16], want)
	}
	// 4.2.4.1.4 and 4.2.4.1.5: the keys behind the seal.
	if want := unhex(t, "59f600973cc4960a25480a7c196e4c58"); !bytes.Equal(md5Sum(c.exportedKey, clientSealMagic), want) {
		t.Errorf("sealing key, want %x", want)
	}
	if want := unhex(t, "4788dc861b4782f35d43fd98fe1a2d39"); !bytes.Equal(sess.sendSign, want) {
		t.Errorf("signing key %x, want %x", sess.sendSign, want)
	}
}

// With the server's timestamp the client sends a MIC over the three
// messages, says so in MsvAvFlags, and leaves the LMv2 response empty.
func TestNTLMMIC(t *testing.T) {
	s := &ntlmServer{domain: "Domain", user: "User", password: "Password"}
	c := vectorClient()
	ch, err := s.challengeMessage(c.negotiateMessage())
	if err != nil {
		t.Fatal(err)
	}
	msg, _, err := c.authenticate(ch)
	if err != nil {
		t.Fatal(err)
	}
	if got := authField(t, msg, 12); !bytes.Equal(got, make([]byte, 24)) {
		t.Errorf("LMv2 response %x with a timestamp, want zeros", got)
	}
	if _, err := s.verify(msg); err != nil {
		t.Fatalf("verify: %v", err)
	}
	// The MIC covers the whole message, so the server notices a change to
	// a field nothing else covers, such as the version.
	tampered := bytes.Clone(msg)
	tampered[64] ^= 1
	if _, err := s.verify(tampered); err == nil || !strings.Contains(err.Error(), "MIC") {
		t.Errorf("verify of a tampered message: %v, want a bad MIC", err)
	}
}

// A challenge that refuses what CredSSP needs, such as sealing, fails.
func TestNTLMRequiredFlags(t *testing.T) {
	ch := challengeMessage(vectorFlags&^ntlmSeal, unhex(t, vectorServerChallenge), "Server", vectorNames())
	c := vectorClient()
	c.negotiateMessage()
	if _, _, err := c.authenticate(ch); err == nil {
		t.Error("authenticate accepted a challenge without sealing")
	}
}

// ntlmCrasher is a challenge whose MsvAvTimestamp is one byte long. It
// made go-msrpc's NTLM client, which the spike first used, read past the
// pair and panic before authentication.
const ntlmCrasher = "NTLMSSP\x00\x02\x00\x00\x00 \x00008\x00\x00\x0000000000000000000000 \x0000D\x00\x00\x000000000000000000000000\f\x00000000000000\a\x00\x01\x00000000000000"

func TestNTLMHostileChallenge(t *testing.T) {
	c := vectorClient()
	c.negotiateMessage()
	for name, ch := range map[string][]byte{
		"crasher": []byte(ntlmCrasher),
		"short timestamp": challengeMessage(vectorFlags, unhex(t, vectorServerChallenge), "",
			append(vectorNames(), avPair{avTimestamp, []byte{1}})),
		"short flags": challengeMessage(vectorFlags, unhex(t, vectorServerChallenge), "",
			append(vectorNames(), avPair{avFlags, []byte{1, 2}})),
		"no EOL":            vectorChallenge(t)[:len(vectorChallenge(t))-4],
		"short":             []byte("NTLMSSP\x00\x02\x00\x00\x00"),
		"info out of range": func() []byte { b := vectorChallenge(t); b[44] = 0xff; return b }(),
	} {
		if _, _, err := c.authenticate(ch); err == nil {
			t.Errorf("%s: authenticate accepted it", name)
		}
	}
}

// The target info is bounded, so that the NT response, which repeats it,
// fits its 16-bit length, and so is each field of the AUTHENTICATE_MESSAGE.
func TestNTLMLengths(t *testing.T) {
	c := vectorClient()
	c.negotiateMessage()
	near := append(vectorNames(), avPair{0x0009, make([]byte, 65480)}, avPair{avTimestamp, make([]byte, 8)})
	ch := challengeMessage(vectorFlags, unhex(t, vectorServerChallenge), "", near)
	if _, _, err := c.authenticate(ch); err == nil || !strings.Contains(err.Error(), "target info of") {
		t.Errorf("a target info of %d bytes: %v", len(ch), err)
	}
	fits := append(vectorNames(), avPair{0x0009, make([]byte, maxTargetInfo-40)})
	if _, _, err := c.authenticate(challengeMessage(vectorFlags, unhex(t, vectorServerChallenge), "", fits)); err != nil {
		t.Errorf("a target info just within %d bytes: %v", maxTargetInfo, err)
	}
	c.user = strings.Repeat("u", 1<<15)
	if _, _, err := c.authenticate(vectorChallenge(t)); err == nil || !strings.Contains(err.Error(), "NTLM field of") {
		t.Errorf("a user name of %d characters: %v", len(c.user), err)
	}
}

// [MS-NLMP] 3.1.5.1.2: with signing or sealing, a challenge without both
// NetBIOS names is a logon failure. An empty target info parses, as an
// absent one, and fails for the same reason.
func TestNTLMNetBIOSNames(t *testing.T) {
	c := vectorClient()
	c.negotiateMessage()
	for name, pairs := range map[string][]avPair{
		"no target info":      nil,
		"no computer name":    vectorNames()[:1],
		"no domain name":      vectorNames()[1:],
		"only the DNS names":  {{0x0003, utf16le("server.test")}, {0x0004, utf16le("test")}},
		"names and timestamp": append(vectorNames(), avPair{avTimestamp, make([]byte, 8)}),
	} {
		ch := challengeMessage(vectorFlags, unhex(t, vectorServerChallenge), "", pairs)
		if pairs == nil {
			ch = ch[:len(ch)-4] // without the MsvAvEOL either
			binary.LittleEndian.PutUint16(ch[40:], 0)
			binary.LittleEndian.PutUint16(ch[42:], 0)
		}
		_, _, err := c.authenticate(ch)
		if want := name == "names and timestamp"; (err == nil) != want ||
			!want && !strings.Contains(err.Error(), "NetBIOS") {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// FuzzNTLMChallenge feeds the client hostile challenges, after which it
// signs and unseals.
func FuzzNTLMChallenge(f *testing.F) {
	f.Add(vectorChallenge(f))
	f.Add([]byte(ntlmCrasher))
	for _, noTimestamp := range []bool{false, true} {
		s := &ntlmServer{domain: "D", user: "u", password: "p", noTimestamp: noTimestamp}
		ch, err := s.challengeMessage((&ntlmClient{}).negotiateMessage())
		if err != nil {
			f.Fatal(err)
		}
		f.Add(ch)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		c := &ntlmClient{domain: "D", user: "u", password: "p"}
		c.negotiateMessage()
		if _, sess, err := c.authenticate(data); err == nil {
			sess.seal([]byte("x"))
			sess.unseal(data)
		}
	})
}

// TSRequest encodes as [MS-CSSP] 2.2.1 lays it out: explicit context
// tags, optional fields left out.
func TestTSRequestEncoding(t *testing.T) {
	b, err := asn1.Marshal(tsRequest{Version: 6, NegoTokens: []negoToken{{[]byte{0xde, 0xad}}}})
	if err != nil {
		t.Fatal(err)
	}
	// SEQUENCE { [0] INTEGER 6, [1] SEQUENCE OF SEQUENCE { [0] OCTET STRING } }
	want := unhex(t, "3011"+"a003020106"+"a10a"+"3008"+"3006"+"a004"+"0402dead")
	if !bytes.Equal(b, want) {
		t.Errorf("TSRequest %x, want %x", b, want)
	}
	var got tsRequest
	if _, err := asn1.Unmarshal(b, &got); err != nil || got.Version != 6 || !bytes.Equal(got.NegoTokens[0].Token, []byte{0xde, 0xad}) {
		t.Errorf("round trip %+v, %v", got, err)
	}
	// An error code from the server, as Windows encodes STATUS_LOGON_FAILURE:
	// a negative four-byte INTEGER.
	b = unhex(t, "300d"+"a003020106"+"a406"+"0204c000006d")
	if _, err := readTSRequest(bufio.NewReader(bytes.NewReader(b))); !isLogonFailure(err) {
		t.Errorf("readTSRequest: %v, want STATUS_LOGON_FAILURE", err)
	}
}

func isLogonFailure(err error) bool {
	var ce *CredSSPError
	return errors.As(err, &ce) && ce.Code == statusLogonFailure
}

// A server that proves a different public key is refused, before the
// client delegates its credentials.
func TestCredSSPWrongPublicKeyProof(t *testing.T) {
	s := &credSSPServer{
		ntlm: ntlmServer{user: "occamy", password: "s3cret"},
		proof: func(nonce, pub []byte) []byte {
			other := bytes.Clone(pub)
			other[len(other)-1] ^= 1
			return bindingHash(serverClientHashMagic, nonce, other)
		},
	}
	cerr, serr := credSSPPair(t, s, "occamy", "s3cret")
	if cerr == nil || !strings.Contains(cerr.Error(), "different public key") {
		t.Errorf("credSSP with a wrong server proof: %v", cerr)
	}
	if serr == nil {
		t.Error("the client delegated its credentials")
	}
}

// A server that speaks a CredSSP version before 5, whose public key
// binding CVE-2018-0886 broke, is refused before the client answers its
// challenge: the server never sees an AUTHENTICATE_MESSAGE.
func TestCredSSPVersionDowngrade(t *testing.T) {
	for _, v := range []int{2, 3, 4} {
		s := &credSSPServer{ntlm: ntlmServer{user: "occamy", password: "s3cret"}, version: v}
		cerr, serr := credSSPPair(t, s, "occamy", "s3cret")
		if cerr == nil || !strings.Contains(cerr.Error(), "version") {
			t.Errorf("version %d: credSSP returned %v", v, cerr)
		}
		if serr == nil {
			t.Errorf("version %d: the client delegated its credentials", v)
		}
		if s.authenticates != 0 {
			t.Errorf("version %d: the client sent %d AUTHENTICATE_MESSAGEs", v, s.authenticates)
		}
	}
}

// A public key proof whose signature does not match is refused, though
// the proof itself is right: only the signature was changed.
func TestCredSSPTamperedProof(t *testing.T) {
	for _, at := range []int{4, 12} { // the checksum, the sequence number
		s := &credSSPServer{
			ntlm:   ntlmServer{user: "occamy", password: "s3cret"},
			tamper: func(sealed []byte) { sealed[at] ^= 1 },
		}
		cerr, serr := credSSPPair(t, s, "occamy", "s3cret")
		if cerr == nil || !strings.Contains(cerr.Error(), "bad signature") {
			t.Errorf("byte %d of the signature changed: credSSP returned %v", at, cerr)
		}
		if serr == nil || s.creds.Password != nil {
			t.Errorf("byte %d of the signature changed: the client delegated its credentials", at)
		}
	}
}

// unseal checks each message's signature and sequence number.
func TestNTLMUnseal(t *testing.T) {
	key := bytes.Repeat([]byte{0x55}, 16)
	server := newNTLMSession(key, ntlmClientFlags, false)
	client := newNTLMSession(key, ntlmClientFlags, true)
	first := server.seal([]byte("first"))
	second := server.seal([]byte("second"))
	if got, err := client.unseal(first); err != nil || string(got) != "first" {
		t.Fatalf("unseal: %q, %v", got, err)
	}
	if _, err := client.unseal(first); err == nil {
		t.Error("unsealed a replayed message")
	}
	client = newNTLMSession(key, ntlmClientFlags, true)
	client.unseal(first)
	tampered := bytes.Clone(second)
	tampered[5] ^= 1
	if _, err := client.unseal(tampered); err == nil {
		t.Error("unsealed a message with a changed checksum")
	}
}

// The round trip against the test acceptor, with and without the
// server's timestamp, and with a wrong password.
func TestCredSSPRoundTrip(t *testing.T) {
	for _, noTimestamp := range []bool{false, true} {
		s := &credSSPServer{ntlm: ntlmServer{domain: "DOMAIN", user: "occamy", password: "s3cret", noTimestamp: noTimestamp}}
		cerr, serr := credSSPPair(t, s, "occamy", "s3cret")
		if cerr != nil || serr != nil {
			t.Errorf("noTimestamp %v: client %v, server %v", noTimestamp, cerr, serr)
		}
		if string(s.creds.Password) != string(utf16le("s3cret")) {
			t.Errorf("delegated %q", s.creds)
		}
	}
	s := &credSSPServer{ntlm: ntlmServer{domain: "DOMAIN", user: "occamy", password: "s3cret"}}
	if cerr, _ := credSSPPair(t, s, "occamy", "wrong"); !isLogonFailure(cerr) {
		t.Errorf("wrong password: %v, want STATUS_LOGON_FAILURE", cerr)
	}
}

// credSSPPair runs credSSP against s over TLS, with the domain s expects.
func credSSPPair(t *testing.T, s *credSSPServer, user, password string) (clientErr, serverErr error) {
	t.Helper()
	cconn, sconn := net.Pipe()
	cert, err := testCert()
	if err != nil {
		t.Fatal(err)
	}
	served := make(chan error, 1)
	go func() {
		tc := tls.Server(sconn, &tls.Config{Certificates: []tls.Certificate{cert}})
		defer sconn.Close()
		served <- s.accept(bufio.NewReader(tc), tc, cert.Leaf)
	}()
	tc := tls.Client(cconn, &tls.Config{InsecureSkipVerify: true})
	if err := tc.Handshake(); err != nil {
		t.Fatal(err)
	}
	clientErr = credSSP(bufio.NewReader(tc), tc, tc.ConnectionState().PeerCertificates[0], s.ntlm.domain, user, password)
	cconn.Close() // not tc.Close, whose close_notify would wait on the pipe
	return clientErr, <-served
}

// ntlmServer is the server's side of NTLMv2 for the tests. It challenges
// the client, checks the response against the password it knows and the
// MIC, and derives the session's keys for the server's side.
type ntlmServer struct {
	domain, user, password string
	// noTimestamp leaves MsvAvTimestamp out of the challenge, so that the
	// client sends the LMv2 response and no MIC.
	noTimestamp bool

	negotiate, challenge []byte
	serverChallenge      []byte
}

var errLogonFailure = errors.New("logon failure")

func (s *ntlmServer) challengeMessage(negotiate []byte) ([]byte, error) {
	if len(negotiate) < 16 || !bytes.Equal(negotiate[:8], ntlmSignature) || binary.LittleEndian.Uint32(negotiate[8:]) != 1 {
		return nil, errors.New("not an NTLM negotiate")
	}
	s.negotiate = negotiate
	s.serverChallenge = make([]byte, 8)
	rand.Read(s.serverChallenge)
	pairs := []avPair{{2, utf16le("DOMAIN")}, {1, utf16le("SERVER")}}
	if !s.noTimestamp {
		pairs = append(pairs, avPair{avTimestamp, fileTime(time.Now())})
	}
	const targetTypeServer = 0x00020000
	flags := binary.LittleEndian.Uint32(negotiate[12:])&^ntlmOEM | ntlmTargetInfo | targetTypeServer
	s.challenge = challengeMessage(flags, s.serverChallenge, "DOMAIN", pairs)
	return s.challenge, nil
}

// challengeMessage encodes a CHALLENGE_MESSAGE, [MS-NLMP] 2.2.1.2.
func challengeMessage(flags uint32, serverChallenge []byte, target string, pairs []avPair) []byte {
	const header = 56
	name, info := utf16le(target), encodeAVPairs(pairs)
	var w writer
	w.bytes(ntlmSignature)
	w.u32(2)
	w.u16(len(name))
	w.u16(len(name))
	w.u32(header)
	w.u32(flags)
	w.bytes(serverChallenge)
	w.zero(8)
	w.u16(len(info))
	w.u16(len(info))
	w.u32(uint32(header + len(name)))
	w.bytes(ntlmVersionField)
	w.bytes(name)
	w.bytes(info)
	return w.b
}

func (s *ntlmServer) verify(msg []byte) (*ntlmSession, error) {
	if len(msg) < ntlmAuthenticateLen || !bytes.Equal(msg[:8], ntlmSignature) || binary.LittleEndian.Uint32(msg[8:]) != 3 {
		return nil, errors.New("not an NTLM authenticate")
	}
	var fields [6][]byte
	for i := range fields {
		b, err := ntlmField(msg, 12+8*i)
		if err != nil {
			return nil, err
		}
		fields[i] = b
	}
	lm, nt, domain, user, encKey := fields[0], fields[1], fields[2], fields[3], fields[5]
	flags := binary.LittleEndian.Uint32(msg[60:])
	if !strings.EqualFold(fromUTF16(user), s.user) || fromUTF16(domain) != s.domain || len(nt) < 16+28 {
		return nil, errLogonFailure
	}
	key := ntowfv2(s.password, s.user, s.domain)
	proof, temp := nt[:16], nt[16:]
	if !hmac.Equal(hmacMD5(key, s.serverChallenge, temp), proof) {
		return nil, errLogonFailure
	}
	exported := hmacMD5(key, proof)
	if flags&ntlmKeyExch != 0 {
		if len(encKey) != 16 {
			return nil, errors.New("no encrypted session key")
		}
		exported = rc4k(exported, encKey)
	}
	if !s.noTimestamp {
		pairs, err := parseAVPairs(temp[28:])
		if err != nil {
			return nil, err
		}
		mic := false
		for _, p := range pairs {
			if p.id == avFlags && binary.LittleEndian.Uint32(p.value)&avFlagMIC != 0 {
				mic = true
			}
		}
		if !mic || !bytes.Equal(lm, make([]byte, 24)) {
			return nil, errors.New("no MIC flag, or an LMv2 response with a timestamp")
		}
		zeroed := bytes.Clone(msg)
		clear(zeroed[ntlmMICOffset : ntlmMICOffset+16])
		if !hmac.Equal(hmacMD5(exported, s.negotiate, s.challenge, zeroed), msg[ntlmMICOffset:ntlmMICOffset+16]) {
			return nil, errors.New("bad MIC")
		}
	}
	return newNTLMSession(exported, flags, false), nil
}

func fromUTF16(b []byte) string {
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = binary.LittleEndian.Uint16(b[2*i:])
	}
	return string(utf16.Decode(u))
}

// credSSPServer is the server's side of CredSSP for the tests.
type credSSPServer struct {
	ntlm ntlmServer
	// version is the TSRequest version it speaks, credSSPVersion if zero.
	version int
	// proof, if set, replaces the server's public key proof.
	proof func(nonce, pub []byte) []byte
	// tamper, if set, changes the sealed public key proof.
	tamper func(sealed []byte)

	// authenticates counts the AUTHENTICATE_MESSAGEs the client sent.
	authenticates int

	// creds are the credentials the client delegated.
	creds tsPasswordCreds
}

func (s *credSSPServer) accept(r *bufio.Reader, w io.Writer, cert *x509.Certificate) error {
	version := s.version
	if version == 0 {
		version = credSSPVersion
	}
	pub, err := subjectPublicKey(cert)
	if err != nil {
		return err
	}
	req, err := readTSRequest(r)
	if err != nil {
		return err
	}
	if len(req.NegoTokens) != 1 {
		return errors.New("no NTLM negotiate")
	}
	challenge, err := s.ntlm.challengeMessage(req.NegoTokens[0].Token)
	if err != nil {
		return err
	}
	if err := writeTSRequest(w, &tsRequest{Version: version, NegoTokens: []negoToken{{challenge}}}); err != nil {
		return err
	}
	if req, err = readTSRequest(r); err != nil {
		return err
	}
	s.authenticates += len(req.NegoTokens)
	if len(req.NegoTokens) != 1 || len(req.ClientNonce) != 32 {
		return fmt.Errorf("authenticate request %+v", req)
	}
	sess, err := s.ntlm.verify(req.NegoTokens[0].Token)
	if err != nil {
		writeTSRequest(w, &tsRequest{Version: version, ErrorCode: statusLogonFailure})
		return err
	}
	got, err := sess.unseal(req.PubKeyAuth)
	if err != nil {
		return err
	}
	if !bytes.Equal(got, bindingHash(clientServerHashMagic, req.ClientNonce, pub)) {
		return errors.New("client bound a different public key")
	}
	proof := bindingHash(serverClientHashMagic, req.ClientNonce, pub)
	if s.proof != nil {
		proof = s.proof(req.ClientNonce, pub)
	}
	sealed := sess.seal(proof)
	if s.tamper != nil {
		s.tamper(sealed)
	}
	if err := writeTSRequest(w, &tsRequest{Version: version, PubKeyAuth: sealed}); err != nil {
		return err
	}
	if req, err = readTSRequest(r); err != nil {
		return err
	}
	b, err := sess.unseal(req.AuthInfo)
	if err != nil {
		return err
	}
	var ts tsCredentials
	if _, err := asn1.Unmarshal(b, &ts); err != nil {
		return err
	}
	if ts.CredType != 1 {
		return fmt.Errorf("credential type %d", ts.CredType)
	}
	_, err = asn1.Unmarshal(ts.Credentials, &s.creds)
	return err
}
