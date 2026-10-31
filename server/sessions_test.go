// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"changkun.de/x/occamy/internal/auth"
	"changkun.de/x/occamy/internal/backend"
	"changkun.de/x/occamy/internal/backend/backendtest"
	"changkun.de/x/occamy/internal/config"
	"changkun.de/x/occamy/protocol"
)

// sessionFile adds connections to a test file, all of the fake protocol
// that sessionServer registers: ops-shell for the group ops, dev-shell
// for bob by name, and both for alice by name and dev by group.
func sessionFile(f *config.File) {
	f.Connections = []config.Connection{
		{ID: "ops-shell", Name: "Ops shell", Protocol: "fake", Target: "ops.example:22", Allow: config.Allow{Groups: []string{"ops"}}},
		{ID: "dev-shell", Name: "Dev shell", Protocol: "fake", Target: "dev.example:22", Allow: config.Allow{Users: []string{"bob"}}},
		{ID: "both", Name: "Both", Protocol: "fake", Target: "both.example:22", Allow: config.Allow{Users: []string{"alice"}, Groups: []string{"dev"}}},
	}
}

// fakeBackends makes the fake protocol's sessions with fake terminals of
// the size the owner's hello asks for, 80x24 by default, and records the
// targets and sizes asked for.
type fakeBackends struct {
	mu        sync.Mutex
	targets   []target
	screens   []screen
	terminals []*backendtest.Terminal
	fail      error // returned instead of a backend if set
}

func (fb *fakeBackends) make(t target, s screen) (backend.Backend, error) {
	fb.mu.Lock()
	defer fb.mu.Unlock()
	fb.targets = append(fb.targets, t)
	fb.screens = append(fb.screens, s)
	if fb.fail != nil {
		return nil, fb.fail
	}
	cols, rows := 80, 24
	if s.cols > 0 && s.rows > 0 {
		cols, rows = s.cols, s.rows
	}
	b := backendtest.NewTerminal(cols, rows, 0)
	fb.terminals = append(fb.terminals, b)
	return b, nil
}

func (fb *fakeBackends) last() (target, screen, *backendtest.Terminal) {
	fb.mu.Lock()
	defer fb.mu.Unlock()
	if len(fb.targets) == 0 {
		return target{}, screen{}, nil
	}
	var b *backendtest.Terminal
	if len(fb.terminals) > 0 {
		b = fb.terminals[len(fb.terminals)-1]
	}
	return fb.targets[len(fb.targets)-1], fb.screens[len(fb.screens)-1], b
}

// sessionServer is a test server with sessionFile's connections, edit
// applied after, and the fake protocol, which every other protocol's
// sessions also use, so that no test dials a remote.
func sessionServer(t *testing.T, edit func(*config.File)) (*testServer, *fakeBackends) {
	t.Helper()
	ts := newTestServer(t, func(f *config.File) {
		sessionFile(f)
		if edit != nil {
			edit(f)
		}
	})
	fb := &fakeBackends{}
	for k, m := range ts.s.backends {
		ts.s.backends[k] = backendMaker{name: m.name, make: fb.make}
	}
	ts.s.backends.register("fake", "", "fake", fb.make)
	return ts, fb
}

// token signs an access token for user in groups with the server's key.
func (ts *testServer) token(t *testing.T, user string, groups ...string) string {
	t.Helper()
	tok, err := ts.s.signer.Access(user, groups, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func bearer(token string) http.Header {
	return http.Header{"Authorization": {"Bearer " + token}, "Content-Type": {"application/json"}}
}

// api sends a request to API v1 with token.
func (ts *testServer) api(t *testing.T, method, path, token, body string) reply {
	t.Helper()
	return do(t, method, ts.srv.URL+path, bearer(token), body)
}

// openSession opens a session on connection for the caller of token.
func (ts *testServer) openSession(t *testing.T, token, connection string) openResponse {
	t.Helper()
	r := ts.api(t, http.MethodPost, "/api/v1/sessions", token, `{"connection":"`+connection+`"}`)
	if r.code != http.StatusCreated {
		t.Fatalf("POST /api/v1/sessions: %d %s", r.code, r.body)
	}
	var o openResponse
	if err := json.Unmarshal(r.body, &o); err != nil {
		t.Fatal(err)
	}
	return o
}

// TestSessionRoutesAuth checks that every session route wants an access
// token: none, a session token and an expired one are refused.
func TestSessionRoutesAuth(t *testing.T) {
	k := newKeys(t)
	path := writeIssuerKey(t, k.gateway)
	ts, _ := sessionServer(t, func(f *config.File) {
		f.Auth.TrustedIssuers = []config.TrustedIssuer{{Issuer: "gateway", PublicKeyFile: path}}
	})
	alice := ts.token(t, "alice", "ops")
	o := ts.openSession(t, alice, "ops-shell")
	expired := access("gateway", "alice", "ops")
	expired.IssuedAt = jwt.NewNumericDate(time.Now().Add(-time.Hour))
	expired.ExpiresAt = jwt.NewNumericDate(time.Now().Add(-time.Minute))
	for _, tt := range []struct {
		name   string
		header http.Header
		code   string
	}{
		{"no token", http.Header{"Content-Type": {"application/json"}}, codeUnauthorized},
		{"not bearer", http.Header{"Authorization": {"Basic " + alice}}, codeUnauthorized},
		{"session token", bearer(o.StreamToken), codeInvalidToken},
		{"expired", bearer(signClaims(t, k.gateway, expired)), codeTokenExpired},
		{"stranger", bearer(signClaims(t, k.stranger, access("gateway", "alice", "ops"))), codeInvalidToken},
	} {
		for _, route := range []struct{ method, path, body string }{
			{http.MethodGet, "/api/v1/connections", ""},
			{http.MethodPost, "/api/v1/sessions", `{"connection":"ops-shell"}`},
			{http.MethodGet, "/api/v1/sessions/" + o.ID, ""},
			{http.MethodDelete, "/api/v1/sessions/" + o.ID, ""},
			{http.MethodPost, "/api/v1/sessions/" + o.ID + "/shares", `{"role":"viewer"}`},
		} {
			r := do(t, route.method, ts.srv.URL+route.path, tt.header, route.body)
			if r.code != http.StatusUnauthorized || decodeError(t, r.body).Code != tt.code {
				t.Errorf("%s: %s %s: %d %s, want 401 %s", tt.name, route.method, route.path, r.code, r.body, tt.code)
			}
		}
	}
	if ts.s.session(o.ID) == nil {
		t.Error("a refused DELETE closed the session")
	}
}

// TestListConnections checks that each caller sees the connections that
// name them or one of their groups, whichever issuer names them, and that
// a trusted issuer linked to static users may name them.
func TestListConnections(t *testing.T) {
	k := newKeys(t)
	path := writeIssuerKey(t, k.gateway)
	otherPath := writeIssuerKey(t, k.stranger)
	ts, _ := sessionServer(t, func(f *config.File) {
		f.Auth.TrustedIssuers = []config.TrustedIssuer{
			{Issuer: "gateway", PublicKeyFile: path, LinkStaticUsers: true, AllowedGroups: []string{"dev"}},
			{Issuer: "other", PublicKeyFile: otherPath},
		}
	})
	for _, tt := range []struct {
		name  string
		token string
		want  []string
	}{
		{"alice in ops", ts.token(t, "alice", "ops"), []string{"ops-shell", "both"}},
		{"alice in no group", ts.token(t, "alice"), []string{"both"}},
		{"bob in dev", ts.token(t, "bob", "dev"), []string{"dev-shell", "both"}},
		{"carol in ops", ts.token(t, "carol", "ops"), []string{"ops-shell"}},
		{"carol in no group", ts.token(t, "carol"), []string{}},
		{"carol in an unlisted group", ts.token(t, "carol", "Ops", "admins"), []string{}},
		{"the gateway's alice", signClaims(t, k.gateway, access("gateway", "alice")), []string{"both"}},
		{"the gateway's dev", signClaims(t, k.gateway, access("gateway", "dave", "dev")), []string{"both"}},
		// An issuer grants only the groups of its allowed_groups.
		{"the gateway's ops, which it may not grant", signClaims(t, k.gateway, access("gateway", "dave", "ops")), []string{}},
		{"the gateway's ops and dev", signClaims(t, k.gateway, access("gateway", "dave", "ops", "dev")), []string{"both"}},
		{"the gateway's alice in ops", signClaims(t, k.gateway, access("gateway", "alice", "ops")), []string{"both"}},
		{"another issuer's dev, with no allowed groups", signClaims(t, k.stranger, access("other", "dave", "dev")), []string{}},
	} {
		r := ts.api(t, http.MethodGet, "/api/v1/connections", tt.token, "")
		if r.code != http.StatusOK || r.header.Get("Cache-Control") != "no-store" {
			t.Errorf("%s: %d %v %s", tt.name, r.code, r.header, r.body)
			continue
		}
		var list struct {
			Connections []map[string]any `json:"connections"`
			Adhoc       *bool            `json:"adhoc_targets"`
		}
		if err := json.Unmarshal(r.body, &list); err != nil {
			t.Fatal(err)
		}
		got := []string{}
		for _, c := range list.Connections {
			got = append(got, c["id"].(string))
			if len(c) != 3 || c["name"] == nil || c["protocol"] != "fake" {
				t.Errorf("%s: connection %v, want id, name and protocol only", tt.name, c)
			}
		}
		if !slices.Equal(got, tt.want) || list.Adhoc == nil || *list.Adhoc {
			t.Errorf("%s: %s, want %v without ad-hoc targets", tt.name, r.body, tt.want)
		}
	}
}

// TestOpenSession opens a session on a connection, and checks what the
// caller may not open and what the request may not say.
func TestOpenSession(t *testing.T) {
	ts, fb := sessionServer(t, nil)
	alice := ts.token(t, "alice", "ops")
	r := ts.api(t, http.MethodPost, "/api/v1/sessions", alice, `{"connection":"ops-shell"}`)
	if r.code != http.StatusCreated || r.header.Get("Cache-Control") != "no-store" {
		t.Fatalf("POST /api/v1/sessions: %d %v %s", r.code, r.header, r.body)
	}
	var o openResponse
	if err := json.Unmarshal(r.body, &o); err != nil {
		t.Fatal(err)
	}
	if r.header.Get("Location") != "/api/v1/sessions/"+o.ID || o.Role != auth.RoleOwner || o.ExpiresIn != 300 {
		t.Errorf("reply %+v, Location %q", o, r.header.Get("Location"))
	}
	c, err := ts.s.verifier.VerifySession(o.StreamToken)
	if err != nil {
		t.Fatalf("stream token: %v", err)
	}
	if c.Session != o.ID || c.Role != auth.RoleOwner || c.Subject != "alice" || c.ExpiresAt.Sub(c.IssuedAt.Time) != 5*time.Minute {
		t.Errorf("stream token claims %+v", c)
	}
	info := ts.s.session(o.ID)
	if info == nil || info.owner != (principal{issuer: auth.Issuer, idp: auth.IdPLocal, subject: "alice"}) || info.connection != "ops-shell" || info.protocol != "fake" {
		t.Fatalf("session %+v", info)
	}
	if tg, sz, _ := fb.last(); tg.addr != "ops.example:22" || tg.connection != "ops-shell" || sz != (screen{}) {
		t.Errorf("backend made for %+v at %+v", tg, sz)
	}

	for _, tt := range []struct {
		name, token, body string
		status            int
		code              string
	}{
		{"another's connection", alice, `{"connection":"dev-shell"}`, http.StatusNotFound, codeNotFound},
		{"unknown connection", alice, `{"connection":"nope"}`, http.StatusNotFound, codeNotFound},
		{"bob on ops", ts.token(t, "bob", "dev"), `{"connection":"ops-shell"}`, http.StatusNotFound, codeNotFound},
		{"connection and target", alice, `{"connection":"ops-shell","protocol":"ssh","target":"a:22"}`, http.StatusBadRequest, codeBadRequest},
		{"nothing", alice, `{}`, http.StatusBadRequest, codeBadRequest},
		{"unknown field", alice, `{"connection":"ops-shell","password":"x"}`, http.StatusBadRequest, codeBadRequest},
		{"not json", alice, `connection=ops-shell`, http.StatusBadRequest, codeBadRequest},
		{"ad-hoc while off", alice, `{"protocol":"vnc","target":"10.0.0.1:5900"}`, http.StatusForbidden, codeForbidden},
	} {
		r := ts.api(t, http.MethodPost, "/api/v1/sessions", tt.token, tt.body)
		if r.code != tt.status || decodeError(t, r.body).Code != tt.code {
			t.Errorf("%s: %d %s, want %d %s", tt.name, r.code, r.body, tt.status, tt.code)
		}
	}
	if n := ts.s.registry.Len(); n != 1 {
		t.Errorf("%d sessions, want 1", n)
	}

	// The owner, who has not connected, has the owner grace to.
	if got := info.s.Viewers(); len(got) != 0 {
		t.Errorf("viewers before the owner connected: %v", got)
	}

	fb.mu.Lock()
	fb.fail = errors.New("no such file")
	fb.mu.Unlock()
	restore := captureLog(t)
	r = ts.api(t, http.MethodPost, "/api/v1/sessions", alice, `{"connection":"both"}`)
	logged := restore()
	if r.code != http.StatusInternalServerError || decodeError(t, r.body).Code != codeInternal || strings.Contains(string(r.body), "no such file") {
		t.Errorf("a backend that fails: %d %s", r.code, r.body)
	}
	if !strings.Contains(logged, "no such file") || !strings.Contains(logged, `user "alice" opening connection "both"`) {
		t.Errorf("log %q does not say why", logged)
	}
}

// TestSessionOwnerOnly checks that only a session's owner reads, closes
// and shares it, and that another user's session is as absent as none.
func TestSessionOwnerOnly(t *testing.T) {
	ts, _ := sessionServer(t, nil)
	alice := ts.token(t, "alice", "ops")
	o := ts.openSession(t, alice, "both")
	for name, token := range map[string]string{
		"bob":           ts.token(t, "bob", "dev"),
		"alice's group": ts.token(t, "carol", "ops"),
	} {
		for _, route := range []struct{ method, path, body string }{
			{http.MethodGet, "/api/v1/sessions/" + o.ID, ""},
			{http.MethodDelete, "/api/v1/sessions/" + o.ID, ""},
			{http.MethodPost, "/api/v1/sessions/" + o.ID + "/shares", `{"role":"viewer"}`},
		} {
			r := ts.api(t, route.method, route.path, token, route.body)
			if r.code != http.StatusNotFound || decodeError(t, r.body).Code != codeNotFound {
				t.Errorf("%s: %s %s: %d %s, want 404", name, route.method, route.path, r.code, r.body)
			}
		}
	}
	if ts.s.session(o.ID) == nil {
		t.Fatal("another user closed the session")
	}

	r := ts.api(t, http.MethodGet, "/api/v1/sessions/"+o.ID, alice, "")
	if r.code != http.StatusOK || r.header.Get("Cache-Control") != "no-store" {
		t.Fatalf("GET: %d %s", r.code, r.body)
	}
	var st sessionStateResponse
	if err := json.Unmarshal(r.body, &st); err != nil {
		t.Fatal(err)
	}
	if st.ID != o.ID || st.Connection != "both" || st.Protocol != "fake" || st.Backend != "fake" || st.Class != "terminal" ||
		len(st.Viewers) != 0 || time.Since(st.Opened) > time.Minute {
		t.Errorf("state %s", r.body)
	}
	waitFor(t, "the terminal's size", func() bool { return ts.s.session(o.ID).s.Size().X == 80 })
	r = ts.api(t, http.MethodGet, "/api/v1/sessions/"+o.ID, alice, "")
	if !strings.Contains(string(r.body), `"size":{"width":80,"height":24}`) {
		t.Errorf("state %s, want the size", r.body)
	}

	r = ts.api(t, http.MethodDelete, "/api/v1/sessions/"+o.ID, alice, "")
	if r.code != http.StatusNoContent {
		t.Fatalf("DELETE: %d %s", r.code, r.body)
	}
	if ts.s.session(o.ID) != nil && ts.s.registry.Len() != 0 {
		t.Error("the session is still running after DELETE")
	}
	waitFor(t, "the session to be forgotten", func() bool { return ts.s.session(o.ID) == nil })
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/sessions/" + o.ID},
		{http.MethodDelete, "/api/v1/sessions/" + o.ID},
		{http.MethodGet, "/api/v1/sessions/nope"},
	} {
		if r := ts.api(t, route.method, route.path, alice, ""); r.code != http.StatusNotFound {
			t.Errorf("%s %s after the end: %d", route.method, route.path, r.code)
		}
	}
}

// TestShares checks the roles and lifetimes a share may have.
func TestShares(t *testing.T) {
	ts, _ := sessionServer(t, nil)
	alice := ts.token(t, "alice", "ops")
	o := ts.openSession(t, alice, "ops-shell")
	for _, tt := range []struct {
		body   string
		status int
		ttl    int64
	}{
		{`{"role":"viewer"}`, http.StatusCreated, 3600},
		{`{"role":"read-only","expires_in":60}`, http.StatusCreated, 60},
		{`{"role":"viewer","expires_in":43200}`, http.StatusCreated, 43200},
		{`{"role":"owner"}`, http.StatusBadRequest, 0},
		{`{"role":""}`, http.StatusBadRequest, 0},
		{`{}`, http.StatusBadRequest, 0},
		{`{"role":"viewer","expires_in":59}`, http.StatusBadRequest, 0},
		{`{"role":"viewer","expires_in":-1}`, http.StatusBadRequest, 0},
		{`{"role":"viewer","expires_in":43201}`, http.StatusBadRequest, 0},
		{`{"role":"viewer","expires_in":9223372036854775807}`, http.StatusBadRequest, 0},
	} {
		r := ts.api(t, http.MethodPost, "/api/v1/sessions/"+o.ID+"/shares", alice, tt.body)
		if r.code != tt.status {
			t.Errorf("%s: %d %s, want %d", tt.body, r.code, r.body, tt.status)
			continue
		}
		if tt.status != http.StatusCreated {
			continue
		}
		var sh shareResponse
		if err := json.Unmarshal(r.body, &sh); err != nil {
			t.Fatal(err)
		}
		if sh.ExpiresIn != tt.ttl || sh.Token == "" || r.header.Get("Cache-Control") != "no-store" {
			t.Errorf("%s: %s", tt.body, r.body)
		}
		v, err := ts.s.registry.JoinShared(o.ID, sh.Token)
		if err != nil {
			t.Errorf("%s: the share does not join: %v", tt.body, err)
			continue
		}
		if want := strings.Trim(strings.SplitN(tt.body, `"`, 5)[3], `"`); v.Role().String() != want {
			t.Errorf("%s: joined as %v", tt.body, v.Role())
		}
		v.Leave()
	}
}

// TestAdhocTargets checks ad-hoc targets against allow_adhoc_targets and
// adhoc_allowlist.
// TestLimits checks the sessions a user owns, the shares a session has
// and the viewers it takes.
func TestLimits(t *testing.T) {
	ts, _ := sessionServer(t, func(f *config.File) {
		f.Limits = config.Limits{SessionsPerUser: 2, ViewersPerSession: 1, SharesPerSession: 2}
	})
	alice := ts.token(t, "alice", "ops")
	first := ts.openSession(t, alice, "ops-shell")
	ts.openSession(t, alice, "ops-shell")
	r := ts.api(t, http.MethodPost, "/api/v1/sessions", alice, `{"connection":"ops-shell"}`)
	if r.code != http.StatusTooManyRequests || decodeError(t, r.body).Code != codeLimitExceeded {
		t.Errorf("a third session: %d %s, want 429 limit_exceeded", r.code, r.body)
	}
	c, status, err := dial(ts.srv.URL+"/api/v1/connections/ops-shell/stream", withToken(alice), &termHello, nil)
	if err != nil {
		t.Fatalf("%d %v", status, err)
	}
	c.closed(t, protocol.CloseForbidden)
	ts.openSession(t, ts.token(t, "carol", "ops"), "ops-shell") // another user's are their own
	if r := ts.api(t, http.MethodDelete, "/api/v1/sessions/"+first.ID, alice, ""); r.code != http.StatusNoContent {
		t.Fatalf("DELETE: %d %s", r.code, r.body)
	}
	waitFor(t, "the closed session to stop counting", func() bool {
		return ts.api(t, http.MethodPost, "/api/v1/sessions", alice, `{"connection":"ops-shell"}`).code == http.StatusCreated
	})

	// occamyd's own limit counts every user's sessions: alice's two and
	// carol's one fill it.
	ts.s.mu.Lock()
	ts.s.limits.Sessions = 3
	ts.s.mu.Unlock()
	dave := ts.token(t, "dave", "ops")
	if r := ts.api(t, http.MethodPost, "/api/v1/sessions", dave, `{"connection":"ops-shell"}`); r.code != http.StatusServiceUnavailable || decodeError(t, r.body).Code != codeLimitExceeded {
		t.Errorf("a session over limits.sessions: %d %s, want 503 limit_exceeded", r.code, r.body)
	}
	c, status, err = dial(ts.srv.URL+"/api/v1/connections/ops-shell/stream", withToken(dave), &termHello, nil)
	if err != nil {
		t.Fatalf("%d %v", status, err)
	}
	c.closed(t, protocol.CloseForbidden)
	ts.s.mu.Lock()
	ts.s.limits.Sessions = config.DefaultSessions
	ts.s.mu.Unlock()

	o := ts.openSession(t, ts.token(t, "bob", "ops"), "ops-shell")
	bob := ts.token(t, "bob", "ops")
	var shares []string
	for range 2 {
		r := ts.api(t, http.MethodPost, "/api/v1/sessions/"+o.ID+"/shares", bob, `{"role":"viewer"}`)
		var sh shareResponse
		if err := json.Unmarshal(r.body, &sh); err != nil || r.code != http.StatusCreated {
			t.Fatalf("share: %d %s", r.code, r.body)
		}
		shares = append(shares, sh.Token)
	}
	r = ts.api(t, http.MethodPost, "/api/v1/sessions/"+o.ID+"/shares", bob, `{"role":"viewer"}`)
	if r.code != http.StatusTooManyRequests || decodeError(t, r.body).Code != codeLimitExceeded {
		t.Errorf("a third share: %d %s, want 429 limit_exceeded", r.code, r.body)
	}
	path := "/api/v1/sessions/" + o.ID + "/stream"
	ts.stream(t, path, o.StreamToken, termHello)
	ts.stream(t, path, shares[0], termHello)
	viewer, err := ts.s.signer.Session(o.ID, auth.RoleViewer, "carol", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{shares[1], viewer} {
		c, status, err := dial(ts.srv.URL+path, withToken(token), &termHello, nil)
		if err != nil {
			t.Fatalf("%d %v", status, err)
		}
		c.closed(t, protocol.CloseViewerLimit)
	}
	ts.stream(t, path, bob, termHello) // the owner, beside a full session
}

// TestAdhocReserved checks that the server keeps guacd and its own
// listeners out of an ad-hoc range's reach.
// classless is a backend of no class, which session.Start refuses, as
// it would any registry.Open that fails after the backend is made.
type classless struct{ *backendtest.Terminal }

func (classless) Class() backend.Class { return 0 }

// TestSessionsCapReleased checks that every way out of a session gives
// its place under limits.sessions back: the session's end, a backend
// that could not be made, and a session that could not start. One place
// kept would refuse every session after limits.sessions of them.
func TestSessionsCapReleased(t *testing.T) {
	ts, fb := sessionServer(t, func(f *config.File) { f.Limits = config.Limits{Sessions: 1} })
	alice := ts.token(t, "alice", "ops")
	for i := range 4 {
		o := ts.openSession(t, alice, "ops-shell")
		if r := ts.api(t, http.MethodDelete, "/api/v1/sessions/"+o.ID, alice, ""); r.code != http.StatusNoContent {
			t.Fatalf("DELETE %d: %d", i, r.code)
		}
		waitFor(t, "the place to come back", func() bool {
			ts.s.mu.Lock()
			defer ts.s.mu.Unlock()
			return ts.s.running == 0
		})
	}
	failing := func(what string) {
		t.Helper()
		for i := range 3 {
			if r := ts.api(t, http.MethodPost, "/api/v1/sessions", alice, `{"connection":"ops-shell"}`); r.code != http.StatusInternalServerError {
				t.Fatalf("%s, %d: %d %s, want 500", what, i, r.code, r.body)
			}
		}
	}
	fb.mu.Lock()
	fb.fail = errors.New("no backend")
	fb.mu.Unlock()
	failing("a backend that could not be made")
	fb.mu.Lock()
	fb.fail = nil
	fb.mu.Unlock()
	ts.s.backends.register("fake", "", "fake", func(target, screen) (backend.Backend, error) {
		return classless{backendtest.NewTerminal(80, 24, 0)}, nil
	})
	failing("a session that could not start")
	ts.s.backends.register("fake", "", "fake", fb.make)
	ts.openSession(t, alice, "ops-shell")
}

// TestConcurrentLimits checks that requests that open sessions at once,
// over REST and streams, cannot all pass a limit: each counts from its
// check on.
func TestConcurrentLimits(t *testing.T) {
	for _, tt := range []struct {
		name    string
		edit    func(*config.File)
		user    func(i int) string
		refused protocol.CloseCode
		status  int
	}{
		{"per user", func(f *config.File) { f.Limits.SessionsPerUser = 3 }, func(int) string { return "alice" }, protocol.CloseForbidden, http.StatusTooManyRequests},
		{"occamyd", func(f *config.File) { f.Limits.Sessions = 3 }, func(i int) string { return fmt.Sprintf("user%d", i) }, protocol.CloseForbidden, http.StatusServiceUnavailable},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ts, _ := sessionServer(t, tt.edit)
			var opened, refused atomic.Int32
			var wg sync.WaitGroup
			start := make(chan struct{})
			for i := range 40 {
				tok := ts.token(t, tt.user(i), "ops")
				wg.Go(func() {
					<-start
					if i%2 == 0 {
						r := do(t, http.MethodPost, ts.srv.URL+"/api/v1/sessions", bearer(tok), `{"connection":"ops-shell"}`)
						switch r.code {
						case http.StatusCreated:
							opened.Add(1)
						case tt.status:
							refused.Add(1)
						default:
							t.Errorf("POST: %d %s", r.code, r.body)
						}
						return
					}
					c, _, err := dial(ts.srv.URL+"/api/v1/connections/ops-shell/stream", withToken(tok), &termHello, nil)
					if err != nil {
						t.Error(err)
						return
					}
					defer func() { _ = c.ws.CloseNow() }()
					m, err := c.next()
					switch m := m.(type) {
					case *protocol.Welcome:
						opened.Add(1)
					case *protocol.Close:
						if m.Code != tt.refused {
							t.Errorf("close %v %q, want %v", m.Code, m.Reason, tt.refused)
						}
						refused.Add(1)
					default:
						t.Errorf("stream: %#v %v", m, err)
					}
				})
			}
			close(start)
			wg.Wait()
			if opened.Load() != 3 || refused.Load() != 37 {
				t.Errorf("%d opened and %d refused at a limit of 3", opened.Load(), refused.Load())
			}
		})
	}
}

func TestAdhocReserved(t *testing.T) {
	ts, _ := sessionServer(t, func(f *config.File) {
		f.AllowAdhocTargets = true
		f.AdhocAllowlist = []string{"10.0.0.0/8:1-65535"}
		f.Guacd.Address = "10.9.9.9:4822"
		f.Listen = "10.8.8.8:8443"
		f.Debug.Address = "10.8.8.8:6060"
	})
	carol := ts.token(t, "carol")
	for _, tt := range []struct {
		target string
		status int
	}{
		{"10.9.9.9:4822", http.StatusForbidden},
		{"10.8.8.8:8443", http.StatusForbidden},
		{"10.8.8.8:6060", http.StatusForbidden},
		{"10.9.9.9:3389", http.StatusCreated},
	} {
		if r := ts.api(t, http.MethodPost, "/api/v1/sessions", carol, `{"protocol":"rdp","target":"`+tt.target+`"}`); r.code != tt.status {
			t.Errorf("%s: %d %s, want %d", tt.target, r.code, r.body, tt.status)
		}
	}
}

// TestConnectionTarget checks that a session on a connection of the
// registry gets every setting of the connection that a backend reads.
// The connection sets them all, and the target it wants sets every field
// but name, which only an ad-hoc target has, so that a field added to
// target is added here too.
func TestConnectionTarget(t *testing.T) {
	c := config.Connection{
		ID: "mac", Name: "Mac", Protocol: "vnc", Target: "mac.lab.internal:5900", Backend: config.BackendNative,
		HostKeys: []string{fakeHostKey}, InsecureIgnoreHostKey: true,
		TLS:         config.ConnectionTLS{Require: true, ServerName: "mac", CAFile: "/ca.pem", Pins: []string{"SHA256:pin"}, InsecureSkipVerify: true},
		ARD:         true,
		Kubernetes:  config.Kubernetes{Namespace: "web", Pod: "web-0", Container: "app", Command: []string{"sh"}},
		Credentials: config.Credentials{Username: "studio", PasswordFile: "/password", Prompt: true},
		Allow:       config.Allow{Users: []string{"alice"}},
	}
	want := target{
		connection:            "mac",
		protocol:              "vnc",
		backend:               config.BackendNative,
		addr:                  "mac.lab.internal:5900",
		creds:                 c.Credentials,
		hostKeys:              c.HostKeys,
		insecureIgnoreHostKey: true,
		tls:                   c.TLS,
		ard:                   true,
		kubernetes:            c.Kubernetes,
	}
	if got := connectionTarget(&c); !reflect.DeepEqual(got, want) {
		t.Errorf("connectionTarget = %+v, want %+v", got, want)
	}
	v := reflect.ValueOf(want)
	for i := range v.NumField() {
		if name := v.Type().Field(i).Name; name != "name" && v.Field(i).IsZero() {
			t.Errorf("the test sets no target.%s", name)
		}
	}
}

func TestAdhocTargets(t *testing.T) {
	ts, fb := sessionServer(t, func(f *config.File) {
		f.AllowAdhocTargets = true
		f.AdhocAllowlist = []string{"10.0.0.0/24", "192.168.1.5:3390", "*.lab.internal", "build.example:2222"}
	})
	resolved := map[string][]netip.Addr{
		"inside.example":    {netip.MustParseAddr("10.0.0.7")},
		"outside.example":   {netip.MustParseAddr("10.0.1.7")},
		"partly.example":    {netip.MustParseAddr("10.0.0.8"), netip.MustParseAddr("8.8.8.8")},
		"host.lab.internal": {netip.MustParseAddr("203.0.113.5")},
		"loop.lab.internal": {netip.MustParseAddr("127.0.0.1")},
		"build.example":     {netip.MustParseAddr("203.0.113.6")},
	}
	ts.s.adhoc.lookup = func(_ context.Context, host string) ([]netip.Addr, error) {
		if a, ok := resolved[host]; ok {
			return a, nil
		}
		return nil, errors.New("no such host")
	}
	carol := ts.token(t, "carol")
	for _, tt := range []struct {
		body   string
		status int
		addr   string // dialled
		name   string // the name a certificate names
	}{
		{`{"protocol":"vnc","target":"10.0.0.9"}`, http.StatusCreated, "10.0.0.9:5900", ""},
		{`{"protocol":"rdp","target":"192.168.1.5:3390","username":"carol"}`, http.StatusCreated, "192.168.1.5:3390", ""},
		{`{"protocol":"ssh","target":"host.lab.internal","insecure_ignore_host_key":true}`, http.StatusCreated, "203.0.113.5:22", "host.lab.internal"},
		{`{"protocol":"ssh","target":"build.example:2222","host_keys":["` + fakeHostKey + `"]}`, http.StatusCreated, "203.0.113.6:2222", "build.example"},
		{`{"protocol":"vnc","target":"inside.example"}`, http.StatusCreated, "10.0.0.7:5900", "inside.example"},
		{`{"protocol":"vnc","target":"[::ffff:10.0.0.3]:5901"}`, http.StatusCreated, "10.0.0.3:5901", ""},
		{`{"protocol":"rdp","target":"192.168.1.5"}`, http.StatusForbidden, "", ""},
		{`{"protocol":"ssh","target":"build.example","insecure_ignore_host_key":true}`, http.StatusForbidden, "", ""},
		{`{"protocol":"vnc","target":"10.0.0.9:22"}`, http.StatusForbidden, "", ""},
		{`{"protocol":"vnc","target":"loop.lab.internal"}`, http.StatusForbidden, "", ""},
		{`{"protocol":"vnc","target":"10.0.1.9"}`, http.StatusForbidden, "", ""},
		{`{"protocol":"vnc","target":"192.168.1.6"}`, http.StatusForbidden, "", ""},
		{`{"protocol":"vnc","target":"outside.example"}`, http.StatusForbidden, "", ""},
		{`{"protocol":"vnc","target":"partly.example"}`, http.StatusForbidden, "", ""},
		{`{"protocol":"vnc","target":"unknown.example"}`, http.StatusForbidden, "", ""},
		{`{"protocol":"vnc","target":"lab.internal"}`, http.StatusForbidden, "", ""},
		{`{"protocol":"vnc","target":"evil-lab.internal"}`, http.StatusForbidden, "", ""},
		{`{"protocol":"vnc","target":"build.example.evil"}`, http.StatusForbidden, "", ""},
		{`{"protocol":"ssh","target":"host.lab.internal"}`, http.StatusBadRequest, "", ""},
		{`{"protocol":"ssh","target":"host.lab.internal","host_keys":["nope"]}`, http.StatusBadRequest, "", ""},
		{`{"protocol":"ssh","target":"host.lab.internal","host_keys":["` + fakeHostKey + `"],"insecure_ignore_host_key":true}`, http.StatusBadRequest, "", ""},
		{`{"protocol":"vnc","target":"10.0.0.9","insecure_ignore_host_key":true}`, http.StatusBadRequest, "", ""},
		{`{"protocol":"kubernetes","target":"10.0.0.9:443"}`, http.StatusBadRequest, "", ""},
		{`{"protocol":"vnc","target":"10.0.0.9:99999"}`, http.StatusBadRequest, "", ""},
		{`{"target":"10.0.0.9:5900"}`, http.StatusBadRequest, "", ""},
	} {
		r := ts.api(t, http.MethodPost, "/api/v1/sessions", carol, tt.body)
		if r.code != tt.status {
			t.Errorf("%s: %d %s, want %d", tt.body, r.code, r.body, tt.status)
			continue
		}
		if tt.status != http.StatusCreated {
			continue
		}
		tg, _, _ := fb.last()
		if tg.addr != tt.addr || tg.name != tt.name || tg.connection != "" || !tg.creds.Prompt {
			t.Errorf("%s: target %+v, want %s named %q with prompted credentials", tt.body, tg, tt.addr, tt.name)
		}
		// An ad-hoc target has no setting that only a connection can
		// make: no ard, and the default tls policy, the system's roots.
		if tg.ard || !reflect.DeepEqual(tg.tls, config.ConnectionTLS{}) {
			t.Errorf("%s: target with ard %v and tls %+v, want neither", tt.body, tg.ard, tg.tls)
		}
		var o openResponse
		_ = json.Unmarshal(r.body, &o)
		if info := ts.s.session(o.ID); info == nil || info.owner != (principal{issuer: auth.Issuer, idp: auth.IdPLocal, subject: "carol"}) || info.connection != "" {
			t.Errorf("%s: session %+v", tt.body, info)
		}
	}
	// occamyd serves kubernetes, but only to the registry's connections.
	if r := ts.api(t, http.MethodPost, "/api/v1/sessions", carol, `{"protocol":"kubernetes","target":"10.0.0.9:443"}`); r.code != http.StatusBadRequest || !strings.Contains(string(r.body), "kubernetes has no ad-hoc targets") {
		t.Errorf("an ad-hoc kubernetes target: %d %s", r.code, r.body)
	}
}
