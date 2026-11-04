// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package server

import (
	"bytes"
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
	"time"

	"changkun.de/x/occamy/internal/auth"
	"changkun.de/x/occamy/internal/config"
)

// replace writes b to path as an operator does: beside it, then renamed
// into place. Its modification time is a second on from the last, since
// a file system may keep it in coarse steps.
func replace(t *testing.T, path string, b []byte, step int) {
	t.Helper()
	tmp := path + ".new"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(time.Duration(step) * time.Second)
	if err := os.Chtimes(tmp, at, at); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
}

// served returns the certificate the server at addr presents.
func served(t *testing.T, addr string, pool *x509.CertPool) []byte {
	t.Helper()
	c, err := tls.Dial("tcp", addr, &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	return c.ConnectionState().PeerCertificates[0].Raw
}

func certDER(t *testing.T, certPEM []byte) []byte {
	t.Helper()
	b, _ := pem.Decode(certPEM)
	if b == nil {
		t.Fatal("no PEM block")
	}
	return b.Bytes
}

// A renewed certificate is served from the next handshake, without a
// restart, and one that doesn't load leaves the one before it served.
func TestTLSRenewed(t *testing.T) {
	oldCert, oldKey := testCertPEM(t)
	newCert, newKey := testCertPEM(t)
	dir := t.TempDir()
	certFile, keyFile := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	replace(t, certFile, oldCert, 0)
	replace(t, keyFile, oldKey, 0)
	f := testFile(t)
	f.TLS = config.TLS{Cert: certFile, Key: keyFile}
	s, err := newServer(f)
	if err != nil {
		t.Fatal(err)
	}
	ln := localListener(t)
	serving(t, time.Second, newPublicServer(s.routes(), s.tls), ln, nil, nil)
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(oldCert)
	pool.AppendCertsFromPEM(newCert)
	addr := ln.Addr().String()

	if got := served(t, addr, pool); !bytes.Equal(got, certDER(t, oldCert)) {
		t.Fatal("the first handshake didn't present the certificate in the files")
	}
	replace(t, certFile, newCert, 1)
	replace(t, keyFile, newKey, 1)
	if got := served(t, addr, pool); !bytes.Equal(got, certDER(t, newCert)) {
		t.Fatal("a handshake after the renewal presented the old certificate")
	}
	replace(t, certFile, []byte("not a certificate"), 2)
	if got := served(t, addr, pool); !bytes.Equal(got, certDER(t, newCert)) {
		t.Fatal("a handshake after a broken renewal didn't present the last good certificate")
	}
}

// A trusted issuer's rotated key verifies its tokens from the next
// request, without a restart, and those of the old key are refused.
func TestTrustedIssuerKeyRotated(t *testing.T) {
	k := newKeys(t)
	path := writeIssuerKey(t, k.gateway)
	ts := newTestServer(t, func(f *config.File) {
		f.Auth.TrustedIssuers = []config.TrustedIssuer{{Issuer: "gateway", PublicKeyFile: path}}
	})
	verify := func(key ed25519.PrivateKey) error {
		_, err := ts.s.verifier.VerifyAccess(signClaims(t, key, access("gateway", "carol")))
		return err
	}
	if err := verify(k.gateway); err != nil {
		t.Fatalf("the gateway's token: %v", err)
	}
	_, rotated, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := os.ReadFile(writeIssuerKey(t, rotated))
	if err != nil {
		t.Fatal(err)
	}
	replace(t, path, pub, 1)
	if err := verify(rotated); err != nil {
		t.Errorf("a token of the rotated key: %v", err)
	}
	if err := verify(k.gateway); err == nil {
		t.Error("a token of the old key verified after the rotation")
	}
	replace(t, path, []byte("not a key"), 2)
	if err := verify(rotated); err != nil {
		t.Errorf("a token of the rotated key after a broken rotation: %v", err)
	}
	// occamyd's own tokens never depend on the file.
	if err := verify(k.stranger); err == nil {
		t.Error("a stranger's token verified")
	}
	if _, err := ts.s.verifier.VerifyAccess(signClaims(t, k.gateway, access(auth.Issuer, "carol"))); err == nil {
		t.Error("the gateway signed for occamyd")
	}
}
