// Copyright 2019 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package server

import (
	"context"
	"log"
	"net/http"
	"net/http/pprof"
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
		sessions: make(map[string]*Session),
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

	mu       sync.Mutex
	sessions map[string]*Session
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
	if gin.Mode() == gin.DebugMode {
		p.profile()
	}
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

// profile the standard HandlerFuncs from the net/http/pprof package with
// the provided gin.Engine. prefixOptions is a optional. If not prefixOptions,
// the default path prefix is used, otherwise first prefixOptions will be path prefix.
//
// Basic Usage:
//
// - use the pprof tool to look at the heap profile:
//   go tool pprof http://0.0.0.0:5636/debug/pprof/heap
// - look at a 30-second CPU profile:
//   go tool pprof http://0.0.0.0:5636/debug/pprof/profile
// - look at the goroutine blocking profile, after calling runtime.SetBlockProfileRate:
//   go tool pprof http://0.0.0.0:5636/debug/pprof/block
// - collect a 5-second execution trace:
//   wget http://0.0.0.0:5636/debug/pprof/trace?seconds=5
//
func (p *proxy) profile() {
	pprofHandler := func(h http.HandlerFunc) gin.HandlerFunc {
		handler := http.HandlerFunc(h)
		return func(c *gin.Context) {
			handler.ServeHTTP(c.Writer, c.Request)
		}
	}
	r := p.engine.Group("/debug/pprof")
	{
		r.GET("/", pprofHandler(pprof.Index))
		r.GET("/cmdline", pprofHandler(pprof.Cmdline))
		r.GET("/profile", pprofHandler(pprof.Profile))
		r.POST("/symbol", pprofHandler(pprof.Symbol))
		r.GET("/symbol", pprofHandler(pprof.Symbol))
		r.GET("/trace", pprofHandler(pprof.Trace))
		r.GET("/allocs", pprofHandler(pprof.Handler("allocs").ServeHTTP))
		r.GET("/block", pprofHandler(pprof.Handler("block").ServeHTTP))
		r.GET("/goroutine", pprofHandler(pprof.Handler("goroutine").ServeHTTP))
		r.GET("/heap", pprofHandler(pprof.Handler("heap").ServeHTTP))
		r.GET("/mutex", pprofHandler(pprof.Handler("mutex").ServeHTTP))
		r.GET("/threadcreate", pprofHandler(pprof.Handler("threadcreate").ServeHTTP))
	}
}
