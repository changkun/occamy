// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package rdp

import (
	"fmt"
	"image"
	"image/color"
)

// cursor is a decoded pointer shape.
type cursor struct {
	img *image.RGBA
	hot image.Point
}

// maxPointerCache bounds the cache indices the client keeps; the client
// asks for 25 in its pointer capability set.
const maxPointerCache = 32

// parsePointerAttribute decodes TS_POINTERATTRIBUTE ([MS-RDPBCGR]
// 2.2.9.1.1.4.5) when xorBpp is set, TS_COLORPOINTERATTRIBUTE (2.2.9.1.1.4.4,
// 24 bits per pixel) when xorBpp is 0, and TS_LARGEPOINTERATTRIBUTE
// (2.2.9.1.1.4.6) when large is set. It returns the cache index and the
// shape.
func (d *decoder) parsePointerAttribute(p []byte, withBpp, large bool) (int, cursor, error) {
	r := reader{b: p}
	bpp := 24
	if withBpp || large {
		bpp = int(r.u16())
	}
	index := int(r.u16())
	hot := image.Pt(int(r.u16()), int(r.u16()))
	w, h := int(r.u16()), int(r.u16())
	var andLen, xorLen int
	if large {
		andLen, xorLen = int(r.u32()), int(r.u32())
	} else {
		andLen, xorLen = int(r.u16()), int(r.u16())
	}
	xorMask := r.take(xorLen)
	andMask := r.take(andLen)
	if r.err != nil {
		return 0, cursor{}, fmt.Errorf("rdp: pointer update: %w", r.err)
	}
	if index >= maxPointerCache {
		return 0, cursor{}, fmt.Errorf("rdp: pointer cache index %d", index)
	}
	img, err := d.pointerImage(w, h, bpp, xorMask, andMask)
	if err != nil {
		return 0, cursor{}, err
	}
	return index, cursor{img: img, hot: hot}, nil
}

// pointerImage combines a pointer's XOR and AND masks, both stored
// bottom-up with rows padded to two bytes, into an RGBA image.
func (d *decoder) pointerImage(w, h, bpp int, xorMask, andMask []byte) (*image.RGBA, error) {
	if w <= 0 || h <= 0 || w > 384 || h > 384 {
		return nil, fmt.Errorf("rdp: pointer of %d×%d pixels", w, h)
	}
	switch bpp {
	case 1, 8, 16, 24, 32:
	default:
		return nil, fmt.Errorf("rdp: pointer of %d bits per pixel", bpp)
	}
	xorStride := (w*bpp + 15) / 16 * 2
	andStride := (w + 15) / 16 * 2
	if len(xorMask) < xorStride*h {
		return nil, fmt.Errorf("rdp: pointer XOR mask of %d bytes for %d×%d at %d bits", len(xorMask), w, h, bpp)
	}
	hasAnd := len(andMask) >= andStride*h
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		xrow := xorMask[(h-1-y)*xorStride:]
		var arow []byte
		if hasAnd {
			arow = andMask[(h-1-y)*andStride:]
		}
		for x := range w {
			var c color.RGBA
			switch bpp {
			case 1:
				if xrow[x/8]&(0x80>>(x%8)) != 0 {
					c = color.RGBA{0xff, 0xff, 0xff, 0}
				}
			case 8:
				p := d.palette[xrow[x]]
				c = color.RGBA{p[0], p[1], p[2], 0}
			case 16:
				v := uint32(xrow[2*x]) | uint32(xrow[2*x+1])<<8
				c = color.RGBA{expand5(v >> 11), expand6(v >> 5), expand5(v), 0}
			case 24:
				c = color.RGBA{xrow[3*x+2], xrow[3*x+1], xrow[3*x], 0}
			case 32:
				c = color.RGBA{xrow[4*x+2], xrow[4*x+1], xrow[4*x], xrow[4*x+3]}
			}
			transparent := arow != nil && arow[x/8]&(0x80>>(x%8)) != 0
			switch {
			case bpp == 32 && c.A != 0:
				// Alpha cursors carry their own transparency.
			case !transparent:
				c.A = 0xff
			case c.R|c.G|c.B != 0:
				// Screen inversion has no RGBA equivalent; show black.
				c = color.RGBA{0, 0, 0, 0xff}
			default:
				c = color.RGBA{}
			}
			img.SetRGBA(x, y, premultiply(c))
		}
	}
	return img, nil
}

// premultiply turns straight alpha into the premultiplied alpha of
// image.RGBA.
func premultiply(c color.RGBA) color.RGBA {
	if c.A == 0xff {
		return c
	}
	m := func(v byte) byte { return byte(uint16(v) * uint16(c.A) / 0xff) } // #nosec G115 -- at most 0xff
	return color.RGBA{m(c.R), m(c.G), m(c.B), c.A}
}

// defaultCursor is a plain arrow for the server's default system pointer,
// whose shape the server does not send.
var defaultCursor = func() cursor {
	rows := []string{
		"X",
		"XX",
		"X.X",
		"X..X",
		"X...X",
		"X....X",
		"X.....X",
		"X......X",
		"X.......X",
		"X........X",
		"X.....XXXXX",
		"X..X..X",
		"X.X X..X",
		"XX  X..X",
		"X    X..X",
		"     X..X",
		"      XX",
	}
	img := image.NewRGBA(image.Rect(0, 0, 11, len(rows)))
	for y, row := range rows {
		for x, ch := range row {
			switch ch {
			case 'X':
				img.SetRGBA(x, y, color.RGBA{0, 0, 0, 0xff})
			case '.':
				img.SetRGBA(x, y, color.RGBA{0xff, 0xff, 0xff, 0xff})
			}
		}
	}
	return cursor{img: img}
}()
