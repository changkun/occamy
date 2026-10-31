// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package stream

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"math/rand/v2"
	"testing"

	"changkun.de/x/occamy/internal/framebuffer"
)

// pagePixel is the pixel at x of row y of a page of seed: no two rows of
// a page, nor of two pages, are alike.
func pagePixel(seed, x, y int) color.RGBA {
	v := uint64(seed)*0x9e3779b97f4a7c15 ^ uint64(y)*0xbf58476d1ce4e5b9 ^ uint64(x)*0x94d049bb133111eb // #nosec G115 -- test patterns
	v ^= v >> 31
	v *= 0xd6e8feb86657d7ab
	v ^= v >> 29
	return color.RGBA{byte(v), byte(v >> 8), byte(v >> 16), 0xff}
}

// drawPage paints r of the screen with page seed from its row top on.
func drawPage(fb *framebuffer.Framebuffer, r image.Rectangle, seed, top int) {
	img := fb.Lock(r)
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			img.SetRGBA(x, y, pagePixel(seed, x, top+y-r.Min.Y))
		}
	}
	fb.Unlock(r)
}

// fill paints r of the screen in one colour.
func fillFB(fb *framebuffer.Framebuffer, r image.Rectangle, c color.RGBA) {
	img := fb.Lock(r)
	draw.Draw(img, r, image.NewUniform(c), image.Point{}, draw.Src)
	fb.Unlock(r)
}

// scrollRig runs a scroller as a writer does, without a connection: it
// sends every dirty tile raw to a model client, after the copies found.
type scrollRig struct {
	t      *testing.T
	fb     *framebuffer.Framebuffer
	sc     *scroller
	client *image.RGBA
	sent   []uint64
}

func newScrollRig(t *testing.T, w, h, tile int) *scrollRig {
	t.Helper()
	fb := framebuffer.New(tile)
	fb.Resize(w, h)
	r := &scrollRig{t: t, fb: fb, sc: &scroller{}}
	r.resized()
	return r
}

// resized takes the framebuffer's size, as syncSize does.
func (r *scrollRig) resized() {
	b := r.fb.Bounds()
	tile := r.fb.TileSize()
	g := grid{bounds: b, tile: tile, cols: (b.Dx() + tile - 1) / tile}
	r.sc.reset(g)
	cols, rows := r.fb.Grid()
	r.sent = make([]uint64, cols*rows)
	r.client = image.NewRGBA(b)
}

// dirty is the tiles changed since sent, in order.
func (r *scrollRig) dirty() []int {
	var order []int
	for i, v := range r.fb.Versions(nil) {
		if v > r.sent[i] {
			order = append(order, i)
		}
	}
	return order
}

// frame sends what changed: the copies found, then the tiles still
// dirty. It checks that each tile a copy completed shows the screen
// once the copies are applied, and returns the copies and how many
// tiles went as tiles.
func (r *scrollRig) frame() (copies []scrollCopy, tiles int) {
	r.t.Helper()
	order := r.dirty()
	for _, cp := range r.sc.find(r.fb, order) {
		dst := cp.rect(r.sc.g)
		draw.Draw(r.client, dst, r.client, dst.Min.Add(image.Pt(0, cp.dy)), draw.Src)
		r.sc.apply(cp)
		for k, t := range cp.done {
			r.sent[t] = cp.vers[k]
		}
		cp.done = append([]int(nil), cp.done...)
		copies = append(copies, cp)
	}
	screen := r.screen()
	for _, cp := range copies {
		for _, t := range cp.done {
			tr := r.sc.g.rect(t)
			if !sameRect(r.client, screen, tr) {
				r.t.Fatalf("tile %d at %v differs after the copy %+v", t, tr, cp)
			}
		}
	}
	for _, t := range order {
		if r.sent[t] >= r.fb.Version(t) {
			continue
		}
		buf := make([]byte, 4*r.fb.TileSize()*r.fb.TileSize())
		tr, v := r.fb.ReadTile(t, buf)
		src := &image.RGBA{Pix: buf, Stride: 4 * tr.Dx(), Rect: tr}
		draw.Draw(r.client, tr, src, tr.Min, draw.Src)
		r.sc.keep(t, buf)
		r.sent[t] = v
		tiles++
	}
	r.check()
	return copies, tiles
}

// check fails unless the client shows the screen and the shadow holds
// what the client shows.
func (r *scrollRig) check() {
	r.t.Helper()
	screen := r.screen()
	if !bytes.Equal(r.client.Pix, screen.Pix) {
		r.t.Fatal("the client's screen differs from the server's")
	}
	g := r.sc.g
	for y := range g.bounds.Dy() {
		for c := range g.cols {
			row := r.sc.row(c, y)
			if row == nil {
				continue
			}
			x := g.rect(c).Min.X
			if !bytes.Equal(row, r.client.Pix[r.client.PixOffset(x, y):r.client.PixOffset(x, y)+len(row)]) {
				r.t.Fatalf("the shadow's row %d of column %d differs from the client's", y, c)
			}
		}
	}
}

func (r *scrollRig) screen() *image.RGBA {
	b := r.fb.Bounds()
	img := image.NewRGBA(b)
	cols, rows := r.fb.Grid()
	buf := make([]byte, 4*r.fb.TileSize()*r.fb.TileSize())
	for i := range cols * rows {
		tr, _ := r.fb.ReadTile(i, buf)
		draw.Draw(img, tr, &image.RGBA{Pix: buf, Stride: 4 * tr.Dx(), Rect: tr}, tr.Min, draw.Src)
	}
	return img
}

func sameRect(a, b *image.RGBA, r image.Rectangle) bool {
	for y := r.Min.Y; y < r.Max.Y; y++ {
		if !bytes.Equal(a.Pix[a.PixOffset(r.Min.X, y):a.PixOffset(r.Max.X, y)], b.Pix[b.PixOffset(r.Min.X, y):b.PixOffset(r.Max.X, y)]) {
			return false
		}
	}
	return true
}

// A page scrolled by any offset, up or down, is found exactly: one copy
// of the rows that stayed on the screen, and tiles only where the
// exposed rows are.
func TestScrollOffsets(t *testing.T) {
	const w, h, tile = 256, 512, 64
	for _, dy := range []int{1, 13, 63, 64, 65, 100, 300, -1, -13, -64, -200} {
		t.Run(fmt.Sprint(dy), func(t *testing.T) {
			r := newScrollRig(t, w, h, tile)
			screen := image.Rect(0, 0, w, h)
			drawPage(r.fb, screen, 1, 1000)
			r.frame()
			drawPage(r.fb, screen, 1, 1000+dy)
			copies, tiles := r.frame()
			if len(copies) != 1 {
				t.Fatalf("%d copies, want 1: %+v", len(copies), copies)
			}
			cp := copies[0]
			// The rows that remain move by dy; the exposed rows are |dy|
			// at the bottom for a scroll down, at the top for one up.
			a, b := 0, h-dy
			if dy < 0 {
				a, b = -dy, h
			}
			if cp.dy != dy || cp.a != a || cp.b != b || cp.rect(r.sc.g) != image.Rect(0, a, w, b) {
				t.Errorf("copy %v by %d, want %v by %d", cp.rect(r.sc.g), cp.dy, image.Rect(0, a, w, b), dy)
			}
			// Every row of tiles the exposed rows touch is sent again, and
			// no other.
			exposed := (abs(dy) + tile - 1) / tile
			if want := exposed * w / tile; tiles != want {
				t.Errorf("%d tiles sent, want %d", tiles, want)
			}
		})
	}
}

func abs(x int) int { return max(x, -x) }

// A pane that scrolls beside a still sidebar is copied in the columns it
// fills; a column it shares with the sidebar is sent as tiles, since its
// rows did not move as a whole. (A sidebar of one colour would: it looks
// the same at any offset, and the column would be copied, rightly.)
func TestScrollPartialWidth(t *testing.T) {
	const w, h, tile = 320, 384, 64
	for _, side := range []int{64, 100} {
		t.Run(fmt.Sprint(side), func(t *testing.T) {
			r := newScrollRig(t, w, h, tile)
			drawPage(r.fb, image.Rect(0, 0, side, h), 3, 0)
			pane := image.Rect(side, 0, w, h)
			drawPage(r.fb, pane, 2, 0)
			r.frame()
			drawPage(r.fb, pane, 2, 20)
			copies, _ := r.frame()
			if len(copies) != 1 {
				t.Fatalf("%d copies, want 1: %+v", len(copies), copies)
			}
			first := (side + tile - 1) / tile // the first column the pane fills
			want := image.Rect(first*tile, 0, w, h-20)
			if got := copies[0].rect(r.sc.g); got != want || copies[0].dy != 20 {
				t.Errorf("copy %v by %d, want %v by 20", got, copies[0].dy, want)
			}
		})
	}
}

// Content made of repeated rows gives no row a unique hash, so nothing
// votes and nothing is copied, though a copy would be right.
func TestScrollRepeatedRows(t *testing.T) {
	const w, h = 256, 256
	stripes := func(fb *framebuffer.Framebuffer, phase int) {
		img := fb.Lock(fb.Bounds())
		for y := range h {
			c := color.RGBA{0xff, 0xff, 0xff, 0xff}
			if (y+phase)%2 == 0 {
				c = color.RGBA{0, 0, 0, 0xff}
			}
			draw.Draw(img, image.Rect(0, y, w, y+1), image.NewUniform(c), image.Point{}, draw.Src)
		}
		fb.Unlock(fb.Bounds())
	}
	r := newScrollRig(t, w, h, 64)
	stripes(r.fb, 0)
	r.frame()
	stripes(r.fb, 1)
	if copies, _ := r.frame(); len(copies) != 0 {
		t.Errorf("stripes: %d copies, want none", len(copies))
	}
	fillFB(r.fb, r.fb.Bounds(), color.RGBA{0x10, 0x20, 0x30, 0xff})
	r.frame()
	fillFB(r.fb, r.fb.Bounds(), color.RGBA{0x10, 0x20, 0x31, 0xff})
	if copies, _ := r.frame(); len(copies) != 0 {
		t.Errorf("a new colour: %d copies, want none", len(copies))
	}
	// A page whose rows repeat every 8 rows, scrolled by 3.
	period := func(phase int) {
		img := r.fb.Lock(r.fb.Bounds())
		for y := range h {
			for x := range w {
				img.SetRGBA(x, y, pagePixel(5, x, (y+phase)%8))
			}
		}
		r.fb.Unlock(r.fb.Bounds())
	}
	period(0)
	r.frame()
	period(3)
	if copies, _ := r.frame(); len(copies) != 0 {
		t.Errorf("a period of 8 rows: %d copies, want none", len(copies))
	}
}

// A hash that collides makes the vote pick an offset that the pixels do
// not show: the exact comparison refuses it, and the screen stays right.
func TestScrollHashCollision(t *testing.T) {
	defer func(h func([]byte) uint64) { rowHash = h }(rowHash)
	// The hash of a row is its first pixel.
	rowHash = func(b []byte) uint64 { return uint64(binary.LittleEndian.Uint32(b)) }
	const w, h = 256, 384
	r := newScrollRig(t, w, h, 64)
	// Every row starts with a pixel of its own, and the rest of it is
	// the page.
	paint := func(top, seed int) {
		img := r.fb.Lock(r.fb.Bounds())
		for y := range h {
			for x := range w {
				img.SetRGBA(x, y, pagePixel(seed, x, y))
			}
			img.SetRGBA(0, y, color.RGBA{byte(top + y), byte((top + y) >> 8), 1, 0xff})
		}
		r.fb.Unlock(r.fb.Bounds())
	}
	paint(0, 1)
	r.frame()
	// The first pixels moved up by 9 rows, the rest did not move: every
	// sampled row votes for 9.
	paint(9, 2)
	if dy := r.vote(); dy != 9 {
		t.Fatalf("the colliding hash voted for %d, want 9: the test does not collide", dy)
	}
	if copies, _ := r.frame(); len(copies) != 0 {
		t.Errorf("%d copies of rows that only collide", len(copies))
	}
	// Half the screen scrolled for real by 9 rows, the other half only
	// collides: the copy covers the half that moved and no more.
	paint(0, 3)
	r.frame()
	img := r.fb.Lock(r.fb.Bounds())
	for y := range h {
		for x := range w {
			if y < 192 {
				img.SetRGBA(x, y, pagePixel(3, x, y+9))
			} else {
				img.SetRGBA(x, y, pagePixel(4, x, y))
			}
		}
		img.SetRGBA(0, y, color.RGBA{byte(9 + y), byte((9 + y) >> 8), 1, 0xff})
	}
	r.fb.Unlock(r.fb.Bounds())
	copies, _ := r.frame()
	if len(copies) != 1 || copies[0].a != 0 || copies[0].b != 192 || copies[0].dy != 9 {
		t.Errorf("copies %+v, want rows 0 to 192 by 9", copies)
	}
}

// vote runs the vote of column 0's run of dirty tiles.
func (r *scrollRig) vote() int {
	r.sc.dirty = nil
	_, rows := r.fb.Grid()
	rn := run{c: 0, r0: 0, r1: rows}
	if !r.sc.read(r.fb, rn) {
		r.t.Fatal("resized")
	}
	return r.sc.vote(rn)
}

// Tiles at the right and bottom edges, smaller than the tile size, are
// searched and copied like the others.
func TestScrollEdges(t *testing.T) {
	const w, h = 200, 150 // 3 columns of 64 and one of 8; rows of 64, 64, 22
	r := newScrollRig(t, w, h, 64)
	screen := image.Rect(0, 0, w, h)
	drawPage(r.fb, screen, 7, 0)
	r.frame()
	drawPage(r.fb, screen, 7, 30)
	copies, tiles := r.frame()
	if len(copies) != 1 || copies[0].rect(r.sc.g) != image.Rect(0, 0, w, h-30) {
		t.Fatalf("copies %+v, want one of %v", copies, image.Rect(0, 0, w, h-30))
	}
	// Rows 120 to 150 are exposed: the tiles of rows of tiles 1 and 2.
	if tiles != 8 {
		t.Errorf("%d tiles sent, want 8", tiles)
	}
	// Down again, by 3 rows: the bottom row of tiles is 22 high.
	drawPage(r.fb, screen, 7, 27)
	if copies, _ := r.frame(); len(copies) != 1 || copies[0].dy != -3 {
		t.Errorf("copies %+v, want one by -3", copies)
	}
}

// After a resize the client's screen is undefined: nothing is copied
// from it, though the new screen shows the old one scrolled.
func TestScrollResize(t *testing.T) {
	r := newScrollRig(t, 256, 256, 64)
	drawPage(r.fb, r.fb.Bounds(), 8, 0)
	r.frame()
	r.fb.Resize(256, 320)
	// The search of a grid that no longer matches finds nothing.
	if copies := r.sc.find(r.fb, []int{0, 4, 8}); len(copies) != 0 {
		t.Fatalf("%d copies across a resize", len(copies))
	}
	r.resized()
	drawPage(r.fb, r.fb.Bounds(), 8, 10)
	if copies, _ := r.frame(); len(copies) != 0 {
		t.Fatalf("%d copies from a screen the client has not been sent", len(copies))
	}
	drawPage(r.fb, r.fb.Bounds(), 8, 20)
	if copies, _ := r.frame(); len(copies) != 1 {
		t.Fatalf("%d copies once the new screen was sent, want 1", len(copies))
	}
}

// Rows only partly sent since a resize are not a source: the tile that
// holds them is undefined on the client until sent whole.
func TestScrollUndefinedSource(t *testing.T) {
	r := newScrollRig(t, 128, 256, 64)
	drawPage(r.fb, r.fb.Bounds(), 9, 0)
	r.frame()
	// Forget the bottom row of tiles, as if never sent.
	for c := range 2 {
		r.sc.valid[3*2+c] = false
	}
	drawPage(r.fb, r.fb.Bounds(), 9, 40)
	copies, _ := r.frame()
	if len(copies) != 1 || copies[0].b != 192-40 {
		t.Fatalf("copies %+v, want rows up to %d", copies, 192-40)
	}
}

// The search's cost is bounded: a change of one tile is not searched; a
// change of the whole screen that is no scroll gives up after
// scrollMaxMisses runs, each hashing its column once and an eighth of its
// own rows; and a scroll of the whole screen votes once, the other
// columns trying the offset found.
func TestScrollBounds(t *testing.T) {
	const w, h, tile = 1024, 768, 64
	r := newScrollRig(t, w, h, tile)
	drawPage(r.fb, r.fb.Bounds(), 1, 0)
	r.frame()
	fillFB(r.fb, image.Rect(100, 100, 110, 110), color.RGBA{0xff, 0, 0, 0xff})
	r.frame()
	if r.sc.searched != 0 || r.sc.hashes+r.sc.rehashed != 0 {
		t.Errorf("one tile: %d runs searched, %d rows hashed", r.sc.searched, r.sc.hashes+r.sc.rehashed)
	}
	drawPage(r.fb, r.fb.Bounds(), 2, 0)
	if copies, _ := r.frame(); len(copies) != 0 {
		t.Fatalf("%d copies of a new page", len(copies))
	}
	if r.sc.searched != scrollMaxMisses || r.sc.hashes > scrollMaxMisses*h/scrollStride || r.sc.rehashed > scrollMaxMisses*h {
		t.Errorf("a new page: %d runs searched, %d rows of it hashed and %d of the shadow; want %d runs, at most %d and %d",
			r.sc.searched, r.sc.hashes, r.sc.rehashed, scrollMaxMisses, scrollMaxMisses*h/scrollStride, scrollMaxMisses*h)
	}
	drawPage(r.fb, r.fb.Bounds(), 2, 10)
	if copies, _ := r.frame(); len(copies) != 1 {
		t.Fatalf("%d copies of a scroll", len(copies))
	}
	if r.sc.searched != w/tile || r.sc.hashes > h/scrollStride || r.sc.rehashed > h {
		t.Errorf("a scroll: %d runs searched, %d rows of it hashed and %d of the shadow; want %d runs, at most %d and %d",
			r.sc.searched, r.sc.hashes, r.sc.rehashed, w/tile, h/scrollStride, h)
	}
}

// Two runs of one column whose copies would read what the other wrote
// get one copy: the client applies copies in turn, and each was checked
// against the screen before any.
func TestScrollConflict(t *testing.T) {
	const w, h = 64, 512
	r := newScrollRig(t, w, h, 64)
	drawPage(r.fb, r.fb.Bounds(), 1, 0)
	r.frame()
	// Rows 0 to 128 show the page's rows 256 to 384, and rows 192 to 320
	// its rows 0 to 128; row of tiles 2 stays as it was.
	drawPage(r.fb, image.Rect(0, 0, w, 128), 1, 256)
	drawPage(r.fb, image.Rect(0, 192, w, 320), 1, 0)
	copies, _ := r.frame()
	if len(copies) != 1 {
		t.Errorf("%d copies, want 1: %+v", len(copies), copies)
	}
	// Two that do not: rows 0 to 128 from 256 below, rows 320 to 448 from
	// 128 above.
	drawPage(r.fb, r.fb.Bounds(), 1, 0)
	r.frame()
	drawPage(r.fb, image.Rect(0, 0, w, 128), 1, 256)
	drawPage(r.fb, image.Rect(0, 320, w, 448), 1, 192)
	if copies, _ := r.frame(); len(copies) != 2 {
		t.Errorf("%d copies, want 2: %+v", len(copies), copies)
	}
}

// Random scrolls, of the whole screen or of panes, and random paints:
// the client always ends up showing the screen, and the shadow what the
// client shows.
func TestScrollRandom(t *testing.T) {
	for seed := range uint64(40) {
		rng := rand.New(rand.NewPCG(seed, 1))
		w, h := 64+rng.IntN(400), 64+rng.IntN(400)
		tile := []int{16, 32, 64}[rng.IntN(3)]
		r := newScrollRig(t, w, h, tile)
		page := int(seed)
		// The screen shows the page in a sidebar and a pane right of it,
		// each scrolled to its own row.
		side, sideTop, paneTop := rng.IntN(w/2), 0, 0
		show := func() {
			drawPage(r.fb, image.Rect(0, 0, side, h), page, sideTop)
			drawPage(r.fb, image.Rect(side, 0, w, h), page, paneTop)
		}
		if side == 0 {
			side = 1
		}
		show()
		r.frame()
		copies := 0
		for range 40 {
			dy := rng.IntN(h) - h/2
			if rng.IntN(8) == 0 {
				dy = (h - 1 - rng.IntN(8)) * (1 - 2*rng.IntN(2)) // nearly a page
			}
			switch rng.IntN(6) {
			case 0: // a paint
				x, y := rng.IntN(w), rng.IntN(h)
				rect := image.Rect(x, y, x+1+rng.IntN(w), y+1+rng.IntN(h)).Intersect(r.fb.Bounds())
				fillFB(r.fb, rect, color.RGBA{byte(rng.IntN(256)), 0, byte(rng.IntN(4)), 0xff})
			case 1: // a resize
				r.fb.Resize(64+rng.IntN(400), 64+rng.IntN(400))
				w, h = r.fb.Bounds().Dx(), r.fb.Bounds().Dy()
				side = 1 + rng.IntN(w/2)
				r.resized()
				show()
			case 2: // the whole screen scrolls
				sideTop += dy
				paneTop += dy
				show()
			default: // the pane scrolls
				paneTop += dy
				drawPage(r.fb, image.Rect(side, 0, w, h), page, paneTop)
			}
			c, _ := r.frame()
			copies += len(c)
		}
		if copies == 0 {
			t.Errorf("seed %d: no copy in 30 steps", seed)
		}
	}
}
