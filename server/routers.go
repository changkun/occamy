// Copyright 2019 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"

	"github.com/coder/websocket"

	"changkun.de/x/occamy/internal/config"
	"changkun.de/x/occamy/internal/guacamole"
	"changkun.de/x/occamy/internal/guacd"
)

// serveWS implements GET /api/v0/connect.
func (p *proxy) serveWS(w http.ResponseWriter, r *http.Request) {
	jwt, ok := p.tokens.authenticate(w, r)
	if !ok {
		return
	}
	ws, err := accept(w, r)
	if err != nil {
		log.Printf("upgrade websocket failed: %v", err)
		return
	}
	p.serveConn(ws, jwt, displayHints(r.URL.Query()))
}

// accept upgrades the request to a WebSocket with the subprotocol
// guacamole, which guacamole-common-js asks for. On failure it has
// already replied with an HTTP error.
//
// Like gorilla/websocket's default, it rejects a cross-origin request
// with 403: an Origin header, if present, must name the request's host.
// Compression stays off, as it was on gorilla/websocket.
func accept(w http.ResponseWriter, r *http.Request) (*websocket.Conn, error) {
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		Subprotocols:    []string{"guacamole"}, // fixed by guacamole-common-js
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		return nil, err
	}
	// gorilla/websocket read messages of any size. coder/websocket stops
	// at 32 KiB by default, short of one instruction whose element holds
	// MaxInstructionLength 4-byte code points, so the limit is the largest
	// instruction that forward accepts.
	ws.SetReadLimit(guacamole.MaxInstructionSize)
	return ws, nil
}

// serveConn serves one client until it leaves, then closes the WebSocket
// with the Guacamole status code as the close reason.
func (p *proxy) serveConn(ws *websocket.Conn, jwt *config.JWT, d display) {
	err := p.routeConn(ws, jwt, d)
	if err != nil {
		log.Printf("connection failed: %v", err)
	}
	// serveIO has closed the WebSocket if the client reached guacd; this
	// closes it otherwise.
	closeWith(ws, err)
}

// routeConn connects the client to guacd: as the owner of a new session,
// or as a joiner of the session with the same claims. It returns when the
// client or guacd leaves. The proxy lock is never held across network I/O.
func (p *proxy) routeConn(ws *websocket.Conn, jwt *config.JWT, d display) error {
	params, err := jwt.GuacdParams(p.guacd.Params)
	if err != nil {
		return fmt.Errorf("%w: %w", errBadRequest, err)
	}

	cfg := guacd.Config{
		Params:   params,
		Width:    d.width,
		Height:   d.height,
		DPI:      d.dpi,
		Timezone: d.timezone,
	}

	key := jwt.GenerateID()
	s, owner := p.enter(key)
	conn, err := p.connect(s, owner, jwt.Protocol, cfg)
	// guacd ends a connection as soon as its last user leaves, but the
	// entry goes only after that user's proxy loop has returned. A client
	// that joins in between starts a new session instead, or joins the one
	// that another such client has started. Each pass drops a session that
	// guacd has ended, and the loop stops once the client owns a session
	// or has joined a live one.
	for !owner && ended(err) {
		log.Printf("session %s: ended while joining: %v", s.ConnID, err)
		p.abandon(s)
		s, owner = p.enter(key)
		conn, err = p.connect(s, owner, jwt.Protocol, cfg)
	}
	defer p.leave(s)
	if err != nil {
		return err
	}
	defer conn.Close()

	role := "owner"
	if !owner {
		role = "joiner"
	}
	log.Printf("session %s: %s joined", conn.ID, role)
	err = serveIO(conn, ws)
	log.Printf("session %s: %s left", conn.ID, role)
	return err
}

// connect performs the guacd handshake for a user of s. The owner creates
// the connection; a joiner waits for the owner's handshake and then joins
// the connection it created.
func (p *proxy) connect(s *Session, owner bool, protocol string, cfg guacd.Config) (*guacd.Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), guacd.DefaultTimeout)
	defer cancel()
	if owner {
		cfg.Protocol = protocol
	} else {
		select {
		case <-s.ready:
		case <-ctx.Done():
			return nil, fmt.Errorf("waiting for the session to start: %w", ctx.Err())
		}
		if s.err != nil {
			return nil, s.err
		}
		cfg.ConnectionID = s.ConnID
	}

	conn, err := guacd.Dial(ctx, p.guacd.Address, cfg)
	if owner {
		p.started(s, conn, err)
	}
	return conn, err
}

// ended reports whether a join failed because guacd has already ended the
// connection. guacd 1.6 answers a join to a connection it has removed with
// status 516. Before the removal, from the moment the last user leaves,
// the connection's process takes no more users, and guacd closes the
// joiner's socket without a word.
func ended(err error) bool {
	var ge *guacd.Error
	if errors.As(err, &ge) {
		return ge.Status == statusResourceNotFound
	}
	return errors.Is(err, io.EOF)
}

// enter counts a user of the session with the given key, creating the
// session if there is none. The user that creates it is the owner.
func (p *proxy) enter(key string) (s *Session, owner bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	s, ok := p.sessions[key]
	if !ok {
		s = &Session{Key: key, ready: make(chan struct{})}
		p.sessions[key] = s
	}
	s.users++
	return s, !ok
}

// started records the outcome of the owner's handshake and wakes the
// joiners. A failed session leaves the table at once, so that the next
// connection with the same claims starts afresh.
func (p *proxy) started(s *Session, conn *guacd.Conn, err error) {
	if err != nil {
		s.err = err
		p.mu.Lock()
		p.remove(s)
		p.mu.Unlock()
	} else {
		s.ConnID = conn.ID
	}
	close(s.ready)
}

// leave counts a user out of the session. Like guacd, which ends a
// connection when its last user leaves, the session goes with its last
// user.
func (p *proxy) leave(s *Session) {
	p.mu.Lock()
	defer p.mu.Unlock()
	s.users--
	if s.users == 0 {
		p.remove(s)
	}
}

// abandon counts a user out of a session that guacd has ended, and takes
// the session out of the table, so that the user's next attempt starts a
// new one. Its other users still leave it as usual.
func (p *proxy) abandon(s *Session) {
	p.mu.Lock()
	defer p.mu.Unlock()
	s.users--
	p.remove(s)
}

// remove deletes s from the table unless a newer session has taken its
// key. The caller holds p.mu.
func (p *proxy) remove(s *Session) {
	if p.sessions[s.Key] == s {
		delete(p.sessions, s.Key)
	}
}
