// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package session

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Errors from a Registry.
var (
	// ErrNotFound reports an unknown or ended session, or an unknown,
	// expired or revoked share token.
	ErrNotFound = errors.New("session: not found")
	// ErrShareRole reports a share for a role other than viewer or
	// read-only.
	ErrShareRole = errors.New("session: shares are for viewers or read-only viewers")
)

// Registry holds the running sessions by ID and the share tokens that let
// others join them. A session leaves the registry when it ends, and its
// share tokens die with it.
type Registry struct {
	ctx context.Context
	now func() time.Time

	mu       sync.Mutex
	sessions map[string]*Session
	shares   map[[sha256.Size]byte]share // by the token's hash
}

type share struct {
	session string
	role    Role
	expires time.Time
}

// NewRegistry returns an empty registry. Its sessions end when ctx is
// done.
func NewRegistry(ctx context.Context) *Registry {
	return &Registry{
		ctx:      ctx,
		now:      time.Now,
		sessions: map[string]*Session{},
		shares:   map[[sha256.Size]byte]share{},
	}
}

// Open starts a session and returns its ID, the session and the owner's
// viewer.
func (r *Registry) Open(cfg Config) (id string, s *Session, owner *Viewer, err error) {
	id = randomID(16)
	s, owner, err = Start(r.ctx, cfg)
	if err != nil {
		return "", nil, nil, err
	}
	r.mu.Lock()
	r.sessions[id] = s
	r.mu.Unlock()
	go func() {
		<-s.Done()
		r.remove(id)
	}()
	return id, s, owner, nil
}

// Get returns the running session with the given ID.
func (r *Registry) Get(id string) (*Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sessions[id]
	if !ok {
		return nil, ErrNotFound
	}
	return s, nil
}

// Share mints a token that lets its holder join session id with role
// until ttl passes or the session ends.
func (r *Registry) Share(id string, role Role, ttl time.Duration) (token string, err error) {
	if role != RoleViewer && role != RoleReadOnly {
		return "", ErrShareRole
	}
	if ttl <= 0 {
		return "", fmt.Errorf("session: share lifetime %v", ttl)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.sessions[id]; !ok {
		return "", ErrNotFound
	}
	// Expired shares go here, so that they do not pile up in a session
	// that keeps minting them.
	now := r.now()
	for k, sh := range r.shares {
		if !now.Before(sh.expires) {
			delete(r.shares, k)
		}
	}
	token = randomID(32)
	r.shares[sha256.Sum256([]byte(token))] = share{session: id, role: role, expires: now.Add(ttl)}
	return token, nil
}

// JoinShared joins session id with the role of a share token for it. A
// token for another session is not found, so that a share opens only the
// session it was minted for, and the other session's viewers hear of no
// join.
func (r *Registry) JoinShared(id, token string) (*Viewer, error) {
	key := sha256.Sum256([]byte(token))
	r.mu.Lock()
	sh, ok := r.shares[key]
	if ok && !r.now().Before(sh.expires) {
		delete(r.shares, key)
		ok = false
	}
	s := r.sessions[sh.session]
	r.mu.Unlock()
	if !ok || s == nil || sh.session != id {
		return nil, ErrNotFound
	}
	v, err := s.Join(sh.role)
	if errors.Is(err, ErrEnded) {
		return nil, ErrNotFound
	}
	return v, err
}

// Len is the number of running sessions.
func (r *Registry) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.sessions)
}

// remove drops an ended session and its share tokens.
func (r *Registry) remove(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.sessions, id)
	for k, sh := range r.shares {
		if sh.session == id {
			delete(r.shares, k)
		}
	}
}

// randomID returns n random bytes in unpadded base64url.
func randomID(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b) // crypto/rand.Read never fails
	return base64.RawURLEncoding.EncodeToString(b)
}
