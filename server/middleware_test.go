// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package server

import (
	"bytes"
	"crypto/ed25519"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"changkun.de/x/occamy/internal/auth"
)

// captureLog sends the log to a buffer until the test ends, and returns a
// function that reads it.
func captureLog(t *testing.T) func() string {
	t.Helper()
	var buf bytes.Buffer
	var mu sync.Mutex
	log.SetOutput(writerFunc(func(p []byte) (int, error) {
		mu.Lock()
		defer mu.Unlock()
		return buf.Write(p)
	}))
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return func() string {
		mu.Lock()
		defer mu.Unlock()
		return buf.String()
	}
}

// decodeError reads an API v1 error body, failing the test if the body
// has any other shape.
func decodeError(t *testing.T, body []byte) errorBody {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	var e apiError
	if err := dec.Decode(&e); err != nil || e.Error.Code == "" || e.Error.Message == "" {
		t.Fatalf("body %q is not an API v1 error: %v", body, err)
	}
	return e.Error
}

func TestRecovery(t *testing.T) {
	logs := captureLog(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /boom", func(http.ResponseWriter, *http.Request) {
		panic("boom")
	})
	mux.HandleFunc("GET /late", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, "partial")
		panic("late")
	})
	mux.HandleFunc("GET /abort", func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler)
	})
	mux.Handle("GET /v0", recoverV0(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("v0")
	})))
	srv := httptest.NewServer(logRequests(recovery(mux)))
	defer srv.Close()

	r := do(t, http.MethodGet, srv.URL+"/boom?token=secret", nil, "")
	if r.code != http.StatusInternalServerError || r.header.Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatalf("panic: %d %q %s, want 500 JSON", r.code, r.header.Get("Content-Type"), r.body)
	}
	if e := decodeError(t, r.body); e.Code != codeInternal {
		t.Errorf("panic: code %q, want %q", e.Code, codeInternal)
	}
	got := logs()
	for _, want := range []string{
		`panic serving "GET" "/boom": boom`,
		"goroutine ",                // the stack
		"server.TestRecovery.func1", // the panicking handler in it
		`"GET" "/boom" 500 `,        // the request log
	} {
		if !strings.Contains(got, want) {
			t.Errorf("log lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "secret") {
		t.Errorf("log has the query string:\n%s", got)
	}

	// A response under way is not written over.
	if r := do(t, http.MethodGet, srv.URL+"/late", nil, ""); r.code != http.StatusAccepted || string(r.body) != "partial" {
		t.Errorf("late panic: %d %q, want 202 partial", r.code, r.body)
	}
	// http.ErrAbortHandler still aborts the response, unlogged.
	if resp, err := http.Get(srv.URL + "/abort"); err == nil {
		resp.Body.Close()
		t.Errorf("abort: %d, want a dropped connection", resp.StatusCode)
	}
	if strings.Contains(logs(), "panic serving \"GET\" \"/abort\"") {
		t.Errorf("abort was logged as a panic:\n%s", logs())
	}
	// A v0 route answers as gin did: 500, no body.
	if r := do(t, http.MethodGet, srv.URL+"/v0", nil, ""); r.code != http.StatusInternalServerError || len(r.body) != 0 {
		t.Errorf("v0 panic: %d %q, want 500 without a body", r.code, r.body)
	}
}

// TestRecoveryV0Routes checks that the v0 routes recover as gin did on a
// v0 file's mux.
func TestRecoveryV0Routes(t *testing.T) {
	captureLog(t)
	rp := newRouteProxy(t, true, false)
	rp.p.tokens.now = nil // login panics on the nil clock
	r := rp.login(t, "application/json", `{"protocol":"vnc","host":"h:1","password":"p"}`)
	if r.code != http.StatusInternalServerError || len(r.body) != 0 {
		t.Errorf("login panic: %d %q, want 500 without a body", r.code, r.body)
	}
}

func TestLimitBodies(t *testing.T) {
	var got error
	h := limitBodies(10)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, got = io.ReadAll(r.Body)
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", strings.NewReader("0123456789")))
	if got != nil {
		t.Errorf("10 bytes: %v", got)
	}
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", strings.NewReader("0123456789x")))
	var tooLarge *http.MaxBytesError
	if !errors.As(got, &tooLarge) {
		t.Errorf("11 bytes: %v, want *http.MaxBytesError", got)
	}
}

// keys holds the keys of occamyd and of the issuers in the tests.
type keys struct {
	own, gateway, stranger ed25519.PrivateKey
	verifier               *auth.Verifier
}

func newKeys(t *testing.T) *keys {
	t.Helper()
	k := &keys{}
	for _, p := range []*ed25519.PrivateKey{&k.own, &k.gateway, &k.stranger} {
		_, priv, err := ed25519.GenerateKey(nil)
		if err != nil {
			t.Fatal(err)
		}
		*p = priv
	}
	k.verifier = auth.NewVerifier(auth.Audience, map[string]ed25519.PublicKey{
		auth.Issuer: k.own.Public().(ed25519.PublicKey),
		"gateway":   k.gateway.Public().(ed25519.PublicKey),
	})
	return k
}

// signClaims signs claims with key, filling in an hour's lifetime from
// now if they have none.
func signClaims(t *testing.T, key ed25519.PrivateKey, c auth.Claims) string {
	t.Helper()
	if c.IssuedAt == nil {
		now := time.Now()
		c.IssuedAt = jwt.NewNumericDate(now)
		c.ExpiresAt = jwt.NewNumericDate(now.Add(time.Hour))
	}
	s, err := jwt.NewWithClaims(jwt.SigningMethodEdDSA, &c).SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func access(issuer, subject string, groups ...string) auth.Claims {
	return auth.Claims{
		Kind:   auth.KindAccess,
		Groups: groups,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer: issuer, Subject: subject, Audience: jwt.ClaimStrings{auth.Audience},
		},
	}
}

func TestAuthenticate(t *testing.T) {
	k := newKeys(t)
	valid, err := auth.NewSigner(k.own, auth.Issuer, auth.Audience).Access("alice", []string{"ops"}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	session, err := auth.NewSigner(k.own, auth.Issuer, auth.Audience).Session("s1", auth.RoleOwner, "alice", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	expired := access(auth.Issuer, "alice")
	expired.IssuedAt = jwt.NewNumericDate(time.Now().Add(-2 * time.Hour))
	expired.ExpiresAt = jwt.NewNumericDate(time.Now().Add(-time.Hour))
	wrongAudience := access(auth.Issuer, "alice")
	wrongAudience.Audience = jwt.ClaimStrings{"elsewhere"}

	srv := httptest.NewServer(authenticate(k.verifier)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, ok := claimsFrom(r.Context())
		if !ok {
			t.Error("no claims in the context")
			return
		}
		respond(w, http.StatusOK, map[string]any{"user": c.Subject, "groups": c.Groups, "issuer": c.Issuer})
	})))
	defer srv.Close()

	bearer := func(token string) http.Header { return http.Header{"Authorization": {"Bearer " + token}} }
	for _, tt := range []struct {
		name   string
		header http.Header
		query  string
		code   string // error code, or empty for admitted
		admit  string // the JSON the handler writes
	}{
		{name: "none", code: codeUnauthorized},
		{name: "basic", header: http.Header{"Authorization": {"Basic YWxpY2U6cHc="}}, code: codeUnauthorized},
		{name: "scheme alone", header: http.Header{"Authorization": {"Bearer"}}, code: codeUnauthorized},
		{name: "empty", header: http.Header{"Authorization": {"Bearer  "}}, code: codeUnauthorized},
		{name: "two tokens", header: http.Header{"Authorization": {"Bearer " + valid + " " + valid}}, code: codeUnauthorized},
		{name: "two headers", header: http.Header{"Authorization": {"Bearer " + valid, "Bearer " + valid}}, code: codeUnauthorized},
		{name: "query", query: "?access_token=" + valid + "&token=" + valid, code: codeUnauthorized},
		{name: "cookie", header: http.Header{"Cookie": {"jwt=" + valid}}, code: codeUnauthorized},
		{name: "garbage", header: bearer("not.a.token"), code: codeInvalidToken},
		{name: "expired", header: bearer(signClaims(t, k.own, expired)), code: codeTokenExpired},
		{name: "session token", header: bearer(session), code: codeInvalidToken},
		{name: "wrong audience", header: bearer(signClaims(t, k.own, wrongAudience)), code: codeInvalidToken},
		{name: "unknown issuer", header: bearer(signClaims(t, k.stranger, access("stranger", "alice"))), code: codeInvalidToken},
		{name: "forged", header: bearer(signClaims(t, k.stranger, access(auth.Issuer, "alice"))), code: codeInvalidToken},
		{name: "valid", header: bearer(valid), admit: `{"groups":["ops"],"issuer":"occamy","user":"alice"}`},
		{name: "lowercase scheme", header: http.Header{"Authorization": {"bearer " + valid}}, admit: `{"groups":["ops"],"issuer":"occamy","user":"alice"}`},
		{name: "trusted issuer", header: bearer(signClaims(t, k.gateway, access("gateway", "carol"))), admit: `{"groups":null,"issuer":"gateway","user":"carol"}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := do(t, http.MethodGet, srv.URL+"/"+tt.query, tt.header, "")
			if tt.code == "" {
				if r.code != http.StatusOK || string(r.body) != tt.admit {
					t.Errorf("%d %s, want 200 %s", r.code, r.body, tt.admit)
				}
				return
			}
			if r.code != http.StatusUnauthorized {
				t.Fatalf("%d %s, want 401", r.code, r.body)
			}
			if e := decodeError(t, r.body); e.Code != tt.code {
				t.Errorf("code %q, want %q", e.Code, tt.code)
			}
			challenge := r.header.Get("WWW-Authenticate")
			if !strings.HasPrefix(challenge, `Bearer realm="occamy"`) ||
				strings.Contains(challenge, "invalid_token") != (tt.code != codeUnauthorized) {
				t.Errorf("WWW-Authenticate %q", challenge)
			}
			if strings.Contains(string(r.body), valid) {
				t.Errorf("body repeats the token: %s", r.body)
			}
		})
	}
}

// TestRequestLogOmitsSecrets checks that API v1's request log names each
// request but none of the password, the token or the query string.
func TestRequestLogOmitsSecrets(t *testing.T) {
	logs := captureLog(t)
	ts := newTestServer(t, nil)
	r := ts.signIn(t, "alice", alicePassword)
	if r.code != http.StatusOK {
		t.Fatalf("sign-in: %d %s", r.code, r.body)
	}
	var tr tokenResponse
	if err := json.Unmarshal(r.body, &tr); err != nil {
		t.Fatal(err)
	}
	ts.signIn(t, "alice", "wrong-"+alicePassword)
	do(t, http.MethodGet, ts.srv.URL+"/healthz?token="+tr.AccessToken, http.Header{"Authorization": {"Bearer " + tr.AccessToken}}, "")

	authed := httptest.NewServer(logRequests(authenticate(ts.s.verifier)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))))
	defer authed.Close()
	do(t, http.MethodGet, authed.URL+"/api/v1/connections?access_token="+tr.AccessToken, http.Header{"Authorization": {"Bearer " + tr.AccessToken}}, "")

	got := logs()
	for _, secret := range []string{alicePassword, tr.AccessToken, "token=", "?"} {
		if strings.Contains(got, secret) {
			t.Fatalf("log contains %q:\n%s", secret, got)
		}
	}
	for _, line := range []string{
		`"POST" "/api/v1/auth/token" 200 `,
		`"POST" "/api/v1/auth/token" 401 `,
		`"GET" "/healthz" 200 `,
		`"GET" "/api/v1/connections" 200 `,
		`user "alice" signed in`,
		"sign-in failed from 127.0.0.1:",
	} {
		if !strings.Contains(got, line) {
			t.Errorf("log lacks %q:\n%s", line, got)
		}
	}
}

// TestConnFrom checks that a request finds its TCP connection, below TLS
// too, where SetLowWater can reach the socket.
func TestConnFrom(t *testing.T) {
	cert, pool := testCert(t)
	for _, tlsConfig := range []*tls.Config{nil, {Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}} {
		var got net.Conn
		srv := newPublicServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got, _ = connFrom(r.Context())
		}), tlsConfig)
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		go func() {
			if tlsConfig != nil {
				srv.ServeTLS(ln, "", "")
			} else {
				srv.Serve(ln)
			}
		}()
		scheme := "http"
		if tlsConfig != nil {
			scheme = "https"
		}
		resp, err := testClient(pool).Get(scheme + "://" + ln.Addr().String() + "/")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		srv.Close()
		if _, ok := got.(*net.TCPConn); !ok {
			t.Errorf("%s: connFrom = %T, want *net.TCPConn", scheme, got)
		}
	}
	if c, ok := connFrom(t.Context()); ok || c != nil {
		t.Errorf("connFrom without a connection = %v, %v", c, ok)
	}
}

func TestBearerToken(t *testing.T) {
	for _, tt := range []struct {
		header []string
		token  string
	}{
		{nil, ""},
		{[]string{"Bearer abc"}, "abc"},
		{[]string{"BEARER abc"}, "abc"},
		{[]string{"Bearer  abc "}, "abc"},
		{[]string{"Bearer a\tbc"}, ""},
		{[]string{"Bearerabc"}, ""},
		{[]string{"Token abc"}, ""},
		{[]string{"Bearer abc", "Bearer abc"}, ""},
	} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header["Authorization"] = slices.Clone(tt.header)
		if token, ok := bearerToken(r); token != tt.token || ok != (tt.token != "") {
			t.Errorf("bearerToken(%q) = %q, %v; want %q", tt.header, token, ok, tt.token)
		}
	}
}
