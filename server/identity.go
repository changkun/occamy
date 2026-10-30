// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package server

import (
	"errors"
	"slices"

	"changkun.de/x/occamy/internal/auth"
	"changkun.de/x/occamy/internal/config"
)

// principal is the user a token speaks for: a subject of an issuer, and,
// for one of occamyd's own users, of the source where it signed in. A
// session belongs to a principal, so that the same subject from two
// issuers, or from OIDC and the static users, is two users, and a
// trusted issuer acts for its own users only: not for occamyd's, nor
// for another issuer's. The allow lists of connections still name users
// by subject alone.
type principal struct {
	issuer  string // auth.Issuer for occamyd's own users
	idp     string // auth.IdPLocal or auth.IdPOIDC for occamyd's own users
	subject string
}

// String names the principal in the log: by subject for a static user,
// with the source for an OIDC user, and with the issuer for a trusted
// issuer's.
func (p principal) String() string {
	switch {
	case p.issuer != auth.Issuer:
		return p.issuer + ":" + p.subject
	case p.idp == auth.IdPLocal:
		return p.subject
	}
	return p.idp + ":" + p.subject
}

// identities maps the claims of verified tokens to principals.
type identities struct {
	static     map[string]bool // the static users, by name
	names      []string        // the same names, for config.SameName
	linked     map[string]bool // the trusted issuers with link_static_users
	oidcLinked bool            // auth.oidc.link_static_users
}

func newIdentities(f *config.File) identities {
	ids := identities{static: map[string]bool{}, linked: map[string]bool{}}
	for _, u := range f.Auth.Users {
		ids.static[u.Name] = true
		ids.names = append(ids.names, u.Name)
	}
	for _, ti := range f.Auth.TrustedIssuers {
		ids.linked[ti.Issuer] = ti.LinkStaticUsers
	}
	ids.oidcLinked = f.Auth.OIDC != nil && f.Auth.OIDC.LinkStaticUsers
	return ids
}

// errStaticUser refuses a token whose subject names a static user while
// its source is not linked to the static users: a trusted issuer without
// link_static_users, or an OIDC sign-in from when auth.oidc's
// link_static_users was on.
var errStaticUser = errors.New("the token's subject is a static user's name, which its source may not use")

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
		return ids.own(c.IdP, c.Subject)
	}
	if c.Subject != "" && ids.static[c.Subject] {
		if !ids.linked[c.Issuer] {
			return principal{}, errStaticUser
		}
		return principal{issuer: auth.Issuer, idp: auth.IdPLocal, subject: c.Subject}, nil
	}
	return principal{issuer: c.Issuer, subject: c.Subject}, nil
}

// own returns the principal of one of occamyd's own users, who signed in
// at idp. A static user's name is the static user's whoever signed in
// with it: OIDC sign-in takes such a name only when
// auth.oidc.link_static_users says the two are one person, so the OIDC
// user owns the static user's sessions, while its tokens keep idp oidc
// for the log. A token that names a static user from OIDC, by
// config.SameName as sign-in compares, is refused once the two are no
// longer linked. A token without idp predates OIDC sign-in, so it is a
// static user's.
func (ids identities) own(idp, subject string) (principal, error) {
	if subject == "" {
		return principal{issuer: auth.Issuer}, nil // a share's session token
	}
	if idp == "" {
		idp = auth.IdPLocal
	}
	if idp != auth.IdPLocal && !ids.oidcLinked && slices.ContainsFunc(ids.names, func(n string) bool { return config.SameName(subject, n) }) {
		return principal{}, errStaticUser
	}
	if ids.static[subject] {
		idp = auth.IdPLocal
	}
	return principal{issuer: auth.Issuer, idp: idp, subject: subject}, nil
}

// sessionUser is the user that a session token for p names. For one of
// occamyd's own users it keeps the idp that claims, the caller's access
// token, say, such as oidc for a linked OIDC user whose principal is
// the static user's, so that the token tells where the user signed in.
func sessionUser(p principal, c *auth.Claims) auth.User {
	u := auth.User{Issuer: p.issuer, IdP: p.idp, Subject: p.subject}
	if c != nil && c.Issuer == auth.Issuer && c.UserIssuer == "" && c.IdP != "" {
		u.IdP = c.IdP
	}
	return u
}
