// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package vnc

// VeNCrypt, the security type 19 (rfbproto, section 7.2.19), negotiates
// a subtype. The X.509 subtypes run TLS, with the server's certificate
// checked by the backend's TLS policy, and then an inner authentication:
// None, VNC Authentication, or Plain's username and password.
//
// The anonymous TLS subtypes (TLSNone, TLSVnc and TLSPlain) are never
// chosen: they use anonymous Diffie-Hellman, which no certificate
// authenticates, so a man in the middle reads everything, and crypto/tls
// does not implement it anyway. Plain without TLS (256) is not chosen
// either, since it sends the password in the clear.

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"
)

// secVeNCrypt is the VeNCrypt security type.
const secVeNCrypt = 19

// The VeNCrypt subtypes the backend chooses from. Servers such as
// TigerVNC also list the security types None and VNC Authentication
// among the subtypes, which then run without TLS.
const (
	subX509None  uint32 = 260
	subX509Vnc   uint32 = 261
	subX509Plain uint32 = 262
)

// maxCredential bounds Plain's username and password.
const maxCredential = 64 << 10

// veNCrypt runs VeNCrypt 0.2 after the client has chosen it: the
// version, the subtype, TLS for the X.509 subtypes, and the subtype's
// authentication. SecurityResult follows.
func veNCrypt(r io.Reader, w io.Writer, a auth) error {
	var b [4]byte
	if err := read(r, b[:2]); err != nil {
		return fmt.Errorf("vnc: read the VeNCrypt version: %w", err)
	}
	if b[0] != 0 || b[1] < 2 {
		// 0.1 numbers its subtypes otherwise. 0.0 tells the server that
		// the client supports none of its versions, as TigerVNC does.
		_, _ = w.Write([]byte{0, 0})
		return fmt.Errorf("%w: the server offers VeNCrypt %d.%d", ErrNoSecurityType, b[0], b[1])
	}
	if _, err := w.Write([]byte{0, 2}); err != nil {
		return fmt.Errorf("vnc: %w", err)
	}
	if err := read(r, b[:1]); err != nil {
		return fmt.Errorf("vnc: read the VeNCrypt version's ack: %w", err)
	}
	if b[0] != 0 {
		return fmt.Errorf("%w: the server does not accept VeNCrypt 0.2", ErrRefused)
	}
	if err := read(r, b[:1]); err != nil {
		return fmt.Errorf("vnc: read the VeNCrypt subtypes: %w", err)
	}
	raw := make([]byte, 4*int(b[0]))
	if err := read(r, raw); err != nil {
		return fmt.Errorf("vnc: read the VeNCrypt subtypes: %w", err)
	}
	offered := make([]uint32, b[0])
	for i := range offered {
		offered[i] = binary.BigEndian.Uint32(raw[4*i:])
	}
	st := chooseSubtype(offered, a)
	if st == 0 {
		return fmt.Errorf("%w: the server offers the VeNCrypt subtypes %v", ErrNoSecurityType, offered)
	}
	if _, err := w.Write(binary.BigEndian.AppendUint32(nil, st)); err != nil {
		return fmt.Errorf("vnc: %w", err)
	}
	if st >= subX509None {
		if err := read(r, b[:1]); err != nil {
			return fmt.Errorf("vnc: read the VeNCrypt subtype's ack: %w", err)
		}
		if b[0] != 1 {
			return fmt.Errorf("%w: the server did not start TLS", ErrRefused)
		}
		if err := a.startTLS(); err != nil {
			return err
		}
	}
	switch st {
	case subX509Vnc, secVNCAuth:
		return vncAuth(r, w, a.password)
	case subX509Plain:
		return plainAuth(w, a.plain)
	}
	return nil
}

// chooseSubtype picks a VeNCrypt subtype from those offered, or returns
// 0. The X.509 subtypes come first, by what the configuration holds:
// Plain if it has a username, VNC Authentication if it has a password,
// and None otherwise, so that nobody is asked for credentials that a
// server does not need. Without them, and unless a.requireTLS, the
// security types None and VNC Authentication are chosen as outside
// VeNCrypt.
func chooseSubtype(offered []uint32, a auth) uint32 {
	order := []uint32{subX509None, subX509Plain, subX509Vnc}
	switch {
	case a.hasUser:
		order = []uint32{subX509Plain, subX509Vnc, subX509None}
	case a.hasPassword:
		order = []uint32{subX509Vnc, subX509Plain, subX509None}
	}
	for _, st := range order {
		if slices.Contains(offered, st) {
			return st
		}
	}
	if a.requireTLS {
		return 0
	}
	var plain []byte
	for _, st := range offered {
		if st == secNone || st == secVNCAuth {
			plain = append(plain, byte(st))
		}
	}
	return uint32(choose(plain, auth{hasPassword: a.hasPassword}))
}

// plainAuth sends Plain's username and password.
func plainAuth(w io.Writer, creds func() (string, string, error)) error {
	user, password, err := creds()
	if err != nil {
		return err
	}
	if len(user) > maxCredential || len(password) > maxCredential {
		return fmt.Errorf("vnc: a username or password longer than %d bytes", maxCredential)
	}
	p := binary.BigEndian.AppendUint32(nil, uint32(len(user)))  // #nosec G115 -- at most maxCredential
	p = binary.BigEndian.AppendUint32(p, uint32(len(password))) // #nosec G115 -- at most maxCredential
	p = append(append(p, user...), password...)
	_, err = w.Write(p)
	clear(p)
	if err != nil {
		return fmt.Errorf("vnc: %w", err)
	}
	return nil
}

// conn is the connection as Run reads and writes it: the TCP
// connection, until VeNCrypt replaces both directions with TLS.
type conn struct {
	io.Reader
	io.Writer
}

// TLS is the policy for the certificate that a server presents in
// VeNCrypt's X.509 subtypes. By default the certificate must chain to
// the system's roots and name the server's host, as a browser checks it.
// Pins and InsecureSkipVerify mirror the SSH backend's host keys.
type TLS struct {
	// Require refuses servers that do not offer VeNCrypt with an X.509
	// subtype, so that nobody between the backend and the server can
	// strip VeNCrypt from the security types the server offers. It is
	// implied by RootCAs and Pins.
	Require bool
	// ServerName is the name the certificate must carry. It defaults to
	// the host of Config.Addr, which may be an IP address.
	ServerName string
	// RootCAs replaces the system's roots, as for a private CA.
	RootCAs *x509.CertPool
	// Pins are the certificates accepted as they are, as SSH host keys
	// are: their chain, names and validity are not checked. Each is a
	// certificate in PEM, or the SHA-256 fingerprint of one, in hex as
	// openssl x509 -noout -fingerprint -sha256 prints it (colons
	// optional) or in base64 after "SHA256:". Pins replace the roots,
	// so RootCAs must be nil.
	Pins []string
	// InsecureSkipVerify accepts any certificate that is not pinned, and
	// logs a warning with its fingerprint. It is meant for demos.
	InsecureSkipVerify bool
}

// CertificateError is the error, wrapped in Run's, for a certificate
// that the TLS policy does not accept. Its fingerprint is what Pins
// takes.
type CertificateError struct {
	Subject     string // the certificate's subject
	Fingerprint string // its SHA-256 fingerprint, as openssl prints it
	Err         error  // why it was not accepted
}

func (e *CertificateError) Error() string {
	return fmt.Sprintf("vnc: the server's certificate %q (SHA-256 %s) is not trusted: %v", e.Subject, e.Fingerprint, e.Err)
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
	require    bool
}

// newTLSPolicy parses c for a server on host.
func newTLSPolicy(c TLS, host string) (tlsPolicy, error) {
	p := tlsPolicy{
		serverName: c.ServerName,
		roots:      c.RootCAs,
		insecure:   c.InsecureSkipVerify,
		require:    c.Require || c.RootCAs != nil || len(c.Pins) > 0,
	}
	if p.serverName == "" {
		p.serverName = host
	}
	if c.RootCAs != nil && len(c.Pins) > 0 {
		return tlsPolicy{}, errors.New("vnc: TLS pins replace the roots: set RootCAs or Pins, not both")
	}
	for i, s := range c.Pins {
		sum, err := parsePin(s)
		if err != nil {
			return tlsPolicy{}, fmt.Errorf("vnc: TLS pin %d: %w", i, err)
		}
		p.pins = append(p.pins, sum)
	}
	return p, nil
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

// config returns the TLS configuration for a connection to addr. The
// certificate is checked by verify, not by crypto/tls, so that pins and
// insecure mode share one path and a rejection carries the fingerprint.
func (p *tlsPolicy) config(addr string, log *slog.Logger) *tls.Config {
	return &tls.Config{
		ServerName: p.serverName,
		MinVersion: tls.VersionTLS12,
		// #nosec G402 -- VerifyConnection checks the certificate by the policy
		InsecureSkipVerify: true,
		VerifyConnection: func(cs tls.ConnectionState) error {
			return p.verify(cs.PeerCertificates, addr, log)
		},
	}
}

// verify checks the server's certificate chain, leaf first: against the
// pins if there are any, against the roots and the server name if not.
// In insecure mode a certificate that fails is accepted with a warning.
func (p *tlsPolicy) verify(chain []*x509.Certificate, addr string, log *slog.Logger) error {
	if len(chain) == 0 {
		return errors.New("vnc: the server presented no certificate")
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
		log.Warn("vnc: accepting an unverified certificate", "addr", addr,
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
