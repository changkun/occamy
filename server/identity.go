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
	static     map[string]bool            // the static users, by name
	names      []string                   // the same names, for config.SameName
	linked     map[string]bool            // the trusted issuers with link_static_users
	groups     map[string]map[string]bool // each trusted issuer's allowed_groups
	oidcLinked bool                       // auth.oidc.link_static_users
}

func newIdentities(f *config.File) identities {
	ids := identities{static: map[string]bool{}, linked: map[string]bool{}, groups: map[string]map[string]bool{}}
	for _, u := range f.Auth.Users {
		ids.static[u.Name] = true
		ids.names = append(ids.names, u.Name)
	}
	for _, ti := range f.Auth.TrustedIssuers {
		ids.linked[ti.Issuer] = ti.LinkStaticUsers
		ids.groups[ti.Issuer] = map[string]bool{}
		for _, g := range ti.AllowedGroups {
			ids.groups[ti.Issuer][g] = true
		}
	}
	ids.oidcLinked = f.Auth.OIDC != nil && f.Auth.OIDC.LinkStaticUsers
	return ids
}

// errStaticUser refuses a token whose subject names a static user while
// its source is not linked to the static users: a trusted issuer without
// link_static_users, or an OIDC sign-in from when auth.oidc's
// link_static_users was on.
var errStaticUser = errors.New("the token's subject is a static user's name, which its source may not use")

// errSubject refuses a token whose subject is not a usable name, by
// config.UsableName, as OIDC sign-in refuses one.
var errSubject = errors.New("the token's subject is not a usable name")

// of returns the principal that verified claims speak for. Every token
// names a user whose name config.UsableName passes. occamyd's own tokens
// speak for its users, or, in a session token, for the trusted issuer's
// user it names. A trusted issuer's token speaks for the issuer's user
// of its subject, or, if the issuer is linked to static users, for the
// static user of that name. Unless the issuer is linked, its subject
// must not be a static user's name by config.SameName, as OIDC sign-in
// checks a subject, so that the two paths refuse the same names.
//
// of also drops from c the groups that a trusted issuer may not claim,
// those its allowed_groups does not list, so that the allow lists read
// only the groups the file lets the issuer grant. A linked issuer acting
// as a static user claims the groups of its token too, not the static
// user's.
func (ids identities) of(c *auth.Claims) (principal, error) {
	p, err := ids.principal(c)
	if err == nil && c.Issuer != auth.Issuer {
		c.Groups = slices.DeleteFunc(c.Groups, func(g string) bool { return !ids.groups[c.Issuer][g] })
	}
	return p, err
}

// principal is of without the groups.
func (ids identities) principal(c *auth.Claims) (principal, error) {
	if !validSubject(c.Subject) {
		return principal{}, errSubject
	}
	if c.Issuer == auth.Issuer {
		if c.UserIssuer != "" {
			return principal{issuer: c.UserIssuer, subject: c.Subject}, nil
		}
		return ids.own(c.IdP, c.Subject)
	}
	if ids.takesStaticName(c.Subject) {
		if !ids.linked[c.Issuer] {
			return principal{}, errStaticUser
		}
		if ids.static[c.Subject] {
			return principal{issuer: auth.Issuer, idp: auth.IdPLocal, subject: c.Subject}, nil
		}
	}
	return principal{issuer: c.Issuer, subject: c.Subject}, nil
}

// grants reports whether session token c may join a session of owner,
// in any role. occamyd signs session tokens only for a session's owner.
// A trusted issuer's token joins only a session of one of the issuer's
// own users, or, if the issuer is linked to the static users, of a
// static user: an issuer has no say over another's users' sessions,
// whatever role its token claims for whichever subject.
func (ids identities) grants(c *auth.Claims, owner principal) bool {
	switch {
	case c.Issuer == auth.Issuer, owner.issuer == c.Issuer:
		return true
	}
	return ids.linked[c.Issuer] && owner.issuer == auth.Issuer && owner.idp == auth.IdPLocal
}

// takesStaticName reports whether subject is a static user's name by
// config.SameName, as OIDC sign-in compares it.
func (ids identities) takesStaticName(subject string) bool {
	return slices.ContainsFunc(ids.names, func(n string) bool { return config.SameName(subject, n) })
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
	if idp == "" {
		idp = auth.IdPLocal
	}
	if idp != auth.IdPLocal && !ids.oidcLinked && ids.takesStaticName(subject) {
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
