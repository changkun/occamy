// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"sync"
	"time"

	"changkun.de/x/occamy/internal/auth"
	"changkun.de/x/occamy/internal/backend/ssh"
	"changkun.de/x/occamy/internal/config"
	"changkun.de/x/occamy/internal/session"
)

// API v1 error codes of the session routes, beside those in api.go.
const (
	codeForbidden     = "forbidden"      // the caller may not do this
	codeLimitExceeded = "limit_exceeded" // the caller holds as much as limits allows
)

// errSessionLimit refuses a session to a user who owns as many as
// limits.sessions_per_user allows.
var errSessionLimit = errors.New("too many sessions open")

// errSessionsFull refuses a session when occamyd runs as many as
// limits.sessions allows, whoever owns them.
var errSessionsFull = errors.New("occamyd runs as many sessions as it takes")

// Lifetimes of what the session routes hand out.
const (
	// streamTokenLifetime is how long the stream token that
	// POST /api/v1/sessions returns lasts: long enough to open the
	// stream and to come back after a short drop. Later the owner joins
	// with an access token.
	streamTokenLifetime = 5 * time.Minute
	// defaultShareLifetime is a share's lifetime when the request names
	// none, and maxShareLifetime the longest a share may have, as long as
	// any token occamyd accepts.
	defaultShareLifetime = time.Hour
	maxShareLifetime     = auth.DefaultMaxLifetime
)

// sessionInfo is what API v1 keeps about a running session beside the
// registry: whose it is and what it connects to.
type sessionInfo struct {
	id         string
	s          *session.Session
	owner      principal
	connection string // the registry's ID, empty for an ad-hoc target
	protocol   string
	backend    string
	opened     time.Time

	// gone is closed once the server has forgotten the session and
	// released its count toward the limits, after its end.
	gone chan struct{}

	mu          sync.Mutex
	ownerStream *ownerStream // the stream the owner is connected through, if any
}

// registerSessions registers the routes of connections and sessions,
// each for callers with an access token.
func (s *server) registerSessions(mux *routeMux) {
	authed := authenticate(s.verifier, s.ids)
	mux.Handle("GET /api/v1/connections", authed(http.HandlerFunc(s.listConnections)))
	mux.Handle("POST /api/v1/sessions", authed(http.HandlerFunc(s.openSession)))
	mux.Handle("GET /api/v1/sessions/{id}", authed(http.HandlerFunc(s.sessionState)))
	mux.Handle("DELETE /api/v1/sessions/{id}", authed(http.HandlerFunc(s.closeSession)))
	mux.Handle("POST /api/v1/sessions/{id}/shares", authed(http.HandlerFunc(s.share)))
}

// mayOpen reports whether the caller of claims may open c: c's allow
// list names the caller or one of the caller's groups. Names are matched
// whichever issuer signed the token, as the allow lists name no issuer:
// trusting an issuer trusts it for every name it may use, which excludes
// static users' unless the issuer is linked to them.
func mayOpen(c *config.Connection, claims *auth.Claims) bool {
	if slices.Contains(c.Allow.Users, claims.Subject) {
		return true
	}
	for _, g := range claims.Groups {
		if slices.Contains(c.Allow.Groups, g) {
			return true
		}
	}
	return false
}

// connection returns the connection id if the caller of claims may open
// it, and nil otherwise, so that a connection the caller may not open is
// as absent as one that does not exist.
func (s *server) connection(id string, claims *auth.Claims) *config.Connection {
	for i := range s.connections {
		if c := &s.connections[i]; c.ID == id && mayOpen(c, claims) {
			return c
		}
	}
	return nil
}

// connectionInfo describes a connection in GET /api/v1/connections. The
// target is left out: it names hosts the caller has no need to know.
type connectionInfo struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Protocol string `json:"protocol"`
}

type connectionList struct {
	Connections []connectionInfo `json:"connections"`
	// AdhocTargets reports whether POST /api/v1/sessions takes ad-hoc
	// targets.
	AdhocTargets bool `json:"adhoc_targets"`
}

// listConnections implements GET /api/v1/connections: the connections
// the caller may open, in the order of the configuration.
func (s *server) listConnections(w http.ResponseWriter, r *http.Request) {
	claims, _ := claimsFrom(r.Context())
	list := connectionList{Connections: []connectionInfo{}, AdhocTargets: s.adhoc != nil}
	for i := range s.connections {
		if c := &s.connections[i]; mayOpen(c, claims) {
			list.Connections = append(list.Connections, connectionInfo{ID: c.ID, Name: c.Name, Protocol: c.Protocol})
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	respond(w, http.StatusOK, list)
}

// openRequest is the body of POST /api/v1/sessions: a connection of the
// registry, or an ad-hoc target.
type openRequest struct {
	Connection string `json:"connection,omitempty"`

	// An ad-hoc target, with allow_adhoc_targets. Its credentials are
	// asked of the owner, so the request carries none.
	Protocol              string   `json:"protocol,omitempty"`
	Target                string   `json:"target,omitempty"`
	Username              string   `json:"username,omitempty"`
	HostKeys              []string `json:"host_keys,omitempty"`
	InsecureIgnoreHostKey bool     `json:"insecure_ignore_host_key,omitempty"`
}

// openResponse is the reply of POST /api/v1/sessions.
type openResponse struct {
	ID   string `json:"id"`
	Role string `json:"role"` // always owner
	// StreamToken is a session token that joins the session as its
	// owner, for GET /api/v1/sessions/{id}/stream.
	StreamToken string `json:"stream_token"`
	ExpiresIn   int64  `json:"expires_in"` // the stream token's, in seconds
}

// requestError is a request that the session routes refuse, with its
// API v1 error.
type requestError struct {
	status        int
	code, message string
}

func (e *requestError) Error() string { return e.message }

func badRequest(format string, args ...any) *requestError {
	return &requestError{http.StatusBadRequest, codeBadRequest, fmt.Sprintf(format, args...)}
}

// openSession implements POST /api/v1/sessions. The session starts at
// once, and its owner has the owner grace, 60 s, to open its stream with
// the stream token or an access token; prompts for credentials wait for
// that stream.
func (s *server) openSession(w http.ResponseWriter, r *http.Request) {
	claims, _ := claimsFrom(r.Context())
	var req openRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	t, rerr := s.target(r.Context(), claims, &req)
	if rerr != nil {
		writeError(w, rerr.status, rerr.code, rerr.message)
		return
	}
	p := principalFrom(r.Context())
	info, owner, err := s.open(t, p, screen{})
	if errors.Is(err, errSessionLimit) {
		writeError(w, http.StatusTooManyRequests, codeLimitExceeded, fmt.Sprintf("you have %d sessions open, as many as a user may: close one first", s.limits.SessionsPerUser))
		return
	}
	if errors.Is(err, errSessionsFull) {
		writeError(w, http.StatusServiceUnavailable, codeLimitExceeded, "occamyd runs as many sessions as it takes: try again later")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, codeInternal, "the session could not be opened")
		return
	}
	// Nobody serves the owner's viewer, so it leaves, which starts the
	// grace period within which the owner's stream joins.
	owner.Leave()
	token, err := s.signer.SessionFor(info.id, auth.RoleOwner, sessionUser(p, claims), s.streamTTL)
	if err != nil {
		log.Printf("session %s: signing the stream token: %v", info.id, err)
		info.s.Close()
		writeError(w, http.StatusInternalServerError, codeInternal, "internal server error")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Location", "/api/v1/sessions/"+info.id)
	respond(w, http.StatusCreated, openResponse{
		ID:          info.id,
		Role:        auth.RoleOwner,
		StreamToken: token,
		ExpiresIn:   int64(s.streamTTL / time.Second),
	})
}

// target resolves the body of POST /api/v1/sessions to what the session
// connects to.
func (s *server) target(ctx context.Context, claims *auth.Claims, req *openRequest) (target, *requestError) {
	adhoc := req.Protocol != "" || req.Target != "" || req.Username != "" || len(req.HostKeys) > 0 || req.InsecureIgnoreHostKey
	switch {
	case req.Connection != "" && adhoc:
		return target{}, badRequest("name a connection or an ad-hoc target, not both")
	case req.Connection != "":
		c := s.connection(req.Connection, claims)
		if c == nil {
			return target{}, &requestError{http.StatusNotFound, codeNotFound, "no such connection"}
		}
		return connectionTarget(c), nil
	case !adhoc:
		return target{}, badRequest("name a connection, or an ad-hoc target with protocol and target")
	}
	return s.adhocTarget(ctx, req)
}

// connectionTarget is what a connection of the registry connects to.
func connectionTarget(c *config.Connection) target {
	return target{
		connection:            c.ID,
		protocol:              c.Protocol,
		backend:               c.Backend,
		addr:                  c.Target,
		creds:                 c.Credentials,
		hostKeys:              c.HostKeys,
		insecureIgnoreHostKey: c.InsecureIgnoreHostKey,
		tls:                   c.TLS,
		ard:                   c.ARD,
		security:              c.Security,
		kubernetes:            c.Kubernetes,
	}
}

// adhocTarget checks an ad-hoc target as ParseV1 checks a connection,
// and against adhoc_allowlist. Its credentials are prompted for.
func (s *server) adhocTarget(ctx context.Context, req *openRequest) (target, *requestError) {
	if s.adhoc == nil {
		return target{}, &requestError{http.StatusForbidden, codeForbidden, "ad-hoc targets are off: name a connection"}
	}
	if !s.backends.serves(req.Protocol) {
		return target{}, badRequest("unknown protocol %q", req.Protocol)
	}
	if !config.AdhocProtocol(req.Protocol) {
		return target{}, badRequest("%s has no ad-hoc targets: name a connection", req.Protocol)
	}
	addr, err := config.NormalizeTarget(req.Protocol, req.Target)
	if err != nil {
		return target{}, badRequest("target: %v", err)
	}
	if req.Protocol == "ssh" {
		switch {
		case len(req.HostKeys) == 0 && !req.InsecureIgnoreHostKey:
			return target{}, badRequest("an ssh target needs host_keys, or insecure_ignore_host_key: true")
		case len(req.HostKeys) > 0 && req.InsecureIgnoreHostKey:
			return target{}, badRequest("set host_keys or insecure_ignore_host_key, not both")
		}
		for i, k := range req.HostKeys {
			if err := ssh.CheckHostKey(k); err != nil {
				return target{}, badRequest("host_keys[%d]: %v", i, err)
			}
		}
	} else if len(req.HostKeys) > 0 || req.InsecureIgnoreHostKey {
		return target{}, badRequest("only ssh targets have host keys")
	}
	host, port, _ := net.SplitHostPort(addr)
	n, err := strconv.ParseUint(port, 10, 16)
	if err != nil {
		return target{}, badRequest("target: %v", err) // NormalizeTarget took it
	}
	dial, err := s.adhoc.allow(ctx, req.Protocol, host, uint16(n))
	if err != nil {
		return target{}, &requestError{http.StatusForbidden, codeForbidden, err.Error()}
	}
	t := target{
		protocol:              req.Protocol,
		backend:               s.adhocBackends[req.Protocol],
		addr:                  net.JoinHostPort(dial.String(), port),
		creds:                 config.Credentials{Username: req.Username, Prompt: true},
		hostKeys:              req.HostKeys,
		insecureIgnoreHostKey: req.InsecureIgnoreHostKey,
	}
	if _, err := netip.ParseAddr(host); err != nil {
		t.name = host
	}
	return t, nil
}

// open starts a session on t for owner, sized as s asks, and returns it
// with the owner's viewer, or errSessionLimit if owner has as many as
// limits allows. Failures are logged here; they are occamyd's own, such
// as a password file it cannot read.
func (s *server) open(t target, owner principal, sz screen) (*sessionInfo, *session.Viewer, error) {
	what := fmt.Sprintf("connection %q", t.connection)
	if t.connection == "" {
		what = fmt.Sprintf("ad-hoc target %q", t.protocol+"://"+t.addr)
	}
	fail := func(err error) (*sessionInfo, *session.Viewer, error) {
		// #nosec G706 -- the user name and the target are quoted, so they cannot break the line.
		log.Printf("user %q opening %s: %v", owner.String(), what, err)
		return nil, nil, err
	}
	maker, ok := s.backends.lookup(t.protocol, t.backend)
	if !ok {
		return fail(fmt.Errorf("no backend %q serves %s", config.BackendOf(t.protocol, t.backend), t.protocol))
	}
	// The session counts toward the owner's, and occamyd's, from now
	// until it ends, so that concurrent requests cannot all pass the
	// limits.
	s.mu.Lock()
	switch {
	case s.owned[owner] >= s.limits.SessionsPerUser:
		s.mu.Unlock()
		return fail(errSessionLimit)
	case s.running >= s.limits.Sessions:
		s.mu.Unlock()
		return fail(errSessionsFull)
	}
	s.owned[owner]++
	s.running++
	s.mu.Unlock()
	release := func() {
		s.mu.Lock()
		if s.owned[owner]--; s.owned[owner] <= 0 {
			delete(s.owned, owner)
		}
		s.running--
		s.mu.Unlock()
	}
	b, err := maker.make(t, sz)
	if err != nil {
		release()
		return fail(err)
	}
	id, sess, v, err := s.registry.Open(session.Config{Backend: b, MaxViewers: s.limits.ViewersPerSession})
	if err != nil {
		release()
		return fail(err)
	}
	info := &sessionInfo{
		id:         id,
		s:          sess,
		owner:      owner,
		connection: t.connection,
		protocol:   t.protocol,
		backend:    maker.name,
		opened:     time.Now(),
		gone:       make(chan struct{}),
	}
	s.mu.Lock()
	s.sessions[id] = info
	s.mu.Unlock()
	go func() {
		<-sess.Done()
		s.mu.Lock()
		delete(s.sessions, id)
		s.mu.Unlock()
		release()
		close(info.gone)
	}()
	// #nosec G706 -- the user name and the target are quoted, so they cannot break the line.
	log.Printf("session %s: user %q opened %s through %s", id, owner.String(), what, maker.name)
	return info, v, nil
}

// session returns the running session id, or nil.
func (s *server) session(id string) *sessionInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions[id]
}

// ownSession returns the running session of the route's {id} if the
// caller owns it. Otherwise it has answered 404, so that another user's
// session is as absent as an ended one.
func (s *server) ownSession(w http.ResponseWriter, r *http.Request) (*sessionInfo, bool) {
	info := s.session(r.PathValue("id"))
	if info == nil || info.owner != principalFrom(r.Context()) {
		writeError(w, http.StatusNotFound, codeNotFound, "no such session")
		return nil, false
	}
	return info, true
}

// sessionStateResponse is the reply of GET /api/v1/sessions/{id}.
type sessionStateResponse struct {
	ID         string `json:"id"`
	Connection string `json:"connection,omitempty"` // absent for an ad-hoc target
	Protocol   string `json:"protocol"`
	Backend    string `json:"backend"`
	Class      string `json:"class"`
	// Size is in pixels for a graphical session and in character cells
	// for a terminal; absent until the remote reports it.
	Size    *sessionSize `json:"size,omitempty"`
	Viewers []viewerInfo `json:"viewers"`
	Opened  time.Time    `json:"opened"`
}

type sessionSize struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

type viewerInfo struct {
	ID   int    `json:"id"`
	Role string `json:"role"`
}

// sessionState implements GET /api/v1/sessions/{id} for the owner.
func (s *server) sessionState(w http.ResponseWriter, r *http.Request) {
	info, ok := s.ownSession(w, r)
	if !ok {
		return
	}
	st := sessionStateResponse{
		ID:         info.id,
		Connection: info.connection,
		Protocol:   info.protocol,
		Backend:    info.backend,
		Class:      info.s.Class().String(),
		Viewers:    []viewerInfo{},
		Opened:     info.opened.UTC().Truncate(time.Second),
	}
	if size := info.s.Size(); size.X > 0 && size.Y > 0 {
		st.Size = &sessionSize{Width: size.X, Height: size.Y}
	}
	for _, v := range info.s.Viewers() {
		st.Viewers = append(st.Viewers, viewerInfo{ID: v.ID, Role: v.Role.String()})
	}
	w.Header().Set("Cache-Control", "no-store")
	respond(w, http.StatusOK, st)
}

// closeSession implements DELETE /api/v1/sessions/{id} for the owner. It
// returns once the session has ended, and every stream of the session
// gets close with session-closed, whatever tokens are still valid.
func (s *server) closeSession(w http.ResponseWriter, r *http.Request) {
	info, ok := s.ownSession(w, r)
	if !ok {
		return
	}
	info.s.Close()
	// The session has ended. It is forgotten a moment later, and
	// DELETE answers only then, so that a request after it, such as a
	// GET, another DELETE, a share or a new session at the owner's
	// limit, finds the session gone.
	<-info.gone
	log.Printf("session %s: closed by its owner", info.id)
	w.WriteHeader(http.StatusNoContent)
}

// shareRequest is the body of POST /api/v1/sessions/{id}/shares.
type shareRequest struct {
	Role      string `json:"role"`                 // viewer or read-only
	ExpiresIn int64  `json:"expires_in,omitempty"` // seconds, an hour if zero
}

// shareResponse is its reply.
type shareResponse struct {
	Token     string `json:"token"`
	Role      string `json:"role"`
	ExpiresIn int64  `json:"expires_in"`
}

// share implements POST /api/v1/sessions/{id}/shares for the owner: a
// token that joins the session as a viewer or read-only viewer until it
// expires or the session ends. The registry keeps only its hash.
func (s *server) share(w http.ResponseWriter, r *http.Request) {
	info, ok := s.ownSession(w, r)
	if !ok {
		return
	}
	var req shareRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	var role session.Role
	switch req.Role {
	case auth.RoleViewer:
		role = session.RoleViewer
	case auth.RoleReadOnly:
		role = session.RoleReadOnly
	default:
		writeError(w, http.StatusBadRequest, codeBadRequest, `role must be "viewer" or "read-only"`)
		return
	}
	ttl := defaultShareLifetime
	if req.ExpiresIn != 0 {
		// Seconds are checked before they become a Duration, which a
		// huge number would overflow.
		if maxSeconds := int64(maxShareLifetime / time.Second); req.ExpiresIn < 60 || req.ExpiresIn > maxSeconds {
			writeError(w, http.StatusBadRequest, codeBadRequest, fmt.Sprintf("expires_in must lie between 60 and %d seconds", maxSeconds))
			return
		}
		ttl = time.Duration(req.ExpiresIn) * time.Second
	}
	token, err := s.registry.Share(info.id, role, ttl)
	if errors.Is(err, session.ErrNotFound) {
		writeError(w, http.StatusNotFound, codeNotFound, "no such session")
		return
	}
	if errors.Is(err, session.ErrShareLimit) {
		writeError(w, http.StatusTooManyRequests, codeLimitExceeded, fmt.Sprintf("the session has %d shares that have not expired, as many as it may", s.limits.SharesPerSession))
		return
	}
	if err != nil {
		log.Printf("session %s: sharing: %v", info.id, err)
		writeError(w, http.StatusInternalServerError, codeInternal, "internal server error")
		return
	}
	log.Printf("session %s: shared with role %s for %v", info.id, req.Role, ttl)
	w.Header().Set("Cache-Control", "no-store")
	respond(w, http.StatusCreated, shareResponse{Token: token, Role: req.Role, ExpiresIn: int64(ttl / time.Second)})
}
