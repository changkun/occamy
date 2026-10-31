// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package stream

import (
	"image"
	"image/color"
	"image/draw"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"changkun.de/x/occamy/internal/backend/backendtest"
	"changkun.de/x/occamy/protocol"
)

// tileScreen is a screen of 64-pixel tiles that a test paints one tile
// at a time, and shows a copy of.
type tileScreen struct{ img *image.RGBA }

func newTileScreen(w, h int, fill func(r image.Rectangle) *image.RGBA) *tileScreen {
	s := &tileScreen{img: image.NewRGBA(image.Rect(0, 0, w, h))}
	for y := 0; y < h; y += 64 {
		for x := 0; x < w; x += 64 {
			s.set(x/64, y/64, fill(image.Rect(x, y, x+64, y+64).Intersect(s.img.Rect)))
		}
	}
	return s
}

// set paints tile (x, y) with src, whose bounds are the tile's.
func (s *tileScreen) set(x, y int, src *image.RGBA) {
	r := image.Rect(64*x, 64*y, 64*x+64, 64*y+64).Intersect(s.img.Rect)
	draw.Draw(s.img, r, src, r.Min, draw.Src)
}

func (s *tileScreen) shot() *image.RGBA {
	img := image.NewRGBA(s.img.Rect)
	copy(img.Pix, s.img.Pix)
	return img
}

// marked is pattern i at tile (x, y), marked for markEncoder to send
// lossy.
func marked(i, x, y int) *image.RGBA {
	img := pattern(i, image.Pt(64*x, 64*y))
	img.Pix[0] = lossyMark
	return img
}

// tileNotes are the notes of the tile messages since the last call.
func (c *tape) tileNotes() []string {
	var out []string
	for _, p := range c.msgs {
		if n := note(p); strings.HasPrefix(n, "tile ") {
			out = append(out, n)
		}
	}
	c.msgs = nil
	return out
}

// A run of lossy tiles ends before a tile that a slot holds, which goes
// cached; the exact tile after a run, encoded ahead of its message, is
// stored; and a run that the encoder sends exact is stored only if it
// is one tile, since a slot holds no more.
func TestCacheRuns(t *testing.T) {
	d := newDirect(t, false)
	d.w.enc = &markEncoder{}
	d.w.cache = newTileCache(DefaultCacheSlots)
	c := &tape{recorder: d.c}
	d.w.c = c
	s := newTileScreen(1024, 768, func(r image.Rectangle) *image.RGBA { return pattern(0, r.Min) })
	frame := func(name string, want ...string) {
		t.Helper()
		d.show(s.shot())
		d.frame()
		d.exact()
		if got := c.tileNotes(); !slices.Equal(got, want) {
			t.Errorf("%s:\n%s\nwant\n%s", name, strings.Join(got, "\n"), strings.Join(want, "\n"))
		}
	}
	d.show(s.shot())
	d.frame()
	if got := c.tileNotes(); len(got) != 192 || got[0] != "tile 0,0 64x64 raw, stored in slot 1" || got[191] != "tile 960,704 64x64 cached from slot 1" {
		t.Fatalf("the first frame went %d tiles, from %q to %q", len(got), got[0], got[len(got)-1])
	}

	s.set(1, 0, marked(1, 1, 0))
	frame("one lossy tile, sent exact", "tile 64,0 64x64 raw, stored in slot 2")
	s.set(0, 0, marked(2, 0, 0))
	s.set(1, 0, marked(3, 1, 0))
	frame("two lossy tiles, sent exact as one", "tile 0,0 128x64 raw")
	s.set(0, 0, marked(4, 0, 0))
	s.set(1, 0, marked(1, 1, 0))
	frame("a lossy tile, then one a slot holds",
		"tile 0,0 64x64 raw, stored in slot 3", "tile 64,0 64x64 cached from slot 2")
	s.set(0, 0, marked(5, 0, 0))
	s.set(1, 0, pattern(4, image.Pt(64, 0)))
	frame("a lossy tile, then an exact one encoded ahead",
		"tile 0,0 64x64 raw, stored in slot 4", "tile 64,0 64x64 raw, stored in slot 5")
	s.set(3, 1, pattern(4, image.Pt(192, 64)))
	frame("the tile encoded ahead, elsewhere", "tile 192,64 64x64 cached from slot 5")
}

// ramp is a tile of 64 colours, which goes lossy in motion and
// exact while still, the same wherever it is.
func ramp(i int, r image.Rectangle) *image.RGBA {
	img := image.NewRGBA(r)
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			k := (x - r.Min.X + 3*(y-r.Min.Y)) % 64
			img.SetRGBA(x, y, color.RGBA{uint8(4 * k), uint8(40 * i), uint8(255 - 4*k), 0xff}) // #nosec G115 -- small
		}
	}
	return img
}

// Refinement looks a tile up before it encodes it, and stores what it
// sends exact.
func TestCacheRefines(t *testing.T) {
	d := newRefineDirect(t, false)
	d.w.cache = newTileCache(DefaultCacheSlots)
	c := &tape{recorder: d.c}
	d.w.c = c
	s := newTileScreen(1024, 768, func(r image.Rectangle) *image.RGBA { return pattern(0, r.Min) })
	d.show(s.shot())
	d.frame()

	// Tiles (0, 0) and (2, 0) move: they end lossy, with content no slot
	// holds.
	for i := range 4 {
		d.age()
		s.set(0, 0, ramp(1+i, image.Rect(0, 0, 64, 64)))
		s.set(2, 0, ramp(5+i, image.Rect(128, 0, 192, 64)))
		d.show(s.shot())
		d.frame()
	}
	if got := c.tileNotes(); len(got) < 2 || !strings.HasSuffix(got[len(got)-2], "webp") || !strings.HasSuffix(got[len(got)-1], "webp") {
		t.Fatalf("the moving tiles went\n%s\nwant webp", strings.Join(got, "\n"))
	}
	// Tile (4, 2) shows what (2, 0) shows, still and so exact.
	s.set(4, 2, ramp(8, image.Rect(256, 128, 320, 192)))
	d.show(s.shot())
	d.frame()
	if got := c.tileNotes(); len(got) != 1 || !strings.HasPrefix(got[0], "tile 256,128 64x64 deflate, stored in slot ") {
		t.Fatalf("the still tile went %q", got)
	}

	// Refinement sends (0, 0) exact and stores it, and draws (2, 0)
	// from the slot that holds its content.
	stored, cached := d.c.cache.stored, d.c.cache.cached
	d.refineAll()
	if n := d.c.cache.stored - stored; n != 1 {
		t.Errorf("refinement stored %d tiles, want 1", n)
	}
	if n := d.c.cache.cached - cached; n != 1 {
		t.Errorf("refinement drew %d tiles from slots, want 1", n)
	}
	d.exact()
	c.msgs = nil
	s.set(6, 5, ramp(4, image.Rect(384, 320, 448, 384)))
	d.show(s.shot())
	d.frame()
	if got := c.tileNotes(); len(got) != 1 || !strings.HasPrefix(got[0], "tile 384,320 64x64 cached from slot ") {
		t.Errorf("the refined tile, elsewhere, went %q", got)
	}
	d.exact()

	// A message of refinement that the pacer holds back stores nothing,
	// even when the next message goes cached.
	for i := range 4 {
		d.age()
		s.set(0, 0, ramp(9+i, image.Rect(0, 0, 64, 64)))
		d.show(s.shot())
		d.frame()
	}
	c.msgs = nil
	d.w.refineDelay = 0
	d.w.versions = d.w.fb.Versions(d.w.versions[:0])
	d.w.candidates(image.Rectangle{}, time.Now())
	if len(d.w.refine) != 1 {
		t.Fatalf("%d tiles to refine, want 1", len(d.w.refine))
	}
	if send, ok := d.w.refineTile(1, d.w.refine[0]); !send || !ok || d.w.msgSlot == 0 {
		t.Fatalf("refinement: send %v, ok %v, slot %d", send, ok, d.w.msgSlot)
	}
	d.w.refineDelay = DefaultRefineDelay
	s.set(7, 5, ramp(8, image.Rect(448, 320, 512, 384)))
	d.show(s.shot())
	d.frame()
	if got := c.tileNotes(); len(got) != 1 || !strings.HasPrefix(got[0], "tile 448,320 64x64 cached from slot ") {
		t.Fatalf("a tile a slot holds went %q", got)
	}
	s.set(8, 5, ramp(12, image.Rect(512, 320, 576, 384)))
	d.show(s.shot())
	d.frame()
	if got := c.tileNotes(); len(got) != 1 || !strings.HasPrefix(got[0], "tile 512,320 64x64 deflate, stored in slot ") {
		t.Errorf("the tile whose refinement was held back went %q", got)
	}
	// Refined again, it comes from that slot.
	cached = d.c.cache.cached
	d.refineAll()
	if n := d.c.cache.cached - cached; n != 1 {
		t.Errorf("refinement drew %d tiles from slots, want 1", n)
	}
	d.exact()
}

// The server offers the cached codec and its slots unless
// Config.NoCache, and sends cached tiles only to a client that agreed
// to both.
func TestCacheAgreement(t *testing.T) {
	both := protocol.Capabilities{Codecs: []protocol.Codec{protocol.CodecRaw, protocol.CodecCached}, CacheSlots: 16}
	codecOnly := protocol.Capabilities{Codecs: both.Codecs}
	slotsOnly := protocol.Capabilities{Codecs: rawOnly.Codecs, CacheSlots: 16}
	for _, tc := range []struct {
		name   string
		caps   protocol.Capabilities
		cfg    Config
		slots  uint16
		cached bool
	}{
		{"both", both, Config{}, 16, true},
		{"fewer on the server", both, Config{Capabilities: protocol.Capabilities{CacheSlots: 4}}, 4, true},
		{"the server's cache off", both, Config{NoCache: true}, 0, false},
		{"no slots", codecOnly, Config{}, 0, true},
		{"not the codec", slotsOnly, Config{}, 16, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				tc.cfg.SessionID = "s1"
				// Its fill and swatch repeat across the screen's tiles.
				r := newRigConfig(t, backendtest.NewScreen(320, 192, 0), 64, 50e6, time.Millisecond,
					protocol.Hello{Visible: true, Capabilities: tc.caps}, tc.cfg)
				time.Sleep(time.Second)
				synctest.Wait()
				r.cl.mu.Lock()
				defer r.cl.mu.Unlock()
				w := r.cl.welcome
				if w == nil {
					t.Fatal("no welcome")
				}
				if got := slices.Contains(w.Capabilities.Codecs, protocol.CodecCached); got != tc.cached || w.Capabilities.CacheSlots != tc.slots {
					t.Errorf("welcome: codecs %v, %d slots; want cached %v, %d slots", w.Capabilities.Codecs, w.Capabilities.CacheSlots, tc.cached, tc.slots)
				}
				hits := r.cl.cache.cached
				if want := tc.cached && tc.slots != 0; (hits > 0) != want {
					t.Errorf("%d cached tiles sent", hits)
				}
			})
		})
	}
}
