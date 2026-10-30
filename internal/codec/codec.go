// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// Package codec holds the payload formats of protocol v1 that Occamy
// defines itself, both ways: raw pixels (codec 1) and the deflate
// container (codec 2), as docs/protocol.md describes them under Codecs.
// It also counts a tile's colours, which is how the graphics pipeline
// classifies tiles (specs/008-native-architecture/graphics-pipeline.md).
//
// The package uses the standard library only, so that a client decodes
// with it without linking the server's lossy encoders. JPEG and WebP
// payloads are standard files, which image/jpeg and
// golang.org/x/image/webp decode.
//
// Pixels are RGBA, 4 bytes each, with straight alpha, as the protocol
// carries them. A decoder writes them into an *image.RGBA as they are;
// for a cursor, whose alpha is not opaque, view the same pixels as an
// *image.NRGBA.
package codec

import (
	"errors"
	"fmt"
	"image"
)

// ErrInvalid reports a payload that does not hold the pixels of its
// rectangle in its codec's format.
var ErrInvalid = errors.New("codec: invalid payload")

// AppendRaw appends pix, w by h pixels in RGBA order row by row, as a raw
// payload (codec 1), which is the pixels themselves.
func AppendRaw(dst, pix []byte, w, h int) []byte {
	return append(dst, pix[:4*w*h]...)
}

// DecodeRaw decodes a raw payload (codec 1) into dst, whose bounds are the
// rectangle's size: exactly 4 bytes for each of its pixels.
func DecodeRaw(dst *image.RGBA, payload []byte) error {
	w, h := dst.Rect.Dx(), dst.Rect.Dy()
	if w <= 0 || h <= 0 {
		return fmt.Errorf("%w: an empty rectangle", ErrInvalid)
	}
	if len(payload) != 4*w*h {
		return fmt.Errorf("%w: raw payload of %d bytes for %d×%d pixels", ErrInvalid, len(payload), w, h)
	}
	for y := range h {
		o := dst.PixOffset(dst.Rect.Min.X, dst.Rect.Min.Y+y)
		copy(dst.Pix[o:o+4*w], payload[4*w*y:4*w*(y+1)])
	}
	return nil
}
