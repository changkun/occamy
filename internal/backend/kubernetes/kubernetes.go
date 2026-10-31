// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// Package kubernetes is a terminal backend that runs a command in a
// container of a Kubernetes pod, or attaches to the container's main
// process, through the API server's exec and attach subresources over
// WebSockets (the v5.channel.k8s.io and v4.channel.k8s.io
// subprotocols), and feeds the command's output to the session's
// terminal emulator. It reaches the API server over TLS only, checks the
// server's certificate before it sends anything, and authenticates with
// a bearer token, a client certificate, or a token the session's owner
// gives.
package kubernetes

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"changkun.de/x/occamy/internal/backend"
	"changkun.de/x/occamy/internal/terminal"
)

// Defaults for the fields that Config leaves zero.
const (
	DefaultPort        = "443"
	DefaultNamespace   = "default"
	DefaultCols        = 80
	DefaultRows        = 24
	DefaultDialTimeout = 15 * time.Second
)

// DefaultCommand is the command run when Config names none: bash where
// the image has it, sh otherwise, as k9s does.
var DefaultCommand = []string{"/bin/sh", "-c", "command -v bash >/dev/null && exec bash || exec sh"}

// MaxCols and MaxRows are the largest terminal the backend asks the
// server for: the largest the session's terminal emulator keeps.
const (
	MaxCols = terminal.MaxCols
	MaxRows = terminal.MaxRows
)

// inputQueue is the number of inputs that Input holds before it fails.
const inputQueue = 64

// readSize is the most output passed to Sink.TermData in one call.
const readSize = 32 << 10

// keepalive is how often the backend pings the API server, so that a
// load balancer in front of it does not drop an idle terminal. A ping
// without its pong ends nothing: TCP's keepalives find a dead peer.
var keepalive = 30 * time.Second

var (
	// ErrNotRunning is returned by Input before Run has started or after
	// it has returned.
	ErrNotRunning = errors.New("kubernetes: backend not running")
	// ErrInputFull is returned by Input when its queue is full, as when
	// the remote has stopped reading its input.
	ErrInputFull = errors.New("kubernetes: input queue full")
)

// Config describes a terminal in a container of a pod.
type Config struct {
	// Addr is the API server's host and port. The port defaults to 443.
	// The backend speaks HTTPS only.
	Addr string
	// Namespace is the pod's namespace, "default" if empty.
	Namespace string
	// Pod is the pod's name.
	Pod string
	// Container is the container's name. Empty picks the pod's default
	// container, as kubectl does.
	Container string
	// Command is the command to run, one element per argument. Empty
	// runs DefaultCommand, unless Attach is set.
	Command []string
	// Attach connects to the container's main process instead of
	// running a command, as guacd does without exec-command. The
	// container needs stdin, and tty unless NoTTY is set, in its spec.
	Attach bool
	// NoTTY runs the command without a terminal: stderr apart from
	// stdout, line feeds turned into CR LF as a terminal would, no echo
	// of input, and sizes kept to the session's own terminal.
	NoTTY bool
	// Token is a bearer token sent in the Authorization header. It must
	// be visible ASCII.
	Token Token
	// PromptToken asks the session's owner for a bearer token when Run
	// starts, if Token is empty. An empty answer sends none.
	PromptToken bool
	// CertFile and KeyFile name a client certificate and its key, in
	// PEM, for API servers that authenticate clients by certificate.
	// Both or neither.
	CertFile, KeyFile string
	// TLS is the policy for the API server's certificate.
	TLS TLS
	// Cols and Rows are the terminal's initial size in character cells.
	// They default to 80 by 24.
	Cols, Rows int
	// DialTimeout bounds the connection to the API server, from the TCP
	// dial to the WebSocket upgrade, and defaults to 15 seconds.
	DialTimeout time.Duration
	// Logger receives the backend's log. Nil means slog.Default().
	Logger *slog.Logger
}

// Backend is a terminal in a container of a pod. It implements
// backend.Backend.
type Backend struct {
	addr        string
	target      string // namespace/pod or namespace/pod/container, for messages
	url         string // the exec or attach endpoint, which holds no secret
	tty         bool
	token       Token
	promptToken bool
	client      *http.Client
	transport   *http.Transport
	cols, rows  int
	dialTimeout time.Duration
	log         *slog.Logger

	mu  sync.Mutex
	run *run // the state that Input shares with Run, nil when not running
}

var _ backend.Backend = (*Backend)(nil)

// run is the state of one Run that Input shares.
type run struct {
	input  chan []byte   // bytes for stdin
	resize chan struct{} // signals a new size, at most one pending

	mu         sync.Mutex
	cols, rows int // the newest size asked for
}

// New returns a backend for cfg. It checks the configuration and reads
// the client certificate, but does not connect.
func New(cfg Config) (*Backend, error) {
	addr, host, err := hostPort(cfg.Addr)
	if err != nil {
		return nil, err
	}
	if cfg.Namespace == "" {
		cfg.Namespace = DefaultNamespace
	}
	if err := CheckNamespace(cfg.Namespace); err != nil {
		return nil, err
	}
	if err := CheckPod(cfg.Pod); err != nil {
		return nil, err
	}
	if cfg.Container != "" {
		if err := CheckContainer(cfg.Container); err != nil {
			return nil, err
		}
	}
	switch {
	case cfg.Attach && len(cfg.Command) > 0:
		return nil, errors.New("kubernetes: attach runs no command: set Attach or Command, not both")
	case !cfg.Attach && len(cfg.Command) == 0:
		cfg.Command = DefaultCommand
	}
	if len(cfg.Command) > 0 && cfg.Command[0] == "" {
		return nil, errors.New("kubernetes: the command's first element is empty")
	}
	if err := checkToken(cfg.Token); err != nil {
		return nil, err
	}
	if cfg.Token != "" && cfg.PromptToken {
		return nil, errors.New("kubernetes: a token is configured, so none is asked for: set Token or PromptToken, not both")
	}
	if cfg.Cols == 0 {
		cfg.Cols = DefaultCols
	}
	if cfg.Rows == 0 {
		cfg.Rows = DefaultRows
	}
	if !validSize(cfg.Cols, cfg.Rows) {
		return nil, fmt.Errorf("kubernetes: invalid terminal size %dx%d", cfg.Cols, cfg.Rows)
	}
	if cfg.DialTimeout < 0 {
		return nil, fmt.Errorf("kubernetes: negative dial timeout %v", cfg.DialTimeout)
	}
	if cfg.DialTimeout == 0 {
		cfg.DialTimeout = DefaultDialTimeout
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	policy, err := newTLSPolicy(cfg.TLS, host)
	if err != nil {
		return nil, err
	}
	var cert *tls.Certificate
	switch {
	case cfg.CertFile != "" && cfg.KeyFile != "":
		c, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("kubernetes: client certificate: %w", err)
		}
		cert = &c
	case cfg.CertFile != "" || cfg.KeyFile != "":
		return nil, errors.New("kubernetes: a client certificate needs both CertFile and KeyFile")
	}
	target := cfg.Namespace + "/" + cfg.Pod
	if cfg.Container != "" {
		target += "/" + cfg.Container
	}
	tr := &http.Transport{
		// No proxy from the environment: the API server is dialled
		// directly, as the other backends dial their servers.
		Proxy:                  nil,
		DialContext:            (&net.Dialer{Timeout: cfg.DialTimeout}).DialContext,
		TLSClientConfig:        policy.config(addr, cert, cfg.Logger),
		TLSHandshakeTimeout:    cfg.DialTimeout,
		DisableKeepAlives:      true,
		DisableCompression:     true,
		MaxResponseHeaderBytes: 64 << 10,
	}
	return &Backend{
		addr:        addr,
		target:      target,
		url:         endpoint(addr, cfg.Namespace, cfg.Pod, cfg.Container, cfg.Command, cfg.Attach, !cfg.NoTTY).String(),
		tty:         !cfg.NoTTY,
		token:       cfg.Token,
		promptToken: cfg.PromptToken,
		client: &http.Client{
			Transport: tr,
			// A redirect would take the token to another URL.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		transport:   tr,
		cols:        cfg.Cols,
		rows:        cfg.Rows,
		dialTimeout: cfg.DialTimeout,
		log:         cfg.Logger,
	}, nil
}

// hostPort adds the default port to addr if it has none, and returns it
// with its host.
func hostPort(addr string) (hostport, host string, err error) {
	if addr == "" {
		return "", "", errors.New("kubernetes: no API server address")
	}
	if strings.ContainsAny(addr, "/?#@ ") {
		return "", "", fmt.Errorf("kubernetes: %q is not host:port", addr)
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		// No port; an IPv6 literal may still be in brackets.
		host, port = strings.TrimSuffix(strings.TrimPrefix(addr, "["), "]"), ""
	}
	if host == "" {
		return "", "", fmt.Errorf("kubernetes: no host in address %q", addr)
	}
	if port == "" {
		port = DefaultPort
	}
	return net.JoinHostPort(host, port), host, nil
}

func validSize(cols, rows int) bool {
	return cols >= 1 && rows >= 1 && cols <= MaxCols && rows <= MaxRows
}

// Class implements backend.Backend.
func (b *Backend) Class() backend.Class { return backend.Terminal }

// Demand implements backend.Backend. The command's output always feeds
// the session's terminal state, whether or not a viewer can see it, so
// there is nothing to pace.
func (b *Backend) Demand(backend.Demand) {}

// Input implements backend.Backend. TermInput and Text go to stdin, and
// Resize changes the terminal's size. Key, Pointer, Wheel and Clipboard
// are ignored: a viewer's terminal emulator turns keys into TermInput.
//
// Input queues what it is given and returns at once. Input that arrives
// while Run is still connecting is written once the stream is open.
func (b *Backend) Input(ev backend.InputEvent) error {
	r := b.current()
	if r == nil {
		return ErrNotRunning
	}
	switch ev := ev.(type) {
	case backend.TermInput:
		return r.queue(bytes.Clone(ev.Data))
	case backend.Text:
		return r.queue([]byte(ev.Text))
	case backend.Resize:
		return r.setSize(ev.Width, ev.Height)
	}
	return nil
}

// current returns the running Run's state, or nil.
func (b *Backend) current() *run {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.run
}

// start makes r the running Run's state, unless another runs.
func (b *Backend) start(r *run) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.run != nil {
		return errors.New("kubernetes: Run called while running")
	}
	b.run = r
	return nil
}

func (b *Backend) stop() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.run = nil
}

func (r *run) queue(p []byte) error {
	if len(p) == 0 {
		return nil
	}
	select {
	case r.input <- p:
		return nil
	default:
		return ErrInputFull
	}
}

// setSize records the newest size asked for. Sizes are not queued: only
// the newest matters.
func (r *run) setSize(cols, rows int) error {
	if !validSize(cols, rows) {
		return fmt.Errorf("kubernetes: invalid terminal size %dx%d", cols, rows)
	}
	r.storeSize(cols, rows)
	select {
	case r.resize <- struct{}{}:
	default:
	}
	return nil
}

func (r *run) storeSize(cols, rows int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cols, r.rows = cols, rows
}

func (r *run) size() (cols, rows int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cols, r.rows
}

// Run implements backend.Backend. It asks the owner for a token if it is
// to, opens the stream, and serves it until the command exits or ctx is
// done. A command that exits, with any status, ends the session
// normally; a stream that ends without a status, which is how a lost
// connection shows, is an error, and so is a status that is no exit
// status, such as a container that does not exist. Every goroutine that
// Run starts has ended when it returns.
func (b *Backend) Run(ctx context.Context, sink backend.Sink) error {
	r := &run{
		input:  make(chan []byte, inputQueue),
		resize: make(chan struct{}, 1),
		cols:   b.cols,
		rows:   b.rows,
	}
	if err := b.start(r); err != nil {
		return err
	}
	defer b.stop()
	defer b.transport.CloseIdleConnections()

	token := b.token
	if b.promptToken {
		t, err := b.askToken(ctx, sink)
		if err != nil {
			return err
		}
		token = t
	}
	conn, err := b.dial(ctx, token)
	if err != nil {
		return err
	}
	return b.serve(ctx, sink, r, conn)
}

// askToken asks the session's owner for a bearer token.
func (b *Backend) askToken(ctx context.Context, sink backend.Sink) (Token, error) {
	reply, err := sink.Prompt(ctx, backend.Prompt{
		Instruction: fmt.Sprintf("Bearer token for %s at %s", b.target, b.addr),
		Fields:      []backend.PromptField{{Label: "Token", Secret: true}},
	})
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if err != nil {
		return "", fmt.Errorf("kubernetes: prompt: %w", err)
	}
	if len(reply.Answers) != 1 {
		return "", errors.New("kubernetes: the prompt was not answered")
	}
	t := Token(strings.TrimSpace(reply.Answers[0]))
	if err := checkToken(t); err != nil {
		return "", err
	}
	return t, nil
}

// dial opens the WebSocket. The certificate is checked during the TLS
// handshake, before the request, and so the token, is written.
func (b *Backend) dial(ctx context.Context, token Token) (*websocket.Conn, error) {
	hctx, cancel := context.WithTimeout(ctx, b.dialTimeout)
	defer cancel()
	header := http.Header{}
	if token != "" {
		header.Set("Authorization", "Bearer "+string(token))
	}
	conn, resp, err := websocket.Dial(hctx, b.url, &websocket.DialOptions{ //nolint:bodyclose // coder/websocket: the response body need not be closed
		HTTPClient:      b.client,
		HTTPHeader:      header,
		Subprotocols:    []string{ProtocolV5, ProtocolV4},
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if resp != nil && resp.StatusCode != http.StatusSwitchingProtocols {
			return nil, refused(resp)
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, fmt.Errorf("kubernetes: no stream from %s within %v", b.addr, b.dialTimeout)
		}
		return nil, fmt.Errorf("kubernetes: open a stream to %s at %s: %w", b.target, b.addr, err)
	}
	switch p := conn.Subprotocol(); p {
	case ProtocolV5, ProtocolV4:
		return conn, nil
	default:
		_ = conn.CloseNow()
		return nil, fmt.Errorf("kubernetes: the API server at %s chose the subprotocol %s, not %s or %s", b.addr, quote(p), ProtocolV5, ProtocolV4)
	}
}

// refused is the error for an upgrade the API server answered with resp:
// the status code, and the reason and message of the metav1.Status in
// its body if there is one. websocket.Dial has read at most 1 KiB of the
// body.
func refused(resp *http.Response) error {
	e := &StatusError{HTTPStatus: resp.StatusCode}
	if resp.Body != nil {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxStatus))
		var s status
		if json.Unmarshal(body, &s) == nil {
			e.Reason, e.Message = s.Reason, s.Message
		}
	}
	if e.Reason == "" {
		e.Reason = http.StatusText(resp.StatusCode)
	}
	return e
}

// serve copies between the stream and the session until the command
// exits or ctx is done.
func (b *Backend) serve(ctx context.Context, sink backend.Sink, r *run, conn *websocket.Conn) (err error) {
	conn.SetReadLimit(maxMessage)
	v5 := conn.Subprotocol() == ProtocolV5
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// A Resize may have arrived while connecting. Take the signal before
	// the size, so that a Resize between the two is applied below.
	select {
	case <-r.resize:
	default:
	}
	cols, rows := r.size()
	if b.tty {
		if err := conn.Write(wctx, websocket.MessageBinary, resizeMessage(cols, rows)); err != nil {
			_ = conn.CloseNow()
			return failure(ctx, fmt.Errorf("send the terminal's size: %w", err))
		}
	}
	sink.Resize(cols, rows)

	done := make(chan struct{})
	ended := make(chan error, 1)
	// The goroutines run the terminal emulator on what the container
	// prints, which anyone with a shell there can make anything, beyond
	// the session's recover: each recovers its own panic, the first of
	// which stops the others and ends the session.
	const goroutines = 3
	panicked := make(chan error, goroutines)
	safe := func(f func()) func() {
		return func() {
			defer func() {
				if perr := backend.Recovered(recover()); perr != nil {
					panicked <- perr
				}
			}()
			f()
		}
	}
	var reader, others sync.WaitGroup
	reader.Go(safe(func() { ended <- b.read(conn, sink) }))
	others.Go(safe(func() { r.write(wctx, conn, sink, b.tty, done) }))
	others.Go(safe(func() { ping(wctx, conn, done) }))

	select {
	case <-ctx.Done():
		err = ctx.Err()
		if v5 {
			// Close stdin, so that a shell the server keeps exits.
			cctx, ccancel := context.WithTimeout(context.Background(), time.Second)
			_ = conn.Write(cctx, websocket.MessageBinary, []byte{chanClose, chanStdin})
			ccancel()
		}
	case err = <-ended:
		// coder/websocket closes the connection when the context of a
		// write or a ping is done, which ends the reader too, so that
		// its error may come before ctx's. The cancel is then why.
		if err != nil && ctx.Err() != nil {
			err = ctx.Err()
		}
	case err = <-panicked:
	}
	close(done)
	cancel()
	_ = conn.CloseNow()
	reader.Wait()
	others.Wait()
	if !errors.As(err, new(backend.PanicError)) {
		select {
		case err = <-panicked:
		default:
		}
	}
	return err
}

// read reads the stream until it ends, feeding output to sink, and
// returns Run's result: nil for a command that exited, an error
// otherwise.
func (b *Backend) read(conn *websocket.Conn, sink backend.Sink) error {
	buf := make([]byte, readSize)
	var crlf []byte
	out := sink.TermData
	if !b.tty {
		out = func(p []byte) {
			crlf = onlcr(crlf[:0], p)
			sink.TermData(crlf)
		}
	}
	var status bytes.Buffer
	for {
		typ, msg, err := conn.Reader(context.Background())
		if err != nil {
			return b.ended(status.Bytes(), err)
		}
		if typ != websocket.MessageBinary {
			return errors.New("kubernetes: the API server sent a text message; the channel protocols send binary ones")
		}
		if err := demux(msg, buf, out, &status); err != nil {
			if errors.Is(err, errStatusTooLarge) {
				return err
			}
			return fmt.Errorf("kubernetes: reading the stream: %w", err)
		}
	}
}

// ended is Run's result for a stream that ended with err, after the
// error channel carried st.
func (b *Backend) ended(st []byte, err error) error {
	if len(st) == 0 {
		return fmt.Errorf("kubernetes: the stream ended without an exit status: %w", err)
	}
	code, serr := parseStatus(st)
	if serr != nil {
		return serr
	}
	b.log.Debug("kubernetes: command exited", "target", b.target, "status", code)
	return nil
}

// failure returns ctx's error if ctx is done, since closing the
// connection then made the step fail, and err otherwise.
func failure(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return fmt.Errorf("kubernetes: %w", err)
}

// write sends queued input to stdin, and each new size to the resize
// channel before it reports it to sink, until done is closed or a write
// fails. Without a TTY a size goes to sink alone.
func (r *run) write(ctx context.Context, conn *websocket.Conn, sink backend.Sink, tty bool, done <-chan struct{}) {
	var msg []byte
	for {
		select {
		case <-done:
			return
		case p := <-r.input:
			msg = append(append(msg[:0], chanStdin), p...)
			if err := conn.Write(ctx, websocket.MessageBinary, msg); err != nil {
				return
			}
		case <-r.resize:
			cols, rows := r.size()
			if tty {
				if err := conn.Write(ctx, websocket.MessageBinary, resizeMessage(cols, rows)); err != nil {
					return
				}
			}
			sink.Resize(cols, rows)
		}
	}
}

// ping pings the API server every keepalive until done is closed. A ping
// that gets no pong waits for it, or for ctx.
func ping(ctx context.Context, conn *websocket.Conn, done <-chan struct{}) {
	t := time.NewTicker(keepalive)
	defer t.Stop()
	for {
		select {
		case <-done:
			return
		case <-t.C:
			if err := conn.Ping(ctx); err != nil {
				return
			}
		}
	}
}
