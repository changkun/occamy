// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package encoder

import "slices"

// ssimWindow is the side of SSIM's square window.
const ssimWindow = 8

// ssimer scores how much pixels look like others: the structural
// similarity (Wang et al., 2004) on luma, BT.601 weights, over every 8×8
// window, with the usual constants (K1 = 0.01, K2 = 0.03, L = 255), as
// the codec measurement scored lossy codecs (bench/codecs/ssim.go). It
// slides the window down the rows with sums per column, so it keeps five
// rows of sums rather than five tables of the image, and allocates
// nothing once warm.
type ssimer struct {
	sa, sb, saa, sbb, sab []float64 // per column, over the window's rows
	sum                   []float64 // per block, of its windows' scores
	count                 []int     // per block, its windows
}

// SSIM returns the structural similarity of b to a, both w by h pixels
// in RGBA order row by row: 1 means identical, and an image smaller than
// 8×8 scores 1. Still scores each tile of a rectangle so.
func SSIM(a, b []byte, w, h int) float64 {
	var s ssimer
	return s.score(a, b, w, h, w)
}

// lumaAt is the BT.601 luma of the pixel at p[i:i+4], in RGBA order.
func lumaAt(p []byte, i int) float64 {
	return 0.299*float64(p[i]) + 0.587*float64(p[i+1]) + 0.114*float64(p[i+2])
}

// score returns the SSIM of b to a, both w by h pixels in RGBA order row
// by row, of the worst of the blocks bw pixels wide side by side that
// make them up, the last maybe narrower: each block scores the windows
// that lie within it, as if it were an image of its own. 1 means
// identical. A block narrower or lower than the window has no window to
// score, and scores 1.
func (s *ssimer) score(a, b []byte, w, h, bw int) float64 {
	if w < ssimWindow || h < ssimWindow {
		return 1
	}
	bw = min(max(bw, 1), w)
	for _, p := range []*[]float64{&s.sa, &s.sb, &s.saa, &s.sbb, &s.sab} {
		*p = slices.Grow((*p)[:0], w)[:w]
		clear(*p)
	}
	blocks := (w + bw - 1) / bw
	s.sum = slices.Grow(s.sum[:0], blocks)[:blocks]
	s.count = slices.Grow(s.count[:0], blocks)[:blocks]
	clear(s.sum)
	clear(s.count)
	// add adds row y's luma to the column sums, with sign k.
	add := func(y int, k float64) {
		o := 4 * w * y
		for x := range w {
			va, vb := lumaAt(a, o+4*x), lumaAt(b, o+4*x)
			s.sa[x] += k * va
			s.sb[x] += k * vb
			s.saa[x] += k * va * va
			s.sbb[x] += k * vb * vb
			s.sab[x] += k * va * vb
		}
	}
	const (
		n  = ssimWindow * ssimWindow
		c1 = (0.01 * 255) * (0.01 * 255)
		c2 = (0.03 * 255) * (0.03 * 255)
	)
	for y := range ssimWindow - 1 {
		add(y, 1)
	}
	for y := 0; y+ssimWindow <= h; y++ {
		add(y+ssimWindow-1, 1)
		var ra, rb, raa, rbb, rab float64
		for x := range w {
			ra += s.sa[x]
			rb += s.sb[x]
			raa += s.saa[x]
			rbb += s.sbb[x]
			rab += s.sab[x]
			if x >= ssimWindow {
				ra -= s.sa[x-ssimWindow]
				rb -= s.sb[x-ssimWindow]
				raa -= s.saa[x-ssimWindow]
				rbb -= s.sbb[x-ssimWindow]
				rab -= s.sab[x-ssimWindow]
			}
			if x < ssimWindow-1 {
				continue
			}
			// The window ends at column x: it is the block's if it starts
			// in it too.
			k := x / bw
			if (x-ssimWindow+1)/bw != k {
				continue
			}
			ma, mb := ra/n, rb/n
			va := raa/n - ma*ma
			vb := rbb/n - mb*mb
			cov := rab/n - ma*mb
			s.sum[k] += (2*ma*mb + c1) * (2*cov + c2) / ((ma*ma + mb*mb + c1) * (va + vb + c2))
			s.count[k]++
		}
		add(y, -1)
	}
	worst := 1.0
	for k, c := range s.count {
		if c > 0 {
			worst = min(worst, s.sum[k]/float64(c))
		}
	}
	return worst
}
