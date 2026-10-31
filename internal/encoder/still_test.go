// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package encoder

import (
	"bytes"
	"image"
	"math"
	"math/bits"
	"math/rand/v2"
	"slices"
	"testing"

	"changkun.de/x/occamy/internal/codec"
	"changkun.de/x/occamy/protocol"
)

// Motion sends tiles of 17 to 256 colours lossy, and no others that
// would go lossless still: up to 16 colours stay lossless, and a viewer
// with no lossy codec gets every tile lossless.
func TestTileMotion(t *testing.T) {
	for _, codecs := range [][]protocol.Codec{{webpC, deflate}, {jpegC, raw}, {deflate}, {raw}, {webpC}} {
		e := New(codecs)
		hasLossy := slices.Contains(codecs, webpC) || slices.Contains(codecs, jpegC)
		hasLossless := slices.Contains(codecs, deflate) || slices.Contains(codecs, raw)
		for _, n := range []int{1, 16, 17, 256, 257} {
			pix := tileWith(64, 64, n, uint64(n))
			var p codec.Palette
			class := Classify(codec.Colours(pix, 64, 64, &p))
			for _, moving := range []bool{false, true} {
				_, c, lossy := e.Tile(nil, pix, 64, 64, moving)
				want := !hasLossless || hasLossy && (class == Photo || class == Many && moving)
				if lossy != want {
					t.Errorf("%v, %d colours, moving %v: lossy %v, want %v", codecs, n, moving, lossy, want)
				}
				if !lossy && !exactCodec(c) {
					t.Errorf("%v, %d colours, moving %v: lossless in %v", codecs, n, moving, c)
				}
			}
		}
	}
}

func exactCodec(c protocol.Codec) bool { return c == raw || c == deflate }

// refWorst is the measurement's SSIM of b to a, both w by h, of the
// worst of the blocks bw pixels wide that make them up, each scored as
// an image of its own; a block narrower or lower than the window, which
// it cannot score, counts as identical.
func refWorst(a, b []byte, w, h, bw int) float64 {
	worst := 1.0
	ia := &image.RGBA{Pix: a, Stride: 4 * w, Rect: image.Rect(0, 0, w, h)}
	ib := &image.RGBA{Pix: b, Stride: 4 * w, Rect: image.Rect(0, 0, w, h)}
	for x := 0; x < w; x += bw {
		r := image.Rect(x, 0, min(x+bw, w), h)
		if r.Dx() < 8 || h < 8 {
			continue
		}
		pa := &image.RGBA{Pix: pixels(ia, r), Stride: 4 * r.Dx(), Rect: image.Rect(0, 0, r.Dx(), h)}
		pb := &image.RGBA{Pix: pixels(ib, r), Stride: 4 * r.Dx(), Rect: image.Rect(0, 0, r.Dx(), h)}
		worst = min(worst, refSSIM(pa, pb))
	}
	return worst
}

// near reports whether two scores agree to 1e-9; NaN agrees with nothing.
func near(a, b float64) bool { return math.Abs(a-b) <= 1e-9 }

// The scorer matches the measurement's SSIM, which builds summed-area
// tables, on any pixels and sizes, whole or block by block, and scores
// what it cannot window as identical.
func TestSSIMScore(t *testing.T) {
	var s ssimer
	rng := rand.New(rand.NewPCG(1, 2))
	photo := loadFrame(t, "photo")
	for _, sz := range []image.Point{{8, 8}, {64, 64}, {61, 17}, {9, 100}, {1024, 64}, {200, 64}} {
		for k := range 3 {
			a := pixels(photo, image.Rect(0, 64*k, sz.X, 64*k+sz.Y))
			b := slices.Clone(a)
			for i := range b {
				// Noise that grows to the right, so that the blocks
				// score apart.
				if i%4 != 3 && rng.IntN(3) == 0 {
					d := 1 + 40*(i/4%sz.X)/sz.X
					b[i] = byte(int(b[i]) + rng.IntN(2*d+1) - d)
				}
			}
			ia := &image.RGBA{Pix: a, Stride: 4 * sz.X, Rect: image.Rect(0, 0, sz.X, sz.Y)}
			ib := &image.RGBA{Pix: b, Stride: 4 * sz.X, Rect: image.Rect(0, 0, sz.X, sz.Y)}
			want := refSSIM(ia, ib)
			if got := s.score(a, b, sz.X, sz.Y, sz.X); !near(got, want) {
				t.Errorf("%v, image %d: %.12f, want %.12f", sz, k, got, want)
			}
			if got := SSIM(a, b, sz.X, sz.Y); !near(got, want) {
				t.Errorf("%v, image %d: SSIM %.12f, want %.12f", sz, k, got, want)
			}
			for _, bw := range []int{3, 8, 13, 64} {
				want := refWorst(a, b, sz.X, sz.Y, bw)
				if got := s.score(a, b, sz.X, sz.Y, bw); !near(got, want) {
					t.Errorf("%v, image %d, blocks of %d: %.12f, want %.12f", sz, k, bw, got, want)
				}
				if got := s.score(a, a, sz.X, sz.Y, bw); !near(got, 1) {
					t.Errorf("%v, image %d, blocks of %d: %.12f against itself", sz, k, bw, got)
				}
			}
		}
	}
	for _, sz := range []image.Point{{7, 64}, {64, 7}, {1, 1}} {
		a := tileWith(sz.X, sz.Y, 0, 1)
		if got := s.score(a, tileWith(sz.X, sz.Y, 0, 2), sz.X, sz.Y, 64); got != 1 {
			t.Errorf("%v: %v, want 1 for an image smaller than the window", sz, got)
		}
	}
}

// FuzzSSIMScore checks the scorer against the summed-area SSIM on any
// pixels and blocks.
func FuzzSSIMScore(f *testing.F) {
	f.Add(uint8(8), uint8(8), uint8(8), []byte{1, 2, 3})
	f.Add(uint8(64), uint8(9), uint8(20), []byte{0xff, 0, 0x80, 7})
	f.Fuzz(func(t *testing.T, w, h, bw uint8, seed []byte) {
		W, H, BW := int(w%80)+8, int(h%80)+8, int(bw%90)+1
		a, b := make([]byte, 4*W*H), make([]byte, 4*W*H)
		for i := range a {
			if len(seed) > 0 {
				a[i] = seed[i%len(seed)] ^ byte(i*31)
				b[i] = seed[(i+1)%len(seed)] ^ byte(i*17)
			}
		}
		var s ssimer
		if got, want := s.score(a, b, W, H, BW), refWorst(a, b, W, H, BW); !near(got, want) {
			t.Errorf("%d×%d in blocks of %d: %.12f, want %.12f", W, H, BW, got, want)
		}
	})
}

// FuzzStill checks Still's search on any rectangle of the photograph,
// ladder and start: the quality is on the ladder, the encoding is
// Lossy's at it, the next search starts there, and it either reached the
// score there and not a step below, or did not reach it at hi; in about
// twice log2 of the steps past the walk.
func FuzzStill(f *testing.F) {
	photo := loadFrame(f, "photo")
	f.Add(uint16(0), uint16(128), uint8(255), uint8(63), uint8(30), uint8(90), uint8(80))
	f.Add(uint16(320), uint16(384), uint8(63), uint8(63), uint8(1), uint8(100), uint8(0))
	f.Add(uint16(1000), uint16(700), uint8(20), uint8(9), uint8(60), uint8(61), uint8(100))
	f.Fuzz(func(t *testing.T, x, y uint16, w, h, lo, hi, hint uint8) {
		W, H := int(w)%256+8, int(h)%64+8
		X, Y := int(x)%(photo.Rect.Dx()-W+1), int(y)%(photo.Rect.Dy()-H+1)
		pix := pixels(photo, image.Rect(X, Y, X+W, Y+H))
		Lo, Hi := int(lo)%100+1, int(hi)%100+1
		if Lo > Hi {
			Lo, Hi = Hi, Lo
		}
		e := New([]protocol.Codec{webpC, deflate})
		e.SetHint(int(hint) % 101)
		b, c, q, reached := e.Still(nil, pix, W, H, 64, Lo, Hi)
		if c != webpC || q < Lo || q > Hi || q != Hi && (q-Lo)%QualityStep != 0 {
			t.Fatalf("%v at %d, %d to %d", c, q, Lo, Hi)
		}
		if lb, _ := New([]protocol.Codec{webpC}).Lossy(nil, pix, W, H, q); !bytes.Equal(b, lb) {
			t.Errorf("%d to %d: the encoding is not Lossy's at %d", Lo, Hi, q)
		}
		if e.Hint() != q {
			t.Errorf("%d to %d: the next search starts at %d, not %d", Lo, Hi, e.Hint(), q)
		}
		steps := (Hi-Lo+QualityStep-1)/QualityStep + 1
		if e.Tries() > 1+walkSteps+2*bits.Len(uint(steps-1)) {
			t.Errorf("%d to %d from %d: %d tries on a ladder of %d", Lo, Hi, hint, e.Tries(), steps)
		}
		if got := scoreOf(t, c, b, pix, W, H, 64); reached != (got >= StillSSIM) || !reached && q != Hi {
			t.Errorf("%d to %d: reached %v at %d, with SSIM %.3f", Lo, Hi, reached, q, got)
		}
		if reached && q > Lo {
			below := q - QualityStep
			if q == Hi {
				below = Lo + (Hi-Lo-1)/QualityStep*QualityStep
			}
			lb, lc := New([]protocol.Codec{webpC}).Lossy(nil, pix, W, H, below)
			if s := scoreOf(t, lc, lb, pix, W, H, 64); s >= StillSSIM {
				t.Errorf("%d to %d: %d chosen, but %d scores %.3f", Lo, Hi, q, below, s)
			}
		}
	})
}

// scoreOf decodes an encoding as a client does and scores it against
// pix, tile by tile.
func scoreOf(t *testing.T, c protocol.Codec, b, pix []byte, w, h, tile int) float64 {
	t.Helper()
	out, err := decode(c, b, w, h)
	if err != nil {
		t.Fatal(err)
	}
	return refWorst(pix, out.Pix, w, h, tile)
}

// Still picks the lowest quality on its ladder whose SSIM, as a client
// decodes it, reaches StillSSIM, wherever its search starts, or the
// highest it may if none does.
func TestStill(t *testing.T) {
	photo := loadFrame(t, "photo")
	rects := []image.Rectangle{
		image.Rect(0, 0, 64, 64),
		image.Rect(320, 384, 384, 448),
		image.Rect(0, 128, 256, 192),    // a run of four
		image.Rect(960, 704, 1024, 768), // the corner
		image.Rect(0, 0, 61, 9),         // an edge tile
		image.Rect(0, 0, 64, 4),         // the bottom row of 900 lines, smaller than the window
	}
	for _, lossy := range []protocol.Codec{webpC, jpegC} {
		for _, r := range rects {
			pix := pixels(photo, r)
			w, h := r.Dx(), r.Dy()
			for _, hint := range []int{0, 1, 30, 50, 80, 90, 100} {
				for _, lim := range [][2]int{{30, 90}, {30, 60}, {85, 85}, {70, 100}} {
					lo, hi := lim[0], lim[1]
					e := New([]protocol.Codec{lossy, deflate})
					e.hint = hint
					b, c, q, reached := e.Still([]byte("prefix"), pix, w, h, 64, lo, hi)
					if c != lossy || !bytes.HasPrefix(b, []byte("prefix")) {
						t.Fatalf("%v at %v: codec %v", lossy, r, c)
					}
					onLadder := q == hi || (q-lo)%QualityStep == 0
					if q < lo || q > hi || !onLadder {
						t.Fatalf("%v at %v, %d to %d: quality %d is not on the ladder", lossy, r, lo, hi, q)
					}
					if lb, _ := New([]protocol.Codec{lossy}).Lossy(nil, pix, w, h, q); !bytes.Equal(b[len("prefix"):], lb) {
						t.Errorf("%v at %v, %d to %d from %d: the encoding is not Lossy's at %d", lossy, r, lo, hi, hint, q)
					}
					if w < 8 || h < 8 {
						// No score: at hi, and not reached, so that a
						// writer raises it as hi rises.
						if q != hi || reached {
							t.Errorf("%v at %v: a rectangle smaller than the window at %d, reached %v; want %d, not reached", lossy, r, q, reached, hi)
						}
						continue
					}
					if e.hint != q {
						t.Errorf("%v at %v, %d to %d from %d: the next search starts at %d, not %d", lossy, r, lo, hi, hint, e.hint, q)
					}
					// However far from where it starts, it walks a few
					// steps, gallops and halves the gap: about twice log2
					// of the steps beyond the walk.
					if steps := (hi-lo+QualityStep-1)/QualityStep + 1; e.tries > 1+walkSteps+2*bits.Len(uint(steps-1)) {
						t.Errorf("%v at %v, %d to %d from %d: %d tries on a ladder of %d", lossy, r, lo, hi, hint, e.tries, steps)
					}
					got := scoreOf(t, c, b[len("prefix"):], pix, w, h, 64)
					if !reached {
						if q != hi || got >= StillSSIM {
							t.Errorf("%v at %v, %d to %d from %d: not reached at %d, SSIM %.3f", lossy, r, lo, hi, hint, q, got)
						}
						continue
					}
					if got < StillSSIM {
						t.Errorf("%v at %v, %d to %d from %d: reached at %d with SSIM %.3f", lossy, r, lo, hi, hint, q, got)
					}
					if q > lo {
						below := q - QualityStep
						if q == hi {
							below = lo + (hi-lo-1)/QualityStep*QualityStep
						}
						lb, lc := New([]protocol.Codec{lossy}).Lossy(nil, pix, w, h, below)
						if s := scoreOf(t, lc, lb, pix, w, h, 64); s >= StillSSIM {
							t.Errorf("%v at %v, %d to %d from %d: %d chosen, but %d scores %.3f", lossy, r, lo, hi, hint, q, below, s)
						}
					}
				}
			}
		}
	}
}

// Still sends a viewer with no lossy codec its pixels lossless, at
// quality 100, and starts its next search where the last ended.
func TestStillFallbacks(t *testing.T) {
	pix := tileWith(64, 64, 0, 3)
	e := New([]protocol.Codec{deflate})
	b, c, q, reached := e.Still(nil, pix, 64, 64, 64, 30, 90)
	if out, err := decode(c, b, 64, 64); c != deflate || q != 100 || !reached || err != nil || !bytes.Equal(out.Pix, pix) {
		t.Errorf("deflate only: %v at %d, reached %v: %v", c, q, reached, err)
	}
	e = New([]protocol.Codec{webpC, deflate})
	_, _, q, _ = e.Still(nil, pix, 64, 64, 64, 30, 90)
	if e.hint != q {
		t.Errorf("the next search starts at %d, not %d", e.hint, q)
	}
	// A rectangle WebP cannot hold is lossless, as Lossy sends it.
	wide := make([]byte, 4*16384*8)
	for i := range wide {
		wide[i] = byte(i * 7)
	}
	e = New([]protocol.Codec{webpC, raw})
	if b, c, q, _ := e.Still(nil, wide, 16384, 8, 64, 30, 90); c != raw || q != 100 || len(b) != len(wide) {
		t.Errorf("WebP of 16384×8: %d bytes of %v at %d, want raw", len(b), c, q)
	}
	// And the same wherever its search starts, whether it has steps or
	// only its top.
	for _, lim := range [][2]int{{30, 90}, {90, 90}} {
		e = New([]protocol.Codec{deflate})
		e.SetHint(0)
		if b, c, q, reached := e.Still(nil, pix, 64, 64, 64, lim[0], lim[1]); c != deflate || q != 100 || !reached || len(b) == 0 {
			t.Errorf("deflate only, %d to %d from the foot: %v at %d, reached %v", lim[0], lim[1], c, q, reached)
		}
		e = New([]protocol.Codec{webpC, raw})
		e.SetHint(0)
		if b, c, q, _ := e.Still(nil, wide, 16384, 8, 64, lim[0], lim[1]); c != raw || q != 100 || len(b) != len(wide) {
			t.Errorf("WebP of 16384×8, %d to %d from the foot: %d bytes of %v at %d, want raw", lim[0], lim[1], len(b), c, q)
		}
	}
}

// Hint is where Still's next search starts: firstHint for a new
// encoder, what SetHint set, and then what the search chose; Tries
// counts what each search tried.
func TestHint(t *testing.T) {
	e := New([]protocol.Codec{webpC, deflate})
	if e.Hint() != firstHint || e.Tries() != 0 {
		t.Errorf("a new encoder: hint %d, %d tries; want %d, 0", e.Hint(), e.Tries(), firstHint)
	}
	if e.SetHint(42); e.Hint() != 42 {
		t.Errorf("SetHint(42): hint %d", e.Hint())
	}
	pix := pixels(loadFrame(t, "photo"), image.Rect(0, 128, 256, 192))
	e.SetHint(1)
	_, _, q, _ := e.Still(nil, pix, 256, 64, 64, 30, 90)
	first := e.Tries()
	if e.Hint() != q || first == 0 {
		t.Errorf("after a search for %d: hint %d, %d tries", q, e.Hint(), first)
	}
	// From where the last ended, the same pixels take a try, and one
	// more below unless at the foot of the ladder.
	if _, _, again, _ := e.Still(nil, pix, 256, 64, 64, 30, 90); again != q || e.Tries()-first > 2 {
		t.Errorf("again from %d: %d in %d tries", q, again, e.Tries()-first)
	}
	// From up to three steps below, on a ladder that goes on above, it
	// walks up a step at a time: a try a step, and the one that reaches
	// the score.
	if q-3*QualityStep < 30 || q+3*QualityStep > 100 {
		t.Fatalf("%d is too near the foot or the top to walk up to", q)
	}
	for d := 1; d <= 3; d++ {
		e.SetHint(q - d*QualityStep)
		before := e.Tries()
		if _, _, got, _ := e.Still(nil, pix, 256, 64, 64, 30, 100); got != q || e.Tries()-before != d+1 {
			t.Errorf("from %d steps below: %d in %d tries, want %d in %d", d, got, e.Tries()-before, q, d+1)
		}
	}
	// From ten steps below, at the foot of a ladder, it walks three,
	// goes two and four, and eight or to the top, which reaches the
	// score: seven tries short of it. It then halves the gap.
	lo, hi := q-10*QualityStep, min(q+7*QualityStep, 100)
	top := (hi - lo + QualityStep - 1) / QualityStep
	e.SetHint(lo)
	before := e.Tries()
	if _, _, got, _ := e.Still(nil, pix, 256, 64, 64, lo, hi); got != q || e.Tries()-before > 7+bits.Len(uint(top-9)) {
		t.Errorf("from %d, %d to %d: %d in %d tries, want %d in %d at most", lo, lo, hi, got, e.Tries()-before, q, 7+bits.Len(uint(top-9)))
	}
}

// On the corpus's photograph, whole rows of tiles at the quality Still
// picks reach StillSSIM, tile by tile, and come under G6's target of
// 139 KiB.
func TestStillCorpus(t *testing.T) {
	photo := loadFrame(t, "photo")
	e := New([]protocol.Codec{webpC, deflate})
	total := 0
	var qs []int
	for y := 0; y < photo.Rect.Dy(); y += 64 {
		r := image.Rect(0, y, photo.Rect.Dx(), y+64)
		b, c, q, reached := e.Still(nil, pixels(photo, r), r.Dx(), r.Dy(), 64, 30, 90)
		if c != webpC || !reached {
			t.Errorf("row at %d: %v at %d, reached %v", y, c, q, reached)
		}
		total += len(b)
		qs = append(qs, q)
	}
	if total > 139<<10 {
		t.Errorf("the photograph takes %.1f KiB, over G6's 139", float64(total)/1024)
	}
	slices.Sort(qs)
	t.Logf("the photograph at SSIM %.2f: %.1f KiB, rows at quality %d to %d, median %d",
		StillSSIM, float64(total)/1024, qs[0], qs[len(qs)-1], qs[len(qs)/2])
}

// BenchmarkStill times Still against Lossy at quality 90 on the corpus
// photograph's rows, the search a still photographic run pays, and
// reports its allocations. Run it with -cpu 1.
func BenchmarkStill(b *testing.B) {
	photo := loadFrame(b, "photo")
	var rows [][]byte
	for y := 0; y < photo.Rect.Dy(); y += 64 {
		rows = append(rows, pixels(photo, image.Rect(0, y, photo.Rect.Dx(), y+64)))
	}
	w := photo.Rect.Dx()
	b.Run("lossy90", func(b *testing.B) {
		e := New([]protocol.Codec{webpC, deflate})
		var dst []byte
		b.ReportAllocs()
		for i := 0; b.Loop(); i++ {
			dst, _ = e.Lossy(dst[:0], rows[i%len(rows)], w, 64, 90)
		}
	})
	b.Run("still", func(b *testing.B) {
		e := New([]protocol.Codec{webpC, deflate})
		var dst []byte
		b.ReportAllocs()
		for i := 0; b.Loop(); i++ {
			dst, _, _, _ = e.Still(dst[:0], rows[i%len(rows)], w, 64, 64, 30, 90)
		}
	})
}
