// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"image"
	"testing"
	"time"

	"changkun.de/x/occamy/bench/internal/probe"
)

// Every run must show the same pixels, or runs are not comparable.
func TestContentIsDeterministic(t *testing.T) {
	if !bytes.Equal(newPhoto(64, 48, 1).Pix, newPhoto(64, 48, 1).Pix) {
		t.Error("the photo differs between two renderings")
	}
	if bytes.Equal(newPhoto(64, 48, 1).Pix, newPhoto(64, 48, 2).Pix) {
		t.Error("the photo does not depend on its seed")
	}
	a, b := newTextPage(200, 40), newTextPage(200, 40)
	if !bytes.Equal(a.page().Pix, b.page().Pix) || !bytes.Equal(a.next().Pix, b.next().Pix) {
		t.Error("the text differs between two pages")
	}
	p, q := newPlasma(32, 32), newPlasma(32, 32)
	if !bytes.Equal(p.frame(7).Pix, q.frame(7).Pix) {
		t.Error("a motion frame differs between two renderings")
	}
	f7 := bytes.Clone(p.frame(7).Pix)
	if bytes.Equal(f7, p.frame(8).Pix) {
		t.Error("consecutive motion frames are the same")
	}
}

// The dragged window stays on the screen and off the swatch.
func TestDragStaysClear(t *testing.T) {
	const w, h = 1024, 768
	screen := image.Rect(0, 0, w, h)
	for ms := 0; ms < 16000; ms += 17 {
		p := dragPosition(time.Duration(ms)*time.Millisecond, w, h)
		r := image.Rect(p.X, p.Y, p.X+400, p.Y+300)
		if !r.In(screen) {
			t.Fatalf("at %d ms the window is at %v, off the screen", ms, r)
		}
		if r.Overlaps(probe.Swatch) || p.X < margin {
			t.Fatalf("at %d ms the window at %v enters the margin", ms, r)
		}
	}
}
