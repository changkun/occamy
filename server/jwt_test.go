// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package server

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/golang-jwt/jwt/v5"

	"changkun.de/x/occamy/internal/config"
	"changkun.de/x/occamy/internal/guacamole"
)

const testSecret = "test-secret"

// testNow is the clock of the tokens in these tests.
var testNow = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

// routeProxy runs a proxy's full routes against a fake guacd.
type routeProxy struct {
	p   *proxy
	g   *fakeGuacd
	srv *httptest.Server
}

func newRouteProxy(t *testing.T, client, debug bool) *routeProxy {
	t.Helper()
	g := newFakeGuacd(t)
	tokens, err := newTokens(testSecret, "HS256")
	if err != nil {
		t.Fatal(err)
	}
	tokens.now = func() time.Time { return testNow }
	p := &proxy{
		guacd:    config.Guacd{Address: g.ln.Addr().String()},
		tokens:   tokens,
		legacyV0: true,
		client:   client,
		debug:    debug,
		sessions: make(map[string]*Session),
	}
	rp := &routeProxy{p: p, g: g, srv: httptest.NewServer(p.routes())}
	t.Cleanup(func() {
		rp.srv.Close()
		waitFor(t, "an empty session table", func() bool {
			p.mu.Lock()
			defer p.mu.Unlock()
			return len(p.sessions) == 0
		})
	})
	return rp
}

func (rp *routeProxy) wsURL(path string) string {
	return "ws" + strings.TrimPrefix(rp.srv.URL, "http") + path
}

// testClaims are the claims the tests log in with.
var testClaims = config.JWT{Protocol: "vnc", Host: "desktop:5900", Username: "occamy", Password: "correct-horse-battery-staple"}

// reply is a response with its body read.
type reply struct {
	code   int
	header http.Header
	body   []byte
}

// do sends a request and reads the reply.
func do(t *testing.T, method, u string, header http.Header, body string) reply {
	t.Helper()
	req, err := http.NewRequest(method, u, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	maps.Copy(req.Header, header)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return reply{resp.StatusCode, resp.Header, b}
}

// login posts body with the content type.
func (rp *routeProxy) login(t *testing.T, contentType, body string) reply {
	t.Helper()
	return do(t, http.MethodPost, rp.srv.URL+"/api/v0/login", http.Header{"Content-Type": {contentType}}, body)
}

// token logs in with testClaims and returns the token.
func (rp *routeProxy) token(t *testing.T) string {
	t.Helper()
	b, _ := json.Marshal(testClaims)
	r := rp.login(t, "application/json", string(b))
	if r.code != http.StatusOK {
		t.Fatalf("login: %d: %s", r.code, r.body)
	}
	var out struct{ Token string }
	if err := json.Unmarshal(r.body, &out); err != nil {
		t.Fatal(err)
	}
	return out.Token
}

// get requests path without upgrading to a WebSocket.
func (rp *routeProxy) get(t *testing.T, path string, header http.Header) reply {
	t.Helper()
	return do(t, http.MethodGet, rp.srv.URL+path, header, "")
}

// sign signs claims with the algorithm and key, as another issuer would.
func sign(t *testing.T, method jwt.SigningMethod, key any, claims jwt.MapClaims) string {
	t.Helper()
	s, err := jwt.NewWithClaims(method, claims).SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// validClaims are the claims of a v0 token that expires in an hour.
func validClaims() jwt.MapClaims {
	return jwt.MapClaims{
		"protocol": testClaims.Protocol,
		"host":     testClaims.Host,
		"username": testClaims.Username,
		"password": testClaims.Password,
		"exp":      testNow.Add(time.Hour).Unix(),
		"orig_iat": testNow.Unix(),
	}
}

// TestLoginResponse checks the login response byte for byte, and the
// token's header and claims, against what gin-jwt v2 produced.
func TestLoginResponse(t *testing.T) {
	rp := newRouteProxy(t, true, false)
	b, _ := json.Marshal(testClaims)
	r := rp.login(t, "application/json; charset=utf-8", string(b))
	if r.code != http.StatusOK {
		t.Fatalf("login: %d: %s", r.code, r.body)
	}
	if ct := r.header.Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q", ct)
	}
	var out struct{ Token string }
	if err := json.Unmarshal(r.body, &out); err != nil {
		t.Fatal(err)
	}
	expire := testNow.Add(time.Hour)
	want := `{"code":200,"expire":"` + expire.Format(time.RFC3339) + `","token":"` + out.Token + `"}`
	if string(r.body) != want {
		t.Fatalf("login response\n%s\nwant\n%s", r.body, want)
	}

	parts := strings.Split(out.Token, ".")
	if len(parts) != 3 {
		t.Fatalf("token %q has %d parts", out.Token, len(parts))
	}
	header, err := jwt.NewParser().DecodeSegment(parts[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(header) != `{"alg":"HS256","typ":"JWT"}` {
		t.Errorf("token header = %s", header)
	}
	payload, err := jwt.NewParser().DecodeSegment(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	wantPayload, _ := json.Marshal(validClaims())
	if string(payload) != string(wantPayload) {
		t.Errorf("token claims\n%s\nwant\n%s", payload, wantPayload)
	}
	if want := sign(t, jwt.SigningMethodHS256, []byte(testSecret), validClaims()); out.Token != want {
		t.Errorf("token = %s, want %s", out.Token, want)
	}
}

func TestLoginForm(t *testing.T) {
	rp := newRouteProxy(t, true, false)
	form := url.Values{"protocol": {"vnc"}, "host": {"desktop:5900"}, "password": {"secret"}}
	r := rp.login(t, "application/x-www-form-urlencoded", form.Encode())
	if r.code != http.StatusOK {
		t.Fatalf("login: %d: %s", r.code, r.body)
	}
	var out struct{ Token string }
	if err := json.Unmarshal(r.body, &out); err != nil {
		t.Fatal(err)
	}
	payload, err := jwt.NewParser().DecodeSegment(strings.Split(out.Token, ".")[1])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(payload), `"password":"secret"`) || !strings.Contains(string(payload), `"username":""`) {
		t.Errorf("token claims %s, want the form's", payload)
	}
}

// TestLoginRejected checks that a login without a protocol, host or
// password fails as gin-jwt's did.
func TestLoginRejected(t *testing.T) {
	rp := newRouteProxy(t, true, false)
	for _, body := range []string{
		`{"protocol":"vnc","host":"desktop:5900"}`,
		`{"protocol":"vnc","password":"secret"}`,
		`{"host":"desktop:5900","password":"secret"}`,
		`{"protocol":"vnc",`,
		``,
	} {
		r := rp.login(t, "application/json", body)
		if r.code != http.StatusUnauthorized {
			t.Errorf("login with %s: %d, want 401", body, r.code)
		}
		if want := `{"code":401,"message":"incorrect Username or Password"}`; string(r.body) != want {
			t.Errorf("login with %s: body %s, want %s", body, r.body, want)
		}
		if got := r.header.Get("WWW-Authenticate"); got != "JWT realm=occamy-proxy" {
			t.Errorf("WWW-Authenticate = %q", got)
		}
	}
}

// TestRoutes checks which routes exist: login only with client: true,
// and nothing left under the old v0 paths.
func TestRoutes(t *testing.T) {
	rp := newRouteProxy(t, false, false)
	b, _ := json.Marshal(testClaims)
	for _, path := range []string{"/api/v0/login", "/api/v1/login"} {
		r := do(t, http.MethodPost, rp.srv.URL+path, http.Header{"Content-Type": {"application/json"}}, string(b))
		if r.code != http.StatusNotFound {
			t.Errorf("POST %s without client: %d, want 404", path, r.code)
		}
	}
	for _, path := range []string{"/api/v1/connect", "/static/"} {
		if r := rp.get(t, path, nil); r.code != http.StatusNotFound {
			t.Errorf("GET %s without client: %d, want 404", path, r.code)
		}
	}
}

// TestTokenLookup connects with the token in the Authorization header,
// the token query parameter and the jwt cookie. A malformed header falls
// through to the query, as in gin-jwt.
func TestTokenLookup(t *testing.T) {
	rp := newRouteProxy(t, true, false)
	token := rp.token(t)
	for _, tt := range []struct {
		name   string
		query  string
		header http.Header
	}{
		{"header", "", http.Header{"Authorization": {"Bearer " + token}}},
		{"query", "?token=" + token, nil},
		{"cookie", "", http.Header{"Cookie": {"jwt=" + token}}},
		{"bad header, then query", "?token=" + token, http.Header{"Authorization": {"Basic Zm9vOmJhcg=="}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ws, code, err := dialWS(rp.wsURL("/api/v0/connect"+tt.query), tt.header)
			if err != nil {
				t.Fatalf("connect: %d: %v", code, err)
			}
			defer leave(ws)
			if ws.Subprotocol() != "guacamole" {
				t.Errorf("subprotocol %q, want guacamole", ws.Subprotocol())
			}
			msg := mustRead(t, ws)
			ins, err := guacamole.ParseInstruction([]byte(msg))
			if err != nil || ins.Opcode() != "" {
				t.Fatalf("first message %q, want the connection ID", msg)
			}
		})
	}
}

// TestTokenRejected checks that connect turns away tokens that are
// missing, signed with another algorithm or secret, expired, or without
// the claims, with gin-jwt's status codes and JSON bodies.
func TestTokenRejected(t *testing.T) {
	rp := newRouteProxy(t, true, false)
	key := []byte(testSecret)
	expired := validClaims()
	expired["exp"] = testNow.Add(-time.Second).Unix()
	noExp := validClaims()
	delete(noExp, "exp")
	noPassword := validClaims()
	delete(noPassword, "password")
	numericHost := validClaims()
	numericHost["host"] = 5900
	zeroExp := validClaims()
	zeroExp["exp"] = 0
	future := validClaims()
	future["iat"] = testNow.Add(time.Hour).Unix()
	unsigned := sign(t, jwt.SigningMethodNone, jwt.UnsafeAllowNoneSignatureType, validClaims())

	for _, tt := range []struct {
		name    string
		token   string
		code    int
		message string // gin-jwt's text, which v0 clients may match
	}{
		{"none", "", http.StatusUnauthorized, "cookie token is empty"},
		{"HS384", sign(t, jwt.SigningMethodHS384, key, validClaims()), http.StatusUnauthorized, "invalid signing algorithm"},
		{"HS512", sign(t, jwt.SigningMethodHS512, key, validClaims()), http.StatusUnauthorized, "invalid signing algorithm"},
		{"alg none", unsigned, http.StatusUnauthorized, "invalid signing algorithm"},
		{"other secret", sign(t, jwt.SigningMethodHS256, []byte("other"), validClaims()), http.StatusUnauthorized, "signature is invalid"},
		{"tampered", rp.token(t) + "x", http.StatusUnauthorized, "signature is invalid"},
		{"malformed", "a.b", http.StatusUnauthorized, "token contains an invalid number of segments"},
		{"expired", sign(t, jwt.SigningMethodHS256, key, expired), http.StatusUnauthorized, "Token is expired"},
		{"exp zero", sign(t, jwt.SigningMethodHS256, key, zeroExp), http.StatusUnauthorized, "token is expired"},
		{"issued later", sign(t, jwt.SigningMethodHS256, key, future), http.StatusUnauthorized, "Token used before issued"},
		{"no exp", sign(t, jwt.SigningMethodHS256, key, noExp), http.StatusBadRequest, "missing exp field"},
		{"no password", sign(t, jwt.SigningMethodHS256, key, noPassword), http.StatusUnauthorized, errMissingClaims.Error()},
		{"numeric host", sign(t, jwt.SigningMethodHS256, key, numericHost), http.StatusUnauthorized, errMissingClaims.Error()},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := "/api/v0/connect"
			if tt.token != "" {
				path += "?token=" + tt.token
			}
			r := rp.get(t, path, nil)
			if r.code != tt.code {
				t.Fatalf("%d: %s", r.code, r.body)
			}
			var out struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			}
			if err := json.Unmarshal(r.body, &out); err != nil {
				t.Fatalf("body %s: %v", r.body, err)
			}
			if out.Code != tt.code || out.Message != tt.message {
				t.Errorf("body %s, want code %d and message %q", r.body, tt.code, tt.message)
			}
			if got := r.header.Get("WWW-Authenticate"); got != "JWT realm=occamy-proxy" {
				t.Errorf("WWW-Authenticate = %q", got)
			}
		})
	}
	if _, code, err := dialWS(rp.wsURL("/api/v0/connect?token=x"), nil); err == nil || code != http.StatusUnauthorized {
		t.Errorf("a WebSocket with a bad token: %d, %v, want 401", code, err)
	}
}

// TestCrossOriginRejected checks that a valid token does not open a
// WebSocket from another origin, as gorilla/websocket's default check
// did, while the request's own origin passes.
func TestCrossOriginRejected(t *testing.T) {
	rp := newRouteProxy(t, true, false)
	u := rp.wsURL("/api/v0/connect?token=" + rp.token(t))

	_, code, err := dialWS(u, http.Header{"Origin": {"http://evil.example"}})
	if err == nil {
		t.Fatal("a cross-origin WebSocket was accepted")
	}
	if code != http.StatusForbidden {
		t.Fatalf("cross-origin connect: %d, want 403", code)
	}

	ws, _, err := dialWS(u, http.Header{"Origin": {rp.srv.URL}})
	if err != nil {
		t.Fatalf("same-origin connect: %v", err)
	}
	defer leave(ws)
	mustRead(t, ws)
}

// TestLargeMessage sends an instruction over coder/websocket's default
// read limit of 32 KiB, which must reach guacd, and a message over
// MaxInstructionSize, which must close the connection with 1009.
func TestLargeMessage(t *testing.T) {
	rp := newRouteProxy(t, true, false)
	ws, _, err := dialWS(rp.wsURL("/api/v0/connect?token="+rp.token(t)), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer leave(ws)
	mustRead(t, ws)

	large := guacamole.NewInstruction([]string{"blob", "0", strings.Repeat("😀", guacamole.MaxInstructionLength)}).String()
	if len(large) <= 32768 {
		t.Fatalf("instruction of %d bytes is within the default limit", len(large))
	}
	if err := ws.Write(context.Background(), websocket.MessageText, []byte(large)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the instruction at guacd", func() bool {
		_, _, received := rp.g.snapshot()
		return slices.Contains(received, large)
	})

	tooLarge := bytes.Repeat([]byte("3.nop;"), guacamole.MaxInstructionSize/6+1)
	if err := ws.Write(context.Background(), websocket.MessageText, tooLarge); err != nil {
		t.Fatal(err)
	}
	for {
		_, err := read(t, ws)
		if err == nil {
			continue
		}
		if code := websocket.CloseStatus(err); code != websocket.StatusMessageTooBig {
			t.Fatalf("closed with %v (%v), want 1009", code, err)
		}
		break
	}
}

// TestOversizedStall checks that a client which declares a message far
// over the read limit, sends just past the limit and then goes quiet does
// not hold its handler and session: coder/websocket's close would read
// the rest of that message with no deadline.
func TestOversizedStall(t *testing.T) {
	rp := newRouteProxy(t, true, false)
	addr := strings.TrimPrefix(rp.srv.URL, "http://")
	nc, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	fmt.Fprintf(nc, "GET /api/v0/connect?token=%s HTTP/1.1\r\nHost: %s\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n"+
		"Sec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Protocol: guacamole\r\n\r\n", rp.token(t), addr)
	br := bufio.NewReader(nc)
	resp, err := http.ReadResponse(br, nil)
	if err != nil || resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("upgrade: %v %v", resp, err)
	}
	// A 101 has no body; closing it leaves the connection open.
	resp.Body.Close()
	go func() { _, _ = io.Copy(io.Discard, br) }() // the server's writes never block
	sessions := func() int {
		rp.p.mu.Lock()
		defer rp.p.mu.Unlock()
		return len(rp.p.sessions)
	}
	waitFor(t, "a session", func() bool { return sessions() == 1 })

	// A masked text frame that declares 1 TiB, with a zero mask.
	hdr := binary.BigEndian.AppendUint64([]byte{0x81, 0x80 | 127}, 1<<40)
	if _, err := nc.Write(append(hdr, 0, 0, 0, 0)); err != nil {
		t.Fatal(err)
	}
	if _, err := nc.Write(bytes.Repeat([]byte("a"), guacamole.MaxInstructionSize+64)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the session to end while the client is quiet", func() bool { return sessions() == 0 })
}

// TestEmptyOrigin checks that an Origin header without a value is
// refused, as gorilla/websocket refused it; coder/websocket skips the
// check for one.
func TestEmptyOrigin(t *testing.T) {
	rp := newRouteProxy(t, true, false)
	r := rp.get(t, "/api/v0/connect?token="+rp.token(t), http.Header{"Origin": {""}})
	if r.code != http.StatusForbidden {
		t.Errorf("%d %s, want 403", r.code, r.body)
	}
}

// TestRequestLogOmitsToken checks that the request log names each
// request but never the token, which travels in the query string, nor
// the credentials.
func TestRequestLogOmitsToken(t *testing.T) {
	var buf bytes.Buffer
	var mu sync.Mutex
	log.SetOutput(writerFunc(func(p []byte) (int, error) {
		mu.Lock()
		defer mu.Unlock()
		return buf.Write(p)
	}))
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	logs := func() string {
		mu.Lock()
		defer mu.Unlock()
		return buf.String()
	}

	rp := newRouteProxy(t, true, false)
	token := rp.token(t)
	ws, _, err := dialWS(rp.wsURL("/api/v0/connect?token="+token+"&width=800"), nil)
	if err != nil {
		t.Fatal(err)
	}
	mustRead(t, ws)
	leave(ws)
	rp.get(t, "/api/v0/connect?token="+token+"x", nil)

	waitFor(t, "the connect in the log", func() bool {
		return strings.Contains(logs(), `"GET" "/api/v0/connect" 101 `)
	})
	got := logs()
	for _, secret := range []string{token, "token=", testClaims.Password} {
		if strings.Contains(got, secret) {
			t.Fatalf("logs contain %q:\n%s", secret, got)
		}
	}
	for _, line := range []string{`"POST" "/api/v0/login" 200 `, `"GET" "/api/v0/connect" 401 `} {
		if !strings.Contains(got, line) {
			t.Errorf("logs lack %q:\n%s", line, got)
		}
	}
}

// TestProfileOnlyInDebug checks that pprof is served in debug mode only.
func TestProfileOnlyInDebug(t *testing.T) {
	for _, debug := range []bool{true, false} {
		rp := newRouteProxy(t, true, debug)
		for _, path := range []string{"/debug/pprof/", "/debug/pprof/goroutine?debug=1", "/debug/pprof/cmdline"} {
			want := http.StatusNotFound
			if debug {
				want = http.StatusOK
			}
			if r := rp.get(t, path, nil); r.code != want {
				t.Errorf("debug %v: GET %s: %d, want %d", debug, path, r.code, want)
			}
		}
	}
}

func TestNewTokens(t *testing.T) {
	for _, alg := range []string{"HS256", "HS384", "HS512", ""} {
		tk, err := newTokens(testSecret, alg)
		if err != nil {
			t.Errorf("newTokens(%q): %v", alg, err)
			continue
		}
		if want := cmp.Or(alg, "HS256"); tk.method.Alg() != want {
			t.Errorf("newTokens(%q) signs with %s, want %s", alg, tk.method.Alg(), want)
		}
	}
	for _, alg := range []string{"RS256", "ES256", "EdDSA", "none", "hs256"} {
		if _, err := newTokens(testSecret, alg); err == nil {
			t.Errorf("newTokens(%q) did not fail", alg)
		}
	}
	if _, err := newTokens("", "HS256"); err == nil {
		t.Error("newTokens with an empty secret did not fail")
	}
}

func TestLookupToken(t *testing.T) {
	for _, tt := range []struct {
		name   string
		header http.Header
		query  string
		token  string
		err    error
	}{
		{"nothing", nil, "", "", errEmptyCookieToken},
		{"header", http.Header{"Authorization": {"Bearer a"}}, "", "a", nil},
		{"header first", http.Header{"Authorization": {"Bearer a"}, "Cookie": {"jwt=c"}}, "?token=b", "a", nil},
		{"lowercase bearer", http.Header{"Authorization": {"bearer a"}}, "", "", errEmptyCookieToken},
		{"query before cookie", http.Header{"Cookie": {"jwt=c"}}, "?token=b", "b", nil},
		{"cookie", http.Header{"Cookie": {"jwt=c"}}, "", "c", nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/api/v0/connect"+tt.query, nil)
			maps.Copy(r.Header, tt.header)
			token, err := lookupToken(r)
			if token != tt.token || !errors.Is(err, tt.err) {
				t.Errorf("lookupToken() = %q, %v, want %q, %v", token, err, tt.token, tt.err)
			}
		})
	}
}
