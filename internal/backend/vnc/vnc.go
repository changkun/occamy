// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// Package vnc is a graphical backend that connects to a VNC server with
// a pure-Go RFB client (RFC 6143) and draws the remote screen into the
// session's framebuffer
// (specs/008-native-architecture/native-vnc-backend.md). It asks for
// screen updates only while a viewer can see the screen, and asks the
// session's owner for a password that its configuration lacks.
//
// It speaks RFB 3.3, 3.7 and 3.8 with the security types None and VNC
// Authentication, and the encodings Raw, CopyRect, DesktopSize and
// Cursor. Pixels arrive in the framebuffer's own layout, so a rectangle
// is copied into it without conversion. Every length the server sends is
// checked against a limit before anything is read or allocated for it.
package vnc

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"image"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"changkun.de/x/occamy/internal/backend"
)

// Defaults for the fields that Config leaves zero.
const (
	DefaultPort        = "5900"
	DefaultDialTimeout = 15 * time.Second
)

// Limits on the remote screen. A server that reports a screen wider or
// taller than MaxSide, or larger than MaxPixels, is refused, since the
// session allocates the whole screen. 8K UHD (7680×4320) fits.
const (
	MaxSide   = 16384
	MaxPixels = 1 << 25
)

const (
	// inputQueue is the number of inputs that wait to be written before
	// Input fails.
	inputQueue = 256
	// inputBytes bounds the bytes of the inputs that wait to be written,
	// since one input can be large: 4 MiB of clipboard, or 16 bytes per
	// character of Text. It holds two clipboards of the largest size.
	inputBytes = 2*maxClipboard + 64<<10
	// readBuffer is the size of the buffer that the server's messages are
	// read through.
	readBuffer = 64 << 10
	// writeBuffer is the most queued input written in one write.
	writeBuffer = 4 << 10
)

var (
	// ErrNotRunning is returned by Input before Run has started or after
	// it has returned.
	ErrNotRunning = errors.New("vnc: backend not running")
	// ErrInputFull is returned by Input when too many inputs, or too many
	// bytes of them, wait to be written, as when the server has stopped
	// reading.
	ErrInputFull = errors.New("vnc: input queue full")
	// ErrUnsupportedInput is returned by Input for input a graphical
	// backend does not take, such as backend.TermInput.
	ErrUnsupportedInput = errors.New("vnc: unsupported input")
	// ErrAuthFailed is wrapped by Run's error when the server rejects the
	// password, or fails the security handshake otherwise.
	ErrAuthFailed = errors.New("vnc: authentication failed")
	// ErrRefused is wrapped by Run's error when the server refuses the
	// connection before authentication, with its reason.
	ErrRefused = errors.New("vnc: server refused the connection")
	// ErrNoSecurityType is wrapped by Run's error when the server offers
	// no security type that the backend supports.
	ErrNoSecurityType = errors.New("vnc: no supported security type")
)

// Config describes a VNC connection.
type Config struct {
	// Addr is the server's host and TCP port, not a VNC display number.
	// The port defaults to 5900.
	Addr string
	// Password is the VNC password, of which VNC Authentication uses the
	// first 8 bytes. If it is empty and the server asks for one, the
	// session's owner is asked. If it is set and the server offers both
	// None and VNC Authentication, VNC Authentication is chosen.
	Password string
	// DialTimeout bounds the TCP connection and defaults to 15 seconds.
	// Only Run's context bounds the handshake that follows, since it may
	// wait for the owner to answer a prompt.
	DialTimeout time.Duration
	// Logger receives the backend's log. Nil means slog.Default().
	Logger *slog.Logger
}

// Backend is a connection to one VNC server. It implements
// backend.Backend.
type Backend struct {
	addr        string
	password    string
	dialTimeout time.Duration
	log         *slog.Logger
	limits      limits

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
	if cfg.DialTimeout < 0 {
		return nil, fmt.Errorf("vnc: negative dial timeout %v", cfg.DialTimeout)
	}
	if cfg.DialTimeout == 0 {
		cfg.DialTimeout = DefaultDialTimeout
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Backend{
		addr:        addr,
		password:    cfg.Password,
		dialTimeout: cfg.DialTimeout,
		log:         cfg.Logger,
		limits:      defaultLimits,
	}, nil
}

// hostPort adds the default port to addr if it has none.
func hostPort(addr string) (string, error) {
	if addr == "" {
		return "", errors.New("vnc: no address")
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		// No port; an IPv6 literal may still be in brackets.
		host, port = strings.TrimSuffix(strings.TrimPrefix(addr, "["), "]"), ""
	}
	if host == "" {
		return "", fmt.Errorf("vnc: no host in address %q", addr)
	}
	if port == "" {
		port = DefaultPort
	}
	return net.JoinHostPort(host, port), nil
}

// Class implements backend.Backend.
func (b *Backend) Class() backend.Class { return backend.Graphical }

// Demand implements backend.Backend. While d.Visible, the backend asks
// for an update, and asks for the next once that update has been
// applied: at most one request is sent per update applied. While nobody
// can see, no request is sent, and an update already requested is
// applied when it comes.
//
// RFB has no way to tell which request an update answers, so every
// update counts as the answer. A server that sends updates nobody asked
// for, which RFC 6143 forbids, may then hold two requests. libvncserver,
// which sends size changes and cursor shapes before it looks at the
// requested region, keeps that region as the union of the requests, so
// the second merges with the first.
func (b *Backend) Demand(d backend.Demand) {
	b.visible.Store(d.Visible)
	b.mu.Lock()
	r := b.run
	b.mu.Unlock()
	if r != nil {
		r.poke()
	}
}

// Input implements backend.Backend. Key and Text become key events,
// Pointer and Wheel pointer events, and text Clipboard content becomes
// the server's cut text in Latin-1. Resize is ignored: without
// ExtendedDesktopSize the server's size is fixed and viewers scale.
// TermInput fails with ErrUnsupportedInput.
//
// Input queues what it is given and returns at once. Input that arrives
// while Run is still connecting is written once the handshake ends. The
// queue holds 256 inputs and about 8 MiB, so a single Text of more than
// about 500,000 characters, at 16 bytes each, fails with ErrInputFull.
func (b *Backend) Input(ev backend.InputEvent) error {
	b.mu.Lock()
	r := b.run
	b.mu.Unlock()
	if r == nil {
		return ErrNotRunning
	}
	var p []byte
	switch ev := ev.(type) {
	case backend.Key:
		if ev.Keysym == 0 {
			return nil
		}
		p = appendKey(nil, ev.Keysym, ev.Down)
	case backend.Text:
		for _, c := range ev.Text {
			if k, ok := textKeysym(c); ok {
				p = appendKey(appendKey(p, k, true), k, false)
			}
		}
	case backend.Pointer:
		p = r.pointer(ev)
	case backend.Wheel:
		p = r.wheel(ev)
	case backend.Clipboard:
		if !isText(ev.MIME) {
			return nil
		}
		p = appendCutText(nil, ev.Data)
	case backend.Resize:
		return nil
	default:
		return fmt.Errorf("%w: %T", ErrUnsupportedInput, ev)
	}
	return r.send(p)
}

// run is the state of one Run that Input and Demand share.
type run struct {
	queue  chan []byte   // client messages from Input, for the writer
	queued atomic.Int64  // the bytes in queue and not yet written
	kick   chan struct{} // asks the writer to reconsider an update request
	werr   error         // the writer's error; read after it has ended

	mu      sync.Mutex
	size    image.Point // the screen, zero until the handshake ends
	pending bool        // a request was sent and no update has come since
	full    bool        // the next request asks for the whole screen
	buttons uint8       // the pointer's buttons, as last sent
	pos     image.Point // the pointer, as last sent
	scroll  image.Point // pixels scrolled toward the next wheel press
}

func newRun() *run {
	return &run{
		queue: make(chan []byte, inputQueue),
		kick:  make(chan struct{}, 1),
		full:  true,
	}
}

// send queues p for the writer, unless the queue is full. Input calls it
// one call at a time, and the writer only lowers queued, so the check
// and the addition need no lock.
func (r *run) send(p []byte) error {
	if len(p) == 0 {
		return nil
	}
	n := int64(len(p))
	if r.queued.Load()+n > inputBytes {
		return ErrInputFull
	}
	r.queued.Add(n)
	select {
	case r.queue <- p:
		return nil
	default:
		r.queued.Add(-n)
		return ErrInputFull
	}
}

// poke asks the writer to send an update request if one is due.
func (r *run) poke() {
	select {
	case r.kick <- struct{}{}:
	default:
	}
}

// request returns the update request to send now, or nil. One is due
// when a viewer can see the screen and no request has been sent since
// the last update. It asks for the whole screen after the handshake and
// after a resize, and for changes otherwise.
func (r *run) request(visible bool) []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !visible || r.pending || r.size == (image.Point{}) {
		return nil
	}
	r.pending = true
	incremental := !r.full
	r.full = false
	return appendUpdateRequest(nil, incremental, image.Rectangle{Max: r.size})
}

// updated records that an update has been applied, and that it resized
// the screen to size if resized.
func (r *run) updated(size image.Point, resized bool) {
	r.mu.Lock()
	r.size = size
	r.pending = false
	r.full = r.full || resized
	r.mu.Unlock()
	r.poke()
}

// Run implements backend.Backend. It connects, authenticates, and
// applies the server's updates until ctx is done or the server closes
// the connection. A server that closes it between messages ends the
// session normally, since RFB has no other way to end one; closing it
// within a message is an error that wraps io.ErrUnexpectedEOF. Every
// goroutine that Run starts has ended when it returns.
func (b *Backend) Run(ctx context.Context, sink backend.Sink) error {
	r := newRun()
	b.mu.Lock()
	if b.run != nil {
		b.mu.Unlock()
		return errors.New("vnc: Run called while running")
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

// connect dials, runs the handshake, and serves the connection.
func (b *Backend) connect(ctx context.Context, sink backend.Sink, r *run) error {
	d := net.Dialer{Timeout: b.dialTimeout}
	nc, err := d.DialContext(ctx, "tcp", b.addr)
	if err != nil {
		return failure(ctx, fmt.Errorf("vnc: %w", err))
	}
	// Closing the connection is the only way to abort a read or a write,
	// including the handshake's.
	closed := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		nc.Close()
		close(closed)
	})
	defer func() {
		if stop() {
			nc.Close()
		} else {
			<-closed
		}
	}()

	br := bufio.NewReaderSize(nc, readBuffer)
	init, err := handshake(br, nc, b.passwordFunc(ctx, sink), b.password != "", b.limits)
	if err != nil {
		return failure(ctx, err)
	}
	b.log.Debug("vnc: connected", "addr", b.addr, "version", fmt.Sprintf("3.%d", init.version),
		"security", init.security, "width", init.size.X, "height", init.size.Y)
	if _, err := nc.Write(appendSetup(nil)); err != nil {
		return failure(ctx, fmt.Errorf("vnc: %w", err))
	}
	sink.Resize(init.size.X, init.size.Y)
	if init.name != "" {
		sink.Title(init.name)
	}
	r.mu.Lock()
	r.size = init.size
	r.mu.Unlock()

	dec := newDecoder(ctx, br, sink, init.size, b.limits, b.log)
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		if r.werr = r.writeLoop(nc, done, &b.visible); r.werr != nil {
			nc.Close() // stops the reader too
		}
	})
	err = r.readLoop(dec)
	close(done)
	nc.Close()
	wg.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil && r.werr != nil {
		// The failed write closed the connection under the reader.
		return r.werr
	}
	return err
}

// passwordFunc returns the configured password, or asks the owner for
// one. The answer is passed to the handshake and kept nowhere else.
func (b *Backend) passwordFunc(ctx context.Context, sink backend.Sink) func() (string, error) {
	return func() (string, error) {
		if b.password != "" {
			return b.password, nil
		}
		p := backend.Prompt{
			Instruction: "VNC password for " + b.addr,
			Fields:      []backend.PromptField{{Label: "Password", Secret: true}},
		}
		reply, err := sink.Prompt(ctx, p)
		if err != nil {
			return "", fmt.Errorf("vnc: prompt: %w", err)
		}
		if len(reply.Answers) != 1 {
			return "", fmt.Errorf("vnc: prompt: %d answers for 1 question", len(reply.Answers))
		}
		return reply.Answers[0], nil
	}
}

// failure returns ctx's error if ctx is done, since closing the
// connection then made the step fail, and err otherwise.
func failure(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

// readLoop applies the server's messages until the connection ends. It
// returns nil when the server closes the connection between messages.
func (r *run) readLoop(d *decoder) error {
	for {
		t, err := d.next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if t == msgFramebufferUpdate {
			r.updated(d.size, d.resized)
		}
	}
}

// writeLoop writes queued input and update requests until done is closed
// or a write fails. Whatever is ready is written in one write.
func (r *run) writeLoop(w io.Writer, done <-chan struct{}, visible *atomic.Bool) error {
	// A bufio.Writer's error sticks, so Flush reports a failed Write.
	bw := bufio.NewWriterSize(w, writeBuffer)
	// Demand may have come before the handshake ended.
	r.poke()
	for {
		select {
		case <-done:
			return nil
		case p := <-r.queue:
			r.write(bw, p)
		case <-r.kick:
			_, _ = bw.Write(r.request(visible.Load()))
		}
	more:
		for bw.Buffered() < writeBuffer {
			select {
			case p := <-r.queue:
				r.write(bw, p)
			case <-r.kick:
				_, _ = bw.Write(r.request(visible.Load()))
			default:
				break more
			}
		}
		if err := bw.Flush(); err != nil {
			return fmt.Errorf("vnc: write: %w", err)
		}
	}
}

// write writes queued input to bw. Its bytes leave the queue's budget
// once bw has taken them: buffered, or written through for a large p.
func (r *run) write(bw *bufio.Writer, p []byte) {
	_, _ = bw.Write(p)
	r.queued.Add(-int64(len(p)))
}
