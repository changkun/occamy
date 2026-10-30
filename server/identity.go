// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package server

import (
	"errors"

	"changkun.de/x/occamy/internal/auth"
	"changkun.de/x/occamy/internal/config"
)

// principal is the user a token speaks for: a subject of an issuer. A
// session belongs to a principal, so that the same subject from two
// issuers is two users, and a trusted issuer acts for its own users only:
// not for occamyd's, nor for another issuer's. The allow lists of
// connections still name users by subject alone. Access tokens do not
// say yet where one of occamyd's own users signed in; once OIDC sign-in
// names it in an idp claim, it becomes part of the key here and in
// session tokens.
type principal struct {
	issuer  string // auth.Issuer for occamyd's own users
	subject string
}

// String names the principal in the log: by subject for occamyd's own
// users, and with the issuer for a trusted issuer's.
func (p principal) String() string {
	if p.issuer == auth.Issuer {
		return p.subject
	}
	return p.issuer + ":" + p.subject
}

// identities maps the claims of verified tokens to principals.
type identities struct {
	static map[string]bool // the static users, by name
	linked map[string]bool // the trusted issuers with link_static_users
}

func newIdentities(f *config.File) identities {
	ids := identities{static: map[string]bool{}, linked: map[string]bool{}}
	for _, u := range f.Auth.Users {
		ids.static[u.Name] = true
	}
	for _, ti := range f.Auth.TrustedIssuers {
		ids.linked[ti.Issuer] = ti.LinkStaticUsers
	}
	return ids
}

// errStaticUser refuses a token of a trusted issuer whose subject names a
// static user, unless the issuer has link_static_users.
var errStaticUser = errors.New("the token's subject is a static user's name, which its issuer may not use")

// of returns the principal that verified claims speak for. occamyd's own
// tokens speak for its users, or, in a session token, for the trusted
// issuer's user it names. A trusted issuer's token speaks for the
// issuer's user of its subject, or for the static user of that name if
// the issuer is linked to static users; otherwise such a subject is
// refused.
func (ids identities) of(c *auth.Claims) (principal, error) {
	if c.Issuer == auth.Issuer {
		if c.UserIssuer != "" {
			return principal{issuer: c.UserIssuer, subject: c.Subject}, nil
		}
		return principal{issuer: auth.Issuer, subject: c.Subject}, nil
	}
	if c.Subject != "" && ids.static[c.Subject] {
		if !ids.linked[c.Issuer] {
			return principal{}, errStaticUser
		}
		return principal{issuer: auth.Issuer, subject: c.Subject}, nil
	}
	return principal{issuer: c.Issuer, subject: c.Subject}, nil
}
