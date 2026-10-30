// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// Package auth issues and verifies occamyd's tokens
// (specs/008-native-architecture/api-and-server-stack.md, decision 1).
//
// Tokens are JWTs signed with Ed25519. An access token names a user and
// the groups the user belongs to. A session token names a session and the
// role its holder joins with. occamyd signs both with its own key, and
// accepts tokens from the issuers it is configured to trust, each with its
// own public key. No other algorithm is accepted, whatever a token's
// header says.
package auth

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Kind tells an access token from a session token, so that one is never
// accepted where the other is expected.
type Kind string

// Kinds of token.
const (
	KindAccess  Kind = "access"
	KindSession Kind = "session"
)

// Roles a session token can carry, as the session core names them.
const (
	RoleOwner    = "owner"
	RoleViewer   = "viewer"
	RoleReadOnly = "read-only"
)

// occamyd's own tokens name Issuer in iss and Audience in aud. A trusted
// issuer may not be named Issuer, since the issuer picks the key, and its
// tokens must name Audience too.
const (
	Issuer   = "occamy"
	Audience = "occamy"
)

// Defaults for a Verifier.
const (
	// DefaultLeeway is the clock skew allowed between issuers and occamyd.
	DefaultLeeway = 30 * time.Second
	// DefaultMaxLifetime is the longest a token may live from its issue
	// to its expiry. It bounds what a trusted issuer can mint.
	DefaultMaxLifetime = 12 * time.Hour
)

// Errors from Verify, besides those of the jwt package it wraps.
var (
	// ErrUnknownIssuer reports a token from an issuer that is not trusted.
	ErrUnknownIssuer = errors.New("auth: unknown issuer")
	// ErrKind reports a token of the wrong kind, or of none.
	ErrKind = errors.New("auth: wrong kind of token")
	// ErrLifetime reports a token that lives longer than allowed.
	ErrLifetime = errors.New("auth: token lifetime too long")
	// ErrClaims reports a token whose claims do not fit its kind.
	ErrClaims = errors.New("auth: invalid claims")
)

// Claims are what a token says. Access tokens carry Groups; session tokens
// carry Session and Role, and Subject if they were issued to a user.
type Claims struct {
	Kind    Kind     `json:"kind"`
	Groups  []string `json:"groups,omitempty"`
	Session string   `json:"sid,omitempty"`
	Role    string   `json:"role,omitempty"`
	jwt.RegisteredClaims
}

// check reports whether the claims fit their kind.
func (c *Claims) check() error {
	switch c.Kind {
	case KindAccess:
		if c.Subject == "" || c.Session != "" || c.Role != "" {
			return ErrClaims
		}
	case KindSession:
		if c.Session == "" || c.Groups != nil {
			return ErrClaims
		}
		switch c.Role {
		case RoleOwner, RoleViewer, RoleReadOnly:
		default:
			return ErrClaims
		}
	default:
		return ErrKind
	}
	return nil
}

// Signer issues occamyd's own tokens.
type Signer struct {
	key      ed25519.PrivateKey
	issuer   string
	audience string
	now      func() time.Time
}

// NewSigner returns a signer that issues tokens from issuer for audience,
// signed with key.
func NewSigner(key ed25519.PrivateKey, issuer, audience string) *Signer {
	return &Signer{key: key, issuer: issuer, audience: audience, now: time.Now}
}

// Access issues an access token for a user and the groups the user belongs
// to, valid for ttl.
func (s *Signer) Access(subject string, groups []string, ttl time.Duration) (string, error) {
	return s.sign(&Claims{
		Kind:             KindAccess,
		Groups:           groups,
		RegisteredClaims: jwt.RegisteredClaims{Subject: subject},
	}, ttl)
}

// Session issues a session token that joins session with role, valid for
// ttl. subject names the user it is issued to, or is empty for a token
// that anyone holding it may use, such as a share.
func (s *Signer) Session(session, role, subject string, ttl time.Duration) (string, error) {
	return s.sign(&Claims{
		Kind:             KindSession,
		Session:          session,
		Role:             role,
		RegisteredClaims: jwt.RegisteredClaims{Subject: subject},
	}, ttl)
}

func (s *Signer) sign(c *Claims, ttl time.Duration) (string, error) {
	if ttl <= 0 {
		return "", fmt.Errorf("auth: token lifetime %v", ttl)
	}
	if err := c.check(); err != nil {
		return "", err
	}
	now := s.now().Truncate(time.Second)
	c.Issuer = s.issuer
	c.Audience = jwt.ClaimStrings{s.audience}
	c.IssuedAt = jwt.NewNumericDate(now)
	c.ExpiresAt = jwt.NewNumericDate(now.Add(ttl))
	return jwt.NewWithClaims(jwt.SigningMethodEdDSA, c).SignedString(s.key)
}

// Verifier checks tokens from occamyd and from trusted issuers.
type Verifier struct {
	audience    string
	keys        map[string]ed25519.PublicKey
	leeway      time.Duration
	maxLifetime time.Duration
	now         func() time.Time
}

// NewVerifier returns a verifier for tokens meant for audience, from the
// issuers in keys, each with its public key. occamyd's own issuer and key
// belong in keys too.
func NewVerifier(audience string, keys map[string]ed25519.PublicKey) *Verifier {
	return &Verifier{
		audience:    audience,
		keys:        keys,
		leeway:      DefaultLeeway,
		maxLifetime: DefaultMaxLifetime,
		now:         time.Now,
	}
}

// VerifyAccess checks an access token and returns its claims.
func (v *Verifier) VerifyAccess(token string) (*Claims, error) {
	return v.verify(token, KindAccess)
}

// VerifySession checks a session token and returns its claims.
func (v *Verifier) VerifySession(token string) (*Claims, error) {
	return v.verify(token, KindSession)
}

func (v *Verifier) verify(token string, kind Kind) (*Claims, error) {
	c := &Claims{}
	_, err := jwt.ParseWithClaims(token, c, v.key,
		jwt.WithValidMethods([]string{jwt.SigningMethodEdDSA.Alg()}),
		jwt.WithAudience(v.audience),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
		jwt.WithLeeway(v.leeway),
		jwt.WithTimeFunc(v.now),
		jwt.WithStrictDecoding(),
	)
	if err != nil {
		return nil, err
	}
	if c.IssuedAt == nil || c.ExpiresAt.Sub(c.IssuedAt.Time) > v.maxLifetime {
		return nil, ErrLifetime
	}
	if err := c.check(); err != nil {
		return nil, err
	}
	if c.Kind != kind {
		return nil, ErrKind
	}
	return c, nil
}

// key returns the public key of the token's issuer. The token's signature
// is checked with it afterwards; until then nothing in the token is
// trusted, the issuer included, which is why an unknown issuer fails here.
func (v *Verifier) key(t *jwt.Token) (any, error) {
	c, ok := t.Claims.(*Claims)
	if !ok {
		return nil, ErrClaims
	}
	key, ok := v.keys[c.Issuer]
	if !ok {
		return nil, ErrUnknownIssuer
	}
	return key, nil
}
