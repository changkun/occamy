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
// the wheel as mouse events. Clipboard text goes both ways through the
// clipboard channel, unless the configuration disables a direction.
// While no viewer can see the screen, the server is told to suppress its
// output.
//
// Every length the server sends is bounded by the client before it is
// read or allocated, and the client decodes bitmaps and pointer updates
// at a bounded rate of pixels (internal/rdp).
package rdp

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

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

// The bounds of Config.Width and Config.Height: each side from MinSide
// to MaxSide, and the area at most MaxPixels.
const (
	MinSide   = 200
	MaxSide   = 8192
	MaxPixels = 1 << 25
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
	// characters fits.
	inputEvents = 1 << 18
	// maxBatch is the most input events written at once. A Text is typed
	// a part at a time, so that the writer holds no more events than
	// this whatever the input.
	maxBatch = 1024
	// hangUpGrace is how long a writer whose write failed lets the reader
	// go on before it closes the connection: long enough for the reader
	// to read what the server sent before it hung up, the reason it
	// ended the session among it.
	hangUpGrace = time.Second
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
	// SecuritySetting names what sets Security where the backend's user
	// sets it, such as a configuration key, in the error for a server
	// that selects standard RDP security when offered NLA alone, as
	// xrdp does although it takes TLS; "Config.Security" if empty.
	SecuritySetting string
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
	// RoutingCookie sends the first nine characters of the username as
	// the routing cookie of the connection request, as mstsc does, for a
	// load balancer that routes connections by user, such as an RD
	// Connection Broker. The request goes before TLS, so the username
	// goes in the clear; it is off by default, and then nothing about
	// the user is sent before the certificate policy passes.
	RoutingCookie bool
	// DisableCopy keeps the server's clipboard from the viewers, and
	// DisablePaste the viewers' clipboard from the server, as guacd's
	// disable-copy and disable-paste do. With both, the backend does not
	// ask for the clipboard channel.
	DisableCopy, DisablePaste bool
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
	// securitySetting is Config.SecuritySetting, with its default.
	securitySetting string
	// dial connects to the server; tests replace it.
	dial func(ctx context.Context) (net.Conn, error)
	// grace is hangUpGrace; tests shorten it.
	grace time.Duration

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
	if cfg.Width < MinSide || cfg.Height < MinSide || cfg.Width > MaxSide || cfg.Height > MaxSide || cfg.Width*cfg.Height > MaxPixels {
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
	var clip rdp.ClipboardMode
	if !cfg.DisableCopy {
		clip |= rdp.ClipboardFromServer
	}
	if !cfg.DisablePaste {
		clip |= rdp.ClipboardToServer
	}
	b := &Backend{
		addr: addr,
		cfg: rdp.Config{
			Width: cfg.Width, Height: cfg.Height,
			ColorDepth: cfg.ColorDepth,
			Domain:     cfg.Domain, Username: cfg.Username, Password: cfg.Password,
			ClientName:     cfg.ClientName,
			RoutingCookie:  cfg.RoutingCookie,
			KeyboardLayout: cfg.KeyboardLayout,
			Protocols:      protocols,
			TLS:            cfg.TLS,
			Clipboard:      clip,
			Resize:         true,
			Logger:         cfg.Logger,
		},
		dialTimeout:     cfg.DialTimeout,
		log:             cfg.Logger,
		securitySetting: cmp.Or(cfg.SecuritySetting, "Config.Security"),
		grace:           hangUpGrace,
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
// events (see input.go). Clipboard text becomes the server's clipboard
// through the clipboard channel, at most rdp.MaxClipboard bytes of it,
// unless DisablePaste is set; other clipboard content is dropped.
// Resize asks the server for the size through Display Control
// (rdp.Client.RequestSize), the newest size at most every half second;
// a server without it keeps its size, which viewers scale. TermInput
// fails with ErrUnsupportedInput.
//
// Input queues what it is given and returns at once, the clipboard in
// order with the keys, so that the server has it before the keys that
// paste it. Input that arrives while Run is still connecting is written
// once the connection is up. The queue holds 256 inputs of at most 2^18
// events and two clipboards of the most text, so a single Text of more
// than 65,536 characters fails with ErrInputFull.
func (b *Backend) Input(ev backend.InputEvent) error {
	b.mu.Lock()
	r := b.run
	b.mu.Unlock()
	if r == nil {
		return ErrNotRunning
	}
	if c, ok := ev.(backend.Clipboard); ok {
		if b.cfg.Clipboard&rdp.ClipboardToServer == 0 || !isText(c.MIME) {
			return nil
		}
		text := clipboardText(c.Data)
		return r.send(queued{ev: backend.Clipboard{MIME: c.MIME, Data: text}, n: 1, bytes: len(text)})
	}
	if rs, ok := ev.(backend.Resize); ok {
		if rs.Width <= 0 || rs.Height <= 0 {
			return nil
		}
		return r.send(queued{ev: rs, n: 1})
	}
	n, err := cost(ev)
	if err != nil || n == 0 {
		return err
	}
	return r.send(queued{ev: ev, n: n})
}

// clipboardBytes bounds the bytes of clipboard text in the queue: two
// clipboards of the most text, as the VNC backend bounds its queue.
const clipboardBytes = 2 * rdp.MaxClipboard

// isText reports whether a clipboard MIME type is text, which is all
// the backend gives the server. An empty type is taken as text.
func isText(mime string) bool {
	return mime == "" || strings.HasPrefix(strings.ToLower(mime), "text/")
}

// clipboardText returns a copy of the first rdp.MaxClipboard bytes of
// text, cut at a character's start.
func clipboardText(text []byte) []byte {
	if len(text) > rdp.MaxClipboard {
		n := rdp.MaxClipboard
		for n > 0 && !utf8.RuneStart(text[n]) {
			n--
		}
		text = text[:n]
	}
	return bytes.Clone(text)
}

// run is the state of one Run that Input and Demand share.
type run struct {
	queue  chan queued   // inputs from Input, for the writer
	queued atomic.Int64  // the events that the inputs in queue may make
	bytes  atomic.Int64  // the bytes of clipboard text in queue
	kick   chan struct{} // asks the writer to reconsider the demand

	// Owned by the writer.
	input  inputState
	typing string // the rest of a Text being typed
	paste  []byte // clipboard text taken from the queue and not yet set
	pasted bool   // paste holds text to set
}

// queued is an input in the queue, with the events it may make and the
// bytes of clipboard text it holds.
type queued struct {
	ev    backend.InputEvent
	n     int
	bytes int
}

func newRun() *run {
	return &run{
		queue: make(chan queued, inputQueue),
		kick:  make(chan struct{}, 1),
	}
}

// send queues q unless the queue is full: of inputs, of the events they
// may make, or of clipboard text. Input calls it one call at a time, and
// the writer only lowers queued and bytes, so the checks and the
// additions need no lock.
func (r *run) send(q queued) error {
	if r.queued.Load()+int64(q.n) > inputEvents || r.bytes.Load()+int64(q.bytes) > clipboardBytes {
		return ErrInputFull
	}
	r.queued.Add(int64(q.n))
	r.bytes.Add(int64(q.bytes))
	select {
	case r.queue <- q:
		return nil
	default:
		r.queued.Add(-int64(q.n))
		r.bytes.Add(-int64(q.bytes))
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
		var pe *rdp.ProtocolError
		switch {
		case errors.As(err, &ce):
			err = fmt.Errorf("%w: %w", ErrAuthFailed, err)
		case errors.As(err, &pe) && pe.Selected == rdp.ProtocolRDP && pe.Offered&rdp.ProtocolSSL == 0:
			err = fmt.Errorf("%w; a server that selects standard RDP security when offered NLA alone may still take TLS, as xrdp does: allow TLS with %s", err, b.securitySetting)
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
		// The writer turns viewers' input into RDP's, beyond the
		// session's recover: a panic stops the client and ends Run.
		defer func() {
			if perr := backend.Recovered(recover()); perr != nil {
				werr = perr
				c.Close()
			}
		}()
		if werr = r.writeLoop(c, done, &b.visible); werr == nil {
			return
		}
		// A write fails when the server hangs up, often before the
		// reader has read what the server sent first: the reader ends
		// on its own then, with the server's reason. Otherwise the
		// failure stops it.
		t := time.NewTimer(b.grace)
		defer t.Stop()
		select {
		case <-done:
		case <-t.C:
			c.Close()
		}
	})
	err = func() error {
		// Deferred, so that a panic of the client, which the session
		// recovers from, stops the writer and closes the connection too.
		defer func() {
			close(done)
			c.Close()
			wg.Wait()
		}()
		return c.Run(ctx, sink)
	}()
	return b.end(ctx, err, werr)
}

// end returns Run's result for the client's, and the writer's error,
// which is the result when it is a panic, whatever the client returned,
// and otherwise only when it stopped the client.
func (b *Backend) end(ctx context.Context, err, werr error) error {
	if errors.As(werr, new(backend.PanicError)) {
		// A bug, whatever else happened meanwhile: the session ends with
		// its internal reason and logs the stack.
		return werr
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if werr != nil && errors.Is(err, net.ErrClosed) {
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

// client is what the writer needs of an rdp.Client; the tests replace
// it.
type client interface {
	SendInput(events ...rdp.InputEvent) error
	SuppressOutput(suppress bool) error
	SetClipboard(text []byte) error
	RequestSize(width, height int)
	UnicodeInput() bool
	MouseXInput() bool
}

// writeLoop writes queued input and what the demand asks of the server,
// until done is closed or a write fails. Input that is ready is written
// at once, in writes of up to maxBatch events, in input PDUs of up to 255
// events. Clipboard text is set after the input queued before it and
// before the input queued after it.
func (r *run) writeLoop(c client, done <-chan struct{}, visible *atomic.Bool) error {
	shown := true // the server sends updates until told otherwise
	batch := make([]rdp.InputEvent, 0, maxBatch)
	r.poke() // Demand may have come before the connection was up
	for {
		if r.typing == "" {
			select {
			case <-done:
				return nil
			case q := <-r.queue:
				batch = r.take(batch, c, q)
			case <-r.kick:
			}
		}
		batch = r.fill(batch, c)
		if len(batch) > 0 {
			if err := c.SendInput(batch...); err != nil {
				return writeFailed(done, err)
			}
			clear(batch)
			batch = batch[:0]
		}
		if r.pasted {
			text := r.paste
			r.paste, r.pasted = nil, false
			if err := c.SetClipboard(text); err != nil {
				return writeFailed(done, err)
			}
		}
		if v := visible.Load(); v != shown {
			if err := c.SuppressOutput(!v); err != nil {
				return writeFailed(done, err)
			}
			shown = v
		}
	}
}

// fill adds to batch what is ready to write, up to maxBatch events: the
// rest of a Text being typed, as much of it as fits, then queued inputs,
// which go whole, while the batch has room for the most an input other
// than Text makes, up to clipboard text, which is set after the batch.
func (r *run) fill(batch []rdp.InputEvent, c client) []rdp.InputEvent {
	for {
		if r.typing != "" {
			if batch, r.typing = text(batch, capabilitiesOf(c), r.typing, maxBatch); r.typing != "" {
				return batch
			}
		}
		if r.pasted || len(batch) > maxBatch-maxInputEvents {
			return batch
		}
		select {
		case q := <-r.queue:
			batch = r.take(batch, c, q)
		case <-r.kick:
		default:
			return batch
		}
	}
}

// take appends the events of a queued input, or for a Text makes it the
// text being typed, or for a Clipboard the text to set. The input then
// leaves the queue's budget.
func (r *run) take(batch []rdp.InputEvent, c client, q queued) []rdp.InputEvent {
	switch ev := q.ev.(type) {
	case backend.Text:
		r.typing = ev.Text
	case backend.Clipboard:
		r.paste, r.pasted = ev.Data, true
	case backend.Resize:
		// The client keeps the newest size and paces what it asks for.
		c.RequestSize(ev.Width, ev.Height)
	default:
		batch = r.input.events(batch, capabilitiesOf(c), q.ev)
	}
	r.queued.Add(-int64(q.n))
	r.bytes.Add(-int64(q.bytes))
	return batch
}

// capabilitiesOf returns what the server's input capability set allows.
func capabilitiesOf(c client) capabilities {
	return capabilities{unicode: c.UnicodeInput(), mouseX: c.MouseXInput()}
}

// writeFailed returns the writer's result for a write that failed: nil
// if Run is done or the connection was closed under the writer, by Run
// or by the reader, whose result is then Run's; the error otherwise.
func writeFailed(done <-chan struct{}, err error) error {
	select {
	case <-done:
		return nil
	default:
	}
	if errors.Is(err, net.ErrClosed) {
		return nil
	}
	return fmt.Errorf("rdp: write: %w", err)
}
