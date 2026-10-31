// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package server

import (
	"crypto/ed25519"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"changkun.de/x/occamy/internal/auth"
	"changkun.de/x/occamy/internal/config"
	"changkun.de/x/occamy/protocol"
)

func TestIdentities(t *testing.T) {
	file := func(oidcLinked bool) *config.File {
		return &config.File{Auth: config.Auth{
			Users:          []config.User{{Name: "alice"}, {Name: "zoë"}},
			TrustedIssuers: []config.TrustedIssuer{{Issuer: "gateway"}, {Issuer: "linked", LinkStaticUsers: true}},
			OIDC:           &config.OIDC{LinkStaticUsers: oidcLinked},
		}}
	}
	ids, linked := newIdentities(file(false)), newIdentities(file(true))
	claims := func(iss, uiss, idp, sub string) *auth.Claims {
		return &auth.Claims{UserIssuer: uiss, IdP: idp, RegisteredClaims: jwt.RegisteredClaims{Issuer: iss, Subject: sub}}
	}
	own := func(idp, sub string) principal { return principal{issuer: auth.Issuer, idp: idp, subject: sub} }
	for _, tt := range []struct {
		name   string
		ids    identities
		claims *auth.Claims
		want   principal
		err    error
	}{
		{"a static user", ids, claims(auth.Issuer, "", auth.IdPLocal, "alice"), own(auth.IdPLocal, "alice"), nil},
		{"a static user's token from before idp", ids, claims(auth.Issuer, "", "", "alice"), own(auth.IdPLocal, "alice"), nil},
		{"an OIDC user", ids, claims(auth.Issuer, "", auth.IdPOIDC, "carol"), own(auth.IdPOIDC, "carol"), nil},
		{"an OIDC user linked to a static user", linked, claims(auth.Issuer, "", auth.IdPOIDC, "alice"), own(auth.IdPLocal, "alice"), nil},
		{"an OIDC user no longer linked", ids, claims(auth.Issuer, "", auth.IdPOIDC, "alice"), principal{}, errStaticUser},
		{"an OIDC user no longer linked, in another case", ids, claims(auth.Issuer, "", auth.IdPOIDC, "Alice"), principal{}, errStaticUser},
		{"a linked OIDC user in another case", linked, claims(auth.Issuer, "", auth.IdPOIDC, "Alice"), own(auth.IdPOIDC, "Alice"), nil},
		{"a trusted issuer's user", ids, claims("gateway", "", "", "carol"), principal{issuer: "gateway", subject: "carol"}, nil},
		{"a trusted issuer's idp is not read", ids, claims("gateway", "", auth.IdPLocal, "carol"), principal{issuer: "gateway", subject: "carol"}, nil},
		{"a trusted issuer's static name", ids, claims("gateway", "", "", "alice"), principal{}, errStaticUser},
		{"a trusted issuer's static name in another case", ids, claims("gateway", "", "", "ALICE"), principal{}, errStaticUser},
		{"a trusted issuer's static name in another normal form", ids, claims("gateway", "", "", "zoe\u0308"), principal{}, errStaticUser},
		{"a trusted issuer's subject with a zero-width space", ids, claims("gateway", "", "", "al\u200bice"), principal{}, errSubject},
		{"a trusted issuer's subject of a blank Braille pattern", ids, claims("gateway", "", "", "\u2800"), principal{}, errSubject},
		{"a trusted issuer's subject with a tab", ids, claims("gateway", "", "", "carol\t"), principal{}, errSubject},
		{"a linked issuer's static name in another case", ids, claims("linked", "", "", "Alice"), principal{issuer: "linked", subject: "Alice"}, nil},
		{"a linked issuer's static name", ids, claims("linked", "", "", "alice"), own(auth.IdPLocal, "alice"), nil},
		{"a linked issuer's own user", ids, claims("linked", "", "", "carol"), principal{issuer: "linked", subject: "carol"}, nil},
		{"occamyd's token for a trusted issuer's user", ids, claims(auth.Issuer, "gateway", "", "carol"), principal{issuer: "gateway", subject: "carol"}, nil},
		{"a trusted issuer's token without a subject", ids, claims("gateway", "", "", ""), principal{}, errSubject},
		{"occamyd's token without a subject", ids, claims(auth.Issuer, "", "", ""), principal{}, errSubject},
		{"occamyd's token for a trusted issuer's user without a subject", ids, claims(auth.Issuer, "gateway", "", ""), principal{}, errSubject},
		{"occamyd's token with a zero-width space", ids, claims(auth.Issuer, "", auth.IdPOIDC, "car\u200bol"), principal{}, errSubject},
	} {
		got, err := tt.ids.of(tt.claims)
		if got != tt.want || !errors.Is(err, tt.err) {
			t.Errorf("%s: %+v %v, want %+v %v", tt.name, got, err, tt.want, tt.err)
		}
	}
}

// TestOIDCOwners checks that an OIDC user linked to a static user owns
// the static user's sessions, whichever of the two opened them, that
// the stream token says where the user signed in, and that the OIDC
// user's tokens are refused once the two are no longer linked.
func TestOIDCOwners(t *testing.T) {
	oidcFile := func(link bool) func(*config.File) {
		return func(f *config.File) {
			f.Auth.OIDC = &config.OIDC{
				Issuer:          "https://idp.example",
				ClientID:        "occamy",
				RedirectURL:     "https://occamy.example/api/v1/auth/oidc/callback",
				ReturnURLs:      []string{"https://occamy.example/"},
				SubjectClaim:    "sub",
				LinkStaticUsers: link,
			}
		}
	}
	ts, _ := sessionServer(t, oidcFile(true))
	oidc := func(ts *testServer, sub string) string {
		tok, err := ts.s.signer.AccessFrom(auth.IdPOIDC, sub, []string{"ops"}, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		return tok
	}
	local := ts.token(t, "alice", "ops")
	mine := ts.openSession(t, local, "ops-shell")
	if r := ts.api(t, http.MethodGet, "/api/v1/sessions/"+mine.ID, oidc(ts, "alice"), ""); r.code != http.StatusOK {
		t.Errorf("the linked OIDC alice reading the static alice's session: %d %s", r.code, r.body)
	}
	if c := ts.stream(t, "/api/v1/sessions/"+mine.ID+"/stream", oidc(ts, "alice"), termHello); c.welcome.Role != protocol.RoleOwner {
		t.Errorf("the linked OIDC alice joined as %v, want the owner", c.welcome.Role)
	}
	theirs := ts.openSession(t, oidc(ts, "alice"), "ops-shell")
	if info := ts.s.session(theirs.ID); info == nil || info.owner != (principal{issuer: auth.Issuer, idp: auth.IdPLocal, subject: "alice"}) {
		t.Errorf("the linked OIDC alice's session belongs to %+v", info)
	}
	if c, err := ts.s.verifier.VerifySession(theirs.StreamToken); err != nil || c.IdP != auth.IdPOIDC || c.Subject != "alice" {
		t.Errorf("stream token %+v %v, want idp oidc and alice", c, err)
	}
	if c := ts.stream(t, "/api/v1/sessions/"+theirs.ID+"/stream", theirs.StreamToken, termHello); c.welcome.Role != protocol.RoleOwner {
		t.Errorf("the stream token joined as %v, want the owner", c.welcome.Role)
	}
	// An OIDC user of another name owns sessions of its own.
	dave := ts.openSession(t, oidc(ts, "dave"), "ops-shell")
	if info := ts.s.session(dave.ID); info == nil || info.owner != (principal{issuer: auth.Issuer, idp: auth.IdPOIDC, subject: "dave"}) {
		t.Errorf("dave's session belongs to %+v", info)
	}
	if r := ts.api(t, http.MethodGet, "/api/v1/sessions/"+dave.ID, local, ""); r.code != http.StatusNotFound {
		t.Errorf("the static alice reading dave's session: %d", r.code)
	}

	// The file without the link: tokens of the linked OIDC alice, issued
	// before a restart with the same key, are refused.
	unlinked, _ := sessionServer(t, oidcFile(false))
	o := unlinked.openSession(t, unlinked.token(t, "alice", "ops"), "ops-shell")
	for _, sub := range []string{"alice", "Alice"} {
		r := unlinked.api(t, http.MethodGet, "/api/v1/sessions/"+o.ID, oidc(unlinked, sub), "")
		if r.code != http.StatusUnauthorized || !strings.Contains(string(r.body), codeInvalidToken) {
			t.Errorf("an unlinked OIDC %s: %d %s, want 401 invalid_token", sub, r.code, r.body)
		}
	}
	c, status, err := dial(unlinked.srv.URL+"/api/v1/sessions/"+o.ID+"/stream", withToken(oidc(unlinked, "alice")), &termHello, nil)
	if err != nil {
		t.Fatalf("%d %v", status, err)
	}
	c.closed(t, protocol.CloseUnauthorized)
	stale, err := unlinked.s.signer.SessionFor(o.ID, auth.RoleOwner, auth.User{IdP: auth.IdPOIDC, Subject: "alice"}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	c, status, err = dial(unlinked.srv.URL+"/api/v1/sessions/"+o.ID+"/stream", withToken(stale), &termHello, nil)
	if err != nil {
		t.Fatalf("%d %v", status, err)
	}
	c.closed(t, protocol.CloseUnauthorized)
}

// TestIssuersOwnTheirSessions checks that a session belongs to its owner
// as a subject of an issuer: the same subject from another issuer is
// another user, and a trusted issuer cannot name a static user unless it
// is linked to them.
// TestIssuerSessionTokens checks that a trusted issuer's session token, of
// any role, joins only its own users' sessions, or a static user's if the
// issuer is linked to the static users. An unlinked gateway's viewer
// token, for no subject, once joined static alice's session and typed
// into it.
func TestIssuerSessionTokens(t *testing.T) {
	k := newKeys(t)
	gatewayKey := writeIssuerKey(t, k.gateway)
	linkedKey := writeIssuerKey(t, k.stranger)
	ts, _ := sessionServer(t, func(f *config.File) {
		f.Auth.TrustedIssuers = []config.TrustedIssuer{
			{Issuer: "gateway", PublicKeyFile: gatewayKey},
			{Issuer: "linked", PublicKeyFile: linkedKey, LinkStaticUsers: true},
		}
		f.Connections[0].Allow.Users = []string{"carol", "dave"}
	})
	key := map[string]ed25519.PrivateKey{"gateway": k.gateway, "linked": k.stranger}
	ids := map[string]string{
		"alice": ts.openSession(t, ts.token(t, "alice", "ops"), "ops-shell").ID,
		"carol": ts.openSession(t, signClaims(t, k.gateway, access("gateway", "carol")), "ops-shell").ID,
		"dave":  ts.openSession(t, signClaims(t, k.stranger, access("linked", "dave")), "ops-shell").ID,
	}
	const (
		owner    = protocol.RoleOwner
		viewer   = protocol.RoleViewer
		readOnly = protocol.RoleReadOnly
	)
	for _, tt := range []struct {
		owner, issuer, role, sub string
		joins                    protocol.Role
		refused                  protocol.CloseCode
	}{
		// The gateway, unlinked: its own users' sessions only.
		{"alice", "gateway", auth.RoleViewer, "", 0, protocol.CloseUnauthorized},
		{"alice", "gateway", auth.RoleViewer, "mallory", 0, protocol.CloseForbidden},
		{"alice", "gateway", auth.RoleReadOnly, "mallory", 0, protocol.CloseForbidden},
		{"alice", "gateway", auth.RoleOwner, "alice", 0, protocol.CloseUnauthorized},
		{"dave", "gateway", auth.RoleViewer, "mallory", 0, protocol.CloseForbidden},
		{"dave", "gateway", auth.RoleReadOnly, "dave", 0, protocol.CloseForbidden},
		{"dave", "gateway", auth.RoleOwner, "dave", 0, protocol.CloseForbidden},
		{"carol", "gateway", auth.RoleViewer, "frank", viewer, 0},
		{"carol", "gateway", auth.RoleReadOnly, "frank", readOnly, 0},
		{"carol", "gateway", auth.RoleOwner, "frank", 0, protocol.CloseForbidden},
		{"carol", "gateway", auth.RoleOwner, "carol", owner, 0},
		// The linked issuer: its own users' and the static users'.
		{"alice", "linked", auth.RoleViewer, "erin", viewer, 0},
		{"alice", "linked", auth.RoleReadOnly, "erin", readOnly, 0},
		{"alice", "linked", auth.RoleOwner, "alice", owner, 0},
		{"carol", "linked", auth.RoleViewer, "erin", 0, protocol.CloseForbidden},
		{"carol", "linked", auth.RoleReadOnly, "erin", 0, protocol.CloseForbidden},
		{"carol", "linked", auth.RoleOwner, "carol", 0, protocol.CloseForbidden},
		{"dave", "linked", auth.RoleViewer, "erin", viewer, 0},
		{"dave", "linked", auth.RoleOwner, "dave", owner, 0},
	} {
		name := tt.issuer + "'s " + tt.role + " token for " + tt.owner + "'s session, for " + tt.sub
		token := signClaims(t, key[tt.issuer], auth.Claims{
			Kind: auth.KindSession, Session: ids[tt.owner], Role: tt.role,
			RegisteredClaims: jwt.RegisteredClaims{Issuer: tt.issuer, Subject: tt.sub, Audience: jwt.ClaimStrings{auth.Audience}},
		})
		c, status, err := dial(ts.srv.URL+"/api/v1/sessions/"+ids[tt.owner]+"/stream", withToken(token), &termHello, nil)
		if err != nil {
			t.Fatalf("%s: %d %v", name, status, err)
		}
		m, err := c.next()
		switch m := m.(type) {
		case *protocol.Welcome:
			if tt.joins != m.Role {
				t.Errorf("%s: joined as %v, want close %v", name, m.Role, tt.refused)
			}
		case *protocol.Close:
			if m.Code != tt.refused {
				t.Errorf("%s: close %v %q, want role %v or close %v", name, m.Code, m.Reason, tt.joins, tt.refused)
			}
		default:
			t.Errorf("%s: %T %v", name, m, err)
		}
		_ = c.ws.CloseNow()
	}
}

// TestIssuerGroups checks that a trusted issuer's groups open
// connections only where its allowed_groups lets it grant them: an
// unlisted group is dropped, on every route that reads groups.
func TestIssuerGroups(t *testing.T) {
	k := newKeys(t)
	gatewayKey := writeIssuerKey(t, k.gateway)
	grantingKey := writeIssuerKey(t, k.stranger)
	ts, _ := sessionServer(t, func(f *config.File) {
		f.Auth.TrustedIssuers = []config.TrustedIssuer{
			{Issuer: "gateway", PublicKeyFile: gatewayKey},
			{Issuer: "granting", PublicKeyFile: grantingKey, AllowedGroups: []string{"ops"}},
		}
	})
	mallory := signClaims(t, k.gateway, access("gateway", "mallory", "ops"))
	if r := ts.api(t, http.MethodPost, "/api/v1/sessions", mallory, `{"connection":"ops-shell"}`); r.code != http.StatusNotFound {
		t.Errorf("the gateway's ops, not allowed: %d %s, want 404", r.code, r.body)
	}
	c, status, err := dial(ts.srv.URL+"/api/v1/connections/ops-shell/stream", withToken(mallory), &termHello, nil)
	if err != nil {
		t.Fatalf("%d %v", status, err)
	}
	c.closed(t, protocol.CloseForbidden)

	erin := signClaims(t, k.stranger, access("granting", "erin", "ops", "dev"))
	ts.openSession(t, erin, "ops-shell")
	if r := ts.api(t, http.MethodPost, "/api/v1/sessions", erin, `{"connection":"both"}`); r.code != http.StatusNotFound {
		t.Errorf("an allowed issuer's dev, not in its allowed_groups: %d %s, want 404", r.code, r.body)
	}
	if w := ts.stream(t, "/api/v1/connections/ops-shell/stream", erin, termHello).welcome; w.Role != protocol.RoleOwner {
		t.Errorf("an allowed issuer's ops on a stream: %+v", w)
	}
}

func TestIssuersOwnTheirSessions(t *testing.T) {
	k := newKeys(t)
	gatewayKey := writeIssuerKey(t, k.gateway)
	linkedKey := writeIssuerKey(t, k.stranger)
	ts, _ := sessionServer(t, func(f *config.File) {
		f.Auth.TrustedIssuers = []config.TrustedIssuer{
			{Issuer: "gateway", PublicKeyFile: gatewayKey},
			{Issuer: "linked", PublicKeyFile: linkedKey, LinkStaticUsers: true},
		}
		f.Connections[0].Allow.Users = []string{"carol"}
	})
	alice := ts.token(t, "alice", "ops")
	carol := ts.token(t, "carol")
	gatewayAlice := signClaims(t, k.gateway, access("gateway", "alice", "ops"))
	gatewayCarol := signClaims(t, k.gateway, access("gateway", "carol"))
	linkedAlice := signClaims(t, k.stranger, access("linked", "alice", "ops"))

	// The gateway may not name alice, a static user.
	if r := ts.api(t, http.MethodGet, "/api/v1/connections", gatewayAlice, ""); r.code != http.StatusUnauthorized || decodeError(t, r.body).Code != codeInvalidToken {
		t.Errorf("the gateway's alice: %d %s, want 401", r.code, r.body)
	}
	o := ts.openSession(t, alice, "ops-shell")
	path := "/api/v1/sessions/" + o.ID + "/stream"
	hers := ts.stream(t, path, o.StreamToken, termHello)
	c, status, err := dial(ts.srv.URL+path, withToken(gatewayAlice), &termHello, nil)
	if err != nil {
		t.Fatalf("%d %v", status, err)
	}
	c.closed(t, protocol.CloseUnauthorized)
	c, status, err = dial(ts.srv.URL+"/api/v1/connections/ops-shell/stream", withToken(gatewayAlice), &termHello, nil)
	if err != nil {
		t.Fatalf("%d %v", status, err)
	}
	c.closed(t, protocol.CloseUnauthorized)

	// An issuer linked to static users acts as them.
	if r := ts.api(t, http.MethodGet, "/api/v1/sessions/"+o.ID, linkedAlice, ""); r.code != http.StatusOK {
		t.Errorf("the linked issuer's alice: %d %s, want her session", r.code, r.body)
	}
	ts.stream(t, path, linkedAlice, termHello)
	hers.closed(t, protocol.CloseNormal) // taken over by the same user

	// carol of occamyd and carol of the gateway are two users.
	theirs := ts.openSession(t, gatewayCarol, "ops-shell")
	mine := ts.openSession(t, carol, "ops-shell")
	for _, tt := range []struct {
		token, id string
	}{{carol, theirs.ID}, {gatewayCarol, mine.ID}} {
		for _, route := range []struct{ method, path, body string }{
			{http.MethodGet, "/api/v1/sessions/" + tt.id, ""},
			{http.MethodPost, "/api/v1/sessions/" + tt.id + "/shares", `{"role":"viewer"}`},
			{http.MethodDelete, "/api/v1/sessions/" + tt.id, ""},
		} {
			if r := ts.api(t, route.method, route.path, tt.token, route.body); r.code != http.StatusNotFound {
				t.Errorf("%s %s by the other carol: %d %s, want 404", route.method, route.path, r.code, r.body)
			}
		}
		c, status, err := dial(ts.srv.URL+"/api/v1/sessions/"+tt.id+"/stream", withToken(tt.token), &termHello, nil)
		if err != nil {
			t.Fatalf("%d %v", status, err)
		}
		c.closed(t, protocol.CloseForbidden)
	}
	// The gateway's carol's stream token is hers alone: it names her
	// issuer, so that it opens her session as its owner.
	ts.stream(t, "/api/v1/sessions/"+theirs.ID+"/stream", theirs.StreamToken, termHello)
	own, err := ts.s.signer.Session(theirs.ID, auth.RoleOwner, "carol", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	c, status, err = dial(ts.srv.URL+"/api/v1/sessions/"+theirs.ID+"/stream", withToken(own), &termHello, nil)
	if err != nil {
		t.Fatalf("%d %v", status, err)
	}
	c.closed(t, protocol.CloseForbidden)
	if r := ts.api(t, http.MethodDelete, "/api/v1/sessions/"+theirs.ID, gatewayCarol, ""); r.code != http.StatusNoContent {
		t.Errorf("the gateway's carol closing her session: %d %s", r.code, r.body)
	}
}
