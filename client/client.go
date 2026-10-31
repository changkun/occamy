// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// Package client is a protocol v1 client for occamyd, in pure Go
// (specs/008-native-architecture/client-core.md). The tests, command
// line tools and the Swift client's C export layer build on it.
//
// Dial opens a session over a WebSocket, and New over any connection
// that carries one protocol message per call. The caller pulls events
// with Next: the frames of a graphical session, decoded into the
// session's framebuffer with the rectangles they changed; the bytes of
// a terminal session's frames, untouched, for the caller's terminal
// emulator; and the small state, such as the title, the clipboard and
// the pointer's image. Key, Text, TermInput, Pointer, Wheel, Resize and
// Clipboard send input, numbered from 1 as the protocol says, and
// return each input's sequence number, which a Frame's LastSeq reports
// once the server applied it.
//
// # Frames and flow control
//
// A frame is applied to the framebuffer, and acknowledged to the
// server, when Next delivers it, not when it arrives. The framebuffer
// therefore always holds the last frame Next delivered, never half of
// one, and a caller that falls behind holds back the server instead of
// queueing stale frames: the server keeps at most its frame window
// unacknowledged (one more after an input) and coalesces what changes
// meanwhile into its next frame. The acknowledgement's decode time runs
// from the frame's frame-end arriving to its delivery, so the server
// spaces its frames by how fast the caller takes them rather than
// taking a slow caller for a slow network. The server's frame interval
// follows that time, so a long pause slows motion, though not the frame
// after an input, for about as long again; a caller that stops taking
// events for long, as when its window is hidden, says so with View
// instead, and gets one frame of the current screen when it shows again.
//
// Tiles of codecs other than raw are decoded as they arrive, in
// parallel, by decoders the caller plugs in through Options.Decoders;
// raw tiles need no decoding and are copied when applied. Small state
// that only ever holds its latest value (the title, clipboard, cursor
// and pointer position) replaces an undelivered event of its kind, so a
// caller that does not call Next for a while gets the newest value once.
//
// Input never waits for Next: input methods queue their message for a
// writer goroutine and return, waiting only while the queue holds more
// than the network has taken.
//
// The package has no API stability promise until protocol v1's release
// candidate.
package client

import (
	"context"
	"errors"
	"fmt"
	"image"
	"math"
	"net/http"
	"slices"
	"sync"
	"time"

	"changkun.de/x/occamy/protocol"
)

// Conn is a connection to occamyd that carries one protocol message per
// call, such as a WebSocket with one message per binary frame. Its
// methods are those of the server's end, so that tests can join a client
// to a server in process.
type Conn interface {
	// ReadMessage returns the server's next message, which the Session
	// keeps: the Conn must not reuse it. It returns io.EOF once the
	// server closed the connection without a close code, and a
	// *CloseError if it closed with one.
	ReadMessage(ctx context.Context) ([]byte, error)
	// WriteMessage sends p, which the caller may reuse once it returns.
	WriteMessage(ctx context.Context, p []byte) error
}

// A Conn may also implement these. Close closes the connection with a
// close code once the Session ends; without it, the Session stops
// reading by cancelling the context of ReadMessage. SetReadLimit is
// given the largest message the Session accepts, so that a larger one
// is refused before it is buffered.
type (
	closer interface {
		Close(code protocol.CloseCode, reason string) error
	}
	readLimiter interface {
		SetReadLimit(n int64)
	}
)

// CloseError reports that the server closed the connection with a close
// code, such as when it refuses a token before welcome.
type CloseError struct {
	Code   protocol.CloseCode
	Reason string
}

func (e *CloseError) Error() string {
	if e.Reason == "" {
		return fmt.Sprintf("client: closed by the server: %v", e.Code)
	}
	return fmt.Sprintf("client: closed by the server: %v: %s", e.Code, e.Reason)
}

// RefusedError reports that the client refused the session the server
// welcomed it to, because it cannot show it, such as when they have no
// codec in common. The server made the session before it sent welcome,
// and SessionID names it, so that a client can end it (DELETE
// /api/v1/sessions/{id}) rather than leave it for the owner grace. It
// matches protocol.ErrUnsupported.
type RefusedError struct {
	SessionID string
	Reason    string
}

func (e *RefusedError) Error() string {
	return fmt.Sprintf("client: %s: %v", e.Reason, protocol.ErrUnsupported)
}

func (e *RefusedError) Unwrap() error { return protocol.ErrUnsupported }

// Errors of a Session.
var (
	// ErrClosed reports a call on a session that has ended, and Next
	// after it delivered End.
	ErrClosed = errors.New("client: session ended")
	// ErrReadOnly reports input from a read-only viewer, which the
	// server would drop.
	ErrReadOnly = errors.New("client: read-only viewer")
	// ErrNotOwner reports a prompt reply from a viewer other than the
	// owner, which the server ignores.
	ErrNotOwner = errors.New("client: not the owner")
)

// Defaults of Options.
const (
	// DefaultName is the hello's name if Options.Name is empty.
	DefaultName = "occamy-go"
	// DefaultMaxPixels is the largest graphical screen a Session takes,
	// in pixels, if Options.MaxPixels is zero: 8192×8192, 256 MiB.
	DefaultMaxPixels = 8192 * 8192
	// DefaultCacheSlots is the tile cache slots a Session offers if
	// Options.CacheSlots is zero: 16 MiB of tiles at most.
	DefaultCacheSlots = 1024
)

// closeTimeout bounds the wait to send close to a server that does not
// read.
const closeTimeout = time.Second

// opCost is what an op of an undelivered frame holds beyond its
// message, counted in the backlog with it: the op in its frame's slice,
// and a decoding tile's job and pixels' slice. Measured with 200,000
// ops of 1×1 pixels: 110 bytes beyond the message for a raw tile or a
// copy-rect, 334 for a tile of a plugged codec, decoded. Without it, a
// frame of tiny ops would hold several times what it counts.
const opCost = 512

// minBacklog is the least the Session holds of undelivered frames and
// events before it takes the server for broken, and the least work it
// allows a frame; see maxBacklog and maxWork. Tests lower it to reach
// the limits with small screens.
var minBacklog = 64 << 20

// maxEvents bounds the undelivered small events. Those that hold the
// latest value replace each other, and a viewer that left cancels its
// undelivered join, so a server that is not broken stays far below.
const maxEvents = 1024

// maxBells bounds the undelivered bells; more ring as one burst anyway.
const maxBells = 3

// Options configure a Session. The zero value offers raw tiles and an
// xterm-compatible terminal, visible, at the server's choice of size.
type Options struct {
	// Name names the client and its version in hello, such as
	// "occamy-cli/1.0"; DefaultName if empty.
	Name string
	// Width and Height are the screen size the client asks for, in
	// device pixels, if the session is graphical; zero leaves it to the
	// remote.
	Width, Height uint16
	// Cols and Rows are the terminal size the client asks for, in
	// character cells, if the session is a terminal; zero for the
	// server's default.
	Cols, Rows uint16
	// Hidden starts the client hidden: the server sends no frames until
	// View shows it.
	Hidden bool
	// Decoders decode codecs other than raw, in the order of preference
	// the client offers them in. Raw is built in and offered last.
	Decoders []Decoder
	// MaxTextureSize is the largest tile or cursor side the client
	// decodes; zero means protocol.DefaultMaxTextureSize.
	MaxTextureSize uint16
	// PixelRatio is the display's device pixels per CSS pixel in
	// hundredths; zero means 100.
	PixelRatio uint16
	// FrameWindow is the number of unacknowledged frames the client
	// allows; zero means the server's default.
	FrameWindow uint8
	// MaxMessageSize is the largest message the client takes, in bytes;
	// zero means protocol.DefaultMaxMessageSize, and less than
	// protocol.MinMessageSize is refused.
	MaxMessageSize uint32
	// MaxPixels is the largest graphical screen the client shows, in
	// pixels; DefaultMaxPixels if zero. A larger one ends the session
	// with unsupported, before the framebuffer is allocated.
	MaxPixels int
	// DecodeWorkers is the number of goroutines that decode tiles of
	// plugged codecs; runtime.GOMAXPROCS(0) if zero.
	DecodeWorkers int
	// CacheSlots is the number of tile cache slots the client offers,
	// DefaultCacheSlots if zero. A slot holds the pixels of one tile of
	// at most protocol.MaxCachedPixels, 16 KiB, which the server can
	// then draw again by naming the slot. NoCache offers no slots and
	// not the cached codec.
	CacheSlots uint16
	NoCache    bool

	// HTTPClient and HTTPHeader are Dial's: the client of the upgrade
	// request, http.DefaultClient if nil, and headers to add to it, such
	// as Origin or an Authorization header that replaces the token.
	HTTPClient *http.Client
	HTTPHeader http.Header
}

// Hello returns the hello that Dial sends for o. A caller of New sends
// it itself, such as a test that hands it to the server in process.
func (o Options) Hello() protocol.Hello {
	name := o.Name
	if name == "" {
		name = DefaultName
	}
	return protocol.Hello{
		Width:   o.Width,
		Height:  o.Height,
		Cols:    o.Cols,
		Rows:    o.Rows,
		Visible: !o.Hidden,
		Name:    name,
		Capabilities: protocol.Capabilities{
			Codecs:         o.codecs(),
			MaxTextureSize: o.MaxTextureSize,
			Terminal:       1,
			PixelRatio:     o.PixelRatio,
			FrameWindow:    o.FrameWindow,
			MaxMessageSize: o.MaxMessageSize,
			CacheSlots:     o.cacheSlots(),
		},
	}
}

// codecs are the codecs o offers, in its order of preference, raw last,
// and then cached, unless the client keeps no tile cache.
func (o Options) codecs() []protocol.Codec {
	var cs []protocol.Codec
	for _, d := range o.Decoders {
		c := d.Codec()
		if c != 0 && c != protocol.CodecRaw && c != protocol.CodecCached && !slices.Contains(cs, c) {
			cs = append(cs, c)
		}
	}
	cs = append(cs, protocol.CodecRaw)
	if !o.NoCache {
		cs = append(cs, protocol.CodecCached)
	}
	return cs
}

// cacheSlots is the number of tile cache slots o offers.
func (o Options) cacheSlots() uint16 {
	switch {
	case o.NoCache:
		return 0
	case o.CacheSlots == 0:
		return DefaultCacheSlots
	}
	return o.CacheSlots
}

func (o Options) check() error {
	if o.MaxMessageSize != 0 && o.MaxMessageSize < protocol.MinMessageSize {
		return fmt.Errorf("client: a max_message_size of %d is below %d", o.MaxMessageSize, protocol.MinMessageSize)
	}
	if o.MaxPixels < 0 || o.DecodeWorkers < 0 {
		return errors.New("client: negative MaxPixels or DecodeWorkers")
	}
	return nil
}

// limits are the sizes the client accepts before welcome: its own.
func (o Options) limits() protocol.Limits {
	return protocol.Limits{
		MaxMessageSize: int(orDefault(o.MaxMessageSize, protocol.DefaultMaxMessageSize)),
		MaxTextureSize: int(orDefault(o.MaxTextureSize, protocol.DefaultMaxTextureSize)),
	}
}

func orDefault[T uint8 | uint16 | uint32](v, def T) T {
	if v == 0 {
		return def
	}
	return v
}

// Session is a client's connection to one occamyd session. Next, the
// input methods, View, PromptReply, Framebuffer and Close may be called
// from any goroutine.
type Session struct {
	conn     Conn
	opts     Options
	welcome  protocol.Welcome
	agreed   protocol.Capabilities // as the client uses them
	limits   protocol.Limits
	window   int // the agreed frame window
	decoders map[protocol.Codec]Decoder
	pool     *pool // nil unless a plugged codec was agreed

	ctx    context.Context // ends with the session
	cancel context.CancelFunc
	wg     sync.WaitGroup
	once   sync.Once // the session's end
	closed chan struct{}
	// abort is closed by Close: the decoders drop what is queued.
	abort     chan struct{}
	abortOnce sync.Once

	// The reader's state, owned by the read goroutine.
	reg     *protocol.Registry
	wire    image.Point // the screen's size after the last resize read
	sized   bool        // a resize was read
	lastEnd uint32      // the ID of the last frame-end read
	open    *frame      // the frame being read, nil between frames
	slots   []slot      // the tile cache, slot s at s-1

	mu        sync.Mutex
	queue     []item  // undelivered, in order
	pending   []Event // the rest of the frame being delivered
	frames    int     // complete frames in queue
	taken     int     // frames Next took from queue and has not acknowledged
	backlog   int     // bytes held by queue and the open frame
	end       *End
	delivered bool   // End was delivered
	discard   bool   // Close was called: nothing but End is delivered
	shown     *Frame // delivered and not yet acknowledged
	size      image.Point
	wake      chan struct{} // holds a token when Next has news

	next chan struct{} // held by the caller in Next

	fbMu sync.RWMutex
	fb   *image.RGBA

	out outbox
}

// item is an undelivered small event, or a complete frame.
type item struct {
	ev    Event
	f     *frame
	bytes int
}

// New starts a session over c, whose server was sent the hello of opts
// (Options.Hello). It reads welcome, and returns a *CloseError if the
// server closed instead, such as for a refused token, or a
// *RefusedError if the client cannot show the session welcome names.
// ctx bounds the wait for welcome; once New returns, the session lasts
// until Close or the server ends it.
func New(ctx context.Context, c Conn, opts Options) (*Session, error) {
	if err := opts.check(); err != nil {
		return nil, err
	}
	if rl, ok := c.(readLimiter); ok {
		rl.SetReadLimit(int64(opts.limits().MaxMessageSize))
	}
	w, err := readWelcome(ctx, c, opts)
	if err != nil {
		var ce *CloseError
		switch {
		case errors.As(err, &ce):
			closeConn(c, ce.Code, ce.Reason)
		case isProtocolError(err):
			code := protocol.CloseCodeFor(err)
			sendClose(c, protocol.Close{Code: code, Reason: reasonFor(code)})
			closeConn(c, code, reasonFor(code))
		default: // the connection failed
			closeConn(c, protocol.CloseNormal, "")
		}
		return nil, err
	}
	s := newSession(c, opts, w)
	if rl, ok := c.(readLimiter); ok {
		rl.SetReadLimit(int64(s.limits.MaxMessageSize))
	}
	if reason := s.unsupported(); reason != "" {
		closeErr := &CloseError{Code: protocol.CloseUnsupported, Reason: reason}
		sendClose(c, protocol.Close{Code: closeErr.Code, Reason: reason})
		closeConn(c, closeErr.Code, reason)
		return nil, &RefusedError{SessionID: w.SessionID, Reason: reason}
	}
	s.start()
	return s, nil
}

// readWelcome reads the server's first message, which is welcome, or
// close if the server refused the client.
func readWelcome(ctx context.Context, c Conn, opts Options) (protocol.Welcome, error) {
	reg := protocol.ServerMessages()
	l := opts.limits()
	for {
		b, err := c.ReadMessage(ctx)
		if err != nil {
			return protocol.Welcome{}, err
		}
		m, err := protocol.Decode(b, reg, l)
		if err != nil {
			return protocol.Welcome{}, err
		}
		switch m := m.(type) {
		case nil: // an ignorable message of a later version
		case *protocol.Welcome:
			return *m, nil
		case *protocol.Close:
			return protocol.Welcome{}, &CloseError{Code: m.Code, Reason: m.Reason}
		default:
			return protocol.Welcome{}, fmt.Errorf("%w: %v before welcome", protocol.ErrInvalid, m.Type())
		}
	}
}

func newSession(c Conn, opts Options, w protocol.Welcome) *Session {
	s := &Session{
		conn:     c,
		opts:     opts,
		welcome:  w,
		reg:      protocol.ServerMessages(),
		decoders: map[protocol.Codec]Decoder{protocol.CodecRaw: raw{}},
		closed:   make(chan struct{}),
		abort:    make(chan struct{}),
		wake:     make(chan struct{}, 1),
		next:     make(chan struct{}, 1),
		fb:       &image.RGBA{},
	}
	s.ctx, s.cancel = context.WithCancel(context.Background())
	for _, d := range opts.Decoders {
		if c := d.Codec(); c != protocol.CodecRaw {
			if _, ok := s.decoders[c]; !ok {
				s.decoders[c] = d
			}
		}
	}
	s.agreed = s.agree(w.Capabilities)
	s.limits = s.agreed.Limits()
	s.window = int(s.agreed.FrameWindow)
	s.out.init(s.limits.MaxMessageSize)
	return s
}

// agree returns the capabilities the client uses from those the server
// agreed to: none beyond what the client offered, and no limit above its
// own, whatever the server states.
func (s *Session) agree(w protocol.Capabilities) protocol.Capabilities {
	offered := s.opts.Hello().Capabilities
	var a protocol.Capabilities
	for _, c := range w.Codecs {
		if slices.Contains(offered.Codecs, c) {
			a.Codecs = append(a.Codecs, c)
		}
	}
	a.MaxTextureSize = min(orDefault(w.MaxTextureSize, protocol.DefaultMaxTextureSize), orDefault(offered.MaxTextureSize, protocol.DefaultMaxTextureSize))
	a.Terminal = min(w.Terminal, offered.Terminal)
	a.PixelRatio = offered.PixelRatio
	a.FrameWindow = min(orDefault(w.FrameWindow, protocol.DefaultFrameWindow), orDefault(offered.FrameWindow, protocol.DefaultFrameWindow))
	a.MaxMessageSize = min(orDefault(w.MaxMessageSize, protocol.DefaultMaxMessageSize), orDefault(offered.MaxMessageSize, protocol.DefaultMaxMessageSize))
	a.CacheSlots = min(w.CacheSlots, offered.CacheSlots)
	return a
}

// unsupported says why the client cannot show the session the server
// welcomed it to, or "" if it can.
func (s *Session) unsupported() string {
	switch s.welcome.Class {
	case protocol.ClassGraphical:
		// cached draws only what another codec stored.
		if !slices.ContainsFunc(s.agreed.Codecs, func(c protocol.Codec) bool { return c != protocol.CodecCached }) {
			return "no codec in common"
		}
	case protocol.ClassTerminal:
		if s.agreed.Terminal == 0 {
			return "no terminal level in common"
		}
	}
	return ""
}

func (s *Session) start() {
	if s.welcome.Class == protocol.ClassGraphical {
		for _, c := range s.agreed.Codecs {
			if c != protocol.CodecRaw && c != protocol.CodecCached {
				s.pool = newPool(s.opts.DecodeWorkers, s.abort, &s.wg)
				break
			}
		}
		s.slots = make([]slot, s.agreed.CacheSlots)
	}
	s.wg.Add(2)
	go s.read()
	go s.write()
}

// ViewerID is this viewer's ID, as ViewerJoined and ViewerLeft name
// viewers.
func (s *Session) ViewerID() uint32 { return s.welcome.ViewerID }

// Role is this viewer's role in the session.
func (s *Session) Role() protocol.Role { return s.welcome.Role }

// Class is whether the session is a terminal or graphical.
func (s *Session) Class() protocol.Class { return s.welcome.Class }

// SessionID is the session's ID, for joining and sharing it.
func (s *Session) SessionID() string { return s.welcome.SessionID }

// Capabilities are the capabilities the client uses: those the server
// agreed to in welcome, within what the client offered, with the frame
// window and the maximum message and texture sizes always stated.
func (s *Session) Capabilities() protocol.Capabilities {
	a := s.agreed
	a.Codecs = slices.Clone(a.Codecs)
	return a
}

// Size is the screen's size as of the last Resize that Next delivered:
// pixels for a graphical session, character cells for a terminal. It is
// zero until the first frame.
func (s *Session) Size() image.Point {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.size
}

// Framebuffer calls f with a graphical session's screen as of the last
// frame that Next delivered, locked against the next frame. img and its
// pixels are valid only during f, and f must not call Next. Its
// rectangle is empty before the first frame, and for a terminal.
func (s *Session) Framebuffer(f func(img *image.RGBA)) {
	s.fbMu.RLock()
	defer s.fbMu.RUnlock()
	f(s.fb)
}

// Screenshot returns a copy of the screen as of the last frame that
// Next delivered.
func (s *Session) Screenshot() *image.RGBA {
	var img *image.RGBA
	s.Framebuffer(func(fb *image.RGBA) {
		img = &image.RGBA{Pix: slices.Clone(fb.Pix), Stride: fb.Stride, Rect: fb.Rect}
	})
	return img
}

// Next returns the session's next event. It blocks until there is one
// or ctx is done. A Frame of a graphical session is applied to the
// framebuffer as Next delivers it (a Resize first, if the frame resized
// the screen). Calling Next again says the caller has shown the frame,
// and acknowledges it to the server. A caller that does not come back
// for the server's viewer timeout, 30 s for occamyd by default, hidden
// or not, is closed with too-slow.
//
// Next returns ctx's error while it waits for an event or for a frame's
// tiles to decode; once it applies a frame, it finishes it, which the
// limits on a frame's work keep short. Once the session ended, Next
// delivers the events that arrived before the end, then End, and then
// returns ErrClosed. After Close, it delivers End at once.
func (s *Session) Next(ctx context.Context) (Event, error) {
	select {
	case s.next <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-s.next }()
	s.mu.Lock()
	s.ackShown()
	s.mu.Unlock()
	for {
		s.mu.Lock()
		if s.delivered {
			s.mu.Unlock()
			return nil, ErrClosed
		}
		if s.discard { // what the reader added since Close
			s.dropQueue()
		}
		if len(s.pending) > 0 {
			ev := s.pending[0]
			s.pending[0] = nil
			s.pending = s.pending[1:]
			s.delivering(ev)
			s.mu.Unlock()
			return ev, nil
		}
		if len(s.queue) > 0 {
			it := s.queue[0]
			if it.f == nil {
				s.queue[0] = item{}
				s.queue = s.queue[1:]
				s.backlog -= it.bytes
				s.mu.Unlock()
				return it.ev, nil
			}
			s.mu.Unlock()
			if err := s.decoded(ctx, it.f); err != nil {
				if ctx.Err() != nil {
					return nil, err // the frame waits for the next call
				}
				continue // Close dropped the frame
			}
			s.mu.Lock()
			if len(s.queue) == 0 || s.queue[0].f != it.f {
				s.mu.Unlock()
				continue // Close dropped the frame
			}
			s.queue[0] = item{}
			s.queue = s.queue[1:]
			s.backlog -= it.f.bytes
			s.frames--
			s.taken++
			s.mu.Unlock()
			evs, err := s.apply(it.f)
			s.mu.Lock()
			if err != nil {
				// What follows builds on the frame.
				s.dropQueue()
				s.mu.Unlock()
				if !errors.Is(err, ErrClosed) { // Close ends the session itself
					s.fail(err)
				}
				continue
			}
			s.pending = evs
			s.mu.Unlock()
			continue
		}
		if s.end != nil {
			s.delivered = true
			e := *s.end
			s.mu.Unlock()
			return e, nil
		}
		s.mu.Unlock()
		select {
		case <-s.wake:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// decoded waits until f's tiles of plugged codecs are decoded. It
// returns ctx's error if ctx is done first, and ErrClosed after Close.
func (s *Session) decoded(ctx context.Context, f *frame) error {
	for _, o := range f.ops {
		if o.job == nil {
			continue
		}
		select {
		case <-o.job.done:
		case <-s.abort:
			return ErrClosed
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// dropQueue drops the undelivered events and frames. s.mu is held.
func (s *Session) dropQueue() {
	for _, it := range s.queue {
		if it.f != nil {
			s.backlog -= it.f.bytes
			s.frames--
		} else {
			s.backlog -= it.bytes
		}
	}
	s.queue, s.pending = nil, nil
}

// delivering records what delivering ev means: a Frame is to be
// acknowledged once the caller comes back for the next event, and a
// Resize sets the size. s.mu is held.
func (s *Session) delivering(ev Event) {
	switch ev := ev.(type) {
	case Frame:
		s.shown = &ev
	case Resize:
		s.size = image.Pt(ev.Width, ev.Height)
	}
}

// ackShown acknowledges the frame Next delivered last, which the caller
// has shown now that it asks for the next event. Its decode time runs
// from its frame-end arriving until now. s.mu is held.
func (s *Session) ackShown() {
	f := s.shown
	if f == nil {
		return
	}
	s.shown = nil
	s.taken--
	d := time.Since(f.arrived).Microseconds()
	s.out.ack(protocol.FrameAck{
		FrameID:    f.ID,
		DecodeTime: uint32(min(max(d, 0), math.MaxUint32)), // #nosec G115 -- clamped to a u32
	})
}

// poke wakes Next.
func (s *Session) poke() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Close ends the session: it sends the input queued so far and close,
// closes the connection, and waits for the session's goroutines to
// exit. Next then delivers End and nothing else; a frame Next was
// applying may be left half drawn in the framebuffer.
//
// Close waits up to a second for the network to take the queued input,
// then for the WebSocket's close handshake, which coder/websocket bounds
// at 5 s a step: about 6 s against a server that stopped answering. A
// UI calls it off its main thread.
func (s *Session) Close() error {
	s.abortOnce.Do(func() { close(s.abort) })
	s.mu.Lock()
	s.discard = true
	s.dropQueue()
	s.mu.Unlock()
	s.terminate(End{Code: protocol.CloseNormal}, &protocol.Close{Code: protocol.CloseNormal}, true)
	s.wg.Wait()
	return nil
}

// fail ends the session over err: a protocol error by the server, which
// it is told with close, or a failed connection.
func (s *Session) fail(err error) {
	var ce *CloseError
	switch {
	case errors.As(err, &ce):
		s.terminate(End{Code: ce.Code, Reason: ce.Reason}, nil, false)
	case isProtocolError(err):
		code := protocol.CloseCodeFor(err)
		reason := reasonFor(code)
		s.terminate(End{Code: code, Reason: reason, Err: err}, &protocol.Close{Code: code, Reason: reason}, false)
	default:
		s.terminate(End{Err: err}, nil, false)
	}
}

func isProtocolError(err error) bool {
	return errors.Is(err, protocol.ErrTruncated) || errors.Is(err, protocol.ErrTooLarge) ||
		errors.Is(err, protocol.ErrInvalidUTF8) || errors.Is(err, protocol.ErrTrailingData) ||
		errors.Is(err, protocol.ErrInvalid) || errors.Is(err, protocol.ErrUnsupported)
}

func reasonFor(c protocol.CloseCode) string {
	switch c {
	case protocol.CloseUnsupported:
		return "unsupported"
	case protocol.CloseTooLarge:
		return "too large"
	}
	return "protocol error"
}

// terminate ends the session with e, once. If m is set, the client tells
// the server with close, after the input queued so far if flush is set;
// then it closes the connection, and the session's goroutines exit.
func (s *Session) terminate(e End, m *protocol.Close, flush bool) {
	s.once.Do(func() {
		s.mu.Lock()
		s.end = &e
		s.mu.Unlock()
		s.poke()
		close(s.closed)
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			code := e.Code
			if m != nil {
				s.out.finish(m.Append(nil), flush)
				t := time.NewTimer(closeTimeout)
				select {
				case <-s.out.flushed:
				case <-t.C:
				}
				t.Stop()
			} else {
				s.out.finish(nil, false)
			}
			if code == 0 {
				code = protocol.CloseNormal
			}
			closeConn(s.conn, code, e.Reason)
			s.cancel()
			if s.pool != nil {
				s.pool.close()
			}
		}()
	})
}

// closeConn closes c with code if it can be closed. A reason longer
// than a WebSocket close frame holds is left out.
func closeConn(c Conn, code protocol.CloseCode, reason string) {
	if len(reason) > maxCloseReason {
		reason = ""
	}
	if cc, ok := c.(closer); ok {
		_ = cc.Close(code, reason)
	}
}

// maxCloseReason is the longest reason a WebSocket close frame holds.
const maxCloseReason = 123

// sendClose sends m on c, waiting at most closeTimeout.
func sendClose(c Conn, m protocol.Close) {
	ctx, cancel := context.WithTimeout(context.Background(), closeTimeout)
	defer cancel()
	_ = c.WriteMessage(ctx, m.Append(nil))
}
