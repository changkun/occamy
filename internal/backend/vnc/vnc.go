// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// Package vnc is a graphical backend that connects to a VNC server with
// a pure-Go RFB client (RFC 6143) and draws the remote screen into the
// session's framebuffer
// (specs/008-native-architecture/native-vnc-backend.md). It asks for
// screen updates only while a viewer can see the screen, and asks the
// session's owner for credentials that its configuration lacks.
//
// It speaks RFB 3.3, 3.7 and 3.8 with the security types None and VNC
// Authentication, the encodings Tight, ZRLE, Hextile, Raw and CopyRect,
// and the pseudo-encodings DesktopSize and Cursor. Pixels arrive in the
// framebuffer's own layout, so a raw rectangle is copied into it without
// conversion. Every length the server sends is checked against a limit
// before anything is read or allocated for it.
//
// It also speaks these extensions of the community RFB specification
// (rfbproto) when the server offers them: VeNCrypt with TLS and an X.509
// certificate checked by a TLS policy, and Plain, None or VNC
// Authentication within (vencrypt.go); ExtendedDesktopSize, so that the
// owner can resize the remote screen (desktopsize.go); ContinuousUpdates
// with Fence (continuous.go); QEMU Extended Key Events with physical key
// codes (qemukeys.go); Extended Clipboard in UTF-8 (clipboard.go); and
// LastRect (extensions.go).
//
// Apple Remote Desktop authentication, the security type with which
// macOS Screen Sharing logs in its users, is spoken only when
// Config.ARD allows it (ard.go), since its key exchange authenticates
// no server.
package vnc

import (
	"bufio"
	"context"
	"crypto/tls"
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
	// controlQueue is the number of answers to the server's Fences that
	// wait to be written before the reader waits for the writer.
	controlQueue = 16
)

var (
	// ErrNotRunning is returned by Input before Run has started or after
	// it has returned, except for a Resize, which the next Run asks for.
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
	// Username is the username for VeNCrypt's Plain authentication, or
	// for Apple Remote Desktop's. If it is set, Plain is chosen over
	// VeNCrypt's other subtypes, and Apple Remote Desktop, if ARD allows
	// it, over None and VNC Authentication.
	Username string
	// Password is the VNC password, of which VNC Authentication uses the
	// first 8 bytes, or the password for VeNCrypt's Plain or Apple Remote
	// Desktop, which use all of it. If it is empty and the server asks
	// for one, the session's owner is asked, as for a username that Plain
	// or Apple Remote Desktop needs. If it is set and the server offers
	// both None and VNC Authentication, VNC Authentication is chosen.
	Password string
	// TLS is the policy for the server's certificate when VeNCrypt runs
	// TLS. VeNCrypt is chosen whenever a server offers it.
	TLS TLS
	// ARD allows Apple Remote Desktop authentication (security type 30),
	// which macOS Screen Sharing needs to log in a user of the Mac. It
	// is off by default: its Diffie-Hellman key exchange authenticates no
	// server, so that anyone between the backend and the server can read
	// the username and the password, which are the Mac account's own,
	// and the session that follows is not encrypted. With ARD, type 30 is
	// chosen when the server does not offer VeNCrypt and either Username
	// is set or the server offers neither None nor VNC Authentication.
	// It sends Username and Password, each of at most 63 bytes and
	// without a NUL. ARD cannot be combined with a TLS requirement.
	//
	// ARD raises what an attacker in between gets from any server, not
	// only from a Mac: without a TLS requirement, it can strip VeNCrypt
	// from the server's offer and offer type 30 alone, and the backend
	// sends it the whole password. Without ARD, the most such a
	// downgrade gets is VNC Authentication's DES response, from the
	// password's first 8 bytes. Set ARD only for Macs, on networks you
	// trust.
	ARD bool
	// ARDSetting names what allows ARD where the backend's user sets it,
	// such as a configuration key, in the error for a server that offers
	// Apple Remote Desktop authentication and nothing else the backend
	// takes; "Config.ARD" if empty.
	ARDSetting string
	// Width and Height are the screen size to ask the server for, as a
	// Resize before Run does: with SetDesktopSize, once the server has
	// shown that it supports ExtendedDesktopSize. Zero keeps the server's
	// size.
	Width, Height int
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
	username    string
	password    string
	tls         tlsPolicy
	ard         bool
	ardSetting  string
	dialTimeout time.Duration
	log         *slog.Logger
	limits      limits

	// visible is the latest Demand. It outlives a Run, since the session
	// reports only changes and may report one before Run starts.
	visible atomic.Bool

	mu  sync.Mutex
	run *run // the state that Input shares with Run, nil when not running
	// size is the newest size asked for, by Config or a Resize, which
	// each Run asks for at its start; zero if none.
	size image.Point
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
	host, _, _ := net.SplitHostPort(addr)
	policy, err := newTLSPolicy(cfg.TLS, host)
	if err != nil {
		return nil, err
	}
	if cfg.ARD && policy.require {
		return nil, errors.New("vnc: Apple Remote Desktop runs without TLS: ARD cannot be allowed " +
			"while TLS is required by TLS.Require, RootCAs or Pins")
	}
	if cfg.Width < 0 || cfg.Height < 0 || (cfg.Width == 0) != (cfg.Height == 0) {
		return nil, fmt.Errorf("vnc: invalid screen size %dx%d", cfg.Width, cfg.Height)
	}
	if cfg.ARDSetting == "" {
		cfg.ARDSetting = "Config.ARD"
	}
	return &Backend{
		addr:        addr,
		username:    cfg.Username,
		password:    cfg.Password,
		tls:         policy,
		ard:         cfg.ARD,
		ardSetting:  cfg.ARDSetting,
		size:        image.Pt(cfg.Width, cfg.Height),
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
//
// A server that supports ContinuousUpdates and Fence pushes updates
// instead: the backend enables them for the whole screen while
// d.Visible and disables them while nobody can see, and asks for no
// changes of its own.
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
// the server's clipboard. A Key with a physical code becomes a QEMU
// Extended Key Event once the server has asked for them. Resize asks for
// the screen's new size with SetDesktopSize once the server has shown
// that it supports ExtendedDesktopSize; without it, the server's size is
// fixed and viewers scale. A Resize is also kept for the next Run, so
// that one before Run is asked for once Run starts, as Config's size
// would be. TermInput fails with ErrUnsupportedInput.
//
// Input queues what it is given and returns at once. Input that arrives
// while Run is still connecting is written once the handshake ends. The
// queue holds 256 inputs and about 8 MiB, so a single Text of more than
// about 500,000 characters, at 16 bytes each, fails with ErrInputFull.
func (b *Backend) Input(ev backend.InputEvent) error {
	if ev, ok := ev.(backend.Resize); ok {
		b.resize(ev.Width, ev.Height)
		return nil
	}
	b.mu.Lock()
	r := b.run
	b.mu.Unlock()
	if r == nil {
		return ErrNotRunning
	}
	var p []byte
	switch ev := ev.(type) {
	case backend.Key:
		p = r.key(ev)
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
		p = r.clipboard(ev.Data)
	default:
		return fmt.Errorf("%w: %T", ErrUnsupportedInput, ev)
	}
	return r.send(p)
}

// resize asks for a screen of w×h in the Run that runs, if any, and in
// every Run that follows. A size beyond the limits is ignored.
func (b *Backend) resize(w, h int) {
	if b.limits.check(w, h) != nil {
		return
	}
	b.mu.Lock()
	b.size = image.Pt(w, h)
	r := b.run
	b.mu.Unlock()
	if r != nil {
		r.resize(w, h, b.limits)
	}
}

// run is the state of one Run that Input and Demand share.
type run struct {
	queue   chan []byte   // client messages from Input, for the writer
	queued  atomic.Int64  // the bytes in queue and not yet written
	kick    chan struct{} // asks the writer to reconsider what is due
	control chan []byte   // answers to the server's Fences, for the writer
	wdone   chan struct{} // closed when the writer has ended
	werr    error         // the writer's error; read after it has ended

	mu      sync.Mutex
	size    image.Point // the screen, zero until the handshake ends
	pending bool        // a request was sent and no update has come since
	full    bool        // the next request asks for the whole screen
	buttons uint8       // the pointer's buttons, as last sent
	pos     image.Point // the pointer, as last sent
	scroll  image.Point // pixels scrolled toward the next wheel press
	cont    continuous  // ContinuousUpdates and Fence
	desk    desktop     // ExtendedDesktopSize
	keys    extKeys     // QEMU Extended Key Events
	clip    clipboard   // Extended Clipboard
}

func newRun() *run {
	return &run{
		queue:   make(chan []byte, inputQueue),
		kick:    make(chan struct{}, 1),
		control: make(chan []byte, controlQueue),
		wdone:   make(chan struct{}),
		full:    true,
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

// poke asks the writer to send what is due: an update request, or a
// message of an extension.
func (r *run) poke() {
	select {
	case r.kick <- struct{}{}:
	default:
	}
}

// due writes to w the messages that are due: continuous updates enabled
// or disabled, SetDesktopSize, an update request, and the clipboard's.
// A clipboard's text is compressed after r.mu is released.
func (r *run) due(w io.Writer, visible bool) {
	r.mu.Lock()
	p := r.continuousDue(nil, visible)
	p = r.desktopSizeDue(p)
	p = r.request(p, visible)
	p, text := r.clipboardDue(p)
	r.mu.Unlock()
	if text != nil {
		p = appendClipboardProvide(p, text)
	}
	_, _ = w.Write(p)
}

// request appends the update request to send now, if one is due. One is
// due when a viewer can see the screen and no request has been sent
// since the last update. It asks for the whole screen after the
// handshake and after a resize, and for changes otherwise, except while
// continuous updates bring the changes. r.mu must be held.
func (r *run) request(p []byte, visible bool) []byte {
	if !visible || r.pending || r.size == (image.Point{}) || r.cont.on && !r.full {
		return p
	}
	r.pending = true
	incremental := !r.full
	r.full = false
	return appendUpdateRequest(p, incremental, image.Rectangle{Max: r.size})
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
	if b.size != (image.Point{}) {
		r.resize(b.size.X, b.size.Y, b.limits)
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
	c := &conn{Reader: br, Writer: nc}
	init, err := handshake(c, c, b.auth(ctx, sink, nc, br, c), b.limits)
	if err != nil {
		return failure(ctx, err)
	}
	b.log.Debug("vnc: connected", "addr", b.addr, "version", fmt.Sprintf("3.%d", init.version),
		"security", init.security, "width", init.size.X, "height", init.size.Y)
	if init.security == secARD {
		b.log.Warn("vnc: logged in with Apple Remote Desktop authentication, which authenticates no server "+
			"and leaves the session unencrypted", "addr", b.addr)
	}
	if _, err := c.Write(appendSetup(nil)); err != nil {
		return failure(ctx, fmt.Errorf("vnc: %w", err))
	}
	sink.Resize(init.size.X, init.size.Y)
	if init.name != "" {
		sink.Title(init.name)
	}
	r.mu.Lock()
	r.size = init.size
	r.mu.Unlock()

	dec := newDecoder(ctx, c.Reader, sink, init.size, b.limits, b.log)
	dec.peer = r
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		defer close(r.wdone) // a reader that waits to answer a Fence stops waiting
		if r.werr = r.writeLoop(c.Writer, done, &b.visible); r.werr != nil {
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

// auth returns what the handshake needs: the credentials, asked of the
// owner through sink when the configuration lacks them, and a TLS
// handshake on nc that replaces both directions of c. br is what the
// handshake has read nc through.
func (b *Backend) auth(ctx context.Context, sink backend.Sink, nc net.Conn, br *bufio.Reader, c *conn) auth {
	return auth{
		password: func() (string, error) {
			_, pw, err := b.credentials(ctx, sink, false)
			return pw, err
		},
		plain: func() (string, string, error) {
			return b.credentials(ctx, sink, true)
		},
		hasUser:     b.username != "",
		hasPassword: b.password != "",
		ard:         b.ard,
		ardSetting:  b.ardSetting,
		requireTLS:  b.tls.require,
		startTLS: func() error {
			// The server waits for the client's hello: anything it sent
			// before would be read as plain text, and is a protocol error.
			if br.Buffered() > 0 {
				return errors.New("vnc: the server sent data before the TLS handshake")
			}
			tc := tls.Client(nc, b.tls.config(b.addr, b.log))
			if err := tc.HandshakeContext(ctx); err != nil {
				return fmt.Errorf("vnc: TLS: %w", err)
			}
			cs := tc.ConnectionState()
			b.log.Debug("vnc: TLS", "addr", b.addr, "version", tls.VersionName(cs.Version),
				"cipher", tls.CipherSuiteName(cs.CipherSuite))
			c.Reader = bufio.NewReaderSize(tc, readBuffer)
			c.Writer = tc
			return nil
		},
	}
}

// credentials returns the configured username, if user, and password,
// asking the owner for the ones the configuration lacks. The answers are
// passed to the handshake and kept nowhere else.
func (b *Backend) credentials(ctx context.Context, sink backend.Sink, user bool) (string, string, error) {
	name, pw := b.username, b.password
	var fields []backend.PromptField
	var what []string
	if user && name == "" {
		fields = append(fields, backend.PromptField{Label: "Username"})
		what = append(what, "username")
	}
	if pw == "" {
		fields = append(fields, backend.PromptField{Label: "Password", Secret: true})
		what = append(what, "password")
	}
	if len(fields) == 0 {
		return name, pw, nil
	}
	p := backend.Prompt{
		Instruction: "VNC " + strings.Join(what, " and ") + " for " + b.addr,
		Fields:      fields,
	}
	reply, err := sink.Prompt(ctx, p)
	if err != nil {
		return "", "", fmt.Errorf("vnc: prompt: %w", err)
	}
	if len(reply.Answers) != len(fields) {
		return "", "", fmt.Errorf("vnc: prompt: %d answers for %d questions", len(reply.Answers), len(fields))
	}
	answers := reply.Answers
	if user && name == "" {
		name, answers = answers[0], answers[1:]
	}
	if pw == "" {
		pw = answers[0]
	}
	return name, pw, nil
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

// writeLoop writes queued input, answers to Fences, and what is due
// until done is closed or a write fails. Whatever is ready is written in
// one write.
func (r *run) writeLoop(w io.Writer, done <-chan struct{}, visible *atomic.Bool) error {
	// A bufio.Writer's error sticks, so Flush reports a failed Write.
	bw := bufio.NewWriterSize(w, writeBuffer)
	// Demand may have come before the handshake ended.
	r.poke()
	for {
		select {
		case <-done:
			return nil
		case p := <-r.control:
			_, _ = bw.Write(p)
		case p := <-r.queue:
			r.write(bw, p)
		case <-r.kick:
			r.due(bw, visible.Load())
		}
	more:
		for bw.Buffered() < writeBuffer {
			select {
			case p := <-r.control:
				_, _ = bw.Write(p)
			case p := <-r.queue:
				r.write(bw, p)
			case <-r.kick:
				r.due(bw, visible.Load())
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
