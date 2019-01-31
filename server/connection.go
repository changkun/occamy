// Copyright 2019 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package server

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"time"

	"github.com/appleboy/gin-jwt"
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
			CheckOrigin: func(r *http.Request) bool {
				// FIXME: add origin check
				return true
			},
		},
	}
	proxy.serve()
}

// proxy is an occamy proxy that serves all sessions
// connects to occamy
type proxy struct {
	sessions map[string]*Session
	mu       sync.Mutex
	upgrader *websocket.Upgrader
}

func (p *proxy) serve() {
	server := &http.Server{
		Handler: p.setupRouter(),
		Addr:    fmt.Sprintf("%s", config.Runtime.Address),
	}
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
	}()
	logrus.Infof("occamy-proxy: starting at http://%s...", config.Runtime.Address)
	err := server.ListenAndServe()
	if err != http.ErrServerClosed {
		logrus.Errorf("occamy-proxy: close with error: %v", err)
	}
	logrus.Info("occamy-proxy: occamy proxy is down, good bye!")
	return
}

func (p *proxy) setupRouter() (r *gin.Engine) {
	r = gin.Default()

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
			v, ok := data.(*config.JWT)
			if !ok {
				return jwt.MapClaims{}
			}
			return jwt.MapClaims{
				"protocol": v.Protocol,
				"host":     v.Host,
				"username": v.Username,
				"password": v.Password,
			}
		},
		TokenLookup: "header: Authorization, query: token, cookie: jwt",
	})
	if err != nil {
		logrus.Fatalf("occamy-proxy: initialize router error: %v", err)
	}

	r.StaticFS("/static", http.Dir("./client/static"))
	v1 := r.Group("/api/v1")
	{
		v1.GET("/ping", func(c *gin.Context) {
			c.JSON(http.StatusOK, struct {
				Version   string `json:"version"`
				BuildTime string `json:"build_time"`
				GitCommit string `json:"git_commit"`
			}{
				Version:   config.Version,
				GitCommit: config.GitCommit,
				BuildTime: config.BuildTime,
			})
		})
		v1.POST("/login", jwtm.LoginHandler)
		auth := v1.Group("/connect")
		auth.Use(jwtm.MiddlewareFunc())
		auth.GET("", p.serveWS)
	}
	return
}

func (p *proxy) serveWS(c *gin.Context) {
	claims := jwt.ExtractClaims(c)
	conf := &config.JWT{
		Protocol: claims["protocol"].(string),
		Host:     claims["host"].(string),
		Username: claims["username"].(string),
		Password: claims["password"].(string),
	}

	ws, err := p.upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		logrus.Errorf("occamy-proxy: upgrade websocket failed: %v", err)
		c.Writer.Write([]byte(http.StatusText(http.StatusBadRequest)))
		return
	}

	err = p.routeConn(ws, conf)
	if err != nil {
		ws.WriteMessage(websocket.CloseMessage, []byte(err.Error()))
	}
	ws.Close()
}

func (p *proxy) routeConn(ws *websocket.Conn, conf *config.JWT) (err error) {
	// fast path
	sess, ok := p.sessions[conf.GenerateID()]
	if ok {
		err = sess.Join(ws, conf, false)
		return
	}

	// slow path
	p.mu.Lock()
	sess, ok = p.sessions[conf.GenerateID()]
	if ok {
		p.mu.Unlock()
		err = sess.Join(ws, conf, false)
		return
	}

	sess, err = NewSession(conf.Protocol)
	if err != nil {
		p.mu.Unlock()
		return
	}

	p.sessions[conf.GenerateID()] = sess
	p.mu.Unlock()

	logrus.Infof("occamy-proxy: new session was created: %s", sess.ID())
	err = sess.Join(ws, conf, true) // block here
	p.mu.Lock()
	delete(p.sessions, conf.GenerateID())
	p.mu.Unlock()
	return
}
