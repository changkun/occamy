// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package rdp

import (
	"errors"
	"fmt"
)

// The planar codec, the RDP 6.0 bitmap compression of [MS-RDPEGDI]
// 2.2.2.5.1 and 3.1.9, which compresses 32-bit bitmap updates and is one
// of the graphics pipeline's codecs.

// Planar format header bits, [MS-RDPEGDI] 2.2.2.5.1.
const (
	planarCLLMask = 0x07 // colour loss level
	planarCS      = 0x08 // chroma subsampling
	planarRLE     = 0x10
	planarNA      = 0x20 // no alpha plane
)

// planarStream is where a planar bitmap comes from, which decides how
// its rows go and what it may hold.
type planarStream int

const (
	// planarBitmap is a bitmap update's: its rows are bottom-up, and it
	// is in ARGB, since the client advertises neither colour loss nor
	// chroma subsampling ([MS-RDPBCGR] 2.2.7.1.2).
	planarBitmap planarStream = iota
	// planarGFX is the graphics pipeline's: its rows are top-down, and
	// it may be in AYCoCg, with colour loss and chroma subsampling.
	planarGFX
)

var errPlanarOverrun = errors.New("rdp: planar plane overruns its row")

// planarDecode decodes a planar bitmap of width×height pixels from the
// stream kind into dst, opaque RGBA rows top-down, stride bytes apart.
// The alpha plane, if any, is decoded and dropped: bitmap updates and
// the graphics pipeline's opaque surfaces take none.
func (d *decoder) planarDecode(src []byte, width, height int, dst []byte, stride int, kind planarStream) error {
	if len(src) < 1 {
		return errShort
	}
	hdr := src[0]
	cll := int(hdr & planarCLLMask)
	cs := hdr&planarCS != 0
	switch {
	case kind == planarBitmap && cll != 0:
		return fmt.Errorf("rdp: planar bitmap update with colour loss level %d", cll)
	case cs && cll == 0:
		// [MS-RDPEGDI] 2.2.2.5.1: chroma subsampling is of AYCoCg alone.
		return errors.New("rdp: planar bitmap with chroma subsampling and no colour loss")
	}
	if width <= 0 || height <= 0 || len(dst) < stride*(height-1)+4*width {
		return errors.New("rdp: planar destination too small")
	}
	n := width * height
	// The chroma planes of a subsampled bitmap have half its pixels each
	// way, rounded up ([MS-RDPEGDI] 3.1.9.1.3).
	cw, ch := width, height
	if cs {
		cw, ch = (width+1)/2, (height+1)/2
	}
	nplanes := 4
	if hdr&planarNA != 0 {
		nplanes = 3
	}
	o := nplanes - 3 // the colour planes' first: the alpha plane comes first
	pos := 1
	for i := range nplanes {
		pw, ph := width, height
		if i > o && cs {
			pw, ph = cw, ch
		}
		size := pw * ph
		if cap(d.planes[i]) < size {
			d.planes[i] = make([]byte, max(size, n))
		}
		plane := d.planes[i][:size]
		if hdr&planarRLE != 0 {
			k, err := planarRLEPlane(src[pos:], plane, pw, ph)
			if err != nil {
				return err
			}
			pos += k
		} else {
			if len(src)-pos < size {
				return errShort
			}
			copy(plane, src[pos:pos+size])
			pos += size
		}
	}
	p0, p1, p2 := d.planes[o], d.planes[o+1], d.planes[o+2]
	for y := range height {
		sy := y
		if kind == planarBitmap {
			sy = height - 1 - y
		}
		row := dst[y*stride : y*stride+4*width]
		s := sy * width
		if cll == 0 {
			r, g, b := p0[s:s+width], p1[s:s+width], p2[s:s+width]
			for x := range width {
				row[4*x] = r[x]
				row[4*x+1] = g[x]
				row[4*x+2] = b[x]
				row[4*x+3] = 0xff
			}
			continue
		}
		ycocg(row, p0[s:s+width], p1, p2, sy, cw, cll, cs)
	}
	return nil
}

// ycocg converts the row sy of AYCoCg planes ([MS-RDPEGDI] 3.1.9.1.2) to
// opaque RGBA in row: lum is the row's luma, and co and cg the chroma
// planes, cw wide, subsampled if cs. The chroma lost cll bits; shifted
// back, they are 9-bit signed values, of which the transform takes half.
func ycocg(row, lum, co, cg []byte, sy, cw, cll int, cs bool) {
	shift := cll - 1
	c := sy * cw
	if cs {
		c = sy / 2 * cw
	}
	for x := range lum {
		cx := c + x
		if cs {
			cx = c + x/2
		}
		y := int(lum[x])
		o := int(int8(co[cx] << shift)) // #nosec G115 -- the 9-bit chroma's half
		g := int(int8(cg[cx] << shift)) // #nosec G115 -- likewise
		t := y - g
		row[4*x] = clamp8(t + o)
		row[4*x+1] = clamp8(y + g)
		row[4*x+2] = clamp8(t - o)
		row[4*x+3] = 0xff
	}
}

// clamp8 clamps v to a byte.
func clamp8(v int) byte {
	return byte(min(max(v, 0), 0xff)) // #nosec G115 -- clamped
}

// planarRLEPlane decodes one RLE plane, [MS-RDPEGDI] 2.2.2.5.1.1, into
// dst, width×height bytes in the order the stream stores the rows, and
// returns the bytes it consumed. The first row holds values, every other
// row differences from the row before it.
func planarRLEPlane(src, dst []byte, width, height int) (int, error) {
	si := 0
	for y := range height {
		row := dst[y*width : (y+1)*width]
		var prev []byte
		if y > 0 {
			prev = dst[(y-1)*width : y*width]
		}
		var v byte // the last value, or the last difference
		for x := 0; x < width; {
			if si >= len(src) {
				return 0, errShort
			}
			ctl := src[si]
			si++
			run, raw := int(ctl&0x0f), int(ctl>>4)
			switch run {
			case 1:
				run, raw = raw+16, 0
			case 2:
				run, raw = raw+32, 0
			}
			if x+raw+run > width {
				return 0, errPlanarOverrun
			}
			if len(src)-si < raw {
				return 0, errShort
			}
			if prev == nil {
				for ; raw > 0; raw-- {
					v = src[si]
					si++
					row[x] = v
					x++
				}
				for ; run > 0; run-- {
					row[x] = v
					x++
				}
				continue
			}
			for ; raw > 0; raw-- {
				// Differences are stored as magnitude and sign bit.
				if c := src[si]; c&1 != 0 {
					v = -(c>>1 + 1)
				} else {
					v = c >> 1
				}
				si++
				row[x] = prev[x] + v
				x++
			}
			for ; run > 0; run-- {
				row[x] = prev[x] + v
				x++
			}
		}
	}
	return si, nil
}
