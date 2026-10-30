// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package codecs

import (
	"bytes"
	"image"
	"image/draw"
	"math/rand/v2"
	"testing"
)

// tileWith returns a side×side tile of n colours in random positions,
// or of random pixels if n is zero.
func tileWith(side, n int, seed uint64) *image.RGBA {
	rng := rand.New(rand.NewPCG(seed, 1))
	t := image.NewRGBA(image.Rect(0, 0, side, side))
	colours := make([][3]byte, n)
	for i := range colours {
		// Distinct by construction.
		colours[i] = [3]byte{byte(i), byte(i * 7), byte(rng.IntN(256))}
	}
	for i := 0; i < len(t.Pix); i += 4 {
		switch {
		case n == 0:
			t.Pix[i], t.Pix[i+1], t.Pix[i+2] = byte(rng.IntN(256)), byte(rng.IntN(256)), byte(rng.IntN(256))
		case i/4 < n:
			c := colours[i/4] // every colour appears at least once
			t.Pix[i], t.Pix[i+1], t.Pix[i+2] = c[0], c[1], c[2]
		default:
			c := colours[rng.IntN(n)]
			t.Pix[i], t.Pix[i+1], t.Pix[i+2] = c[0], c[1], c[2]
		}
		t.Pix[i+3] = 0xff
	}
	return t
}

func TestColoursAndClass(t *testing.T) {
	var p Palette
	for _, tc := range []struct {
		n     int
		want  int
		class string
	}{
		{1, 1, "1"}, {2, 2, "2"}, {16, 16, "3-16"}, {17, 17, "17-256"},
		{256, 256, "17-256"}, {0, 257, ">256"},
	} {
		got := Colours(tileWith(64, tc.n, 1), &p)
		if got != tc.want || Class(got) != tc.class {
			t.Errorf("%d colours: counted %d, class %s; want %d, %s", tc.n, got, Class(got), tc.want, tc.class)
		}
	}
}

func TestContainerRoundTrip(t *testing.T) {
	var p Palette
	for _, n := range []int{1, 2, 3, 4, 5, 16, 17, 256, 0} {
		for _, side := range []int{64, 61} {
			tile := tileWith(side, n, uint64(n))
			paletted := p.Build(tile)
			b := AppendContainer(nil, tile, &p, paletted)
			out := image.NewRGBA(tile.Rect)
			if err := DecodeContainer(out, b); err != nil {
				t.Fatalf("%d colours, side %d: %v", n, side, err)
			}
			if !bytes.Equal(out.Pix, tile.Pix) {
				t.Errorf("%d colours, side %d: the tile differs after a round trip", n, side)
			}
		}
	}
}

func TestQOIRoundTrip(t *testing.T) {
	for _, n := range []int{1, 2, 40, 0} {
		tile := tileWith(64, n, 7)
		// A gradient and a run exercise the difference and run chunks.
		for x := range 64 {
			tile.SetRGBA(x, 3, tile.RGBAAt(0, 3))
			c := tile.RGBAAt(x, 5)
			c.R, c.G, c.B = byte(x), byte(x+1), byte(x*2)
			tile.SetRGBA(x, 5, c)
		}
		out := image.NewRGBA(tile.Rect)
		if err := DecodeQOI(out, AppendQOI(nil, tile)); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(out.Pix, tile.Pix) {
			t.Errorf("%d colours: the tile differs after a round trip", n)
		}
	}
}

func TestSSIM(t *testing.T) {
	a := tileWith(64, 0, 3)
	if s := SSIM(a, a); s < 0.9999 {
		t.Errorf("SSIM of a tile with itself is %v", s)
	}
	b := image.NewRGBA(a.Rect)
	draw.Draw(b, b.Rect, image.NewUniform(a.RGBAAt(0, 0)), image.Point{}, draw.Src)
	if s := SSIM(a, b); s > 0.1 {
		t.Errorf("SSIM of noise against a flat tile is %v", s)
	}
}

func TestSwizzle(t *testing.T) {
	src := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	dst := make([]byte, len(src))
	Swizzle(dst, src)
	if want := []byte{3, 2, 1, 4, 7, 6, 5, 8}; !bytes.Equal(dst, want) {
		t.Errorf("Swizzle = %v, want %v", dst, want)
	}
}

func TestLosslessCandidatesRoundTrip(t *testing.T) {
	var p Palette
	tiles := []*image.RGBA{tileWith(64, 2, 1), tileWith(64, 100, 2), tileWith(64, 0, 3)}
	for _, c := range Candidates(64) {
		if !c.Lossless() {
			continue
		}
		for i, tile := range tiles {
			pal := &p
			if !p.Build(tile) {
				pal = nil
			}
			b, err := c.Encode(tile, pal)
			if err != nil {
				t.Fatalf("%s: %v", c.Name, err)
			}
			got, err := c.Decode(b)
			if err != nil {
				t.Fatalf("%s: %v", c.Name, err)
			}
			if !bytes.Equal(got.Pix, tile.Pix) {
				t.Errorf("%s: tile %d differs after a round trip", c.Name, i)
			}
		}
	}
}
