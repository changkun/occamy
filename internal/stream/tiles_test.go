// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package stream

import (
	"image"
	"slices"
	"testing"
)

func TestDirty(t *testing.T) {
	// A 3x2 grid of 64-pixel tiles over 160x100: the right column is 32
	// wide and the bottom row 36 high.
	g := grid{bounds: image.Rect(0, 0, 160, 100), tile: 64, cols: 3}
	versions := []uint64{5, 1, 7, 2, 9, 3}
	sent := []uint64{5, 0, 0, 2, 0, 0}
	for _, c := range []struct {
		name     string
		viewport image.Rectangle
		after    uint64
		want     []int
		fresh    int
	}{
		{"all", image.Rectangle{}, 0, []int{1, 2, 4, 5}, 0},
		{"after input", image.Rectangle{}, 6, []int{2, 4, 1, 5}, 2},
		{"input with no effect yet", image.Rectangle{}, 9, []int{1, 2, 4, 5}, 0},
		{"viewport", image.Rect(0, 0, 100, 50), 0, []int{1}, 0},
		{"viewport edge", image.Rect(128, 64, 160, 100), 0, []int{5}, 0},
		{"viewport after input", image.Rect(0, 0, 100, 100), 6, []int{4, 1}, 1},
	} {
		got, fresh := dirty(nil, g, versions, sent, c.viewport, c.after)
		if !slices.Equal(got, c.want) || fresh != c.fresh {
			t.Errorf("%s: tiles %v fresh %d, want %v fresh %d", c.name, got, fresh, c.want, c.fresh)
		}
	}
	if r := g.rect(5); r != image.Rect(128, 64, 160, 100) {
		t.Errorf("corner tile %v", r)
	}
}
