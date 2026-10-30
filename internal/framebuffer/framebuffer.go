// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// Package framebuffer holds a session's screen: RGBA pixels divided into
// square tiles, which a backend draws into while viewers' writers read
// them (specs/008-native-architecture/graphics-pipeline.md).
//
// Every change to a tile gives it a new version from one counter for the
// whole screen. A writer finds the tiles a viewer has not seen by
// comparing their versions with those it last sent, and the tiles changed
// since an input by comparing them with the counter's value when the
// input was applied.
//
// Each row of tiles has its own lock, so that a backend drawing one part
// of the screen and writers reading another do not wait for each other.
package framebuffer

import (
	"fmt"
	"image"
	"image/draw"
	"sync"
	"sync/atomic"
)

// Framebuffer is a session's screen. Its methods are safe for concurrent
// use, but a goroutine that holds a Lock calls no other method until it
// unlocks.
type Framebuffer struct {
	tile int

	mu       sync.RWMutex // held for writing by Resize only
	img      *image.RGBA
	cols     int
	rows     int
	rowLocks []sync.RWMutex
	versions []atomic.Uint64
	clock    atomic.Uint64

	wmu     sync.Mutex
	changed chan struct{} // closed at the next change, nil if nobody waits
}

// New returns an empty framebuffer with square tiles of the given side
// in pixels. Resize gives it a size.
func New(tile int) *Framebuffer {
	if tile <= 0 {
		panic(fmt.Sprintf("framebuffer: tile size %d", tile))
	}
	return &Framebuffer{tile: tile, img: &image.RGBA{}}
}

// Resize discards the screen for a black one of the given size, every
// tile at a new version.
func (f *Framebuffer) Resize(width, height int) {
	if width <= 0 || height <= 0 {
		panic(fmt.Sprintf("framebuffer: Resize(%d, %d)", width, height))
	}
	f.mu.Lock()
	f.img = image.NewRGBA(image.Rect(0, 0, width, height))
	draw.Draw(f.img, f.img.Rect, image.Black, image.Point{}, draw.Src)
	f.cols = (width + f.tile - 1) / f.tile
	f.rows = (height + f.tile - 1) / f.tile
	f.rowLocks = make([]sync.RWMutex, f.rows)
	f.versions = make([]atomic.Uint64, f.cols*f.rows)
	v := f.clock.Add(1)
	for i := range f.versions {
		f.versions[i].Store(v)
	}
	f.mu.Unlock()
	f.notify()
}

// Bounds is the screen's rectangle, empty before Resize.
func (f *Framebuffer) Bounds() image.Rectangle {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.img.Rect
}

// TileSize is the side of a tile in pixels.
func (f *Framebuffer) TileSize() int { return f.tile }

// Grid is the number of columns and rows of tiles. Tile i is in row
// i / cols and column i % cols.
func (f *Framebuffer) Grid() (cols, rows int) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.cols, f.rows
}

// Tile is tile i's rectangle on the screen; tiles at the right and bottom
// edges may be smaller than the tile size. It is empty if i is outside
// the grid, as after a Resize.
func (f *Framebuffer) Tile(i int) image.Rectangle {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.tileRect(i)
}

func (f *Framebuffer) tileRect(i int) image.Rectangle {
	if i < 0 || i >= len(f.versions) {
		return image.Rectangle{}
	}
	x, y := i%f.cols*f.tile, i/f.cols*f.tile
	return image.Rect(x, y, x+f.tile, y+f.tile).Intersect(f.img.Rect)
}

// Clock is the latest version given to any tile.
func (f *Framebuffer) Clock() uint64 { return f.clock.Load() }

// Version is tile i's version, or 0 if i is outside the grid.
func (f *Framebuffer) Version(i int) uint64 {
	f.mu.RLock()
	defer f.mu.RUnlock()
	if i < 0 || i >= len(f.versions) {
		return 0
	}
	return f.versions[i].Load()
}

// Versions appends every tile's version to dst, in tile order, and
// returns it.
func (f *Framebuffer) Versions(dst []uint64) []uint64 {
	f.mu.RLock()
	defer f.mu.RUnlock()
	for i := range f.versions {
		dst = append(dst, f.versions[i].Load())
	}
	return dst
}

// Changed returns a channel that is closed at the next change to the
// screen. Call it before checking versions, so that a change between the
// check and the wait is not missed.
func (f *Framebuffer) Changed() <-chan struct{} {
	f.wmu.Lock()
	defer f.wmu.Unlock()
	if f.changed == nil {
		f.changed = make(chan struct{})
	}
	return f.changed
}

func (f *Framebuffer) notify() {
	f.wmu.Lock()
	if f.changed != nil {
		close(f.changed)
		f.changed = nil
	}
	f.wmu.Unlock()
}

// Lock returns the pixels in r, locked for drawing. r must be non-empty
// and lie within the screen; backends check rectangles from the remote
// server before they draw. Unlock with the same r releases the pixels and
// gives the tiles that r touches a new version.
func (f *Framebuffer) Lock(r image.Rectangle) *image.RGBA {
	f.mu.RLock()
	if r.Empty() || !r.In(f.img.Rect) {
		f.mu.RUnlock()
		panic(fmt.Sprintf("framebuffer: Lock(%v) outside the screen %v", r, f.img.Rect))
	}
	f.lockRows(r)
	return f.img.SubImage(r).(*image.RGBA)
}

// Unlock releases the pixels that Lock(r) returned and records r as
// changed.
func (f *Framebuffer) Unlock(r image.Rectangle) {
	f.touch(r)
	f.unlockRows(r)
	f.mu.RUnlock()
	f.notify()
}

// CopyRect copies the pixels of dst's size at src to dst, as when the
// remote scrolls or moves a window, and records dst as changed. Both
// rectangles must lie within the screen; they may overlap.
func (f *Framebuffer) CopyRect(dst image.Rectangle, src image.Point) {
	f.mu.RLock()
	from := dst.Sub(dst.Min).Add(src)
	if dst.Empty() || !dst.In(f.img.Rect) || !from.In(f.img.Rect) {
		f.mu.RUnlock()
		panic(fmt.Sprintf("framebuffer: CopyRect(%v, %v) outside the screen %v", dst, src, f.img.Rect))
	}
	both := dst.Union(from)
	f.lockRows(both)
	// draw.Draw copies overlapping rectangles of one image correctly.
	draw.Draw(f.img, dst, f.img, src, draw.Src)
	f.touch(dst)
	f.unlockRows(both)
	f.mu.RUnlock()
	f.notify()
}

// ReadTile copies tile i's pixels into dst, row by row at the tile's
// width, and returns its rectangle and the version the pixels belong to.
// dst must hold the tile: 4 bytes per pixel. If i is outside the grid,
// as after a Resize, it returns an empty rectangle and copies nothing.
func (f *Framebuffer) ReadTile(i int, dst []byte) (r image.Rectangle, version uint64) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	r = f.tileRect(i)
	if r.Empty() {
		return r, 0
	}
	row := &f.rowLocks[i/f.cols]
	row.RLock()
	defer row.RUnlock()
	w := 4 * r.Dx()
	for y := r.Min.Y; y < r.Max.Y; y++ {
		o := f.img.PixOffset(r.Min.X, y)
		copy(dst[(y-r.Min.Y)*w:(y-r.Min.Y+1)*w], f.img.Pix[o:o+w])
	}
	return r, f.versions[i].Load()
}

// lockRows locks the rows of tiles that r touches, top to bottom, which
// is the order every caller locks in. f.mu must be read-locked.
func (f *Framebuffer) lockRows(r image.Rectangle) {
	for row := r.Min.Y / f.tile; row <= (r.Max.Y-1)/f.tile; row++ {
		f.rowLocks[row].Lock()
	}
}

func (f *Framebuffer) unlockRows(r image.Rectangle) {
	for row := r.Min.Y / f.tile; row <= (r.Max.Y-1)/f.tile; row++ {
		f.rowLocks[row].Unlock()
	}
}

// touch gives the tiles that r touches a new version. Their rows must be
// locked.
func (f *Framebuffer) touch(r image.Rectangle) {
	v := f.clock.Add(1)
	for row := r.Min.Y / f.tile; row <= (r.Max.Y-1)/f.tile; row++ {
		for col := r.Min.X / f.tile; col <= (r.Max.X-1)/f.tile; col++ {
			f.versions[row*f.cols+col].Store(v)
		}
	}
}
