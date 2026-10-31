// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package vnc

import (
	"encoding/binary"
	"image"
)

// codecs is the decoder's state for Hextile, ZRLE and Tight: the zlib
// streams, which carry over from one rectangle to the next and end with
// Run, and scratch space that is reused.
type codecs struct {
	zrle  zstream
	tight [4]zstream

	// band holds a row of tiles while it is decoded, at the rectangle's
	// width: 16 rows for Hextile, 64 for ZRLE, 4 MiB at most.
	band []byte
	// tile holds one tile's data as it arrives: a raw Hextile tile, or
	// its subrectangles, or a raw ZRLE tile.
	tile [zrleTile * zrleTile * 3]byte
	// prev is the gradient filter's row above, as reconstructed.
	prev []byte
	// jpeg holds a Tight JPEG image as it arrives.
	jpeg []byte

	// counts are the tiles and rectangles decoded by kind, for tests.
	counts [numKinds]int
}

// kind is a kind of tile or rectangle, which the decoder counts.
type kind int

const (
	hextileRawTile    kind = iota
	hextileSolidTile       // the background only
	hextileMonoTile        // subrectangles in the foreground colour
	hextileColourTile      // subrectangles of their own colours
	zrleRawTile
	zrleSolidTile
	zrlePackedTile
	zrlePlainRLETile
	zrlePaletteRLETile
	tightFillRect
	tightJPEGRect
	tightCopyRect
	tightPaletteRect
	tightGradientRect
	tightRawData  // a basic rectangle's data, sent as it is
	tightZlibData // a basic rectangle's data, inflated
	numKinds
)

var kindNames = [numKinds]string{
	"hextile raw", "hextile solid", "hextile mono", "hextile coloured",
	"zrle raw", "zrle solid", "zrle packed palette", "zrle plain rle", "zrle palette rle",
	"tight fill", "tight jpeg", "tight copy", "tight palette", "tight gradient",
	"tight raw data", "tight zlib data",
}

func (k kind) String() string { return kindNames[k] }

// bandBuffer returns n bytes of scratch space for a row of tiles.
func (c *codecs) bandBuffer(n int) []byte {
	if cap(c.band) < n {
		c.band = make([]byte, n)
	}
	return c.band[:n]
}

// drawBand copies pixels decoded into band, which are opaque and in rows
// of r's width, to the framebuffer at r, under one lock. Each hold of a
// lock is a function of its own that defers the Unlock, so that a panic
// while it draws releases the rows (backend.Sink).
func (d *decoder) drawBand(r image.Rectangle, band []byte) {
	row := 4 * r.Dx()
	img := d.sink.Lock(r)
	defer d.sink.Unlock(r)
	for i := range r.Dy() {
		copy(img.Pix[i*img.Stride:i*img.Stride+row], band[i*row:])
	}
}

// fill sets w×h pixels of a tile, which has the given stride and starts
// at pix, to the pixel px, from the pixel at (x, y).
func fill(pix []byte, stride, x, y, w, h int, px uint32) {
	for j := y; j < y+h; j++ {
		row := pix[j*stride+4*x : j*stride+4*(x+w)]
		for i := 0; i < len(row); i += 4 {
			binary.LittleEndian.PutUint32(row[i:], px)
		}
	}
}

// fillRect sets the pixels of r, which lies within the screen, to px, in
// strips of whole rows.
func (d *decoder) fillRect(r image.Rectangle, px uint32) {
	rows := max(1, stripSize/(4*r.Dx()))
	for y := r.Min.Y; y < r.Max.Y; y += rows {
		d.fillStrip(image.Rect(r.Min.X, y, r.Max.X, min(y+rows, r.Max.Y)), px)
	}
}

// fillStrip sets the pixels of strip to px under one lock.
func (d *decoder) fillStrip(strip image.Rectangle, px uint32) {
	img := d.sink.Lock(strip)
	defer d.sink.Unlock(strip)
	first := img.Pix[:4*strip.Dx()]
	fill(first, 0, 0, 0, strip.Dx(), 1, px)
	for i := 1; i < strip.Dy(); i++ {
		copy(img.Pix[i*img.Stride:], first)
	}
}

// rgb returns the opaque pixel of the colour in p's first three bytes:
// red, green and blue, as a compact pixel of ZRLE or Tight carries them.
func rgb(p []byte) uint32 {
	return uint32(p[0]) | uint32(p[1])<<8 | uint32(p[2])<<16 | 0xff<<24
}

// expand copies compact pixels of three bytes from src to dst as opaque
// pixels of four, until either ends.
func expand(dst, src []byte) {
	n := min(len(dst)/4, len(src)/3)
	for i := range n {
		d, s := dst[4*i:4*i+4], src[3*i:3*i+3]
		d[0], d[1], d[2], d[3] = s[0], s[1], s[2], 0xff
	}
}
