// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package vnc

import (
	"encoding/binary"
	"fmt"
	"image"
)

// encZRLE is the ZRLE encoding (RFC 6143, section 7.7.6).
const encZRLE int32 = 16

// zrleTile is the side of a ZRLE tile.
const zrleTile = 64

// zrleMaxData is the most data, before compression, that a w×h rectangle
// of ZRLE takes in any of its encodings: per tile its subencoding and a
// palette of 127 compact pixels, and per pixel at most 4 bytes, a compact
// pixel and a run length of one in plain RLE.
func zrleMaxData(w, h int) int {
	tiles := ((w + zrleTile - 1) / zrleTile) * ((h + zrleTile - 1) / zrleTile)
	return tiles*(1+3*127) + 4*w*h
}

// zrle draws a ZRLE rectangle: the length of its zlib data, which
// continues the connection's one ZRLE stream, and in it tiles of 64×64
// pixels, left to right and top to bottom, smaller at the right and
// bottom edges. The backend's pixel format makes ZRLE's compact pixels 3
// bytes: red, green and blue. Each row of tiles is inflated into scratch
// space and then drawn under one lock.
func (d *decoder) zrle(r image.Rectangle) error {
	var b [4]byte
	if err := d.read(b[:]); err != nil {
		return err
	}
	n := int64(binary.BigEndian.Uint32(b[:]))
	if r.Empty() && n == 0 {
		return nil
	}
	if bound := int64(deflateBound(zrleMaxData(r.Dx(), r.Dy()))); n > bound {
		return fmt.Errorf("zrle data of %d bytes for a rectangle of %d×%d, more than %d", n, r.Dx(), r.Dy(), bound)
	}
	if !r.Empty() {
		if err := d.onScreen("zrle rectangle", r); err != nil {
			return err
		}
	}
	z := &d.enc.zrle
	if err := z.begin(d.r, n); err != nil {
		return fmt.Errorf("zrle: %w", err)
	}
	w := r.Dx()
	stride := 4 * w
	// An empty rectangle has no tiles, but may hold a flush.
	for y := r.Min.Y; y < r.Max.Y && w > 0; y += zrleTile {
		th := min(zrleTile, r.Max.Y-y)
		band := d.enc.bandBuffer(stride * th)
		for x := 0; x < w; x += zrleTile {
			tw := min(zrleTile, w-x)
			if err := d.zrleTile(z, band[4*x:], stride, tw, th); err != nil {
				return fmt.Errorf("zrle tile at (%d, %d): %w", r.Min.X+x, y, err)
			}
		}
		d.drawBand(image.Rect(r.Min.X, y, r.Max.X, y+th), band)
	}
	if err := z.end(); err != nil {
		return fmt.Errorf("zrle: %w", err)
	}
	return nil
}

// zrleTile decodes one tile of tw×th pixels into pix, whose rows are
// stride bytes apart.
func (d *decoder) zrleTile(z *zstream, pix []byte, stride, tw, th int) error {
	sub, err := z.byte()
	if err != nil {
		return err
	}
	var pal [127]uint32
	var palSize int
	switch {
	case sub == 0, sub == 128: // raw, plain RLE
	case sub <= 16: // solid, packed palette
		palSize = int(sub)
	case sub >= 130: // palette RLE
		palSize = int(sub) - 128
	default:
		return fmt.Errorf("unused subencoding %d", sub)
	}
	buf := d.enc.tile[:]
	if palSize > 0 {
		if err := z.read(buf[:3*palSize]); err != nil {
			return err
		}
		for i := range palSize {
			pal[i] = rgb(buf[3*i:])
		}
	}
	switch {
	case sub == 0:
		row := 3 * tw
		raw := buf[:row*th]
		if err := z.read(raw); err != nil {
			return err
		}
		for j := range th {
			expand(pix[j*stride:j*stride+4*tw], raw[j*row:])
		}
		d.enc.counts[zrleRawTile]++
	case sub == 1:
		fill(pix, stride, 0, 0, tw, th, pal[0])
		d.enc.counts[zrleSolidTile]++
	case sub <= 16:
		return d.zrlePacked(z, pix, stride, tw, th, pal[:palSize])
	case sub == 128:
		return d.zrlePlainRLE(z, pix, stride, tw, th)
	default:
		return d.zrlePaletteRLE(z, pix, stride, tw, th, pal[:palSize])
	}
	return nil
}

// zrlePacked decodes a packed palette tile: indices of 1, 2 or 4 bits,
// the most significant first, each row padded to a whole byte.
func (d *decoder) zrlePacked(z *zstream, pix []byte, stride, tw, th int, pal []uint32) error {
	bits := 4
	switch {
	case len(pal) == 2:
		bits = 1
	case len(pal) <= 4:
		bits = 2
	}
	row := (tw*bits + 7) / 8
	packed := d.enc.tile[:row*th]
	if err := z.read(packed); err != nil {
		return err
	}
	mask := byte(1)<<bits - 1
	for j := range th {
		src, dst := packed[j*row:(j+1)*row], pix[j*stride:]
		for i := range tw {
			bit := i * bits
			idx := int(src[bit/8] >> (8 - bits - bit%8) & mask)
			if idx >= len(pal) {
				return fmt.Errorf("palette index %d of %d colours", idx, len(pal))
			}
			binary.LittleEndian.PutUint32(dst[4*i:], pal[idx])
		}
	}
	d.enc.counts[zrlePackedTile]++
	return nil
}

// zrlePlainRLE decodes a plain RLE tile: runs of a compact pixel and a
// run length, which may not run past the tile.
func (d *decoder) zrlePlainRLE(z *zstream, pix []byte, stride, tw, th int) error {
	var c [3]byte
	for at, end := 0, tw*th; at < end; {
		if err := z.read(c[:]); err != nil {
			return err
		}
		n, err := zrleRun(z, end-at)
		if err != nil {
			return err
		}
		zrleFill(pix, stride, tw, at, n, rgb(c[:]))
		at += n
	}
	d.enc.counts[zrlePlainRLETile]++
	return nil
}

// zrlePaletteRLE decodes a palette RLE tile: palette indices, of single
// pixels, or with the top bit set of runs whose length follows.
func (d *decoder) zrlePaletteRLE(z *zstream, pix []byte, stride, tw, th int, pal []uint32) error {
	for at, end := 0, tw*th; at < end; {
		idx, err := z.byte()
		if err != nil {
			return err
		}
		n := 1
		if idx&0x80 != 0 {
			idx &= 0x7f
			if n, err = zrleRun(z, end-at); err != nil {
				return err
			}
		}
		if int(idx) >= len(pal) {
			return fmt.Errorf("palette index %d of %d colours", idx, len(pal))
		}
		zrleFill(pix, stride, tw, at, n, pal[idx])
		at += n
	}
	d.enc.counts[zrlePaletteRLETile]++
	return nil
}

// zrleRun reads a run length: bytes that add up, each 255 but the last,
// plus one. A run longer than left, the pixels left in the tile, is an
// error as soon as it is, so that a server cannot send 255s forever.
func zrleRun(z *zstream, left int) (int, error) {
	n := 1
	for {
		b, err := z.byte()
		if err != nil {
			return 0, err
		}
		n += int(b)
		if n > left {
			return 0, fmt.Errorf("a run of %d or more pixels with %d left in the tile", n, left)
		}
		if b != 255 {
			return n, nil
		}
	}
}

// zrleFill sets n pixels of a tile of width tw to px, from the at-th in
// raster order, across rows.
func zrleFill(pix []byte, stride, tw, at, n int, px uint32) {
	x, y := at%tw, at/tw
	for n > 0 {
		w := min(n, tw-x)
		row := pix[y*stride+4*x : y*stride+4*(x+w)]
		for i := 0; i < len(row); i += 4 {
			binary.LittleEndian.PutUint32(row[i:], px)
		}
		n -= w
		x, y = 0, y+1
	}
}
