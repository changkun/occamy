// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package codec

import (
	"encoding/binary"
	"slices"
)

// The scaled codec (codec 7) carries a tile's image scaled down by a
// factor f, ceil(w/f) by ceil(h/f) pixels, which the receiver scales up
// by f from the tile's top left corner. Downscale is what occamyd sends,
// and Upscale what a receiver draws.
//
// Both work on a pixel's four bytes as one little-endian word, red and
// blue in the low bytes of its halves (lanes & 0x00ff00ff) and green and
// alpha in the high (lanes >> 8 & 0x00ff00ff): a sum of up to 256 bytes
// fits a half.

// lanes masks the red and blue of a pixel's word, or its green and alpha
// shifted down by a byte.
const lanes = 0x00ff00ff

// Downscale puts pix, w by h pixels in RGBA order row by row, scaled down
// by f, from 2 to 16, into dst, ceil(w/f) by ceil(h/f) pixels, each the
// mean of the f by f pixels of pix it covers, fewer at the right and
// bottom edges, rounded, and opaque. sums holds a row's sums between
// calls. It returns dst and sums.
func Downscale(dst []byte, sums []uint32, pix []byte, w, h, f int) ([]byte, []uint32) {
	sw, sh := (w+f-1)/f, (h+f-1)/f
	dst = slices.Grow(dst[:0], 4*sw*sh)[:4*sw*sh]
	sums = slices.Grow(sums[:0], 2*sw)[:2*sw]
	for y := range sh {
		clear(sums)
		y0, y1 := y*f, min(y*f+f, h)
		for sy := y0; sy < y1; sy++ {
			row := pix[4*sy*w : 4*(sy+1)*w]
			for x := range sw {
				var rb, ga uint32
				for i := 4 * x * f; i < 4*min(x*f+f, w); i += 4 {
					p := binary.LittleEndian.Uint32(row[i:])
					rb += p & lanes
					ga += p >> 8 & lanes
				}
				sums[2*x] += rb
				sums[2*x+1] += ga
			}
		}
		out := dst[4*y*sw : 4*(y+1)*sw]
		for x := range sw {
			n := uint32((min(x*f+f, w) - x*f) * (y1 - y0)) // #nosec G115 -- at most f²
			rb, ga := sums[2*x], sums[2*x+1]
			var p uint32
			if n == 4 {
				p = (rb+0x00020002)>>2&lanes | ((ga+0x00020002)>>2&lanes)<<8
			} else {
				p = (rb&0xffff+n/2)/n | ((rb>>16+n/2)/n)<<16 | ((ga&0xffff+n/2)/n)<<8
			}
			binary.LittleEndian.PutUint32(out[4*x:], p|0xff000000)
		}
	}
	return dst, sums
}

// Upscale puts src, ceil(w/f) by ceil(h/f) pixels in RGBA order row by
// row, scaled up by f, from 2 to 8, into dst, w by h pixels: each pixel
// of dst interpolated bilinearly at its centre, with src's edge pixels
// extended beyond its centres, and rounded. It uses integers only, so
// that it draws the same pixels on every platform. col holds a row of
// src blended between two rows between calls; it returns col.
func Upscale(dst []byte, w, h int, src []byte, f int, col []uint32) []uint32 {
	sw, sh := (w+f-1)/f, (h+f-1)/f
	// A pixel's neighbours in src weigh in 2f-ths in each direction, so
	// that the four weigh (2f)² in all, at most 256: the rows are
	// blended first, then the columns of the blend.
	d := uint32(2 * f) // #nosec G115 -- f is at most 8
	dd := d * d
	col = slices.Grow(col[:0], 2*sw)[:2*sw]
	for y := range h {
		y0, y1, ky := between(y, f, sh)
		r0, r1 := src[4*y0*sw:4*(y0+1)*sw], src[4*y1*sw:4*(y1+1)*sw]
		for i := range sw {
			a := binary.LittleEndian.Uint32(r0[4*i:])
			c := binary.LittleEndian.Uint32(r1[4*i:])
			col[2*i] = (a&lanes)*(d-ky) + (c&lanes)*ky
			col[2*i+1] = (a>>8&lanes)*(d-ky) + (c>>8&lanes)*ky
		}
		out := dst[4*y*w : 4*(y+1)*w]
		// Pixel x's centre is at v/(2f) in src's pixels, where v starts
		// at 1-f and steps by 2: between pixels i and i+1, at k/(2f).
		v := 1 - f
		x := 0
		for ; x < w && v < 0; x, v = x+1, v+2 {
			put(out[4*x:], col[0]*d, col[1]*d, f, dd)
		}
		i, k := v/(2*f), uint32(v%(2*f)) // #nosec G115 -- below 2f
		for ; x < w; x++ {
			j := min(i+1, sw-1)
			rb := col[2*i]*(d-k) + col[2*j]*k
			ga := col[2*i+1]*(d-k) + col[2*j+1]*k
			put(out[4*x:], rb, ga, f, dd)
			if k += 2; k >= d {
				k -= d
				i++
			}
		}
	}
	return col
}

// put writes the pixel whose red and blue sum to rb and green and alpha
// to ga, in lanes, each dd times the pixel's, rounded.
func put(out []byte, rb, ga uint32, f int, dd uint32) {
	var p uint32
	if f == 2 {
		p = (rb+0x00080008)>>4&lanes | ((ga+0x00080008)>>4&lanes)<<8
	} else {
		p = (rb&0xffff+dd/2)/dd | ((rb>>16+dd/2)/dd)<<16 |
			((ga&0xffff+dd/2)/dd)<<8 | ((ga>>16+dd/2)/dd)<<24
	}
	binary.LittleEndian.PutUint32(out, p)
}

// between returns where the centre of pixel x of a column scaled up by
// f falls in the n pixels of the column: between pixels i and j, at
// weight k/(2f) of j's. Pixel x's centre is at (2x+1-f)/(2f) in pixels
// of the column, whose first and last pixels extend beyond their
// centres.
func between(x, f, n int) (i, j int, k uint32) {
	v := 2*x + 1 - f
	if v < 0 {
		return 0, 0, 0
	}
	i = v / (2 * f)
	return i, min(i+1, n-1), uint32(v % (2 * f)) // #nosec G115 -- below 2f
}
