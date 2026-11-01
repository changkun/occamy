// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package stream

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/draw"
	"math/rand/v2"
	"testing"
	"testing/synctest"
	"time"

	"changkun.de/x/occamy/internal/backend"
	"changkun.de/x/occamy/internal/backend/backendtest"
	"changkun.de/x/occamy/internal/pacer"
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
		// The window takes two frames; the next three scrolls wait. Each
		// scroll lands at once: one the writer reads half drawn leaves the
		// client a screen that is not the page at any row, as
		// TestScrollTorn shows.
		top := 0
		for range 5 {
			scrollSink(b, screen, 2, top, 7, false)
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

// A remote's scroll is a CopyRect, then a paint of the rows it exposes,
// and the writer may read the screen between the two, or while it
// paints: a frame then shows the scroll half drawn, and the next the
// rest. Each copy is checked against the screen the client has, so the
// client ends with the server's screen, and a copy goes only by the
// scroll's offset.
func TestScrollTorn(t *testing.T) {
	const w, h = 320, 256
	screen := image.Rect(0, 0, w, h)
	for _, tc := range []struct {
		name string
		// scroll moves page 9 up by dy rows from top, pausing where the
		// writer reads.
		scroll func(b *tapped, top, dy int)
	}{
		{"between the copy and the paint", func(b *tapped, top, dy int) {
			b.Sink().CopyRect(image.Rect(0, 0, w, h-dy), image.Pt(0, dy))
			time.Sleep(time.Millisecond)
			pageSink(b, image.Rect(0, h-dy, w, h), 9, top+h)
		}},
		{"while the exposed rows are painted", func(b *tapped, top, dy int) {
			b.Sink().CopyRect(image.Rect(0, 0, w, h-dy), image.Pt(0, dy))
			pageSink(b, image.Rect(w-64, h-dy, w, h), 9, top+h)
			time.Sleep(time.Millisecond)
			pageSink(b, image.Rect(0, h-dy, w-64, h), 9, top+h)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				b := &tapped{Backend: backendtest.NewScreen(w, h, 0)}
				r := newRig(t, b, 50e6, time.Millisecond, visible)
				time.Sleep(100 * time.Millisecond)
				pageSink(b, screen, 9, 0)
				time.Sleep(100 * time.Millisecond)
				n := r.cl.frameCount()
				top := 0
				for _, dy := range []int{7, 7, 16} {
					tc.scroll(b, top, dy)
					top += dy
					time.Sleep(100 * time.Millisecond)
					matches(t, r.s, r.cl, "after a scroll read half drawn")
				}
				r.cl.mu.Lock()
				defer r.cl.mu.Unlock()
				copies, torn := 0, false
				for _, f := range r.cl.frames[n:] {
					// A frame with the copy but not every tile of the
					// exposed rows shows the scroll half drawn.
					torn = torn || len(f.copies) > 0 && f.tiles < w/64
					for _, c := range f.copies {
						copies++
						if dy := int(c.SrcY) - int(c.DstY); c.SrcX != c.DstX || dy != 7 && dy != 16 {
							t.Errorf("a copy by %d rows (%+v)", dy, c)
						}
					}
				}
				if copies == 0 || !torn {
					t.Errorf("%d copies, and no frame showed a scroll half drawn", copies)
				}
			})
		})
	}
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
// once the input reaches it, in one paint: the frame of a scroll that
// the writer reads half drawn carries only part of it, as
// TestScrollTorn shows.
type wheeled struct {
	*tapped
	mu  chan struct{}
	top int
}

func (b *wheeled) Input(ev backend.InputEvent) error {
	if wh, ok := ev.(backend.Wheel); ok {
		go func() {
			b.mu <- struct{}{}
			dy := 16 * wh.DY
			scrollSink(b.tapped, image.Rect(0, 0, 320, 256), 6, b.top, dy, false)
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

// A resize that keeps the number of tiles still leaves the client's
// screen undefined: nothing is copied from it until it is sent again.
func TestScrollResizeSameGrid(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := &tapped{Backend: backendtest.NewScreen(320, 256, 0)}
		r := newRig(t, b, 50e6, time.Millisecond, visible)
		time.Sleep(100 * time.Millisecond)
		pageSink(b, image.Rect(0, 0, 320, 256), 8, 0)
		time.Sleep(100 * time.Millisecond)
		n := r.cl.frameCount()
		// 300×250 is 5×4 tiles of 64 too.
		b.Sink().Resize(300, 250)
		pageSink(b, image.Rect(0, 0, 300, 250), 8, 10)
		time.Sleep(100 * time.Millisecond)
		matches(t, r.s, r.cl, "after the resize")
		if _, copies, _, _, _ := r.cl.since(n); copies != 0 {
			t.Errorf("%d copies from the screen before the resize", copies)
		}
	})
}

// A run of lossy tiles ends before a tile that a copy completed: the
// copy is not sent again inside the run.
func TestScrollLossyRun(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const w, h = 512, 256
		img := photo(t)
		b := &tapped{Backend: backendtest.NewScreen(w, h, 0)}
		hello := protocol.Hello{Visible: true, Capabilities: protocol.Capabilities{Codecs: []protocol.Codec{protocol.CodecWebP, protocol.CodecDeflate}}}
		r := newRig(t, b, 50e6, time.Millisecond, hello)
		time.Sleep(100 * time.Millisecond)
		// The photograph's rows from 0.
		paint(b, image.Rect(0, 0, w, h), img)
		time.Sleep(100 * time.Millisecond)
		n := r.cl.frameCount()
		r.cl.mu.Lock()
		webp := r.cl.codecs[protocol.CodecWebP]
		r.cl.mu.Unlock()
		// In one drawing operation, the top tile of the first column shows
		// other rows and the other columns scroll by 20: the top row's
		// lossy run, which the first column's tile begins, ends where the
		// copy's tiles begin. The first column's run is one search that
		// misses.
		next := image.NewRGBA(image.Rect(0, 0, w, h))
		draw.Draw(next, next.Rect, img, image.Point{}, draw.Src)
		draw.Draw(next, image.Rect(0, 0, 64, 64), img, image.Pt(0, 300), draw.Src)
		draw.Draw(next, image.Rect(64, 0, w, h), img, image.Pt(64, 20), draw.Src)
		paint(b, next.Rect, next)
		time.Sleep(100 * time.Millisecond)
		_, copies, _, _, _ := r.cl.since(n)
		if copies != 1 {
			t.Fatalf("%d copies, want 1", copies)
		}
		r.cl.mu.Lock()
		defer r.cl.mu.Unlock()
		copied := image.Rect(64, 0, w, 192) // the tiles the copy completes
		for _, f := range r.cl.frames[n:] {
			for _, rect := range f.rects {
				if rect.Overlaps(copied) {
					t.Errorf("a tile message at %v covers what the copy completed", rect)
				}
			}
		}
		if r.cl.codecs[protocol.CodecWebP] == webp {
			t.Error("no lossy tile sent: the test does not test a run")
		}
	})
}

// motion repaints r every period in a new colour until ctx is done.
func motion(ctx context.Context, b *tapped, r image.Rectangle, period time.Duration) {
	for i := 0; ; i++ {
		select {
		case <-ctx.Done():
			return
		case <-time.After(period):
			paint(b, r, image.NewUniform(color.RGBA{byte(i), byte(i >> 8), 0x40, 0xff}))
		}
	}
}

// A wheel whose scroll shows while a frame of motion is being sent: the
// frame turns to the input's effect, and the effect is the copy.
func TestScrollWheelMidFrame(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const w, h = 320, 512
		b := &wheeled{tapped: &tapped{Backend: backendtest.NewScreen(w, h, 0)}, mu: make(chan struct{}, 1)}
		// The motion below the page is 160 KiB raw a frame, 80 ms of the
		// link: a wheel finds a frame half sent.
		r := newRig(t, b, 2e6, 5*time.Millisecond, visible)
		time.Sleep(100 * time.Millisecond)
		pageSink(b.tapped, image.Rect(0, 0, w, 256), 6, 0)
		time.Sleep(time.Second)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go motion(ctx, b.tapped, image.Rect(0, 256, w, h), 10*time.Millisecond)
		time.Sleep(500 * time.Millisecond)
		mid := 0
		for seq := uint32(1); seq <= 4; seq++ {
			n := r.cl.frameCount()
			r.cl.send(t, protocol.Wheel{Seq: seq, DY: 1})
			time.Sleep(300 * time.Millisecond)
			r.cl.mu.Lock()
			var carrier *frameRecord
			for i := range r.cl.frames[n:] {
				if f := &r.cl.frames[n+i]; f.lastSeq >= seq {
					carrier = f
					break
				}
			}
			r.cl.mu.Unlock()
			switch {
			case carrier == nil:
				t.Fatalf("no frame reported wheel %d", seq)
			case len(carrier.copies) != 1:
				t.Errorf("wheel %d: its frame carried %d copies and %d tiles", seq, len(carrier.copies), carrier.tiles)
			case carrier.late > 0:
				mid++
			}
		}
		if mid == 0 {
			t.Error("no wheel's scroll showed mid-frame: the test does not test that")
		}
	})
}

// document is a 1024×768 screen of a document scrolled to row top: rows
// of text down to row 600, blank ones below. It has 64 colours, so that
// its tiles go lossless and the client's screen is exact.
func document(top int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, 1024, 768))
	for y := range 768 {
		for x := range 1024 {
			c := color.RGBA{0xfa, 0xfa, 0xf5, 0xff}
			if top+y < 600 {
				c = pagePixel(9, x, top+y)
				c.R, c.G, c.B = c.R&0xc0, c.G&0xc0, c.B&0xc0
			}
			img.SetRGBA(x, y, c)
		}
	}
	return img
}

// The tiles a copy completes are sent as of the pixels it was found
// against: a screen that scrolled again after the frame's tiles were
// collected is sent once, by the copy.
func TestScrollMovedSinceCollected(t *testing.T) {
	d := newDirect(t, true)
	d.show(document(0))
	d.frame()
	d.show(document(16))
	if _, ok := d.w.collect(image.Rectangle{}); !ok {
		t.Fatal("resized")
	}
	d.show(document(32))
	if ok, _ := d.w.p.StartFrame(time.Now()); !ok {
		t.Fatal("the pacer holds the frame")
	}
	d.c.bytes, d.c.tiles, d.c.copies, d.c.rects = 0, 0, 0, nil
	if err := d.w.sendFrame(d.ctx, d.w.order, false); err != nil {
		t.Fatal(err)
	}
	if d.c.copies != 1 || d.c.tiles != 0 {
		t.Fatalf("the scroll went as %d copies and %d tiles, want a copy alone", d.c.copies, d.c.tiles)
	}
	d.exact()
	if _, tiles, copies, _, _ := d.frame(); tiles != 0 || copies != 0 {
		t.Errorf("the next frame sent %d tiles and %d copies, want none", tiles, copies)
	}
}

// An input whose effect is a copy alone is resolved by it: the next
// input is awaited afresh.
func TestScrollCopyAloneResolves(t *testing.T) {
	d := newDirect(t, true)
	// Scrolled by 16 rows, the document exposes blank rows where the
	// client has blank already.
	doc := document
	d.show(doc(0))
	d.frame()
	if err := d.w.v.Input(1, backend.Wheel{DY: 1}); err != nil {
		t.Fatal(err)
	}
	// Painted whole, as a server without CopyRect would.
	paint(d.b, image.Rect(0, 0, 1024, 768), doc(16))
	if err := d.w.small(d.ctx); err != nil {
		t.Fatal(err)
	}
	if d.w.in == nil {
		t.Fatal("the wheel is not pending")
	}
	fresh, ok := d.w.collect(image.Rectangle{})
	if !ok || fresh == 0 {
		t.Fatalf("the wheel's effect is not fresh: %d tiles", fresh)
	}
	d.w.p.Input()
	d.w.in.boosted = true
	if ok, _ := d.w.p.StartFrame(time.Now()); !ok {
		t.Fatal("the pacer holds the input's frame")
	}
	d.c.bytes, d.c.tiles, d.c.copies, d.c.rects = 0, 0, 0, nil
	order := d.w.order[:fresh]
	byDamage(order, d.w.versions)
	if err := d.w.sendFrame(d.ctx, order, true); err != nil {
		t.Fatal(err)
	}
	if d.c.copies != 1 || d.c.tiles != 0 {
		t.Fatalf("the effect went as %d copies and %d tiles of %d fresh, want a copy alone", d.c.copies, d.c.tiles, fresh)
	}
	d.exact()
	if d.w.in != nil {
		t.Error("the wheel is still pending after its effect was sent")
	}
}

// The lossless tile that ends a lossy run is read ahead and sent as the
// next message: the shadow takes its pixels from the buffer they were
// read into, so the shadow is what the client shows (direct.frame checks
// it), and a copy from that tile's rows draws what the client has.
func TestScrollLookahead(t *testing.T) {
	d := newDirect(t, true)
	ph := photo(t)
	screen := func(top int) *image.RGBA {
		img := document(top)
		draw.Draw(img, image.Rect(0, 0, 256, 64), ph, image.Point{}, draw.Src)
		return img
	}
	d.show(screen(0))
	d.frame()
	if len(d.c.rects) < 2 || d.c.rects[0] != image.Rect(0, 0, 256, 64) || d.c.rects[1] != image.Rect(256, 0, 320, 64) {
		t.Fatalf("tile messages begin %v: want the photograph's run, then the tile read ahead after it", d.c.rects[:min(2, len(d.c.rects))])
	}
	// The document scrolls under the photograph, the tile read ahead
	// among the rows copied.
	d.show(screen(16))
	if _, _, copies, _, _ := d.frame(); copies == 0 {
		t.Fatal("no copy")
	}
	want := serverScreen(d.s)
	for _, r := range []image.Rectangle{image.Rect(256, 0, 1024, 768), image.Rect(0, 64, 256, 768)} {
		if !sameRect(d.c.img, want, r) {
			t.Errorf("the client differs from the server in %v", r)
		}
	}
}

// A copy keeps the quality the client has its pixels at. A photograph
// sent at quality 30 and scrolled once the pacer is back at 90 is sent
// again at 90, as it would be without scroll detection, after a copy that
// completes none of its tiles; one sent at 90 and scrolled at 30 is
// copied.
func TestScrollLossyQuality(t *testing.T) {
	ph := corpusFrame(t, "photo")
	shifted := func(dy int) *image.RGBA {
		img := image.NewRGBA(ph.Rect)
		draw.Draw(img, img.Rect, ph, image.Pt(0, dy), draw.Src)
		return img
	}
	at := func(q int) *pacer.Pacer {
		return pacer.New(pacer.Config{Window: 1 << 20, MinQuality: q, MaxQuality: q, InitialCap: 1 << 30})
	}
	type result struct {
		psnr                 float64
		bytes, tiles, copies int
	}
	send := func(scroll bool, q0, q1 int) result {
		d := newDirect(t, scroll)
		d.w.p = at(q0)
		d.show(shifted(0))
		d.frame()
		d.w.p = at(q1)
		d.show(shifted(16))
		b, tiles, copies, _, _ := d.frame()
		return result{psnr(serverScreen(d.s), d.c.img), b, tiles, copies}
	}
	for _, tc := range []struct {
		q0, q1 int
		copied bool
	}{{30, 90, false}, {90, 30, true}} {
		off, on := send(false, tc.q0, tc.q1), send(true, tc.q0, tc.q1)
		t.Logf("quality %d, then scrolled at %d: without detection %.1f dB, %d tiles, %.1f KiB; with it %.1f dB, %d copies, %d tiles, %.1f KiB",
			tc.q0, tc.q1, off.psnr, off.tiles, float64(off.bytes)/1024, on.psnr, on.copies, on.tiles, float64(on.bytes)/1024)
		if on.copies != 1 {
			t.Errorf("quality %d then %d: %d copies, want 1", tc.q0, tc.q1, on.copies)
		}
		if tc.copied {
			if 4*on.tiles > off.tiles || on.psnr < off.psnr {
				t.Errorf("quality %d then %d: %d tiles at %.1f dB, want the copy to keep most of the %d tiles at the higher quality, above %.1f dB",
					tc.q0, tc.q1, on.tiles, on.psnr, off.tiles, off.psnr)
			}
		} else if on.tiles != off.tiles || on.psnr < off.psnr-0.05 {
			t.Errorf("quality %d then %d: %d tiles at %.1f dB, want the %d tiles sent again as without detection, at %.1f dB",
				tc.q0, tc.q1, on.tiles, on.psnr, off.tiles, off.psnr)
		}
	}
}

// A copy the client would not take is neither sent nor applied to the
// shadow, which stays what the client shows, and completes no tile: the
// frame's tiles, which every client takes, draw the scroll.
func TestScrollCopyNotTaken(t *testing.T) {
	d := newDirect(t, true)
	d.show(document(0))
	d.frame()
	// Below a copy-rect's size, which no client may ask for. Tiles of
	// up to 256 colours are not checked against it, being smaller than
	// the least a client may ask for.
	d.w.limits.MaxMessageSize = 16
	d.show(document(16))
	if _, ok := d.w.collect(image.Rectangle{}); !ok {
		t.Fatal("resized")
	}
	d.c.copies = 0
	copies, done, _, err := d.w.scroll(d.ctx, d.w.frame+1, d.w.order, false)
	if err != nil {
		t.Fatal(err)
	}
	if copies != 0 || done != 0 || d.c.copies != 0 {
		t.Errorf("%d copies sent, %d of them taken, completing %d tiles; want none", copies, d.c.copies, done)
	}
	// Before any tile goes, the shadow still shows the client's screen.
	d.shadowed()
	if ok, _ := d.w.p.StartFrame(time.Now()); !ok {
		t.Fatal("the pacer holds the frame")
	}
	if err := d.w.sendFrame(d.ctx, d.w.order, false); err != nil {
		t.Fatal(err)
	}
	if d.c.copies != 0 {
		t.Errorf("%d copies the client would not take", d.c.copies)
	}
	d.shadowed()
	d.exact()
}

// A copy in a frame of no input is motion, not an input's effect: the
// tiles it completes are not marked as one, so that an input that comes
// while a page scrolls on its own does not take the scroll for its
// effect.
func TestScrollNotAnEffect(t *testing.T) {
	d := newDirect(t, true)
	d.show(document(0))
	d.frame()
	d.show(document(16))
	if _, _, copies, _, _ := d.frame(); copies == 0 {
		t.Fatal("no copy")
	}
	for tt, e := range d.w.effects {
		if e != 0 {
			t.Errorf("tile %d marked as an input's effect at version %d", tt, e)
		}
	}
}
