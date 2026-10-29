// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// Package probe is what the scene program and the harness agree on: where
// the latency swatch is, and which colour each key paints it.
package probe

import (
	"image"
	"image/color"
)

// Swatch is the area the scene fills when it receives a probe key. It is
// clear of everything that moves in every scene.
var Swatch = image.Rect(16, 16, 48, 48)

// Centre is the pixel the harness checks.
var Centre = image.Pt(32, 32)

// palette has saturated colours that stay distinct after lossy encoding
// and 16-bit RDP colour.
var palette = []color.RGBA{
	{0xff, 0x00, 0x00, 0xff},
	{0x00, 0xff, 0x00, 0xff},
	{0x00, 0x00, 0xff, 0xff},
	{0xff, 0xff, 0x00, 0xff},
	{0x00, 0xff, 0xff, 0xff},
	{0xff, 0x00, 0xff, 0xff},
	{0xff, 0x80, 0x00, 0xff},
	{0x80, 0x00, 0xff, 0xff},
}

// Colour is the colour the letter k paints the swatch. Letters that
// follow each other have different colours.
func Colour(k rune) color.RGBA { return palette[int(k)%len(palette)] }

// tolerance is how far a channel may be from the colour the key painted:
// JPEG and WebP blur it, and 16-bit colour truncates it.
const tolerance = 40

// Match reports whether c shows the colour the letter k paints.
func Match(c color.RGBA, k rune) bool {
	want := Colour(k)
	return near(c.R, want.R) && near(c.G, want.G) && near(c.B, want.B)
}

func near(a, b uint8) bool {
	d := int(a) - int(b)
	return -tolerance <= d && d <= tolerance
}
