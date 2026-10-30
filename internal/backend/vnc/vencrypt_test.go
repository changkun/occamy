// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package vnc

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"math/big"
	"net"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"changkun.de/x/occamy/internal/backend"
	"changkun.de/x/occamy/internal/backend/backendtest"
)

// testPKI is a CA with a server certificate for localhost and
// 127.0.0.1, and another CA that signed nothing the server presents.
type testPKI struct {
	roots, otherRoots *x509.CertPool
	server            tls.Certificate // signed by the CA
	leaf              *x509.Certificate
	selfSigned        tls.Certificate // for localhost too, signed by itself
}

var (
	pkiOnce sync.Once
	pki     *testPKI
)

// newPKI returns the tests' PKI, made once.
func newPKI(t *testing.T) *testPKI {
	t.Helper()
	pkiOnce.Do(func() {
		ca, caKey := newCert(t, "Occamy test CA", nil, nil)
		other, _ := newCert(t, "Another CA", nil, nil)
		leaf, leafKey := newCert(t, "localhost", ca, caKey)
		self, selfKey := newCert(t, "localhost", nil, nil)
		pki = &testPKI{
			roots:      x509.NewCertPool(),
			otherRoots: x509.NewCertPool(),
			server:     tls.Certificate{Certificate: [][]byte{leaf.Raw}, PrivateKey: leafKey, Leaf: leaf},
			leaf:       leaf,
			selfSigned: tls.Certificate{Certificate: [][]byte{self.Raw}, PrivateKey: selfKey, Leaf: self},
		}
		pki.roots.AddCert(ca)
		pki.otherRoots.AddCert(other)
	})
	return pki
}

// newCert makes a certificate for name, signed by parent's key, or a CA
// signed by itself if parent is nil and name ends in CA, or a server
// certificate signed by itself otherwise.
func newCert(t *testing.T, name string, parent *x509.Certificate, parentKey *ecdsa.PrivateKey) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: name},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
	}
	if strings.HasSuffix(name, "CA") {
		tmpl.IsCA, tmpl.BasicConstraintsValid = true, true
		tmpl.KeyUsage = x509.KeyUsageCertSign
	} else {
		tmpl.DNSNames = []string{"localhost"}
		tmpl.IPAddresses = []net.IP{net.IPv4(127, 0, 0, 1)}
		tmpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
		tmpl.KeyUsage = x509.KeyUsageDigitalSignature
	}
	if parent == nil {
		parent, parentKey = tmpl, key
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, &key.PublicKey, parentKey)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert, key
}

// logBuffer collects a logger's output.
type logBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func (l *logBuffer) logger() *slog.Logger { return slog.New(slog.NewTextHandler(l, nil)) }

// runAgainst starts a fake server with opts and a backend of cfg, whose
// address it fills in, connected to it.
func runAgainst(t *testing.T, opts serverOptions, cfg Config, sink *backendtest.Sink) (*fakeServer, *Backend, *runner) {
	t.Helper()
	srv := newServer(t, opts)
	cfg.Addr = srv.addr
	b := newBackend(t, cfg)
	return srv, b, start(t, b, sink)
}

func TestVeNCrypt(t *testing.T) {
	p := newPKI(t)
	for _, tt := range []struct {
		name    string
		version string // the server's RFB version, 3.8 if empty
		types   []byte
		subs    []uint32
		cfg     Config
		answer  []string // the owner's answers, if asked
		want    uint32   // the subtype chosen; 0 for an error
		err     error
		prompt  []backend.PromptField
		plain   string // what Plain sent
		noCA    bool   // configure no roots, which would require TLS
	}{
		{name: "X509None", subs: []uint32{subX509None}, want: subX509None},
		{name: "3.7", version: "003.007", subs: []uint32{subX509None}, want: subX509None},
		{name: "None without credentials", subs: []uint32{subX509Vnc, subX509Plain, subX509None}, want: subX509None},
		{
			name: "VNC Authentication with a password", subs: []uint32{subX509None, subX509Plain, subX509Vnc},
			cfg: Config{Password: "hunter2"}, want: subX509Vnc,
		},
		{
			name: "Plain with a username", subs: []uint32{subX509None, subX509Vnc, subX509Plain},
			cfg: Config{Username: "alice", Password: "hunter2"}, want: subX509Plain, plain: "alice:hunter2",
		},
		{
			name: "Plain asks for both", subs: []uint32{subX509Vnc, subX509Plain},
			answer: []string{"alice", "hunter2"}, want: subX509Plain, plain: "alice:hunter2",
			prompt: []backend.PromptField{{Label: "Username"}, {Label: "Password", Secret: true}},
		},
		{
			name: "Plain asks for the username", subs: []uint32{subX509Plain}, cfg: Config{Password: "hunter2"},
			answer: []string{"alice"}, want: subX509Plain, plain: "alice:hunter2",
			prompt: []backend.PromptField{{Label: "Username"}},
		},
		{
			name: "Plain asks for the password", subs: []uint32{subX509Plain}, cfg: Config{Username: "alice"},
			answer: []string{"hunter2"}, want: subX509Plain, plain: "alice:hunter2",
			prompt: []backend.PromptField{{Label: "Password", Secret: true}},
		},
		{
			name: "VNC Authentication asks for the password", subs: []uint32{subX509Vnc},
			answer: []string{"hunter2"}, want: subX509Vnc,
			prompt: []backend.PromptField{{Label: "Password", Secret: true}},
		},
		{
			name: "wrong Plain password", subs: []uint32{subX509Plain}, cfg: Config{Username: "alice", Password: "hunter3"},
			err: ErrAuthFailed, plain: "alice:hunter3",
		},
		{
			name: "wrong VNC password", subs: []uint32{subX509Vnc}, cfg: Config{Password: "hunter3"},
			err: ErrAuthFailed,
		},
		{
			name: "VNC Authentication without TLS, as TigerVNC lists it", types: []byte{secVeNCrypt, secVNCAuth},
			subs: []uint32{258, secVNCAuth}, cfg: Config{Password: "hunter2"}, want: secVNCAuth, noCA: true,
		},
		{
			name: "a private CA requires TLS", types: []byte{secVeNCrypt, secVNCAuth},
			subs: []uint32{258, secVNCAuth}, cfg: Config{Password: "hunter2"}, err: ErrNoSecurityType,
		},
		{name: "anonymous TLS only", subs: []uint32{257, 258, 259}, err: ErrNoSecurityType},
		{name: "Plain in the clear", subs: []uint32{256}, cfg: Config{Username: "alice", Password: "hunter2"}, err: ErrNoSecurityType},
		{
			name: "required TLS refuses VNC Authentication within", subs: []uint32{258, secVNCAuth},
			cfg: Config{Password: "hunter2", TLS: TLS{Require: true}}, err: ErrNoSecurityType,
		},
		{
			name: "required TLS refuses a server without VeNCrypt", types: []byte{secNone, secVNCAuth},
			cfg: Config{Password: "hunter2", TLS: TLS{Require: true}}, err: ErrNoSecurityType,
		},
		{
			name: "a pin requires TLS", types: []byte{secNone, secVNCAuth},
			cfg: Config{TLS: TLS{Pins: []string{certFingerprint(p.leaf)}}}, err: ErrNoSecurityType,
		},
		{
			name: "required TLS refuses a 3.3 server", version: "003.003", types: []byte{secVNCAuth},
			cfg: Config{Password: "hunter2", TLS: TLS{Require: true}}, err: ErrNoSecurityType,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			checkGoroutines(t)
			types := tt.types
			if types == nil {
				types = []byte{secVeNCrypt}
			}
			opts := serverOptions{
				version: tt.version, types: types, vencrypt: tt.subs, cert: p.server,
				user: "alice", password: "hunter2",
			}
			cfg := tt.cfg
			if cfg.TLS.Pins == nil && !tt.noCA {
				cfg.TLS.RootCAs = p.roots
			}
			sink := &backendtest.Sink{}
			if tt.answer != nil {
				sink.Answer = answers(tt.answer...)
			}
			srv, _, r := runAgainst(t, opts, cfg, sink)
			if tt.err != nil {
				if err := r.wait(t); !errors.Is(err, tt.err) {
					t.Fatalf("Run = %v, want %v", err, tt.err)
				}
			} else {
				c := srv.accept(t)
				if c.security != secVeNCrypt {
					t.Errorf("security type %d, want VeNCrypt", c.security)
				}
				if err := r.stop(t); !errors.Is(err, context.Canceled) {
					t.Errorf("Run = %v, want context.Canceled", err)
				}
			}
			if got := srv.Subtypes(); tt.want != 0 && !slices.Equal(got, []uint32{tt.want}) {
				t.Errorf("subtypes chosen %v, want %d", got, tt.want)
			}
			if got := srv.Plains(); tt.plain != "" && !slices.Equal(got, []string{tt.plain}) || tt.plain == "" && len(got) > 0 {
				t.Errorf("Plain sent %q, want %q", got, tt.plain)
			}
			prompts := sink.Prompts()
			if len(prompts) != min(len(tt.prompt), 1) {
				t.Fatalf("%d prompts, want %d", len(prompts), min(len(tt.prompt), 1))
			}
			if len(prompts) == 1 {
				if !slices.Equal(prompts[0].Fields, tt.prompt) || !strings.Contains(prompts[0].Instruction, srv.addr) {
					t.Errorf("prompt %+v, want fields %+v for %s", prompts[0], tt.prompt, srv.addr)
				}
			}
		})
	}
}

// certFingerprint returns cert's SHA-256 fingerprint as openssl prints
// it, computed apart from fingerprint.
func certFingerprint(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.Raw)
	parts := make([]string, len(sum))
	for i, b := range sum {
		parts[i] = strings.ToUpper(hex.EncodeToString([]byte{b}))
	}
	return strings.Join(parts, ":")
}

func TestTLSPolicy(t *testing.T) {
	p := newPKI(t)
	leafPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: p.leaf.Raw}))
	sum := sha256.Sum256(p.leaf.Raw)
	selfFP := certFingerprint(p.selfSigned.Leaf)
	for _, tt := range []struct {
		name string
		cert tls.Certificate
		tls  TLS
		host string // to dial, 127.0.0.1 if empty
		err  error  // wrapped by a CertificateError; nil to connect
		warn bool   // an unverified certificate is logged
	}{
		{name: "a private CA", cert: p.server, tls: TLS{RootCAs: p.roots}},
		{name: "a private CA by name", cert: p.server, tls: TLS{RootCAs: p.roots}, host: "localhost"},
		{name: "the server name", cert: p.server, tls: TLS{RootCAs: p.roots, ServerName: "localhost"}},
		{name: "a wrong host", cert: p.server, tls: TLS{RootCAs: p.roots, ServerName: "vnc.example"}, err: x509.HostnameError{}},
		{name: "an untrusted root", cert: p.server, tls: TLS{RootCAs: p.otherRoots}, err: x509.UnknownAuthorityError{}},
		{name: "the system's roots", cert: p.server, err: errAny},
		{name: "self-signed", cert: p.selfSigned, tls: TLS{RootCAs: p.roots}, err: x509.UnknownAuthorityError{}},
		{name: "a pinned fingerprint", cert: p.selfSigned, tls: TLS{Pins: []string{selfFP}}},
		{name: "a pinned fingerprint in lower case without colons", cert: p.selfSigned, tls: TLS{Pins: []string{strings.ToLower(strings.ReplaceAll(selfFP, ":", ""))}}},
		{name: "a pinned fingerprint in base64", cert: p.server, tls: TLS{Pins: []string{"SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:])}}},
		{name: "a pinned certificate", cert: p.server, tls: TLS{Pins: []string{leafPEM}}},
		{name: "a pin ignores the name", cert: p.server, tls: TLS{Pins: []string{leafPEM}, ServerName: "vnc.example"}},
		{name: "another certificate pinned", cert: p.server, tls: TLS{Pins: []string{selfFP}}, err: errNotPinned},
		{name: "insecure", cert: p.selfSigned, tls: TLS{InsecureSkipVerify: true}, warn: true},
		{name: "insecure after the pins", cert: p.server, tls: TLS{Pins: []string{selfFP}, InsecureSkipVerify: true}, warn: true},
		{name: "insecure but pinned", cert: p.selfSigned, tls: TLS{Pins: []string{selfFP}, InsecureSkipVerify: true}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			checkGoroutines(t)
			var log logBuffer
			srv := newServer(t, serverOptions{types: []byte{secVeNCrypt}, vencrypt: []uint32{subX509None}, cert: tt.cert})
			addr := srv.addr
			if tt.host != "" {
				_, port, _ := net.SplitHostPort(addr)
				addr = net.JoinHostPort(tt.host, port)
			}
			b := newBackend(t, Config{Addr: addr, TLS: tt.tls, Logger: log.logger()})
			r := start(t, b, &backendtest.Sink{})
			if tt.err == nil {
				srv.accept(t)
				if err := r.stop(t); !errors.Is(err, context.Canceled) {
					t.Errorf("Run = %v, want context.Canceled", err)
				}
			} else {
				err := r.wait(t)
				var ce *CertificateError
				if !errors.As(err, &ce) {
					t.Fatalf("Run = %v, want a CertificateError", err)
				}
				if want := certFingerprint(tt.cert.Leaf); ce.Fingerprint != want {
					t.Errorf("fingerprint %s, want %s", ce.Fingerprint, want)
				}
				if !strings.Contains(err.Error(), ce.Fingerprint) {
					t.Errorf("Run = %v, which does not show the fingerprint", err)
				}
				switch want := tt.err.(type) {
				case x509.HostnameError:
					if !errors.As(err, &want) {
						t.Errorf("Run = %v, want a wrong host", err)
					}
				case x509.UnknownAuthorityError:
					if !errors.As(err, &want) {
						t.Errorf("Run = %v, want an unknown authority", err)
					}
				default:
					if !errors.Is(tt.err, errAny) && !errors.Is(err, tt.err) {
						t.Errorf("Run = %v, want %v", err, tt.err)
					}
				}
			}
			logged := log.String()
			warned := strings.Contains(logged, "unverified certificate")
			if warned != tt.warn {
				t.Errorf("warned %v, want %v; log:\n%s", warned, tt.warn, logged)
			}
			if warned && !strings.Contains(logged, certFingerprint(tt.cert.Leaf)) {
				t.Errorf("the warning does not show the fingerprint:\n%s", logged)
			}
		})
	}
}

func TestNewTLSPolicy(t *testing.T) {
	p := newPKI(t)
	fp := certFingerprint(p.leaf)
	for _, tt := range []struct {
		name string
		addr string
		tls  TLS
		err  string // in New's error; empty for none
		want tlsPolicy
	}{
		{name: "default", addr: "vnc.example:5901", want: tlsPolicy{serverName: "vnc.example"}},
		{name: "IPv6", addr: "[::1]", want: tlsPolicy{serverName: "::1"}},
		{name: "server name", addr: "10.0.0.2", tls: TLS{ServerName: "vnc.example"}, want: tlsPolicy{serverName: "vnc.example"}},
		{name: "require", addr: "x", tls: TLS{Require: true}, want: tlsPolicy{serverName: "x", require: true}},
		{name: "roots require", addr: "x", tls: TLS{RootCAs: p.roots}, want: tlsPolicy{serverName: "x", roots: p.roots, require: true}},
		{name: "insecure", addr: "x", tls: TLS{InsecureSkipVerify: true}, want: tlsPolicy{serverName: "x", insecure: true}},
		{name: "roots and pins", addr: "x", tls: TLS{RootCAs: p.roots, Pins: []string{fp}}, err: "not both"},
		{name: "short fingerprint", addr: "x", tls: TLS{Pins: []string{fp[:30]}}, err: "TLS pin 0"},
		{name: "not hex", addr: "x", tls: TLS{Pins: []string{strings.Repeat("zz", 32)}}, err: "TLS pin 0"},
		{name: "bad base64", addr: "x", tls: TLS{Pins: []string{fp, "SHA256:!!!"}}, err: "TLS pin 1"},
		{name: "a key, not a certificate", addr: "x", tls: TLS{Pins: []string{"-----BEGIN PUBLIC KEY-----\nAAAA\n-----END PUBLIC KEY-----\n"}}, err: "TLS pin 0"},
		{name: "a broken certificate", addr: "x", tls: TLS{Pins: []string{"-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n"}}, err: "TLS pin 0"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			b, err := New(Config{Addr: tt.addr, TLS: tt.tls})
			if tt.err != "" {
				if err == nil || !strings.Contains(err.Error(), tt.err) {
					t.Fatalf("New = %v, want an error with %q", err, tt.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got := b.tls
			if got.serverName != tt.want.serverName || got.roots != tt.want.roots || got.insecure != tt.want.insecure || got.require != tt.want.require || len(got.pins) != 0 {
				t.Errorf("policy %+v, want %+v", got, tt.want)
			}
		})
	}
	// Each form of a pin is the same pin.
	sum := sha256.Sum256(p.leaf.Raw)
	for _, s := range []string{
		fp,
		strings.ToLower(fp),
		strings.ReplaceAll(fp, ":", ""),
		"SHA256:" + base64.StdEncoding.EncodeToString(sum[:]),
		" " + string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: p.leaf.Raw})) + "\n",
	} {
		got, err := parsePin(s)
		if err != nil || got != sum {
			t.Errorf("parsePin(%q) = %x, %v; want %x", s, got, err, sum)
		}
	}
}

func TestVeNCryptScripts(t *testing.T) {
	errTLS := errors.New("handshake failure")
	creds := func() (string, string, error) { return "alice", "hunter2", nil }
	pw := func() (string, error) { return "hunter2", nil }
	vc := func(subs ...uint32) script {
		s := script{}.version("003.008").u8(1, secVeNCrypt).u8(0, 2, 0, byte(len(subs)))
		for _, st := range subs {
			s = s.u32(st)
		}
		return s
	}
	for _, tt := range []struct {
		name     string
		in       script
		startTLS func() error
		sent     string // what the client sends, if not empty
		err      string // in the error; empty for success
		is       error
	}{
		{
			name: "X509Plain", in: vc(subX509Plain).u8(1).u32(0).init(10, 10, ""),
			sent: "RFB 003.008\n\x13\x00\x02\x00\x00\x01\x06\x00\x00\x00\x05\x00\x00\x00\x07alicehunter2\x01",
		},
		{
			name: "X509Vnc", in: vc(subX509Vnc).u8(1).u8(make([]byte, 16)...).u32(0).init(10, 10, ""),
		},
		{name: "VeNCrypt 0.1", in: script{}.version("003.008").u8(1, secVeNCrypt).u8(0, 1), is: ErrNoSecurityType, err: "VeNCrypt 0.1", sent: "RFB 003.008\n\x13\x00\x00"},
		{name: "version refused", in: script{}.version("003.008").u8(1, secVeNCrypt).u8(0, 2, 1), is: ErrRefused},
		{name: "no subtypes", in: vc(), is: ErrNoSecurityType},
		{name: "TLS refused", in: vc(subX509None).u8(0), is: ErrRefused, err: "did not start TLS"},
		{name: "TLS fails", in: vc(subX509None).u8(1), startTLS: func() error { return errTLS }, is: errTLS},
		{name: "plain failed", in: vc(subX509Plain).u8(1).u32(1).str("bad password"), is: ErrAuthFailed, err: "bad password"},
		{name: "subtypes cut short", in: vc(subX509None)[:len(vc(subX509None))-2], is: io.ErrUnexpectedEOF},
		{name: "no subtype ack", in: vc(subX509None), is: io.ErrUnexpectedEOF},
		{name: "no version", in: script{}.version("003.008").u8(1, secVeNCrypt), is: io.ErrUnexpectedEOF},
		{name: "no version ack", in: script{}.version("003.008").u8(1, secVeNCrypt).u8(0, 2), is: io.ErrUnexpectedEOF},
		{name: "no subtype count", in: script{}.version("003.008").u8(1, secVeNCrypt).u8(0, 2, 0), is: io.ErrUnexpectedEOF},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var sent bytes.Buffer
			started := false
			a := auth{password: pw, plain: creds, hasUser: true, startTLS: func() error { started = true; return nil }}
			if tt.startTLS != nil {
				a.startTLS = tt.startTLS
			}
			_, err := handshake(bytes.NewReader(tt.in), &sent, a, defaultLimits)
			if tt.sent != "" && sent.String() != tt.sent {
				t.Errorf("client sent %q, want %q", sent.String(), tt.sent)
			}
			if tt.err == "" && tt.is == nil {
				if err != nil {
					t.Fatalf("handshake = %v", err)
				}
				if !started {
					t.Error("TLS did not start")
				}
				return
			}
			if err == nil {
				t.Fatal("handshake succeeded, want an error")
			}
			if tt.err != "" && !strings.Contains(err.Error(), tt.err) {
				t.Errorf("handshake = %v, want an error with %q", err, tt.err)
			}
			if tt.is != nil && !errors.Is(err, tt.is) {
				t.Errorf("handshake = %v, want %v", err, tt.is)
			}
		})
	}
}

func TestPlainCredentialErrors(t *testing.T) {
	errOwner := errors.New("the owner left")
	in := script{}.version("003.008").u8(1, secVeNCrypt).u8(0, 2, 0, 1).u32(subX509Plain).u8(1)
	for _, creds := range []func() (string, string, error){
		func() (string, string, error) { return "", "", errOwner },
		func() (string, string, error) { return strings.Repeat("a", maxCredential+1), "", nil },
	} {
		var sent bytes.Buffer
		a := auth{plain: creds, startTLS: func() error { return nil }}
		_, err := handshake(bytes.NewReader(in), &sent, a, defaultLimits)
		if err == nil || strings.Contains(sent.String(), "aaaa") {
			t.Errorf("handshake = %v, sent %d bytes; want an error and no credentials", err, sent.Len())
		}
	}
	// A prompt that does not answer every question fails.
	srv := newServer(t, serverOptions{types: []byte{secVeNCrypt}, vencrypt: []uint32{subX509Plain}, cert: newPKI(t).server})
	sink := &backendtest.Sink{Answer: answers("alice")}
	r := start(t, newBackend(t, Config{Addr: srv.addr, TLS: TLS{RootCAs: newPKI(t).roots}}), sink)
	if err := r.wait(t); err == nil || !strings.Contains(err.Error(), "1 answers for 2 questions") {
		t.Errorf("Run = %v, want an error about the answers", err)
	}
	if len(srv.Plains()) != 0 {
		t.Errorf("Plain sent %q after a short answer", srv.Plains())
	}
}

func TestDataBeforeTLS(t *testing.T) {
	// A server that sends anything after its ack, before the client's
	// hello, would have it read as plain text by the handshake.
	checkGoroutines(t)
	in := script{}.version("003.008").u8(1, secVeNCrypt).u8(0, 2, 0, 1).u32(subX509None).u8(1).u8([]byte("\x16\x03\x01 not a hello")...)
	addr := listen(t, func(nc net.Conn) {
		nc.Write(in)
		io.Copy(io.Discard, nc)
	})
	err := start(t, newBackend(t, Config{Addr: addr}), &backendtest.Sink{}).wait(t)
	if err == nil || !strings.Contains(err.Error(), "before the TLS handshake") {
		t.Errorf("Run = %v, want an error about data before TLS", err)
	}
}

func TestCertificateErrorMessage(t *testing.T) {
	err := error(&CertificateError{Subject: "CN=vnc", Fingerprint: "AB:CD", Err: errNotPinned})
	if got := err.Error(); !strings.Contains(got, `"CN=vnc"`) || !strings.Contains(got, "AB:CD") || !strings.Contains(got, "not pinned") {
		t.Errorf("Error() = %q", got)
	}
	if !errors.Is(err, errNotPinned) {
		t.Error("CertificateError does not unwrap")
	}
	var policy tlsPolicy
	if err := policy.verify(nil, "x", discardLogger); err == nil {
		t.Error("verify accepted no certificate")
	}
}
