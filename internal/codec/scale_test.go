// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package codec

import (
	"math"
	"slices"
	"testing"
)

// grey returns w by h opaque pixels whose red, green and blue are each v(x, y).
func grey(w, h int, v func(x, y int) byte) []byte {
	pix := make([]byte, 0, 4*w*h)
	for y := range h {
		for x := range w {
			c := v(x, y)
			pix = append(pix, c, c, c, 0xff)
		}
	}
	return pix
}

func TestDownscale(t *testing.T) {
	// 3 by 3 by 2: a full block, two half blocks and a corner.
	pix := grey(3, 3, func(x, y int) byte { return byte(10*x + 100*y) })
	for i := 3; i < len(pix); i += 8 {
		pix[i] = 0x80 // the image is opaque whatever the alpha
	}
	got, _ := Downscale(nil, nil, pix, 3, 3, 2)
	// (0+10+100+110)/4, (20+120)/2, (200+210)/2, 220.
	want := grey(2, 2, func(x, y int) byte { return [2][2]byte{{55, 70}, {205, 220}}[y][x] })
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	// A factor of 3 makes one pixel of the mean, rounded.
	got, _ = Downscale(got, nil, pix, 3, 3, 3)
	if want := grey(1, 1, func(int, int) byte { return 110 }); !slices.Equal(got, want) {
		t.Errorf("by 3: got %v, want %v", got, want)
	}
}

func TestUpscale(t *testing.T) {
	// Each pixel of dst is sampled at its centre: a quarter and three
	// quarters of the way between src's, and src's own at the edges.
	src := grey(2, 1, func(x, _ int) byte { return byte(200 * x) })
	dst := make([]byte, 4*4)
	Upscale(dst, 4, 1, src, 2, nil)
	if want := grey(4, 1, func(x, _ int) byte { return [4]byte{0, 50, 150, 200}[x] }); !slices.Equal(dst, want) {
		t.Errorf("got %v, want %v", dst, want)
	}
	// By 4, from the tile's top left: the last pixel of a 7 wide tile
	// falls at 1 and 5/8 of src's 2.
	dst = make([]byte, 4*7)
	Upscale(dst, 7, 1, src, 4, nil)
	if want := grey(7, 1, func(x, _ int) byte { return [7]byte{0, 0, 25, 75, 125, 175, 200}[x] }); !slices.Equal(dst, want) {
		t.Errorf("by 4: got %v, want %v", dst, want)
	}
}

// TestScaleRoundTrip scales a gradient down and up again at every
// factor and size up to 20 by 20: what comes back fills the tile and is
// within a little of the gradient.
func TestScaleRoundTrip(t *testing.T) {
	for f := 2; f <= 4; f++ {
		for w := 1; w <= 20; w++ {
			for h := 1; h <= 20; h++ {
				pix := grey(w, h, func(x, y int) byte { return byte(3*x + 5*y) })
				small, _ := Downscale(nil, nil, pix, w, h, f)
				if sw, sh := (w+f-1)/f, (h+f-1)/f; len(small) != 4*sw*sh {
					t.Fatalf("%dx%d by %d: %d bytes scaled down", w, h, f, len(small))
				}
				dst := make([]byte, 4*w*h)
				Upscale(dst, w, h, small, f, nil)
				var worst float64
				for i := range dst {
					worst = max(worst, math.Abs(float64(dst[i])-float64(pix[i])))
				}
				// A step of 5 a pixel, blurred over f pixels.
				if limit := float64(5 * f); worst > limit {
					t.Errorf("%dx%d by %d: off by %v, more than %v", w, h, f, worst, limit)
				}
			}
		}
	}
}

func BenchmarkDownscale(b *testing.B) {
	pix := grey(1024, 768, func(x, y int) byte { return byte(x ^ y) })
	var dst []byte
	var sums []uint32
	b.SetBytes(int64(len(pix)))
	for b.Loop() {
		dst, sums = Downscale(dst, sums, pix, 1024, 768, 2)
	}
}

func BenchmarkUpscale(b *testing.B) {
	src := grey(512, 384, func(x, y int) byte { return byte(x ^ y) })
	dst := make([]byte, 4*1024*768)
	var col []uint32
	b.SetBytes(int64(len(dst)))
	for b.Loop() {
		col = Upscale(dst, 1024, 768, src, 2, col)
	}
}
