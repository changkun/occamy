// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package protocol

import "fmt"

// The factors a Scaled image may be scaled up by.
const (
	MinScale = 2
	MaxScale = 4
)

// Scaled is the payload of a CodecScaled tile: an image of the tile at
// 1/Factor of its width and height, rounded up (ScaledSize), in Codec.
// The client scales it up by Factor, bilinearly, from the tile's top
// left corner, and draws what falls on the tile.
type Scaled struct {
	// Factor is from MinScale to MaxScale.
	Factor uint8
	// Codec is CodecJPEG or CodecWebP, and one the client agreed to.
	Codec Codec
	// Image is the payload of Codec. It aliases the tile's payload.
	Image []byte
}

// Append appends the payload to b.
func (s Scaled) Append(b []byte) []byte {
	return append(append(b, s.Factor, byte(s.Codec)), s.Image...)
}

// ParseScaled reads the payload of a CodecScaled tile. It fails with
// ErrInvalid if the payload has no factor and codec, or a factor or
// codec that Scaled does not allow; it does not look at the image.
func ParseScaled(p []byte) (Scaled, error) {
	if len(p) < 2 {
		return Scaled{}, fmt.Errorf("%w: scaled payload of %d bytes", ErrInvalid, len(p))
	}
	s := Scaled{Factor: p[0], Codec: Codec(p[1]), Image: p[2:]}
	if s.Factor < MinScale || s.Factor > MaxScale {
		return Scaled{}, fmt.Errorf("%w: scaled by %d", ErrInvalid, s.Factor)
	}
	if s.Codec != CodecJPEG && s.Codec != CodecWebP {
		return Scaled{}, fmt.Errorf("%w: scaled image of codec %v", ErrInvalid, s.Codec)
	}
	return s, nil
}

// ScaledSize returns the size of the image of a w by h tile scaled by
// factor: w and h divided by factor, rounded up.
func ScaledSize(w, h, factor int) (int, int) {
	return (w + factor - 1) / factor, (h + factor - 1) / factor
}
