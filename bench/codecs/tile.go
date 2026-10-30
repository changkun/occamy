// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// Package codecs measures tile codecs for the graphics pipeline of
// specs/008-native-architecture/graphics-pipeline.md on the frames in
// bench/testdata/corpus. This file and its siblings hold the candidates
// that no library provides (the palette container, QOI), the colour
// count that classifies tiles, the swizzle between pixel layouts, and
// SSIM. The measurement itself is measure_test.go, built with the tag
// measure; docs/benchmarks/codecs.md says how to run it and what it
// found.
package codecs

import (
	"encoding/binary"
	"image"
)

// Palette is the distinct colours of a tile with each pixel's index into
// them, as the palette container and paletted PNG need.
type Palette struct {
	Colours []uint32 // 0xRRGGBB, in order of first appearance
	Index   []uint8  // one per pixel, rows from the top
	table   [512]uint32
	slot    [512]uint8
}

// Build fills p from img's pixels, ignoring alpha. It stops and reports
// false at the 257th distinct colour.
func (p *Palette) Build(img *image.RGBA) bool {
	p.Colours = p.Colours[:0]
	p.Index = p.Index[:0]
	clear(p.table[:])
	b := img.Rect
	for y := b.Min.Y; y < b.Max.Y; y++ {
		row := img.Pix[img.PixOffset(b.Min.X, y):img.PixOffset(b.Max.X, y)]
		last, lastIndex := uint32(1<<31), uint8(0)
		for i := 0; i < len(row); i += 4 {
			c := uint32(row[i])<<16 | uint32(row[i+1])<<8 | uint32(row[i+2])
			if c == last {
				p.Index = append(p.Index, lastIndex)
				continue
			}
			v, ok := p.lookup(c)
			if !ok {
				return false
			}
			p.Index = append(p.Index, v)
			last, lastIndex = c, v
		}
	}
	return true
}

// lookup finds or adds c in an open-addressed table, with zero meaning
// empty, so keys are stored plus one.
func (p *Palette) lookup(c uint32) (uint8, bool) {
	key := c + 1
	h := (c * 0x9e3779b1) >> 23
	for {
		switch p.table[h] {
		case key:
			return p.slot[h], true
		case 0:
			if len(p.Colours) == 256 {
				return 0, false
			}
			p.table[h] = key
			p.slot[h] = uint8(len(p.Colours)) // #nosec G115 -- fewer than 256 colours
			p.Colours = append(p.Colours, c)
			return p.slot[h], true
		}
		h = (h + 1) & 511
	}
}

// Colours counts img's distinct colours, ignoring alpha, up to 257: a
// result of 257 means more than 256. This is the pipeline's
// classification, with its early exit.
func Colours(img *image.RGBA, p *Palette) int {
	if !p.Build(img) {
		return 257
	}
	return len(p.Colours)
}

// Class is the reporting class of a tile with n colours as Colours
// counts them. The pipeline encodes the first four losslessly and the
// last lossily.
func Class(n int) string {
	switch {
	case n == 1:
		return "1"
	case n == 2:
		return "2"
	case n <= 16:
		return "3-16"
	case n <= 256:
		return "17-256"
	}
	return ">256"
}

// Classes are the classes in reporting order.
var Classes = []string{"1", "2", "3-16", "17-256", ">256"}

// Swizzle copies src to dst, swapping the first and third byte of every
// 4-byte pixel: RGBA to BGRA and back.
func Swizzle(dst, src []byte) {
	for i := 0; i+4 <= len(src); i += 4 {
		v := binary.LittleEndian.Uint32(src[i:])
		v = v&0xff00ff00 | v>>16&0xff | v&0xff<<16
		binary.LittleEndian.PutUint32(dst[i:], v)
	}
}
