// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package kubernetes_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"

	"changkun.de/x/occamy/internal/backend/backendtest"
	"changkun.de/x/occamy/internal/backend/kubernetes"
	"changkun.de/x/occamy/internal/backend/kubernetes/kubetest"
)

// testCA is a CA that issues client certificates.
type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
}

func newCA() (*testCA, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test cluster CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	return &testCA{cert: cert, key: key}, nil
}

func (ca *testCA) pool() *x509.CertPool {
	p := x509.NewCertPool()
	p.AddCert(ca.cert)
	return p
}

// issue returns a client certificate for user and its key, in PEM.
func (ca *testCA) issue(user string) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: user, Organization: []string{"system:masters"}},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		return nil, nil, err
	}
	kder, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kder}), nil
}

// base64Pin is cert's fingerprint in the SHA256: form.
func base64Pin(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.Raw)
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:])
}

// sign issues a certificate from tmpl for a new key, signed by parent
// and its key.
func sign(t *testing.T, tmpl, parent *x509.Certificate, parentKey *ecdsa.PrivateKey) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl.NotBefore, tmpl.NotAfter = time.Now().Add(-time.Hour), time.Now().Add(time.Hour)
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

// TestCertificateChain checks an API server whose certificate a CA's
// intermediate signed: the chain it presents is verified through the
// intermediate to the configured CA, and without the intermediate the
// certificate is refused, since nothing fetches it.
func TestCertificateChain(t *testing.T) {
	ca, err := newCA()
	if err != nil {
		t.Fatal(err)
	}
	inter, interKey := sign(t, &x509.Certificate{
		SerialNumber:          big.NewInt(10),
		Subject:               pkix.Name{CommonName: "test intermediate CA"},
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}, ca.cert, ca.key)
	leaf, leafKey := sign(t, &x509.Certificate{
		SerialNumber: big.NewInt(11),
		Subject:      pkix.Name{CommonName: "kube-apiserver"},
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1)},
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}, inter, interKey)
	for _, tc := range []struct {
		name  string
		chain [][]byte
		ok    bool
	}{
		{"with the intermediate", [][]byte{leaf.Raw, inter.Raw}, true},
		{"without it", [][]byte{leaf.Raw}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checkGoroutines(t)
			s := newServer(t, kubetest.Options{Certificate: &tls.Certificate{Certificate: tc.chain, PrivateKey: leafKey}})
			cfg := config(s)
			cfg.TLS = kubernetes.TLS{RootCAs: ca.pool()}
			b := newBackend(t, cfg)
			r := start(t, b, &backendtest.Sink{})
			if !tc.ok {
				var ce *kubernetes.CertificateError
				if err := r.wait(t); !errors.As(err, &ce) {
					t.Errorf("Run = %v, want a CertificateError", err)
				}
				return
			}
			e := accept(t, s)
			if err := e.Exit(0); err != nil {
				t.Fatal(err)
			}
			if err := r.wait(t); err != nil {
				t.Errorf("Run = %v, want nil", err)
			}
		})
	}
}

// TestCertificateErrorCut checks that the error for a refused
// certificate quotes at most 256 bytes of each thing the server chose,
// its subject and the names that x509 lists, and still says why, even
// after a long request URL.
func TestCertificateErrorCut(t *testing.T) {
	checkGoroutines(t)
	ca, err := newCA()
	if err != nil {
		t.Fatal(err)
	}
	// x509 lists up to 99 names, and counts more.
	names := make([]string, 99)
	for i := range names {
		names[i] = fmt.Sprintf("name-%03d.%s.cluster.example", i, strings.Repeat("n", 60))
	}
	leaf, leafKey := sign(t, &x509.Certificate{
		SerialNumber: big.NewInt(12),
		Subject:      pkix.Name{CommonName: "kube-apiserver", Organization: []string{strings.Repeat("o", 5000) + "\x1b[31m"}},
		DNSNames:     names,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}, ca.cert, ca.key)
	s := newServer(t, kubetest.Options{Certificate: &tls.Certificate{Certificate: [][]byte{leaf.Raw}, PrivateKey: leafKey}})
	cfg := config(s)
	// A name the certificate lacks, so that x509 lists the ones it has.
	cfg.TLS = kubernetes.TLS{RootCAs: ca.pool(), ServerName: "api.example.org"}
	cfg.Command = []string{"sh", "-c", strings.Repeat("echo a long command; ", 50)}
	b := newBackend(t, cfg)
	err = run(t, b, &backendtest.Sink{})
	var ce *kubernetes.CertificateError
	if !errors.As(err, &ce) {
		t.Fatalf("Run = %v, want a CertificateError", err)
	}
	msg := err.Error()
	if len(msg) > 1024 {
		t.Errorf("an error of %d bytes: %.300s", len(msg), msg)
	}
	if !strings.Contains(msg, "is not trusted: x509: certificate is valid for name-000") {
		t.Errorf("the error does not say why: %s", msg)
	}
	if strings.ContainsRune(msg, 0x1b) {
		t.Errorf("a control character in %q", msg)
	}

	// Insecure mode accepts the certificate, and its warning cuts the
	// same things.
	var l logs
	cfg.TLS.InsecureSkipVerify = true
	cfg.Logger = l.logger()
	b = newBackend(t, cfg)
	r := start(t, b, &backendtest.Sink{})
	if err := accept(t, s).Exit(0); err != nil {
		t.Fatal(err)
	}
	if err := r.wait(t); err != nil {
		t.Errorf("Run in insecure mode = %v, want nil", err)
	}
	warning := l.String()
	if !strings.Contains(warning, "accepting an unverified certificate") || !strings.Contains(warning, "name-000") {
		t.Errorf("no warning with the reason: %s", warning)
	}
	if len(warning) > 1500 {
		t.Errorf("a warning of %d bytes: %.300s", len(warning), warning)
	}
}
