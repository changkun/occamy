// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package codecs

import "image"

// SSIM is the structural similarity of b to a (Wang et al., 2004) on
// luma, BT.601 weights, over every 8×8 window, with the usual constants
// (K1 = 0.01, K2 = 0.03, L = 255). a and b must have the same size and
// be at least 8×8. 1 means identical.
func SSIM(a, b *image.RGBA) float64 {
	w, h := a.Rect.Dx(), a.Rect.Dy()
	ya, yb := luma(a), luma(b)
	// Summed-area tables of x, y, x², y² and xy, with a zero row and
	// column in front.
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
