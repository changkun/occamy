// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package stream

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"testing/synctest"
	"time"
	"unicode/utf8"

	"github.com/gen2brain/vpx/webp"

	"changkun.de/x/occamy/internal/backend"
	"changkun.de/x/occamy/internal/backend/backendtest"
	"changkun.de/x/occamy/internal/codec"
	"changkun.de/x/occamy/internal/session"
	"changkun.de/x/occamy/internal/terminal"
	"changkun.de/x/occamy/protocol"
)

// client is a protocol v1 client for tests: it draws raw tiles into its
// own screen, acks every frame, and records what it received and when.
type client struct {
	c     Conn
	start time.Time

	mu      sync.Mutex
	img     *image.RGBA
	types   []protocol.Type
	frames  []frameRecord
	titles  []timedTitle
	close   *protocol.Close
	welcome *protocol.Welcome
	tiles   int
	codecs  map[protocol.Codec]int // tiles received in each codec
	cursors []protocol.Cursor
	clips   int // server clipboards received
	pongs   []uint32
	// errs holds messages the client could not decode, which a client
	// answers by closing the connection.
	errs []error
	// outside reports a tile outside the screen the client was told of.
	outside bool
	// term is the client's terminal, made at term-snapshot from the size
	// of the last resize; snapshots counts the term-snapshots.
	term      *terminal.Terminal
	size      image.Point
	snapshots int
	// noAcks stops the client acknowledging frames, as one whose
	// renderer stalls.
	noAcks bool
	// onFrame, if set, runs at each frame-end with the lock held.
	onFrame func(f frameRecord)
}

type frameRecord struct {
	id      uint32
	lastSeq uint32
	at      time.Duration
	tiles   int
	rects   []image.Rectangle // of its tiles, in the order they came
}

type timedTitle struct {
	title string
	at    time.Duration
	tiles int // received before it
}

func newClient(c Conn) *client {
	return &client{c: c, start: time.Now(), img: &image.RGBA{}, codecs: map[protocol.Codec]int{}}
}

// decodeLossy decodes a WebP or JPEG payload of a w×h rectangle as a
// client should. It checks the size the header claims against the
// rectangle before it decodes, since a few bytes of header can claim a
// frame that takes hundreds of megabytes to decode into. It decodes
// lossy WebP as libwebp, and so a browser, does: an image.YCbCr or
// image.NYCbCrA drawn with the standard library's conversion, which is
// JPEG's full-range one, misreads VP8's limited-range colours, by about
// 10 dB on the golden vectors. A lossless WebP image, a cursor, comes
// back as an image.NRGBA, with straight alpha.
func decodeLossy(c protocol.Codec, payload []byte, w, h int) (image.Image, error) {
	var cfg image.Config
	var err error
	switch c {
	case protocol.CodecWebP:
		cfg, err = webp.DecodeConfig(bytes.NewReader(payload))
	case protocol.CodecJPEG:
		cfg, err = jpeg.DecodeConfig(bytes.NewReader(payload))
	default:
		return nil, fmt.Errorf("codec %v is not lossy", c)
	}
	if err != nil {
		return nil, err
	}
	if cfg.Width != w || cfg.Height != h {
		return nil, fmt.Errorf("a %v header claims %d×%d for a %d×%d rectangle", c, cfg.Width, cfg.Height, w, h)
	}
	if c == protocol.CodecJPEG {
		return jpeg.Decode(bytes.NewReader(payload))
	}
	opts := webp.Options{Threads: 1, FrameSizeLimit: w * h}
	m, err := webp.Decode(bytes.NewReader(payload), opts)
	if _, lossless := m.(*image.NRGBA); err != nil || lossless {
		return m, err
	}
	opts.ToRGBA = true
	return webp.Decode(bytes.NewReader(payload), opts)
}

// decodeTile decodes a tile's payload into dst, its rectangle of the
// screen, as a client does.
func decodeTile(dst *image.RGBA, c protocol.Codec, payload []byte) error {
	var m image.Image
	var err error
	switch c {
	case protocol.CodecRaw:
		return codec.DecodeRaw(dst, payload)
	case protocol.CodecDeflate:
		var in codec.Inflater
		return in.Decode(dst, payload)
	case protocol.CodecWebP, protocol.CodecJPEG:
		m, err = decodeLossy(c, payload, dst.Rect.Dx(), dst.Rect.Dy())
	default:
		return fmt.Errorf("tile in codec %v", c)
	}
	if err != nil {
		return err
	}
	if m.Bounds().Size() != dst.Rect.Size() {
		return fmt.Errorf("%v tile of %v decodes to %v", c, dst.Rect.Size(), m.Bounds().Size())
	}
	if n, ok := m.(*image.NRGBA); ok {
		// Lossless WebP keeps straight alpha.
		for y := range dst.Rect.Dy() {
			o := dst.PixOffset(dst.Rect.Min.X, dst.Rect.Min.Y+y)
			copy(dst.Pix[o:o+4*dst.Rect.Dx()], n.Pix[y*n.Stride:])
		}
		return nil
	}
	draw.Draw(dst, dst.Rect, m, m.Bounds().Min, draw.Src)
	return nil
}

func (cl *client) run(ctx context.Context) {
	reg := protocol.ServerMessages()
	var rects []image.Rectangle
	for {
		b, err := cl.c.ReadMessage(ctx)
		if err != nil {
			return
		}
		m, err := protocol.Decode(b, reg, protocol.Limits{MaxMessageSize: 1 << 30})
		cl.mu.Lock()
		if cl.welcome != nil && err == nil {
			// Every message after welcome is within the agreed size.
			err = cl.welcome.Capabilities.Limits().CheckSize(len(b))
		}
		if err != nil {
			cl.errs = append(cl.errs, err)
			cl.mu.Unlock()
			continue
		}
		cl.types = append(cl.types, m.Type())
		var ackID uint32
		switch m := m.(type) {
		case *protocol.Welcome:
			cl.welcome = m
		case *protocol.Resize:
			cl.img = image.NewRGBA(image.Rect(0, 0, int(m.Width), int(m.Height)))
			cl.size = image.Pt(int(m.Width), int(m.Height))
			if cl.term != nil {
				cl.term.Resize(cl.size.X, cl.size.Y)
			}
		case *protocol.TermSnapshot:
			if !utf8.Valid(m.Data) {
				cl.errs = append(cl.errs, errors.New("term-snapshot split inside a character"))
			}
			cl.term = terminal.New(cl.size.X, cl.size.Y, terminal.Options{})
			cl.term.Write(m.Data)
			cl.snapshots++
		case *protocol.TermData:
			if cl.term == nil {
				cl.errs = append(cl.errs, errors.New("term-data before term-snapshot"))
				break
			}
			if !utf8.Valid(m.Data) {
				cl.errs = append(cl.errs, errors.New("term-data split inside a character"))
			}
			cl.term.Write(m.Data)
		case *protocol.Tile:
			r := image.Rect(int(m.X), int(m.Y), int(m.X)+int(m.W), int(m.Y)+int(m.H))
			if !r.In(cl.img.Rect) {
				cl.outside = true
				break
			}
			if err := decodeTile(cl.img.SubImage(r).(*image.RGBA), m.Codec, m.Payload); err != nil {
				cl.errs = append(cl.errs, err)
				break
			}
			rects = append(rects, r)
			cl.tiles++
			cl.codecs[m.Codec]++
		case *protocol.FrameEnd:
			f := frameRecord{id: m.FrameID, lastSeq: m.LastSeq, at: time.Since(cl.start), tiles: len(rects), rects: rects}
			cl.frames = append(cl.frames, f)
			if cl.onFrame != nil {
				cl.onFrame(f)
			}
			rects = nil
			ackID = m.FrameID
		case *protocol.Cursor:
			c := *m
			c.Payload = slices.Clone(m.Payload)
			cl.cursors = append(cl.cursors, c)
		case *protocol.ServerClipboard:
			cl.clips++
		case *protocol.Pong:
			cl.pongs = append(cl.pongs, m.Nonce)
		case *protocol.Title:
			cl.titles = append(cl.titles, timedTitle{m.Title, time.Since(cl.start), cl.tiles})
		case *protocol.Close:
			c := *m
			cl.close = &c
		}
		noAcks := cl.noAcks
		cl.mu.Unlock()
		if ackID != 0 && !noAcks {
			_ = cl.c.WriteMessage(ctx, protocol.FrameAck{FrameID: ackID, DecodeTime: 500}.Append(nil))
		}
	}
}

func (cl *client) send(t *testing.T, m interface{ Append([]byte) []byte }) {
	t.Helper()
	if err := cl.c.WriteMessage(context.Background(), m.Append(nil)); err != nil {
		t.Fatal(err)
	}
}

// titled reports how many times the title arrived; the lock is held.
func (cl *client) titled(title string) int {
	n := 0
	for _, t := range cl.titles {
		if t.title == title {
			n++
		}
	}
	return n
}

// screen returns a copy of what the client shows.
func (cl *client) screen() *image.RGBA {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	img := image.NewRGBA(cl.img.Rect)
	copy(img.Pix, cl.img.Pix)
	return img
}

// serverScreen reads the session's framebuffer whole.
func serverScreen(s *session.Session) *image.RGBA {
	fb := s.Framebuffer()
	img := image.NewRGBA(fb.Bounds())
	cols, rows := fb.Grid()
	pix := make([]byte, 4*fb.TileSize()*fb.TileSize())
	for i := range cols * rows {
		r, _ := fb.ReadTile(i, pix)
		src := &image.RGBA{Pix: pix, Stride: 4 * r.Dx(), Rect: image.Rect(0, 0, r.Dx(), r.Dy())}
		draw.Draw(img, r, src, image.Point{}, draw.Src)
	}
	return img
}

// rig is a session served to a test client over a simulated link.
type rig struct {
	s      *session.Session
	owner  *session.Viewer
	link   *link
	cl     *client
	served chan error
}

func newRig(t *testing.T, b backend.Backend, rate float64, delay time.Duration, hello protocol.Hello) *rig {
	t.Helper()
	return newRigTiles(t, b, 64, rate, delay, hello)
}

// newRigTiles is newRig with tiles of the given side.
func newRigTiles(t *testing.T, b backend.Backend, tile int, rate float64, delay time.Duration, hello protocol.Hello) *rig {
	t.Helper()
	return newRigConfig(t, b, tile, rate, delay, hello, Config{SessionID: "s1"})
}

// newRigConfig is newRigTiles with the stream's configuration.
func newRigConfig(t *testing.T, b backend.Backend, tile int, rate float64, delay time.Duration, hello protocol.Hello, cfg Config) *rig {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	s, owner, err := session.Start(ctx, session.Config{Backend: b, TileSize: tile})
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{s: s, owner: owner, link: newLink(rate, delay), served: make(chan error, 1)}
	r.cl = newClient(r.link.client())
	go r.cl.run(ctx)
	go func() {
		r.served <- Serve(ctx, r.link.server(), owner, hello, cfg)
	}()
	t.Cleanup(func() {
		cancel()
		s.Close()
		r.link.close()
		<-r.served
		r.cl.mu.Lock()
		defer r.cl.mu.Unlock()
		if len(r.cl.errs) > 0 {
			t.Errorf("the client could not decode %d messages, the first: %v", len(r.cl.errs), r.cl.errs[0])
		}
	})
	return r
}

// rawOnly are the capabilities of a client that decodes raw tiles.
var rawOnly = protocol.Capabilities{Codecs: []protocol.Codec{protocol.CodecRaw}}

var (
	visible = protocol.Hello{Visible: true, Capabilities: rawOnly}
	hidden  = protocol.Hello{Capabilities: rawOnly}
)

func TestConverges(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRig(t, backendtest.NewScreen(200, 150, 0), 50e6, time.Millisecond, visible)
		time.Sleep(time.Second)
		synctest.Wait()
		want, got := serverScreen(r.s), r.cl.screen()
		if got.Rect != want.Rect || !bytes.Equal(got.Pix, want.Pix) {
			t.Fatalf("client screen %v differs from the server's %v", got.Rect, want.Rect)
		}
		r.cl.mu.Lock()
		defer r.cl.mu.Unlock()
		if r.cl.welcome == nil || r.cl.types[0] != protocol.TypeWelcome {
			t.Fatalf("first message %v, want welcome", r.cl.types[0])
		}
		if r.cl.welcome.SessionID != "s1" || r.cl.welcome.Role != protocol.RoleOwner {
			t.Errorf("welcome %+v", r.cl.welcome)
		}
		if r.cl.outside {
			t.Error("a tile arrived outside the screen the client was told of")
		}
		// 200x150 in 64-pixel tiles is 4x3; the screen was drawn twice
		// before the writer looked, so one frame carries all of it.
		if r.cl.tiles != 12 {
			t.Errorf("%d tiles sent for a still 4x3 screen, want 12", r.cl.tiles)
		}
	})
}

// tapped exposes the sink a backend runs with.
type tapped struct {
	backend.Backend
	mu   sync.Mutex
	sink backend.Sink
}

func (b *tapped) Run(ctx context.Context, sink backend.Sink) error {
	b.mu.Lock()
	b.sink = sink
	b.mu.Unlock()
	return b.Backend.Run(ctx, sink)
}

func (b *tapped) Sink() backend.Sink {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sink
}

// Small state does not wait behind a large frame.
func TestSmallStateAhead(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// 1024x768 raw is 3 MiB: three seconds at 1 MB/s.
		b := &tapped{Backend: backendtest.NewScreen(1024, 768, 0)}
		r := newRig(t, b, 1e6, 10*time.Millisecond, visible)
		time.Sleep(500 * time.Millisecond)
		set := time.Since(r.cl.start)
		b.Sink().Title("hello")
		time.Sleep(3 * time.Second)
		r.cl.mu.Lock()
		defer r.cl.mu.Unlock()
		if len(r.cl.titles) == 0 {
			t.Fatal("no title")
		}
		// The title waits for at most a tile in the send buffer and one
		// being written: 2 x 16 KiB at 1 MB/s, plus the delay.
		last := r.cl.titles[len(r.cl.titles)-1]
		if lag := last.at - set; last.title != "hello" || lag > 50*time.Millisecond {
			t.Errorf("title %q arrived %v after it was set", last.title, lag)
		}
		// 1024x768 in 64-pixel tiles is 16x12.
		if last.tiles >= 16*12 || r.cl.tiles < 16*12 {
			t.Errorf("%d of %d tiles arrived before the title; the test does not show the title overtaking tiles", last.tiles, r.cl.tiles)
		}
	})
}

// A hidden client is sent no frames, and is sent the current screen as
// soon as it is visible.
func TestHidden(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRig(t, backendtest.NewScreen(200, 150, 10*time.Millisecond), 50e6, 5*time.Millisecond, hidden)
		time.Sleep(time.Second)
		r.cl.mu.Lock()
		frames := len(r.cl.frames)
		r.cl.mu.Unlock()
		if frames != 0 {
			t.Fatalf("%d frames sent to a hidden client", frames)
		}
		shown := time.Since(r.cl.start)
		r.cl.send(t, protocol.View{Visible: true})
		time.Sleep(100 * time.Millisecond)
		r.cl.mu.Lock()
		defer r.cl.mu.Unlock()
		if len(r.cl.frames) == 0 {
			t.Fatal("no frame after the client became visible")
		}
		// The view takes one delay to arrive and the frame another.
		if f := r.cl.frames[0]; f.at-shown > 15*time.Millisecond || f.tiles != 12 {
			t.Errorf("first frame after %v with %d tiles, want within 15ms with all 12", f.at-shown, f.tiles)
		}
	})
}

// Tiles outside the viewport wait until they show.
func TestViewport(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRig(t, backendtest.NewScreen(256, 256, 0), 50e6, time.Millisecond, hidden)
		r.cl.send(t, protocol.View{Visible: true, X: 0, Y: 0, W: 100, H: 60})
		time.Sleep(time.Second)
		r.cl.mu.Lock()
		tiles := r.cl.tiles
		r.cl.mu.Unlock()
		if tiles != 2 {
			t.Fatalf("%d tiles sent for a viewport over 2 tiles", tiles)
		}
		r.cl.send(t, protocol.View{Visible: true})
		time.Sleep(time.Second)
		r.cl.mu.Lock()
		defer r.cl.mu.Unlock()
		if r.cl.tiles != 16 {
			t.Errorf("%d tiles after the viewport grew to the screen, want 16", r.cl.tiles)
		}
	})
}

// delayed applies input after a delay, as a remote server does one round
// trip away.
type delayed struct {
	backend.Backend
	d time.Duration
}

func (b delayed) Input(ev backend.InputEvent) error {
	time.AfterFunc(b.d, func() { _ = b.Backend.Input(ev) })
	return nil
}

// A key's effect reaches the client within a round trip of the effect
// appearing, plus a tile, while full-screen motion keeps the link busy.
func TestInputFrame(t *testing.T) {
	for _, c := range []struct {
		name   string
		effect time.Duration // from the input reaching the backend to its effect
	}{
		{"local", 0},
		{"remote", 40 * time.Millisecond},
	} {
		t.Run(c.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				const (
					delay = 20 * time.Millisecond // one way, 40 ms round trip
					rate  = 5e6
					// The swatch's own tile, so that motion does not
					// repaint it; raw, a 32-pixel tile is 4 KiB, about what
					// a coded 64-pixel tile will be.
					tile      = backendtest.SwatchSize
					tileBytes = 4 * tile * tile
				)
				var b backend.Backend = backendtest.NewScreen(640, 480, 10*time.Millisecond)
				if c.effect > 0 {
					b = delayed{b, c.effect}
				}
				// 640x480 raw is 1.2 MB a frame; at 5 MB/s motion alone
				// takes 4 frames a second, so the link stays full.
				r := newRigTiles(t, b, tile, rate, delay, visible)
				time.Sleep(2 * time.Second)

				want := backendtest.SwatchColor('k')
				var arrived time.Duration
				var carrier frameRecord
				r.cl.mu.Lock()
				r.cl.onFrame = func(f frameRecord) {
					if arrived == 0 && r.cl.img.RGBAAt(1, 1) == want {
						arrived, carrier = f.at, f
					}
				}
				r.cl.mu.Unlock()
				pressed := time.Since(r.cl.start)
				r.cl.send(t, protocol.Key{Seq: 1, Down: true, Keysym: 'k'})
				time.Sleep(2 * time.Second)
				r.cl.mu.Lock()
				defer r.cl.mu.Unlock()
				if arrived == 0 {
					t.Fatal("the key's effect never arrived")
				}
				// The key takes one delay to arrive and the effect
				// c.effect to appear. The swatch's tile then waits for at
				// most three tiles, one on the wire, one in the send buffer
				// and one the writer is writing, is sent itself and takes
				// one delay back.
				lag := arrived - pressed
				bound := 2*delay + c.effect + time.Duration(4*tileBytes/rate*float64(time.Second))
				if lag > bound {
					t.Errorf("key to effect %v, want at most %v", lag, bound)
				}
				// The swatch ends its frame: the frame-end that shows it
				// waits behind no motion.
				if n := len(carrier.rects); n == 0 || carrier.rects[n-1] != backendtest.Swatch {
					t.Errorf("frame %d showed the key with %d tiles after the swatch's", carrier.id, n-1-slices.Index(carrier.rects, backendtest.Swatch))
				}
				t.Logf("key to effect %v (bound %v), frame %d of %d tiles, %d frames", lag, bound, carrier.id, carrier.tiles, len(r.cl.frames))
			})
		})
	}
}

// The client is told of a resize before any tile of the new size.
func TestResize(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := &tapped{Backend: backendtest.NewScreen(200, 150, 10*time.Millisecond)}
		r := newRig(t, b, 2e6, 5*time.Millisecond, visible)
		time.Sleep(300 * time.Millisecond)
		sink := b.Sink()
		sink.Resize(320, 240)
		img := sink.Lock(image.Rect(0, 0, 320, 240))
		draw.Draw(img, img.Rect, image.NewUniform(color.RGBA{R: 0xff, A: 0xff}), image.Point{}, draw.Src)
		sink.Unlock(image.Rect(0, 0, 320, 240))
		time.Sleep(2 * time.Second)
		synctest.Wait()
		r.cl.mu.Lock()
		outside := r.cl.outside
		r.cl.mu.Unlock()
		if outside {
			t.Error("a tile of the new size arrived before the resize")
		}
		if got := r.cl.screen().Rect; got != image.Rect(0, 0, 320, 240) {
			t.Errorf("client screen %v after the resize", got)
		}
	})
}

// When the session ends, the client is told why and Serve returns.
func TestSessionEnd(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRig(t, backendtest.NewScreen(200, 150, 0), 50e6, time.Millisecond, visible)
		time.Sleep(100 * time.Millisecond)
		r.s.Close()
		if err := <-r.served; err != nil {
			t.Errorf("Serve returned %v at the session's end", err)
		}
		r.served <- nil // for the cleanup
		time.Sleep(10 * time.Millisecond)
		r.cl.mu.Lock()
		defer r.cl.mu.Unlock()
		if r.cl.close == nil || r.cl.close.Code != protocol.CloseSessionClosed {
			t.Errorf("close %+v, want session closed", r.cl.close)
		}
	})
}

// A client that breaks the protocol is told so, and the viewer leaves.
func TestProtocolError(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRig(t, backendtest.NewScreen(200, 150, 0), 50e6, time.Millisecond, visible)
		time.Sleep(100 * time.Millisecond)
		if err := r.link.client().WriteMessage(context.Background(), []byte{0xff, 0xff, 0, 0}); err != nil {
			t.Fatal(err)
		}
		err := <-r.served
		r.served <- nil
		if !errors.Is(err, protocol.ErrUnsupported) {
			t.Errorf("Serve returned %v, want unsupported", err)
		}
		time.Sleep(10 * time.Millisecond)
		r.cl.mu.Lock()
		defer r.cl.mu.Unlock()
		if r.cl.close == nil || r.cl.close.Code != protocol.CloseUnsupported {
			t.Errorf("close %+v, want unsupported", r.cl.close)
		}
	})
}

// Small state sent while a tile waits for the pacer does not replace the
// tile, and every ping is answered.
func TestPacedSmallState(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// 1024x768 raw is 3 MiB: three seconds at 1 MB/s, paced.
		b := &tapped{Backend: backendtest.NewScreen(1024, 768, 0)}
		r := newRig(t, b, 1e6, 10*time.Millisecond, visible)
		time.Sleep(500 * time.Millisecond)
		b.Sink().Title("hello")
		const pings = 400
		for i := range pings {
			r.cl.send(t, protocol.Ping{Nonce: uint32(i + 1)}) // #nosec G115 -- a small count
			time.Sleep(5 * time.Millisecond)
		}
		time.Sleep(5 * time.Second)
		synctest.Wait()
		want, got := serverScreen(r.s), r.cl.screen()
		if got.Rect != want.Rect || !bytes.Equal(got.Pix, want.Pix) {
			t.Errorf("client screen differs from the server's")
		}
		r.cl.mu.Lock()
		defer r.cl.mu.Unlock()
		if n := r.cl.titled("hello"); n != 1 {
			t.Errorf("title sent %d times, want once", n)
		}
		if len(r.cl.pongs) != pings || !slices.IsSorted(r.cl.pongs) {
			t.Errorf("%d pongs for %d pings", len(r.cl.pongs), pings)
		}
	})
}

// A hidden cursor carries no hot spot, codec or payload, and a shown
// cursor's hot spot is inside its image.
func TestCursor(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := &tapped{Backend: backendtest.NewScreen(200, 150, 0)}
		r := newRig(t, b, 50e6, time.Millisecond, visible)
		time.Sleep(100 * time.Millisecond)
		img := image.NewRGBA(image.Rect(0, 0, 8, 8))
		img.Pix[0], img.Pix[3] = 0xff, 0xff // opaque red
		b.Sink().Cursor(img, image.Pt(20, -3))
		time.Sleep(100 * time.Millisecond)
		b.Sink().Cursor(nil, image.Pt(3, 4))
		time.Sleep(100 * time.Millisecond)
		r.cl.mu.Lock()
		defer r.cl.mu.Unlock()
		if len(r.cl.cursors) != 2 {
			t.Fatalf("%d cursors, want 2", len(r.cl.cursors))
		}
		if c := r.cl.cursors[0]; c.W != 8 || c.H != 8 || c.HotX != 7 || c.HotY != 0 || c.Payload[0] != 0xff {
			t.Errorf("shown cursor %dx%d hot %d,%d", c.W, c.H, c.HotX, c.HotY)
		}
		if c := r.cl.cursors[1]; c.W != 0 || c.Codec != 0 || c.HotX != 0 || len(c.Payload) != 0 {
			t.Errorf("hidden cursor %+v", c)
		}
	})
}

// While typing, each key's effect goes first, not only the first key's:
// a key-up, which shows nothing, and the previous key's effect on the same
// tile take no priority from the next key.
func TestTyping(t *testing.T) {
	for _, c := range []struct {
		name  string
		keyUp bool          // a key-up 80 ms after the first key
		gap   time.Duration // from the first key to the second
	}{
		{"down down 60ms", false, 60 * time.Millisecond},
		{"down down 90ms", false, 90 * time.Millisecond},
		{"down down 130ms", false, 130 * time.Millisecond},
		{"down up down 150ms", true, 150 * time.Millisecond},
		{"down up down 400ms", true, 400 * time.Millisecond},
	} {
		t.Run(c.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				const (
					delay     = 20 * time.Millisecond
					rate      = 5e6
					tile      = backendtest.SwatchSize
					tileBytes = 4 * tile * tile
				)
				r := newRigTiles(t, backendtest.NewScreen(640, 480, 10*time.Millisecond), tile, rate, delay, visible)
				time.Sleep(2 * time.Second)
				wantK, wantJ := backendtest.SwatchColor('k'), backendtest.SwatchColor('j')
				var arrK, arrJ time.Duration
				r.cl.mu.Lock()
				r.cl.onFrame = func(f frameRecord) {
					switch px := r.cl.img.RGBAAt(1, 1); {
					case arrK == 0 && px == wantK:
						arrK = f.at
					case arrJ == 0 && px == wantJ:
						arrJ = f.at
					}
				}
				r.cl.mu.Unlock()
				pk := time.Since(r.cl.start)
				r.cl.send(t, protocol.Key{Seq: 1, Down: true, Keysym: 'k'})
				seq := uint32(2)
				if c.keyUp {
					time.Sleep(80 * time.Millisecond)
					r.cl.send(t, protocol.Key{Seq: 2, Keysym: 'k'})
					seq = 3
					time.Sleep(c.gap - 80*time.Millisecond)
				} else {
					time.Sleep(c.gap)
				}
				pj := time.Since(r.cl.start)
				r.cl.send(t, protocol.Key{Seq: seq, Down: true, Keysym: 'j'})
				time.Sleep(2 * time.Second)
				r.cl.mu.Lock()
				defer r.cl.mu.Unlock()
				bound := 2*delay + time.Duration(4*tileBytes/rate*float64(time.Second))
				if arrK == 0 || arrK-pk > bound {
					t.Errorf("first key's effect after %v, want at most %v", arrK-pk, bound)
				}
				if arrJ == 0 || arrJ-pj > bound {
					t.Errorf("second key's effect after %v, want at most %v", arrJ-pj, bound)
				}
			})
		})
	}
}

// The frame that shows an input's effect reports that input in last_seq,
// also when the effect joins a frame that started before the input.
func TestLastSeq(t *testing.T) {
	for _, c := range []struct {
		name   string
		b      func() backend.Backend
		w, h   int
		rate   float64
		before time.Duration // from the start to the key
	}{
		{"remote", func() backend.Backend {
			return delayed{backendtest.NewScreen(640, 480, 10*time.Millisecond), 40 * time.Millisecond}
		}, 640, 480, 5e6, 2 * time.Second},
		// A still 1024x768 screen takes three seconds at 1 MB/s: the key
		// comes during its frames.
		{"mid-frame 503ms", func() backend.Backend { return backendtest.NewScreen(1024, 768, 0) }, 1024, 768, 1e6, 503 * time.Millisecond},
		{"mid-frame 541ms", func() backend.Backend { return backendtest.NewScreen(1024, 768, 0) }, 1024, 768, 1e6, 541 * time.Millisecond},
		{"mid-frame 589ms", func() backend.Backend { return backendtest.NewScreen(1024, 768, 0) }, 1024, 768, 1e6, 589 * time.Millisecond},
	} {
		t.Run(c.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				r := newRigTiles(t, c.b(), backendtest.SwatchSize, c.rate, 20*time.Millisecond, visible)
				time.Sleep(c.before)
				want := backendtest.SwatchColor('k')
				var carrier *frameRecord
				r.cl.mu.Lock()
				r.cl.onFrame = func(f frameRecord) {
					if carrier == nil && r.cl.img.RGBAAt(1, 1) == want {
						carrier = &f
					}
				}
				r.cl.mu.Unlock()
				r.cl.send(t, protocol.Key{Seq: 1, Down: true, Keysym: 'k'})
				time.Sleep(5 * time.Second)
				r.cl.mu.Lock()
				defer r.cl.mu.Unlock()
				if carrier == nil {
					t.Fatal("the key's effect never arrived")
				}
				if carrier.lastSeq != 1 {
					t.Errorf("frame %d shows key 1's effect but reports last_seq %d", carrier.id, carrier.lastSeq)
				}
			})
		})
	}
}

// A client that hangs up is no error.
func TestHangup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRig(t, backendtest.NewScreen(200, 150, 0), 50e6, time.Millisecond, visible)
		time.Sleep(100 * time.Millisecond)
		r.link.close()
		err := <-r.served
		r.served <- nil
		if err != nil {
			t.Errorf("Serve returned %v for a client that hung up", err)
		}
	})
}

// stuck is a connection whose client stopped reading: writes after the
// first few block until their context is done.
type stuck struct {
	mu     sync.Mutex
	writes int
	read   chan []byte
}

func (c *stuck) WriteMessage(ctx context.Context, p []byte) error {
	c.mu.Lock()
	c.writes++
	n := c.writes
	c.mu.Unlock()
	if n <= 3 {
		return nil
	}
	<-ctx.Done()
	return ctx.Err()
}

func (c *stuck) ReadMessage(ctx context.Context) ([]byte, error) {
	select {
	case b := <-c.read:
		return b, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// panicky is a connection whose reads panic, as a bug in handling a
// client's message would on the reader's goroutine, and which keeps what
// was written last.
type panicky struct {
	mu   sync.Mutex
	last []byte
}

func (c *panicky) WriteMessage(_ context.Context, p []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.last = slices.Clone(p)
	return nil
}

func (*panicky) ReadMessage(context.Context) ([]byte, error) { panic("the reader's bug") }

// A panic on the reader's goroutine, which net/http's recover does not
// reach, ends the stream with internal-error rather than occamyd.
func TestReaderPanics(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		s, owner, err := session.Start(ctx, session.Config{Backend: backendtest.NewScreen(200, 150, 0)})
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		c := &panicky{}
		err = Serve(ctx, c, owner, visible, Config{Logger: slog.New(slog.DiscardHandler)})
		var p session.PanicError
		if !errors.As(err, &p) || p.Value != "the reader's bug" {
			t.Errorf("Serve returned %v, want the reader's panic", err)
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		m, err := protocol.Decode(c.last, protocol.ServerMessages(), protocol.Limits{})
		if cl, ok := m.(*protocol.Close); !ok || cl.Code != protocol.CloseInternalError {
			t.Errorf("last message %#v %v, want close internal-error", m, err)
		}
	})
}

// Serve returns after a protocol error even if the client stopped
// reading, without waiting for its context.
func TestCloseTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		s, owner, err := session.Start(ctx, session.Config{Backend: backendtest.NewScreen(200, 150, 0)})
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		c := &stuck{read: make(chan []byte, 1)}
		served := make(chan error, 1)
		go func() { served <- Serve(ctx, c, owner, visible, Config{}) }()
		time.Sleep(100 * time.Millisecond)
		start := time.Now()
		c.read <- []byte{0xff, 0xff, 0, 0}
		select {
		case <-served:
			if d := time.Since(start); d > closeTimeout {
				t.Errorf("Serve returned %v after the protocol error, want at most %v", d, closeTimeout)
			}
		case <-time.After(time.Hour):
			t.Fatal("Serve still blocked an hour after the protocol error")
		}
	})
}

// stallConfig is the configuration of the stall tests: a short viewer
// timeout, and no log of the viewers dropped.
var stallConfig = Config{SessionID: "s1", ViewerTimeout: 2 * time.Second, Logger: slog.New(slog.DiscardHandler)}

// servedErr reports what Serve returned, or false if it still serves.
func (r *rig) servedErr() (error, bool) {
	select {
	case err := <-r.served:
		r.served <- err // for the cleanup
		return err, true
	default:
		return nil, false
	}
}

// A visible viewer that acknowledges no frame for the viewer timeout is
// dropped with too-slow, and one that acknowledges its frames stays.
func TestStalledViewer(t *testing.T) {
	for _, acks := range []bool{false, true} {
		t.Run(fmt.Sprintf("acks=%v", acks), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				r := newRigConfig(t, backendtest.NewScreen(200, 150, 10*time.Millisecond), 64, 50e6, time.Millisecond, visible, stallConfig)
				r.cl.mu.Lock()
				r.cl.noAcks = !acks
				r.cl.mu.Unlock()
				time.Sleep(10 * time.Second)
				synctest.Wait()
				err, done := r.servedErr()
				r.cl.mu.Lock()
				defer r.cl.mu.Unlock()
				if acks {
					if done {
						t.Fatalf("Serve returned %v for a viewer that acknowledges its frames", err)
					}
					return
				}
				if !errors.Is(err, ErrStalled) {
					t.Fatalf("Serve returned %v, %v; want ErrStalled", err, done)
				}
				if r.cl.close == nil || r.cl.close.Code != protocol.CloseTooSlow {
					t.Errorf("close %+v, want too-slow", r.cl.close)
				}
				// The first frame ends within a few milliseconds: the
				// viewer goes the viewer timeout after it.
				if n := len(r.cl.frames); n == 0 || r.cl.frames[0].at > 50*time.Millisecond {
					t.Fatalf("frames %+v, want the first within 50ms", r.cl.frames)
				}
			})
		})
	}
}

// The viewer goes when its timeout has passed, not before.
func TestStalledViewerTimely(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRigConfig(t, backendtest.NewScreen(200, 150, 10*time.Millisecond), 64, 50e6, time.Millisecond, visible, stallConfig)
		r.cl.mu.Lock()
		r.cl.noAcks = true
		r.cl.mu.Unlock()
		time.Sleep(stallConfig.ViewerTimeout - 100*time.Millisecond)
		synctest.Wait()
		if err, done := r.servedErr(); done {
			t.Fatalf("Serve returned %v before the viewer timeout", err)
		}
		time.Sleep(200 * time.Millisecond)
		synctest.Wait()
		if err, _ := r.servedErr(); !errors.Is(err, ErrStalled) {
			t.Fatalf("Serve returned %v 100ms after the viewer timeout, want ErrStalled", err)
		}
	})
}

// An ack starts the viewer's time over, though a frame it does not cover
// has awaited one for longer.
func TestStalledAckRestarts(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRigConfig(t, backendtest.NewScreen(200, 150, 10*time.Millisecond), 64, 50e6, time.Millisecond, visible, stallConfig)
		r.cl.mu.Lock()
		r.cl.noAcks = true
		r.cl.mu.Unlock()
		time.Sleep(stallConfig.ViewerTimeout * 3 / 4)
		r.cl.mu.Lock()
		frames := slices.Clone(r.cl.frames)
		r.cl.mu.Unlock()
		if len(frames) < 2 {
			t.Fatalf("%d frames, want a second awaiting an ack after the first's", len(frames))
		}
		r.cl.send(t, protocol.FrameAck{FrameID: frames[0].id})
		time.Sleep(stallConfig.ViewerTimeout - 100*time.Millisecond)
		synctest.Wait()
		if err, done := r.servedErr(); done {
			t.Fatalf("Serve returned %v though the viewer acknowledged a frame within the timeout", err)
		}
		time.Sleep(200 * time.Millisecond)
		synctest.Wait()
		if err, _ := r.servedErr(); !errors.Is(err, ErrStalled) {
			t.Fatalf("Serve returned %v, want ErrStalled once the timeout passed since the ack", err)
		}
	})
}

// A hidden viewer's time runs as a visible one's: hidden, it gets no
// new frames, but must acknowledge those it was sent; and one that does
// stays, hidden, for as long as it likes.
func TestStalledHidden(t *testing.T) {
	for _, acks := range []bool{false, true} {
		t.Run(fmt.Sprintf("acks=%v", acks), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				r := newRigConfig(t, backendtest.NewScreen(200, 150, 10*time.Millisecond), 64, 50e6, time.Millisecond, visible, stallConfig)
				r.cl.mu.Lock()
				r.cl.noAcks = !acks
				r.cl.mu.Unlock()
				time.Sleep(stallConfig.ViewerTimeout / 4)
				r.cl.send(t, protocol.View{Visible: false})
				time.Sleep(stallConfig.ViewerTimeout / 2)
				synctest.Wait()
				if err, done := r.servedErr(); done {
					t.Fatalf("Serve returned %v before the viewer timeout", err)
				}
				time.Sleep(10 * time.Second)
				synctest.Wait()
				err, done := r.servedErr()
				if acks && done {
					t.Fatalf("Serve returned %v for a hidden viewer that acknowledged its frames", err)
				}
				if !acks && !errors.Is(err, ErrStalled) {
					t.Fatalf("Serve returned %v, %v for a hidden viewer that acknowledged nothing; want ErrStalled", err, done)
				}
			})
		})
	}
}

// A write that blocks for the viewer timeout drops the viewer, though no
// frame was completed for it to leave unacknowledged.
func TestBlockedWrite(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		s, owner, err := session.Start(ctx, session.Config{Backend: backendtest.NewScreen(200, 150, 0)})
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		c := &stuck{read: make(chan []byte, 1)}
		start := time.Now()
		err = Serve(ctx, c, owner, visible, stallConfig)
		if !errors.Is(err, ErrStalled) {
			t.Fatalf("Serve returned %v, want ErrStalled", err)
		}
		// The close for the stalled viewer blocks too, for closeTimeout.
		if d, most := time.Since(start), stallConfig.ViewerTimeout+closeTimeout; d < stallConfig.ViewerTimeout || d > most {
			t.Errorf("Serve returned after %v, want between %v and %v", d, stallConfig.ViewerTimeout, most)
		}
	})
}

// A hello after the handshake is a protocol error.
func TestHelloAfterHandshake(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRig(t, backendtest.NewScreen(200, 150, 0), 50e6, time.Millisecond, visible)
		time.Sleep(100 * time.Millisecond)
		r.cl.send(t, protocol.Hello{Visible: true})
		err := <-r.served
		r.served <- nil
		if !errors.Is(err, protocol.ErrInvalid) {
			t.Errorf("Serve returned %v, want invalid", err)
		}
		time.Sleep(10 * time.Millisecond)
		r.cl.mu.Lock()
		defer r.cl.mu.Unlock()
		if r.cl.close == nil || r.cl.close.Code != protocol.CloseProtocolError {
			t.Errorf("close %+v, want protocol error", r.cl.close)
		}
	})
}

// A client that decodes no codec the server sends is refused, whether it
// lists none or only codecs the server does not send.
func TestNoCodec(t *testing.T) {
	for _, codecs := range [][]protocol.Codec{nil, {protocol.CodecPNG, protocol.CodecCached}} {
		synctest.Test(t, func(t *testing.T) {
			hello := protocol.Hello{Visible: true, Capabilities: protocol.Capabilities{Codecs: codecs}}
			r := newRig(t, backendtest.NewScreen(200, 150, 0), 50e6, time.Millisecond, hello)
			if err := <-r.served; err != nil {
				t.Errorf("%v: Serve returned %v", codecs, err)
			}
			r.served <- nil
			time.Sleep(10 * time.Millisecond)
			r.cl.mu.Lock()
			defer r.cl.mu.Unlock()
			if r.cl.close == nil || r.cl.close.Code != protocol.CloseUnsupported || r.cl.tiles != 0 {
				t.Errorf("%v: close %+v after %d tiles, want unsupported before any", codecs, r.cl.close, r.cl.tiles)
			}
		})
	}
}

// A message larger than the client takes is dropped, and the stream goes
// on.
func TestTooLarge(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := &tapped{Backend: backendtest.NewScreen(200, 150, 0)}
		r := newRig(t, b, 50e6, time.Millisecond, visible)
		time.Sleep(100 * time.Millisecond)
		b.Sink().Clipboard("text/plain", make([]byte, protocol.DefaultMaxMessageSize))
		b.Sink().Title("after")
		time.Sleep(100 * time.Millisecond)
		r.cl.mu.Lock()
		defer r.cl.mu.Unlock()
		if r.cl.clips != 0 || r.cl.titled("after") != 1 {
			t.Errorf("%d clipboards and %d titles, want the clipboard dropped and the title sent", r.cl.clips, r.cl.titled("after"))
		}
	})
}

// A client that floods acks loses the oldest.
func TestAcksKeepNewest(t *testing.T) {
	a := newAcks()
	for i := range maxAcks + 10 {
		a.put(ack{id: uint32(i)}) // #nosec G115 -- a small count
	}
	got := a.take(nil)
	if len(got) != maxAcks || got[0].id != 10 || got[len(got)-1].id != maxAcks+9 {
		t.Errorf("kept %d acks from %d to %d", len(got), got[0].id, got[len(got)-1].id)
	}
}

// An input that comes while an input with no visible effect is pending
// joins it: its effect keeps priority even when the first input's
// timeout passes before the effect shows.
func TestInputJoinsPending(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const (
			delay     = 20 * time.Millisecond
			effect    = 40 * time.Millisecond
			rate      = 5e6
			tile      = backendtest.SwatchSize
			tileBytes = 4 * tile * tile
		)
		b := delayed{backendtest.NewScreen(640, 480, 10*time.Millisecond), effect}
		r := newRigTiles(t, b, tile, rate, delay, visible)
		time.Sleep(2 * time.Second)
		want := backendtest.SwatchColor('j')
		var arrived time.Duration
		r.cl.mu.Lock()
		r.cl.onFrame = func(f frameRecord) {
			if arrived == 0 && r.cl.img.RGBAAt(1, 1) == want {
				arrived = f.at
			}
		}
		r.cl.mu.Unlock()
		// A key-up shows nothing; the key-down comes just before its
		// timeout, and shows after it.
		r.cl.send(t, protocol.Key{Seq: 1, Keysym: 'k'})
		time.Sleep(inputTimeout - effect/2)
		pressed := time.Since(r.cl.start)
		r.cl.send(t, protocol.Key{Seq: 2, Down: true, Keysym: 'j'})
		time.Sleep(2 * time.Second)
		r.cl.mu.Lock()
		defer r.cl.mu.Unlock()
		bound := 2*delay + effect + time.Duration(4*tileBytes/rate*float64(time.Second))
		if arrived == 0 || arrived-pressed > bound {
			t.Errorf("key's effect after %v, want at most %v", arrived-pressed, bound)
		}
	})
}

// failing is a terminal backend whose remote cannot be reached.
type failing struct{ backend.Backend }

func (failing) Run(context.Context, backend.Sink) error { return errors.New("connection refused") }

// sent records the messages written to it.
type sent struct {
	mu sync.Mutex
	p  [][]byte
}

func (c *sent) ReadMessage(ctx context.Context) ([]byte, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (c *sent) WriteMessage(_ context.Context, p []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.p = append(c.p, slices.Clone(p))
	return nil
}

// A session that ends before its stream begins, as when the backend
// fails at once, closes the stream with the session's end, not with an
// internal error.
func TestEndedBeforeServe(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, owner, err := session.Start(t.Context(), session.Config{Backend: failing{backendtest.NewTerminal(80, 24, 0)}})
		if err != nil {
			t.Fatal(err)
		}
		<-s.Done()
		c := &sent{}
		if err := Serve(t.Context(), c, owner, termHello, Config{}); err != nil {
			t.Errorf("Serve returned %v", err)
		}
		if len(c.p) != 1 {
			t.Fatalf("sent %d messages, want close", len(c.p))
		}
		var m protocol.Close
		h, body, err := protocol.Split(c.p[0], protocol.Limits{})
		if err != nil || h.Type != protocol.TypeClose || m.Decode(body, protocol.Limits{}) != nil {
			t.Fatalf("sent %x, want close", c.p[0])
		}
		if m.Code != protocol.CloseBackendFailed || m.Reason != session.Failed.String() {
			t.Errorf("close %+v, want backend failed", m)
		}
	})
}

// A resize-request is clamped to what a backend of the session's class
// is asked for.
func TestBoundResize(t *testing.T) {
	for _, tc := range []struct {
		class         backend.Class
		width, height int
		want          backend.Resize
	}{
		{backend.Terminal, 80, 24, backend.Resize{Width: 80, Height: 24}},
		{backend.Terminal, 65535, 65535, backend.Resize{Width: terminal.MaxCols, Height: terminal.MaxRows}},
		{backend.Graphical, 1920, 1080, backend.Resize{Width: 1920, Height: 1080}},
		{backend.Graphical, 65535, 65535, backend.Resize{Width: backend.MaxResizeSide, Height: backend.MaxResizeSide}},
	} {
		if got := boundResize(tc.class, tc.width, tc.height); got != tc.want {
			t.Errorf("boundResize(%v, %d, %d) = %+v, want %+v", tc.class, tc.width, tc.height, got, tc.want)
		}
	}
}
