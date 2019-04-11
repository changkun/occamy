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
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/changkun/occamy/config"
	"github.com/changkun/occamy/lib"
	"github.com/changkun/occamy/protocol"

	"github.com/appleboy/gin-jwt"
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
	upgrader *websocket.Upgrader
	sessions map[string]*Session
	mu       sync.Mutex
}

func (p *proxy) serve() {
	server := &http.Server{
		Handler: p.router(),
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

func (p *proxy) router() (r *gin.Engine) {
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
	if config.Runtime.Client {
		r.StaticFS("/static", http.Dir("./client/occamy-web/dist"))
	}
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
		if config.Runtime.Client {
			v1.POST("/login", jwtm.LoginHandler)
		}
		auth := v1.Group("/connect")
		auth.Use(jwtm.MiddlewareFunc())
		auth.GET("", p.serveWS)
	}
	return
}

func (p *proxy) serveWS(c *gin.Context) {
	ws, err := p.upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		logrus.Errorf("occamy-proxy: upgrade websocket failed: %v", err)
		c.Writer.Write([]byte(http.StatusText(http.StatusBadRequest)))
		return
	}

	claims := jwt.ExtractClaims(c)
	err = p.routeConn(ws, &config.JWT{
		Protocol: claims["protocol"].(string),
		Host:     claims["host"].(string),
		Username: claims["username"].(string),
		Password: claims["password"].(string),
	})
	if err != nil {
		ws.WriteMessage(websocket.CloseMessage, []byte(err.Error()))
	}
	ws.Close()
}

func (p *proxy) routeConn(ws *websocket.Conn, conf *config.JWT) (err error) {
	// fast path
	sess, ok := p.sessions[conf.GenerateID()]
	if ok {
		err = sess.Join(ws, conf, false) // block here
		return
	}

	// slow path
	p.mu.Lock()
	sess, ok = p.sessions[conf.GenerateID()]
	if ok {
		p.mu.Unlock()
		err = sess.Join(ws, conf, false) // block here
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

// Session is an occamy proxy session that shares connection
// within an user group
type Session struct {
	connectedUsers uint64
	id             string
	once           sync.Once
	client         *lib.Client // shared client in a session
}

// NewSession creates a new occamy proxy session
func NewSession(proto string) (*Session, error) {
	runtime.LockOSThread() // without unlock to exit the Go thread

	cli, err := lib.NewClient()
	if err != nil {
		return nil, fmt.Errorf("occamy-lib: new client error: %v", err)
	}

	s := &Session{client: cli}
	if err = s.initialize(proto); err != nil {
		s.close()
		return nil, fmt.Errorf("occamy-lib: session initialization failed with error: %v", err)
	}
	return s, nil
}

// ID reports the session id
func (s *Session) ID() string {
	return s.id
}

// Join adds the given socket as a new user to the given process, automatically
// reading/writing from the socket via read/write threads. The given socket,
// parser, and any associated resources will be freed unless the user is not
// added successfully.
func (s *Session) Join(ws *websocket.Conn, conf *config.JWT, owner bool) error {
	defer s.close()
	lib.ResetErrors()

	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		return fmt.Errorf("occamy-proxy: new socket pair error: %v", err)
	}
	sock, err := lib.NewSocket(fds[0])
	if err != nil {
		logrus.Errorf("occamy-lib: create guac socket error: %v", err)
		return err
	}
	defer sock.Close()
	u, err := lib.NewUser(sock, s.client, owner, conf)
	if err != nil {
		logrus.Errorf("occamy-lib: create guac user error: %v", err)
		return err
	}
	defer u.Close()
	s.addUser()
	defer s.delUser()

	// 1. user goroutine
	go func(u *lib.User) {
		err := u.HandleConnectionWithHandshake() // block until disconnect/completion
		if err != nil {
			logrus.Errorf("occamy-lib: handle user connection error: %v", err)
		}
	}(u)

	// 2. proxy io
	conn := protocol.NewInstructionIO(fds[1])
	defer conn.Close()
	return s.serveIO(conn, ws)
}

func (s *Session) addUser() {
	atomic.AddUint64(&s.connectedUsers, 1)
}
func (s *Session) delUser() {
	atomic.AddUint64(&s.connectedUsers, ^uint64(0))
}

func (s *Session) initialize(proto string) error {
	s.client.InitLogLevel(config.Runtime.MaxLogLevel)
	err := s.client.LoadProtocolPlugin(proto)
	if err != nil {
		return err
	}
	s.id = s.client.Identifier()
	return nil
}

func (s *Session) close() {
	if atomic.LoadUint64(&s.connectedUsers) > 0 {
		return
	}
	s.client.Close()
}

func (s *Session) serveIO(conn *protocol.InstructionIO, ws *websocket.Conn) (err error) {
	wg := sync.WaitGroup{}
	exit := make(chan error, 2)
	wg.Add(2)
	go func(conn *protocol.InstructionIO, ws *websocket.Conn) {
		var err error
		for {
			raw, err := conn.ReadRaw()
			if err != nil {
				break
			}
			err = ws.WriteMessage(websocket.TextMessage, raw)
			if err != nil {
				break
			}
		}
		exit <- err
		wg.Done()
	}(conn, ws)
	go func(conn *protocol.InstructionIO, ws *websocket.Conn) {
		var err error
		for {
			_, buf, err := ws.ReadMessage()
			if err != nil {
				break
			}
			_, err = conn.WriteRaw(buf)
			if err != nil {
				break
			}
		}
		exit <- err
		wg.Done()
	}(conn, ws)
	err = <-exit
	wg.Wait()
	return
}
