// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package rdp

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"slices"
	"strings"
	"sync"
)

// The server certificate policy, as the SSH backend's host key policy:
// the system roots by default, pinned certificates, or an explicit
// insecure mode that warns. A Client without any of them does not
// connect.

// CertificateError is the error for a server certificate that the
// configuration does not accept.
type CertificateError struct {
	// Fingerprint is the certificate's SHA-256 fingerprint, in the form
	// Config.ServerCerts takes.
	Fingerprint string
	// Err is why the roots refused it, if the configuration named the
	// server.
	Err error
}

func (e *CertificateError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("rdp: server certificate %s is not pinned, and %v", e.Fingerprint, e.Err)
	}
	return fmt.Sprintf("rdp: server certificate %s is not pinned", e.Fingerprint)
}

func (e *CertificateError) Unwrap() error { return e.Err }

// certPolicy checks the server's certificate.
type certPolicy struct {
	serverName string
	roots      *x509.CertPool
	pins       [][sha256.Size]byte
	insecure   bool
	log        *slog.Logger
	warn       sync.Once
}

// newCertPolicy parses the policy of cfg.
func newCertPolicy(cfg *Config) (*certPolicy, error) {
	p := &certPolicy{
		serverName: cfg.ServerName,
		roots:      cfg.RootCAs,
		insecure:   cfg.InsecureSkipVerify,
		log:        cfg.Logger,
	}
	if p.log == nil {
		p.log = slog.Default()
	}
	for i, s := range cfg.ServerCerts {
		s = strings.TrimSpace(s)
		if strings.HasPrefix(s, "-----BEGIN") {
			n := len(p.pins)
			for rest := []byte(s); ; {
				var b *pem.Block
				if b, rest = pem.Decode(rest); b == nil {
					break
				}
				if b.Type == "CERTIFICATE" {
					p.pins = append(p.pins, sha256.Sum256(b.Bytes))
				}
			}
			if len(p.pins) == n {
				return nil, fmt.Errorf("rdp: server certificate %d: no certificate in the PEM", i)
			}
			continue
		}
		sum, err := hex.DecodeString(strings.ReplaceAll(s, ":", ""))
		if err != nil || len(sum) != sha256.Size {
			return nil, fmt.Errorf("rdp: server certificate %d is neither PEM nor a SHA-256 fingerprint", i)
		}
		p.pins = append(p.pins, [sha256.Size]byte(sum))
	}
	if p.serverName == "" && len(p.pins) == 0 && !p.insecure {
		return nil, errors.New("rdp: no certificate policy: name the server, pin its certificate or skip verification explicitly")
	}
	return p, nil
}

// tlsConfig returns the TLS configuration that applies the policy. Go's
// own verification is off, since a pinned certificate is accepted
// whatever its chain and names; verify replaces it.
func (p *certPolicy) tlsConfig() *tls.Config {
	cfg := &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: true, // #nosec G402 -- VerifyConnection applies the policy
		VerifyConnection:   p.verify,
	}
	if p.serverName != "" && net.ParseIP(p.serverName) == nil {
		cfg.ServerName = p.serverName
	}
	return cfg
}

func (p *certPolicy) verify(cs tls.ConnectionState) error {
	if len(cs.PeerCertificates) == 0 {
		return errors.New("rdp: the server sent no certificate")
	}
	leaf := cs.PeerCertificates[0]
	sum := sha256.Sum256(leaf.Raw)
	if slices.Contains(p.pins, sum) {
		return nil
	}
	var err error
	if p.serverName != "" {
		opts := x509.VerifyOptions{DNSName: p.serverName, Roots: p.roots, Intermediates: x509.NewCertPool()}
		for _, c := range cs.PeerCertificates[1:] {
			opts.Intermediates.AddCert(c)
		}
		if _, err = leaf.Verify(opts); err == nil {
			return nil
		}
	}
	fp := fingerprint(sum)
	if p.insecure {
		p.warn.Do(func() {
			p.log.Warn("rdp: accepting an unverified server certificate", "server", p.serverName, "fingerprint", fp)
		})
		return nil
	}
	return &CertificateError{Fingerprint: fp, Err: err}
}

// fingerprint formats a SHA-256 fingerprint as openssl x509 -fingerprint
// prints it: upper-case hex bytes between colons.
func fingerprint(sum [sha256.Size]byte) string {
	parts := make([]string, len(sum))
	for i, b := range sum {
		parts[i] = fmt.Sprintf("%02X", b)
	}
	return strings.Join(parts, ":")
}
