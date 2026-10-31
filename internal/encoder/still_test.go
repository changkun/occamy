// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package encoder

import (
	"bytes"
	"image"
	"math"
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
			for _, hint := range []int{1, 30, 50, 80, 90, 100} {
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
					if w < 8 || h < 8 {
						// No score: at hi, and not reached, so that a
						// writer raises it as hi rises.
						if q != hi || reached {
							t.Errorf("%v at %v: a rectangle smaller than the window at %d, reached %v; want %d, not reached", lossy, r, q, reached, hi)
						}
						continue
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
