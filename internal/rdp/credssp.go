// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package rdp

import (
	"bufio"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/asn1"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// CredSSP ([MS-CSSP]) with NTLM ([MS-NLMP], ntlm.go) as its only
// mechanism: no SPNEGO wrapping and no Kerberos, which [MS-CSSP] 3.1.5
// allows. This file frames NTLM's messages in TSRequests and binds the
// server's TLS public key.

const (
	// credSSPVersion is the TSRequest version the client speaks: 6 adds
	// the error code to version 5.
	credSSPVersion = 6
	// minCredSSPVersion is the oldest version the client accepts. Version
	// 5 binds the public key with the client's nonce and a hash
	// ([MS-CSSP] 3.1.5.1.1); the binding of earlier versions is the one
	// CVE-2018-0886 broke, and since the 2018 fix Windows clients refuse
	// servers that offer only it.
	minCredSSPVersion = 5
)

// tsRequest is [MS-CSSP] 2.2.1 TSRequest.
type tsRequest struct {
	Version     int         `asn1:"explicit,tag:0"`
	NegoTokens  []negoToken `asn1:"optional,explicit,tag:1"`
	AuthInfo    []byte      `asn1:"optional,explicit,tag:2"`
	PubKeyAuth  []byte      `asn1:"optional,explicit,tag:3"`
	ErrorCode   int64       `asn1:"optional,explicit,tag:4"`
	ClientNonce []byte      `asn1:"optional,explicit,tag:5"`
}

// negoToken is an element of [MS-CSSP] 2.2.1.1 NegoData.
type negoToken struct {
	Token []byte `asn1:"explicit,tag:0"`
}

// tsCredentials is [MS-CSSP] 2.2.1.2 TSCredentials.
type tsCredentials struct {
	CredType    int    `asn1:"explicit,tag:0"`
	Credentials []byte `asn1:"explicit,tag:1"`
}

// tsPasswordCreds is [MS-CSSP] 2.2.1.2.1 TSPasswordCreds, in UTF-16LE.
type tsPasswordCreds struct {
	DomainName []byte `asn1:"explicit,tag:0"`
	UserName   []byte `asn1:"explicit,tag:1"`
	Password   []byte `asn1:"explicit,tag:2"`
}

// Magic strings of the public key binding, [MS-CSSP] 3.1.5.
var (
	clientServerHashMagic = []byte("CredSSP Client-To-Server Binding Hash\x00")
	serverClientHashMagic = []byte("CredSSP Server-To-Client Binding Hash\x00")
)

// CredSSPError is an error the server reported in a TSRequest, an
// NTSTATUS such as 0xc000006d (STATUS_LOGON_FAILURE).
type CredSSPError struct {
	Code uint32
}

func (e *CredSSPError) Error() string {
	return fmt.Sprintf("rdp: CredSSP failed with NTSTATUS %#08x", e.Code)
}

// credSSP authenticates over an established TLS connection, read through
// r and written to w, whose server certificate is cert.
func credSSP(r *bufio.Reader, w io.Writer, cert *x509.Certificate, domain, user, password string) error {
	pub, err := subjectPublicKey(cert)
	if err != nil {
		return err
	}
	nc := &ntlmClient{domain: domain, user: user, password: password}
	if err := writeTSRequest(w, &tsRequest{Version: credSSPVersion, NegoTokens: []negoToken{{nc.negotiateMessage()}}}); err != nil {
		return err
	}
	resp, err := readTSRequest(r)
	if err != nil {
		return err
	}
	if resp.Version < minCredSSPVersion {
		return fmt.Errorf("rdp: the server speaks CredSSP version %d; the client needs %d or later", resp.Version, minCredSSPVersion)
	}
	if len(resp.NegoTokens) == 0 {
		return errors.New("rdp: CredSSP response without NTLM challenge")
	}
	version := min(resp.Version, credSSPVersion)
	authenticate, sess, err := nc.authenticate(resp.NegoTokens[0].Token)
	if err != nil {
		return err
	}
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	err = writeTSRequest(w, &tsRequest{
		Version:     version,
		NegoTokens:  []negoToken{{authenticate}},
		PubKeyAuth:  sess.seal(bindingHash(clientServerHashMagic, nonce, pub)),
		ClientNonce: nonce,
	})
	if err != nil {
		return err
	}
	resp, err = readTSRequest(r)
	if err != nil {
		return err
	}
	got, err := sess.unseal(resp.PubKeyAuth)
	if err != nil {
		return fmt.Errorf("rdp: CredSSP server public key: %w", err)
	}
	if !hmac.Equal(got, bindingHash(serverClientHashMagic, nonce, pub)) {
		return errors.New("rdp: CredSSP server proved a different public key")
	}

	creds, err := asn1.Marshal(tsPasswordCreds{utf16le(domain), utf16le(user), utf16le(password)})
	if err != nil {
		return err
	}
	ts, err := asn1.Marshal(tsCredentials{CredType: 1, Credentials: creds})
	if err != nil {
		return err
	}
	return writeTSRequest(w, &tsRequest{Version: version, AuthInfo: sess.seal(ts)})
}

// bindingHash is the SHA-256 public key hash of [MS-CSSP] 3.1.5 for
// versions 5 and later.
func bindingHash(magic, nonce, pub []byte) []byte {
	h := sha256.New()
	h.Write(magic)
	h.Write(nonce)
	h.Write(pub)
	return h.Sum(nil)
}

// subjectPublicKey returns the contents of the certificate's
// SubjectPublicKey bit string, which CredSSP binds ([MS-CSSP] 3.1.5).
func subjectPublicKey(cert *x509.Certificate) ([]byte, error) {
	var spki struct {
		Algorithm asn1.RawValue
		PublicKey asn1.BitString
	}
	if _, err := asn1.Unmarshal(cert.RawSubjectPublicKeyInfo, &spki); err != nil {
		return nil, fmt.Errorf("rdp: server certificate public key: %w", err)
	}
	return spki.PublicKey.Bytes, nil
}

func writeTSRequest(w io.Writer, req *tsRequest) error {
	b, err := asn1.Marshal(*req)
	if err != nil {
		return err
	}
	_, err = w.Write(b)
	return err
}

// maxTSRequest bounds a TSRequest from the server; NTLM messages and
// certificates fit in a few KiB.
const maxTSRequest = 64 << 10

// readTSRequest reads one DER TSRequest and reports the error code it
// may carry.
func readTSRequest(r *bufio.Reader) (*tsRequest, error) {
	hdr := make([]byte, 2, 4)
	if _, err := io.ReadFull(r, hdr); err != nil {
		return nil, fmt.Errorf("rdp: CredSSP: %w", err)
	}
	if hdr[0] != 0x30 {
		return nil, fmt.Errorf("rdp: CredSSP: not a TSRequest: %#02x", hdr[0])
	}
	n := int(hdr[1])
	if n&0x80 != 0 {
		k := n & 0x7f
		if k == 0 || k > 2 {
			return nil, errors.New("rdp: CredSSP: bad length")
		}
		ext := make([]byte, k)
		if _, err := io.ReadFull(r, ext); err != nil {
			return nil, err
		}
		hdr = append(hdr, ext...)
		n = 0
		for _, b := range ext {
			n = n<<8 | int(b)
		}
	}
	if n > maxTSRequest {
		return nil, fmt.Errorf("rdp: CredSSP: TSRequest of %d bytes", n)
	}
	b := make([]byte, len(hdr)+n)
	copy(b, hdr)
	if _, err := io.ReadFull(r, b[len(hdr):]); err != nil {
		return nil, err
	}
	var req tsRequest
	if _, err := asn1.Unmarshal(b, &req); err != nil {
		return nil, fmt.Errorf("rdp: CredSSP: %w", err)
	}
	if req.ErrorCode != 0 {
		return nil, &CredSSPError{Code: uint32(req.ErrorCode)} // #nosec G115 -- NTSTATUS is 32 bits
	}
	return &req, nil
}

// readEarlyUserAuth reads the Early User Authorization Result PDU of
// [MS-RDPBCGR] 2.2.10.2, which follows CredSSP under PROTOCOL_HYBRID_EX.
func readEarlyUserAuth(r io.Reader) error {
	var b [4]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return err
	}
	if v := binary.LittleEndian.Uint32(b[:]); v != 0 {
		return fmt.Errorf("rdp: early user authorization denied (%#x)", v)
	}
	return nil
}
