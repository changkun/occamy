// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package stream

import (
	"bytes"
	"image"
	"image/color"
	"log/slog"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"changkun.de/x/occamy/internal/backend/backendtest"
	"changkun.de/x/occamy/internal/encoder"
	"changkun.de/x/occamy/internal/pacer"
	"changkun.de/x/occamy/protocol"
)

// A tile warms by one for each snapshot it changed in and cools by half
// a second; it moves from its third snapshot of changes in a row, and a
// change that went out as an input's effect is not motion.
func TestHeat(t *testing.T) {
	w := &writer{heat: make([]float32, 3), effects: make([]uint64, 3)}
	now := time.Unix(0, 0)
	w.versions = []uint64{1, 1, 1}
	var moved []int
	for k := uint64(2); k <= 6; k++ {
		now = now.Add(activeWindow)
		w.snap = slices.Clone(w.versions)
		// Tile 0 changes in every snapshot; tile 1 in the first only;
		// tile 2 in every one, each change an input's effect.
		w.versions = []uint64{k, w.versions[1], k}
		if k == 2 {
			w.versions[1] = k
		}
		w.effects[2] = k
		w.warm(now)
		if w.moving(0, false) {
			moved = append(moved, int(k))
		}
		if w.moving(0, true) {
			t.Errorf("snapshot %d: an input's effect counts as moving", k)
		}
	}
	// Snapshots 2 and 3 are its first two changes; 4 is the third.
	if !slices.Equal(moved, []int{4, 5, 6}) {
		t.Errorf("tile 0 moving at snapshots %v, want 4 to 6", moved)
	}
	if h := w.heat[1]; h >= 1 || h < 0.6 {
		t.Errorf("tile 1 changed once, 400 ms ago: heat %v", h)
	}
	if w.heat[2] != 0 || w.moving(2, false) {
		t.Errorf("tile 2 changed only as inputs' effects: heat %v", w.heat[2])
	}
	// A second without changes halves the heat.
	h := w.heat[0]
	w.snap = slices.Clone(w.versions)
	w.warm(now.Add(time.Second))
	if got := w.heat[0]; got < h/2-1e-6 || got > h/2+1e-6 {
		t.Errorf("heat %v a second after %v, want half", got, h)
	}
	// Versions of another screen size leave the heat alone.
	w.versions = []uint64{9}
	w.warm(now.Add(2 * time.Second))
	if w.heat[0] == 0 {
		t.Error("a resized snapshot reset the heat")
	}
}

// Candidates are the tiles the client has, unchanged since, still for the
// delay, in the viewport and below their goal at the pacer's quality.
func TestCandidates(t *testing.T) {
	g := grid{bounds: image.Rect(0, 0, 640, 64), tile: 64, cols: 10}
	start := time.Unix(0, 0)
	now := start.Add(time.Second)
	w := &writer{
		g:           g,
		start:       start,
		refineDelay: 300 * time.Millisecond,
		p:           pacer.New(pacer.Config{MinQuality: 30, MaxQuality: 60}),
		versions:    []uint64{5, 5, 5, 5, 5, 5, 5, 5, 5, 5},
		sent:        []uint64{5, 5, 5, 5, 5, 5, 4, 0, 5, 5},
		qual:        []uint8{30, 30, 30, 61, 30, lossless, 30, 30, 30, 30},
		goal:        []uint8{0, lossless, 61, 62, 50, lossless, 0, 0, 0, 30},
		changed:     make([]time.Duration, 10),
	}
	// Tile 0: goal unknown. 1: below lossless. 2: its goal is above the
	// pacer's 60. 3: has more than its goal. 4: goal 50, within 60. 5:
	// lossless already. 6: dirty. 7: never drawn. 8: still for 250 ms
	// only. 9: at its goal.
	w.changed[8] = 750 * time.Millisecond
	wake := w.candidates(image.Rectangle{}, now)
	if want := []int{0, 1, 4}; !slices.Equal(w.refine, want) {
		t.Errorf("candidates %v, want %v", w.refine, want)
	}
	if wake != 50*time.Millisecond {
		t.Errorf("wake in %v, want 50ms", wake)
	}
	w.candidates(image.Rect(200, 0, 300, 64), now)
	if want := []int{4}; !slices.Equal(w.refine, want) {
		t.Errorf("candidates in the viewport %v, want %v", w.refine, want)
	}
	w.refineDelay = -1
	if w.candidates(image.Rectangle{}, now); len(w.refine) != 0 {
		t.Errorf("candidates %v with refinement off", w.refine)
	}
}

// gradient is a smooth pattern of 127 colours a 64-pixel tile, shifted by
// k: content of 17 to 256 colours, which goes lossy while it moves.
type gradient int

func (gradient) ColorModel() color.Model { return color.RGBAModel }
func (gradient) Bounds() image.Rectangle { return image.Rect(-1<<20, -1<<20, 1<<20, 1<<20) }
func (k gradient) At(x, y int) color.Color {
	v := uint8(x&63 + y&63 + int(k)*3)
	return color.RGBA{R: v, G: 255 - v, B: v / 2, A: 0xff}
}

// shifted is the corpus photograph moved k pixels to the left, wrapping.
type shifted struct {
	img *image.RGBA
	k   int
}

func (s shifted) ColorModel() color.Model { return color.RGBAModel }
func (s shifted) Bounds() image.Rectangle { return image.Rect(-1<<20, -1<<20, 1<<20, 1<<20) }
func (s shifted) At(x, y int) color.Color {
	b := s.img.Rect
	return s.img.RGBAAt(b.Min.X+((x+s.k)%b.Dx()+b.Dx())%b.Dx(), b.Min.Y+(y%b.Dy()+b.Dy())%b.Dy())
}

// animate paints img(k) over r at 30 frames a second for d.
func animate(b *tapped, r image.Rectangle, d time.Duration, img func(k int) image.Image) {
	for k := 0; time.Duration(k)*time.Second/30 < d; k++ {
		paint(b, r, img(k))
		time.Sleep(time.Second / 30)
	}
}

// webpDeflate is a client that decodes WebP and deflate, as a browser.
var webpDeflate = protocol.Hello{Visible: true, Capabilities: protocol.Capabilities{
	Codecs: []protocol.Codec{protocol.CodecWebP, protocol.CodecDeflate},
}}

// tileScores is the SSIM of each of r's tiles on the client against the
// server's.
func tileScores(client, server *image.RGBA, r image.Rectangle) []float64 {
	var out []float64
	for y := r.Min.Y; y < r.Max.Y; y += 64 {
		for x := r.Min.X; x < r.Max.X; x += 64 {
			tr := image.Rect(x, y, x+64, y+64).Intersect(r)
			out = append(out, encoder.SSIM(pixels(server, tr), pixels(client, tr), tr.Dx(), tr.Dy()))
		}
	}
	return out
}

// pixels copies r of img row by row.
func pixels(img *image.RGBA, r image.Rectangle) []byte {
	out := make([]byte, 0, 4*r.Dx()*r.Dy())
	for y := r.Min.Y; y < r.Max.Y; y++ {
		out = append(out, img.Pix[img.PixOffset(r.Min.X, y):img.PixOffset(r.Max.X, y)]...)
	}
	return out
}

// quiet fails if the client gets another frame in the next three
// seconds: refinement ends.
func quiet(t *testing.T, r *rig) {
	t.Helper()
	r.cl.mu.Lock()
	n := len(r.cl.frames)
	r.cl.mu.Unlock()
	time.Sleep(3 * time.Second)
	synctest.Wait()
	r.cl.mu.Lock()
	defer r.cl.mu.Unlock()
	if extra := len(r.cl.frames) - n; extra != 0 {
		t.Errorf("%d more frames on a still screen", extra)
	}
}

// Tiles of 17 to 256 colours go lossy while they move, and exact once they
// have been still for the refine delay, with scroll detection on or off;
// then refinement ends.
func TestRefineExact(t *testing.T) {
	for _, noScroll := range []bool{false, true} {
		synctest.Test(t, func(t *testing.T) {
			b := &tapped{Backend: backendtest.NewScreen(512, 256, 0)}
			r := newRigConfig(t, b, 64, 50e6, time.Millisecond, webpDeflate, Config{NoScroll: noScroll})
			time.Sleep(500 * time.Millisecond)
			synctest.Wait()
			area := image.Rect(64, 64, 320, 192)
			animate(b, area, time.Second, func(k int) image.Image { return gradient(k) })
			synctest.Wait()
			r.cl.mu.Lock()
			lossy, exact := r.cl.codecs[protocol.CodecWebP], r.cl.codecs[protocol.CodecDeflate]
			r.cl.mu.Unlock()
			if lossy == 0 {
				t.Fatalf("no scroll %v: no tile went lossy while it moved", noScroll)
			}
			time.Sleep(DefaultRefineDelay + time.Second)
			synctest.Wait()
			if !bytes.Equal(r.cl.screen().Pix, serverScreen(r.s).Pix) {
				t.Errorf("no scroll %v: the client's screen is not the server's after refinement", noScroll)
			}
			r.cl.mu.Lock()
			refined := r.cl.codecs[protocol.CodecDeflate] - exact
			r.cl.mu.Unlock()
			if refined < 8 {
				t.Errorf("no scroll %v: %d tiles refined, want the 8 that moved", noScroll, refined)
			}
			quiet(t, r)
		})
	}
}

// A photograph that moved goes at the pacer's lowest quality, and is
// raised once still to the quality its SSIM calls for, tile by tile, or
// the pacer's highest.
func TestRefinePhoto(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		img := photo(t)
		b := &tapped{Backend: backendtest.NewScreen(512, 256, 0)}
		r := newRigConfig(t, b, 64, 50e6, time.Millisecond, webpDeflate, Config{})
		time.Sleep(500 * time.Millisecond)
		synctest.Wait()
		area := image.Rect(0, 64, 512, 256)
		animate(b, area, time.Second, func(k int) image.Image { return shifted{img, 7 * k} })
		synctest.Wait()
		moving := tileScores(r.cl.screen(), serverScreen(r.s), area)
		time.Sleep(DefaultRefineDelay + time.Second)
		synctest.Wait()
		still := tileScores(r.cl.screen(), serverScreen(r.s), area)
		for i, s := range still {
			// At quality 90 the photograph's tiles score 0.91 and up.
			if s < 0.9 || s <= moving[i] {
				t.Errorf("tile %d: SSIM %.3f while moving, %.3f refined", i, moving[i], s)
			}
		}
		quiet(t, r)
	})
}

// A photograph that appears once goes at once at the quality its SSIM
// calls for, and needs no refinement.
func TestStillPhotoOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		img := photo(t)
		b := &tapped{Backend: backendtest.NewScreen(512, 256, 0)}
		r := newRigConfig(t, b, 64, 50e6, time.Millisecond, webpDeflate, Config{})
		time.Sleep(500 * time.Millisecond)
		synctest.Wait()
		area := image.Rect(0, 64, 512, 256)
		r.cl.mu.Lock()
		before := len(r.cl.frames)
		r.cl.mu.Unlock()
		paint(b, area, img)
		time.Sleep(100 * time.Millisecond)
		synctest.Wait()
		r.cl.mu.Lock()
		frames := len(r.cl.frames) - before
		r.cl.mu.Unlock()
		if frames != 1 {
			t.Errorf("the photograph took %d frames, want 1", frames)
		}
		for i, s := range tileScores(r.cl.screen(), serverScreen(r.s), area) {
			if s < 0.9 {
				t.Errorf("tile %d: SSIM %.3f", i, s)
			}
		}
		quiet(t, r)
	})
}

// A hidden viewer is refined nothing, and one with a viewport only the
// tiles in it, until the rest come into view.
func TestRefineView(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := &tapped{Backend: backendtest.NewScreen(512, 256, 0)}
		r := newRigConfig(t, b, 64, 50e6, time.Millisecond, webpDeflate, Config{})
		time.Sleep(500 * time.Millisecond)
		synctest.Wait()
		area := image.Rect(0, 64, 512, 192)
		animate(b, area, time.Second, func(k int) image.Image { return gradient(k) })
		r.cl.send(t, protocol.View{})
		synctest.Wait()
		r.cl.mu.Lock()
		n := len(r.cl.frames)
		r.cl.mu.Unlock()
		time.Sleep(2 * time.Second)
		synctest.Wait()
		r.cl.mu.Lock()
		if got := len(r.cl.frames); got != n {
			t.Errorf("%d frames while hidden", got-n)
		}
		r.cl.mu.Unlock()
		left := image.Rect(0, 0, 256, 256)
		r.cl.send(t, protocol.View{Visible: true, X: 0, Y: 0, W: 256, H: 256})
		time.Sleep(2 * time.Second)
		synctest.Wait()
		client, server := r.cl.screen(), serverScreen(r.s)
		if !bytes.Equal(pixels(client, area.Intersect(left)), pixels(server, area.Intersect(left))) {
			t.Error("the viewport is not exact after refinement")
		}
		if bytes.Equal(pixels(client, area.Sub(image.Pt(0, 0)).Intersect(image.Rect(320, 0, 512, 256))), pixels(server, area.Intersect(image.Rect(320, 0, 512, 256)))) {
			t.Error("tiles outside the viewport were refined")
		}
		r.cl.send(t, protocol.View{Visible: true})
		time.Sleep(2 * time.Second)
		synctest.Wait()
		if !bytes.Equal(r.cl.screen().Pix, serverScreen(r.s).Pix) {
			t.Error("the screen is not exact once all of it is in view")
		}
	})
}

// keyLatencies sends 40 keys 23 to 63 ms apart over a link of rate bytes a
// second and delay each way, starting when a moving photograph has been
// still for the refine delay, and returns the time each took to show and
// the tiles refined meanwhile.
func keyLatencies(t *testing.T, rate float64, delay time.Duration, cfg Config) (lat []time.Duration, refined int) {
	img := photo(t)
	b := &tapped{Backend: backendtest.NewScreen(1024, 768, 0)}
	r := newRigConfig(t, b, 64, rate, delay, webpDeflate, cfg)
	time.Sleep(500 * time.Millisecond)
	synctest.Wait()
	area := image.Rect(0, 64, 1024, 768)
	animate(b, area, time.Second, func(k int) image.Image { return shifted{img, 7 * k} })
	time.Sleep(DefaultRefineDelay + 20*time.Millisecond)
	sent := map[color.RGBA]time.Duration{}
	shown := map[color.RGBA]time.Duration{}
	r.cl.mu.Lock()
	tiles := r.cl.tiles
	r.cl.onFrame = func(f frameRecord) {
		px := r.cl.img.RGBAAt(1, 1)
		if _, ok := sent[px]; ok && shown[px] == 0 {
			shown[px] = f.at
		}
	}
	r.cl.mu.Unlock()
	for k := range 40 {
		key := uint32('a' + k)
		r.cl.mu.Lock()
		sent[backendtest.SwatchColor(key)] = time.Since(r.cl.start)
		r.cl.mu.Unlock()
		r.cl.send(t, protocol.Key{Seq: uint32(k + 1), Down: true, Keysym: key})
		time.Sleep(time.Duration(23+k*17%41) * time.Millisecond)
	}
	time.Sleep(time.Second)
	synctest.Wait()
	r.cl.mu.Lock()
	defer r.cl.mu.Unlock()
	for c, at := range sent {
		if shown[c] == 0 {
			t.Fatalf("key %v never showed", c)
		}
		lat = append(lat, shown[c]-at)
	}
	slices.Sort(lat)
	return lat, r.cl.tiles - tiles - len(sent)
}

// Refinement never delays an input by more than the pacer's target delay:
// keys pressed while a screen of photograph is refined, over the
// benchmark's N1 and N2, show at most 5 ms later than without refinement,
// the time the path takes to deliver a frame of refinement.
func TestRefineYieldsToInput(t *testing.T) {
	for _, c := range []struct {
		name  string
		rate  float64
		delay time.Duration
	}{
		{"N1", 100e6 / 8, 10 * time.Millisecond},
		{"N2", 20e6 / 8, 25 * time.Millisecond},
	} {
		t.Run(c.name, func(t *testing.T) {
			var with, without []time.Duration
			var refined int
			synctest.Test(t, func(t *testing.T) { with, refined = keyLatencies(t, c.rate, c.delay, Config{}) })
			synctest.Test(t, func(t *testing.T) { without, _ = keyLatencies(t, c.rate, c.delay, Config{NoRefine: true}) })
			if refined == 0 {
				t.Fatal("nothing was refined while the keys were pressed")
			}
			worst, base := with[len(with)-1], without[len(without)-1]
			late := 0
			for _, l := range with {
				if l > without[0]+time.Millisecond {
					late++
				}
			}
			t.Logf("key to display, median and worst: %v and %v while refining %d tiles (%d keys later than the fastest), %v and %v without",
				with[len(with)/2], worst, refined, late, without[len(without)/2], base)
			if worst > base+5*time.Millisecond+time.Millisecond {
				t.Errorf("a key took %v while refining, %v at most without", worst, base)
			}
		})
	}
}

// The writer's state for refinement follows resizes.
func TestRefineResize(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := &tapped{Backend: backendtest.NewScreen(512, 256, 0)}
		r := newRigConfig(t, b, 64, 50e6, time.Millisecond, webpDeflate, Config{})
		time.Sleep(500 * time.Millisecond)
		synctest.Wait()
		area := image.Rect(0, 64, 512, 256)
		animate(b, area, 500*time.Millisecond, func(k int) image.Image { return gradient(k) })
		b.Sink().Resize(320, 200)
		paint(b, image.Rect(0, 0, 320, 200), gradient(5))
		time.Sleep(2 * time.Second)
		synctest.Wait()
		if !bytes.Equal(r.cl.screen().Pix, serverScreen(r.s).Pix) {
			t.Error("the client's screen is not the server's after a resize")
		}
		quiet(t, r)
	})
}

var _ = slog.Default
