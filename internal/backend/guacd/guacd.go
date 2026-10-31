// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// Package guacd is a backend that bridges a guacd connection into a
// session (specs/008-native-architecture/guacd-bridge.md). It applies
// guacd's drawing to a Guacamole display, copies each frame's damage into
// the session's framebuffer before it answers the frame's sync, so that
// guacd paces against the bridge alone, and translates the viewers'
// input into Guacamole instructions.
//
// Where guacd 1.6 expects something of its client, the bridge does what
// guacamole-common-js 1.6 does, and the comments say which part of
// guacamole-server or guacamole-client a rule comes from.
package guacd

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"io"
	"log/slog"
	"maps"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"changkun.de/x/occamy/internal/backend"
	"changkun.de/x/occamy/internal/guacamole"
	"changkun.de/x/occamy/internal/guacd"
)

const (
	// keepAlive is how long the bridge stays silent before it sends a
	// nop, guacamole-common-js's KEEP_ALIVE_FREQUENCY. guacd drops a user
	// it has not heard from for 15 seconds (libguac's user-handshake.c).
	keepAlive = 5 * time.Second

	// inputQueue is the number of inputs that wait to be written before
	// Input fails.
	inputQueue = 256

	// blobSize is the most bytes in one blob the bridge sends,
	// guacamole-common-js's ArrayBufferWriter.DEFAULT_BLOB_LENGTH. Its
	// base64 is 8064 characters, within guacd's 8192 per element.
	blobSize = 6048

	// maxClipboard bounds the clipboard content kept from guacd, the
	// largest message protocol v1 carries by default.
	maxClipboard = 4 << 20

	// Wheel scrolling follows guacamole-common-js's Mouse: a line is 18
	// pixels, each 53 pixels scrolled press and release a wheel button
	// once, and what remains after a press is dropped, so that a notch
	// scrolls once whether a browser reports it as 3 lines or 100 pixels.
	pixelsPerLine  = 18
	pixelsPerPress = 53
	// maxPresses bounds the presses one Wheel makes.
	maxPresses = 32
)

// The button mask of Guacamole's mouse instruction. guacd's RDP support
// reads these five bits only (src/protocols/rdp/input-queue.c), and its
// VNC support passes the mask to the VNC server as it is.
const (
	maskLeft   = 1
	maskMiddle = 2
	maskRight  = 4
	maskUp     = 8
	maskDown   = 16
)

// Stream indices for the streams the bridge sends. Each stream goes out
// whole, from the instruction that opens it to its end, in one write, so
// that streams never interleave and one index per kind is enough. guacd
// accepts indices below 512 (GUAC_USER_MAX_STREAMS).
const (
	clipboardStream = 0
	argvStream      = 1
)

// refusals are the streams guacd may open that the bridge does not take,
// with the ack guacamole-common-js's Client sends when it has no handler,
// and the position of the stream index among the arguments.
var refusals = map[string]struct {
	message string
	status  int
	at      int
}{
	"audio": {"BAD TYPE", 0x030F, 0},
	"video": {"BAD TYPE", 0x030F, 0},
	"file":  {"File transfer unsupported", 0x0100, 0},
	"pipe":  {"Named pipes unsupported", 0x0100, 0},
	"body":  {"Receipt of body unsupported", 0x0100, 1},
	// guacd echoes argument values it accepted; nothing here needs them,
	// so they are refused but not counted as unsupported.
	"argv": {"Receiving argument values unsupported", 0x0100, 0},
}

var (
	// ErrClosed is returned by Input once Run has returned.
	ErrClosed = errors.New("backend/guacd: closed")
	// ErrQueueFull is returned by Input when too many inputs wait to be
	// written.
	ErrQueueFull = errors.New("backend/guacd: input queue full")
	// ErrUnsupportedInput is returned by Input for input a graphical
	// backend does not take, such as backend.TermInput.
	ErrUnsupportedInput = errors.New("backend/guacd: unsupported input")
)

var nop = []byte("3.nop;")

// Options configure a Backend. The zero value is ready to use.
type Options struct {
	// Logger receives the unsupported instructions, each opcode once per
	// session, and the streams guacd refuses. Nil means slog.Default().
	Logger *slog.Logger
	// Dial connects to guacd's address. Nil means a TCP connection with a
	// net.Dialer.
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
}

// Backend is a session's connection through guacd. It is graphical, even
// for guacd's terminal protocols, which guacd renders as pixels.
//
// Demand is ignored: guacd only sends what changed, so the bridge applies
// every frame to keep its display whole, and answers every sync so that
// guacd keeps sending.
type Backend struct {
	addr string
	cfg  guacd.Config
	log  *slog.Logger
	dial func(ctx context.Context, network, addr string) (net.Conn, error)

	started atomic.Bool
	queue   chan []byte   // instructions from Input, for the writer
	done    chan struct{} // closed when Run returns

	in struct {
		sync.Mutex
		x, y, buttons int // the pointer, as last sent
		scroll        int // pixels scrolled toward the next press
	}

	mu          sync.Mutex
	unsupported map[string]int // counted by the bridge
	drawing     map[string]int // counted by the display, as of the last frame
}

var _ backend.Backend = (*Backend)(nil)

// New returns a backend that connects to guacd at addr and creates or
// joins the connection that cfg describes.
func New(addr string, cfg guacd.Config, opts Options) *Backend {
	b := &Backend{
		addr:        addr,
		cfg:         cfg,
		log:         opts.Logger,
		dial:        opts.Dial,
		queue:       make(chan []byte, inputQueue),
		done:        make(chan struct{}),
		unsupported: map[string]int{},
	}
	if b.log == nil {
		b.log = slog.Default()
	}
	if b.dial == nil {
		var d net.Dialer
		b.dial = d.DialContext
	}
	return b
}

// Class implements backend.Backend.
func (b *Backend) Class() backend.Class { return backend.Graphical }

// Demand implements backend.Backend. It has no effect; see Backend.
func (b *Backend) Demand(backend.Demand) {}

// Unsupported returns how many instructions the session could not apply,
// by opcode: drawing the display does not implement, images it cannot
// decode ("img image/gif"), streams and instructions the bridge does not
// take, and instructions that failed ("end failed"). Drawing is counted
// as of the last frame.
func (b *Backend) Unsupported() map[string]int {
	b.mu.Lock()
	defer b.mu.Unlock()
	m := maps.Clone(b.unsupported)
	maps.Copy(m, b.drawing)
	return m
}

// Run implements backend.Backend. It returns a *guacd.Error when guacd
// ends the session with an error instruction.
func (b *Backend) Run(ctx context.Context, sink backend.Sink) error {
	if !b.started.CompareAndSwap(false, true) {
		return errors.New("backend/guacd: Run called more than once")
	}
	defer close(b.done)

	nc, err := b.dial(ctx, "tcp", b.addr)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("backend/guacd: %w", err)
	}
	defer nc.Close()
	conn, err := guacd.Handshake(ctx, nc, b.cfg)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	// Closing the connection unblocks the reader and the writer.
	stop := context.AfterFunc(ctx, func() { nc.Close() })
	defer stop()

	s := &session{
		b:         b,
		sink:      sink,
		conn:      conn,
		nc:        nc,
		display:   guacamole.NewDisplay(),
		clips:     map[int]*clipStream{},
		logged:    map[string]bool{},
		lastWrite: time.Now(),
	}
	quit := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() { s.writeLoop(quit) })
	err = s.readLoop(ctx)
	close(quit)
	nc.Close()
	wg.Wait()
	s.recordDrawing()
	if err != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

// Input implements backend.Backend. Inputs before the handshake ends wait
// for it.
func (b *Backend) Input(ev backend.InputEvent) error {
	select {
	case <-b.done:
		return ErrClosed
	default:
	}
	b.in.Lock()
	defer b.in.Unlock()
	var p []byte
	switch ev := ev.(type) {
	case backend.Key:
		// A key of keysym 0, such as a browser's dead key, has only its
		// HID code, for which guacd's key instruction has no field; it is
		// dropped, as the VNC backend drops it without QEMU's extension.
		if ev.Keysym != 0 {
			p = appendKey(nil, ev.Keysym, ev.Down)
		}
	case backend.Pointer:
		b.in.x, b.in.y, b.in.buttons = max(ev.X, 0), max(ev.Y, 0), buttonMask(ev.Buttons)
		p = b.appendMouse(nil, b.in.buttons)
	case backend.Wheel:
		p = b.wheel(ev)
	case backend.Text:
		for _, r := range ev.Text {
			if k, ok := textKeysym(r); ok {
				p = appendKey(appendKey(p, k, true), k, false)
			}
		}
	case backend.Clipboard:
		mime := ev.MIME
		if mime == "" {
			mime = "text/plain"
		}
		p = appendStream(nil, clipboardStream, ev.Data, "clipboard", mime)
	case backend.Resize:
		if ev.Width <= 0 || ev.Height <= 0 {
			return fmt.Errorf("backend/guacd: resize to %dx%d", ev.Width, ev.Height)
		}
		p = appendInstruction(nil, "size", strconv.Itoa(ev.Width), strconv.Itoa(ev.Height))
	default:
		return fmt.Errorf("%w: %T", ErrUnsupportedInput, ev)
	}
	if len(p) == 0 {
		return nil
	}
	select {
	case b.queue <- p:
		return nil
	default:
		return ErrQueueFull
	}
}

// buttonMask maps buttons to the mouse instruction's mask. Back and
// Forward have no bits there, so they are dropped.
func buttonMask(b backend.Buttons) int {
	m := 0
	if b&backend.ButtonLeft != 0 {
		m |= maskLeft
	}
	if b&backend.ButtonMiddle != 0 {
		m |= maskMiddle
	}
	if b&backend.ButtonRight != 0 {
		m |= maskRight
	}
	return m
}

func (b *Backend) appendMouse(p []byte, mask int) []byte {
	return appendInstruction(p, "mouse", strconv.Itoa(b.in.x), strconv.Itoa(b.in.y), strconv.Itoa(mask))
}

// wheel turns a scroll into presses and releases of the wheel buttons at
// the pointer, with the buttons held. Guacamole has no horizontal wheel,
// so DX is dropped.
func (b *Backend) wheel(w backend.Wheel) []byte {
	dy := min(max(w.DY, -1<<20), 1<<20)
	if w.Mode == backend.WheelLines {
		dy *= pixelsPerLine
	}
	b.in.scroll += dy
	bit, n := maskDown, b.in.scroll/pixelsPerPress
	if n < 0 {
		bit, n = maskUp, -n
	}
	if n == 0 {
		return nil
	}
	b.in.scroll = 0
	var p []byte
	for range min(n, maxPresses) {
		p = b.appendMouse(p, b.in.buttons|bit)
		p = b.appendMouse(p, b.in.buttons)
	}
	return p
}

func appendKey(p []byte, keysym uint32, down bool) []byte {
	pressed := "0"
	if down {
		pressed = "1"
	}
	return appendInstruction(p, "key", strconv.FormatUint(uint64(keysym), 10), pressed)
}

// textKeysym returns the keysym that types r, as guacamole-common-js's
// Keyboard derives one from a character: Latin-1 as it is, other
// characters as 0x1000000 plus the code point, and C0 controls as the
// keysyms 0xff00 plus the control (BackSpace, Tab, Return, Escape).
// A line feed types Return, and DEL and the C1 controls type nothing.
func textKeysym(r rune) (uint32, bool) {
	switch {
	case r == '\n':
		return 0xff0d, true
	case r < 0:
		return 0, false
	case r < 0x20:
		return 0xff00 | uint32(r), true // #nosec G115 -- r is in [0, 0x20)
	case r < 0x7f, r >= 0xa0 && r <= 0xff:
		return uint32(r), true // #nosec G115 -- r is in [0x20, 0xff]
	case r <= 0x9f:
		return 0, false
	case r <= utf8.MaxRune:
		return 0x1000000 + uint32(r), true // #nosec G115 -- r is in [0x100, utf8.MaxRune]
	}
	return 0, false
}

// appendInstruction appends an instruction with the given elements, the
// opcode first.
func appendInstruction(p []byte, elements ...string) []byte {
	for i, e := range elements {
		if i > 0 {
			p = append(p, ',')
		}
		p = strconv.AppendInt(p, int64(utf8.RuneCountInString(e)), 10)
		p = append(p, '.')
		p = append(p, e...)
	}
	return append(p, ';')
}

// appendStream appends a whole stream: the instruction that opens it,
// which has the stream's index after its opcode, data in blobs each
// base64 on its own, and end. Empty data has no blob, as
// guacamole-common-js's ArrayBufferWriter sends it.
func appendStream(p []byte, index int, data []byte, opcode string, args ...string) []byte {
	i := strconv.Itoa(index)
	p = appendInstruction(p, append([]string{opcode, i}, args...)...)
	for len(data) > 0 {
		n := min(len(data), blobSize)
		p = appendInstruction(p, "blob", i, base64.StdEncoding.EncodeToString(data[:n]))
		data = data[n:]
	}
	return appendInstruction(p, "end", i)
}

// session is one run of a Backend.
type session struct {
	b       *Backend
	sink    backend.Sink
	conn    *guacd.Conn
	nc      net.Conn
	display *guacamole.Display

	size        image.Point // as last passed to sink.Resize
	clips       map[int]*clipStream
	lastSync    string
	logged      map[string]bool // opcodes logged as unsupported
	drawingKeys int             // len(display.Unsupported) when last logged

	wmu       sync.Mutex // serialises writes to guacd
	werr      error
	lastWrite time.Time
}

// clipStream is a clipboard stream from guacd.
type clipStream struct {
	mime string
	data []byte
}

// write writes p to guacd. A failed write closes the connection, so that
// the reader stops too, and fails every later write.
func (s *session) write(p []byte) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	if s.werr != nil {
		return s.werr
	}
	if _, err := s.conn.WriteRaw(p); err != nil {
		s.werr = fmt.Errorf("backend/guacd: write: %w", err)
		s.nc.Close()
		return s.werr
	}
	s.lastWrite = time.Now()
	return nil
}

// writeLoop writes the inputs that Input queues, and a nop whenever
// nothing was written for keepAlive, until quit is closed or a write
// fails. It does not depend on the reader, which waits while the owner
// answers a prompt.
func (s *session) writeLoop(quit <-chan struct{}) {
	t := time.NewTimer(keepAlive)
	defer t.Stop()
	for {
		select {
		case <-quit:
			return
		case p := <-s.b.queue:
			if s.write(p) != nil {
				return
			}
		case <-t.C:
			s.wmu.Lock()
			idle := time.Since(s.lastWrite)
			s.wmu.Unlock()
			if idle < keepAlive {
				t.Reset(keepAlive - idle)
				continue
			}
			if s.write(nop) != nil {
				return
			}
			t.Reset(keepAlive)
		}
	}
}

func (s *session) readLoop(ctx context.Context) error {
	for {
		raw, err := s.conn.ReadRaw()
		if err != nil {
			s.wmu.Lock()
			werr := s.werr
			s.wmu.Unlock()
			switch {
			case werr != nil:
				return werr
			case errors.Is(err, io.EOF):
				// guacd sends disconnect before it closes
				// (libguac's user-handshake.c).
				return fmt.Errorf("backend/guacd: guacd closed the connection without disconnect: %w", io.ErrUnexpectedEOF)
			}
			return fmt.Errorf("backend/guacd: read: %w", err)
		}
		ins, err := guacamole.ParseInstruction(raw)
		if err != nil {
			return fmt.Errorf("backend/guacd: %w", err)
		}
		if done, err := s.handle(ctx, ins); done || err != nil {
			return err
		}
	}
}

// handle applies one instruction from guacd. It reports done when the
// session ends.
func (s *session) handle(ctx context.Context, ins *guacamole.Instruction) (done bool, err error) {
	args := ins.Args()
	switch op := ins.Opcode(); op {
	case "sync":
		return false, s.sync(args)
	case "disconnect":
		return true, nil
	case "error":
		e := &guacd.Error{}
		if len(args) > 0 {
			e.Message = args[0]
		}
		if len(args) > 1 {
			e.Status, _ = strconv.Atoi(args[1])
		}
		return true, e
	case "required":
		return false, s.prompt(ctx, args)
	case "name":
		if len(args) > 0 {
			s.sink.Title(args[0])
		}
	case "mouse":
		// guacd reports the pointer that another user or the remote
		// moved (libguac's display-flush.c).
		if len(args) >= 2 {
			x, errx := strconv.Atoi(args[0])
			y, erry := strconv.Atoi(args[1])
			if errx == nil && erry == nil {
				s.sink.CursorMove(image.Pt(x, y))
			}
		}
	case "cursor":
		s.draw(ins)
		if img, hot, ok := s.display.TakeCursor(); ok {
			// guacd hides the pointer with a transparent pixel
			// (libguac's display-builtin-cursors.c).
			if img != nil && transparent(img) {
				img = nil
			}
			s.sink.Cursor(img, hot)
		}
	case "clipboard":
		if i, ok := streamIndex(args); ok && len(args) >= 2 {
			s.clips[i] = &clipStream{mime: args[1]}
		}
	case "blob":
		if !s.clipboardBlob(args) {
			s.draw(ins)
		}
	case "end":
		if !s.clipboardEnd(args) {
			s.draw(ins)
		}
	case "ack":
		// guacd acks the streams the bridge sends; only refusals matter.
		if len(args) >= 3 {
			if status, err := strconv.Atoi(args[2]); err == nil && status >= 0x0100 {
				s.b.log.Warn("backend/guacd: guacd refused a stream",
					"stream", args[0], "message", args[1], "status", status)
			}
		}
	case "nop", "msg", "set", "log", "ready":
		// Keep-alives, notices of users joining and leaving, layer
		// properties for touch and strokes, and handshake leftovers.
	case "img", "copy", "transfer", "rect", "cfill", "dispose", "move", "shade", "size":
		s.draw(ins)
	default:
		if r, ok := refusals[op]; ok {
			if op != "argv" {
				s.count(op)
			}
			if r.at < len(args) {
				return false, s.write(appendInstruction(nil, "ack", args[r.at], r.message, strconv.Itoa(r.status)))
			}
			return false, nil
		}
		n := s.display.Unsupported[op]
		s.draw(ins)
		if s.display.Unsupported[op] == n {
			s.count(op) // not drawing that the display counted
		}
	}
	return false, nil
}

// draw applies an instruction to the display. A malformed instruction or
// an image that does not decode is counted and logged, and the session
// goes on.
func (s *session) draw(ins *guacamole.Instruction) {
	if err := s.display.Apply(ins); err != nil {
		key := ins.Opcode() + " failed"
		s.b.mu.Lock()
		s.b.unsupported[key]++
		s.b.mu.Unlock()
		if !s.logged[key] {
			s.logged[key] = true
			s.b.log.Warn("backend/guacd: instruction failed", "opcode", ins.Opcode(), "err", err)
		}
	}
	if n := len(s.display.Unsupported); n != s.drawingKeys {
		s.drawingKeys = n
		for op := range s.display.Unsupported {
			s.logOnce(op)
		}
	}
}

// count counts an instruction the bridge does not take.
func (s *session) count(op string) {
	s.b.mu.Lock()
	s.b.unsupported[op]++
	s.b.mu.Unlock()
	s.logOnce(op)
}

func (s *session) logOnce(op string) {
	if !s.logged[op] {
		s.logged[op] = true
		s.b.log.Warn("backend/guacd: unsupported instruction", "opcode", op)
	}
}

// recordDrawing makes the display's count of unsupported drawing
// visible to Backend.Unsupported.
func (s *session) recordDrawing() {
	if len(s.display.Unsupported) == 0 {
		return
	}
	m := maps.Clone(s.display.Unsupported)
	s.b.mu.Lock()
	s.b.drawing = m
	s.b.mu.Unlock()
}

// sync ends a frame: the damage goes into the sink, and only then is the
// sync answered, with its timestamp alone and once per timestamp, as
// guacamole-common-js's Client does. guacd rejects a timestamp it has
// not sent (libguac's user-handlers.c), so it is echoed as it came.
func (s *session) sync(args []string) error {
	if len(args) == 0 {
		return errors.New("backend/guacd: sync without a timestamp")
	}
	s.flush()
	s.recordDrawing()
	if args[0] == s.lastSync {
		return nil
	}
	s.lastSync = args[0]
	return s.write(appendInstruction(nil, "sync", args[0]))
}

// flush copies the display's damage into the sink, after a Resize if the
// default layer's size changed.
func (s *session) flush() {
	damage := s.display.TakeDamageRects()
	if size := s.display.Bounds().Size(); size != s.size && size.X > 0 && size.Y > 0 {
		s.size = size
		s.sink.Resize(size.X, size.Y)
		damage = []image.Rectangle{{Max: size}}
	}
	screen := image.Rectangle{Max: s.size}
	for _, r := range damage {
		r = r.Intersect(screen)
		if r.Empty() {
			continue
		}
		s.display.CopyTo(s.sink.Lock(r), r)
		s.sink.Unlock(r)
	}
}

// prompt asks the session's owner for the parameters guacd requires, and
// sends the answers as argv streams, as guacamole-client's ManagedClient
// does. guacd acks each stream; the bridge does not wait for the ack.
func (s *session) prompt(ctx context.Context, names []string) error {
	p := backend.Prompt{Name: s.b.cfg.Protocol, Fields: make([]backend.PromptField, len(names))}
	for i, name := range names {
		p.Fields[i] = backend.PromptField{Label: label(name), Secret: secret(name)}
	}
	reply, err := s.sink.Prompt(ctx, p)
	if err != nil {
		return fmt.Errorf("backend/guacd: prompt: %w", err)
	}
	var b []byte
	for i, name := range names {
		var answer string
		if i < len(reply.Answers) {
			answer = reply.Answers[i]
		}
		b = appendStream(b, argvStream, []byte(answer), "argv", "text/plain", name)
	}
	return s.write(b)
}

// label turns a parameter name into a label: "private-key" becomes
// "Private key".
func label(name string) string {
	l := strings.ReplaceAll(name, "-", " ")
	if l == "" {
		return l
	}
	return strings.ToUpper(l[:1]) + l[1:]
}

// secret reports whether a parameter is not to be echoed: passwords and
// passphrases.
func secret(name string) bool {
	n := strings.ToLower(name)
	return strings.Contains(n, "password") || strings.Contains(n, "passphrase")
}

func streamIndex(args []string) (int, bool) {
	if len(args) == 0 {
		return 0, false
	}
	i, err := strconv.Atoi(args[0])
	return i, err == nil
}

// clipboardBlob adds a blob to a clipboard stream, and reports whether
// the blob was one. Clipboard streams are not acked: guacd sends them
// without waiting (src/common/clipboard.c), and guacamole-common-js's
// StringReader does not ack either.
func (s *session) clipboardBlob(args []string) bool {
	i, ok := streamIndex(args)
	c := s.clips[i]
	if !ok || c == nil {
		return false
	}
	if len(args) < 2 || len(c.data) >= maxClipboard {
		return true
	}
	data, err := base64.StdEncoding.AppendDecode(c.data, []byte(args[1]))
	if err != nil {
		s.count("clipboard failed")
		delete(s.clips, i)
		return true
	}
	c.data = data[:min(len(data), maxClipboard)]
	return true
}

// clipboardEnd ends a clipboard stream, and reports whether the stream
// was one.
func (s *session) clipboardEnd(args []string) bool {
	i, ok := streamIndex(args)
	c := s.clips[i]
	if !ok || c == nil {
		return false
	}
	delete(s.clips, i)
	s.sink.Clipboard(c.mime, c.data)
	return true
}

// transparent reports whether img has no visible pixel.
func transparent(img *image.RGBA) bool {
	for i := 3; i < len(img.Pix); i += 4 {
		if img.Pix[i] != 0 {
			return false
		}
	}
	return true
}
