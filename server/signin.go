// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package server

import (
	"crypto/rand"
	"fmt"
	"log"
	"net/http"
	"time"

	"golang.org/x/crypto/bcrypt"

	"changkun.de/x/occamy/internal/auth"
	"changkun.de/x/occamy/internal/config"
)

// staticUsers signs in the static users of a v1 file.
type staticUsers struct {
	users  map[string]staticUser
	signer *auth.Signer
	ttl    time.Duration
	// dummy is compared with the password of a user who does not exist,
	// so that a sign-in takes as long whether or not the user exists. It
	// has the highest cost among the users' hashes, since bcrypt's time
	// is set by its cost.
	dummy   []byte
	compare func(hash, password []byte) error // bcrypt.CompareHashAndPassword
}

type staticUser struct {
	hash   []byte
	groups []string
}

// newStaticUsers prepares the users' sign-in. Making the dummy hash costs
// one bcrypt hash at the highest cost.
func newStaticUsers(users []config.User, signer *auth.Signer, ttl time.Duration) (*staticUsers, error) {
	s := &staticUsers{
		users:   make(map[string]staticUser, len(users)),
		signer:  signer,
		ttl:     ttl,
		compare: bcrypt.CompareHashAndPassword,
	}
	cost, costs := 0, map[int]bool{}
	for _, u := range users {
		c, err := bcrypt.Cost([]byte(u.PasswordHash))
		if err != nil {
			return nil, fmt.Errorf("auth.users: %s: %w", u.Name, err)
		}
		cost = max(cost, c)
		costs[c] = true
		s.users[u.Name] = staticUser{hash: []byte(u.PasswordHash), groups: u.Groups}
	}
	if cost == 0 {
		cost = config.MinBcryptCost // no static users
	}
	if len(costs) > 1 {
		log.Printf("warning: the static users' password hashes have different bcrypt costs, so the time a sign-in takes tells which users exist; give them all cost %d", cost)
	}
	dummy, err := bcrypt.GenerateFromPassword([]byte(rand.Text()), cost)
	if err != nil {
		return nil, err
	}
	s.dummy = dummy
	return s, nil
}

// tokenRequest is the body of POST /api/v1/auth/token.
type tokenRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// tokenResponse is the reply of POST /api/v1/auth/token, in the shape of
// an OAuth 2.0 token response (RFC 6749, section 5.1).
type tokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"` // always Bearer
	ExpiresIn   int64  `json:"expires_in"` // seconds
}

// token implements POST /api/v1/auth/token for static users: a user name
// and password in, an access token out. A user who does not exist gets
// the same 401 as a wrong password, after the same bcrypt comparison.
func (s *staticUsers) token(w http.ResponseWriter, r *http.Request) {
	var req tokenRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Username == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, codeBadRequest, "username and password are required")
		return
	}
	u, known := s.users[req.Username]
	hash := u.hash
	if !known {
		hash = s.dummy
	}
	// An unknown user costs the same comparison as a known one.
	err := s.compare(hash, []byte(req.Password))
	if !known || err != nil {
		// The name is not logged: people type passwords into it.
		// #nosec G706 -- the remote address comes from net/http, not the client's data.
		log.Printf("sign-in failed from %s", r.RemoteAddr)
		w.Header().Set("WWW-Authenticate", bearerRealm)
		writeError(w, http.StatusUnauthorized, codeUnauthorized, "wrong user name or password")
		return
	}
	token, err := s.signer.Access(req.Username, u.groups, s.ttl)
	if err != nil {
		log.Printf("signing an access token for %q: %v", req.Username, err)
		writeError(w, http.StatusInternalServerError, codeInternal, "internal server error")
		return
	}
	log.Printf("user %q signed in", req.Username)
	w.Header().Set("Cache-Control", "no-store")
	respond(w, http.StatusOK, tokenResponse{
		AccessToken: token,
		TokenType:   "Bearer",
		ExpiresIn:   int64(s.ttl / time.Second),
	})
}
