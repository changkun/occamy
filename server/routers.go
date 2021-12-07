// Copyright 2019 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package server

import (
	"log"
	"net/http"

	"changkun.de/x/occamy/internal/config"
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
	err = p.routeConn(ws, jwt)
	if err != nil {
		log.Printf("route connection failed: %v", err)
		ws.WriteMessage(websocket.CloseMessage, []byte(err.Error()))
	}
	ws.Close()
}

func (p *proxy) routeConn(ws *websocket.Conn, jwt *config.JWT) (err error) {
	p.mu.Lock()
	s, ok := p.sessions[jwt.GenerateID()]
	if ok {
		err = s.Join(ws, jwt, false, func() { p.mu.Unlock() })
		return
	}

	s, err = NewSession(jwt.Protocol)
	if err != nil {
		p.mu.Unlock()
		return
	}

	p.sessions[jwt.GenerateID()] = s
	log.Printf("new session was created: %s", s.ID)
	err = s.Join(ws, jwt, true, func() { p.mu.Unlock() }) // block here

	p.mu.Lock()
	delete(p.sessions, jwt.GenerateID())
	p.mu.Unlock()
	return
}
