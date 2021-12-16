// Copyright 2019 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"time"

	"changkun.de/x/occamy/internal/config"
	"changkun.de/x/occamy/internal/protocol"
	jwt "github.com/appleboy/gin-jwt/v2"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

func init() {
	config.Init()
}

// Run is an export method that serves occamy proxy
func Run() {
	proxy := &proxy{
		// sessions: make(map[string]*Session),
		upgrader: &websocket.Upgrader{
			ReadBufferSize:  protocol.MaxInstructionLength,
			WriteBufferSize: protocol.MaxInstructionLength,
			Subprotocols:    []string{"guacamole"}, // fixed by guacamole-client
		},
	}
	proxy.serve()
}

// proxy is an occamy proxy that serves all sessions
// connects to occamy
type proxy struct {
	jwtm     *jwt.GinJWTMiddleware
	upgrader *websocket.Upgrader
	engine   *gin.Engine
	sess     sync.Map // map[string]*Session
}

func (p *proxy) serve() {
	s := &http.Server{
		Handler: p.routers(),
		Addr:    config.Runtime.Address,
	}
	done := make(chan struct{})
	go func() {
		quit := make(chan os.Signal, 1)
		signal.Notify(quit, os.Interrupt, os.Kill)
		sig := <-quit
		log.Printf("shutting down occammy proxy... %v", sig)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := s.Shutdown(ctx); err != nil {
			log.Printf("server shutdown with error: %v", err)
		}
		cancel()
		done <- struct{}{}
	}()
	log.Printf("starting at http://%s...", config.Runtime.Address)
	err := s.ListenAndServe()
	if err != http.ErrServerClosed {
		log.Printf("close with error: %v", err)
	}
	<-done
	log.Println("occamy proxy is down, good bye!")
	return
}

func (p *proxy) routers() *gin.Engine {
	p.engine = gin.Default()
	p.initJWT()
	if config.Runtime.Client {
		p.engine.StaticFS("/static", http.Dir("./client/occamy-web/dist"))
	}
	v1 := p.engine.Group("/api/v1")
	if config.Runtime.Client {
		v1.POST("/login", p.jwtm.LoginHandler)
	}
	auth := v1.Group("/connect")
	auth.Use(p.jwtm.MiddlewareFunc())
	auth.GET("", p.serveWS)
	return p.engine
}

func (p *proxy) initJWT() {
	jwtm, err := jwt.New(&jwt.GinJWTMiddleware{
		Realm:            "occamy-proxy",
		Key:              []byte(config.Runtime.Auth.JWTSecret),
		SigningAlgorithm: config.Runtime.Auth.JWTAlgorithm,
		TimeFunc:         time.Now().UTC,
		Authenticator: func(c *gin.Context) (interface{}, error) {
			var conf config.JWT
			err := c.ShouldBind(&conf)
			if err != nil {
				log.Printf("err: %v", err)
				return &conf, jwt.ErrFailedAuthentication
			}
			return &conf, nil
		},
		PayloadFunc: func(data interface{}) jwt.MapClaims {
			if v, ok := data.(*config.JWT); ok {
				return jwt.MapClaims{
					"protocol": v.Protocol,
					"host":     v.Host,
					"username": v.Username,
					"password": v.Password,
				}
			}
			return jwt.MapClaims{}
		},
		TokenLookup: "header: Authorization, query: token, cookie: jwt",
	})
	if err != nil {
		log.Fatalf("initialize router error: %v", err)
	}
	p.jwtm = jwtm
}
