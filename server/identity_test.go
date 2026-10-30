// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package server

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"changkun.de/x/occamy/internal/auth"
	"changkun.de/x/occamy/internal/config"
	"changkun.de/x/occamy/protocol"
)

func TestIdentities(t *testing.T) {
	ids := newIdentities(&config.File{Auth: config.Auth{
		Users:          []config.User{{Name: "alice"}},
		TrustedIssuers: []config.TrustedIssuer{{Issuer: "gateway"}, {Issuer: "linked", LinkStaticUsers: true}},
	}})
	claims := func(iss, uiss, sub string) *auth.Claims {
		return &auth.Claims{UserIssuer: uiss, RegisteredClaims: jwt.RegisteredClaims{Issuer: iss, Subject: sub}}
	}
	for _, tt := range []struct {
		name   string
		claims *auth.Claims
		want   principal
		err    error
	}{
		{"a static user", claims(auth.Issuer, "", "alice"), principal{issuer: auth.Issuer, subject: "alice"}, nil},
		{"another of occamyd's users", claims(auth.Issuer, "", "carol"), principal{issuer: auth.Issuer, subject: "carol"}, nil},
		{"a trusted issuer's user", claims("gateway", "", "carol"), principal{issuer: "gateway", subject: "carol"}, nil},
		{"a trusted issuer's static name", claims("gateway", "", "alice"), principal{}, errStaticUser},
		{"a linked issuer's static name", claims("linked", "", "alice"), principal{issuer: auth.Issuer, subject: "alice"}, nil},
		{"a linked issuer's own user", claims("linked", "", "carol"), principal{issuer: "linked", subject: "carol"}, nil},
		{"occamyd's token for a trusted issuer's user", claims(auth.Issuer, "gateway", "carol"), principal{issuer: "gateway", subject: "carol"}, nil},
		{"a share-like token", claims("gateway", "", ""), principal{issuer: "gateway"}, nil},
	} {
		got, err := ids.of(tt.claims)
		if got != tt.want || !errors.Is(err, tt.err) {
			t.Errorf("%s: %+v %v, want %+v %v", tt.name, got, err, tt.want, tt.err)
		}
	}
}

// TestIssuersOwnTheirSessions checks that a session belongs to its owner
// as a subject of an issuer: the same subject from another issuer is
// another user, and a trusted issuer cannot name a static user unless it
// is linked to them.
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
