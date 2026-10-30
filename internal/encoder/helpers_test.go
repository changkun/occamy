// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package encoder

import (
	"bytes"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	"image/png"
	"math/rand/v2"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/image/webp"

	"changkun.de/x/occamy/internal/codec"
	"changkun.de/x/occamy/protocol"
)

// corpusDir holds the frames the codec measurement used
// (docs/benchmarks/codecs.md).
var corpusDir = filepath.Join("..", "..", "bench", "testdata", "corpus")

// scenes are the corpus's scenes and their frames, in the measurement's
// order.
var scenes = []struct {
	name   string
	frames []string
}{
	{"desktop", []string{"desktop"}},
	{"terminal", []string{"terminal"}},
	{"terminal-aa", []string{"terminal-aa"}},
	{"browser", []string{"browser"}},
	{"photo", []string{"photo"}},
	{"drag", []string{"drag-0", "drag-1", "drag-2"}},
	{"scroll", []string{"scroll-0", "scroll-1", "scroll-2"}},
	{"motion", []string{"motion-0", "motion-1"}},
}

// loadFrame reads a corpus frame as RGBA.
func loadFrame(tb testing.TB, name string) *image.RGBA {
	tb.Helper()
	f, err := os.Open(filepath.Join(corpusDir, name+".png"))
	if err != nil {
		tb.Fatal(err)
	}
	defer f.Close()
	m, err := png.Decode(f)
	if err != nil {
		tb.Fatal(err)
	}
	img := image.NewRGBA(m.Bounds())
	draw.Draw(img, img.Rect, m, m.Bounds().Min, draw.Src)
	return img
}

// pixels copies r of img into w×h pixels row by row, as a writer reads a
// tile.
func pixels(img *image.RGBA, r image.Rectangle) []byte {
	out := make([]byte, 0, 4*r.Dx()*r.Dy())
	for y := r.Min.Y; y < r.Max.Y; y++ {
		out = append(out, img.Pix[img.PixOffset(r.Min.X, y):img.PixOffset(r.Max.X, y)]...)
	}
	return out
}

// tileWith returns w×h opaque pixels of n distinct colours, each at least
// once, or random ones if n is zero.
func tileWith(w, h, n int, seed uint64) []byte {
	rng := rand.New(rand.NewPCG(seed, 2))
	pix := make([]byte, 4*w*h)
	for i := 0; i < len(pix); i += 4 {
		k := i / 4
		if n > 0 && k >= n {
			k = rng.IntN(n)
		}
		if n == 0 {
			pix[i], pix[i+1], pix[i+2] = byte(rng.IntN(256)), byte(rng.IntN(256)), byte(rng.IntN(256))
		} else {
			// Distinct by construction: k is in the first two bytes.
			pix[i], pix[i+1], pix[i+2] = byte(k), byte(k>>8), byte(k*37)
		}
		pix[i+3] = 0xff
	}
	return pix
}

// decode decodes a payload of any codec the encoder sends into w×h
// pixels, as a client does.
func decode(c protocol.Codec, payload []byte, w, h int) (*image.RGBA, error) {
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	var m image.Image
	var err error
	switch c {
	case protocol.CodecRaw:
		return out, codec.DecodeRaw(out, payload)
	case protocol.CodecDeflate:
		var in codec.Inflater
		return out, in.Decode(out, payload)
	case protocol.CodecWebP:
		m, err = webp.Decode(bytes.NewReader(payload))
	case protocol.CodecJPEG:
		m, err = jpeg.Decode(bytes.NewReader(payload))
	default:
		return nil, fmt.Errorf("codec %v", c)
	}
	if err != nil {
		return nil, err
	}
	if m.Bounds().Dx() != w || m.Bounds().Dy() != h {
		return nil, fmt.Errorf("%v decodes to %v, want %d×%d", c, m.Bounds(), w, h)
	}
	if n, ok := m.(*image.NRGBA); ok {
		// Lossless WebP, a cursor: keep its straight alpha as it is.
		copy(out.Pix, n.Pix)
		return out, nil
	}
	draw.Draw(out, out.Rect, m, m.Bounds().Min, draw.Src)
	return out, nil
}

// SSIM is the structural similarity of b to a (Wang et al., 2004) on
// luma, BT.601 weights, over every 8×8 window, with the usual constants,
// as the codec measurement scores lossy codecs (bench/codecs/ssim.go). a
// and b must have the same size and be at least 8×8. 1 means identical.
func SSIM(a, b *image.RGBA) float64 {
	w, h := a.Rect.Dx(), a.Rect.Dy()
	ya, yb := luma(a), luma(b)
	sw := w + 1
	sa := make([]float64, sw*(h+1))
	sb := make([]float64, sw*(h+1))
	saa := make([]float64, sw*(h+1))
	sbb := make([]float64, sw*(h+1))
	sab := make([]float64, sw*(h+1))
	for y := range h {
		var ra, rb, raa, rbb, rab float64
		for x := range w {
			va, vb := ya[y*w+x], yb[y*w+x]
			ra += va
			rb += vb
			raa += va * va
			rbb += vb * vb
			rab += va * vb
			i := (y+1)*sw + x + 1
			sa[i] = sa[i-sw] + ra
			sb[i] = sb[i-sw] + rb
			saa[i] = saa[i-sw] + raa
			sbb[i] = sbb[i-sw] + rbb
			sab[i] = sab[i-sw] + rab
		}
	}
	const (
		n  = 8
		c1 = (0.01 * 255) * (0.01 * 255)
		c2 = (0.03 * 255) * (0.03 * 255)
	)
	box := func(s []float64, x, y int) float64 {
		return s[(y+n)*sw+x+n] - s[y*sw+x+n] - s[(y+n)*sw+x] + s[y*sw+x]
	}
	var sum float64
	count := 0
	for y := 0; y+n <= h; y++ {
		for x := 0; x+n <= w; x++ {
			ma, mb := box(sa, x, y)/(n*n), box(sb, x, y)/(n*n)
			va := box(saa, x, y)/(n*n) - ma*ma
			vb := box(sbb, x, y)/(n*n) - mb*mb
			cov := box(sab, x, y)/(n*n) - ma*mb
			sum += (2*ma*mb + c1) * (2*cov + c2) / ((ma*ma + mb*mb + c1) * (va + vb + c2))
			count++
		}
	}
	return sum / float64(count)
}

func luma(img *image.RGBA) []float64 {
	w, h := img.Rect.Dx(), img.Rect.Dy()
	out := make([]float64, 0, w*h)
	for y := img.Rect.Min.Y; y < img.Rect.Max.Y; y++ {
		row := img.Pix[img.PixOffset(img.Rect.Min.X, y):img.PixOffset(img.Rect.Max.X, y)]
		for i := 0; i < len(row); i += 4 {
			out = append(out, 0.299*float64(row[i])+0.587*float64(row[i+1])+0.114*float64(row[i+2]))
		}
	}
	return out
}
