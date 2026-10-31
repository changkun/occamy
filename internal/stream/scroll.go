// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package stream

import (
	"bytes"
	"context"
	"image"
	"math"
	"math/bits"
	"slices"
	"time"

	"github.com/zeebo/xxh3"

	"changkun.de/x/occamy/internal/framebuffer"
	"changkun.de/x/occamy/protocol"
)

// Scroll detection (specs/008-native-architecture/graphics-pipeline.md,
// Implementation notes: Scroll detection) finds the dirty parts of the
// screen that a viewer's client already shows elsewhere, moved up or
// down, and sends a copy-rect for them instead of their tiles.
//
// A copy-rect copies the client's pixels, so the screen it is found
// against is the one that client shows: each writer keeps a shadow, the
// pixels of every tile it sent and every copy it made, in the order the
// client applies them. A viewer that skipped frames, or whose tiles
// outside its viewport are old, has a shadow that says so, and a copy
// is found against exactly that. Versions cannot stand in for it: the
// framebuffer holds only the current pixels, and in a scroll the
// source rows have changed too.
//
// The search is per tile column. Row hashes (xxh3-64) of every eighth
// row of a dirty run of tiles vote for an offset among the shadow's row
// hashes of the same column; a row whose hash occurs twice in the
// column, such as a blank line, does not vote. The rows the winning
// offset copies are then found by comparing pixels exactly, so a hash
// collision can cost a search but never a wrong pixel.

// rowHash hashes the pixels of one row of a tile column. Tests replace
// it to force collisions.
var rowHash = xxh3.Hash

const (
	// scrollMinTiles is how many dirty tiles one above the other make a
	// column worth searching: a key, a caret or a button never reaches
	// the hashing.
	scrollMinTiles = 2
	// scrollStride is the distance between the rows of a run that vote.
	scrollStride = 8
	// scrollMinVotes is the fewest votes an offset needs.
	scrollMinVotes = 2
	// scrollMaxMisses is how many more runs than it found copies in a
	// frame searches without finding one before it stops: motion repaints
	// every tile and scrolls none, and a scroll beside motion is found
	// in a few runs.
	scrollMaxMisses = 3
)

// lossless is the quality of pixels the client has exactly; a lossy
// quality is below it.
const lossless = math.MaxUint8

// final is the quality of lossy pixels whose SSIM reached the encoder's
// score (refine.go): they need no raising whatever the pacer allows, so
// it is above every lossy quality, and a copy of them completes a tile
// as a copy of exact pixels does.
const final = lossless - 1

// lossyQuality is the shadow's record of the lossy quality q.
func lossyQuality(q int) uint8 {
	return uint8(min(max(q, 0), final-1)) // #nosec G115 -- clamped below final
}

// shadow is the screen as a viewer's client has it, tile by tile.
type shadow struct {
	g    grid
	rows int // rows of tiles
	// pix holds each tile's pixels row by row at its width, nil until a
	// tile or a copy drew it.
	pix [][]byte
	// valid marks the tiles whose pixels the client has: drawn whole
	// since the last resize, after which the screen is undefined.
	valid []bool
	// qual is the quality of each valid tile's pixels on the client:
	// lossless, or the lowest lossy quality any of its rows was sent at.
	qual []uint8
	// hash holds the row hashes of each tile, g.tile of them, which
	// hashed says are current.
	hash   []uint64
	hashed []bool
	// rehashed counts the rows hashed, for tests of the search's cost.
	rehashed int
}

// reset empties the shadow for a screen tiled as g.
func (s *shadow) reset(g grid) {
	s.g = g
	s.rows = 0
	if g.cols > 0 {
		s.rows = (g.bounds.Dy() + g.tile - 1) / g.tile
	}
	n := g.cols * s.rows
	s.pix = make([][]byte, n)
	s.valid = make([]bool, n)
	s.qual = make([]uint8, n)
	s.hash = make([]uint64, n*g.tile)
	s.hashed = make([]bool, n)
}

// keep makes buf, tile t's pixels as just sent at quality q, the
// shadow's, and returns the buffer it held, which may be nil. For a tile
// sent lossy the shadow holds the pixels that were encoded, and the
// client an approximation of them at q: a copy of them moves the
// approximation, and completes a tile only where q is no lower than the
// quality the tile would be sent at.
func (s *shadow) keep(t int, buf []byte, q uint8) []byte {
	old := s.pix[t]
	s.pix[t] = buf
	s.valid[t] = true
	s.qual[t] = q
	s.hashed[t] = false
	return old
}

// row returns row y of the screen in column c, the width of the
// column's tiles, or nil if the client has no defined pixels there.
func (s *shadow) row(c, y int) []byte {
	t := y/s.g.tile*s.g.cols + c
	if !s.valid[t] {
		return nil
	}
	w := 4 * s.g.rect(t).Dx()
	o := y % s.g.tile * w
	return s.pix[t][o : o+w]
}

// rowHash returns the hash of row y in column c, which must be valid.
func (s *shadow) rowHash(c, y int) uint64 {
	t := y/s.g.tile*s.g.cols + c
	if !s.hashed[t] {
		r := s.g.rect(t)
		w := 4 * r.Dx()
		for k := range r.Dy() {
			s.hash[t*s.g.tile+k] = rowHash(s.pix[t][k*w : (k+1)*w])
		}
		s.hashed[t] = true
		s.rehashed += r.Dy()
	}
	return s.hash[t*s.g.tile+y%s.g.tile]
}

// scrollCopy is a copy-rect that a search found: columns c0 to c1 of
// tiles, rows a to b of the screen, take the rows dy below them (above
// them if dy is negative). It completes tiles done, which then show
// their pixels at the versions in vers.
type scrollCopy struct {
	c0, c1 int
	a, b   int
	dy     int
	done   []int
	vers   []uint64
}

// rect is the copy's destination.
func (cp *scrollCopy) rect(g grid) image.Rectangle {
	return image.Rect(g.rect(cp.c0).Min.X, cp.a, g.rect(cp.c1-1).Max.X, cp.b)
}

// message is the copy as protocol v1 sends it.
func (cp *scrollCopy) message(g grid, frame uint32) protocol.CopyRect {
	r := cp.rect(g)
	return protocol.CopyRect{
		FrameID: frame,
		SrcX:    uint16(r.Min.X),         // #nosec G115 -- screens are at most 65535 pixels wide
		SrcY:    uint16(r.Min.Y + cp.dy), // #nosec G115 -- the source lies on the screen
		DstX:    uint16(r.Min.X),         // #nosec G115 -- screens are at most 65535 pixels wide
		DstY:    uint16(r.Min.Y),         // #nosec G115 -- and high
		W:       uint16(r.Dx()),          // #nosec G115 -- at most the screen's width
		H:       uint16(r.Dy()),          // #nosec G115 -- and height
	}
}

// run is a vertical run of dirty tiles in one column: rows r0 to r1 of
// tiles.
type run struct{ c, r0, r1 int }

// scroller searches a viewer's dirty tiles for scrolls against its
// shadow.
type scroller struct {
	shadow

	dirty  []bool
	runs   []run
	col    []byte   // the current pixels of the run searched, row by row
	vers   []uint64 // the version each of its tiles was read at
	keys   []uint64 // the shadow's row hashes by slot, for the vote
	slots  []int32  // the row of each slot plus one; 0 empty, -1 repeated
	dys    []int
	quals  []uint8 // apply's qualities of a column's tiles
	copies []scrollCopy

	// What the last search cost: the runs it read, the rows of them it
	// hashed, and the offsets that won a vote and were compared.
	searched, hashes, tried int
}

// find returns the copies that bring the client's screen, as the shadow
// holds it, closer to fb's in the tiles of order, with the tiles each
// completes at a quality of at least target, to be sent and applied in
// the order returned. Each copy moves rows within the tile columns it
// spans. It returns nil if the screen no longer matches the shadow's
// grid.
//
// It searches the longest runs first, and stops once it has searched
// scrollMaxMisses runs more without a copy than it found copies in: at
// most 2h+3 runs for h copies.
func (sc *scroller) find(fb *framebuffer.Framebuffer, order []int, target int) []scrollCopy {
	g := sc.g
	sc.copies = sc.copies[:0]
	sc.searched, sc.hashes, sc.tried, sc.rehashed = 0, 0, 0, 0
	if len(order) < scrollMinTiles || g.cols == 0 {
		return nil
	}
	n := g.cols * sc.rows
	if cap(sc.dirty) < n {
		sc.dirty = make([]bool, n)
	}
	sc.dirty = sc.dirty[:n]
	clear(sc.dirty)
	for _, t := range order {
		if t < n {
			sc.dirty[t] = true
		}
	}
	sc.runs = sc.runs[:0]
	for c := range g.cols {
		for r := 0; r < sc.rows; {
			if !sc.dirty[r*g.cols+c] {
				r++
				continue
			}
			r0 := r
			for r < sc.rows && sc.dirty[r*g.cols+c] {
				r++
			}
			if r-r0 >= scrollMinTiles {
				sc.runs = append(sc.runs, run{c, r0, r})
			}
		}
	}
	if len(sc.runs) == 0 {
		return nil // the quick reject: no column has a run to scroll
	}
	// The longest runs first, as the likeliest to be a scroll and the
	// ones a miss costs most.
	slices.SortStableFunc(sc.runs, func(x, y run) int { return (y.r1 - y.r0) - (x.r1 - x.r0) })

	hint, misses := 0, 0
	for _, rn := range sc.runs {
		if misses >= scrollMaxMisses+len(sc.copies) {
			break
		}
		if !sc.read(fb, rn) {
			sc.copies = sc.copies[:0]
			return nil // resized
		}
		cp, ok := scrollCopy{}, false
		if hint != 0 {
			cp, ok = sc.try(rn, hint, target)
		}
		if !ok {
			if dy := sc.vote(rn); dy != 0 {
				sc.tried++
				cp, ok = sc.try(rn, dy, target)
			}
		}
		if !ok || sc.conflicts(cp) {
			misses++
			continue
		}
		hint = cp.dy
		sc.copies = append(sc.copies, cp)
	}
	sc.merge()
	return sc.copies
}

// read reads run rn's tiles from fb into sc.col, and their versions into
// sc.vers. It reports false if the screen was resized.
func (sc *scroller) read(fb *framebuffer.Framebuffer, rn run) bool {
	g := sc.g
	w := 4 * g.rect(rn.c).Dx()
	size := w * g.tile * (rn.r1 - rn.r0)
	if cap(sc.col) < size {
		sc.col = make([]byte, size)
	}
	sc.col = sc.col[:size]
	sc.vers = sc.vers[:0]
	sc.searched++
	for r := rn.r0; r < rn.r1; r++ {
		t := r*g.cols + rn.c
		got, v := fb.ReadTile(t, sc.col[(r-rn.r0)*g.tile*w:])
		if got != g.rect(t) || fb.Bounds() != g.bounds {
			return false
		}
		sc.vers = append(sc.vers, v)
	}
	return true
}

// cur returns row y of the screen in run rn's column as read.
func (sc *scroller) cur(rn run, y int) []byte {
	w := 4 * sc.g.rect(rn.c).Dx()
	o := (y - rn.r0*sc.g.tile) * w
	return sc.col[o : o+w]
}

// span is the screen rows of run rn.
func (sc *scroller) span(rn run) (y0, y1 int) {
	return rn.r0 * sc.g.tile, min(rn.r1*sc.g.tile, sc.g.bounds.Max.Y)
}

// vote returns the offset that more of run rn's sampled rows moved by
// than any other, or 0 if none got scrollMinVotes: each changed row
// found once among the shadow's rows of the column votes for the
// distance to it. A plurality is enough, since try checks the offset
// pixel by pixel: two panes in one column may scroll by two offsets.
func (sc *scroller) vote(rn run) int {
	h := sc.g.bounds.Dy()
	size := 1 << bits.Len(uint(2*h))
	shift := 64 - bits.Len(uint(size-1))
	if len(sc.slots) != size {
		sc.slots = make([]int32, size)
		sc.keys = make([]uint64, size)
	}
	clear(sc.slots)
	slot := func(k uint64) int {
		// Fibonacci hashing spreads hashes that differ only in their high
		// bits, as a test's weak hash may.
		i := int((k * 0x9e3779b97f4a7c15) >> shift) // #nosec G115 -- shifted down to an index of the table
		for sc.slots[i] != 0 && sc.keys[i] != k {
			i = (i + 1) & (size - 1)
		}
		return i
	}
	for y := range h {
		if sc.row(rn.c, y) == nil {
			continue
		}
		k := sc.rowHash(rn.c, y)
		switch i := slot(k); sc.slots[i] {
		case 0:
			sc.keys[i], sc.slots[i] = k, int32(y+1) // #nosec G115 -- screens are at most 65535 pixels high
		default:
			sc.slots[i] = -1 // repeated, as blank rows are: no vote
		}
	}
	sc.dys = sc.dys[:0]
	y0, y1 := sc.span(rn)
	for y := y0 + scrollStride/2; y < y1; y += scrollStride {
		k := rowHash(sc.cur(rn, y))
		sc.hashes++
		if sc.row(rn.c, y) != nil && sc.rowHash(rn.c, y) == k {
			continue // unchanged
		}
		if s := sc.slots[slot(k)]; s > 0 && int(s-1) != y {
			sc.dys = append(sc.dys, int(s-1)-y)
		}
	}
	if len(sc.dys) < scrollMinVotes {
		return 0
	}
	slices.Sort(sc.dys)
	best, votes := 0, 0
	for i := 0; i < len(sc.dys); {
		j := i
		for j < len(sc.dys) && sc.dys[j] == sc.dys[i] {
			j++
		}
		if j-i > votes {
			best, votes = sc.dys[i], j-i
		}
		i = j
	}
	if votes < scrollMinVotes {
		return 0
	}
	return best
}

// try finds the longest span of run rn's rows that are, pixel for pixel,
// the shadow's rows dy below, and the tiles a copy of it completes: those
// whose rows outside the span the client already has, in a tile it has
// drawn, and whose pixels end up at a quality of at least target. It
// reports false if the copy would draw no tile right, at any quality. A
// copy whose tiles are right but of a lower quality is still sent, since
// it costs a few bytes, and they go as tiles after it, as they would
// without it.
func (sc *scroller) try(rn run, dy, target int) (scrollCopy, bool) {
	y0, y1 := sc.span(rn)
	h := sc.g.bounds.Dy()
	a, b := 0, 0
	for y := y0; y < y1; {
		for y < y1 && !sc.moved(rn, y, dy, h) {
			y++
		}
		s := y
		for y < y1 && sc.moved(rn, y, dy, h) {
			y++
		}
		if y-s > b-a {
			a, b = s, y
		}
	}
	if a == b {
		return scrollCopy{}, false
	}
	cp := scrollCopy{c0: rn.c, c1: rn.c + 1, a: a, b: b, dy: dy}
	tile := sc.g.tile
	right := 0
	for r := a / tile; r <= (b-1)/tile; r++ {
		t := r*sc.g.cols + rn.c
		ty0, ty1 := r*tile, min((r+1)*tile, h)
		// Rows outside the copy must already be right, in a tile whose
		// pixels the client has: row is nil in one it has not.
		complete := true
		for y := ty0; complete && y < ty1; y++ {
			if y < a || y >= b {
				own := sc.row(rn.c, y)
				complete = own != nil && bytes.Equal(sc.cur(rn, y), own)
			}
		}
		if !complete {
			continue
		}
		right++
		if int(sc.copied(rn.c, r, a, b, dy)) >= target {
			cp.done = append(cp.done, t)
			cp.vers = append(cp.vers, sc.vers[r-rn.r0])
		}
	}
	return cp, right > 0
}

// copied returns the quality of the pixels of tile row r of column c once
// rows a to b of the column take those dy below: the lowest quality of
// the tiles its copied rows come from, and of its own if it keeps any of
// its rows.
func (s *shadow) copied(c, r, a, b, dy int) uint8 {
	tile := s.g.tile
	ty0, ty1 := r*tile, min((r+1)*tile, s.g.bounds.Dy())
	y0, y1 := max(a, ty0), min(b, ty1)
	q := uint8(lossless)
	if y0 < y1 {
		for sr := (y0 + dy) / tile; sr <= (y1-1+dy)/tile; sr++ {
			q = min(q, s.qual[sr*s.g.cols+c])
		}
	}
	if y0 > ty0 || y1 < ty1 {
		q = min(q, s.qual[r*s.g.cols+c])
	}
	return q
}

// moved reports whether row y of run rn is the shadow's row y+dy of the
// same column, pixel for pixel.
func (sc *scroller) moved(rn run, y, dy, h int) bool {
	sy := y + dy
	if sy < 0 || sy >= h {
		return false
	}
	src := sc.row(rn.c, sy)
	return src != nil && bytes.Equal(sc.cur(rn, y), src)
}

// conflicts reports whether cp would read rows of its column that a copy
// found before writes, or write rows such a copy reads, in the order
// merge sends them: the client applies copies one after another, while
// each was checked against the shadow before any. Within a column that
// order is by the first row written.
func (sc *scroller) conflicts(cp scrollCopy) bool {
	overlap := func(a0, a1, b0, b1 int) bool { return a0 < b1 && b0 < a1 }
	for _, o := range sc.copies {
		if o.c0 != cp.c0 {
			continue
		}
		first, then := o, cp
		if cp.a < o.a {
			first, then = cp, o
		}
		if overlap(then.a+then.dy, then.b+then.dy, first.a, first.b) {
			return true
		}
	}
	return false
}

// merge joins copies of adjacent columns that move the same rows by the
// same offset into one, and orders the copies by first row, then
// column, which within a column is the order conflicts expects.
func (sc *scroller) merge() {
	if len(sc.copies) < 2 {
		return
	}
	slices.SortFunc(sc.copies, func(x, y scrollCopy) int {
		if x.a != y.a {
			return x.a - y.a
		}
		return x.c0 - y.c0
	})
	out := sc.copies[:1]
	for _, cp := range sc.copies[1:] {
		last := &out[len(out)-1]
		if cp.c0 == last.c1 && cp.a == last.a && cp.b == last.b && cp.dy == last.dy {
			last.c1 = cp.c1
			last.done = append(last.done, cp.done...)
			last.vers = append(last.vers, cp.vers...)
			continue
		}
		out = append(out, cp)
	}
	sc.copies = out
}

// apply makes the shadow show cp as the client does once it applies it:
// the rows a to b of its columns become those dy below, with their
// hashes, and the tiles they land in take their quality.
func (sc *scroller) apply(cp scrollCopy) {
	g, tile := sc.g, sc.g.tile
	r0, r1 := cp.a/tile, (cp.b-1)/tile
	for c := cp.c0; c < cp.c1; c++ {
		// The qualities as the rows had them before the copy.
		sc.quals = sc.quals[:0]
		for r := r0; r <= r1; r++ {
			sc.quals = append(sc.quals, sc.copied(c, r, cp.a, cp.b, cp.dy))
		}
		w := 4 * g.rect(c).Dx()
		// Sources below are read before they are written, top to bottom;
		// sources above, bottom to top.
		y, end, step := cp.a, cp.b, 1
		if cp.dy < 0 {
			y, end, step = cp.b-1, cp.a-1, -1
		}
		for ; y != end; y += step {
			sy := y + cp.dy
			t, st := y/tile*g.cols+c, sy/tile*g.cols+c
			if sc.pix[t] == nil {
				sc.pix[t] = make([]byte, 4*tile*tile)
			}
			o, so := y%tile*w, sy%tile*w
			copy(sc.pix[t][o:o+w], sc.pix[st][so:so+w])
			if sc.hashed[st] {
				sc.hash[t*tile+y%tile] = sc.hash[st*tile+sy%tile]
			} else {
				sc.hashed[t] = false
			}
		}
		for r := r0; r <= r1; r++ {
			t := r*g.cols + c
			sc.qual[t] = sc.quals[r-r0]
			if cp.a <= r*tile && min((r+1)*tile, g.bounds.Max.Y) <= cp.b {
				sc.valid[t] = true
			}
		}
	}
}

// scroll searches order, the tiles the frame is about to send, for
// scrolls, sends a copy-rect for each, and takes the tiles they complete
// at the pacer's quality as sent. Copies go out unpaced, as frame-end
// does: they are a few bytes. It reports the copies sent, the tiles they
// completed and their bytes.
//
// A copy is applied to the shadow only once it is written: one the
// client would not take, which no client allowed to connect refuses, is
// neither sent nor applied, and nor are the copies after it.
func (w *writer) scroll(ctx context.Context, id uint32, order []int, input bool) (copies, done, size int, err error) {
	start := time.Now()
	found := w.sc.find(w.fb, order, w.p.Quality())
	took := time.Since(start)
	w.scrollTime += took
	w.encodeTime += took
	for _, cp := range found {
		w.sbuf = cp.message(w.g, id).Append(w.sbuf[:0])
		if err := w.limits.CheckSize(len(w.sbuf)); err != nil {
			w.log.Warn("copy-rect not sent", "err", err)
			break
		}
		if err := w.c.WriteMessage(ctx, w.sbuf); err != nil {
			return copies, done, size, err
		}
		w.sc.apply(cp)
		at := time.Since(w.start)
		for k, t := range cp.done {
			w.sent[t] = cp.vers[k]
			// Pixels moved in from elsewhere: refinement finds out what
			// they are to reach.
			w.goal[t] = 0
			w.changed[t] = at
			if input {
				w.effects[t] = cp.vers[k]
				w.heat[t] = 0
			}
		}
		copies++
		done += len(cp.done)
		size += len(w.sbuf)
	}
	return copies, done, size, nil
}
