// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package stream

import (
	"cmp"
	"image"
	"slices"
)

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
// the whole screen. If after is nonzero, the tiles that changed after it,
// versions[i] > after, and were still before it, !active[i], come first:
// they show the effect of the input that the clock value after was read
// for. A nil active marks no tile. Within each group tiles keep their
// order, row by row. It reports how many tiles show the effect.
func dirty(dst []int, g grid, versions, sent []uint64, viewport image.Rectangle, after uint64, active []bool) (tiles []int, fresh int) {
	tiles = dst[:0]
	show := func(i int) bool {
		return viewport.Empty() || g.rect(i).Overlaps(viewport)
	}
	if len(active) != len(versions) {
		active = nil
	}
	effect := func(i int) bool {
		return after != 0 && versions[i] > after && (active == nil || !active[i])
	}
	if after != 0 {
		for i, v := range versions {
			if v > sent[i] && effect(i) && show(i) {
				tiles = append(tiles, i)
			}
		}
		fresh = len(tiles)
	}
	for i, v := range versions {
		if v > sent[i] && !effect(i) && show(i) {
			tiles = append(tiles, i)
		}
	}
	return tiles, fresh
}

// byDamage orders tiles that all changed after an input so that the
// smallest damage goes first. Tiles that share a version were changed by
// one drawing operation, and an input's effect is usually a small one, a
// character, a caret or a button, where motion repaints large areas at
// once. Among operations of one size the earlier goes first.
func byDamage(tiles []int, versions []uint64) {
	count := make(map[uint64]int, len(tiles))
	for _, t := range tiles {
		count[versions[t]]++
	}
	slices.SortStableFunc(tiles, func(a, b int) int {
		va, vb := versions[a], versions[b]
		return cmp.Or(cmp.Compare(count[va], count[vb]), cmp.Compare(va, vb))
	})
}

// opTiles is the number of tiles from order[i] on that share its
// version, the rest of one drawing operation once byDamage ordered them.
func opTiles(order []int, i int, versions []uint64) int {
	n := 1
	for i+n < len(order) && versions[order[i+n]] == versions[order[i]] {
		n++
	}
	return n
}
