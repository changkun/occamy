// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package rdp

import (
	"fmt"
	"image"
	"image/color"
	"sync"
)

// screen is an Output that keeps the desktop in an image, for tests.
type screen struct {
	mu      sync.Mutex // held between Lock and Unlock
	img     *image.RGBA
	resizes int
	cursor  *image.RGBA
	hot     image.Point
	pos     image.Point
	draws   int
}

func (s *screen) Resize(w, h int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.img = image.NewRGBA(image.Rect(0, 0, w, h))
	s.resizes++
}

func (s *screen) Lock(r image.Rectangle) *image.RGBA {
	s.mu.Lock()
	if s.img == nil || r.Empty() || !r.In(s.img.Rect) {
		s.mu.Unlock()
		panic(fmt.Sprintf("screen: Lock(%v) outside %v", r, s.img.Rect))
	}
	return s.img.SubImage(r).(*image.RGBA)
}

func (s *screen) Unlock(image.Rectangle) {
	s.draws++
	s.mu.Unlock()
}

func (s *screen) Cursor(img *image.RGBA, hot image.Point) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cursor, s.hot = img, hot
}

func (s *screen) CursorMove(p image.Point) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pos = p
}

// at returns the pixel at (x, y).
func (s *screen) at(x, y int) color.RGBA {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.img == nil {
		return color.RGBA{}
	}
	return s.img.RGBAAt(x, y)
}

// snapshot returns a copy of the desktop.
func (s *screen) snapshot() *image.RGBA {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.img == nil {
		return nil
	}
	c := image.NewRGBA(s.img.Rect)
	copy(c.Pix, s.img.Pix)
	return c
}
