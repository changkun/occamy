// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// Package ssh is a terminal backend that connects to an SSH server, starts
// a shell in a pseudo-terminal, and feeds the shell's output to the
// session's terminal emulator. It accepts only pinned host keys, unless
// told to ignore them, and asks the session's owner for the answers that
// its configuration does not hold.
package ssh

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	gossh "golang.org/x/crypto/ssh"

	"changkun.de/x/occamy/internal/backend"
	"changkun.de/x/occamy/internal/terminal"
)

// Defaults for the fields that Config leaves zero.
const (
	DefaultPort        = "22"
	DefaultTerm        = "xterm-256color"
	DefaultCols        = 80
	DefaultRows        = 24
	DefaultDialTimeout = 15 * time.Second
)

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

var (
	// ErrNotRunning is returned by Input before Run has started or after
	// it has returned.
	ErrNotRunning = errors.New("ssh: backend not running")
	// ErrInputFull is returned by Input when its queue is full, as when
	// the remote has stopped reading its input.
	ErrInputFull = errors.New("ssh: input queue full")
)

// Config describes an SSH connection.
type Config struct {
	// Addr is the server's host and port. The port defaults to 22.
	Addr string
	// User is the user to log in as.
	User string
	// Password, if set, is tried after the keys, by the password method
	// or as the answer to a keyboard-interactive challenge that asks a
	// single hidden question, once per connection. If it is empty and the
	// server accepts passwords, the session's owner is asked for one.
	Password string
	// KeyFiles are paths to private keys, tried first and in order. Keys
	// protected by a passphrase are not supported yet.
	KeyFiles []string
	// HostKeys are the host keys accepted from the server. Each is either
	// a public key in authorized_keys format, such as
	// "ssh-ed25519 AAAA...", or a SHA-256 fingerprint as ssh-keygen -l
	// prints it, such as "SHA256:...".
	HostKeys []string
	// InsecureIgnoreHostKey accepts any host key and logs a warning with
	// its fingerprint. It is meant for demos.
	InsecureIgnoreHostKey bool
	// Term is the pseudo-terminal's TERM. It defaults to xterm-256color.
	Term string
	// Cols and Rows are the pseudo-terminal's initial size in character
	// cells. They default to 80 by 24.
	Cols, Rows int
	// DialTimeout bounds the TCP connection and defaults to 15 seconds.
	// Only Run's context bounds the SSH handshake that follows, since it
	// may wait for the owner to answer a prompt.
	DialTimeout time.Duration
	// Logger receives the backend's log. Nil means slog.Default().
	Logger *slog.Logger
}

// Backend is an SSH connection to one shell. It implements
// backend.Backend.
type Backend struct {
	addr        string
	user        string
	password    string
	signers     []gossh.Signer
	pins        hostKeys
	term        string
	cols, rows  int
	dialTimeout time.Duration
	log         *slog.Logger

	mu  sync.Mutex
	run *run // the state that Input shares with Run, nil when not running
}

var _ backend.Backend = (*Backend)(nil)

// run is the state of one Run that Input shares.
type run struct {
	input  chan []byte   // bytes for the shell's stdin
	resize chan struct{} // signals a new size, at most one pending
	warn   sync.Once     // warns once about an unverified host key

	mu         sync.Mutex
	cols, rows int // the newest size asked for
}

// New returns a backend for cfg. It reads the key files and checks the
// configuration, but does not connect.
func New(cfg Config) (*Backend, error) {
	addr, err := hostPort(cfg.Addr)
	if err != nil {
		return nil, err
	}
	if cfg.User == "" {
		return nil, errors.New("ssh: no user")
	}
	pins, err := parseHostKeys(cfg.HostKeys, cfg.InsecureIgnoreHostKey)
	if err != nil {
		return nil, err
	}
	if cfg.Term == "" {
		cfg.Term = DefaultTerm
	}
	if cfg.Cols == 0 {
		cfg.Cols = DefaultCols
	}
	if cfg.Rows == 0 {
		cfg.Rows = DefaultRows
	}
	if !validSize(cfg.Cols, cfg.Rows) {
		return nil, fmt.Errorf("ssh: invalid terminal size %dx%d", cfg.Cols, cfg.Rows)
	}
	if cfg.DialTimeout < 0 {
		return nil, fmt.Errorf("ssh: negative dial timeout %v", cfg.DialTimeout)
	}
	if cfg.DialTimeout == 0 {
		cfg.DialTimeout = DefaultDialTimeout
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	signers, err := loadKeys(cfg.KeyFiles)
	if err != nil {
		return nil, err
	}
	return &Backend{
		addr:        addr,
		user:        cfg.User,
		password:    cfg.Password,
		signers:     signers,
		pins:        pins,
		term:        cfg.Term,
		cols:        cfg.Cols,
		rows:        cfg.Rows,
		dialTimeout: cfg.DialTimeout,
		log:         cfg.Logger,
	}, nil
}

// hostPort adds the default port to addr if it has none.
func hostPort(addr string) (string, error) {
	if addr == "" {
		return "", errors.New("ssh: no address")
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		// No port; an IPv6 literal may still be in brackets.
		host, port = strings.TrimSuffix(strings.TrimPrefix(addr, "["), "]"), ""
	}
	if host == "" {
		return "", fmt.Errorf("ssh: no host in address %q", addr)
	}
	if port == "" {
		port = DefaultPort
	}
	return net.JoinHostPort(host, port), nil
}

func validSize(cols, rows int) bool {
	return cols >= 1 && rows >= 1 && cols <= MaxCols && rows <= MaxRows
}

// loadKeys reads and parses the private keys at paths.
func loadKeys(paths []string) ([]gossh.Signer, error) {
	var signers []gossh.Signer
	for _, path := range paths {
		pem, err := os.ReadFile(path) // #nosec G304 -- key paths come from the administrator's configuration
		if err != nil {
			return nil, fmt.Errorf("ssh: %w", err)
		}
		signer, err := gossh.ParsePrivateKey(pem)
		clear(pem)
		if _, ok := errors.AsType[*gossh.PassphraseMissingError](err); ok {
			return nil, fmt.Errorf("ssh: key file %s is protected by a passphrase, which is not supported yet", path)
		}
		if err != nil {
			return nil, fmt.Errorf("ssh: key file %s: %w", path, err)
		}
		signers = append(signers, signer)
	}
	return signers, nil
}

// Class implements backend.Backend.
func (b *Backend) Class() backend.Class { return backend.Terminal }

// Demand implements backend.Backend. The shell's output always feeds the
// session's terminal state, whether or not a viewer can see it, so SSH
// has nothing to pace.
func (b *Backend) Demand(backend.Demand) {}

// Input implements backend.Backend. TermInput and Text go to the shell's
// stdin, and Resize changes the pseudo-terminal's size. Key, Pointer,
// Wheel and Clipboard are ignored: a viewer's terminal emulator turns
// keys into TermInput.
//
// Input queues what it is given and returns at once. Input that arrives
// while Run is still connecting is written once the shell has started.
func (b *Backend) Input(ev backend.InputEvent) error {
	b.mu.Lock()
	r := b.run
	b.mu.Unlock()
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
		return fmt.Errorf("ssh: invalid terminal size %dx%d", cols, rows)
	}
	r.mu.Lock()
	r.cols, r.rows = cols, rows
	r.mu.Unlock()
	select {
	case r.resize <- struct{}{}:
	default:
	}
	return nil
}

func (r *run) size() (cols, rows int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cols, r.rows
}

// Run implements backend.Backend. It connects, authenticates, starts a
// shell in a pseudo-terminal, and serves it until the shell exits or ctx
// is done. A shell that exits, with any status, ends the session
// normally; a session channel that closes without an exit status, which
// is how a lost connection shows, is an error. Every goroutine that Run
// starts has ended when it returns.
func (b *Backend) Run(ctx context.Context, sink backend.Sink) error {
	r := &run{
		input:  make(chan []byte, inputQueue),
		resize: make(chan struct{}, 1),
		cols:   b.cols,
		rows:   b.rows,
	}
	b.mu.Lock()
	if b.run != nil {
		b.mu.Unlock()
		return errors.New("ssh: Run called while running")
	}
	b.run = r
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		b.run = nil
		b.mu.Unlock()
	}()
	return b.connect(ctx, sink, r)
}

// connect dials and authenticates, then serves the shell.
func (b *Backend) connect(ctx context.Context, sink backend.Sink, r *run) error {
	d := net.Dialer{Timeout: b.dialTimeout}
	nc, err := d.DialContext(ctx, "tcp", b.addr)
	if err != nil {
		return failure(ctx, err)
	}
	// Closing the connection is the only way to abort the handshake, and
	// it unblocks every read and write after it.
	closed := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		nc.Close()
		close(closed)
	})
	defer func() {
		if !stop() {
			<-closed
		}
	}()

	conn, chans, reqs, err := gossh.NewClientConn(nc, b.addr, b.clientConfig(ctx, sink, r))
	if err != nil {
		return failure(ctx, fmt.Errorf("connect to %s: %w", b.addr, err))
	}
	// The client rejects channels that the server opens, such as agent or
	// X11 forwarding, and declines its global requests.
	client := gossh.NewClient(conn, chans, reqs)
	err = b.serve(ctx, sink, r, client)
	client.Close()
	// Wait returns once the connection's reader has stopped. Its error
	// tells nothing that serve's does not.
	_ = client.Wait()
	return err
}

// serve starts the shell and copies between it and the session until the
// shell exits or ctx is done.
func (b *Backend) serve(ctx context.Context, sink backend.Sink, r *run, client *gossh.Client) error {
	sess, err := client.NewSession()
	if err != nil {
		return failure(ctx, fmt.Errorf("open a session: %w", err))
	}
	// The pipes are the channel itself, so the session starts no
	// goroutines of its own to copy them.
	stdin, err := sess.StdinPipe()
	if err != nil {
		return failure(ctx, err)
	}
	stdout, err := sess.StdoutPipe()
	if err != nil {
		return failure(ctx, err)
	}
	stderr, err := sess.StderrPipe()
	if err != nil {
		return failure(ctx, err)
	}
	// A Resize may have arrived while connecting. Take the signal before
	// the size, so that a Resize between the two is applied below.
	select {
	case <-r.resize:
	default:
	}
	cols, rows := r.size()
	if err := sess.RequestPty(b.term, rows, cols, gossh.TerminalModes{gossh.IUTF8: 1}); err != nil {
		return failure(ctx, fmt.Errorf("request a terminal: %w", err))
	}
	if err := sess.Shell(); err != nil {
		return failure(ctx, fmt.Errorf("start a shell: %w", err))
	}
	sink.Resize(cols, rows)

	done := make(chan struct{})
	exited := make(chan error, 1)
	var readers, others sync.WaitGroup
	readers.Go(func() { pump(stdout, sink) })
	readers.Go(func() { pump(stderr, sink) })
	others.Go(func() { r.writeInput(stdin, done) })
	others.Go(func() { r.applySizes(sess, sink, done) })
	others.Go(func() { exited <- sess.Wait() })

	select {
	case <-ctx.Done():
		err = ctx.Err()
	case werr := <-exited:
		err = b.exitResult(ctx, werr)
		// The channel has closed, so the readers stop once they have
		// passed on the shell's last output.
		readers.Wait()
	}
	close(done)
	client.Close()
	readers.Wait()
	others.Wait()
	return err
}

// exitResult is Run's result for the error from Session.Wait.
func (b *Backend) exitResult(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err == nil {
		b.log.Debug("ssh: shell exited", "addr", b.addr, "status", 0)
		return nil
	}
	if exit, ok := errors.AsType[*gossh.ExitError](err); ok {
		// The status is the shell's last command's, not the session's.
		b.log.Debug("ssh: shell exited", "addr", b.addr, "status", exit.ExitStatus(), "signal", exit.Signal())
		return nil
	}
	return fmt.Errorf("ssh: session ended: %w", err)
}

// failure returns ctx's error if ctx is done, since closing the
// connection then made the step fail, and err otherwise.
func failure(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return fmt.Errorf("ssh: %w", err)
}

// pump feeds what r yields to sink until r ends.
func pump(r io.Reader, sink backend.Sink) {
	buf := make([]byte, readSize)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			sink.TermData(buf[:n])
		}
		if err != nil {
			return
		}
	}
}

// writeInput writes queued input to w until done is closed or a write
// fails.
func (r *run) writeInput(w io.Writer, done <-chan struct{}) {
	for {
		select {
		case <-done:
			return
		case p := <-r.input:
			if _, err := w.Write(p); err != nil {
				return
			}
		}
	}
}

// applySizes sends each new size to the server and then reports it to
// sink, until done is closed or sending fails. It runs apart from
// writeInput, since window-change is not held back when the remote stops
// reading its input.
func (r *run) applySizes(sess *gossh.Session, sink backend.Sink, done <-chan struct{}) {
	for {
		select {
		case <-done:
			return
		case <-r.resize:
			cols, rows := r.size()
			if err := sess.WindowChange(rows, cols); err != nil {
				return
			}
			sink.Resize(cols, rows)
		}
	}
}
