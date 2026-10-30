// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// Package rdp is a graphical backend that connects to an RDP server with
// the pure-Go client of internal/rdp and draws the remote desktop into
// the session's framebuffer
// (specs/008-native-architecture/native-rdp-backend.md).
//
// It connects over TLS, with NLA (CredSSP with NTLMv2) by default, and
// asks the session's owner for the credentials NLA needs that its
// configuration lacks. It draws the bitmap updates the client decodes,
// interleaved RLE and planar, into the framebuffer in strips of rows,
// and the server's pointer shapes and moves into the session's cursor.
// Viewers' keys go out as scancodes, by their physical key where the
// viewer knows it, and text as Unicode keyboard events; the pointer and
// the wheel as mouse events. While no viewer can see the screen, the
// server is told to suppress its output.
//
// Every length the server sends is bounded by the client before it is
// read or allocated, and the client decodes bitmaps and pointer updates
// at a bounded rate of pixels (internal/rdp).
package rdp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"changkun.de/x/occamy/internal/backend"
	"changkun.de/x/occamy/internal/rdp"
)

// Defaults for the fields that Config leaves zero.
const (
	DefaultPort        = "3389"
	DefaultDialTimeout = 15 * time.Second
	DefaultWidth       = 1024
	DefaultHeight      = 768
)

// The security modes of Config.Security, named as guacd names them.
const (
	// SecurityNLA offers CredSSP, with and without the early user
	// authorization result (HYBRID and HYBRID_EX). It is the default.
	SecurityNLA = "nla"
	// SecurityNLAExt offers CredSSP with the early user authorization
	// result only (HYBRID_EX).
	SecurityNLAExt = "nla-ext"
	// SecurityTLS offers TLS without NLA, which sends the password in the
	// Client Info PDU to whichever server passed the certificate check.
	// xrdp supports nothing else.
	SecurityTLS = "tls"
	// SecurityAny offers TLS and NLA, and lets the server choose.
	SecurityAny = "any"
)

const (
	// inputQueue is the number of inputs that wait to be written before
	// Input fails.
	inputQueue = 256
	// inputEvents bounds the RDP input events that the queued inputs
	// make, counted when Input queues them by the most an input can make:
	// four for a character of Text, so a Text of up to 65,536
	// characters fits. Each event is at most 12 bytes on the wire.
	inputEvents = 1 << 18
	// maxBatch is the most input events written at once.
	maxBatch = 1024
)

var (
	// ErrNotRunning is returned by Input before Run has started or after
	// it has returned.
	ErrNotRunning = errors.New("rdp: backend not running")
	// ErrInputFull is returned by Input when too many inputs, or inputs
	// of too many events, wait to be written, as when the server has
	// stopped reading.
	ErrInputFull = errors.New("rdp: input queue full")
	// ErrUnsupportedInput is returned by Input for input a graphical
	// backend does not take, such as backend.TermInput.
	ErrUnsupportedInput = errors.New("rdp: unsupported input")
	// ErrAuthFailed is wrapped by Run's error when the server refuses
	// the credentials in NLA.
	ErrAuthFailed = errors.New("rdp: authentication failed")
	// ErrConnectionClosed is wrapped by Run's error when the server
	// closes the connection without ending the session.
	ErrConnectionClosed = errors.New("rdp: the server closed the connection without ending the session")
)

// Config describes an RDP connection.
type Config struct {
	// Addr is the server's host and TCP port; the port defaults to 3389.
	Addr string
	// Domain, Username and Password log on. NLA needs a username and a
	// password: if the configuration lacks them and the security mode
	// offers NLA, the session's owner is asked before the connection.
	// Without NLA, the server shows its logon screen if they are empty.
	Domain, Username, Password string
	// Width and Height are the desktop size to ask for at connect,
	// 1024×768 by default; the server may choose another. Each is at
	// least 200 and at most 8192, and the area at most 2^25 pixels.
	Width, Height int
	// ColorDepth is 15, 16, 24 or 32 bits per pixel; 32 by default.
	ColorDepth int
	// Security is the security mode: SecurityNLA (the default),
	// SecurityNLAExt, SecurityTLS or SecurityAny. Standard RDP security
	// is not supported.
	Security string
	// TLS is the policy for the server's certificate, shared in meaning
	// with the VNC backend's. Its ServerName defaults to Addr's host.
	TLS rdp.TLS
	// KeyboardLayout is the input locale identifier of the keyboard
	// layout the server maps scancodes through; US English (0x409) by
	// default.
	KeyboardLayout uint32
	// ClientName is the client's name the server sees; "occamy" by
	// default.
	ClientName string
	// DialTimeout bounds the TCP connection and defaults to 15 seconds.
	// Only Run's context bounds the rest, since it may wait for the owner
	// to answer a prompt.
	DialTimeout time.Duration
	// Logger receives the backend's log. Nil means slog.Default().
	Logger *slog.Logger
}

// Backend is a connection to one RDP server. It implements
// backend.Backend.
type Backend struct {
	addr        string
	cfg         rdp.Config // without the logger's default
	dialTimeout time.Duration
	log         *slog.Logger
	// dial connects to the server; tests replace it.
	dial func(ctx context.Context) (net.Conn, error)

	// visible is the latest Demand. It outlives a Run, since the session
	// reports only changes and may report one before Run starts.
	visible atomic.Bool

	mu  sync.Mutex
	run *run // the state that Input shares with Run, nil when not running
}

var _ backend.Backend = (*Backend)(nil)

// New returns a backend for cfg. It checks the configuration, but does
// not connect.
func New(cfg Config) (*Backend, error) {
	addr, err := hostPort(cfg.Addr)
	if err != nil {
		return nil, err
	}
	host, _, _ := net.SplitHostPort(addr)
	if cfg.DialTimeout < 0 {
		return nil, fmt.Errorf("rdp: negative dial timeout %v", cfg.DialTimeout)
	}
	if cfg.DialTimeout == 0 {
		cfg.DialTimeout = DefaultDialTimeout
	}
	if cfg.Width == 0 && cfg.Height == 0 {
		cfg.Width, cfg.Height = DefaultWidth, DefaultHeight
	}
	if cfg.Width < 200 || cfg.Height < 200 || cfg.Width > 8192 || cfg.Height > 8192 || cfg.Width*cfg.Height > 1<<25 {
		return nil, fmt.Errorf("rdp: desktop size %d×%d out of range", cfg.Width, cfg.Height)
	}
	switch cfg.ColorDepth {
	case 0, 15, 16, 24, 32:
	default:
		return nil, fmt.Errorf("rdp: colour depth %d: want 15, 16, 24 or 32", cfg.ColorDepth)
	}
	protocols, err := securityProtocols(cfg.Security)
	if err != nil {
		return nil, err
	}
	if cfg.TLS.ServerName == "" {
		cfg.TLS.ServerName = host
	}
	if err := cfg.TLS.Check(host); err != nil {
		return nil, err
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	b := &Backend{
		addr: addr,
		cfg: rdp.Config{
			Width: cfg.Width, Height: cfg.Height,
			ColorDepth: cfg.ColorDepth,
			Domain:     cfg.Domain, Username: cfg.Username, Password: cfg.Password,
			ClientName:     cfg.ClientName,
			KeyboardLayout: cfg.KeyboardLayout,
			Protocols:      protocols,
			TLS:            cfg.TLS,
			Logger:         cfg.Logger,
		},
		dialTimeout: cfg.DialTimeout,
		log:         cfg.Logger,
	}
	b.dial = func(ctx context.Context) (net.Conn, error) {
		d := net.Dialer{Timeout: b.dialTimeout}
		return d.DialContext(ctx, "tcp", b.addr)
	}
	return b, nil
}

// securityProtocols returns the protocols a security mode offers.
func securityProtocols(mode string) (uint32, error) {
	switch mode {
	case "", SecurityNLA:
		return rdp.ProtocolHybrid | rdp.ProtocolHybridEx, nil
	case SecurityNLAExt:
		return rdp.ProtocolHybridEx, nil
	case SecurityTLS:
		return rdp.ProtocolSSL, nil
	case SecurityAny:
		return rdp.ProtocolSSL | rdp.ProtocolHybrid | rdp.ProtocolHybridEx, nil
	}
	return 0, fmt.Errorf("rdp: security mode %q: want %q, %q, %q or %q", mode, SecurityNLA, SecurityNLAExt, SecurityTLS, SecurityAny)
}

// hostPort adds the default port to addr if it has none.
func hostPort(addr string) (string, error) {
	if addr == "" {
		return "", errors.New("rdp: no address")
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		// No port; an IPv6 literal may still be in brackets.
		host, port = strings.TrimSuffix(strings.TrimPrefix(addr, "["), "]"), ""
	}
	if host == "" {
		return "", fmt.Errorf("rdp: no host in address %q", addr)
	}
	if port == "" {
		port = DefaultPort
	}
	return net.JoinHostPort(host, port), nil
}

// Class implements backend.Backend.
func (b *Backend) Class() backend.Class { return backend.Graphical }

// Demand implements backend.Backend. While nobody can see the screen,
// the server is told to suppress its output, with the Suppress Output
// PDU; when a viewer can see it again, the server is told to resume and
// redraws the desktop. A Demand that comes while Run connects is sent
// once the connection is up.
func (b *Backend) Demand(d backend.Demand) {
	b.visible.Store(d.Visible)
	b.mu.Lock()
	r := b.run
	b.mu.Unlock()
	if r != nil {
		r.poke()
	}
}

// Input implements backend.Backend. Key becomes a keyboard event by
// scancode, Text Unicode keyboard events, Pointer and Wheel mouse
// events (see input.go). Clipboard and Resize are ignored until the
// backend speaks the clipboard and display control channels, so viewers
// scale the remote desktop. TermInput fails with ErrUnsupportedInput.
//
// Input queues what it is given and returns at once. Input that arrives
// while Run is still connecting is written once the connection is up.
// The queue holds 256 inputs of at most 2^18 events, so a single Text
// of more than 65,536 characters fails with ErrInputFull.
func (b *Backend) Input(ev backend.InputEvent) error {
	b.mu.Lock()
	r := b.run
	b.mu.Unlock()
	if r == nil {
		return ErrNotRunning
	}
	n, err := cost(ev)
	if err != nil || n == 0 {
		return err
	}
	return r.send(ev, n)
}

// run is the state of one Run that Input and Demand share.
type run struct {
	queue  chan queued   // inputs from Input, for the writer
	queued atomic.Int64  // the events that the inputs in queue may make
	kick   chan struct{} // asks the writer to reconsider the demand

	// Owned by the writer.
	input inputState
}

// queued is an input in the queue, with the events it may make.
type queued struct {
	ev backend.InputEvent
	n  int
}

func newRun() *run {
	return &run{
		queue: make(chan queued, inputQueue),
		kick:  make(chan struct{}, 1),
	}
}

// send queues ev, which may make n events, unless the queue is full.
// Input calls it one call at a time, and the writer only lowers queued,
// so the check and the addition need no lock.
func (r *run) send(ev backend.InputEvent, n int) error {
	if r.queued.Load()+int64(n) > inputEvents {
		return ErrInputFull
	}
	r.queued.Add(int64(n))
	select {
	case r.queue <- queued{ev: ev, n: n}:
		return nil
	default:
		r.queued.Add(-int64(n))
		return ErrInputFull
	}
}

// poke asks the writer to look at the demand.
func (r *run) poke() {
	select {
	case r.kick <- struct{}{}:
	default:
	}
}

// Run implements backend.Backend. It asks the owner for credentials if
// NLA needs them and the configuration lacks them, connects, and draws
// the server's updates until ctx is done or the server ends the
// session. Every goroutine that Run starts has ended when it returns.
//
// It returns nil when the server ends the session on purpose: when the
// user logs off or disconnects, when an administrator does, when a
// session time limit runs out, or when another connection takes the
// session over. The last four are logged with their reason. Other
// reasons the server gives are errors that wrap rdp.ErrorInfo; a
// refused NLA logon wraps ErrAuthFailed, and a connection that closes
// without a reason wraps ErrConnectionClosed.
func (b *Backend) Run(ctx context.Context, sink backend.Sink) error {
	r := newRun()
	b.mu.Lock()
	if b.run != nil {
		b.mu.Unlock()
		return errors.New("rdp: Run called while running")
	}
	b.run = r
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		b.run = nil
		b.mu.Unlock()
	}()

	cfg, err := b.credentials(ctx, sink)
	if err != nil {
		return failure(ctx, err)
	}
	nc, err := b.dial(ctx)
	if err != nil {
		return failure(ctx, fmt.Errorf("rdp: %w", err))
	}
	c, err := rdp.Connect(ctx, nc, cfg) // closes nc on an error
	if err != nil {
		var ce *rdp.CredSSPError
		if errors.As(err, &ce) {
			err = fmt.Errorf("%w: %w", ErrAuthFailed, err)
		}
		return failure(ctx, err)
	}
	w, h := c.Size()
	b.log.Debug("rdp: connected", "addr", b.addr, "protocol", c.Protocol(), "width", w, "height", h,
		"unicode", c.UnicodeInput())

	done := make(chan struct{})
	var werr error
	var wg sync.WaitGroup
	wg.Go(func() {
		if werr = r.writeLoop(c, done, &b.visible); werr != nil {
			c.Close() // stops Run too
		}
	})
	err = c.Run(ctx, sink)
	close(done)
	c.Close()
	wg.Wait()
	return b.end(ctx, err, werr)
}

// end returns Run's result for the client's, and the writer's error.
func (b *Backend) end(ctx context.Context, err, werr error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil && werr != nil {
		// The failed write closed the connection under the reader.
		return werr
	}
	var info rdp.ErrorInfo
	switch {
	case err == nil:
		return nil
	case errors.As(err, &info) && remoteEnded(info):
		b.log.Info("rdp: the server ended the session", "addr", b.addr, "reason", info.Error())
		return nil
	case rdp.HungUp(err):
		return fmt.Errorf("%w: %w", ErrConnectionClosed, err)
	}
	return err
}

// remoteEnded reports whether the reason a server gave for ending the
// session is a deliberate end rather than a failure: an administrator's
// disconnect or logoff, the idle and active session time limits, and
// another connection taking the session over ([MS-RDPBCGR] 2.2.5.1.1).
// The client itself returns nil for no reason and for the user's own
// disconnect or logoff.
func remoteEnded(info rdp.ErrorInfo) bool {
	return info >= 0x1 && info <= 0x5
}

// failure returns ctx's error if ctx is done, since closing the
// connection then made the step fail, and err otherwise.
func failure(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

// credentials returns the client's configuration with the credentials
// NLA needs, asking the owner for those the configuration lacks: the
// username, the domain if neither it nor the username is configured,
// and the password, as guacd asks for them. Without NLA in the security
// mode nothing is asked, and empty credentials leave the server's logon
// screen to the user.
func (b *Backend) credentials(ctx context.Context, sink backend.Sink) (rdp.Config, error) {
	cfg := b.cfg
	if cfg.Protocols&(rdp.ProtocolHybrid|rdp.ProtocolHybridEx) == 0 || cfg.Username != "" && cfg.Password != "" {
		return cfg, nil
	}
	var fields []backend.PromptField
	var names []string
	var into []*string
	ask := func(label string, secret bool, s *string) {
		fields = append(fields, backend.PromptField{Label: label, Secret: secret})
		names = append(names, strings.ToLower(label))
		into = append(into, s)
	}
	if cfg.Username == "" {
		ask("Username", false, &cfg.Username)
		if cfg.Domain == "" {
			ask("Domain", false, &cfg.Domain)
		}
	}
	if cfg.Password == "" {
		ask("Password", true, &cfg.Password)
	}
	what := names[len(names)-1]
	if len(names) > 1 {
		what = strings.Join(names[:len(names)-1], ", ") + " and " + what
	}
	reply, err := sink.Prompt(ctx, backend.Prompt{
		Instruction: "RDP " + what + " for " + b.addr,
		Fields:      fields,
	})
	if err != nil {
		return cfg, fmt.Errorf("rdp: prompt: %w", err)
	}
	if len(reply.Answers) != len(fields) {
		return cfg, fmt.Errorf("rdp: prompt: %d answers for %d questions", len(reply.Answers), len(fields))
	}
	for i, s := range into {
		*s = reply.Answers[i]
	}
	return cfg, nil
}

// writeLoop writes queued input and what the demand asks of the server,
// until done is closed or a write fails. Input that is ready is written
// at once: up to maxBatch events, in input PDUs of up to 255 events.
func (r *run) writeLoop(c *rdp.Client, done <-chan struct{}, visible *atomic.Bool) error {
	shown := true // the server sends updates until told otherwise
	batch := make([]rdp.InputEvent, 0, maxBatch)
	r.poke() // Demand may have come before the connection was up
	for {
		select {
		case <-done:
			return nil
		case q := <-r.queue:
			batch = r.take(batch, c, q)
		case <-r.kick:
		}
	more:
		for len(batch) < maxBatch {
			select {
			case q := <-r.queue:
				batch = r.take(batch, c, q)
			case <-r.kick:
			default:
				break more
			}
		}
		if len(batch) > 0 {
			if err := c.SendInput(batch...); err != nil {
				return fmt.Errorf("rdp: write: %w", err)
			}
			clear(batch)
			batch = batch[:0]
		}
		if v := visible.Load(); v != shown {
			if err := c.SuppressOutput(!v); err != nil {
				return fmt.Errorf("rdp: write: %w", err)
			}
			shown = v
		}
	}
}

// take appends the events of a queued input, which then leaves the
// queue's budget.
func (r *run) take(batch []rdp.InputEvent, c *rdp.Client, q queued) []rdp.InputEvent {
	batch = r.input.events(batch, capabilities{unicode: c.UnicodeInput(), mouseX: c.MouseXInput()}, q.ev)
	r.queued.Add(-int64(q.n))
	return batch
}
