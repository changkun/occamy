// Copyright 2019 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/pprof"
	"os"
	"os/signal"
	"sync"
	"time"

	jwt "github.com/appleboy/gin-jwt/v2"
	"github.com/changkun/occamy/config"
	"github.com/changkun/occamy/protocol"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/sirupsen/logrus"
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
	sessions map[string]*Session
	mu       sync.Mutex
	upgrader *websocket.Upgrader
}

func (p *proxy) serve() {
	server := &http.Server{
		Handler: p.routers(),
		Addr:    fmt.Sprintf("%s", config.Runtime.Address),
	}
	done := make(chan struct{})
	go func() {
		quit := make(chan os.Signal, 1)
		signal.Notify(quit, os.Interrupt, os.Kill)
		sig := <-quit
		logrus.Errorf("occamy-proxy: shutting down occammy proxy... %v", sig)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := server.Shutdown(ctx); err != nil {
			logrus.Errorf("occamy-proxy: server shutdown with error: %v", err)
		}
		cancel()
		done <- struct{}{}
	}()
	logrus.Infof("occamy-proxy: starting at http://%s...", config.Runtime.Address)
	err := server.ListenAndServe()
	if err != http.ErrServerClosed {
		logrus.Errorf("occamy-proxy: close with error: %v", err)
	}
	<-done
	logrus.Info("occamy-proxy: occamy proxy is down, good bye!")
	return
}

func (p *proxy) routers() (r *gin.Engine) {
	r = gin.Default()
	p.initJWT()
	if config.Runtime.Client {
		r.StaticFS("/static", http.Dir("./client/occamy-web/dist"))
	}
	v1 := r.Group("/api/v1")
	v1.GET("/ping", p.Ping)
	if config.Runtime.Client {
		v1.POST("/login", p.jwtm.LoginHandler)
	}
	auth := v1.Group("/connect")
	auth.Use(p.jwtm.MiddlewareFunc())
	auth.GET("", p.serveWS)
	if gin.Mode() != gin.DebugMode {
		return
	}
	profile(r)
	return
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
				logrus.Error("err: ", err)
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
		logrus.Fatalf("occamy-proxy: initialize router error: %v", err)
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
func profile(r *gin.Engine) {
	pprofHandler := func(h http.HandlerFunc) gin.HandlerFunc {
		handler := http.HandlerFunc(h)
		return func(c *gin.Context) {
			handler.ServeHTTP(c.Writer, c.Request)
		}
	}
	prefixRouter := r.Group("/debug/pprof")
	{
		prefixRouter.GET("/", pprofHandler(pprof.Index))
		prefixRouter.GET("/cmdline", pprofHandler(pprof.Cmdline))
		prefixRouter.GET("/profile", pprofHandler(pprof.Profile))
		prefixRouter.POST("/symbol", pprofHandler(pprof.Symbol))
		prefixRouter.GET("/symbol", pprofHandler(pprof.Symbol))
		prefixRouter.GET("/trace", pprofHandler(pprof.Trace))
		prefixRouter.GET("/block", pprofHandler(pprof.Handler("block").ServeHTTP))
		prefixRouter.GET("/goroutine", pprofHandler(pprof.Handler("goroutine").ServeHTTP))
		prefixRouter.GET("/heap", pprofHandler(pprof.Handler("heap").ServeHTTP))
		prefixRouter.GET("/mutex", pprofHandler(pprof.Handler("mutex").ServeHTTP))
		prefixRouter.GET("/threadcreate", pprofHandler(pprof.Handler("threadcreate").ServeHTTP))
	}
}
