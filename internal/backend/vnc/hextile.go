// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package vnc

import (
	"encoding/binary"
	"fmt"
	"image"
)

// encHextile is the Hextile encoding (RFC 6143, section 7.7.4).
const encHextile int32 = 5

// hextileTile is the side of a Hextile tile.
const hextileTile = 16

// The bits of a Hextile tile's subencoding.
const (
	hextileRaw         = 1 << 0
	hextileBackground  = 1 << 1
	hextileForeground  = 1 << 2
	hextileAnySubrects = 1 << 3
	hextileColoured    = 1 << 4
)

// hextile draws a Hextile rectangle: tiles of 16×16 pixels, left to right
// and top to bottom, smaller at the right and bottom edges. A tile is raw
// pixels, or a background colour with subrectangles in the foreground
// colour or in colours of their own. The background and foreground carry
// over from tile to tile, raw tiles included, as in TigerVNC; they start
// black. Each row of tiles is decoded into scratch space and then drawn
// under one lock.
func (d *decoder) hextile(r image.Rectangle) error {
	if r.Empty() {
		return nil
	}
	if err := d.onScreen("hextile rectangle", r); err != nil {
		return err
	}
	w := r.Dx()
	stride := 4 * w
	bg, fg := uint32(0xff<<24), uint32(0xff<<24)
	for y := r.Min.Y; y < r.Max.Y; y += hextileTile {
		th := min(hextileTile, r.Max.Y-y)
		band := d.enc.bandBuffer(stride * th)
		for x := 0; x < w; x += hextileTile {
			tw := min(hextileTile, w-x)
			if err := d.hextileTile(band[4*x:], stride, tw, th, &bg, &fg); err != nil {
				return fmt.Errorf("hextile tile at (%d, %d): %w", r.Min.X+x, y, err)
			}
		}
		d.drawBand(image.Rect(r.Min.X, y, r.Max.X, y+th), band)
	}
	return nil
}

// hextileTile decodes one tile of tw×th pixels into pix, whose rows are
// stride bytes apart.
func (d *decoder) hextileTile(pix []byte, stride, tw, th int, bg, fg *uint32) error {
	buf := d.enc.tile[:]
	if err := d.read(buf[:1]); err != nil {
		return err
	}
	sub := buf[0]
	if sub&hextileRaw != 0 {
		row := 4 * tw
		raw := buf[:row*th]
		if err := d.read(raw); err != nil {
			return err
		}
		for j := range th {
			copyOpaque(pix[j*stride:j*stride+row], raw[j*row:])
		}
		d.enc.counts[hextileRawTile]++
		return nil
	}
	if sub&hextileBackground != 0 {
		if err := d.read(buf[:4]); err != nil {
			return err
		}
		*bg = binary.LittleEndian.Uint32(buf) | 0xff<<24
	}
	fill(pix, stride, 0, 0, tw, th, *bg)
	if sub&hextileForeground != 0 {
		if err := d.read(buf[:4]); err != nil {
			return err
		}
		*fg = binary.LittleEndian.Uint32(buf) | 0xff<<24
	}
	if sub&hextileAnySubrects == 0 {
		d.enc.counts[hextileSolidTile]++
		return nil
	}
	if err := d.read(buf[:1]); err != nil {
		return err
	}
	n, size := int(buf[0]), 2
	if sub&hextileColoured != 0 {
		size = 6
	}
	rects := buf[:n*size] // at most 255×6 bytes
	if err := d.read(rects); err != nil {
		return err
	}
	for i := range n {
		p := rects[i*size : (i+1)*size]
		if size == 6 {
			*fg = binary.LittleEndian.Uint32(p) | 0xff<<24
			p = p[4:]
		}
		x, y := int(p[0]>>4), int(p[0]&15)
		w, h := int(p[1]>>4)+1, int(p[1]&15)+1
		if x+w > tw || y+h > th {
			return fmt.Errorf("subrectangle %d×%d at (%d, %d) outside the tile of %d×%d", w, h, x, y, tw, th)
		}
		fill(pix, stride, x, y, w, h, *fg)
	}
	if size == 6 {
		d.enc.counts[hextileColourTile]++
	} else {
		d.enc.counts[hextileMonoTile]++
	}
	return nil
}
