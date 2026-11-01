// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package rdp

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"slices"
	"sync"
	"unicode/utf8"
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
	copies  int
	// clipboard holds the clipboards the client reported.
	clipboard []string
}

// Resize panics on a size the client should have refused, so that the
// fuzzers notice.
func (s *screen) Resize(w, h int) {
	if err := checkDesktop(w, h); err != nil {
		panic(fmt.Sprintf("screen: Resize(%d, %d): %v", w, h, err))
	}
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

// CopyRect panics on rectangles off the desktop, so that the fuzzers
// notice, and copies as the framebuffer does, overlaps and all.
func (s *screen) CopyRect(dst image.Rectangle, src image.Point) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sr := image.Rectangle{Min: src, Max: src.Add(dst.Size())}
	if s.img == nil || dst.Empty() || !dst.In(s.img.Rect) || !sr.In(s.img.Rect) {
		panic(fmt.Sprintf("screen: CopyRect(%v, %v) outside %v", dst, src, s.img.Rect))
	}
	from := image.NewRGBA(sr)
	for y := sr.Min.Y; y < sr.Max.Y; y++ {
		copy(from.Pix[from.PixOffset(sr.Min.X, y):][:4*sr.Dx()], s.img.Pix[s.img.PixOffset(sr.Min.X, y):])
	}
	for y := 0; y < dst.Dy(); y++ {
		copy(s.img.Pix[s.img.PixOffset(dst.Min.X, dst.Min.Y+y):][:4*dst.Dx()], from.Pix[from.PixOffset(sr.Min.X, sr.Min.Y+y):])
	}
	s.copies++
}

// copyCount returns the copies the client made on the screen.
func (s *screen) copyCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.copies
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

// Clipboard panics on text the client should have bounded or converted,
// so that the fuzzers notice, and keeps the latest.
func (s *screen) Clipboard(mime string, data []byte) {
	if mime != "text/plain" || len(data) > MaxClipboard || !utf8.Valid(data) || bytes.ContainsRune(data, '\r') {
		panic(fmt.Sprintf("screen: Clipboard(%q) of %d bytes", mime, len(data)))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clipboard = append(s.clipboard, string(data))
}

// clipboards returns the clipboards the client reported.
func (s *screen) clipboards() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.clipboard)
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

// wallpaper is the scene's background in row y of a screen h pixels high,
// as bench/scene's newWallpaper draws it.
func wallpaper(y, h int) color.RGBA {
	top, bottom := color.RGBA{0x1f, 0x3b, 0x5a, 0xff}, color.RGBA{0x3c, 0x6e, 0x8f, 0xff}
	t := float64(y) / float64(max(1, h-1))
	f := func(a, b uint8) uint8 { return uint8(float64(a) + (float64(b)-float64(a))*t) }
	return color.RGBA{f(top.R, bottom.R), f(top.G, bottom.G), f(top.B, bottom.B), 0xff}
}

// matchesWallpaper counts the pixels outside the swatch that differ from
// the scene's wallpaper by more than tol in a channel.
func matchesWallpaper(img *image.RGBA, tol int) (bad int, first image.Point) {
	if img == nil {
		return -1, image.Point{}
	}
	w, h := img.Rect.Dx(), img.Rect.Dy()
	swatch := image.Rect(16, 16, 48, 48)
	for y := range h {
		want := wallpaper(y, h)
		for x := range w {
			if image.Pt(x, y).In(swatch) {
				continue
			}
			got := img.RGBAAt(x, y)
			if !near(got.R, want.R, tol) || !near(got.G, want.G, tol) || !near(got.B, want.B, tol) {
				if bad == 0 {
					first = image.Pt(x, y)
				}
				bad++
			}
		}
	}
	return bad, first
}

func near(a, b uint8, tol int) bool {
	d := int(a) - int(b)
	return -tol <= d && d <= tol
}
