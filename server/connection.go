// Copyright 2019 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package server

import (
	"context"
	"errors"
	"log"
	"net/http"
	"net/http/pprof"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"changkun.de/x/occamy/internal/config"
)

// clientDir holds the built web client, which occamyd serves under
// /static with client: true.
const clientDir = "./client/occamy-web/dist"

// Run is an export method that serves occamy proxy
func Run() {
	config.Init()
	tokens, err := newTokens(config.Runtime.Auth.JWTSecret, config.Runtime.Auth.JWTAlgorithm)
	if err != nil {
		log.Fatalf("initialize router error: %v", err)
	}
	proxy := &proxy{
		guacd:  config.Runtime.Guacd,
		tokens: tokens,
		// Every configuration that internal/config reads is a v0 file,
		// and a v0 file implies legacy_v0.
		legacyV0: true,
		client:   config.Runtime.Client,
		debug:    config.Runtime.Debug(),
		sessions: make(map[string]*Session),
	}
	proxy.serve(config.Runtime.Address)
}

// proxy is an occamy proxy that serves all sessions
// connects to occamy
type proxy struct {
	guacd    config.Guacd
	tokens   *tokens // v0 tokens
	legacyV0 bool    // serve the v0 routes under /api/v0
	client   bool    // serve the web client and the v0 login
	debug    bool    // serve pprof under /debug/pprof

	mu       sync.Mutex
	sessions map[string]*Session
}

func (p *proxy) serve(addr string) {
	s := &http.Server{
		Handler: p.routes(),
		Addr:    addr,
		// Bound how long a client may take to send its request headers,
		// so that slow clients cannot hold connections open.
		ReadHeaderTimeout: 10 * time.Second,
	}
	done := make(chan struct{})
	go func() {
		quit := make(chan os.Signal, 1)
		signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
		sig := <-quit
		log.Printf("shutting down occammy proxy... %v", sig)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := s.Shutdown(ctx); err != nil {
			log.Printf("server shutdown with error: %v", err)
		}
		cancel()
		done <- struct{}{}
	}()
	if p.debug {
		log.Printf("debug mode: serving /debug/pprof without a token; set mode: release where others can reach occamyd")
	}
	log.Printf("starting at http://%s...", addr)
	err := s.ListenAndServe()
	if !errors.Is(err, http.ErrServerClosed) {
		log.Printf("close with error: %v", err)
	}
	<-done
	log.Println("occamy proxy is down, good bye!")
}

// routes returns occamyd's handler: its routes on a ServeMux, behind the
// request log.
func (p *proxy) routes() http.Handler {
	mux := http.NewServeMux()
	if p.client {
		mux.Handle("GET /static/", http.StripPrefix("/static", http.FileServer(http.Dir(clientDir))))
	}
	if p.legacyV0 {
		if p.client {
			mux.HandleFunc("POST /api/v0/login", p.tokens.login)
		}
		mux.HandleFunc("GET /api/v0/connect", p.serveWS)
	}
	if p.debug {
		profile(mux)
	}
	return logRequests(mux)
}

// profile serves the net/http/pprof handlers under /debug/pprof, in debug
// mode only. docs/debugging.md describes how to use them. Index also
// serves the named profiles, such as /debug/pprof/heap.
func profile(mux *http.ServeMux) {
	mux.HandleFunc("GET /debug/pprof/", pprof.Index)
	mux.HandleFunc("GET /debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("GET /debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("GET /debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("POST /debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("GET /debug/pprof/trace", pprof.Trace)
}

// logRequests logs each request once it has been served: its method,
// path, status, duration and the client's address. It leaves out the
// query string, which carries the token to /api/v0/connect, and the
// headers. gin's logger wrote the query string, and with it the token.
func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r)
		// #nosec G706 -- the client's method and path are quoted, so they cannot break the line.
		log.Printf("%q %q %d %v %s", r.Method, r.URL.Path, sw.status(), time.Since(start).Round(time.Microsecond), r.RemoteAddr)
	})
}

// statusWriter records the status code of a response. Unwrap lets
// coder/websocket and http.ResponseController reach the connection's
// http.Hijacker and http.Flusher.
type statusWriter struct {
	http.ResponseWriter
	code int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.code == 0 {
		w.code = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.code == 0 {
		w.code = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *statusWriter) status() int {
	if w.code == 0 {
		return http.StatusOK
	}
	return w.code
}
