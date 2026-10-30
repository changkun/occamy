// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package rdp

import (
	"errors"
	"fmt"
)

// The planar codec, the RDP 6.0 bitmap compression of [MS-RDPEGDI]
// 2.2.2.5.1 and 3.1.9, which compresses 32-bit bitmap updates.

// Planar format header bits, [MS-RDPEGDI] 2.2.2.5.1.
const (
	planarCLLMask = 0x07 // colour loss level
	planarCS      = 0x08 // chroma subsampling
	planarRLE     = 0x10
	planarNA      = 0x20 // no alpha plane
)

var errPlanarOverrun = errors.New("rdp: planar plane overruns its row")

// planarDecode decodes a planar bitmap of width×height pixels into dst,
// RGBA rows top-down. bottomUp tells whether the stream stores the rows
// bottom-up, as bitmap updates do.
func (d *decoder) planarDecode(src []byte, width, height int, dst []byte, bottomUp bool) error {
	if len(src) < 1 {
		return errShort
	}
	hdr := src[0]
	if hdr&planarCLLMask != 0 || hdr&planarCS != 0 {
		// Colour loss and chroma subsampling select YCoCg, which xrdp does
		// not send; it is left for the GFX planar codec.
		return fmt.Errorf("rdp: planar bitmap with colour loss level %d", hdr&planarCLLMask)
	}
	n := width * height
	if len(dst) < 4*n {
		return errors.New("rdp: planar destination too small")
	}
	nplanes := 4
	if hdr&planarNA != 0 {
		nplanes = 3
	}
	for i := range nplanes {
		if cap(d.planes[i]) < n {
			d.planes[i] = make([]byte, n)
		}
		d.planes[i] = d.planes[i][:n]
	}
	pos := 1
	for i := range nplanes {
		if hdr&planarRLE != 0 {
			k, err := planarRLEPlane(src[pos:], d.planes[i], width, height)
			if err != nil {
				return err
			}
			pos += k
		} else {
			if len(src)-pos < n {
				return errShort
			}
			copy(d.planes[i], src[pos:pos+n])
			pos += n
		}
	}
	// The alpha plane comes first when present; bitmap updates are opaque,
	// so it is decoded and dropped.
	o := nplanes - 3
	r, g, b := d.planes[o], d.planes[o+1], d.planes[o+2]
	for y := range height {
		sy := y
		if bottomUp {
			sy = height - 1 - y
		}
		row := dst[4*y*width : 4*(y+1)*width]
		s := sy * width
		for x := range width {
			row[4*x] = r[s+x]
			row[4*x+1] = g[s+x]
			row[4*x+2] = b[s+x]
			row[4*x+3] = 0xff
		}
	}
	return nil
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
