// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package rdp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"log/slog"
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
		cfg    Config
		ok     bool
		policy bool // refused for the policy it lacks, before connecting
	}{
		{"no policy", Config{}, false, true},
		{"pinned fingerprint", Config{ServerCerts: []string{fp}}, true, false},
		{"pinned fingerprint in lower case without colons", Config{ServerCerts: []string{hex.EncodeToString(sum[:])}}, true, false},
		{"pinned PEM", Config{ServerCerts: []string{pemCert}}, true, false},
		{"another pin", Config{ServerCerts: []string{strings.Repeat("00", 32)}}, false, false},
		{"name under the roots", Config{ServerName: "rdp.test", RootCAs: roots}, true, false},
		{"another name under the roots", Config{ServerName: "other.test", RootCAs: roots}, false, false},
		{"name under the system roots", Config{ServerName: "rdp.test"}, false, false},
		{"insecure", Config{InsecureSkipVerify: true}, true, false},
	} {
		err := connectTo(t, nil, tc.cfg)
		var ce *CertificateError
		switch {
		case tc.ok && err != nil:
			t.Errorf("%s: %v", tc.name, err)
		case tc.ok:
		case tc.policy && (err == nil || !strings.Contains(err.Error(), "no certificate policy")):
			t.Errorf("%s: %v, want no certificate policy", tc.name, err)
		case !tc.policy && (!errors.As(err, &ce) || ce.Fingerprint != fp):
			t.Errorf("%s: %v, want a certificate error for %s", tc.name, err, fp)
		}
	}
	for _, bad := range []string{"not hex", "abcd", "-----BEGIN PRIVATE KEY-----\nAAAA\n-----END PRIVATE KEY-----\n"} {
		if err := connectTo(t, nil, Config{ServerCerts: []string{bad}}); err == nil {
			t.Errorf("pin %q accepted", bad)
		}
	}
}

// The insecure mode warns once, with the certificate's fingerprint.
func TestCertificateInsecureWarns(t *testing.T) {
	cert, err := testCert()
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	if err := connectTo(t, nil, Config{InsecureSkipVerify: true, Logger: log}); err != nil {
		t.Fatal(err)
	}
	fp := fingerprint(sha256.Sum256(cert.Leaf.Raw))
	if n := strings.Count(buf.String(), "unverified server certificate"); n != 1 || !strings.Contains(buf.String(), fp) {
		t.Errorf("logged %q, want one warning with %s", buf.String(), fp)
	}
}

// The client compares the protocols the server says it was offered with
// those it offered, so that an attacker who removes NLA from the offer in
// the clear is noticed under TLS.
func TestProtocolDowngrade(t *testing.T) {
	s := &fakeServer{frames: loadTrace(t, "testdata/xrdp-32bpp.trace.gz"), echo: ProtocolSSL}
	err := connectTo(t, s, Config{Protocols: ProtocolSSL | ProtocolHybrid | ProtocolHybridEx, InsecureSkipVerify: true})
	if err == nil || !strings.Contains(err.Error(), "offered security protocols 0x1") {
		t.Errorf("Connect returned %v, want the downgrade noticed", err)
	}
	s.echo = 0
	if err := connectTo(t, s, Config{Protocols: ProtocolSSL | ProtocolHybrid | ProtocolHybridEx, InsecureSkipVerify: true}); err != nil {
		t.Errorf("the server echoes the offer: %v", err)
	}
}
