// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package stream

import "image"

// grid is the tiling of a screen: tile i is in row i / cols and column
// i % cols, and tiles at the right and bottom edges may be smaller.
type grid struct {
	bounds image.Rectangle
	tile   int
	cols   int
}

func (g grid) rect(i int) image.Rectangle {
	x, y := i%g.cols*g.tile, i/g.cols*g.tile
	return image.Rect(x, y, x+g.tile, y+g.tile).Intersect(g.bounds)
}

// dirty appends to dst the tiles that changed since they were last sent,
// versions[i] > sent[i], and that show in viewport; an empty viewport is
// the whole screen. If after is nonzero, the tiles changed after it,
// versions[i] > after, come first: they show the effect of the input
// that the clock value after was read for. Within each group tiles keep
// their order, row by row. It reports how many tiles changed after.
func dirty(dst []int, g grid, versions, sent []uint64, viewport image.Rectangle, after uint64) (tiles []int, fresh int) {
	tiles = dst[:0]
	show := func(i int) bool {
		return viewport.Empty() || g.rect(i).Overlaps(viewport)
	}
	if after != 0 {
		for i, v := range versions {
			if v > sent[i] && v > after && show(i) {
				tiles = append(tiles, i)
			}
		}
		fresh = len(tiles)
	}
	for i, v := range versions {
		if v > sent[i] && (after == 0 || v <= after) && show(i) {
			tiles = append(tiles, i)
		}
	}
	return tiles, fresh
}
