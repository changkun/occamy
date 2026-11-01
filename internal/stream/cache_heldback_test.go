// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package stream

import (
	"image"
	"slices"
	"strings"
	"testing"
	"time"

	"changkun.de/x/occamy/internal/pacer"
)

// What the mirror records follows the messages written: a message of a
// frame that the pacer holds back stores nothing, so its tile goes again
// stored rather than cached from the slot it would have filled; and a
// cached tile held back does not make its slot the one used last.
func TestCacheHeldBack(t *testing.T) {
	d := newDirect(t, false)
	d.w.enc = &markEncoder{}
	d.w.cache = newTileCache(2)
	c := &tape{recorder: d.c}
	d.w.c = c
	s := newTileScreen(1024, 768, func(r image.Rectangle) *image.RGBA { return pattern(0, r.Min) })
	wide := d.w.p
	check := func(name string, want []string) {
		t.Helper()
		if got := c.tileNotes(); !slices.Equal(got, want) {
			t.Errorf("%s:\n%s\nwant\n%s", name, strings.Join(got, "\n"), strings.Join(want, "\n"))
		}
	}
	frame := func(name string, want ...string) {
		t.Helper()
		d.w.p = wide
		d.show(s.shot())
		d.frame()
		d.exact()
		check(name, want)
	}
	// held sends a frame that the pacer cuts after its first message: a
	// cap of one byte in flight lets only the first write of a frame go.
	held := func(name string, want ...string) {
		t.Helper()
		w := d.w
		w.p = pacer.New(pacer.Config{Window: 1 << 20, InitialCap: 1})
		d.show(s.shot())
		if _, ok := w.collect(image.Rectangle{}); !ok {
			t.Fatal("resized")
		}
		if ok, _ := w.p.StartFrame(time.Now()); !ok {
			t.Fatal("the pacer holds the frame")
		}
		if err := w.sendFrame(d.ctx, w.order, false); err != nil {
			t.Fatal(err)
		}
		check(name, want)
	}
	d.show(s.shot())
	d.frame()
	c.tileNotes()

	s.set(0, 0, pattern(1, image.Pt(0, 0)))
	s.set(1, 0, pattern(2, image.Pt(64, 0)))
	held("a frame cut before a tile it would store", "tile 0,0 64x64 raw, stored in slot 2")
	frame("the tile it held back", "tile 64,0 64x64 raw, stored in slot 1")

	// Slot 1 is now the one used last. A cached tile of slot 1 goes, and
	// one of slot 2 is held back: slot 2 stays the one used least.
	s.set(2, 0, pattern(2, image.Pt(128, 0)))
	s.set(0, 2, pattern(1, image.Pt(0, 128)))
	held("a frame cut before a cached tile", "tile 128,0 64x64 cached from slot 1")
	if v := d.w.cache.victim(); v != 2 {
		t.Errorf("the next tile goes in slot %d, want 2, which the tile held back did not use", v)
	}
	frame("the cached tile it held back", "tile 0,128 64x64 cached from slot 2")

	// Slot 2 is the one used last, and a cached tile of it is held back.
	// The next message, which stores in slot 1, makes slot 1 the one
	// used last: nothing of the tile held back remains to mark slot 2.
	s.set(3, 0, pattern(1, image.Pt(192, 0)))
	s.set(4, 0, pattern(1, image.Pt(256, 0)))
	held("a frame cut before a cached tile of the slot used last", "tile 192,0 64x64 cached from slot 2")
	s.set(4, 0, pattern(3, image.Pt(256, 0)))
	s.set(5, 0, pattern(4, image.Pt(320, 0)))
	frame("two tiles stored after it",
		"tile 256,0 64x64 raw, stored in slot 1",
		"tile 320,0 64x64 raw, stored in slot 2")
}
