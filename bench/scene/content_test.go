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

// A stamp repaints only the cells that change, and all of them after a
// redraw cleared it: the scene's stamp must end up showing the frame,
// whatever cells it skipped.
func TestStampChanges(t *testing.T) {
	var shown [24]bool
	apply := func(was, now uint32) int {
		bits := stampChanges(was, now)
		for _, i := range bits {
			shown[i] = now&(1<<(23-i)) != 0
		}
		return len(bits)
	}
	if n := apply(noStamp, probe.StampValue(0)); n != 24 {
		t.Errorf("a first stamp repaints %d cells, want all 24", n)
	}
	most := 0
	for f := range 1000 {
		was, now := probe.StampValue(uint16(f)), probe.StampValue(uint16(f+1))
		most = max(most, apply(was, now))
		for i, on := range shown {
			if on != (now&(1<<(23-i)) != 0) {
				t.Fatalf("after frame %d, cell %d shows %v", f+1, i, on)
			}
		}
	}
	if most == 24 {
		t.Error("every frame repaints the whole stamp")
	}
}

// A stamp's colours read back as its bits, and frame numbers in a screen
// drawn by withStamp read back as the harness reads them.
func TestWithStamp(t *testing.T) {
	d := &display{stamps: true, stamped: noStamp, frame: 12345}
	img := d.withStamp(newPlasma(1024, 768).frame(3))
	if n, ok := probe.ReadStamp(img); !ok || n != 12345 {
		t.Errorf("read frame %d, %v, want 12345", n, ok)
	}
	if d.stamped != probe.StampValue(12345) {
		t.Errorf("the display records %#x as drawn", d.stamped)
	}
	// A screen too small for a stamp, as TestEncodings' 64×48, shows none
	// and keeps its pixels.
	small := newPlasma(64, 48).frame(3)
	want := bytes.Clone(small.Pix)
	d = &display{stamps: probe.StampArea.In(small.Bounds()), stamped: noStamp}
	if !bytes.Equal(d.withStamp(small).Pix, want) {
		t.Error("a screen too small for the stamp was drawn on")
	}
}

// The dragged window stays on the screen and off the swatch and the
// stamp, and what each of its steps clears behind it (dragArea) covers
// everywhere it goes and neither of them: clearing the stamp on every
// step would hide it from every frame.
func TestDragStaysClear(t *testing.T) {
	const w, h = 1024, 768
	screen := image.Rect(0, 0, w, h)
	area := dragArea(w, h)
	if area.Overlaps(probe.Swatch) || area.Overlaps(probe.StampArea) {
		t.Fatalf("a step of the drag clears %v, over the swatch or the stamp", area)
	}
	for ms := 0; ms < 16000; ms += 17 {
		p := dragPosition(time.Duration(ms)*time.Millisecond, w, h)
		r := image.Rect(p.X, p.Y, p.X+400, p.Y+300)
		if !r.In(screen) {
			t.Fatalf("at %d ms the window is at %v, off the screen", ms, r)
		}
		if r.Overlaps(probe.Swatch) || r.Overlaps(probe.StampArea) || p.X < margin {
			t.Fatalf("at %d ms the window at %v enters the margin", ms, r)
		}
		if !r.In(area) {
			t.Fatalf("at %d ms the window at %v leaves what a step clears, %v", ms, r, area)
		}
	}
}
