// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package stream

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"slices"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"changkun.de/x/occamy/internal/backend/backendtest"
	"changkun.de/x/occamy/internal/pacer"
	"changkun.de/x/occamy/protocol"
)

// countEncoder counts the calls of Lossy and Still on the encoder it
// wraps, sleeps for slow in each call of Tile, Lossy and Still, as a
// writer on a loaded host takes long over its frames, and calls onStill,
// if set, with the count before each Still.
type countEncoder struct {
	Encoder
	mu           sync.Mutex
	lossy, still int
	slow         time.Duration
	onStill      func(n int)
}

// wrap makes the encoder Serve gives a viewer the one e wraps.
func (e *countEncoder) wrap(codecs []protocol.Codec) Encoder {
	e.Encoder = newEncoder(codecs)
	return e
}

func (e *countEncoder) sleep() {
	e.mu.Lock()
	d := e.slow
	e.mu.Unlock()
	if d > 0 {
		time.Sleep(d)
	}
}

func (e *countEncoder) setSlow(d time.Duration) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.slow = d
}

func (e *countEncoder) counts() (lossy, still int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.lossy, e.still
}

func (e *countEncoder) Tile(dst, pix []byte, w, h int, moving bool) ([]byte, protocol.Codec, bool) {
	e.sleep()
	return e.Encoder.Tile(dst, pix, w, h, moving)
}

func (e *countEncoder) Lossy(dst, pix []byte, w, h, quality int) ([]byte, protocol.Codec) {
	e.mu.Lock()
	e.lossy++
	e.mu.Unlock()
	e.sleep()
	return e.Encoder.Lossy(dst, pix, w, h, quality)
}

func (e *countEncoder) Still(dst, pix []byte, w, h, tile, lo, hi int) ([]byte, protocol.Codec, int, bool) {
	e.mu.Lock()
	e.still++
	n, f := e.still, e.onStill
	e.mu.Unlock()
	if f != nil {
		f(n)
	}
	e.sleep()
	return e.Encoder.Still(dst, pix, w, h, tile, lo, hi)
}

// codecCount is the tiles the client decoded in codec.
func (cl *client) codecCount(c protocol.Codec) int {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	return cl.codecs[c]
}

// An input that shows nothing, such as a key-up after motion stopped,
// does not hold refinement back for good: the writer gives it up after
// inputTimeout, and refines the tiles that moved.
func TestRefineAfterSilentInput(t *testing.T) {
	for _, c := range []struct {
		name string
		keys []protocol.Key
	}{
		{"key-up", []protocol.Key{{Seq: 1, Down: false, Keysym: 'a'}}},
		{"key down and up", []protocol.Key{{Seq: 1, Down: true, Keysym: 'a'}, {Seq: 2, Down: false, Keysym: 'a'}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				b := &tapped{Backend: backendtest.NewScreen(512, 256, 0)}
				r := newRigConfig(t, b, 64, 50e6, time.Millisecond, webpDeflate, Config{})
				time.Sleep(500 * time.Millisecond)
				synctest.Wait()
				animate(b, image.Rect(64, 64, 320, 192), time.Second, func(k int) image.Image { return gradient(k) })
				synctest.Wait()
				if r.cl.codecCount(protocol.CodecWebP) == 0 {
					t.Fatal("the motion went exact")
				}
				for _, k := range c.keys {
					r.cl.send(t, k)
					time.Sleep(80 * time.Millisecond)
					synctest.Wait()
				}
				time.Sleep(inputTimeout + 2*time.Second)
				synctest.Wait()
				if !bytes.Equal(r.cl.screen().Pix, serverScreen(r.s).Pix) {
					t.Errorf("the client still shows the motion lossy %v after the input", inputTimeout+2*time.Second)
				}
			})
		})
	}
}

// Content that changes in every frame is in motion however long the
// writer takes over its frames: the time it spends sending one does not
// cool the heat, since it takes no snapshot meanwhile.
func TestMotionSlowWriter(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := &tapped{Backend: backendtest.NewScreen(512, 256, 0)}
		enc := &countEncoder{}
		newRigConfig(t, b, 64, 50e6, time.Millisecond, webpDeflate, Config{NewEncoder: enc.wrap})
		time.Sleep(500 * time.Millisecond)
		synctest.Wait()
		// Eight tiles at 150 ms each: over a second a frame.
		enc.setSlow(150 * time.Millisecond)
		start := time.Now()
		animate(b, image.Rect(64, 64, 320, 192), 8*time.Second, func(k int) image.Image { return gradient(k) })
		synctest.Wait()
		enc.setSlow(0)
		lossy, _ := enc.counts()
		t.Logf("%d runs sent in motion over %v", lossy, time.Since(start))
		if lossy < 3 {
			t.Errorf("%d runs sent in motion while the content changed in every frame of a second, want 3 or more", lossy)
		}
	})
}

// A tile that changes less often than the refine delay is not in
// motion: it goes at its final quality when it changes, not lossy first
// and then again refined, also while something else on the screen
// animates and the writer sends frames for it between the snapshots.
// One that changes more often is in motion.
func TestBlink(t *testing.T) {
	for _, c := range []struct {
		period time.Duration
		moving bool
		busy   time.Duration // how often another region changes, 0 for never
		delay  time.Duration // the refine delay, the default if 0
	}{
		{100 * time.Millisecond, true, 0, 0},
		{530 * time.Millisecond, false, 0, 0}, // a Windows caret
		{600 * time.Millisecond, false, 0, 0}, // a GTK caret
		{530 * time.Millisecond, false, time.Second / 30, 0},
		{530 * time.Millisecond, false, time.Second / 60, 0},
		{time.Second, false, time.Second / 30, 0}, // a clock
		// Below minHalfLife, the half-life is minHalfLife.
		{time.Second / 30, true, 0, 50 * time.Millisecond},
	} {
		name := c.period.String()
		if c.busy > 0 {
			name += "/beside " + c.busy.String()
		}
		if c.delay > 0 {
			name += "/refined after " + c.delay.String()
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				b := &tapped{Backend: backendtest.NewScreen(512, 256, 0)}
				r := newRigConfig(t, b, 64, 50e6, time.Millisecond, webpDeflate, Config{RefineDelay: c.delay})
				time.Sleep(500 * time.Millisecond)
				synctest.Wait()
				stop, done := make(chan struct{}), make(chan struct{})
				go func() {
					// A region of one colour, which goes exact moving or
					// not, changing every c.busy.
					defer close(done)
					for k := 0; c.busy > 0; k++ {
						select {
						case <-stop:
							return
						default:
						}
						paint(b, image.Rect(384, 192, 448, 256), image.NewUniform(color.RGBA{uint8(k), 0, 0, 255})) // #nosec G115 -- wraps
						time.Sleep(c.busy)
					}
				}()
				webp0, deflate0 := r.cl.codecCount(protocol.CodecWebP), r.cl.codecCount(protocol.CodecDeflate)
				blinks := 0
				for d := time.Duration(0); d < 8*time.Second; d += c.period {
					paint(b, image.Rect(128, 128, 192, 192), gradient(blinks%2))
					blinks++
					time.Sleep(c.period)
					synctest.Wait()
				}
				close(stop)
				<-done
				webp := r.cl.codecCount(protocol.CodecWebP) - webp0
				deflate := r.cl.codecCount(protocol.CodecDeflate) - deflate0
				t.Logf("%d changes: %d tiles WebP, %d deflate", blinks, webp, deflate)
				switch {
				case c.moving && webp == 0:
					t.Errorf("changes %v apart went exact, want them in motion", c.period)
				case !c.moving && webp > 0:
					t.Errorf("%d changes %v apart went as %d WebP tiles, want none", blinks, c.period, webp)
				case !c.moving && c.busy == 0 && deflate > blinks:
					t.Errorf("%d changes %v apart went as %d deflate tiles, want one each", blinks, c.period, deflate)
				}
			})
		})
	}
}

// An input's photographic effect goes at the pacer's quality without the
// search; refinement then finds the quality the score needs, which the
// client has already, and sends nothing, once for each tile.
func TestInputEffectNotResent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := &tapped{Backend: backendtest.NewScreen(512, 256, 0)}
		enc := &countEncoder{}
		r := newRigConfig(t, b, 64, 50e6, time.Millisecond, webpDeflate, Config{NewEncoder: enc.wrap})
		time.Sleep(500 * time.Millisecond)
		synctest.Wait()
		area := image.Rect(0, 64, 512, 256)
		r.cl.send(t, protocol.Key{Seq: 1, Down: false, Keysym: 'a'})
		time.Sleep(5 * time.Millisecond)
		synctest.Wait()
		paint(b, area, photo(t))
		time.Sleep(100 * time.Millisecond)
		synctest.Wait()
		webp := r.cl.codecCount(protocol.CodecWebP)
		_, still0 := enc.counts()
		before := tileScores(r.cl.screen(), serverScreen(r.s), area)
		time.Sleep(3 * time.Second)
		synctest.Wait()
		if again := r.cl.codecCount(protocol.CodecWebP) - webp; again > 0 {
			t.Errorf("refinement sent the effect's tiles again: %d WebP tiles", again)
		}
		if _, still := enc.counts(); still-still0 > len(before) {
			t.Errorf("refinement searched %d times for the effect's %d tiles", still-still0, len(before))
		}
		after := tileScores(r.cl.screen(), serverScreen(r.s), area)
		for i := range before {
			if after[i] < before[i] {
				t.Errorf("tile %d: SSIM %.4f after refinement, %.4f before", i, after[i], before[i])
			}
		}
	})
}

// shiftedPhoto is the corpus photo with its rows from dy on at the top.
func shiftedPhoto(t *testing.T, dy int) *image.RGBA {
	ph := corpusFrame(t, "photo")
	img := image.NewRGBA(ph.Rect)
	draw.Draw(img, img.Rect, ph, image.Pt(0, dy), draw.Src)
	return img
}

// fixedQuality is a pacer at quality q.
func fixedQuality(q int) *pacer.Pacer {
	return pacer.New(pacer.Config{Window: 1 << 20, MinQuality: q, MaxQuality: q, InitialCap: 1 << 30})
}

// refinable returns the tiles the writer would refine now, were they
// still long enough.
func (d *direct) refinable() []int {
	w := d.w
	w.refineDelay = 0
	defer func() { w.refineDelay = DefaultRefineDelay }()
	w.versions = w.fb.Versions(w.versions[:0])
	w.candidates(image.Rectangle{}, time.Now())
	return slices.Clone(w.refine)
}

// A copy carries the goal of the pixels it moves, as it carries their
// quality: a copy of final pixels is final, and not searched again; and
// one of pixels whose goal is unknown has none, and is refined, even
// where the tile's own goal was beyond the pacer's reach.
func TestCopyCarriesGoal(t *testing.T) {
	t.Run("final", func(t *testing.T) {
		d := newRefineDirect(t, false)
		d.w.p = fixedQuality(90)
		d.show(shiftedPhoto(t, 0))
		d.frame()
		d.refineAll()
		if c := d.refinable(); len(c) > 0 {
			t.Fatalf("%d tiles to refine before the scroll", len(c))
		}
		d.show(shiftedPhoto(t, 32))
		if _, _, copies, _, _ := d.frame(); copies == 0 {
			t.Fatal("the scroll went without a copy")
		}
		if c := d.refinable(); len(c) > 0 {
			t.Errorf("%d tiles to refine after a copy of pixels at their goal: %v", len(c), c)
		}
	})
	t.Run("unknown", func(t *testing.T) {
		d := newRefineDirect(t, false)
		w := d.w
		w.p = fixedQuality(90)
		d.show(shiftedPhoto(t, 0))
		d.frame()
		d.refineAll()
		// In column 3, the tile of row 2 went in motion at 30, its goal
		// unknown, and the one above is at 90, its goal 91 beyond the
		// pacer's reach. A scroll by half a tile at 30, on a congested
		// link, makes the tile of row 1 half of each.
		below, tile := 2*w.g.cols+3, w.g.cols+3
		w.sc.qual[below], w.goal[below] = 30, 0
		w.sc.qual[tile], w.goal[tile] = 90, 91
		w.p = fixedQuality(30)
		d.show(shiftedPhoto(t, 32))
		if _, _, copies, _, _ := d.frame(); copies == 0 {
			t.Fatal("the scroll went without a copy")
		}
		if w.sent[tile] != w.versions[tile] || w.quality(tile) != 30 {
			t.Fatalf("the copy did not complete the tile at 30: quality %d", w.quality(tile))
		}
		w.p = fixedQuality(90)
		if c := d.refinable(); !slices.Contains(c, tile) {
			t.Errorf("a tile completed at 30 by a copy of pixels whose goal is unknown is not refined: goal %d", w.goal[tile])
		}
	})
}

// A frame of refinement ends once the screen changes, after the tile it
// tried, even when no tile it tried went: the change goes next.
func TestRefineYieldsWithoutSending(t *testing.T) {
	d := newRefineDirect(t, false)
	enc := &countEncoder{Encoder: d.w.enc}
	d.w.enc = enc
	d.show(corpusFrame(t, "photo"))
	d.frame()
	// With every goal unknown, refinement searches each lossy tile and
	// finds the client has it as the score needs: none goes.
	for t := range d.w.goal {
		d.w.goal[t] = 0
	}
	tiles := d.refinable()
	if len(tiles) < 2 {
		t.Fatalf("%d tiles to refine", len(tiles))
	}
	_, still0 := enc.counts()
	enc.onStill = func(n int) {
		if n == still0+1 {
			r := image.Rect(0, 0, 64, 64)
			paint(d.b, r, image.NewUniform(color.RGBA{1, 2, 3, 0xff}))
		}
	}
	w := d.w
	w.refineDelay = 0
	w.candidates(image.Rectangle{}, time.Now())
	w.refineDelay = DefaultRefineDelay
	if ok, _ := w.p.StartFrame(time.Now()); !ok {
		t.Fatal("the pacer holds the frame")
	}
	sent := d.c.tiles
	if err := w.refineFrame(d.ctx); err != nil {
		t.Fatal(err)
	}
	if _, still := enc.counts(); still-still0 != 1 || d.c.tiles != sent {
		t.Errorf("searched %d tiles and sent %d after the screen changed in the first search, want 1 and none", still-still0, d.c.tiles-sent)
	}
}

// The refinement that the pacer turns away for the frame's budget goes
// in the next frame of refinement as it was encoded, not searched again.
func TestRefineKeepsTurnedAway(t *testing.T) {
	d := newRefineDirect(t, false)
	enc := &countEncoder{Encoder: d.w.enc}
	d.w.enc = enc
	d.w.p = fixedQuality(30)
	d.show(corpusFrame(t, "photo"))
	d.frame()
	w := d.w
	_, still0 := enc.counts()
	frames, sent, turned := 0, 0, 0
	for ; frames < 200; frames++ {
		// A pacer of its own for each frame, with no frame in flight and
		// no rate: refinement's budget is a few tiles.
		w.p = fixedQuality(90)
		tiles := d.refinable()
		if len(tiles) == 0 {
			break
		}
		w.refine = tiles
		if ok, _ := w.p.StartFrame(time.Now()); !ok {
			t.Fatal("the pacer holds the frame")
		}
		before := d.c.tiles
		if err := w.refineFrame(d.ctx); err != nil {
			t.Fatal(err)
		}
		sent += d.c.tiles - before
		if w.held.ok {
			turned++
		}
	}
	_, still := enc.counts()
	t.Logf("%d frames of refinement sent %d tiles, %d turned away, with %d searches", frames, sent, turned, still-still0)
	if turned == 0 {
		t.Fatal("the pacer turned no tile away")
	}
	if still-still0 != sent {
		t.Errorf("%d searches for %d tiles sent", still-still0, sent)
	}
}

// The time between frames cools the heat, whatever the frames are: the
// writer counts the time before each frame, of tiles or of refinement,
// toward the cooling at the next snapshot, and not the frame's own time.
func TestHeatCountsIdle(t *testing.T) {
	d := newRefineDirect(t, false)
	w := d.w
	d.show(corpusFrame(t, "photo"))
	d.frame()
	d.age()
	d.show(corpusFrame(t, "desktop"))
	d.frame()
	if w.heatAt.IsZero() {
		t.Fatal("no heat yet")
	}

	w.refine = w.refine[:0]
	w.heatAt, w.heatIdle = time.Now().Add(-time.Second), 0
	if err := w.refineFrame(d.ctx); err != nil {
		t.Fatal(err)
	}
	if w.heatIdle < time.Second || time.Since(w.heatAt) > time.Second/2 {
		t.Errorf("a frame of refinement a second after the last frame: %v idle, the last frame ended %v ago", w.heatIdle, time.Since(w.heatAt))
	}

	d.show(corpusFrame(t, "browser"))
	if _, ok := w.collect(image.Rectangle{}); !ok {
		t.Fatal("resized")
	}
	w.heatAt, w.heatIdle = time.Now().Add(-time.Second), 0
	if ok, _ := w.p.StartFrame(time.Now()); !ok {
		t.Fatal("the pacer holds the frame")
	}
	if err := w.sendFrame(d.ctx, w.order, false); err != nil {
		t.Fatal(err)
	}
	if w.heatIdle < time.Second || time.Since(w.heatAt) > time.Second/2 {
		t.Errorf("a frame a second after the last frame: %v idle, the last frame ended %v ago", w.heatIdle, time.Since(w.heatAt))
	}
}
