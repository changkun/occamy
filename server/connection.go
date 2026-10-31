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
// /static with client: true, as a v1 file's web_client.
const clientDir = "./client/occamy-web/dist"

// RunV0 serves the v0 configuration that config.Init has read into
// config.Runtime, as occamyd served it before API v1.
func RunV0() {
	tokens, err := newTokens(config.Runtime.Auth.JWTSecret, config.Runtime.Auth.JWTAlgorithm)
	if err != nil {
		log.Fatalf("initialize router error: %v", err)
	}
	proxy := &proxy{
		guacd:  config.Runtime.Guacd,
		tokens: tokens,
		// A v0 file implies legacy_v0.
		legacyV0: true,
		client:   config.Runtime.Client,
		login:    config.Runtime.Client,
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
	client   bool    // serve the web client under /static
	login    bool    // serve /api/v0/login
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

// routes returns the handler of a v0 file: the v0 routes on a ServeMux,
// behind the request log.
func (p *proxy) routes() http.Handler {
	mux := newRouteMux()
	p.register(mux)
	return logRequests(mux.handler())
}

// register adds the v0 routes to mux, each behind recoverV0, which
// answers a panic with a bare 500 as gin's recovery did.
func (p *proxy) register(mux *routeMux) {
	handle := func(pattern string, h http.HandlerFunc) {
		mux.Handle(pattern, recoverV0(h))
	}
	if p.client {
		handle("GET /static/", webClient(clientDir).ServeHTTP)
	}
	if p.legacyV0 {
		if p.login {
			handle("POST /api/v0/login", p.tokens.login)
		}
		handle("GET /api/v0/connect", p.serveWS)
	}
	if p.debug {
		profile(handle)
	}
}

// profile serves the net/http/pprof handlers under /debug/pprof: on the
// public listener in a v0 file's debug mode, and on the debug listener
// of a v1 file. docs/debugging.md describes how to use them. Index also
// serves the named profiles, such as /debug/pprof/heap.
func profile(handle func(pattern string, h http.HandlerFunc)) {
	handle("GET /debug/pprof/", pprof.Index)
	handle("GET /debug/pprof/cmdline", pprof.Cmdline)
	handle("GET /debug/pprof/profile", pprof.Profile)
	handle("GET /debug/pprof/symbol", pprof.Symbol)
	handle("POST /debug/pprof/symbol", pprof.Symbol)
	handle("GET /debug/pprof/trace", pprof.Trace)
}
