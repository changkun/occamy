// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package codecs

import (
	"errors"
	"image"
)

// The palette container is lossless option A of the spec before
// compression. Its first byte says which form follows:
//
//   - 0, a palette: one byte holding the number of colours minus one,
//     three bytes of red, green and blue for each colour, then each
//     pixel's index packed at 1, 2, 4 or 8 bits for up to 2, 4, 16 or 256
//     colours, high bits first, rows padded to a byte; a tile of one
//     colour has no indices.
//   - 1, raw pixels: red, green and blue for each pixel.
//
// The tile's width and height travel outside it, in the tile message.
const (
	formPalette = 0
	formRaw     = 1
)

// indexBits is the packed index width for n colours.
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

// AppendContainer appends img in the palette container to dst. p must
// have been built from img; if p.Build failed, the tile goes raw.
func AppendContainer(dst []byte, img *image.RGBA, p *Palette, paletted bool) []byte {
	if !paletted {
		dst = append(dst, formRaw)
		return AppendRGB(dst, img)
	}
	n := len(p.Colours)
	dst = append(dst, formPalette, byte(n-1)) // #nosec G115 -- 1 to 256 colours
	for _, c := range p.Colours {
		dst = append(dst, byte(c>>16), byte(c>>8), byte(c)) // #nosec G115 -- bytes of 0xRRGGBB
	}
	bitsPer := indexBits(n)
	if bitsPer == 0 {
		return dst
	}
	w, h := img.Rect.Dx(), img.Rect.Dy()
	if bitsPer == 8 {
		return append(dst, p.Index[:w*h]...)
	}
	perByte := 8 / bitsPer
	for y := range h {
		row := p.Index[y*w : (y+1)*w]
		for x := 0; x < w; x += perByte {
			var v byte
			for k := range perByte {
				v <<= bitsPer
				if x+k < w {
					v |= row[x+k]
				}
			}
			dst = append(dst, v)
		}
	}
	return dst
}

// AppendRGB appends img's pixels as red, green and blue.
func AppendRGB(dst []byte, img *image.RGBA) []byte {
	b := img.Rect
	for y := b.Min.Y; y < b.Max.Y; y++ {
		row := img.Pix[img.PixOffset(b.Min.X, y):img.PixOffset(b.Max.X, y)]
		for i := 0; i < len(row); i += 4 {
			dst = append(dst, row[i], row[i+1], row[i+2])
		}
	}
	return dst
}

var errContainer = errors.New("codecs: malformed palette container")

// DecodeContainer decodes a w×h tile from the palette container into
// dst, which must be w×h at the origin.
func DecodeContainer(dst *image.RGBA, src []byte) error {
	w, h := dst.Rect.Dx(), dst.Rect.Dy()
	if len(src) < 1 {
		return errContainer
	}
	pix := dst.Pix
	switch src[0] {
	case formRaw:
		src = src[1:]
		if len(src) != 3*w*h {
			return errContainer
		}
		for i, j := 0, 0; j < len(src); i, j = i+4, j+3 {
			pix[i], pix[i+1], pix[i+2], pix[i+3] = src[j], src[j+1], src[j+2], 0xff
		}
		return nil
	case formPalette:
	default:
		return errContainer
	}
	if len(src) < 2 {
		return errContainer
	}
	n := int(src[1]) + 1
	if len(src) < 2+3*n {
		return errContainer
	}
	var lut [256][4]byte
	for i := range n {
		c := src[2+3*i:]
		lut[i] = [4]byte{c[0], c[1], c[2], 0xff} // #nosec G602 -- i < n <= 256
	}
	idx := src[2+3*n:]
	bitsPer := indexBits(n)
	if bitsPer == 0 {
		for i := 0; i < len(pix); i += 4 {
			copy(pix[i:i+4], lut[0][:])
		}
		return nil
	}
	perByte := 8 / bitsPer
	stride := (w + perByte - 1) / perByte
	if len(idx) != stride*h {
		return errContainer
	}
	mask := byte(1<<bitsPer - 1)
	for y := range h {
		row := idx[y*stride : (y+1)*stride]
		out := pix[y*w*4 : (y+1)*w*4]
		for x := range w {
			shift := uint(8 - bitsPer*(x%perByte+1)) // #nosec G115 -- 0 to 7
			v := row[x/perByte] >> shift & mask
			copy(out[x*4:x*4+4], lut[v][:])
		}
	}
	return nil
}
