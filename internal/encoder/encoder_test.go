// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package encoder

import (
	"bytes"
	"image"
	"slices"
	"testing"

	"changkun.de/x/occamy/internal/codec"
	"changkun.de/x/occamy/protocol"
)

const (
	raw     = protocol.CodecRaw
	deflate = protocol.CodecDeflate
	pngC    = protocol.CodecPNG
	jpegC   = protocol.CodecJPEG
	webpC   = protocol.CodecWebP
	cached  = protocol.CodecCached
)

func TestClassify(t *testing.T) {
	var p codec.Palette
	for _, c := range []struct {
		n    int
		want Class
	}{
		{1, Few}, {2, Few}, {16, Few}, {17, Many}, {256, Many}, {257, Photo}, {0, Photo},
	} {
		got := Classify(codec.Colours(tileWith(64, 64, c.n, 1), 64, 64, &p))
		if got != c.want {
			t.Errorf("%d colours: class %v, want %v", c.n, got, c.want)
		}
	}
}

// Each viewer gets the best codec it agreed to for each kind of tile,
// whatever its order of preference.
func TestAgreement(t *testing.T) {
	for _, c := range []struct {
		name            string
		codecs          []protocol.Codec
		lossless, lossy protocol.Codec
		cursor          protocol.Codec
	}{
		{"web client", []protocol.Codec{webpC, deflate, jpegC, raw, cached}, deflate, webpC, deflate},
		{"JPEG before WebP", []protocol.Codec{jpegC, webpC, raw}, raw, webpC, raw},
		{"JPEG only lossy", []protocol.Codec{deflate, jpegC, pngC, raw}, deflate, jpegC, deflate},
		{"raw only", []protocol.Codec{raw}, raw, 0, raw},
		{"deflate only", []protocol.Codec{deflate}, deflate, 0, deflate},
		{"WebP only", []protocol.Codec{webpC}, 0, webpC, webpC},
		{"JPEG only", []protocol.Codec{jpegC}, 0, jpegC, 0},
	} {
		e := New(c.codecs)
		if e == nil {
			t.Fatalf("%s: no encoder", c.name)
		}
		if e.lossless != c.lossless || e.lossy != c.lossy {
			t.Errorf("%s: lossless %v and lossy %v, want %v and %v", c.name, e.lossless, e.lossy, c.lossless, c.lossy)
		}
		cur := tileWith(4, 4, 3, 1)
		cur[3] = 0x80
		_, got, ok := e.Cursor(nil, cur, 4, 4)
		if got != c.cursor || ok != (c.cursor != 0) {
			t.Errorf("%s: cursor in %v (%v), want %v", c.name, got, ok, c.cursor)
		}
		for _, codec := range []protocol.Codec{c.lossless, c.lossy, c.cursor} {
			if codec != 0 && !slices.Contains(c.codecs, codec) {
				t.Errorf("%s: sends %v, which it did not agree to", c.name, codec)
			}
		}
	}
	for _, codecs := range [][]protocol.Codec{nil, {pngC}, {cached}, {pngC, cached}} {
		if e := New(codecs); e != nil {
			t.Errorf("an encoder for %v, want none", codecs)
		}
	}
	if !slices.Equal(Codecs(), []protocol.Codec{webpC, jpegC, deflate, raw}) {
		t.Errorf("Codecs() = %v", Codecs())
	}
}

// Tiles of up to 256 colours round-trip exactly, at every classification
// edge and in tiles smaller than 64 pixels; tiles of more go lossy for a
// viewer that decodes a lossy codec, and round-trip exactly otherwise.
func TestTile(t *testing.T) {
	sizes := []image.Point{{64, 64}, {61, 7}, {1, 1}, {64, 17}}
	for _, codecs := range [][]protocol.Codec{{webpC, deflate, raw}, {jpegC, deflate}, {deflate}, {raw}} {
		e := New(codecs)
		hasLossy := slices.Contains(codecs, webpC) || slices.Contains(codecs, jpegC)
		lossless := raw
		if slices.Contains(codecs, deflate) {
			lossless = deflate
		}
		for _, n := range []int{1, 2, 16, 17, 256, 257, 0} {
			for _, s := range sizes {
				if n > s.X*s.Y {
					continue
				}
				pix := tileWith(s.X, s.Y, n, uint64(n))
				var p codec.Palette
				photo := Classify(codec.Colours(pix, s.X, s.Y, &p)) == Photo
				b, c, lossy := e.Tile([]byte("prefix"), pix, s.X, s.Y)
				if lossy != (photo && hasLossy) {
					t.Errorf("%v, %d colours, %v: lossy %v", codecs, n, s, lossy)
				}
				if !bytes.HasPrefix(b, []byte("prefix")) {
					t.Fatalf("%v, %d colours, %v: dst not kept", codecs, n, s)
				}
				if lossy {
					if len(b) != len("prefix") || c != 0 {
						t.Errorf("%v, %d colours, %v: lossy tile encoded as %v", codecs, n, s, c)
					}
					continue
				}
				if c != lossless {
					t.Errorf("%v, %d colours, %v: codec %v", codecs, n, s, c)
				}
				out, err := decode(c, b[len("prefix"):], s.X, s.Y)
				if err != nil {
					t.Fatalf("%v, %d colours, %v: %v", codecs, n, s, err)
				}
				if !bytes.Equal(out.Pix, pix) {
					t.Errorf("%v, %d colours, %v: the tile differs after a round trip", codecs, n, s)
				}
			}
		}
	}
}

// A solid tile costs a few bytes.
func TestSolidTile(t *testing.T) {
	e := New([]protocol.Codec{deflate})
	b, c, _ := e.Tile(nil, tileWith(64, 64, 1, 1), 64, 64)
	if c != deflate || len(b) > 16 {
		t.Errorf("a solid tile: %d bytes of %v, want at most 16 of deflate", len(b), c)
	}
}

// Lossy tiles, alone or merged, decode to their size above an SSIM floor,
// and the quality setting shows.
func TestLossy(t *testing.T) {
	photo := loadFrame(t, "photo")
	for _, lossy := range []protocol.Codec{webpC, jpegC} {
		e := New([]protocol.Codec{lossy, deflate})
		for _, r := range []image.Rectangle{
			image.Rect(0, 0, 64, 64),
			image.Rect(64, 128, 64*4, 192),  // three tiles merged
			image.Rect(960, 704, 1024, 768), // the corner
			image.Rect(0, 0, 1024, 64),      // a row of 16
			image.Rect(0, 0, 61, 7),         // an edge tile
		} {
			pix := pixels(photo, r)
			src := &image.RGBA{Pix: pix, Stride: 4 * r.Dx(), Rect: image.Rect(0, 0, r.Dx(), r.Dy())}
			var sizes []int
			var scores []float64
			for _, q := range []int{30, 90} {
				b, c := e.Lossy([]byte("prefix"), pix, r.Dx(), r.Dy(), q)
				if c != lossy || !bytes.HasPrefix(b, []byte("prefix")) {
					t.Fatalf("%v at %v: codec %v", lossy, r, c)
				}
				out, err := decode(c, b[len("prefix"):], r.Dx(), r.Dy())
				if err != nil {
					t.Fatalf("%v at %v: %v", lossy, r, err)
				}
				sizes = append(sizes, len(b))
				if r.Dy() >= 8 {
					scores = append(scores, SSIM(src, out))
				}
			}
			if sizes[0] >= sizes[1] {
				t.Errorf("%v at %v: %d bytes at q30, %d at q90", lossy, r, sizes[0], sizes[1])
			}
			// The photograph is grainy: the measurement scored its tiles
			// at 0.85 to 0.96 between q30 and q90.
			if len(scores) == 2 && (scores[0] < 0.75 || scores[1] < 0.9 || scores[1] <= scores[0]) {
				t.Errorf("%v at %v: SSIM %.3f at q30 and %.3f at q90", lossy, r, scores[0], scores[1])
			}
		}
	}
}

// A viewer with no lossy codec gets lossy rectangles lossless, and one
// that has only lossy codecs gets every tile lossy.
func TestLossyFallbacks(t *testing.T) {
	pix := tileWith(64, 64, 0, 3)
	e := New([]protocol.Codec{deflate})
	b, c := e.Lossy(nil, pix, 64, 64, 50)
	if out, err := decode(c, b, 64, 64); c != deflate || err != nil || !bytes.Equal(out.Pix, pix) {
		t.Errorf("deflate only: Lossy in %v: %v", c, err)
	}
	e = New([]protocol.Codec{jpegC})
	text := tileWith(64, 64, 2, 3)
	if b, c, lossy := e.Tile(nil, text, 64, 64); !lossy || c != 0 || len(b) != 0 {
		t.Errorf("JPEG only: a two-colour tile is not lossy: %v", c)
	}
	// Rectangles beyond what WebP holds fall back to the lossless codec.
	e = New([]protocol.Codec{webpC, raw})
	wide := make([]byte, 4*16384)
	if b, c := e.Lossy(nil, wide, 16384, 1, 50); c != raw || len(b) != len(wide) {
		t.Errorf("WebP of 16384×1: %d bytes of %v, want raw", len(b), c)
	}
	e = New([]protocol.Codec{webpC})
	if b, c := e.Lossy([]byte("x"), wide, 16384, 1, 50); c != 0 || string(b) != "x" {
		t.Errorf("WebP only, 16384×1: %d bytes of %v, want none", len(b), c)
	}
}

// A cursor's alpha survives every codec it may go in.
func TestCursorAlpha(t *testing.T) {
	cur := make([]byte, 4*32*32)
	for i := 0; i < len(cur); i += 4 {
		x, y := i/4%32, i/4/32
		switch {
		case x < 2 || y < 2:
			cur[i], cur[i+1], cur[i+2], cur[i+3] = 0, 0, 0, 0xff // the outline
		case x > y:
			cur[i], cur[i+1], cur[i+2], cur[i+3] = 0x40, 0x80, 0xc0, byte(x*8) // a shadow
		default:
			cur[i+3] = 0 // transparent, black
		}
	}
	for _, codecs := range [][]protocol.Codec{{deflate}, {raw}, {webpC, jpegC}} {
		e := New(codecs)
		b, c, ok := e.Cursor(nil, cur, 32, 32)
		if !ok {
			t.Fatalf("%v: no cursor", codecs)
		}
		out, err := decode(c, b, 32, 32)
		if err != nil {
			t.Fatalf("%v: %v", codecs, err)
		}
		if !bytes.Equal(out.Pix, cur) {
			t.Errorf("%v: the cursor differs after a round trip in %v", codecs, c)
		}
	}
}
