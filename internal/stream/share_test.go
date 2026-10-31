// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package stream

import (
	"bytes"
	"fmt"
	"image"
	"image/draw"
	"math/rand/v2"
	"slices"
	"testing"
	"time"

	"changkun.de/x/occamy/internal/encoder"
	"changkun.de/x/occamy/protocol"
)

// An encoding is made once for its tile's version and asked for by
// everyone else: older pixels are told to encode alone, newer ones
// replace it, and one that could not be made, or whose maker panicked,
// is made again by the next to ask.
func TestEncodeCacheEntries(t *testing.T) {
	c := newEncodeCache(1 << 20)
	k := tileKey{tile: 3}
	made := func(e *entry, payload string) {
		t.Helper()
		c.make(e, func(e *entry) bool {
			e.payload, e.codec = []byte(payload), protocol.CodecDeflate
			return true
		})
	}

	e, mine := c.tile(k, 5)
	if e == nil || !mine {
		t.Fatal("the first to ask for a tile does not make it")
	}
	w, mine := c.tile(k, 5)
	if w != e || mine {
		t.Fatal("the second to ask for a tile being made does not wait for it")
	}
	made(e, "v5")
	<-w.done
	if !w.ok || string(w.payload) != "v5" {
		t.Fatalf("the waiter got %q, ok %v", w.payload, w.ok)
	}
	if e, _ := c.tile(k, 4); e != nil {
		t.Error("older pixels got the encoding of newer ones")
	}
	if e, _ := c.tile(tileKey{tile: 3, moving: true}, 5); e == w {
		t.Error("a moving tile got the encoding of a still one")
	}
	if e, _ := c.tile(tileKey{p: profile{lossless: protocol.CodecPNG}, tile: 3}, 5); e == w {
		t.Error("another profile got the encoding of this one")
	}

	n, mine := c.tile(k, 6)
	if n == nil || n == w || !mine {
		t.Fatal("newer pixels did not replace the encoding")
	}
	c.make(n, func(*entry) bool { return false })
	if !n.isDone() || n.ok {
		t.Fatal("an encoding that could not be made is not published as failed")
	}
	again, mine := c.tile(k, 6)
	if again == n || !mine {
		t.Fatal("an encoding that could not be made is not made again")
	}
	func() {
		defer func() { _ = recover() }()
		c.make(again, func(*entry) bool { panic("encoder") })
	}()
	if !again.isDone() || again.ok {
		t.Fatal("an encoding whose maker panicked is not published as failed")
	}
	if e, mine := c.tile(k, 6); e == again || !mine {
		t.Fatal("an encoding whose maker panicked is not made again")
	}
}

func (e *entry) isDone() bool {
	select {
	case <-e.done:
		return true
	default:
		return false
	}
}

// A run's encoding is known by the versions of all its tiles: any older
// is told to encode alone, any newer replaces it.
func TestEncodeCacheRuns(t *testing.T) {
	c := newEncodeCache(1 << 20)
	k := runKey{tile: 4, n: 3, still: true, q: 90, lo: 30}
	e, mine := c.run(k, []uint64{7, 8, 9})
	if !mine {
		t.Fatal("the first to ask for a run does not make it")
	}
	c.make(e, func(e *entry) bool { e.payload, e.codec = []byte("run"), protocol.CodecWebP; return true })
	if w, mine := c.run(k, []uint64{7, 8, 9}); w != e || mine {
		t.Error("the same versions did not find the run")
	}
	if w, _ := c.run(k, []uint64{7, 8, 6}); w != nil {
		t.Error("a run with an older tile got the encoding")
	}
	if w, _ := c.run(runKey{tile: 4, n: 3, still: true, q: 80, lo: 30}, []uint64{7, 8, 9}); w == e {
		t.Error("another quality got the encoding")
	}
	if w, mine := c.run(k, []uint64{7, 10, 9}); w == e || !mine {
		t.Error("a run with a newer tile did not replace the encoding")
	}
	if s := c.stats(); s.entries != 0 || s.bytes != 0 {
		t.Errorf("the replaced run still counts: %+v", s)
	}
}

// The cache holds no more than its budget, the oldest encodings going
// first; one replaced while it was made never counts.
func TestEncodeCacheBudget(t *testing.T) {
	const size = 1000
	c := newEncodeCache(3 * (size + entryBytes))
	make1 := func(tile int, v uint64) *entry {
		t.Helper()
		e, mine := c.tile(tileKey{tile: tile}, v)
		if !mine {
			t.Fatalf("tile %d at %d is not made", tile, v)
		}
		c.make(e, func(e *entry) bool { e.payload = make([]byte, size); return true })
		return e
	}
	for tile := range 5 {
		make1(tile, 1)
	}
	if s := c.stats(); s.bytes != 3*(size+entryBytes) || s.entries != 3 {
		t.Errorf("five encodings in a budget of three hold %d bytes in %d entries", s.bytes, s.entries)
	}
	for tile := range 5 {
		_, mine := c.tile(tileKey{tile: tile}, 1)
		if want := tile < 2; mine != want {
			t.Errorf("tile %d is made again: %v, want %v", tile, mine, want)
		}
	}

	c = newEncodeCache(1 << 20)
	old, _ := c.tile(tileKey{tile: 1}, 1)
	newer, _ := c.tile(tileKey{tile: 1}, 2)
	c.make(old, func(e *entry) bool { e.payload = make([]byte, size); return true })
	if s := c.stats(); s.bytes != 0 || s.entries != 0 {
		t.Errorf("an encoding replaced while it was made counts: %+v", s)
	}
	c.make(newer, func(e *entry) bool { e.payload = make([]byte, size); return true })
	if s := c.stats(); s.bytes != size+entryBytes || s.entries != 1 {
		t.Errorf("one encoding holds %d bytes in %d entries", s.bytes, s.entries)
	}
}

// The viewers of a session share its encode cache and workers, which
// the last to leave stops; viewers of another session share another.
func TestShareRelease(t *testing.T) {
	a, b := newDirect(t, false), newDirect(t, false)
	fa, fb := a.w.fb, b.w.fb
	s1 := share(fa, 2, 1<<20)
	s2 := share(fa, 2, 1<<20)
	if s1 != s2 {
		t.Fatal("two viewers of a session do not share")
	}
	if s3 := share(fb, 2, 1<<20); s3 == s1 {
		t.Fatal("viewers of two sessions share")
	} else {
		s3.release(fb)
	}
	s1.release(fa)
	if !s2.submit(func(*worker) {}) {
		t.Fatal("the workers stopped while a viewer was left")
	}
	s2.release(fa)
	sharedMu.Lock()
	_, left := sharedFor[fa]
	sharedMu.Unlock()
	if left {
		t.Error("the session's share outlives its viewers")
	}
	if s4 := share(fa, 2, 1<<20); s4 == s1 {
		t.Error("a viewer after the last left found the old share")
	} else {
		s4.release(fa)
	}
}

// Only internal/encoder's encoders share, by the codecs they send in.
func TestProfile(t *testing.T) {
	if _, ok := profileOf(&markEncoder{}); ok {
		t.Error("another encoder shares")
	}
	if _, ok := profileOf(nil); ok {
		t.Error("no encoder shares")
	}
	webp, _ := profileOf(encoder.New([]protocol.Codec{protocol.CodecWebP, protocol.CodecDeflate}))
	jpeg, _ := profileOf(encoder.New([]protocol.Codec{protocol.CodecJPEG, protocol.CodecDeflate}))
	again, _ := profileOf(encoder.New([]protocol.Codec{protocol.CodecDeflate, protocol.CodecWebP}))
	if webp == jpeg || webp != again {
		t.Errorf("profiles: webp %v, jpeg %v, webp again %v", webp, jpeg, again)
	}
}

// The workers skip the tiles that the client's tile cache holds, as the
// writer sends them cached: they look tiles up under the mirror's seed.
func TestPrefetchSkipsCached(t *testing.T) {
	d := newDirect(t, false)
	d.w.cache = newTileCache(16)
	s := newTileScreen(1024, 768, func(r image.Rectangle) *image.RGBA { return pattern(0, r.Min) })
	for y := range 12 {
		for x := range 16 {
			s.set(x, y, pattern((x+y)%4, image.Pt(64*x, 64*y)))
		}
	}
	d.show(s.shot())
	d.frame()

	// The same tiles, elsewhere: every one is in a slot.
	for y := range 12 {
		for x := range 16 {
			s.set(x, y, pattern((x+y+1)%4, image.Pt(64*x, 64*y)))
		}
	}
	d.show(s.shot())
	sh := &shared{cache: newEncodeCache(DefaultEncodeCacheBytes), jobs: make(chan func(*worker), 64)}
	p, _ := profileOf(d.w.enc)
	d.w.sh, d.w.prof = sh, p
	if _, ok := d.w.collect(image.Rectangle{}); !ok {
		t.Fatal("resized")
	}
	if f := d.w.prefetch(d.w.order); f == nil {
		t.Fatal("no rows for the workers")
	}
	wk := &worker{encs: map[profile]*encoder.Encoder{}}
	jobs := 0
	for len(sh.jobs) > 0 {
		(<-sh.jobs)(wk)
		jobs++
	}
	if s := sh.cache.stats(); jobs == 0 || s.misses != 0 {
		t.Errorf("the workers encoded %d tiles or runs in %d rows of tiles the client has", s.misses, jobs)
	}
}

// sharedWriter is a writer of d that shares a cache with no workers.
func sharedWriter(t *testing.T, d *direct) *shared {
	t.Helper()
	sh := &shared{cache: newEncodeCache(DefaultEncodeCacheBytes)}
	p, ok := profileOf(d.w.enc)
	if !ok {
		t.Fatal("the encoder is not internal/encoder's")
	}
	d.w.sh, d.w.prof = sh, p
	return sh
}

// A tile's encoding while it moves is not its encoding still: a tile of
// 17 to 256 colours goes lossy while it moves, and exact otherwise.
func TestShareMoving(t *testing.T) {
	d := newDirect(t, false)
	sharedWriter(t, d)
	w := d.w
	r := image.Rect(0, 0, 64, 64)
	pix := w.tile(0)
	for i := range 64 * 64 {
		pix[4*i], pix[4*i+1], pix[4*i+2], pix[4*i+3] = uint8(i%100), uint8(2*(i%100)), 0x40, 0xff // #nosec G115 -- small
	}
	_, _, moving := w.encodeTile(nil, 0, 0, 1, r, true)
	_, codec, still := w.encodeTile(nil, 0, 0, 1, r, false)
	if !moving || still || codec != protocol.CodecDeflate {
		t.Errorf("a tile of 100 colours: lossy %v moving, lossy %v still in %v", moving, still, codec)
	}
}

// A run's encoding at one quality, or searched from one lowest, is not
// one at another.
func TestShareRunQuality(t *testing.T) {
	d := newDirect(t, false)
	sharedWriter(t, d)
	w := d.w
	photo := corpusFrame(t, "photo")
	r := image.Rect(0, 0, 64, 64)
	pix := pixels(photo, r.Add(image.Pt(256, 192)))
	w.runVersion = append(w.runVersion[:0], 1)
	encode := func(still bool, q, lo int) ([]byte, int) {
		_, got, _ := w.encodeRun(0, 1, pix, r, still, q, lo)
		return slices.Clone(w.payload), got
	}
	low, _ := encode(false, 30, 0)
	high, _ := encode(false, 90, 0)
	if bytes.Equal(low, high) {
		t.Error("a run at 90 got the encoding at 30")
	}
	_, from30 := encode(true, 90, 30)
	_, from85 := encode(true, 90, 85)
	if from30 >= 85 || from85 < 85 {
		t.Errorf("Still from 30 chose %d, from 85 chose %d", from30, from85)
	}
}

// The workers encode a frame's rows as the writer then sends them: each
// tile, and each run of lossy tiles between tiles that are clean or
// exact, so that the writer finds every encoding they made.
func TestPrefetchMatchesWriter(t *testing.T) {
	d := newDirect(t, false)
	photo := corpusFrame(t, "photo")
	d.show(photo)
	d.frame()

	// Every other run of tiles changes, some of them to a pattern.
	next := image.NewRGBA(photo.Rect)
	draw.Draw(next, next.Rect, photo, image.Pt(0, 32), draw.Src)
	img := image.NewRGBA(photo.Rect)
	copy(img.Pix, photo.Pix)
	for y := range 12 {
		for x := range 16 {
			r := image.Rect(64*x, 64*y, 64*x+64, 64*y+64)
			switch {
			case (x/3+y)%2 == 1:
			case x%5 == 4:
				draw.Draw(img, r, pattern(x+y, r.Min), r.Min, draw.Src)
			default:
				draw.Draw(img, r, next, r.Min, draw.Src)
			}
		}
	}
	d.show(img)
	sh := sharedWriter(t, d)
	sh.jobs = make(chan func(*worker), 64)
	w := d.w
	if _, ok := w.collect(image.Rectangle{}); !ok {
		t.Fatal("resized")
	}
	if f := w.prefetch(w.order); f == nil {
		t.Fatal("no rows for the workers")
	}
	wk := &worker{encs: map[profile]*encoder.Encoder{}}
	for len(sh.jobs) > 0 {
		(<-sh.jobs)(wk)
	}
	// A worker asks again for an exact tile that ended a run, as the
	// first of the next message.
	before := sh.cache.stats()
	made := before.misses
	sh.jobs = nil // the writer encodes the rest alone
	if ok, _ := w.p.StartFrame(time.Now()); !ok {
		t.Fatal("the pacer holds the frame")
	}
	if err := w.sendFrame(d.ctx, w.order, false); err != nil {
		t.Fatal(err)
	}
	if s := sh.cache.stats(); made == 0 || s.hits-before.hits != made {
		t.Errorf("the workers made %d encodings, and the writer found %d", made, s.hits-before.hits)
	}
}

// Viewers that share the encode cache, and its workers, send what each
// would alone: random screens of tiles of a few patterns and of a
// photograph, moving and still, then refined, with resizes. Every
// viewer that shares sends the same messages, byte for byte, as the
// others; where the screens are only patterns, as a viewer alone; and
// each client ends with the server's pattern tiles exact and its
// photographic tiles at the score refinement seeks.
func TestShareConverges(t *testing.T) {
	photo := corpusFrame(t, "photo")
	modes := []shareMode{{"cache", true, 0}, {"workers", true, 2}}
	for _, m := range modes {
		for seed := range uint64(4) {
			patterns := seed < 2
			t.Run(fmt.Sprintf("%s/seed%d", m.name, seed), func(t *testing.T) {
				alone := runShareSeed(t, 1, shareModes[0], seed, patterns, photo)
				shared := runShareSeed(t, 3, m, seed, patterns, photo)
				for k := 1; k < len(shared); k++ {
					if i := firstDiff(shared[0], shared[k]); i >= 0 {
						t.Errorf("viewer %d's message %d differs from viewer 0's: %s, not %s", k, i, noteAt(shared[k], i), noteAt(shared[0], i))
					}
				}
				if patterns {
					if i := firstDiff(alone[0], shared[0]); i >= 0 {
						t.Errorf("message %d differs from a viewer's alone", i)
					}
				}
			})
		}
	}
}

// noteAt is a note of message i of msgs.
func noteAt(msgs [][]byte, i int) string {
	if i >= len(msgs) {
		return "none"
	}
	m, err := protocol.Decode(msgs[i], protocol.ServerMessages(), testLimits)
	if err != nil {
		return err.Error()
	}
	return fmt.Sprintf("%T %+v", m, m)
}

// drawing keeps of msgs those that draw: what viewers of a session are
// sent alike, unlike the session's state.
func drawing(msgs [][]byte) [][]byte {
	var out [][]byte
	for _, p := range msgs {
		m, err := protocol.Decode(p, protocol.ServerMessages(), testLimits)
		if err != nil {
			out = append(out, p)
			continue
		}
		switch m.(type) {
		case *protocol.Tile, *protocol.CopyRect, *protocol.Resize, *protocol.FrameEnd:
			out = append(out, p)
		}
	}
	return out
}

// firstDiff returns the index of the first message that differs in a
// and b, -1 if none.
func firstDiff(a, b [][]byte) int {
	for i := range max(len(a), len(b)) {
		if i >= len(a) || i >= len(b) || !bytes.Equal(a[i], b[i]) {
			return i
		}
	}
	return -1
}

// runShareSeed sends n viewers of mode m the screens of seed, and returns
// each viewer's messages, after checking what each client shows.
func runShareSeed(t *testing.T, n int, m shareMode, seed uint64, patterns bool, photo *image.RGBA) [][][]byte {
	t.Helper()
	rng := rand.New(rand.NewPCG(seed, 2026))
	vs := newViewers(t, n, m)
	tapes := make([]*tape, n)
	for k, d := range vs.ds {
		tapes[k] = &tape{recorder: d.c}
		d.w.c = tapes[k]
	}
	d0 := vs.ds[0]
	size := image.Pt(1024, 768)
	img := image.NewRGBA(image.Rectangle{Max: size})
	isPhoto := map[image.Point]bool{}
	paintTile := func(x, y int) {
		r := image.Rect(64*x, 64*y, 64*x+64, 64*y+64).Intersect(img.Rect)
		at := image.Pt(x, y)
		if !patterns && rng.IntN(3) == 0 {
			off := image.Pt(rng.IntN(photo.Rect.Dx()-64), rng.IntN(photo.Rect.Dy()-64))
			draw.Draw(img, r, photo, off, draw.Src)
			isPhoto[at] = true
			return
		}
		draw.Draw(img, r, pattern(rng.IntN(6), r.Min), r.Min, draw.Src)
		delete(isPhoto, at)
	}
	cols, rows := (size.X+63)/64, (size.Y+63)/64
	for y := range rows {
		for x := range cols {
			paintTile(x, y)
		}
	}
	show := func() {
		// show keeps the image, which paintTile goes on changing.
		next := image.NewRGBA(img.Rect)
		copy(next.Pix, img.Pix)
		d0.show(next)
	}
	show()
	send := func() {
		t.Helper()
		for _, d := range vs.ds {
			d.age()
		}
		if _, _, err := vs.each((*direct).send); err != nil {
			t.Fatal(err)
		}
	}
	send()
	for frame := range 8 {
		if frame == 5 {
			// A resize to a size whose edge tiles are smaller.
			size = image.Pt(1000, 700)
			img = image.NewRGBA(image.Rectangle{Max: size})
			cols, rows = (size.X+63)/64, (size.Y+63)/64
			clear(isPhoto)
			for y := range rows {
				for x := range cols {
					paintTile(x, y)
				}
			}
			d0.b.Sink().Resize(size.X, size.Y)
			d0.prev = nil
			show()
			send()
			continue
		}
		for range 1 + rng.IntN(12) {
			paintTile(rng.IntN(cols), rng.IntN(rows))
		}
		show()
		send()
	}
	if _, _, err := vs.each(func(d *direct) error { _, err := d.refine(); return err }); err != nil {
		t.Fatal(err)
	}

	if vs.sh != nil {
		if s := vs.sh.cache.stats(); s.hits == 0 {
			t.Errorf("the viewers shared no encoding: %+v", s)
		}
	}
	if !patterns && !d0.c.lossy {
		t.Error("no tile went lossy")
	}

	server := serverScreen(d0.s)
	for k, d := range vs.ds {
		client := d.c.img
		for y := range rows {
			for x := range cols {
				r := image.Rect(64*x, 64*y, 64*x+64, 64*y+64).Intersect(server.Rect)
				if !isPhoto[image.Pt(x, y)] {
					if !bytes.Equal(pixels(client, r), pixels(server, r)) {
						t.Fatalf("%s, %d viewers: viewer %d's pattern tile %v differs from the server's; client lossy %v, SSIM %.3f", m.name, n, k, r.Min, d.c.lossy, tileScores(client, server, r)[0])
					}
					continue
				}
				if s := tileScores(client, server, r)[0]; s < shareMinSSIM {
					t.Errorf("viewer %d's photographic tile %v has an SSIM of %.3f", k, r.Min, s)
				}
			}
		}
	}
	out := make([][][]byte, n)
	for k, c := range tapes {
		out[k] = drawing(c.msgs)
	}
	return out
}

// shareMinSSIM is the least SSIM a refined photographic tile has: the
// score refinement seeks, less what Still's estimate leaves.
const shareMinSSIM = 0.85
