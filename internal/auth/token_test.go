// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package auth

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

var epoch = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

type fixture struct {
	signer   *Signer
	gateway  *Signer
	stranger *Signer
	verifier *Verifier
	pub      ed25519.PublicKey
	clock    *time.Time
	t        *testing.T
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	newKey := func() (ed25519.PublicKey, ed25519.PrivateKey) {
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		return pub, priv
	}
	pub, priv := newKey()
	gwPub, gwPriv := newKey()
	_, strangerPriv := newKey()
	clock := epoch
	now := func() time.Time { return clock }
	f := &fixture{
		signer:   NewSigner(priv, "occamy", "occamy"),
		gateway:  NewSigner(gwPriv, "gateway", "occamy"),
		stranger: NewSigner(strangerPriv, "gateway", "occamy"),
		verifier: NewVerifier("occamy", map[string]ed25519.PublicKey{"occamy": pub, "gateway": gwPub}),
		pub:      pub,
		clock:    &clock,
		t:        t,
	}
	for _, s := range []*Signer{f.signer, f.gateway, f.stranger} {
		s.now = now
	}
	f.verifier.now = now
	return f
}

func (f *fixture) must(token string, err error) string {
	f.t.Helper()
	if err != nil {
		f.t.Fatal(err)
	}
	return token
}

func TestAccess(t *testing.T) {
	f := newFixture(t)
	tok := f.must(f.signer.Access("alice", []string{"ops"}, 15*time.Minute))
	c, err := f.verifier.VerifyAccess(tok)
	if err != nil {
		t.Fatal(err)
	}
	if c.Subject != "alice" || !slices.Equal(c.Groups, []string{"ops"}) || c.Issuer != "occamy" || c.IdP != IdPLocal {
		t.Errorf("claims %+v", c)
	}
	if got := c.ExpiresAt.Sub(epoch); got != 15*time.Minute {
		t.Errorf("lifetime %v, want 15m", got)
	}

	tok = f.must(f.signer.AccessFrom(IdPOIDC, "carol", nil, time.Minute))
	if c, err := f.verifier.VerifyAccess(tok); err != nil || c.Subject != "carol" || c.IdP != IdPOIDC {
		t.Errorf("claims %+v, %v", c, err)
	}
	// A trusted issuer's access token need not name an idp.
	tok = forge(t, jwt.SigningMethodEdDSA, f.gateway.key, claims(KindAccess, "gateway", time.Minute))
	if c, err := f.verifier.VerifyAccess(tok); err != nil || c.IdP != "" {
		t.Errorf("claims %+v, %v", c, err)
	}
}

func TestSession(t *testing.T) {
	f := newFixture(t)
	tok := f.must(f.signer.Session("s1", RoleReadOnly, "alice", time.Hour))
	c, err := f.verifier.VerifySession(tok)
	if err != nil {
		t.Fatal(err)
	}
	if c.Session != "s1" || c.Role != RoleReadOnly || c.Subject != "alice" {
		t.Errorf("claims %+v", c)
	}
}

func TestTrustedIssuer(t *testing.T) {
	f := newFixture(t)
	tok := f.must(f.gateway.Session("s1", RoleOwner, "bob", time.Minute))
	c, err := f.verifier.VerifySession(tok)
	if err != nil {
		t.Fatal(err)
	}
	if c.Issuer != "gateway" || c.Subject != "bob" {
		t.Errorf("claims %+v", c)
	}
}

// A session token of occamyd's for a trusted issuer's user names the
// issuer, and one for its own user where the user signed in; a trusted
// issuer can do neither, and an access token names no user issuer.
func TestSessionFor(t *testing.T) {
	f := newFixture(t)
	c, err := f.verifier.VerifySession(f.must(f.signer.SessionFor("s1", RoleOwner, User{Issuer: "gateway", Subject: "bob"}, time.Minute)))
	if err != nil {
		t.Fatal(err)
	}
	if c.Issuer != "occamy" || c.UserIssuer != "gateway" || c.Subject != "bob" || c.IdP != "" {
		t.Errorf("claims %+v", c)
	}
	c, err = f.verifier.VerifySession(f.must(f.signer.SessionFor("s1", RoleOwner, User{Issuer: "occamy", IdP: IdPOIDC, Subject: "bob"}, time.Minute)))
	if err != nil || c.UserIssuer != "" || c.IdP != IdPOIDC || c.Subject != "bob" {
		t.Errorf("own user: %+v %v", c, err)
	}
	if _, err := f.verifier.VerifySession(f.must(f.gateway.SessionFor("s1", RoleOwner, User{Issuer: "elsewhere", Subject: "bob"}, time.Minute))); !errors.Is(err, ErrClaims) {
		t.Errorf("a trusted issuer naming another's user: %v, want ErrClaims", err)
	}
	if _, err := f.verifier.VerifySession(f.must(f.gateway.SessionFor("s1", RoleOwner, User{IdP: IdPLocal, Subject: "bob"}, time.Minute))); !errors.Is(err, ErrClaims) {
		t.Errorf("a trusted issuer's session token with an idp: %v, want ErrClaims", err)
	}
	for name, u := range map[string]User{
		"a user issuer without a subject": {Issuer: "gateway"},
		"an idp without a subject":        {IdP: IdPLocal},
		"a user issuer and an idp":        {Issuer: "gateway", IdP: IdPLocal, Subject: "bob"},
	} {
		if tok, err := f.signer.SessionFor("s1", RoleViewer, u, time.Minute); err == nil {
			t.Errorf("signed %s: %s", name, tok)
		}
	}
	access := claims(KindAccess, "occamy", time.Minute)
	access.UserIssuer = "gateway"
	if _, err := f.verifier.VerifyAccess(forge(t, jwt.SigningMethodEdDSA, f.signer.key, access)); !errors.Is(err, ErrClaims) {
		t.Errorf("an access token naming a user issuer: %v, want ErrClaims", err)
	}
}

func TestSignRejects(t *testing.T) {
	f := newFixture(t)
	for name, sign := range map[string]func() (string, error){
		"no subject":   func() (string, error) { return f.signer.Access("", nil, time.Minute) },
		"no session":   func() (string, error) { return f.signer.Session("", RoleViewer, "alice", time.Minute) },
		"unknown role": func() (string, error) { return f.signer.Session("s1", "admin", "alice", time.Minute) },
		// Every session token names its user: a share is the registry's.
		"no subject in a session token": func() (string, error) { return f.signer.Session("s1", RoleViewer, "", time.Minute) },
		"no lifetime":                   func() (string, error) { return f.signer.Access("alice", nil, 0) },
	} {
		if tok, err := sign(); err == nil {
			t.Errorf("%s: signed %s", name, tok)
		}
	}
}

// forge signs claims with an arbitrary method and key, as an attacker
// would.
func forge(t *testing.T, m jwt.SigningMethod, key any, c *Claims) string {
	t.Helper()
	tok, err := jwt.NewWithClaims(m, c).SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func claims(kind Kind, iss string, lifetime time.Duration) *Claims {
	return &Claims{
		Kind: kind,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    iss,
			Subject:   "alice",
			Audience:  jwt.ClaimStrings{"occamy"},
			IssuedAt:  jwt.NewNumericDate(epoch),
			ExpiresAt: jwt.NewNumericDate(epoch.Add(lifetime)),
		},
	}
}

func TestVerifyRejects(t *testing.T) {
	f := newFixture(t)
	valid := f.must(f.signer.Access("alice", nil, time.Minute))
	parts := strings.Split(valid, ".")

	noIat := claims(KindAccess, "occamy", time.Minute)
	noIat.IssuedAt = nil
	noExp := claims(KindAccess, "occamy", time.Minute)
	noExp.ExpiresAt = nil
	otherAud := claims(KindAccess, "occamy", time.Minute)
	otherAud.Audience = jwt.ClaimStrings{"elsewhere"}
	noKind := claims("", "occamy", time.Minute)
	sessionWithGroups := claims(KindSession, "occamy", time.Minute)
	sessionWithGroups.Session, sessionWithGroups.Role, sessionWithGroups.Groups = "s1", RoleViewer, []string{"ops"}
	sessionWithIdP := claims(KindSession, "occamy", time.Minute)
	sessionWithIdP.Session, sessionWithIdP.Role, sessionWithIdP.IdP = "s1", RoleViewer, IdPOIDC
	sessionWithoutSubject := claims(KindSession, "occamy", time.Minute)
	sessionWithoutSubject.Session, sessionWithoutSubject.Role, sessionWithoutSubject.Subject = "s1", RoleViewer, ""

	// The payload of a different token, under the valid token's
	// signature.
	other := strings.Split(f.must(f.signer.Access("mallory", nil, time.Minute)), ".")
	tampered := parts[0] + "." + other[1] + "." + parts[2]

	gwPriv := f.gateway.key
	for name, c := range map[string]struct {
		token string
		want  error
	}{
		"none": {
			forge(t, jwt.SigningMethodNone, jwt.UnsafeAllowNoneSignatureType, claims(KindAccess, "occamy", time.Minute)),
			jwt.ErrTokenSignatureInvalid,
		},
		// The classic confusion: an HMAC token keyed with the bytes of
		// the public key, which a verifier that trusts the header would
		// accept.
		"hmac with public key": {
			forge(t, jwt.SigningMethodHS256, []byte(f.pub), claims(KindAccess, "occamy", time.Minute)),
			jwt.ErrTokenSignatureInvalid,
		},
		"unknown issuer": {
			forge(t, jwt.SigningMethodEdDSA, gwPriv, claims(KindAccess, "stranger", time.Minute)),
			ErrUnknownIssuer,
		},
		"trusted issuer, wrong key": {
			f.must(f.stranger.Access("alice", nil, time.Minute)),
			jwt.ErrTokenSignatureInvalid,
		},
		"own issuer, gateway key": {
			forge(t, jwt.SigningMethodEdDSA, gwPriv, claims(KindAccess, "occamy", time.Minute)),
			jwt.ErrTokenSignatureInvalid,
		},
		"wrong audience": {
			forge(t, jwt.SigningMethodEdDSA, gwPriv, setIss(otherAud, "gateway")),
			jwt.ErrTokenInvalidAudience,
		},
		"no expiry": {
			forge(t, jwt.SigningMethodEdDSA, gwPriv, setIss(noExp, "gateway")),
			jwt.ErrTokenRequiredClaimMissing,
		},
		"no issued at": {
			forge(t, jwt.SigningMethodEdDSA, gwPriv, setIss(noIat, "gateway")),
			ErrLifetime,
		},
		"too long": {
			forge(t, jwt.SigningMethodEdDSA, gwPriv, claims(KindAccess, "gateway", DefaultMaxLifetime+time.Second)),
			ErrLifetime,
		},
		"no kind": {
			forge(t, jwt.SigningMethodEdDSA, gwPriv, setIss(noKind, "gateway")),
			ErrKind,
		},
		"session token as access": {
			f.must(f.signer.Session("s1", RoleViewer, "alice", time.Minute)),
			ErrKind,
		},
		"session with groups": {
			forge(t, jwt.SigningMethodEdDSA, gwPriv, setIss(sessionWithGroups, "gateway")),
			ErrClaims,
		},
		"session with an idp": {
			forge(t, jwt.SigningMethodEdDSA, gwPriv, setIss(sessionWithIdP, "gateway")),
			ErrClaims,
		},
		// A trusted issuer's session token names its user, as occamyd's
		// do; one for nobody could join any session.
		"session without a subject": {
			forge(t, jwt.SigningMethodEdDSA, gwPriv, setIss(sessionWithoutSubject, "gateway")),
			ErrClaims,
		},
		"tampered": {tampered, jwt.ErrTokenSignatureInvalid},
		"garbage":  {"not.a.token", jwt.ErrTokenMalformed},
		"padded signature": {
			valid + strings.Repeat("=", 2),
			jwt.ErrTokenMalformed,
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := f.verifier.VerifyAccess(c.token)
			if !errors.Is(err, c.want) {
				t.Errorf("err %v, want %v", err, c.want)
			}
		})
	}
}

func setIss(c *Claims, iss string) *Claims {
	c.Issuer = iss
	return c
}

func TestVerifyAccessAsSession(t *testing.T) {
	f := newFixture(t)
	tok := f.must(f.signer.Access("alice", nil, time.Minute))
	if _, err := f.verifier.VerifySession(tok); !errors.Is(err, ErrKind) {
		t.Errorf("err %v, want ErrKind", err)
	}
}

func TestExpiry(t *testing.T) {
	f := newFixture(t)
	tok := f.must(f.signer.Access("alice", nil, time.Minute))
	*f.clock = epoch.Add(time.Minute + DefaultLeeway - time.Second)
	if _, err := f.verifier.VerifyAccess(tok); err != nil {
		t.Errorf("within leeway: %v", err)
	}
	*f.clock = epoch.Add(time.Minute + DefaultLeeway + time.Second)
	if _, err := f.verifier.VerifyAccess(tok); !errors.Is(err, jwt.ErrTokenExpired) {
		t.Errorf("after leeway: err %v, want expired", err)
	}
}

func TestIssuedInFuture(t *testing.T) {
	f := newFixture(t)
	*f.clock = epoch.Add(time.Hour)
	tok := f.must(f.gateway.Access("alice", nil, time.Minute))
	*f.clock = epoch
	if _, err := f.verifier.VerifyAccess(tok); !errors.Is(err, jwt.ErrTokenUsedBeforeIssued) {
		t.Errorf("err %v, want used before issued", err)
	}
}

// The header names EdDSA, but the signature segment is not one.
func TestShortSignature(t *testing.T) {
	f := newFixture(t)
	parts := strings.Split(f.must(f.signer.Access("alice", nil, time.Minute)), ".")
	short := parts[0] + "." + parts[1] + "." + base64.RawURLEncoding.EncodeToString([]byte("x"))
	if _, err := f.verifier.VerifyAccess(short); !errors.Is(err, jwt.ErrTokenSignatureInvalid) {
		t.Errorf("err %v, want invalid signature", err)
	}
}
