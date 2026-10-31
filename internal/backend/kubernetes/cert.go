// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package kubernetes

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
)

// The API server's certificate policy is the VNC backend's, for VeNCrypt
// (internal/backend/vnc), in meaning and shape, so that one tls block of
// the configuration serves both. TLS is never optional here.

// TLS is the policy for the certificate the API server presents. By
// default the certificate must chain to the system's roots and name the
// API server's host, as a browser checks it. Pins and InsecureSkipVerify
// mirror the SSH backend's host keys.
type TLS struct {
	// ServerName is the name the certificate must carry. It defaults to
	// the host of Config.Addr, which may be an IP address.
	ServerName string
	// RootCAs replaces the system's roots, as for a cluster's own CA.
	RootCAs *x509.CertPool
	// Pins are the certificates accepted as they are, as SSH host keys
	// are: their chain, names and validity are not checked. Each is a
	// certificate in PEM, or the SHA-256 fingerprint of one, in hex as
	// openssl x509 -noout -fingerprint -sha256 prints it (colons
	// optional) or in base64 after "SHA256:". Pins replace the roots,
	// so RootCAs must be nil.
	Pins []string
	// InsecureSkipVerify accepts any certificate that is not pinned, and
	// logs a warning with its fingerprint. The token, if any, then goes
	// to whoever answers. It is meant for demos.
	InsecureSkipVerify bool
}

// CertificateError is the error, wrapped in Run's, for a certificate
// that the TLS policy does not accept. Its fingerprint is what Pins
// takes. Nothing was sent to the server.
type CertificateError struct {
	Subject     string // the certificate's subject
	Fingerprint string // its SHA-256 fingerprint, as openssl prints it
	Err         error  // why it was not accepted
}

// Error quotes the subject and cuts the reason, which can list every
// name in the certificate, as it does any text from the server.
func (e *CertificateError) Error() string {
	return fmt.Sprintf("kubernetes: the API server's certificate %s (SHA-256 %s) is not trusted: %s", quote(e.Subject), e.Fingerprint, cut(e.Err.Error()))
}

func (e *CertificateError) Unwrap() error { return e.Err }

// errNotPinned is a CertificateError's Err when pins are configured.
var errNotPinned = errors.New("it is not pinned")

// tlsPolicy is a parsed TLS.
type tlsPolicy struct {
	serverName string
	roots      *x509.CertPool // nil for the system's
	pins       [][sha256.Size]byte
	insecure   bool
}

// newTLSPolicy parses c for an API server on host, which is never empty.
func newTLSPolicy(c TLS, host string) (tlsPolicy, error) {
	p := tlsPolicy{
		serverName: c.ServerName,
		roots:      c.RootCAs,
		insecure:   c.InsecureSkipVerify,
	}
	if p.serverName == "" {
		p.serverName = host
	}
	if c.RootCAs != nil && len(c.Pins) > 0 {
		return tlsPolicy{}, errors.New("kubernetes: TLS pins replace the roots: set RootCAs or Pins, not both")
	}
	for i, s := range c.Pins {
		sum, err := parsePin(s)
		if err != nil {
			return tlsPolicy{}, fmt.Errorf("kubernetes: TLS pin %d: %w", i, err)
		}
		p.pins = append(p.pins, sum)
	}
	return p, nil
}

// CheckPin reports whether s is a pin as TLS.Pins takes it: a
// certificate in PEM, or a SHA-256 fingerprint in hex or after
// "SHA256:". The configuration checks its entries with it.
func CheckPin(s string) error {
	_, err := parsePin(s)
	return err
}

// parsePin parses a pinned certificate or fingerprint.
func parsePin(s string) ([sha256.Size]byte, error) {
	var sum [sha256.Size]byte
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "-----BEGIN") {
		block, rest := pem.Decode([]byte(s))
		if block == nil || block.Type != "CERTIFICATE" || len(strings.TrimSpace(string(rest))) > 0 {
			return sum, errors.New("not one PEM certificate")
		}
		if _, err := x509.ParseCertificate(block.Bytes); err != nil {
			return sum, err
		}
		return sha256.Sum256(block.Bytes), nil
	}
	var b []byte
	var err error
	if b64, ok := strings.CutPrefix(s, "SHA256:"); ok {
		b, err = base64.RawStdEncoding.DecodeString(strings.TrimRight(b64, "="))
	} else {
		b, err = hex.DecodeString(strings.ReplaceAll(s, ":", ""))
	}
	if err != nil || len(b) != sha256.Size {
		return sum, errors.New("not a certificate or a SHA-256 fingerprint")
	}
	return [sha256.Size]byte(b), nil
}

// config returns the TLS configuration for a connection to addr, with
// the client certificate if there is one. The certificate is checked by
// verify in VerifyConnection, during the handshake, so that nothing, the
// token least of all, is written to a server the policy refuses; pins
// and insecure mode share that one path, and a refusal carries the
// fingerprint.
func (p *tlsPolicy) config(addr string, cert *tls.Certificate, log *slog.Logger) *tls.Config {
	c := &tls.Config{
		ServerName: p.serverName,
		MinVersion: tls.VersionTLS12,
		// #nosec G402 -- VerifyConnection checks the certificate by the policy
		InsecureSkipVerify: true,
		VerifyConnection: func(cs tls.ConnectionState) error {
			return p.verify(cs.PeerCertificates, addr, log)
		},
		// A WebSocket upgrade is HTTP/1.1.
		NextProtos: []string{"http/1.1"},
	}
	if cert != nil {
		c.Certificates = []tls.Certificate{*cert}
	}
	return c
}

// verify checks the server's certificate chain, leaf first: against the
// pins if there are any, against the roots and the server name if not.
// In insecure mode a certificate that fails is accepted with a warning.
func (p *tlsPolicy) verify(chain []*x509.Certificate, addr string, log *slog.Logger) error {
	if len(chain) == 0 {
		return errors.New("kubernetes: the API server presented no certificate")
	}
	leaf := chain[0]
	sum := sha256.Sum256(leaf.Raw)
	var err error
	if len(p.pins) > 0 {
		if slices.Contains(p.pins, sum) {
			return nil
		}
		err = errNotPinned
	} else {
		opts := x509.VerifyOptions{DNSName: p.serverName, Roots: p.roots, Intermediates: x509.NewCertPool()}
		for _, c := range chain[1:] {
			opts.Intermediates.AddCert(c)
		}
		if _, err = leaf.Verify(opts); err == nil {
			return nil
		}
	}
	if p.insecure {
		log.Warn("kubernetes: accepting an unverified certificate", "addr", addr,
			"subject", leaf.Subject.String(), "fingerprint", fingerprint(sum), "reason", err)
		return nil
	}
	return &CertificateError{Subject: leaf.Subject.String(), Fingerprint: fingerprint(sum), Err: err}
}

// fingerprint formats a SHA-256 sum as openssl does: upper-case hex
// bytes separated by colons.
func fingerprint(sum [sha256.Size]byte) string {
	var b strings.Builder
	for i, c := range sum {
		if i > 0 {
			b.WriteByte(':')
		}
		fmt.Fprintf(&b, "%02X", c)
	}
	return b.String()
}
