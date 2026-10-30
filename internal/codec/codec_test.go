// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package codec

import (
	"bytes"
	"compress/flate"
	"errors"
	"image"
	"math/rand/v2"
	"testing"
)

// tileWith returns w×h pixels of n distinct colours, each appearing at
// least once, or of random colours if n is zero. With alpha, the colours
// carry alpha values other than 255.
func tileWith(w, h, n int, alpha bool, seed uint64) []byte {
	rng := rand.New(rand.NewPCG(seed, 1))
	pix := make([]byte, 4*w*h)
	colours := make([][4]byte, n)
	for i := range colours {
		// Distinct by construction: i is in the first two bytes.
		colours[i] = [4]byte{byte(i), byte(i >> 8), byte(rng.IntN(256)), 0xff}
		if alpha {
			colours[i][3] = byte(rng.IntN(256))
		}
	}
	for i := 0; i < len(pix); i += 4 {
		switch {
		case n == 0:
			pix[i], pix[i+1], pix[i+2], pix[i+3] = byte(rng.IntN(256)), byte(rng.IntN(256)), byte(rng.IntN(256)), 0xff
			if alpha {
				pix[i+3] = byte(rng.IntN(256))
			}
		case i/4 < n:
			copy(pix[i:i+4], colours[i/4][:])
		default:
			copy(pix[i:i+4], colours[rng.IntN(n)][:])
		}
	}
	return pix
}

// rgba wraps pix as w×h pixels at the origin.
func rgba(pix []byte, w, h int) *image.RGBA {
	return &image.RGBA{Pix: pix, Stride: 4 * w, Rect: image.Rect(0, 0, w, h)}
}

// inflate returns the container inside a deflate payload.
func inflate(t *testing.T, payload []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	if _, err := out.ReadFrom(flate.NewReader(bytes.NewReader(payload))); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

// deflate compresses a container, as a hostile or careless encoder may.
func deflate(t testing.TB, container []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	fw, err := flate.NewWriter(&out, flate.BestSpeed)
	if err != nil {
		t.Fatal(err)
	}
	fw.Write(container)
	fw.Close()
	return out.Bytes()
}

func TestColours(t *testing.T) {
	var p Palette
	for _, c := range []struct {
		n     int
		alpha bool
		want  int
	}{
		{1, false, 1}, {2, false, 2}, {16, false, 16}, {17, false, 17},
		{256, false, 256}, {257, false, 257}, {0, false, 257},
		{16, true, 16}, {256, true, 256}, {257, true, 257},
	} {
		pix := tileWith(64, 64, c.n, c.alpha, uint64(c.n))
		if got := Colours(pix, 64, 64, &p); got != c.want {
			t.Errorf("%d colours, alpha %v: counted %d, want %d", c.n, c.alpha, got, c.want)
		}
	}
}

// Alpha makes colours distinct, and every 32-bit pixel is a colour,
// including white and transparent black.
func TestColoursAlpha(t *testing.T) {
	var p Palette
	pix := []byte{
		0xff, 0xff, 0xff, 0xff, 0, 0, 0, 0,
		0xff, 0xff, 0xff, 0xfe, 0xff, 0xff, 0xff, 0xff,
	}
	if !p.Build(pix, 2, 2) || len(p.Colours) != 3 || p.Opaque {
		t.Fatalf("colours %08x, opaque %v; want 3, not opaque", p.Colours, p.Opaque)
	}
	if want := []uint8{0, 1, 2, 0}; !bytes.Equal(p.Index, want) {
		t.Errorf("index %v, want %v", p.Index, want)
	}
	if !p.Build(pix[:4], 1, 1) || !p.Opaque || len(p.Colours) != 1 {
		t.Errorf("white alone: colours %08x, opaque %v", p.Colours, p.Opaque)
	}
}

// Every form round-trips exactly, at every index width, on full tiles and
// on the smaller ones at the screen's edges, and the encoder picks the
// form the pixels need.
func TestDeflateRoundTrip(t *testing.T) {
	var d Deflater
	var in Inflater
	var p Palette
	sizes := []image.Point{{64, 64}, {61, 7}, {1, 1}, {3, 64}, {64, 1}, {9, 3}}
	for _, n := range []int{1, 2, 3, 4, 5, 16, 17, 255, 256, 257, 0} {
		for _, alpha := range []bool{false, true} {
			for _, s := range sizes {
				if n > s.X*s.Y {
					continue
				}
				pix := tileWith(s.X, s.Y, n, alpha, uint64(n*s.X+s.Y))
				payload := d.Append(nil, pix, s.X, s.Y)
				out := rgba(make([]byte, len(pix)), s.X, s.Y)
				if err := in.Decode(out, payload); err != nil {
					t.Fatalf("%d colours, alpha %v, %v: %v", n, alpha, s, err)
				}
				if !bytes.Equal(out.Pix, pix) {
					t.Errorf("%d colours, alpha %v, %v: the pixels differ after a round trip", n, alpha, s)
				}
				var form byte
				if Colours(pix, s.X, s.Y, &p) > MaxColours {
					form |= formRaw
				}
				if alpha && !opaque(pix, s.X, s.Y) {
					form |= formAlpha
				}
				if got := inflate(t, payload)[0]; got != form {
					t.Errorf("%d colours, alpha %v, %v: form %d, want %d", n, alpha, s, got, form)
				}
			}
		}
	}
}

// The container's bytes, which other implementations must produce and
// read the same.
func TestContainerLayout(t *testing.T) {
	px := func(c ...uint32) []byte {
		b := make([]byte, 0, 4*len(c))
		for _, v := range c {
			b = append(b, byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
		}
		return b
	}
	const (
		red   = 0xff0000ff
		blue  = 0x0000ffff
		green = 0x00ff00ff
		glass = 0xffffff80
	)
	var p Palette
	for _, c := range []struct {
		name string
		pix  []byte
		w, h int
		want []byte
	}{
		{"solid", px(red, red, red), 3, 1, []byte{0, 0, 0xff, 0, 0}},
		{"two colours, a row padded", px(red, blue, blue, red, blue, red, red, blue, red), 9, 1,
			[]byte{0, 1, 0xff, 0, 0, 0, 0, 0xff, 0b0110_1001, 0b0000_0000}},
		{"two rows of three colours", px(red, blue, green, green, blue, red), 3, 2,
			[]byte{0, 2, 0xff, 0, 0, 0, 0, 0xff, 0, 0xff, 0, 0b00_01_10_00, 0b10_01_00_00}},
		{"alpha", px(glass, red), 2, 1,
			[]byte{2, 1, 0xff, 0xff, 0xff, 0x80, 0xff, 0, 0, 0xff, 0b0100_0000}},
	} {
		if !p.Build(c.pix, c.w, c.h) {
			t.Fatalf("%s: too many colours", c.name)
		}
		if got := AppendContainer(nil, c.pix, c.w, c.h, &p); !bytes.Equal(got, c.want) {
			t.Errorf("%s: container % x, want % x", c.name, got, c.want)
		}
	}
	if got, want := AppendContainer(nil, px(red, glass), 2, 1, nil), []byte{3, 0xff, 0, 0, 0xff, 0xff, 0xff, 0xff, 0x80}; !bytes.Equal(got, want) {
		t.Errorf("raw with alpha: container % x, want % x", got, want)
	}
	if got, want := AppendContainer(nil, px(red, blue), 2, 1, nil), []byte{1, 0xff, 0, 0, 0, 0, 0xff}; !bytes.Equal(got, want) {
		t.Errorf("raw: container % x, want % x", got, want)
	}
}

// Decode writes into a rectangle of a larger image, as a client draws a
// tile into its screen, and leaves the rest alone.
func TestDecodeIntoScreen(t *testing.T) {
	var d Deflater
	var in Inflater
	screen := image.NewRGBA(image.Rect(0, 0, 20, 10))
	for i := range screen.Pix {
		screen.Pix[i] = 0x55
	}
	want := bytes.Clone(screen.Pix)
	r := image.Rect(5, 3, 14, 7)
	for _, n := range []int{1, 2, 17, 0} {
		pix := tileWith(r.Dx(), r.Dy(), n, false, 9)
		dst := screen.SubImage(r).(*image.RGBA)
		if err := in.Decode(dst, d.Append(nil, pix, r.Dx(), r.Dy())); err != nil {
			t.Fatal(err)
		}
		for y := range r.Dy() {
			o := screen.PixOffset(r.Min.X, r.Min.Y+y)
			copy(want[o:o+4*r.Dx()], pix[4*r.Dx()*y:])
		}
		if !bytes.Equal(screen.Pix, want) {
			t.Fatalf("%d colours: the screen differs from the tile drawn at %v", n, r)
		}
		clear(dst.Pix[:4*r.Dx()])
		if err := DecodeRaw(dst, pix); err != nil || !bytes.Equal(screen.Pix, want) {
			t.Fatalf("%d colours, raw: %v, or the screen differs from the tile drawn at %v", n, err, r)
		}
	}
}

func TestDecodeRaw(t *testing.T) {
	pix := tileWith(3, 2, 0, true, 1)
	out := rgba(make([]byte, len(pix)), 3, 2)
	if err := DecodeRaw(out, AppendRaw(nil, pix, 3, 2)); err != nil || !bytes.Equal(out.Pix, pix) {
		t.Errorf("raw round trip: %v", err)
	}
	for _, p := range [][]byte{nil, pix[:len(pix)-1], append(bytes.Clone(pix), 0)} {
		if err := DecodeRaw(out, p); !errors.Is(err, ErrInvalid) {
			t.Errorf("%d bytes for 3×2 pixels: %v, want ErrInvalid", len(p), err)
		}
	}
	if err := DecodeRaw(rgba(nil, 0, 0), nil); !errors.Is(err, ErrInvalid) {
		t.Errorf("an empty rectangle: %v, want ErrInvalid", err)
	}
}
