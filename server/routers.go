// Copyright 2019 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package server

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	"changkun.de/x/occamy/internal/config"
	"changkun.de/x/occamy/internal/guacd"
	jwt "github.com/appleboy/gin-jwt/v2"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

// serveWS implements /api/v1/connect
func (p *proxy) serveWS(c *gin.Context) {
	ws, err := p.upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		log.Printf("upgrade websocket failed: %v", err)
		c.Writer.Write([]byte(http.StatusText(http.StatusBadRequest)))
		return
	}

	claims := jwt.ExtractClaims(c)
	jwt := &config.JWT{
		Protocol: claims["protocol"].(string),
		Host:     claims["host"].(string),
		Username: claims["username"].(string),
		Password: claims["password"].(string),
	}
	p.serveConn(ws, jwt, displayHints(c.Request.URL.Query()))
}

// serveConn serves one client until it leaves, then closes the WebSocket
// with the Guacamole status code as the close reason.
func (p *proxy) serveConn(ws *websocket.Conn, jwt *config.JWT, d display) {
	err := p.routeConn(ws, jwt, d)
	if err != nil {
		log.Printf("connection failed: %v", err)
	}
	code, status := closeStatus(err)
	msg := websocket.FormatCloseMessage(code, strconv.Itoa(status))
	ws.WriteControl(websocket.CloseMessage, msg, time.Now().Add(time.Second))
	ws.Close()
}

// routeConn connects the client to guacd: as the owner of a new session,
// or as a joiner of the session with the same claims. It returns when the
// client or guacd leaves. The proxy lock is never held across network I/O.
func (p *proxy) routeConn(ws *websocket.Conn, jwt *config.JWT, d display) error {
	params, err := jwt.GuacdParams(p.guacd.Params)
	if err != nil {
		return fmt.Errorf("%w: %w", errBadRequest, err)
	}

	s, owner := p.enter(jwt.GenerateID())
	defer p.leave(s)

	ctx, cancel := context.WithTimeout(context.Background(), guacd.DefaultTimeout)
	defer cancel()
	cfg := guacd.Config{
		Params:   params,
		Width:    d.width,
		Height:   d.height,
		DPI:      d.dpi,
		Timezone: d.timezone,
	}
	role := "owner"
	if owner {
		cfg.Protocol = jwt.Protocol
	} else {
		role = "joiner"
		select {
		case <-s.ready:
		case <-ctx.Done():
			return fmt.Errorf("waiting for the session to start: %w", ctx.Err())
		}
		if s.err != nil {
			return s.err
		}
		cfg.ConnectionID = s.ConnID
	}

	conn, err := guacd.Dial(ctx, p.guacd.Address, cfg)
	if owner {
		p.started(s, conn, err)
	}
	if err != nil {
		return err
	}
	defer conn.Close()

	log.Printf("session %s: %s joined", conn.ID, role)
	err = serveIO(conn, ws)
	log.Printf("session %s: %s left", conn.ID, role)
	return err
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

// remove deletes s from the table unless a newer session has taken its
// key. The caller holds p.mu.
func (p *proxy) remove(s *Session) {
	if p.sessions[s.Key] == s {
		delete(p.sessions, s.Key)
	}
}
