// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package codec

import (
	"bytes"
	"compress/flate"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"io"
)

// A deflate payload (codec 2) is a raw DEFLATE stream (RFC 1951) of a
// container whose first byte, the form, says what follows:
//
//   - Bit 0 clear, a palette: one byte holding the number of colours
//     minus one, the colours, then each pixel's index into them, packed
//     at 1, 2, 4 or 8 bits for up to 2, 4, 16 or 256 colours, most
//     significant bits first, each row starting on a byte with zero
//     padding. A palette of one colour has no indices.
//   - Bit 0 set, raw pixels, row by row.
//   - Bit 1 clear, opaque: a colour or a pixel is 3 bytes of red, green
//     and blue, and its alpha is 255.
//   - Bit 1 set, with alpha: 4 bytes of red, green, blue and straight
//     alpha.
//
// The other bits are zero. The rectangle's width and height travel in
// the message, and the container holds exactly what they call for.
// docs/protocol.md is the normative description.
const (
	formRaw   = 1 << 0
	formAlpha = 1 << 1
)

// indexBits is the width of a packed index into n colours.
func indexBits(n int) int {
	switch {
	case n == 1:
		return 0
	case n <= 2:
		return 1
	case n <= 4:
		return 2
	case n <= 16:
		return 4
	}
	return 8
}

// AppendContainer appends the container of pix, w by h pixels in RGBA
// order row by row, to dst, before compression: in a palette form if p
// is not nil, in which case p must hold pix's palette as Build made it,
// and in a raw form otherwise. The forms with alpha are used only when a
// pixel is not opaque. w and h are at least 1.
func AppendContainer(dst, pix []byte, w, h int, p *Palette) []byte {
	if p == nil {
		if opaque(pix, w, h) {
			dst = append(dst, formRaw)
			for i := 0; i < 4*w*h; i += 4 {
				dst = append(dst, pix[i], pix[i+1], pix[i+2])
			}
			return dst
		}
		dst = append(dst, formRaw|formAlpha)
		return append(dst, pix[:4*w*h]...)
	}
	n := len(p.Colours)
	if p.Opaque {
		dst = append(dst, 0, byte(n-1)) // #nosec G115 -- 1 to 256 colours
		for _, c := range p.Colours {
			dst = append(dst, byte(c), byte(c>>8), byte(c>>16)) // #nosec G115 -- the bytes of a pixel
		}
	} else {
		dst = append(dst, formAlpha, byte(n-1)) // #nosec G115 -- 1 to 256 colours
		for _, c := range p.Colours {
			dst = binary.LittleEndian.AppendUint32(dst, c)
		}
	}
	bits := indexBits(n)
	switch bits {
	case 0:
		return dst
	case 8:
		return append(dst, p.Index[:w*h]...)
	}
	perByte := 8 / bits
	for y := range h {
		row := p.Index[y*w : (y+1)*w]
		for x := 0; x < w; x += perByte {
			var v byte
			for k := range perByte {
				v <<= bits
				if x+k < w {
					v |= row[x+k]
				}
			}
			dst = append(dst, v)
		}
	}
	return dst
}

// Deflater writes deflate payloads (codec 2) with compress/flate at
// level 1, as the codec measurement chose. It keeps its buffers and its
// compressor between calls, so it is not safe for concurrent use. Its
// zero value is ready to use.
type Deflater struct {
	p   Palette
	buf []byte // the container before compression
	fw  *flate.Writer
	out sliceWriter
}

// Append appends the deflate payload of pix, w by h pixels in RGBA order
// row by row, to dst: a palette if it has at most MaxColours colours,
// raw pixels otherwise. w and h are at least 1.
func (d *Deflater) Append(dst, pix []byte, w, h int) []byte {
	var p *Palette
	if d.p.Build(pix, w, h) {
		p = &d.p
	}
	return d.AppendPalette(dst, pix, w, h, p)
}

// AppendPalette is Append for a caller that has classified the pixels
// already: p holds their palette as Build made it, or is nil for raw
// pixels.
func (d *Deflater) AppendPalette(dst, pix []byte, w, h int, p *Palette) []byte {
	d.buf = AppendContainer(d.buf[:0], pix, w, h, p)
	d.out.b = dst
	if d.fw == nil {
		// BestSpeed is a valid level, so NewWriter cannot fail.
		d.fw, _ = flate.NewWriter(&d.out, flate.BestSpeed)
	} else {
		d.fw.Reset(&d.out)
	}
	// Writes to a slice cannot fail.
	_, _ = d.fw.Write(d.buf)
	_ = d.fw.Close()
	dst, d.out.b = d.out.b, nil
	return dst
}

// sliceWriter appends what is written to a slice.
type sliceWriter struct{ b []byte }

func (s *sliceWriter) Write(p []byte) (int, error) {
	s.b = append(s.b, p...)
	return len(p), nil
}

// Inflater decodes deflate payloads (codec 2). It inflates no more than
// the container of the rectangle's size can hold, and allocates nothing
// that grows with the payload: its decompressor and one row, both reused
// between calls. It is not safe for concurrent use; its zero value is
// ready to use.
type Inflater struct {
	src bytes.Reader
	fr  io.ReadCloser
	row []byte
	one [1]byte // a byte to read into, which would escape from the stack
	lut [MaxColours]uint32
}

// Decode decodes a deflate payload into dst, whose bounds are the
// rectangle's size. It fails with ErrInvalid unless the payload is one
// DEFLATE stream, with nothing after it, of a container that holds
// exactly the rectangle's pixels and no index beyond its palette. dst
// may be changed even when Decode fails.
func (d *Inflater) Decode(dst *image.RGBA, payload []byte) error {
	w, h := dst.Rect.Dx(), dst.Rect.Dy()
	if w <= 0 || h <= 0 {
		return fmt.Errorf("%w: an empty rectangle", ErrInvalid)
	}
	d.src.Reset(payload)
	if d.fr == nil {
		d.fr = flate.NewReader(&d.src)
	} else if err := d.fr.(flate.Resetter).Reset(&d.src, nil); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	form, err := d.byte()
	if err != nil {
		return err
	}
	if form&^(formRaw|formAlpha) != 0 {
		return fmt.Errorf("%w: container form %d", ErrInvalid, form)
	}
	size := 3
	if form&formAlpha != 0 {
		size = 4
	}
	if form&formRaw != 0 {
		err = d.raw(dst, w, h, size)
	} else {
		err = d.palette(dst, w, h, size)
	}
	if err != nil {
		return err
	}
	// The container ends with the rectangle, and the stream with it.
	switch _, err := io.ReadFull(d.fr, d.one[:]); {
	case err == nil:
		return fmt.Errorf("%w: the container is longer than %d×%d pixels", ErrInvalid, w, h)
	case !errors.Is(err, io.EOF):
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	if d.src.Len() != 0 {
		return fmt.Errorf("%w: %d bytes after the DEFLATE stream", ErrInvalid, d.src.Len())
	}
	return nil
}

// read inflates exactly len(b) bytes of the container.
func (d *Inflater) read(b []byte) error {
	if _, err := io.ReadFull(d.fr, b); err != nil {
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	return nil
}

func (d *Inflater) byte() (byte, error) {
	err := d.read(d.one[:])
	return d.one[0], err
}

// rowBuffer returns d.row with room for n bytes.
func (d *Inflater) rowBuffer(n int) []byte {
	if cap(d.row) < n {
		d.row = make([]byte, n)
	}
	return d.row[:n]
}

// raw decodes raw pixels of size bytes each.
func (d *Inflater) raw(dst *image.RGBA, w, h, size int) error {
	row := d.rowBuffer(size * w)
	for y := range h {
		if err := d.read(row); err != nil {
			return err
		}
		o := dst.PixOffset(dst.Rect.Min.X, dst.Rect.Min.Y+y)
		out := dst.Pix[o : o+4*w]
		if size == 4 {
			copy(out, row)
			continue
		}
		for i, j := 0, 0; i < len(out); i, j = i+4, j+3 {
			out[i], out[i+1], out[i+2], out[i+3] = row[j], row[j+1], row[j+2], 0xff
		}
	}
	return nil
}

// palette decodes a palette of colours of size bytes each, and the
// indices into it.
func (d *Inflater) palette(dst *image.RGBA, w, h, size int) error {
	c, err := d.byte()
	if err != nil {
		return err
	}
	n := int(c) + 1
	colours := d.rowBuffer(size * n)
	if err := d.read(colours); err != nil {
		return err
	}
	for i := range n {
		if size == 4 {
			d.lut[i] = binary.LittleEndian.Uint32(colours[4*i:])
			continue
		}
		d.lut[i] = uint32(colours[3*i]) | uint32(colours[3*i+1])<<8 | uint32(colours[3*i+2])<<16 | 0xff<<24
	}
	bits := indexBits(n)
	if bits == 0 {
		for y := range h {
			o := dst.PixOffset(dst.Rect.Min.X, dst.Rect.Min.Y+y)
			for i := o; i < o+4*w; i += 4 {
				binary.LittleEndian.PutUint32(dst.Pix[i:], d.lut[0])
			}
		}
		return nil
	}
	row := d.rowBuffer((w*bits + 7) / 8)
	mask := byte(1)<<bits - 1
	for y := range h {
		if err := d.read(row); err != nil {
			return err
		}
		o := dst.PixOffset(dst.Rect.Min.X, dst.Rect.Min.Y+y)
		out := dst.Pix[o : o+4*w]
		for x := range w {
			var v byte
			if bits == 8 {
				v = row[x]
			} else {
				bit := x * bits
				v = row[bit/8] >> (8 - bits - bit%8) & mask
			}
			if int(v) >= n {
				return fmt.Errorf("%w: index %d into %d colours", ErrInvalid, v, n)
			}
			binary.LittleEndian.PutUint32(out[4*x:], d.lut[v])
		}
	}
	return nil
}
