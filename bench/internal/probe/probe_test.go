// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package probe_test

import (
	"image"
	"image/color"
	"image/draw"
	"testing"

	"changkun.de/x/occamy/bench/internal/probe"
)

func TestColourChangesBetweenLetters(t *testing.T) {
	// The harness presses a to z in turn, and the scene starts each probe
	// from z, so every key must change the swatch.
	keys := []rune("zabcdefghijklmnopqrstuvwxyza")
	for i := 1; i < len(keys); i++ {
		a, b := probe.Colour(keys[i-1]), probe.Colour(keys[i])
		if probe.Match(a, keys[i]) || probe.Match(b, keys[i-1]) {
			t.Errorf("%c and %c paint colours that match each other: %v, %v", keys[i-1], keys[i], a, b)
		}
	}
	// y and z alternate when the harness waits for the scene.
	if probe.Match(probe.Colour('y'), 'z') {
		t.Error("y and z paint matching colours")
	}
}

func TestMatchTolerance(t *testing.T) {
	want := probe.Colour('a')
	blurred := color.RGBA{R: sat(want.R, -30), G: sat(want.G, 30), B: sat(want.B, -30), A: 0xff}
	if !probe.Match(blurred, 'a') {
		t.Errorf("%v does not match %v within the tolerance", blurred, want)
	}
	if probe.Match(color.RGBA{0x40, 0x40, 0x40, 0xff}, 'a') {
		t.Error("the grey swatch of a new scene matches a")
	}
	if !probe.Centre.In(probe.Swatch.Inset(8)) {
		t.Error("the centre is near the swatch's edge, where encoding blurs it")
	}
}

func sat(v uint8, d int) uint8 { return uint8(min(255, max(0, int(v)+d))) }

// stamped returns a screen that shows frame n's stamp, its cells drawn in
// black and white, or in the given colours.
func stamped(n uint16, black, white color.RGBA) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, 1024, 768))
	draw.Draw(img, img.Bounds(), image.NewUniform(probe.Wallpaper(300, 768)), image.Point{}, draw.Src)
	v := probe.StampValue(n)
	for i := range 24 {
		c := black
		if v&(1<<(23-i)) != 0 {
			c = white
		}
		draw.Draw(img, probe.StampCell(i), image.NewUniform(c), image.Point{}, draw.Src)
	}
	return img
}

var (
	black = color.RGBA{0, 0, 0, 0xff}
	white = color.RGBA{0xff, 0xff, 0xff, 0xff}
)

// Every frame's number reads back, in the colours an encoder leaves.
func TestStampRoundTrip(t *testing.T) {
	for _, n := range []uint16{0, 1, 2, 255, 256, 0x5a5a, 0xfffe, 0xffff} {
		for _, cs := range [][2]color.RGBA{
			{black, white},
			{{0x30, 0x30, 0x30, 0xff}, {0xd0, 0xd0, 0xd0, 0xff}},
		} {
			got, ok := probe.ReadStamp(stamped(n, cs[0], cs[1]))
			if !ok || got != n {
				t.Errorf("frame %d in %v: read %d, %v", n, cs, got, ok)
			}
		}
	}
}

// Consecutive frames differ, or the harness would count them as one.
func TestStampChangesEveryFrame(t *testing.T) {
	for n := range 1 << 16 {
		if probe.StampValue(uint16(n)) == probe.StampValue(uint16(n+1)) {
			t.Fatalf("frames %d and %d have the same stamp", n, n+1)
		}
	}
}

// What the margin shows without a stamp is no frame: every row of the
// wallpaper, and any one colour. The check is what tells an area of one
// colour from frame 0 or frame 0xffff.
func TestStampRejects(t *testing.T) {
	plain := func(c color.RGBA) *image.RGBA {
		img := image.NewRGBA(image.Rect(0, 0, 1024, 768))
		draw.Draw(img, img.Bounds(), image.NewUniform(c), image.Point{}, draw.Src)
		return img
	}
	wallpaper := image.NewRGBA(image.Rect(0, 0, 1024, 768))
	for y := range 768 {
		for x := range 1024 {
			wallpaper.SetRGBA(x, y, probe.Wallpaper(y, 768))
		}
	}
	for name, img := range map[string]*image.RGBA{
		"the wallpaper":  wallpaper,
		"black":          plain(black),
		"white":          plain(white),
		"grey":           plain(color.RGBA{0x80, 0x80, 0x80, 0xff}),
		"no framebuffer": image.NewRGBA(image.Rect(0, 0, 1024, 768)),
	} {
		if n, ok := probe.ReadStamp(img); ok {
			t.Errorf("%s reads as frame %d", name, n)
		}
	}
	// A frame that drew only part of the next stamp, as when a server
	// cuts one of the scene's frames in two.
	for n := range uint16(512) {
		a, b := stamped(n, black, white), stamped(n+1, black, white)
		half := image.Rect(0, 0, 1024, 256)
		draw.Draw(a, half, b, half.Min, draw.Src)
		if got, ok := probe.ReadStamp(a); ok && got != n && got != n+1 {
			t.Fatalf("frames %d and %d mixed read as frame %d", n, n+1, got)
		}
	}
	if _, ok := probe.ReadStamp(image.NewRGBA(image.Rect(0, 0, 64, 48))); ok {
		t.Error("a screen too small for a stamp shows one")
	}
}

// The stamp stays in the margin, off the swatch, on the bench's screen.
func TestStampPlace(t *testing.T) {
	const margin = 64
	if !probe.StampArea.In(image.Rect(0, 0, margin, 768)) {
		t.Errorf("the stamp's area %v leaves the margin", probe.StampArea)
	}
	if probe.StampArea.Overlaps(probe.Swatch) {
		t.Errorf("the stamp's area %v covers the swatch %v", probe.StampArea, probe.Swatch)
	}
	for i := range 24 {
		for j := range i {
			if probe.StampCell(i).Overlaps(probe.StampCell(j)) {
				t.Errorf("cells %d and %d overlap", i, j)
			}
		}
		if !probe.StampCell(i).In(probe.StampArea) {
			t.Errorf("cell %d, %v, is outside the area", i, probe.StampCell(i))
		}
	}
}

func TestWallpaper(t *testing.T) {
	if got, want := probe.Wallpaper(0, 768), (color.RGBA{0x1f, 0x3b, 0x5a, 0xff}); got != want {
		t.Errorf("top %v, want %v", got, want)
	}
	if got, want := probe.Wallpaper(767, 768), (color.RGBA{0x3c, 0x6e, 0x8f, 0xff}); got != want {
		t.Errorf("bottom %v, want %v", got, want)
	}
	// As the scene has always drawn it, rounded down: these are the pixels
	// of G1's and G6's baselines, which were not measured again.
	if got, want := probe.Wallpaper(384, 768), (color.RGBA{0x2d, 0x54, 0x74, 0xff}); got != want {
		t.Errorf("middle %v, want %v", got, want)
	}
	// What a session sees before the desktop arrives is not the wallpaper.
	for _, c := range []color.RGBA{{}, black, white} {
		if probe.Near(c, probe.Wallpaper(384, 768)) {
			t.Errorf("%v matches the wallpaper", c)
		}
	}
}
