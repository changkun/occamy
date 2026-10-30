// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package stream

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"slices"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"changkun.de/x/occamy/internal/backend"
	"changkun.de/x/occamy/internal/backend/backendtest"
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

func newClient(c Conn) *client { return &client{c: c, start: time.Now(), img: &image.RGBA{}} }

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
			cl.term = terminal.New(cl.size.X, cl.size.Y, terminal.Options{})
			cl.term.Write(m.Data)
			cl.snapshots++
		case *protocol.TermData:
			if cl.term == nil {
				cl.errs = append(cl.errs, errors.New("term-data before term-snapshot"))
				break
			}
			cl.term.Write(m.Data)
		case *protocol.Tile:
			r := image.Rect(int(m.X), int(m.Y), int(m.X)+int(m.W), int(m.Y)+int(m.H))
			if !r.In(cl.img.Rect) {
				cl.outside = true
				break
			}
			src := &image.RGBA{Pix: m.Payload, Stride: 4 * r.Dx(), Rect: image.Rect(0, 0, r.Dx(), r.Dy())}
			draw.Draw(cl.img, r, src, image.Point{}, draw.Src)
			rects = append(rects, r)
			cl.tiles++
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
	ctx, cancel := context.WithCancel(context.Background())
	s, owner, err := session.Start(ctx, session.Config{Backend: b, TileSize: tile})
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{s: s, owner: owner, link: newLink(rate, delay), served: make(chan error, 1)}
	r.cl = newClient(r.link.client())
	go r.cl.run(ctx)
	go func() {
		r.served <- Serve(ctx, r.link.server(), owner, hello, Config{SessionID: "s1"})
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
		img.Pix[0] = 0xff
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

// A client that decodes no codec the server sends is refused.
func TestNoCodec(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRig(t, backendtest.NewScreen(200, 150, 0), 50e6, time.Millisecond, protocol.Hello{Visible: true})
		if err := <-r.served; err != nil {
			t.Errorf("Serve returned %v", err)
		}
		r.served <- nil
		time.Sleep(10 * time.Millisecond)
		r.cl.mu.Lock()
		defer r.cl.mu.Unlock()
		if r.cl.close == nil || r.cl.close.Code != protocol.CloseUnsupported || r.cl.tiles != 0 {
			t.Errorf("close %+v after %d tiles, want unsupported before any", r.cl.close, r.cl.tiles)
		}
	})
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
