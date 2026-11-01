// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// Package probe is what the scene program and the harness agree on: where
// the latency swatch is, which colour each key paints it, what the
// wallpaper shows, and how a frame of the scene is numbered.
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
	return Near(c, Colour(k))
}

// Near reports whether c shows want, after what encoding does to a colour.
func Near(c, want color.RGBA) bool {
	return near(c.R, want.R) && near(c.G, want.G) && near(c.B, want.B)
}

func near(a, b uint8) bool {
	d := int(a) - int(b)
	return -tolerance <= d && d <= tolerance
}

// Wallpaper is the colour of row y of the wallpaper on a screen h rows
// high: a vertical gradient, as plain as a desktop background.
func Wallpaper(y, h int) color.RGBA {
	top, bottom := color.RGBA{0x1f, 0x3b, 0x5a, 0xff}, color.RGBA{0x3c, 0x6e, 0x8f, 0xff}
	t := float64(y) / float64(max(1, h-1))
	mix := func(a, b uint8) uint8 { return uint8(float64(a) + (float64(b)-float64(a))*t) }
	return color.RGBA{mix(top.R, bottom.R), mix(top.G, bottom.G), mix(top.B, bottom.B), 0xff}
}

// The stamp numbers a frame of an animated scene, so that the harness
// counts the frames of the scene that reached it, in any protocol and
// however the server cut them into frames of its own. It is a column of
// black and white cells in the margin, big enough to survive lossy
// encoding: a 16-bit frame number, most significant bit first, followed
// by an 8-bit check.
const (
	stampX, stampY = 8, 64
	stampW, stampH = 24, 32
	stampColumns   = 2
	stampBits      = 24
)

// StampArea is the part of the margin that the stamp takes.
var StampArea = image.Rect(stampX, stampY, stampX+stampColumns*stampW, stampY+stampBits/stampColumns*stampH)

// StampCell is the rectangle of bit i of the stamp, counted from the
// first.
func StampCell(i int) image.Rectangle {
	x, y := stampX+i%stampColumns*stampW, stampY+i/stampColumns*stampH
	return image.Rect(x, y, x+stampW, y+stampH)
}

// StampValue is frame n's cells, one bit each, the first cell in the
// highest bit of the 24. A set bit is white.
func StampValue(n uint16) uint32 {
	return uint32(n)<<8 | uint32(stampCheck(n))
}

// stampCheck is not 0 for frame 0 nor 0xff for frame 0xffff, so that an
// area of one colour, such as the wallpaper, reads as no stamp.
func stampCheck(n uint16) uint8 {
	hi, lo := uint8(n>>8), uint8(n) // #nosec G115 -- the two bytes of n
	return hi*31 ^ lo*17 ^ 0x5a
}

// The bounds of a cell's brightness: darker reads as black, lighter as
// white, and between them it is no cell of a stamp.
const stampBlack, stampWhite = 64, 192

// ReadStamp returns the number that img's stamp shows. ok is false if
// there is no whole stamp, as on a frame that drew part of one.
func ReadStamp(img *image.RGBA) (n uint16, ok bool) {
	if !StampArea.In(img.Bounds()) {
		return 0, false
	}
	var v uint32
	for i := range stampBits {
		b := brightness(img, StampCell(i))
		if stampBlack <= b && b <= stampWhite {
			return 0, false
		}
		v <<= 1
		if b > stampWhite {
			v |= 1
		}
	}
	n = uint16(v >> 8)                       // #nosec G115 -- 16 bits of 24
	return n, uint8(v&0xff) == stampCheck(n) // #nosec G115 -- 8 bits
}

// brightness is the mean brightness of the middle of r, away from the
// ringing that lossy encoding leaves at its edges.
func brightness(img *image.RGBA, r image.Rectangle) int {
	r = r.Inset(8)
	sum, pixels := 0, 0
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			c := img.RGBAAt(x, y)
			sum += (299*int(c.R) + 587*int(c.G) + 114*int(c.B)) / 1000
			pixels++
		}
	}
	return sum / pixels
}
