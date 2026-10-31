// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package stream

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"math/rand/v2"
	"testing"
	"testing/synctest"
	"time"

	"changkun.de/x/occamy/internal/backend"
	"changkun.de/x/occamy/internal/backend/backendtest"
	"changkun.de/x/occamy/internal/session"
	"changkun.de/x/occamy/protocol"
)

// pageSink paints r of the session's screen with rows of page seed, the
// page's row top at r's top.
func pageSink(b *tapped, r image.Rectangle, seed, top int) {
	img := b.Sink().Lock(r)
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			img.SetRGBA(x, y, pagePixel(seed, x, top+y-r.Min.Y))
		}
	}
	b.Sink().Unlock(r)
}

// scrollSink moves page seed in r from row top to row top+dy, as a
// remote does: with copy, by a CopyRect of the rows that stay and a
// paint of those exposed, as VNC's CopyRect arrives; without, by a paint
// of the whole of r, as a server that sends the damage does.
func scrollSink(b *tapped, r image.Rectangle, seed, top, dy int, copy bool) {
	if !copy || abs(dy) >= r.Dy() {
		pageSink(b, r, seed, top+dy)
		return
	}
	if dy > 0 {
		b.Sink().CopyRect(image.Rect(r.Min.X, r.Min.Y, r.Max.X, r.Max.Y-dy), image.Pt(r.Min.X, r.Min.Y+dy))
		pageSink(b, image.Rect(r.Min.X, r.Max.Y-dy, r.Max.X, r.Max.Y), seed, top+dy+r.Dy()-dy)
		return
	}
	b.Sink().CopyRect(image.Rect(r.Min.X, r.Min.Y-dy, r.Max.X, r.Max.Y), r.Min)
	pageSink(b, image.Rect(r.Min.X, r.Min.Y, r.Max.X, r.Min.Y-dy), seed, top+dy)
}

// since sums the copies, tiles and bytes of the frames from the n-th on.
func (cl *client) since(n int) (frames, copies, tiles, size int, last []protocol.CopyRect) {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	for _, f := range cl.frames[n:] {
		frames++
		copies += len(f.copies)
		tiles += f.tiles
		size += f.bytes
		if len(f.copies) > 0 {
			last = f.copies
		}
	}
	return frames, copies, tiles, size, last
}

func (cl *client) frameCount() int {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	return len(cl.frames)
}

// matches fails the test unless the client shows the server's screen.
func matches(t *testing.T, s *session.Session, cl *client, when string) {
	t.Helper()
	want, got := serverScreen(s), cl.screen()
	if got.Rect != want.Rect || !bytes.Equal(got.Pix, want.Pix) {
		t.Fatalf("%s: the client's screen differs from the server's", when)
	}
}

// Scrolls go out as a copy-rect and the exposed rows' tiles, whether the
// remote copies the rows itself or paints them again.
func TestScrollFrames(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const w, h = 320, 256
		b := &tapped{Backend: backendtest.NewScreen(w, h, 0)}
		r := newRig(t, b, 50e6, time.Millisecond, visible)
		time.Sleep(100 * time.Millisecond)
		screen := image.Rect(0, 0, w, h)
		pageSink(b, screen, 1, 0)
		time.Sleep(100 * time.Millisecond)
		matches(t, r.s, r.cl, "the first page")
		top := 0
		for k, dy := range []int{10, 10, -25, 40, 64, -3} {
			n := r.cl.frameCount()
			scrollSink(b, screen, 1, top, dy, k%2 == 0)
			top += dy
			time.Sleep(100 * time.Millisecond)
			matches(t, r.s, r.cl, "after a scroll")
			_, copies, tiles, _, last := r.cl.since(n)
			// The exposed rows touch one row of tiles, or two.
			want := (abs(dy) + 63) / 64 * w / 64
			if copies != 1 || last[0].SrcY-last[0].DstY != uint16(dy) && int(last[0].DstY)-int(last[0].SrcY) != -dy || tiles > want {
				t.Errorf("a scroll by %d: %d copies (%+v) and %d tiles, want 1 copy and %d tiles", dy, copies, last, tiles, want)
			}
		}
	})
}

// A client that stops acknowledging frames gets the scrolls that came
// meanwhile as one copy, from the screen it has.
func TestScrollSkippedFrames(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const w, h = 320, 256
		b := &tapped{Backend: backendtest.NewScreen(w, h, 0)}
		r := newRig(t, b, 50e6, time.Millisecond, visible)
		time.Sleep(100 * time.Millisecond)
		screen := image.Rect(0, 0, w, h)
		pageSink(b, screen, 2, 0)
		time.Sleep(100 * time.Millisecond)
		r.cl.mu.Lock()
		r.cl.noAcks = true
		r.cl.mu.Unlock()
		// The window takes two frames; the next three scrolls wait.
		top := 0
		for range 5 {
			scrollSink(b, screen, 2, top, 7, true)
			top += 7
			time.Sleep(50 * time.Millisecond)
		}
		n := r.cl.frameCount()
		r.cl.mu.Lock()
		r.cl.noAcks = false
		last := r.cl.frames[len(r.cl.frames)-1].id
		r.cl.mu.Unlock()
		r.cl.send(t, protocol.FrameAck{FrameID: last})
		time.Sleep(100 * time.Millisecond)
		matches(t, r.s, r.cl, "after the client caught up")
		frames, copies, tiles, _, copied := r.cl.since(n)
		if frames != 1 || copies != 1 || int(copied[0].SrcY)-int(copied[0].DstY) != 21 || tiles != w/64 {
			t.Errorf("caught up in %d frames, %d copies (%+v), %d tiles; want one frame, one copy by 21 rows, %d tiles", frames, copies, copied, tiles, w/64)
		}
	})
}

// A client that was hidden while the page scrolled gets the scrolls as
// one copy once visible again.
func TestScrollHidden(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const w, h = 320, 256
		b := &tapped{Backend: backendtest.NewScreen(w, h, 0)}
		r := newRig(t, b, 50e6, time.Millisecond, visible)
		time.Sleep(100 * time.Millisecond)
		screen := image.Rect(0, 0, w, h)
		pageSink(b, screen, 3, 0)
		time.Sleep(100 * time.Millisecond)
		r.cl.send(t, protocol.View{})
		time.Sleep(50 * time.Millisecond)
		top := 0
		for range 4 {
			scrollSink(b, screen, 3, top, -9, false)
			top -= 9
			time.Sleep(50 * time.Millisecond)
		}
		n := r.cl.frameCount()
		r.cl.send(t, protocol.View{Visible: true})
		time.Sleep(100 * time.Millisecond)
		matches(t, r.s, r.cl, "once visible")
		frames, copies, tiles, _, copied := r.cl.since(n)
		if frames != 1 || copies != 1 || int(copied[0].DstY)-int(copied[0].SrcY) != 36 || tiles != w/64 {
			t.Errorf("shown in %d frames, %d copies (%+v), %d tiles; want one frame, one copy by -36 rows, %d tiles", frames, copies, copied, tiles, w/64)
		}
	})
}

// Rows outside a client's viewport are as old on the client as when it
// last saw them: a copy from them takes only the rows that are right.
func TestScrollViewport(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const w, h = 320, 256
		b := &tapped{Backend: backendtest.NewScreen(w, h, 0)}
		r := newRig(t, b, 50e6, time.Millisecond, visible)
		time.Sleep(100 * time.Millisecond)
		screen := image.Rect(0, 0, w, h)
		pageSink(b, screen, 4, 0)
		time.Sleep(100 * time.Millisecond)
		half := image.Rect(0, 0, w, h/2)
		r.cl.send(t, protocol.View{Visible: true, W: w, H: h / 2})
		top, copies := 0, 0
		for range 3 {
			n := r.cl.frameCount()
			scrollSink(b, screen, 4, top, 20, true)
			top += 20
			time.Sleep(100 * time.Millisecond)
			want, got := serverScreen(r.s), r.cl.screen()
			if !sameRect(want, got, half) {
				t.Fatal("the client's viewport differs from the server's")
			}
			_, c, _, _, _ := r.cl.since(n)
			copies += c
		}
		if copies == 0 {
			t.Error("no copy in the viewport")
		}
		r.cl.send(t, protocol.View{Visible: true})
		time.Sleep(100 * time.Millisecond)
		matches(t, r.s, r.cl, "once the viewport is the screen")
	})
}

// join adds a viewer to r's session, served to a client of its own over
// a link of the given rate.
func (r *rig) join(t *testing.T, rate float64, delay time.Duration, hello protocol.Hello) *client {
	t.Helper()
	v, err := r.s.Join(session.RoleViewer)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	l := newLink(rate, delay)
	cl := newClient(l.client())
	go cl.run(ctx)
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, l.server(), v, hello, Config{SessionID: "s1"}) }()
	t.Cleanup(func() {
		cancel()
		l.close()
		<-done
	})
	return cl
}

// Two viewers, one on a link a tenth as fast, are each sent copies from
// the screen they have, and each ends up with the server's.
func TestScrollTwoViewers(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const w, h = 320, 256
		b := &tapped{Backend: backendtest.NewScreen(w, h, 0)}
		r := newRig(t, b, 20e6, time.Millisecond, visible)
		slow := r.join(t, 2e6, 20*time.Millisecond, visible)
		time.Sleep(200 * time.Millisecond)
		screen := image.Rect(0, 0, w, h)
		pageSink(b, screen, 5, 0)
		time.Sleep(500 * time.Millisecond)
		top := 0
		for range 20 {
			scrollSink(b, screen, 5, top, 5, true)
			top += 5
			time.Sleep(8 * time.Millisecond)
		}
		time.Sleep(time.Second)
		matches(t, r.s, r.cl, "the fast viewer")
		matches(t, r.s, slow, "the slow viewer")
		_, fastCopies, _, _, _ := r.cl.since(0)
		slowFrames, slowCopies, _, _, _ := slow.since(0)
		if fastCopies == 0 || slowCopies == 0 {
			t.Errorf("%d copies to the fast viewer, %d to the slow one", fastCopies, slowCopies)
		}
		t.Logf("fast viewer: %d copies; slow viewer: %d copies in %d frames", fastCopies, slowCopies, slowFrames)
	})
}

// wheeled scrolls its page by 16 rows a wheel notch, as a remote does
// once the input reaches it.
type wheeled struct {
	*tapped
	mu  chan struct{}
	top int
}

func (b *wheeled) Input(ev backend.InputEvent) error {
	if wh, ok := ev.(backend.Wheel); ok {
		go func() {
			b.mu <- struct{}{}
			dy := 16 * int(wh.DY)
			r := b.Sink()
			_ = r
			scrollSink(b.tapped, image.Rect(0, 0, 320, 256), 6, b.top, dy, true)
			b.top += dy
			<-b.mu
		}()
	}
	return b.tapped.Input(ev)
}

// A wheel's scroll is its input's effect: the frame that carries it
// holds the copy and the exposed rows' tiles, and reports the input.
func TestScrollWheel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const w, h = 320, 256
		b := &wheeled{tapped: &tapped{Backend: backendtest.NewScreen(w, h, 0)}, mu: make(chan struct{}, 1)}
		r := newRig(t, b, 5e6, 10*time.Millisecond, visible)
		time.Sleep(100 * time.Millisecond)
		pageSink(b.tapped, image.Rect(0, 0, w, h), 6, 0)
		time.Sleep(500 * time.Millisecond)
		n := r.cl.frameCount()
		r.cl.send(t, protocol.Wheel{Seq: 1, DY: 1})
		time.Sleep(200 * time.Millisecond)
		matches(t, r.s, r.cl, "after the wheel")
		r.cl.mu.Lock()
		defer r.cl.mu.Unlock()
		var carrier *frameRecord
		for i := range r.cl.frames[n:] {
			if f := &r.cl.frames[n+i]; f.lastSeq >= 1 && len(f.copies) > 0 {
				carrier = f
				break
			}
		}
		if carrier == nil {
			t.Fatal("no frame carried the wheel's copy")
		}
		// Raw tiles of 64 pixels: the exposed row of tiles is 80 KiB, the
		// whole screen 320.
		if len(carrier.copies) != 1 || carrier.tiles != w/64 || carrier.late != 0 {
			t.Errorf("the wheel's frame carried %d copies and %d tiles, %d copies after a tile", len(carrier.copies), carrier.tiles, carrier.late)
		}
	})
}

// Random scrolls of panes, with and without a CopyRect from the remote,
// paints, resizes, hidden spells, viewports and stalls: whenever the
// client sees the whole screen and is caught up, it shows the server's.
func TestScrollStreamRandom(t *testing.T) {
	for seed := range uint64(8) {
		synctest.Test(t, func(t *testing.T) {
			rng := rand.New(rand.NewPCG(seed, 2))
			w, h := 128+rng.IntN(256), 128+rng.IntN(256)
			b := &tapped{Backend: backendtest.NewScreen(w, h, 0)}
			tile := []int{32, 64}[rng.IntN(2)]
			r := newRigTiles(t, b, tile, 20e6, 2*time.Millisecond, visible)
			time.Sleep(100 * time.Millisecond)
			side, sideTop, paneTop := 1+rng.IntN(w/2), 0, 0
			show := func() {
				pageSink(b, image.Rect(0, 0, side, h), 7, sideTop)
				pageSink(b, image.Rect(side, 0, w, h), 7, paneTop)
			}
			show()
			hidden, stalled, partial := false, false, false
			copies := 0
			for range 40 {
				dy := rng.IntN(h) - h/2
				switch rng.IntN(10) {
				case 0:
					x, y := rng.IntN(w), rng.IntN(h)
					rect := image.Rect(x, y, x+1+rng.IntN(w), y+1+rng.IntN(h)).Intersect(image.Rect(0, 0, w, h))
					paint(b, rect, image.NewUniform(color.RGBA{byte(rng.IntN(256)), 1, 2, 0xff}))
				case 1:
					w, h = 128+rng.IntN(256), 128+rng.IntN(256)
					side = 1 + rng.IntN(w/2)
					b.Sink().Resize(w, h)
					show()
				case 2:
					hidden = !hidden
					r.cl.send(t, protocol.View{Visible: !hidden})
					partial = false
				case 3:
					partial = !partial
					v := protocol.View{Visible: !hidden}
					if partial {
						v.X, v.Y, v.W, v.H = uint16(rng.IntN(w/2)), uint16(rng.IntN(h/2)), uint16(1+rng.IntN(w/2)), uint16(1+rng.IntN(h/2))
					}
					r.cl.send(t, v)
				case 4:
					r.cl.mu.Lock()
					stalled = !stalled
					r.cl.noAcks = stalled
					var last uint32
					if n := len(r.cl.frames); n > 0 {
						last = r.cl.frames[n-1].id
					}
					r.cl.mu.Unlock()
					if !stalled && last != 0 {
						r.cl.send(t, protocol.FrameAck{FrameID: last})
					}
				case 5, 6: // the whole screen scrolls, as one page
					if sideTop == paneTop {
						scrollSink(b, image.Rect(0, 0, w, h), 7, paneTop, dy, rng.IntN(2) == 0)
					} else {
						pageSink(b, image.Rect(0, 0, w, h), 7, paneTop+dy)
					}
					sideTop, paneTop = paneTop+dy, paneTop+dy
				default:
					scrollSink(b, image.Rect(side, 0, w, h), 7, paneTop, dy, rng.IntN(2) == 0)
					paneTop += dy
				}
				n := r.cl.frameCount()
				time.Sleep(50 * time.Millisecond)
				_, c, _, _, _ := r.cl.since(n)
				copies += c
				if !hidden && !stalled && !partial {
					matches(t, r.s, r.cl, "a step")
				}
			}
			r.cl.mu.Lock()
			r.cl.noAcks = false
			var last uint32
			if n := len(r.cl.frames); n > 0 {
				last = r.cl.frames[n-1].id
			}
			r.cl.mu.Unlock()
			if last != 0 {
				r.cl.send(t, protocol.FrameAck{FrameID: last})
			}
			r.cl.send(t, protocol.View{Visible: true})
			time.Sleep(200 * time.Millisecond)
			matches(t, r.s, r.cl, "the end")
			if copies == 0 {
				t.Errorf("seed %d: no copy", seed)
			}
		})
	}
}
