// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package server

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"changkun.de/x/occamy/internal/auth"
	"changkun.de/x/occamy/internal/config"
)

const (
	alicePassword = "correct-horse-battery-staple"
	bobPassword   = "hunter2-hunter2"
)

// hashPassword hashes a password at bcrypt's lowest cost, to keep the
// tests fast; ParseV1 would refuse it, newServer does not check.
func hashPassword(t *testing.T, password string, cost int) string {
	t.Helper()
	h, err := bcrypt.GenerateFromPassword([]byte(password), cost)
	if err != nil {
		t.Fatal(err)
	}
	return string(h)
}

// testFile returns a v1 file with alice in ops and bob in dev, and a
// signing key that newServer creates in a temporary directory.
func testFile(t *testing.T) *config.File {
	t.Helper()
	return &config.File{
		Version: 1,
		Listen:  "127.0.0.1:0",
		Auth: config.Auth{
			SigningKeyFile:      filepath.Join(t.TempDir(), "signing.key"),
			AccessTokenLifetime: 15 * time.Minute,
			Users: []config.User{
				{Name: "alice", PasswordHash: hashPassword(t, alicePassword, bcrypt.MinCost), Groups: []string{"ops"}},
				{Name: "bob", PasswordHash: hashPassword(t, bobPassword, bcrypt.MinCost), Groups: []string{"dev"}},
			},
		},
		Guacd: config.Guacd{Address: config.DefaultGuacdAddress},
	}
}

// testServer is a v1 server's routes behind httptest.
type testServer struct {
	s   *server
	srv *httptest.Server
}

func newTestServer(t *testing.T, edit func(*config.File)) *testServer {
	t.Helper()
	f := testFile(t)
	if edit != nil {
		edit(f)
	}
	s, err := newServer(f)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(s.routes())
	srv.Config.ConnContext = connContext // as the public server's
	srv.Start()
	t.Cleanup(srv.Close)
	// Before the server closes, the sessions end, and their streams.
	t.Cleanup(func() {
		s.shutdown()
		if !s.streams.wait(10 * time.Second) {
			t.Error("streams still open after the sessions ended")
		}
	})
	return &testServer{s: s, srv: srv}
}

// signIn posts a user name and password to the token endpoint.
func (ts *testServer) signIn(t *testing.T, user, password string) reply {
	t.Helper()
	b, err := json.Marshal(tokenRequest{Username: user, Password: password})
	if err != nil {
		t.Fatal(err)
	}
	return do(t, http.MethodPost, ts.srv.URL+"/api/v1/auth/token", http.Header{"Content-Type": {"application/json"}}, string(b))
}

func TestHealthz(t *testing.T) {
	ts := newTestServer(t, nil)
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		r := do(t, method, ts.srv.URL+"/healthz", nil, "")
		if r.code != http.StatusOK || r.header.Get("Cache-Control") != "no-store" {
			t.Errorf("%s /healthz: %d %v", method, r.code, r.header)
		}
		if method == http.MethodGet && string(r.body) != `{"status":"ok"}` {
			t.Errorf("GET /healthz: %q", r.body)
		}
	}
	if r := do(t, http.MethodPost, ts.srv.URL+"/healthz", nil, ""); r.code != http.StatusMethodNotAllowed {
		t.Errorf("POST /healthz: %d", r.code)
	}
}

// TestAPIFallback checks that API v1 answers a path or method without a
// route with its JSON error, and leaves other paths to the ServeMux.
func TestAPIFallback(t *testing.T) {
	ts := newTestServer(t, nil)
	for _, tt := range []struct {
		method, path string
		status       int
		code, allow  string
	}{
		{http.MethodGet, "/api/v1/nope", http.StatusNotFound, codeNotFound, ""},
		{http.MethodGet, "/api/v1/", http.StatusNotFound, codeNotFound, ""},
		{http.MethodGet, "/api/v1", http.StatusNotFound, codeNotFound, ""},
		{http.MethodGet, "/api/v10", http.StatusNotFound, "", ""},
		{http.MethodGet, "/api/v1/auth/token", http.StatusMethodNotAllowed, codeMethodNotAllowed, "POST"},
		{http.MethodDelete, "/api/v1/auth/token", http.StatusMethodNotAllowed, codeMethodNotAllowed, "POST"},
		{http.MethodGet, "/nope", http.StatusNotFound, "", ""},
		{http.MethodGet, "/api/v0/connect", http.StatusNotFound, "", ""},
	} {
		r := do(t, tt.method, ts.srv.URL+tt.path, nil, "")
		if r.code != tt.status || r.header.Get("Allow") != tt.allow {
			t.Errorf("%s %s: %d, Allow %q; want %d, %q", tt.method, tt.path, r.code, r.header.Get("Allow"), tt.status, tt.allow)
			continue
		}
		if tt.code == "" {
			if strings.HasPrefix(r.header.Get("Content-Type"), "application/json") {
				t.Errorf("%s %s: JSON outside API v1: %s", tt.method, tt.path, r.body)
			}
			continue
		}
		if e := decodeError(t, r.body); e.Code != tt.code {
			t.Errorf("%s %s: code %q, want %q", tt.method, tt.path, e.Code, tt.code)
		}
	}
}

// recordCompare replaces the server's bcrypt comparison with one that
// records the hashes it compares against.
func recordCompare(s *staticUsers) func() []string {
	var mu sync.Mutex
	var hashes []string
	compare := s.compare
	s.compare = func(hash, password []byte) error {
		mu.Lock()
		hashes = append(hashes, string(hash))
		mu.Unlock()
		return compare(hash, password)
	}
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(hashes)
	}
}

func TestTokenEndpoint(t *testing.T) {
	ts := newTestServer(t, nil)
	alice := string(ts.s.signIn.users["alice"].hash)
	dummy := string(ts.s.signIn.dummy)
	jsonType := http.Header{"Content-Type": {"application/json"}}
	for _, tt := range []struct {
		name     string
		header   http.Header
		body     string
		status   int
		code     string   // error code; empty for a token
		compared []string // hashes bcrypt compared against
	}{
		{"right password", jsonType, `{"username":"alice","password":"` + alicePassword + `"}`, http.StatusOK, "", []string{alice}},
		{"json with charset", http.Header{"Content-Type": {"application/json; charset=utf-8"}}, `{"username":"alice","password":"` + alicePassword + `"}`, http.StatusOK, "", []string{alice}},
		{"wrong password", jsonType, `{"username":"alice","password":"` + bobPassword + `"}`, http.StatusUnauthorized, codeUnauthorized, []string{alice}},
		{"unknown user", jsonType, `{"username":"mallory","password":"` + alicePassword + `"}`, http.StatusUnauthorized, codeUnauthorized, []string{dummy}},
		{"user name in another case", jsonType, `{"username":"Alice","password":"` + alicePassword + `"}`, http.StatusUnauthorized, codeUnauthorized, []string{dummy}},
		{"no password", jsonType, `{"username":"alice"}`, http.StatusBadRequest, codeBadRequest, nil},
		{"no user name", jsonType, `{"password":"` + alicePassword + `"}`, http.StatusBadRequest, codeBadRequest, nil},
		{"unknown field", jsonType, `{"username":"alice","password":"x","grant_type":"password"}`, http.StatusBadRequest, codeBadRequest, nil},
		{"two values", jsonType, `{"username":"alice","password":"x"} {}`, http.StatusBadRequest, codeBadRequest, nil},
		{"not json", jsonType, `username=alice`, http.StatusBadRequest, codeBadRequest, nil},
		{"wrong type", jsonType, `{"username":"alice","password":1}`, http.StatusBadRequest, codeBadRequest, nil},
		{"form", http.Header{"Content-Type": {"application/x-www-form-urlencoded"}}, "username=alice&password=" + alicePassword, http.StatusUnsupportedMediaType, codeUnsupportedMediaType, nil},
		{"no content type", nil, `{"username":"alice","password":"` + alicePassword + `"}`, http.StatusUnsupportedMediaType, codeUnsupportedMediaType, nil},
		{"too large", jsonType, `{"username":"alice","password":"` + strings.Repeat("x", maxJSONBody) + `"}`, http.StatusRequestEntityTooLarge, codeTooLarge, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			compared := recordCompare(ts.s.signIn)
			r := do(t, http.MethodPost, ts.srv.URL+"/api/v1/auth/token", tt.header, tt.body)
			if r.code != tt.status {
				t.Fatalf("%d %s, want %d", r.code, r.body, tt.status)
			}
			if got := compared(); !slices.Equal(got, tt.compared) {
				t.Errorf("bcrypt compared against %q, want %q", got, tt.compared)
			}
			if tt.code != "" {
				if e := decodeError(t, r.body); e.Code != tt.code {
					t.Errorf("code %q, want %q", e.Code, tt.code)
				}
				if strings.Contains(string(r.body), alicePassword) {
					t.Errorf("body repeats the password: %s", r.body)
				}
				return
			}
			checkToken(t, ts.s, r, "alice", []string{"ops"}, 15*time.Minute)
		})
	}
}

// checkToken checks a token response and the access token in it.
func checkToken(t *testing.T, s *server, r reply, user string, groups []string, ttl time.Duration) {
	t.Helper()
	if r.header.Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control %q, want no-store", r.header.Get("Cache-Control"))
	}
	var tr tokenResponse
	dec := json.NewDecoder(strings.NewReader(string(r.body)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&tr); err != nil {
		t.Fatalf("%s: %v", r.body, err)
	}
	if tr.TokenType != "Bearer" || tr.ExpiresIn != int64(ttl/time.Second) {
		t.Errorf("token_type %q, expires_in %d; want Bearer, %d", tr.TokenType, tr.ExpiresIn, int64(ttl/time.Second))
	}
	c, err := s.verifier.VerifyAccess(tr.AccessToken)
	if err != nil {
		t.Fatalf("the access token does not verify: %v", err)
	}
	if c.Subject != user || !slices.Equal(c.Groups, groups) || c.Issuer != auth.Issuer || c.IdP != auth.IdPLocal ||
		c.ExpiresAt.Sub(c.IssuedAt.Time) != ttl {
		t.Errorf("claims %+v, want %s in %v for %v", c, user, groups, ttl)
	}
}

// TestTokenUnknownUserSameReply checks that a wrong password and an
// unknown user get the same reply, and that the dummy hash an unknown
// user is compared with fails like a wrong password.
func TestTokenUnknownUserSameReply(t *testing.T) {
	ts := newTestServer(t, func(f *config.File) { f.Auth.AccessTokenLifetime = time.Hour })
	wrong := ts.signIn(t, "alice", bobPassword)
	unknown := ts.signIn(t, "mallory", bobPassword)
	if wrong.code != unknown.code || string(wrong.body) != string(unknown.body) ||
		wrong.header.Get("WWW-Authenticate") != unknown.header.Get("WWW-Authenticate") {
		t.Errorf("wrong password: %d %s; unknown user: %d %s", wrong.code, wrong.body, unknown.code, unknown.body)
	}
	if err := bcrypt.CompareHashAndPassword(ts.s.signIn.dummy, []byte("")); !errors.Is(err, bcrypt.ErrMismatchedHashAndPassword) {
		t.Errorf("the dummy hash compares with %v", err)
	}
	checkToken(t, ts.s, ts.signIn(t, "bob", bobPassword), "bob", []string{"dev"}, time.Hour)
}

// TestDummyHashCost checks that the dummy hash costs what the users'
// hashes cost, so that its comparison takes as long.
func TestDummyHashCost(t *testing.T) {
	for _, tt := range []struct {
		name  string
		costs []int
		want  int
		warn  bool
	}{
		{"same cost", []int{bcrypt.MinCost, bcrypt.MinCost}, bcrypt.MinCost, false},
		{"mixed costs", []int{bcrypt.MinCost, bcrypt.MinCost + 1}, bcrypt.MinCost + 1, true},
		{"no users", nil, config.MinBcryptCost, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			logs := captureLog(t)
			var users []config.User
			for i, c := range tt.costs {
				users = append(users, config.User{Name: string(rune('a' + i)), PasswordHash: hashPassword(t, "pw", c)})
			}
			s, err := newStaticUsers(users, nil, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			if got, err := bcrypt.Cost(s.dummy); err != nil || got != tt.want {
				t.Errorf("dummy cost %d, %v; want %d", got, err, tt.want)
			}
			if warned := strings.Contains(logs(), "different bcrypt costs"); warned != tt.warn {
				t.Errorf("warned %v, want %v:\n%s", warned, tt.warn, logs())
			}
		})
	}
}

// TestLoadWarningsLogged checks that newServer logs loadWarnings: ssh
// connections never go through guacd, so guacd.params.ssh is unused.
func TestLoadWarningsLogged(t *testing.T) {
	logs := captureLog(t)
	f := testFile(t)
	f.Guacd.Params = map[string]map[string]string{"ssh": {"font-size": "12"}}
	if _, err := newServer(f); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs(), "warning: guacd.params.ssh sets font-size, which no session reads") {
		t.Errorf("no warning for guacd.params.ssh:\n%s", logs())
	}
}

// TestSigningKey checks that newServer creates a missing signing key
// with a warning, and that a second start uses it, so tokens survive a
// restart.
func TestSigningKey(t *testing.T) {
	logs := captureLog(t)
	f := testFile(t)
	first, err := newServer(f)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs(), "warning: created a new signing key in "+f.Auth.SigningKeyFile) {
		t.Errorf("no warning for the created key:\n%s", logs())
	}
	token, err := first.signIn.signer.Access("alice", nil, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	before := logs()
	second, err := newServer(f)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(logs(), "created a new signing key") != strings.Count(before, "created a new signing key") {
		t.Errorf("warned again for an existing key:\n%s", logs())
	}
	if _, err := second.verifier.VerifyAccess(token); err != nil {
		t.Errorf("a token from before the restart: %v", err)
	}

	f.Auth.SigningKeyFile = filepath.Join(t.TempDir(), "missing", "signing.key")
	if _, err := newServer(f); err == nil {
		t.Error("newServer with a signing key in a missing directory did not fail")
	}
}

// writeIssuerKey writes a trusted issuer's public key and returns the
// path.
func writeIssuerKey(t *testing.T, key ed25519.PrivateKey) string {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(key.Public())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "issuer.pub")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestTrustedIssuers(t *testing.T) {
	k := newKeys(t)
	path := writeIssuerKey(t, k.gateway)
	ts := newTestServer(t, func(f *config.File) {
		f.Auth.TrustedIssuers = []config.TrustedIssuer{{Issuer: "gateway", PublicKeyFile: path}}
	})
	if c, err := ts.s.verifier.VerifyAccess(signClaims(t, k.gateway, access("gateway", "carol", "ops"))); err != nil || c.Subject != "carol" {
		t.Errorf("the gateway's token: %+v, %v", c, err)
	}
	if _, err := ts.s.verifier.VerifyAccess(signClaims(t, k.gateway, access(auth.Issuer, "carol"))); err == nil {
		t.Error("the gateway signed for occamyd")
	}

	for _, issuers := range [][]config.TrustedIssuer{
		{{Issuer: auth.Issuer, PublicKeyFile: path}},
		{{Issuer: "gateway", PublicKeyFile: path}, {Issuer: "gateway", PublicKeyFile: path}},
		{{Issuer: "gateway", PublicKeyFile: path + ".missing"}},
	} {
		f := testFile(t)
		f.Auth.TrustedIssuers = issuers
		if _, err := newServer(f); err == nil {
			t.Errorf("newServer with trusted issuers %+v did not fail", issuers)
		}
	}
}

// TestNewServerFromV1File starts from a file as ParseV1 reads it.
func TestNewServerFromV1File(t *testing.T) {
	dir := t.TempDir()
	raw := "version: 1\nlisten: 127.0.0.1:0\nlegacy_v0: true\nauth:\n  signing_key_file: " + filepath.Join(dir, "signing.key") +
		"\n  access_token_lifetime: 5m\n  users:\n    - {name: alice, password_hash: \"" +
		hashPassword(t, alicePassword, config.MinBcryptCost) + "\", groups: [ops]}\n"
	f, err := config.ParseV1([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	s, err := newServer(f)
	if err != nil {
		t.Fatal(err)
	}
	ts := &testServer{s: s, srv: httptest.NewServer(s.routes())}
	defer ts.srv.Close()
	checkToken(t, s, ts.signIn(t, "alice", alicePassword), "alice", []string{"ops"}, 5*time.Minute)
	if s.v0 == nil {
		t.Error("legacy_v0: true serves no v0 routes")
	}
}

// TestLegacyV0 checks that a v1 file serves the v0 routes only with
// legacy_v0, without the web client and pprof, and with a key of its own.
func TestLegacyV0(t *testing.T) {
	off := newTestServer(t, nil)
	on := newTestServer(t, func(f *config.File) { f.LegacyV0 = true })
	login := `{"protocol":"vnc","host":"desktop:5900","password":"pw"}`
	jsonType := http.Header{"Content-Type": {"application/json"}}

	if r := do(t, http.MethodPost, off.srv.URL+"/api/v0/login", jsonType, login); r.code != http.StatusNotFound {
		t.Errorf("legacy_v0 off: login %d", r.code)
	}
	r := do(t, http.MethodPost, on.srv.URL+"/api/v0/login", jsonType, login)
	if r.code != http.StatusOK || !strings.HasPrefix(string(r.body), `{"code":200,"expire":"`) {
		t.Fatalf("legacy_v0 on: login %d %s", r.code, r.body)
	}
	if r := do(t, http.MethodGet, on.srv.URL+"/api/v0/connect", nil, ""); r.code != http.StatusUnauthorized ||
		string(r.body) != `{"code":401,"message":"cookie token is empty"}` {
		t.Errorf("legacy_v0 on: connect without a token: %d %s", r.code, r.body)
	}
	for _, path := range []string{"/static/", "/debug/pprof/", "/debug/vars"} {
		if r := do(t, http.MethodGet, on.srv.URL+path, nil, ""); r.code != http.StatusNotFound {
			t.Errorf("legacy_v0 on: GET %s: %d", path, r.code)
		}
	}
	// Each process makes its own v0 key.
	again := newTestServer(t, func(f *config.File) { f.LegacyV0 = true })
	if string(on.s.v0.tokens.key) == string(again.s.v0.tokens.key) || len(on.s.v0.tokens.key) < 16 {
		t.Errorf("v0 keys %q and %q", on.s.v0.tokens.key, again.s.v0.tokens.key)
	}
}

// testCert returns a self-signed certificate for 127.0.0.1 and a pool
// that trusts it.
func testCert(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	certPEM, keyPEM := testCertPEM(t)
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(certPEM)
	return cert, pool
}

func testCertPEM(t *testing.T) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "occamy test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1)},
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kder, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kder})
}

// testClient trusts pool and offers HTTP/2.
func testClient(pool *x509.CertPool) *http.Client {
	return &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig:   &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
			ForceAttemptHTTP2: true,
		},
	}
}

// serving runs serveUntil in the background until the test ends, and
// returns a function that stops it and returns its error.
func serving(t *testing.T, drain time.Duration, public *http.Server, ln net.Listener, debug *http.Server, dln net.Listener) func() error {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serveUntil(ctx, drain, public, ln, debug, dln) }()
	var once sync.Once
	var err error
	stop := func() error {
		once.Do(func() {
			cancel()
			err = <-done
		})
		return err
	}
	t.Cleanup(func() { stop() })
	return stop
}

func localListener(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return ln
}

func TestTLS(t *testing.T) {
	certPEM, keyPEM := testCertPEM(t)
	dir := t.TempDir()
	certFile, keyFile := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	for name, b := range map[string][]byte{certFile: certPEM, keyFile: keyPEM} {
		if err := os.WriteFile(name, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	f := testFile(t)
	f.TLS = config.TLS{Cert: certFile, Key: keyFile}
	s, err := newServer(f)
	if err != nil {
		t.Fatal(err)
	}
	ln := localListener(t)
	serving(t, time.Second, newPublicServer(s.routes(), s.tls), ln, nil, nil)

	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(certPEM)
	resp, err := testClient(pool).Get("https://" + ln.Addr().String() + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.TLS == nil || resp.Proto != "HTTP/1.1" {
		t.Errorf("GET /healthz over TLS: %d %s, TLS %v", resp.StatusCode, resp.Proto, resp.TLS != nil)
	}
	// TLS 1.2 is the least it speaks.
	for _, version := range []uint16{tls.VersionTLS10, tls.VersionTLS11} {
		c, err := tls.Dial("tcp", ln.Addr().String(), &tls.Config{
			RootCAs: pool, MinVersion: tls.VersionTLS10, MaxVersion: version, // #nosec G402 -- the test offers old versions on purpose
		})
		if err == nil {
			c.Close()
			t.Errorf("a TLS handshake at version %x succeeded", version)
		}
	}
	// Plain HTTP on the TLS port is refused.
	if resp, err := http.Get("http://" + ln.Addr().String() + "/healthz"); err == nil {
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("plain HTTP to the TLS listener: %d", resp.StatusCode)
		}
	}

	f.TLS.Key = certFile
	if _, err := newServer(f); err == nil {
		t.Error("newServer with a certificate as the key did not fail")
	}
}

// TestDebugListener checks that the debug listener is off by default,
// and when on serves expvar and pprof on its own listener only.
func TestDebugListener(t *testing.T) {
	f := testFile(t)
	ln, dln, err := listen(f)
	if err != nil {
		t.Fatal(err)
	}
	ln.Close()
	if dln != nil {
		dln.Close()
		t.Fatal("a debug listener without debug.address")
	}

	f.Debug.Address = "127.0.0.1:0"
	ln, dln, err = listen(f)
	if err != nil {
		t.Fatal(err)
	}
	if dln == nil {
		t.Fatal("no debug listener with debug.address")
	}
	ts := newTestServer(t, func(f *config.File) { f.LegacyV0 = true })
	serving(t, time.Second, newPublicServer(ts.s.routes(), nil), ln, newDebugServer(), dln)

	for _, tt := range []struct {
		path        string
		public, dbg int
	}{
		{"/debug/vars", http.StatusNotFound, http.StatusOK},
		{"/debug/pprof/", http.StatusNotFound, http.StatusOK},
		{"/debug/pprof/cmdline", http.StatusNotFound, http.StatusOK},
		{"/debug/pprof/goroutine?debug=1", http.StatusNotFound, http.StatusOK},
		{"/healthz", http.StatusOK, http.StatusNotFound},
	} {
		if r := do(t, http.MethodGet, "http://"+ln.Addr().String()+tt.path, nil, ""); r.code != tt.public {
			t.Errorf("public GET %s: %d, want %d", tt.path, r.code, tt.public)
		}
		if r := do(t, http.MethodGet, "http://"+dln.Addr().String()+tt.path, nil, ""); r.code != tt.dbg {
			t.Errorf("debug GET %s: %d, want %d", tt.path, r.code, tt.dbg)
		}
	}
	r := do(t, http.MethodGet, "http://"+dln.Addr().String()+"/debug/vars", nil, "")
	if !strings.Contains(string(r.body), `"memstats"`) {
		t.Errorf("/debug/vars: %.200s", r.body)
	}
	// occamyd's own: goroutines, and open files where it can count them.
	var vars struct {
		Goroutines *int `json:"goroutines"`
		FDs        *int `json:"fds"`
	}
	if err := json.Unmarshal(r.body, &vars); err != nil || vars.Goroutines == nil || vars.FDs == nil {
		t.Fatalf("/debug/vars without goroutines and fds (%v): %.200s", err, r.body)
	}
	if *vars.Goroutines < 1 {
		t.Errorf("goroutines = %d", *vars.Goroutines)
	}
	switch runtime.GOOS {
	case "linux", "darwin":
		// Standard input, output and error, and the two listeners.
		if *vars.FDs < 5 {
			t.Errorf("fds = %d, want at least 5", *vars.FDs)
		}
	default:
		if *vars.FDs != -1 && *vars.FDs < 5 {
			t.Errorf("fds = %d, want -1 or at least 5", *vars.FDs)
		}
	}
}

func TestLoopback(t *testing.T) {
	for addr, want := range map[string]bool{
		"localhost:6060": true, "127.0.0.1:6060": true, "[::1]:6060": true,
		":6060": false, "0.0.0.0:6060": false, "10.0.0.1:6060": false, "nope": false,
	} {
		if got := loopback(addr); got != want {
			t.Errorf("loopback(%q) = %v, want %v", addr, got, want)
		}
	}
}

// blocking returns a handler that blocks until release is closed, and
// the channel on which it announces each request it has begun.
func blocking(release <-chan struct{}) (http.Handler, <-chan struct{}) {
	started := make(chan struct{}, 1)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		select {
		case <-release:
			_, _ = w.Write([]byte("done"))
		case <-r.Context().Done():
		}
	}), started
}

// TestGracefulShutdown checks that a shutdown stops accepting at once and
// lets a request in flight finish.
func TestGracefulShutdown(t *testing.T) {
	release := make(chan struct{})
	h, started := blocking(release)
	ln := localListener(t)
	addr := ln.Addr().String()
	stop := serving(t, 10*time.Second, newPublicServer(h, nil), ln, nil, nil)

	type result struct {
		body string
		err  error
	}
	got := make(chan result, 1)
	go func() {
		resp, err := http.Get("http://" + addr + "/")
		if err != nil {
			got <- result{err: err}
			return
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		got <- result{string(b), err}
	}()
	<-started

	stopped := make(chan error, 1)
	go func() { stopped <- stop() }()
	// The listener closes at once; the request in flight holds the rest.
	waitFor(t, "the listener to close", func() bool {
		c, err := net.Dial("tcp", addr)
		if err == nil {
			c.Close()
		}
		return err != nil
	})
	select {
	case err := <-stopped:
		t.Fatalf("serveUntil returned %v with a request in flight", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if r := <-got; r.err != nil || r.body != "done" {
		t.Errorf("the request in flight: %q, %v; want done", r.body, r.err)
	}
	if err := <-stopped; err != nil {
		t.Errorf("serveUntil: %v", err)
	}
}

// TestShutdownDrainTimeout checks that a shutdown waits for a request in
// flight no longer than the drain timeout, then closes it with a warning,
// and that a drain that runs out is no error.
func TestShutdownDrainTimeout(t *testing.T) {
	logs := captureLog(t)
	release := make(chan struct{})
	defer close(release)
	h, started := blocking(release)
	ln := localListener(t)
	stop := serving(t, 100*time.Millisecond, newPublicServer(h, nil), ln, nil, nil)
	failed := make(chan error, 1)
	go func() {
		resp, err := http.Get("http://" + ln.Addr().String() + "/")
		if err == nil {
			resp.Body.Close()
		}
		failed <- err
	}()
	<-started
	start := time.Now()
	if err := stop(); err != nil {
		t.Errorf("serveUntil after a drain that ran out: %v, want nil", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("shutdown took %v", d)
	}
	if err := <-failed; err == nil {
		t.Error("the request outlived the drain")
	}
	if !strings.Contains(logs(), "warning: requests still in flight after the 100ms drain were cut") {
		t.Errorf("no warning for the cut request:\n%s", logs())
	}
}

// TestServeUntilFails checks that serveUntil returns when a listener
// fails, and stops the other server.
func TestServeUntilFails(t *testing.T) {
	ln, dln := localListener(t), localListener(t)
	dln.Close()
	done := make(chan error, 1)
	go func() {
		done <- serveUntil(context.Background(), time.Second, newPublicServer(http.NotFoundHandler(), nil), ln, newDebugServer(), dln)
	}()
	select {
	case err := <-done:
		if err == nil || errors.Is(err, http.ErrServerClosed) {
			t.Errorf("serveUntil: %v, want the listener's error", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serveUntil did not return")
	}
	if c, err := net.Dial("tcp", ln.Addr().String()); err == nil {
		c.Close()
		t.Error("the public listener is still open")
	}
}
