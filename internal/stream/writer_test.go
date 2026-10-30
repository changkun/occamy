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
	// newGrid reports a tile outside the screen the client was told of.
	outside bool
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
		if err != nil {
			panic(err)
		}
		cl.mu.Lock()
		cl.types = append(cl.types, m.Type())
		var ackID uint32
		switch m := m.(type) {
		case *protocol.Welcome:
			cl.welcome = m
		case *protocol.Resize:
			cl.img = image.NewRGBA(image.Rect(0, 0, int(m.Width), int(m.Height)))
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
		case *protocol.Title:
			cl.titles = append(cl.titles, timedTitle{m.Title, time.Since(cl.start), cl.tiles})
		case *protocol.Close:
			c := *m
			cl.close = &c
		}
		cl.mu.Unlock()
		if ackID != 0 {
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
	})
	return r
}

var visible = protocol.Hello{Visible: true}

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
		r := newRig(t, backendtest.NewScreen(200, 150, 10*time.Millisecond), 50e6, 5*time.Millisecond, protocol.Hello{})
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
		r := newRig(t, backendtest.NewScreen(256, 256, 0), 50e6, time.Millisecond, protocol.Hello{})
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
