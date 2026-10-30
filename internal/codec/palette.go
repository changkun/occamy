// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package codec

import "encoding/binary"

// MaxColours is the most colours a palette holds. A tile with more is
// photographic.
const MaxColours = 256

// Palette is the distinct colours of a tile, alpha included, with each
// pixel's index into them: what the deflate container's palette forms
// hold, and what classifies a tile. Its zero value is ready to use, and
// Build reuses its memory from one tile to the next.
type Palette struct {
	// Colours are the distinct pixels, 4 bytes of red, green, blue and
	// alpha read as a little-endian uint32, in order of first appearance.
	Colours []uint32
	// Index is each pixel's index into Colours, row by row.
	Index []uint8
	// Opaque reports that every pixel's alpha is 255.
	Opaque bool

	// An open-addressed table of the colours: keys, and each key's index
	// in Colours, in slots that used marks.
	keys [tableSize]uint32
	slot [tableSize]uint8
	used [tableSize]bool
}

// tableSize keeps the table at most half full.
const tableSize = 2 * MaxColours

// Build fills p from pix, w by h pixels in RGBA order row by row, and
// reports whether they have at most MaxColours colours. It stops at the
// first colour over, leaving p incomplete, so that a photographic tile
// costs little to classify.
func (p *Palette) Build(pix []byte, w, h int) bool {
	n := w * h
	p.Colours = p.Colours[:0]
	if cap(p.Index) < n {
		p.Index = make([]uint8, 0, n)
	}
	p.Index = p.Index[:0]
	clear(p.used[:])
	if n <= 0 {
		p.Opaque = true
		return true
	}
	alpha := uint32(0xff)
	// The previous pixel's colour and index, since runs are common on
	// screens; it starts as a colour that differs from the first pixel.
	last, lastIndex := binary.LittleEndian.Uint32(pix)^1, uint8(0)
	for i := 0; i < 4*n; i += 4 {
		c := binary.LittleEndian.Uint32(pix[i:])
		if c == last {
			p.Index = append(p.Index, lastIndex)
			continue
		}
		v, ok := p.lookup(c)
		if !ok {
			return false
		}
		alpha &= c >> 24
		p.Index = append(p.Index, v)
		last, lastIndex = c, v
	}
	p.Opaque = alpha == 0xff
	return true
}

// lookup finds or adds c, and reports false if adding it would make one
// colour too many.
func (p *Palette) lookup(c uint32) (uint8, bool) {
	h := (c * 0x9e3779b1) >> 23 // the top 9 bits: tableSize slots
	for {
		switch {
		case !p.used[h]:
			if len(p.Colours) == MaxColours {
				return 0, false
			}
			p.used[h], p.keys[h] = true, c
			p.slot[h] = uint8(len(p.Colours)) // #nosec G115 -- fewer than 256 colours so far
			p.Colours = append(p.Colours, c)
			return p.slot[h], true
		case p.keys[h] == c:
			return p.slot[h], true
		}
		h = (h + 1) % tableSize
	}
}

// Colours counts the distinct colours of pix, w by h pixels in RGBA
// order row by row, alpha included, up to MaxColours+1: a result of
// MaxColours+1 means more than MaxColours. p is the palette's memory,
// and holds the palette afterwards if there were at most MaxColours.
func Colours(pix []byte, w, h int, p *Palette) int {
	if !p.Build(pix, w, h) {
		return MaxColours + 1
	}
	return len(p.Colours)
}

// opaque reports whether every pixel of pix, w by h, has alpha 255.
func opaque(pix []byte, w, h int) bool {
	for i := 3; i < 4*w*h; i += 4 {
		if pix[i] != 0xff {
			return false
		}
	}
	return true
}
