// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package framebuffer_test

import (
	"image"
	"image/color"
	"image/draw"
	"sync"
	"testing"
	"time"

	"changkun.de/x/occamy/internal/framebuffer"
)

func fill(f *framebuffer.Framebuffer, r image.Rectangle, c color.RGBA) {
	img := f.Lock(r)
	draw.Draw(img, r, image.NewUniform(c), image.Point{}, draw.Src)
	f.Unlock(r)
}

func closed(c <-chan struct{}) bool {
	select {
	case <-c:
		return true
	default:
		return false
	}
}

func TestGrid(t *testing.T) {
	f := framebuffer.New(64)
	if got := f.Bounds(); !got.Empty() {
		t.Fatalf("bounds %v before Resize", got)
	}
	f.Resize(150, 70)
	if cols, rows := f.Grid(); cols != 3 || rows != 2 {
		t.Fatalf("grid %dx%d, want 3x2", cols, rows)
	}
	for i, want := range []image.Rectangle{
		image.Rect(0, 0, 64, 64),
		image.Rect(128, 0, 150, 64),
		image.Rect(64, 64, 128, 70),
		image.Rect(128, 64, 150, 70),
	} {
		idx := []int{0, 2, 4, 5}[i]
		if got := f.Tile(idx); got != want {
			t.Errorf("tile %d is %v, want %v", idx, got, want)
		}
	}
}

func TestVersions(t *testing.T) {
	f := framebuffer.New(64)
	changed := f.Changed()
	f.Resize(256, 128)
	if !closed(changed) {
		t.Error("Resize did not signal a change")
	}
	start := f.Clock()
	for i, v := range f.Versions(nil) {
		if v != start {
			t.Fatalf("tile %d at version %d after Resize, want %d", i, v, start)
		}
	}

	changed = f.Changed()
	if closed(changed) {
		t.Fatal("change signalled before any change")
	}
	// Spans tiles 1 and 2 of the first row, and 5 and 6 of the second.
	fill(f, image.Rect(100, 60, 140, 70), color.RGBA{R: 1, A: 0xff})
	if !closed(changed) {
		t.Error("Unlock did not signal a change")
	}
	v := f.Clock()
	if v <= start {
		t.Fatalf("clock %d did not advance from %d", v, start)
	}
	for i, got := range f.Versions(nil) {
		want := start
		if i == 1 || i == 2 || i == 5 || i == 6 {
			want = v
		}
		if got != want {
			t.Errorf("tile %d at version %d, want %d", i, got, want)
		}
	}

	f.CopyRect(image.Rect(0, 64, 64, 128), image.Pt(100, 60))
	if got := f.Version(4); got <= v {
		t.Errorf("CopyRect's destination tile at version %d, want above %d", got, v)
	}
	if got := f.Version(1); got != v {
		t.Errorf("CopyRect's source tile at version %d, want %d", got, v)
	}
}

func TestReadTile(t *testing.T) {
	f := framebuffer.New(4)
	f.Resize(6, 4)
	red := color.RGBA{R: 0xff, A: 0xff}
	fill(f, image.Rect(4, 0, 6, 4), red)
	buf := make([]byte, 4*4*4)
	r, v := f.ReadTile(1, buf)
	if r != image.Rect(4, 0, 6, 4) || v != f.Clock() {
		t.Fatalf("tile 1 is %v at version %d, want (4,0)-(6,4) at %d", r, v, f.Clock())
	}
	// The edge tile is two pixels wide, packed at its width.
	for p := range 2 * 4 {
		if got := (color.RGBA{buf[4*p], buf[4*p+1], buf[4*p+2], buf[4*p+3]}); got != red {
			t.Fatalf("pixel %d is %v, want red", p, got)
		}
	}
}

func TestCopyRectOverlapping(t *testing.T) {
	f := framebuffer.New(2)
	f.Resize(2, 6)
	for y := range 6 {
		fill(f, image.Rect(0, y, 2, y+1), color.RGBA{R: uint8(y), A: 0xff})
	}
	// Scroll up by two rows across tile rows.
	f.CopyRect(image.Rect(0, 0, 2, 4), image.Pt(0, 2))
	buf := make([]byte, 2*2*4)
	for i, want := range []uint8{2, 4, 4} {
		f.ReadTile(2*i, buf)
		if buf[0] != want {
			t.Errorf("tile row %d starts with row %d, want %d", i, buf[0], want)
		}
	}
}

func f0() *framebuffer.Framebuffer {
	f := framebuffer.New(2)
	f.Resize(4, 4)
	return f
}

func TestContract(t *testing.T) {
	for name, fn := range map[string]func(*framebuffer.Framebuffer){
		"lock before resize": func(f *framebuffer.Framebuffer) { f.Lock(image.Rect(0, 0, 1, 1)) },
		"lock outside":       func(f *framebuffer.Framebuffer) { f.Resize(4, 4); f.Lock(image.Rect(2, 2, 5, 5)) },
		"lock empty":         func(f *framebuffer.Framebuffer) { f.Resize(4, 4); f.Lock(image.Rectangle{}) },
		"copy from outside": func(f *framebuffer.Framebuffer) {
			f.Resize(4, 4)
			f.CopyRect(image.Rect(0, 0, 2, 2), image.Pt(3, 3))
		},
		"resize to zero": func(f *framebuffer.Framebuffer) { f.Resize(4, 0) },
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Error("no panic")
				}
			}()
			fn(framebuffer.New(2))
		})
	}
	if got := f0().Tile(99); !got.Empty() {
		t.Errorf("tile outside the grid is %v", got)
	}
	if r, v := f0().ReadTile(-1, nil); !r.Empty() || v != 0 {
		t.Errorf("ReadTile outside the grid returned %v at %d", r, v)
	}
	// A failed Lock leaves the framebuffer usable.
	f := framebuffer.New(2)
	f.Resize(4, 4)
	func() {
		defer func() { _ = recover() }()
		f.Lock(image.Rect(0, 0, 9, 9))
	}()
	f.Resize(8, 8)
	fill(f, image.Rect(0, 0, 8, 8), color.RGBA{A: 0xff})
}

// TestConcurrent draws, copies and resizes while readers read tiles, and
// checks that a reader never sees a tile half drawn.
func TestConcurrent(t *testing.T) {
	const tile = 8
	f := framebuffer.New(tile)
	f.Resize(64, 64)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for g := range 4 {
		wg.Go(func() {
			for n := 0; ; n++ {
				select {
				case <-stop:
					return
				default:
				}
				// Whole tiles in a uniform colour, sometimes two rows,
				// within the smaller of the two sizes the screen takes.
				x, y := (n*3+g)%7*tile, (n+g*2)%7*tile
				r := image.Rect(x, y, x+tile, y+tile*(1+n%2))
				fill(f, r, color.RGBA{R: uint8(n), G: uint8(g), A: 0xff})
				if n%5 == 0 {
					f.CopyRect(image.Rect(0, 0, tile, tile), image.Pt(x, y))
				}
			}
		})
	}
	for range 4 {
		wg.Go(func() {
			buf := make([]byte, tile*tile*4)
			for n := 0; ; n++ {
				select {
				case <-stop:
					return
				default:
				}
				r, _ := f.ReadTile(n%64, buf)
				px := 4 * r.Dx() * r.Dy()
				for p := 4; p < px; p += 4 {
					if buf[p] != buf[0] || buf[p+1] != buf[1] {
						t.Errorf("tile %v torn: pixel %d is %v, pixel 0 %v", r, p/4, buf[p:p+4], buf[:4])
						return
					}
				}
			}
		})
	}
	wg.Go(func() {
		for n := 0; ; n++ {
			select {
			case <-stop:
				return
			case <-time.After(time.Millisecond):
			}
			f.Resize(64-8*(n%2), 64)
		}
	})
	time.Sleep(200 * time.Millisecond)
	close(stop)
	wg.Wait()
}
