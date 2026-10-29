// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package probe_test

import (
	"image/color"
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
