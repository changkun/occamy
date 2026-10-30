// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package server

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"errors"
	"expvar"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"changkun.de/x/occamy/internal/auth"
	"changkun.de/x/occamy/internal/config"
	"changkun.de/x/occamy/internal/session"
)

// Limits of the public listener. ReadTimeout and WriteTimeout end
// requests, not WebSockets: net/http clears both deadlines when a handler
// hijacks the connection for an upgrade.
const (
	readTimeout    = 30 * time.Second
	writeTimeout   = 30 * time.Second
	idleTimeout    = 2 * time.Minute
	maxHeaderBytes = 64 << 10
	maxBodyBytes   = 1 << 20
)

// Variables, so that tests can shorten them.
var (
	// readHeaderTimeout bounds how long a client may take to send its
	// request headers, so that slow clients cannot hold connections.
	readHeaderTimeout = 10 * time.Second
	// drainTimeout bounds how long a shutdown waits for requests in
	// flight, inside the 10 s that docker stop allows before it kills.
	drainTimeout = 5 * time.Second
)

// server is occamyd serving a v1 file.
type server struct {
	verifier *auth.Verifier
	signer   *auth.Signer
	ids      identities // the principals that tokens speak for
	signIn   *staticUsers
	oidc     *oidcSignIn // nil unless auth.oidc
	tls      *tls.Config // nil serves plain HTTP
	v0       *proxy      // nil unless legacy_v0

	// API v1's sessions and streams.
	connections []config.Connection
	origins     map[string]bool // origins whose pages may open streams, besides occamyd's own
	adhoc       *allowlist      // nil unless allow_adhoc_targets
	backends    backends
	registry    *session.Registry
	stop        context.CancelFunc // ends every session, at shutdown
	streamTTL   time.Duration      // of the stream token POST /api/v1/sessions returns
	limits      config.Limits

	mu       sync.Mutex
	sessions map[string]*sessionInfo // by ID, while they run
	owned    map[principal]int       // the sessions each user owns, while they run
	streams  tracker                 // the streams being served
}

// Run serves a v1 file until SIGINT or SIGTERM, then drains requests in
// flight for up to five seconds; a second signal ends occamyd at once. A
// drain that runs out is a normal stop: only a server that fails is an
// error.
func Run(f *config.File) error {
	s, err := newServer(f)
	if err != nil {
		return err
	}
	if s.oidc != nil {
		go s.oidc.warm()
	}
	ln, dln, err := listen(f)
	if err != nil {
		return err
	}
	var debug *http.Server
	if dln != nil {
		debug = newDebugServer()
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sig)
	go func() {
		select {
		case s := <-sig:
			// Stop listening before the drain, so that a second signal
			// gets Go's default handling and ends occamyd at once.
			signal.Stop(sig)
			log.Printf("received %v; another one stops occamyd at once", s)
			cancel()
		case <-ctx.Done():
		}
	}()
	scheme := "http"
	if s.tls != nil {
		scheme = "https"
	}
	log.Printf("serving API v1 at %s://%s", scheme, ln.Addr())
	if dln != nil {
		log.Printf("serving expvar and pprof at http://%s/debug/", dln.Addr())
	}
	err = serveUntil(ctx, drainTimeout, s.publicServer(), ln, debug, dln)
	// Shutdown neither waits for nor closes hijacked connections, so the
	// streams, which the sessions' ends are closing, get as long again.
	if !s.streams.wait(drainTimeout) {
		log.Printf("warning: streams still open after a further %v were cut", drainTimeout)
	}
	log.Println("occamy is down, good bye!")
	return err
}

// publicServer returns the public listener's server, which ends every
// session, and with them their streams, when it shuts down: Shutdown
// does not reach the hijacked connections of streams.
func (s *server) publicServer() *http.Server {
	public := newPublicServer(s.routes(), s.tls)
	public.RegisterOnShutdown(s.shutdown)
	return public
}

// shutdown ends every session, which sends their streams close with
// server-shutdown.
func (s *server) shutdown() { s.stop() }

// listen opens the public listener and, only if debug.address is set,
// the debug listener; dln is nil otherwise.
func listen(f *config.File) (ln, dln net.Listener, err error) {
	if ln, err = net.Listen("tcp", f.Listen); err != nil {
		return nil, nil, err
	}
	if f.Debug.Address == "" {
		return ln, nil, nil
	}
	// ParseV1 refuses a debug address that overlaps listen; this also
	// covers a File built another way and the port that listen's port 0
	// was given.
	if config.Overlap(ln.Addr().String(), f.Debug.Address) || config.Overlap(f.Listen, f.Debug.Address) {
		ln.Close()
		return nil, nil, fmt.Errorf("debug.address %q shares a port with the public listener on %s", f.Debug.Address, ln.Addr())
	}
	if dln, err = net.Listen("tcp", f.Debug.Address); err != nil {
		ln.Close()
		return nil, nil, err
	}
	if !loopback(f.Debug.Address) {
		// #nosec G706 -- the operator's configuration, not a client's input.
		log.Printf("warning: the debug listener on %s serves profiles and the command line without authentication beyond the loopback interface", f.Debug.Address)
	}
	return ln, dln, nil
}

// newServer prepares a v1 file's server: its signing key, created with a
// warning if the file does not exist, the verifier of occamyd's and the
// trusted issuers' tokens, the static users, TLS and the v0 routes.
func newServer(f *config.File) (*server, error) {
	key, created, err := auth.LoadOrCreateKey(f.Auth.SigningKeyFile)
	if err != nil {
		return nil, err
	}
	if created {
		// #nosec G706 -- the operator's configuration, not a client's input.
		log.Printf("warning: created a new signing key in %s; tokens signed with any earlier key no longer verify", f.Auth.SigningKeyFile)
	}
	pub, ok := key.Public().(ed25519.PublicKey)
	if !ok {
		return nil, errors.New("auth: the signing key has no Ed25519 public key")
	}
	keys := map[string]ed25519.PublicKey{auth.Issuer: pub}
	for _, ti := range f.Auth.TrustedIssuers {
		if _, ok := keys[ti.Issuer]; ok {
			// ParseV1 rejects both; a File built another way could slip.
			return nil, fmt.Errorf("auth.trusted_issuers: %q is occamyd's own issuer or listed twice", ti.Issuer)
		}
		if keys[ti.Issuer], err = auth.LoadPublicKey(ti.PublicKeyFile); err != nil {
			return nil, err
		}
	}
	signer := auth.NewSigner(key, auth.Issuer, auth.Audience)
	signIn, err := newStaticUsers(f.Auth.Users, signer, f.Auth.AccessTokenLifetime)
	if err != nil {
		return nil, err
	}
	ctx, stop := context.WithCancel(context.Background())
	limits := f.Limits.WithDefaults()
	registry := session.NewRegistry(ctx)
	registry.MaxShares = limits.SharesPerSession
	s := &server{
		verifier:    auth.NewVerifier(auth.Audience, keys),
		signer:      signer,
		ids:         newIdentities(f),
		signIn:      signIn,
		connections: f.Connections,
		origins:     map[string]bool{},
		backends:    newBackends(f.Guacd),
		registry:    registry,
		stop:        stop,
		streamTTL:   streamTokenLifetime,
		limits:      limits,
		sessions:    map[string]*sessionInfo{},
		owned:       map[principal]int{},
	}
	for _, o := range f.Origins {
		s.origins[strings.ToLower(o)] = true
	}
	if f.AllowAdhocTargets {
		if s.adhoc, err = newAllowlist(f.AdhocAllowlist); err != nil {
			return nil, fmt.Errorf("adhoc_allowlist: %w", err)
		}
	}
	if f.TLS.Cert != "" || f.TLS.Key != "" {
		cert, err := tls.LoadX509KeyPair(f.TLS.Cert, f.TLS.Key)
		if err != nil {
			return nil, fmt.Errorf("tls: %w", err)
		}
		s.tls = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}}
	}
	if f.Auth.OIDC != nil {
		if s.oidc, err = newOIDC(f, signer, s.tls != nil); err != nil {
			return nil, err
		}
	}
	if f.LegacyV0 {
		if s.v0, err = newLegacyV0(f.Guacd); err != nil {
			return nil, err
		}
		log.Printf("warning: legacy_v0 serves /api/v0/login, which signs any target for anyone, as v0 did")
	}
	return s, nil
}

// newLegacyV0 returns the v0 routes of a v1 file. A v1 file has no v0
// secret, and needs none: the routes sign their HS256 tokens with a key
// made for this process, since only occamyd reads them. The web client
// under /static and pprof are not served, since a v1 file has no key for
// the one and serves the other on the debug listener.
func newLegacyV0(g config.Guacd) (*proxy, error) {
	tokens, err := newTokens(rand.Text(), "HS256")
	if err != nil {
		return nil, err
	}
	return &proxy{
		guacd:    g,
		tokens:   tokens,
		legacyV0: true,
		login:    true,
		sessions: make(map[string]*Session),
	}, nil
}

// routes returns the public listener's handler: API v1, /healthz and,
// with legacy_v0, the v0 routes, behind the body limit, the request log
// and the recovery. The body limit comes first, on net/http's own
// response writer, which is the only one through which a body over the
// limit can have net/http close the connection.
func (s *server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthz)
	mux.HandleFunc("POST /api/v1/auth/token", s.signIn.token)
	if s.oidc != nil {
		s.oidc.register(mux)
	}
	s.registerSessions(mux)
	s.registerStreams(mux)
	if s.v0 != nil {
		s.v0.register(mux)
	}
	return limitBodies(maxBodyBytes)(logRequests(recovery(apiFallback(mux))))
}

// newPublicServer returns the public listener's server for h, which
// serves TLS with tlsConfig unless it is nil. It speaks HTTP/1.1 only:
// the streams are WebSockets, which coder/websocket upgrades by hijacking
// an HTTP/1.1 connection. Over HTTP/2 a browser that takes an advertised
// RFC 8441 upgrade, which a future Go may advertise by default, would get
// no stream. ConnContext keeps each connection where connFrom finds it.
func newPublicServer(h http.Handler, tlsConfig *tls.Config) *http.Server {
	var p http.Protocols
	p.SetHTTP1(true)
	return &http.Server{
		Handler:           h,
		TLSConfig:         tlsConfig,
		Protocols:         &p,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
		ConnContext:       connContext,
	}
}

// newDebugServer returns the debug listener's server: expvar under
// /debug/vars and pprof under /debug/pprof/, never on the public
// listener. It has no write timeout, since a CPU profile or trace takes
// as long as asked.
func newDebugServer() *http.Server {
	mux := http.NewServeMux()
	mux.Handle("GET /debug/vars", expvar.Handler())
	profile(func(pattern string, h http.HandlerFunc) { mux.Handle(pattern, h) })
	return &http.Server{
		Handler:           logRequests(recovery(mux)),
		ReadHeaderTimeout: readHeaderTimeout,
		IdleTimeout:       idleTimeout,
	}
}

// serveUntil serves public on ln, with TLS if public has a TLSConfig, and
// debug on dln unless debug is nil, until ctx is done or a server fails.
// Then it shuts both down: the listeners close at once, requests in
// flight have up to drain to finish, and connections still open after it
// are closed, with a warning. Hijacked connections, such as WebSockets,
// are neither waited for nor closed: net/http no longer tracks them. The
// error is that of a server that failed, and nil after a stop through
// ctx, however the drain ended.
func serveUntil(ctx context.Context, drain time.Duration, public *http.Server, ln net.Listener, debug *http.Server, dln net.Listener) error {
	servers := []*http.Server{public}
	errc := make(chan error, 2)
	go func() {
		if public.TLSConfig != nil {
			errc <- public.ServeTLS(ln, "", "")
			return
		}
		errc <- public.Serve(ln)
	}()
	if debug != nil {
		servers = append(servers, debug)
		go func() { errc <- debug.Serve(dln) }()
	}
	running := len(servers)

	var err error
	select {
	case <-ctx.Done():
		log.Printf("shutting down: draining requests for up to %v", drain)
	case err = <-errc:
		running--
	}
	sctx, cancel := context.WithTimeout(context.Background(), drain)
	defer cancel()
	for _, srv := range servers {
		if serr := srv.Shutdown(sctx); serr != nil {
			// A request that outlasts the drain, such as a long CPU
			// profile, is cut; stopping was asked for, so it is no error.
			log.Printf("warning: requests still in flight after the %v drain were cut: %v", drain, serr)
			srv.Close()
		}
	}
	for ; running > 0; running-- {
		if serr := <-errc; !errors.Is(serr, http.ErrServerClosed) {
			err = errors.Join(err, serr)
		}
	}
	return err
}

// loopback reports whether addr, host:port, listens on the loopback
// interface only.
func loopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	a, err := netip.ParseAddr(host)
	return err == nil && a.IsLoopback()
}
