// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package rdp

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"log/slog"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"
)

// connectTo runs Connect against the recorded xrdp session, offering TLS
// alone as xrdp needs, and returns its error.
func connectTo(t *testing.T, s *fakeServer, cfg Config) error {
	t.Helper()
	if s == nil {
		s = &fakeServer{frames: loadTrace(t, "testdata/xrdp-32bpp.trace.gz")}
	}
	if cfg.Protocols == 0 {
		cfg.Protocols = ProtocolSSL
	}
	if cfg.Logger == nil {
		cfg.Logger = quiet
	}
	cfg.Username, cfg.Password = "occamy", "occamy"
	cconn, sconn := net.Pipe()
	go s.serve(sconn)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := Connect(ctx, cconn, cfg)
	if err == nil {
		c.Close()
	}
	return err
}

// The server's certificate is accepted when it is pinned, valid for the
// server's name, or explicitly not verified, and refused otherwise.
func TestCertificatePolicy(t *testing.T) {
	cert, err := testCert()
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(cert.Leaf.Raw)
	fp := fingerprint(sum)
	roots := x509.NewCertPool()
	roots.AddCert(cert.Leaf)
	pemCert := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Leaf.Raw}))

	for _, tc := range []struct {
		name   string
		tls    TLS
		ok     bool
		policy string // refused for its policy before connecting, with this error
	}{
		{"no server name on a pipe", TLS{}, false, "set TLS.ServerName, TLS.Pins or TLS.InsecureSkipVerify"},
		{"pinned fingerprint", TLS{Pins: []string{fp}}, true, ""},
		{"pinned fingerprint in lower case without colons", TLS{Pins: []string{hex.EncodeToString(sum[:])}}, true, ""},
		{"pinned fingerprint in base64", TLS{Pins: []string{"SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:])}}, true, ""},
		{"pinned PEM", TLS{Pins: []string{pemCert}}, true, ""},
		{"another pin", TLS{Pins: []string{strings.Repeat("00", 32)}}, false, ""},
		{"another pin for the right name", TLS{ServerName: "rdp.test", Pins: []string{strings.Repeat("00", 32)}}, false, ""},
		{"pins and roots", TLS{RootCAs: roots, Pins: []string{fp}}, false, "set RootCAs or Pins"},
		{"name under the roots", TLS{ServerName: "rdp.test", RootCAs: roots}, true, ""},
		{"another name under the roots", TLS{ServerName: "other.test", RootCAs: roots}, false, ""},
		{"name under the system roots", TLS{ServerName: "rdp.test"}, false, ""},
		{"insecure", TLS{InsecureSkipVerify: true}, true, ""},
	} {
		err := connectTo(t, nil, Config{TLS: tc.tls})
		var ce *CertificateError
		switch {
		case tc.ok && err != nil:
			t.Errorf("%s: %v", tc.name, err)
		case tc.ok:
		case tc.policy != "" && (err == nil || !strings.Contains(err.Error(), tc.policy)):
			t.Errorf("%s: %v, want %q", tc.name, err, tc.policy)
		case tc.policy == "" && (!errors.As(err, &ce) || ce.Fingerprint != fp || ce.Subject != "CN=rdp-test"):
			t.Errorf("%s: %v, want a certificate error for CN=rdp-test, %s", tc.name, err, fp)
		}
	}
	for _, bad := range []string{
		"not hex", "abcd", "SHA256:abcd",
		"-----BEGIN PRIVATE KEY-----\nAAAA\n-----END PRIVATE KEY-----\n",
		pemCert + pemCert, // one certificate per pin
	} {
		if err := connectTo(t, nil, Config{TLS: TLS{Pins: []string{bad}}}); err == nil || !strings.Contains(err.Error(), "TLS pin 0") {
			t.Errorf("pin %q: %v", bad, err)
		}
	}
}

// testChain returns a CA and a leaf for rdp.test that it signed.
func testChain(t *testing.T) (ca, leaf *x509.Certificate) {
	t.Helper()
	issue := func(tmpl, parent *x509.Certificate, signer *ecdsa.PrivateKey) (*x509.Certificate, *ecdsa.PrivateKey) {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		if signer == nil {
			signer, parent = key, tmpl
		}
		tmpl.NotBefore, tmpl.NotAfter = time.Now().Add(-time.Hour), time.Now().Add(time.Hour)
		der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, &key.PublicKey, signer)
		if err != nil {
			t.Fatal(err)
		}
		c, err := x509.ParseCertificate(der)
		if err != nil {
			t.Fatal(err)
		}
		return c, key
	}
	ca, caKey := issue(&x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test CA"},
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}, nil, nil)
	leaf, _ = issue(&x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "rdp.test"},
		DNSNames:    []string{"rdp.test"},
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}, ca, caKey)
	return ca, leaf
}

// Pins match the leaf only: pinning the CA that the server sends in its
// chain does not accept the leaf the CA signed.
func TestCertificatePinIsLeafOnly(t *testing.T) {
	ca, leaf := testChain(t)
	p, err := newTLSPolicy(TLS{Pins: []string{fingerprint(sha256.Sum256(ca.Raw))}}, "rdp.test")
	if err != nil {
		t.Fatal(err)
	}
	var ce *CertificateError
	if err := p.verify([]*x509.Certificate{leaf, ca}, "rdp.test:3389", quiet); !errors.As(err, &ce) || !errors.Is(err, errNotPinned) {
		t.Errorf("a pinned CA in the chain: %v, want the leaf not pinned", err)
	}
	if err := p.verify([]*x509.Certificate{ca}, "rdp.test:3389", quiet); err != nil {
		t.Errorf("the pinned certificate itself: %v", err)
	}
}

// Pins replace the roots: a certificate the roots accept for the server's
// name is refused if pins are set and do not match it. newTLSPolicy
// refuses RootCAs with Pins, so the policy is built here as the system
// roots would make it if they held the CA.
func TestCertificatePinsReplaceRoots(t *testing.T) {
	ca, leaf := testChain(t)
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	unpinned := tlsPolicy{serverName: "rdp.test", roots: roots}
	if err := unpinned.verify([]*x509.Certificate{leaf}, "rdp.test:3389", quiet); err != nil {
		t.Fatalf("the roots refused the leaf: %v", err)
	}
	pinned := tlsPolicy{serverName: "rdp.test", roots: roots, pins: [][sha256.Size]byte{{1}}}
	if err := pinned.verify([]*x509.Certificate{leaf}, "rdp.test:3389", quiet); !errors.Is(err, errNotPinned) {
		t.Errorf("with another pin: %v, want the leaf not pinned", err)
	}
}

// The server name defaults to the host of the connection's remote
// address, and to the host Dial dials.
func TestCertificateServerName(t *testing.T) {
	p, err := newTLSPolicy(TLS{}, "192.0.2.1")
	if err != nil || p.serverName != "192.0.2.1" {
		t.Errorf("default server name %q, %v", p.serverName, err)
	}
	p, err = newTLSPolicy(TLS{ServerName: "rdp.test"}, "192.0.2.1")
	if err != nil || p.serverName != "rdp.test" {
		t.Errorf("explicit server name %q, %v", p.serverName, err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	frames := loadTrace(t, "testdata/xrdp-32bpp.trace.gz")
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go (&fakeServer{frames: frames}).serve(conn)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cert, err := testCert()
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(cert.Leaf)
	cfg := Config{Protocols: ProtocolSSL, Username: "occamy", Password: "occamy", TLS: TLS{RootCAs: roots}, Logger: quiet}
	_, port, _ := net.SplitHostPort(ln.Addr().String())

	// The certificate names rdp.test, so the error says which name the
	// client checked it for: the host dialled, not the address it
	// resolved to.
	_, err = Dial(ctx, net.JoinHostPort("localhost", port), cfg)
	var ce *CertificateError
	if !errors.As(err, &ce) || !strings.Contains(err.Error(), "not localhost") {
		t.Errorf("Dial to localhost: %v, want the certificate checked for localhost", err)
	}
	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	_, err = Connect(ctx, conn, cfg)
	if !errors.As(err, &ce) || !strings.Contains(err.Error(), "for 127.0.0.1") {
		t.Errorf("Connect to 127.0.0.1: %v, want the certificate checked for 127.0.0.1", err)
	}
}

// The insecure mode warns with the certificate's fingerprint, once for a
// connection.
func TestCertificateInsecureWarns(t *testing.T) {
	cert, err := testCert()
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	if err := connectTo(t, nil, Config{TLS: TLS{InsecureSkipVerify: true}, Logger: log}); err != nil {
		t.Fatal(err)
	}
	fp := fingerprint(sha256.Sum256(cert.Leaf.Raw))
	if n := strings.Count(buf.String(), "accepting an unverified certificate"); n != 1 || !strings.Contains(buf.String(), fp) {
		t.Errorf("logged %q, want one warning with %s", buf.String(), fp)
	}
}

// The client compares the protocols the server says it was offered with
// those it offered, so that an attacker who removes NLA from the offer in
// the clear is noticed under TLS; a server that does not say is refused.
func TestProtocolDowngrade(t *testing.T) {
	offer := ProtocolSSL | ProtocolHybrid | ProtocolHybridEx
	insecure := TLS{InsecureSkipVerify: true}
	s := &fakeServer{frames: loadTrace(t, "testdata/xrdp-32bpp.trace.gz"), echo: ProtocolSSL}
	err := connectTo(t, s, Config{Protocols: offer, TLS: insecure})
	if err == nil || !strings.Contains(err.Error(), "offered security protocols 0x1") {
		t.Errorf("Connect returned %v, want the downgrade noticed", err)
	}
	s.echo = 0
	if err := connectTo(t, s, Config{Protocols: offer, TLS: insecure}); err != nil {
		t.Errorf("the server echoes the offer: %v", err)
	}
	s.noEcho = true
	if err := connectTo(t, s, Config{Protocols: offer, TLS: insecure}); err == nil || !strings.Contains(err.Error(), "did not say which security protocols") {
		t.Errorf("a server without clientRequestedProtocols: %v", err)
	}
}
