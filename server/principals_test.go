// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package server

import (
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"

	"changkun.de/x/occamy/internal/auth"
	"changkun.de/x/occamy/internal/config"
	"changkun.de/x/occamy/protocol"
)

// matrixToken is a token of the principal matrix.
type matrixToken struct {
	name   string
	token  string
	want   principal // the principal the token must map to
	refuse bool      // the token must be refused as a whole
}

// matrixServer serves static users alice, bob and zoë, OIDC with
// link_static_users as linkOIDC says, and three trusted issuers, of which
// "linked" links static users. It returns the issuers' keys.
func matrixServer(t *testing.T, linkOIDC bool) (*testServer, map[string]ed25519.PrivateKey) {
	k := newKeys(t)
	_, third, _ := ed25519.GenerateKey(nil)
	keys := map[string]ed25519.PrivateKey{"gateway": k.gateway, "linked": k.stranger, "other": third}
	ts, _ := sessionServer(t, func(f *config.File) {
		f.Auth.Users = append(f.Auth.Users, config.User{Name: "zoë", PasswordHash: hashPassword(t, "zzzzzzzzzzzz", bcrypt.MinCost), Groups: []string{"ops"}})
		f.Auth.TrustedIssuers = []config.TrustedIssuer{
			{Issuer: "gateway", PublicKeyFile: writeIssuerKey(t, keys["gateway"]), AllowedGroups: []string{"ops"}},
			{Issuer: "linked", PublicKeyFile: writeIssuerKey(t, keys["linked"]), LinkStaticUsers: true, AllowedGroups: []string{"ops"}},
			{Issuer: "other", PublicKeyFile: writeIssuerKey(t, keys["other"]), AllowedGroups: []string{"ops"}},
		}
		f.Auth.OIDC = &config.OIDC{
			Issuer: "https://idp.example", ClientID: "occamy",
			RedirectURL: "https://occamy.example/api/v1/auth/oidc/callback",
			ReturnURLs:  []string{"https://occamy.example/"}, SubjectClaim: "sub",
			LinkStaticUsers: linkOIDC,
		}
		f.Limits.ViewersPerSession = 1000
		f.Limits.SessionsPerUser = 1000
	})
	return ts, keys
}

// TestPrincipalMatrix runs every token kind, occamyd's static and OIDC
// users, trusted issuers linked and not, and names in another case or
// normal form, against a session of every owner they map to: over
// REST, an access token's stream, a trusted issuer's session token of
// every role, with and without a forged idp or uiss, and shares. Only
// the owner reads, shares, closes or owns a session; a trusted
// issuer's session token joins only its own users' sessions, or a
// static user's when the issuer is linked.
func TestPrincipalMatrix(t *testing.T) {
	for _, linkOIDC := range []bool{false, true} {
		t.Run(fmt.Sprintf("link=%v", linkOIDC), func(t *testing.T) { principalMatrix(t, linkOIDC) })
	}
}

func principalMatrix(t *testing.T, linkOIDC bool) {
	ts, keys := matrixServer(t, linkOIDC)
	local := func(s string) principal { return principal{issuer: auth.Issuer, idp: auth.IdPLocal, subject: s} }
	oidcP := func(s string) principal { return principal{issuer: auth.Issuer, idp: auth.IdPOIDC, subject: s} }
	iss := func(i, s string) principal { return principal{issuer: i, subject: s} }
	oidcTok := func(sub string) string {
		tok, err := ts.s.signer.AccessFrom(auth.IdPOIDC, sub, []string{"ops"}, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		return tok
	}
	foreign := func(issuer, sub string) string { return signClaims(t, keys[issuer], access(issuer, sub, "ops")) }
	foreignIdP := func(issuer, idp, sub string) string {
		c := access(issuer, sub, "ops")
		c.IdP = idp
		return signClaims(t, keys[issuer], c)
	}

	toks := []matrixToken{
		{name: "static alice", token: ts.token(t, "alice", "ops"), want: local("alice")},
		{name: "static bob", token: ts.token(t, "bob", "ops"), want: local("bob")},
		{name: "static zoë", token: ts.token(t, "zoë", "ops"), want: local("zoë")},
		{name: "oidc dave", token: oidcTok("dave"), want: oidcP("dave")},
		{name: "oidc carol", token: oidcTok("carol"), want: oidcP("carol")},
		{name: "oidc erin", token: oidcTok("erin"), want: oidcP("erin")},
		{name: "gateway carol", token: foreign("gateway", "carol"), want: iss("gateway", "carol")},
		{name: "gateway dave", token: foreign("gateway", "dave"), want: iss("gateway", "dave")},
		{name: "gateway erin", token: foreign("gateway", "erin"), want: iss("gateway", "erin")},
		{name: "gateway carol idp=local", token: foreignIdP("gateway", auth.IdPLocal, "carol"), want: iss("gateway", "carol")},
		{name: "gateway bob idp=local", token: foreignIdP("gateway", auth.IdPLocal, "bob"), refuse: true},
		{name: "other carol", token: foreign("other", "carol"), want: iss("other", "carol")},
		{name: "linked erin", token: foreign("linked", "erin"), want: iss("linked", "erin")},
		{name: "linked carol", token: foreign("linked", "carol"), want: iss("linked", "carol")},
		{name: "linked alice", token: foreign("linked", "alice"), want: local("alice")},
		{name: "linked Alice", token: foreign("linked", "Alice"), want: iss("linked", "Alice")},
		{name: "linked zoe+U+0308", token: foreign("linked", "zoe\u0308"), want: iss("linked", "zoe\u0308")},
		{name: "gateway alice", token: foreign("gateway", "alice"), refuse: true},
		{name: "gateway ALICE", token: foreign("gateway", "ALICE"), refuse: true},
		{name: "gateway ZOE+U+0308", token: foreign("gateway", "ZOE\u0308"), refuse: true},
		{name: "other bob", token: foreign("other", "bob"), refuse: true},
	}
	if linkOIDC {
		toks = append(toks,
			matrixToken{name: "oidc alice (linked)", token: oidcTok("alice"), want: local("alice")},
			matrixToken{name: "oidc Alice (linked, other case)", token: oidcTok("Alice"), want: oidcP("Alice")},
			matrixToken{name: "oidc zoe+U+0308 (linked, NFD)", token: oidcTok("zoe\u0308"), want: oidcP("zoe\u0308")},
		)
	} else {
		toks = append(toks,
			matrixToken{name: "oidc alice (unlinked)", token: oidcTok("alice"), refuse: true},
			matrixToken{name: "oidc ALICE (unlinked)", token: oidcTok("ALICE"), refuse: true},
			matrixToken{name: "oidc Zoe+U+0308 (unlinked, NFD)", token: oidcTok("Zoe\u0308"), refuse: true},
		)
	}

	// Sessions, one per distinct owner principal that a usable token
	// maps to.
	type sess struct {
		owner principal
		id    string
		share string
	}
	var sessions []*sess
	seen := map[principal]bool{}
	for _, tk := range toks {
		if tk.refuse || seen[tk.want] {
			continue
		}
		seen[tk.want] = true
		o := ts.openSession(t, tk.token, "ops-shell")
		if info := ts.s.session(o.ID); info == nil || info.owner != tk.want {
			t.Fatalf("%s opened a session owned by %+v, want %+v", tk.name, info, tk.want)
		}
		r := ts.api(t, http.MethodPost, "/api/v1/sessions/"+o.ID+"/shares", tk.token, `{"role":"viewer"}`)
		if r.code != http.StatusCreated {
			t.Fatalf("%s sharing its own session: %d %s", tk.name, r.code, r.body)
		}
		var sh shareResponse
		if err := json.Unmarshal(r.body, &sh); err != nil {
			t.Fatal(err)
		}
		sessions = append(sessions, &sess{owner: tk.want, id: o.ID, share: sh.Token})
	}

	// 1. REST and access-token streams: only the owner principal.
	for _, tk := range toks {
		for _, s := range sessions {
			mine := !tk.refuse && tk.want == s.owner
			r := ts.api(t, http.MethodGet, "/api/v1/sessions/"+s.id, tk.token, "")
			switch {
			case tk.refuse && r.code != http.StatusUnauthorized:
				t.Errorf("GET %v's by %s: %d, want 401", s.owner, tk.name, r.code)
			case mine && r.code != http.StatusOK:
				t.Errorf("GET %v's by %s: %d, want 200", s.owner, tk.name, r.code)
			case !mine && !tk.refuse && r.code != http.StatusNotFound:
				t.Errorf("GET %v's by %s: %d, want 404", s.owner, tk.name, r.code)
			}
			if !mine {
				r = ts.api(t, http.MethodPost, "/api/v1/sessions/"+s.id+"/shares", tk.token, `{"role":"viewer"}`)
				if r.code == http.StatusCreated {
					t.Errorf("SHARE %v's by %s: %d", s.owner, tk.name, r.code)
				}
				r = ts.api(t, http.MethodDelete, "/api/v1/sessions/"+s.id, tk.token, "")
				if r.code == http.StatusNoContent {
					t.Errorf("DELETE %v's by %s: %d", s.owner, tk.name, r.code)
				}
			}
			role, code := joinRole(t, ts, s.id, tk.token)
			switch {
			case mine && role != protocol.RoleOwner:
				t.Errorf("stream %v's by %s: role %v close %v, want owner", s.owner, tk.name, role, code)
			case !mine && role != 0:
				t.Errorf("stream %v's by %s: JOINED as %v", s.owner, tk.name, role)
			}
		}
	}

	// 2. Trusted issuers' session tokens, every role, several subjects.
	linked := map[string]bool{"linked": true}
	for _, issuer := range []string{"gateway", "other", "linked"} {
		for _, role := range []string{auth.RoleViewer, auth.RoleReadOnly, auth.RoleOwner} {
			for _, sub := range []string{"carol", "dave", "erin", "alice", "bob", "Alice", "mallory", "zoe\u0308"} {
				for _, s := range sessions {
					c := auth.Claims{
						Kind: auth.KindSession, Session: s.id, Role: role,
						RegisteredClaims: jwt.RegisteredClaims{Issuer: issuer, Subject: sub, Audience: jwt.ClaimStrings{auth.Audience}},
					}
					tok := signClaims(t, keys[issuer], c)
					// The principal the token speaks for.
					var p principal
					refused := false
					switch {
					case linked[issuer] && (sub == "alice" || sub == "bob" || sub == "zoë"):
						p = local(sub)
					case !linked[issuer] && isStaticName(sub):
						refused = true
					default:
						p = iss(issuer, sub)
					}
					grant := s.owner.issuer == issuer || linked[issuer] && s.owner.issuer == auth.Issuer && s.owner.idp == auth.IdPLocal
					want := protocol.Role(0)
					if !refused && grant {
						switch role {
						case auth.RoleViewer:
							want = protocol.RoleViewer
						case auth.RoleReadOnly:
							want = protocol.RoleReadOnly
						case auth.RoleOwner:
							if p == s.owner {
								want = protocol.RoleOwner
							}
						}
					}
					got, code := joinRole(t, ts, s.id, tok)
					if got != want {
						t.Errorf("%s %s token for %q on %v's session: role %v (close %v), want %v", issuer, role, sub, s.owner, got, code, want)
					}
					// Client-chosen idp and uiss are refused outright.
					c.IdP = auth.IdPLocal
					if got, code := joinRole(t, ts, s.id, signClaims(t, keys[issuer], c)); got != 0 || code != protocol.CloseUnauthorized {
						t.Errorf("%s %s token with idp for %q on %v's: role %v close %v", issuer, role, sub, s.owner, got, code)
					}
					c.IdP = ""
					c.UserIssuer = "gateway"
					if got, code := joinRole(t, ts, s.id, signClaims(t, keys[issuer], c)); got != 0 || code != protocol.CloseUnauthorized {
						t.Errorf("%s %s token with uiss for %q on %v's: role %v close %v", issuer, role, sub, s.owner, got, code)
					}
				}
			}
		}
	}

	// 3. Shares and stream tokens open only their own session.
	for _, s := range sessions {
		for _, o := range sessions {
			got, _ := joinRole(t, ts, o.id, s.share)
			if s != o && got != 0 {
				t.Errorf("share of %v's joined %v's as %v", s.owner, o.owner, got)
			}
			if s == o && got != protocol.RoleViewer {
				t.Errorf("share of %v's on its own: %v", s.owner, got)
			}
		}
	}
}

// isStaticName reports whether s is a static user's name in any case or
// normal form.
func isStaticName(s string) bool {
	for _, n := range []string{"alice", "bob", "zoë"} {
		if config.SameName(s, n) {
			return true
		}
	}
	return false
}

// joinRole dials the session's stream with token and returns the role
// of its welcome, or its close code.
func joinRole(t *testing.T, ts *testServer, id, token string) (protocol.Role, protocol.CloseCode) {
	t.Helper()
	c, status, err := dial(ts.srv.URL+"/api/v1/sessions/"+id+"/stream", withToken(token), &termHello, nil)
	if err != nil {
		t.Fatalf("%d %v", status, err)
	}
	defer func() { _ = c.ws.CloseNow() }()
	m, err := c.next()
	switch m := m.(type) {
	case *protocol.Welcome:
		return m.Role, 0
	case *protocol.Close:
		return 0, m.Code
	}
	t.Fatalf("join: %T %v", m, err)
	return 0, 0
}
