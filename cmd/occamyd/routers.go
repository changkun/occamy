// Copyright 2019 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package main

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
	jwtId := jwt.GenerateID()

	// Creating a new session because there was no session yet.
	s, err := NewSession(jwt.Protocol)
	if err != nil {
		return
	}
	log.Printf("new session was created: %s", s.ID)

	// Check if there are already a session. If so, join.
	ss, loaded := p.sess.LoadOrStore(jwtId, s)
	if loaded {
		s.Close()
		s = ss.(*Session)
		log.Printf("already had old session: %s", s.ID)
	}

	err = s.Join(ws, jwt, true) // block here
	p.sess.Delete(jwtId)
	s.Close()
	return
}
