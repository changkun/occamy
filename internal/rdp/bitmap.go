// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package rdp

import (
	"errors"
	"fmt"
	"image"
)

// Flags of TS_BITMAP_DATA, [MS-RDPBCGR] 2.2.9.1.1.3.1.2.2.
const (
	bitmapCompression      = 0x0001
	noBitmapCompressionHDR = 0x0400
)

const (
	// maxBitmapPixels bounds one bitmap rectangle, whatever the desktop.
	// Servers tile updates in rectangles of 64×64 pixels or a few
	// scanlines; the client bounds bitmaps by the desktop as well, and
	// this only keeps the decoder from allocating gigabytes on its own.
	maxBitmapPixels = 4096 * 2048
)

// bitmapData is one TS_BITMAP_DATA of a bitmap update.
type bitmapData struct {
	dest          image.Rectangle // where the bitmap goes, half-open
	width, height int             // the bitmap's size, which dest clips
	bpp           int
	compressed    bool
	data          []byte
}

// parseBitmapUpdate calls f for each rectangle of a TS_UPDATE_BITMAP_DATA,
// [MS-RDPBCGR] 2.2.9.1.1.3.1.2, after its updateType.
func parseBitmapUpdate(p []byte, f func(*bitmapData) error) error {
	r := reader{b: p}
	n := int(r.u16())
	var b bitmapData
	for range n {
		left, top := int(r.u16()), int(r.u16())
		right, bottom := int(r.u16()), int(r.u16())
		b.width, b.height = int(r.u16()), int(r.u16())
		b.bpp = int(r.u16())
		flags := r.u16()
		length := int(r.u16())
		b.compressed = flags&bitmapCompression != 0
		if b.compressed && flags&noBitmapCompressionHDR == 0 {
			// TS_CD_HEADER: sizes that the stream implies anyway.
			r.skip(8)
			length -= 8
		}
		b.data = r.take(length)
		if r.err != nil {
			return fmt.Errorf("rdp: bitmap update: %w", r.err)
		}
		b.dest = image.Rect(left, top, right+1, bottom+1)
		if err := f(&b); err != nil {
			return err
		}
	}
	return r.err
}

// decoder decodes bitmaps. It keeps its buffers between bitmaps, so
// that they grow to the largest bitmap and stay: xrdp's are at most 8192
// pixels, Windows' 64×64 tiles. The client drops them when the desktop
// changes, which bounds them by the largest bitmap the desktop allows.
type decoder struct {
	raw     []uint32
	rgba    []byte
	planes  [4][]byte
	palette [256][3]byte
}

// decode decodes b into RGBA rows top-down, b.width×b.height pixels. The
// result is valid until the next call.
func (d *decoder) decode(b *bitmapData) ([]byte, error) {
	w, h := b.width, b.height
	if w <= 0 || h <= 0 || w*h > maxBitmapPixels {
		return nil, fmt.Errorf("rdp: bitmap of %d×%d pixels", w, h)
	}
	n := w * h
	if cap(d.rgba) < 4*n {
		d.rgba = make([]byte, 4*n)
	}
	out := d.rgba[:4*n]
	if b.compressed && b.bpp == 32 {
		return out, d.planarDecode(b.data, w, h, out, true)
	}
	bytesPP := (b.bpp + 7) / 8
	switch b.bpp {
	case 8, 15, 16, 24, 32:
	default:
		return nil, fmt.Errorf("rdp: bitmap of %d bits per pixel", b.bpp)
	}
	if cap(d.raw) < n {
		d.raw = make([]uint32, n)
	}
	raw := d.raw[:n]
	if b.compressed {
		clear(raw)
		if err := rleDecode(b.data, raw, w, b.bpp); err != nil {
			return nil, err
		}
	} else {
		// Uncompressed rows are bottom-up; servers send widths whose rows
		// fill whole 4-byte units, but pad the rows if not.
		stride := w * bytesPP
		if len(b.data) >= (stride+3)&^3*h {
			stride = (stride + 3) &^ 3
		}
		if len(b.data) < stride*h {
			return nil, errors.New("rdp: uncompressed bitmap too short")
		}
		for y := range h {
			row := b.data[y*stride:]
			for x := range w {
				var v uint32
				for i := range bytesPP {
					v |= uint32(row[x*bytesPP+i]) << (8 * i)
				}
				raw[y*w+x] = v
			}
		}
	}
	// raw holds the rows bottom-up.
	for y := range h {
		src := raw[(h-1-y)*w : (h-y)*w]
		dst := out[4*y*w : 4*(y+1)*w]
		d.convert(dst, src, b.bpp)
	}
	return out, nil
}

// drop lets go of the scratch buffers. The slice decode returned stays
// valid.
func (d *decoder) drop() {
	d.rgba, d.raw, d.planes = nil, nil, [4][]byte{}
}

// convert turns pixel values of bpp bits into RGBA.
func (d *decoder) convert(dst []byte, src []uint32, bpp int) {
	for i, v := range src {
		var r, g, b byte
		switch bpp {
		case 8:
			c := d.palette[v&0xff]
			r, g, b = c[0], c[1], c[2]
		case 15:
			r, g, b = expand5(v>>10), expand5(v>>5), expand5(v)
		case 16:
			r, g, b = expand5(v>>11), expand6(v>>5), expand5(v)
		default: // 24 and 32: B, G, R in the low bytes
			r, g, b = byte(v>>16), byte(v>>8), byte(v) // #nosec G115 -- extracts bytes
		}
		dst[4*i], dst[4*i+1], dst[4*i+2], dst[4*i+3] = r, g, b, 0xff
	}
}

// expand5 and expand6 widen the low 5 or 6 bits of v to 8, as
// [MS-RDPBCGR] 2.2.9.1.1.3.1.2.2 describes for 15- and 16-bit colour.
func expand5(v uint32) byte {
	v &= 0x1f
	return byte(v<<3 | v>>2) // #nosec G115 -- 8 bits
}

func expand6(v uint32) byte {
	v &= 0x3f
	return byte(v<<2 | v>>4) // #nosec G115 -- 8 bits
}

// parsePalette reads TS_UPDATE_PALETTE_DATA, [MS-RDPBCGR] 2.2.9.1.1.3.1.1,
// after its updateType.
func (d *decoder) parsePalette(p []byte) error {
	r := reader{b: p}
	r.u16() // pad2Octets
	n := int(r.u32())
	if n > 256 {
		return fmt.Errorf("rdp: palette of %d colours", n)
	}
	for i := range n {
		d.palette[i] = [3]byte{r.u8(), r.u8(), r.u8()}
	}
	return r.err
}
