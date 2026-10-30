// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package rdp

import (
	"bufio"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/md5"
	"crypto/x509"
	"encoding/asn1"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/oiweiwei/go-msrpc/ssp/credential"
	"github.com/oiweiwei/go-msrpc/ssp/ntlm"
)

// statusLogonFailure is the NTSTATUS of a wrong password.
const statusLogonFailure = 0xc000006d

func unhex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// The NTLMv2 computations that CredSSP relies on, against the worked
// example of [MS-NLMP] 4.2.4: user "User", domain "Domain", password
// "Password", server challenge 0123456789abcdef, client challenge aa…,
// random session key 55….
func TestNTLMv2Vectors(t *testing.T) {
	ctx := context.Background()
	cred := credential.NewFromPassword(`Domain\User`, "Password")
	v2 := &ntlm.V2{Config: &ntlm.Config{}}

	// 4.2.4.1.1 NTOWFv2.
	key, err := v2.NTOWF(ctx, cred)
	if err != nil {
		t.Fatal(err)
	}
	if want := unhex(t, "0c868a403bfd7a93a3001ef22ef02e3f"); !bytes.Equal(key, want) {
		t.Errorf("NTOWFv2 %x, want %x", key, want)
	}

	serverChallenge := unhex(t, "0123456789abcdef")
	clientChallenge := bytes.Repeat([]byte{0xaa}, 8)
	cm := &ntlm.ChallengeMessage{
		Negotiate: ntlm.NegotiateKeyExchange | ntlm.Negotiate56 | ntlm.Negotiate128 |
			ntlm.NegotiateVersion | ntlm.NegotiateTargetInfo | ntlm.NegotiateExtendedSessionSecurity |
			ntlm.TargetTypeServer | ntlm.NegotiateAlwaysSign | ntlm.NegotiateNTLM |
			ntlm.NegotiateSeal | ntlm.NegotiateSign | ntlm.NegotiateOEM | ntlm.NegotiateUnicode,
		TargetInfo: ntlm.AttrValues{
			ntlm.AttrNetBIOSComputerName: &ntlm.Value{NetBIOSComputerName: "Server"},
			ntlm.AttrNetBIOSDomainName:   &ntlm.Value{NetBIOSDomainName: "Domain"},
		},
		ServerChallenge: serverChallenge,
	}
	resp, err := v2.ChallengeResponse(ctx, cred, cm, clientChallenge)
	if err != nil {
		t.Fatal(err)
	}

	// 4.2.4.2.1 LMv2 response, when the server sends no timestamp.
	if want := unhex(t, "86c35097ac9cec102554764a57cccc19aaaaaaaaaaaaaaaa"); !bytes.Equal(resp.LM, want) {
		t.Errorf("LMv2 response %x, want %x", resp.LM, want)
	}

	// 4.2.4.2.2: go-msrpc adds target name and channel binding AV pairs to
	// the client challenge (temp), so NTProofStr differs from the example's;
	// check it and the session base key against the formulas of 3.3.2
	// instead: NTProofStr = HMAC_MD5(NTOWFv2, server challenge ‖ temp).
	mac := func(key []byte, msg ...[]byte) []byte {
		h := hmac.New(md5.New, key)
		for _, m := range msg {
			h.Write(m)
		}
		return h.Sum(nil)
	}
	proof := mac(key, serverChallenge, resp.Tmp)
	if !bytes.Equal(resp.NT[:16], proof) || !bytes.Equal(resp.NT[16:], resp.Tmp) {
		t.Errorf("NTLMv2 response %x, want NTProofStr %x then temp", resp.NT, proof)
	}
	if !bytes.Contains(resp.Tmp, clientChallenge) {
		t.Error("temp lacks the client challenge")
	}
	if want := mac(key, proof); !bytes.Equal(resp.SessionBaseKey, want) {
		t.Errorf("session base key %x, want %x", resp.SessionBaseKey, want)
	}

	// 4.2.4.2.3: the encrypted random session key, RC4 of the random
	// session key under the example's key exchange key.
	enc, err := ntlm.RC4K(ctx, unhex(t, "8de40ccadbc14a82f15cb0ad0de95ca3"), bytes.Repeat([]byte{0x55}, 16))
	if err != nil {
		t.Fatal(err)
	}
	if want := unhex(t, "c5dad2544fc9799094ce1ce90bc9d03e"); !bytes.Equal(enc, want) {
		t.Errorf("encrypted random session key %x, want %x", enc, want)
	}
}

// Sealing as CredSSP's GSS_WrapEx does, against [MS-NLMP] 4.2.4.4: the
// keys derive from the exported session key 55… and seal "Plaintext"
// with sequence number 0.
func TestNTLMSealVector(t *testing.T) {
	ctx := context.Background()
	exported := bytes.Repeat([]byte{0x55}, 16)
	md := func(b ...[]byte) []byte {
		h := md5.New()
		for _, p := range b {
			h.Write(p)
		}
		return h.Sum(nil)
	}
	sealKey := md(exported, []byte("session key to client-to-server sealing key magic constant\x00"))
	signKey := md(exported, []byte("session key to client-to-server signing key magic constant\x00"))
	if want := unhex(t, "59f600973cc4960a25480a7c196e4c58"); !bytes.Equal(sealKey, want) {
		t.Errorf("sealing key %x, want %x", sealKey, want)
	}
	if want := unhex(t, "4788dc861b4782f35d43fd98fe1a2d39"); !bytes.Equal(signKey, want) {
		t.Errorf("signing key %x, want %x", signKey, want)
	}
	c, err := ntlm.NewCipher(ctx, sealKey, ntlm.HMACMD5(signKey))
	if err != nil {
		t.Fatal(err)
	}
	data := utf16le("Plaintext")
	chk, err := c.Checksum(0, data)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.XORKeyStream(data); err != nil {
		t.Fatal(err)
	}
	if err := c.XORKeyStream(chk); err != nil {
		t.Fatal(err)
	}
	if want := unhex(t, "54e50165bf1936dc996020c1811b0f06fb5f"); !bytes.Equal(data, want) {
		t.Errorf("sealed data %x, want %x", data, want)
	}
	sig := append(append([]byte{1, 0, 0, 0}, chk...), 0, 0, 0, 0)
	if want := unhex(t, "010000007fb38ec5c55d497600000000"); !bytes.Equal(sig, want) {
		t.Errorf("signature %x, want %x", sig, want)
	}
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

// acceptCredSSP is the server's side of CredSSP for the tests, on
// go-msrpc's NTLM acceptor. It returns the delegated credentials.
func acceptCredSSP(r *bufio.Reader, w io.Writer, cert *x509.Certificate, domain, user, password string) (tsPasswordCreds, error) {
	ctx := context.Background()
	var creds tsPasswordCreds
	pub, err := subjectPublicKey(cert)
	if err != nil {
		return creds, err
	}
	name := user
	if domain != "" {
		name = domain + `\` + user
	}
	db := credential.NewLocalDatabase()
	db.Add(credential.NewFromPassword(name, password))
	cfg := ntlm.NewConfig()
	cfg.IsServer = true
	cfg.Integrity, cfg.Confidentiality = true, true
	cfg.NetBIOSComputerName = "SERVER"
	cfg.Verifier = &ntlm.LocalVerifier{Config: cfg, Database: db}
	a := &ntlm.Authentifier{Config: cfg}

	req, err := readTSRequest(r)
	if err != nil {
		return creds, err
	}
	if len(req.NegoTokens) != 1 {
		return creds, errors.New("no NTLM negotiate")
	}
	challenge, err := a.Challenge(ctx, req.NegoTokens[0].Token)
	if err != nil {
		return creds, err
	}
	if err := writeTSRequest(w, &tsRequest{Version: credSSPVersion, NegoTokens: []negoToken{{challenge}}}); err != nil {
		return creds, err
	}
	if req, err = readTSRequest(r); err != nil {
		return creds, err
	}
	if len(req.NegoTokens) != 1 || len(req.ClientNonce) != 32 {
		return creds, fmt.Errorf("authenticate request %+v", req)
	}
	if err := a.VerifyAuthenticate(ctx, req.NegoTokens[0].Token); err != nil {
		writeTSRequest(w, &tsRequest{Version: credSSPVersion, ErrorCode: statusLogonFailure})
		return creds, err
	}
	got, err := unseal(ctx, a, req.PubKeyAuth)
	if err != nil {
		return creds, err
	}
	if !bytes.Equal(got, bindingHash(clientServerHashMagic, req.ClientNonce, pub)) {
		return creds, errors.New("client bound a different public key")
	}
	proof, err := seal(ctx, a, bindingHash(serverClientHashMagic, req.ClientNonce, pub))
	if err != nil {
		return creds, err
	}
	if err := writeTSRequest(w, &tsRequest{Version: credSSPVersion, PubKeyAuth: proof}); err != nil {
		return creds, err
	}
	if req, err = readTSRequest(r); err != nil {
		return creds, err
	}
	b, err := unseal(ctx, a, req.AuthInfo)
	if err != nil {
		return creds, err
	}
	var ts tsCredentials
	if _, err := asn1.Unmarshal(b, &ts); err != nil {
		return creds, err
	}
	if ts.CredType != 1 {
		return creds, fmt.Errorf("credential type %d", ts.CredType)
	}
	_, err = asn1.Unmarshal(ts.Credentials, &creds)
	return creds, err
}
