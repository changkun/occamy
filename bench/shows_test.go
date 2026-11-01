// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"image"
	"image/color"
	"image/draw"
	"io"
	"strconv"
	"testing"
	"time"

	"changkun.de/x/occamy/bench/internal/probe"
	"changkun.de/x/occamy/internal/config"
	"changkun.de/x/occamy/internal/guacamole"
	"changkun.de/x/occamy/internal/terminal"
)

var (
	wholeScreen = image.Rect(0, 0, width, height)
	white       = color.RGBA{0xff, 0xff, 0xff, 0xff}
)

// eachStampCell calls f with the rectangle and the colour of cells from
// up to to of frame n's stamp.
func eachStampCell(n uint16, from, to int, f func(image.Rectangle, color.RGBA)) {
	v := probe.StampValue(n)
	for i := from; i < to; i++ {
		c := gridBlank
		if v>>(23-i)&1 == 1 {
			c = white
		}
		f(probe.StampCell(i), c)
	}
}

// paintStamp paints frame n's stamp, as the scene does.
func paintStamp(img draw.Image, n uint16) {
	eachStampCell(n, 0, 24, func(r image.Rectangle, c color.RGBA) {
		draw.Draw(img, r, image.NewUniform(c), image.Point{}, draw.Src)
	})
}

// paintWallpaper paints the wallpaper, as the scene does.
func paintWallpaper(img *image.RGBA) {
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		c := probe.Wallpaper(y, height)
		for x := b.Min.X; x < b.Max.X; x++ {
			img.SetRGBA(x, y, c)
		}
	}
}

// A stamp counts once, however many frames carry it, and only when
// whole: a frame that drew part of one reads as none, and the next one
// that completes it counts. A frame number that comes back is a new
// frame of the scene.
func TestCountStamp(t *testing.T) {
	img := image.NewRGBA(wholeScreen)
	s := newSession(time.Now())
	check := func(when string, want int64) {
		t.Helper()
		s.countStamp(img)
		if got := s.sceneFrames.Load(); got != want {
			t.Errorf("%s: %d frames of the scene, want %d", when, got, want)
		}
	}
	check("no stamp", 0)
	paintStamp(img, 0)
	check("frame 0", 1)
	check("frame 0 again", 1)
	eachStampCell(1, 0, 12, func(r image.Rectangle, c color.RGBA) {
		draw.Draw(img, r, image.NewUniform(c), image.Point{}, draw.Src)
	})
	check("half of frame 1", 1)
	paintStamp(img, 1)
	check("frame 1", 2)
	paintStamp(img, 0)
	check("frame 0 once more", 3)
}

// grid is a screen of guacd's terminal, black until painted.
type grid struct{ img *image.RGBA }

func newGrid() grid {
	g := grid{image.NewRGBA(wholeScreen)}
	draw.Draw(g.img, wholeScreen, image.NewUniform(gridBlank), image.Point{}, draw.Src)
	return g
}

// text draws a glyph in each of the cells of row from column from up to
// column to: some pixels of the grey on the black.
func (g grid) text(row, from, to int) {
	for col := from; col < to; col++ {
		c := gridCell(col, row)
		for y := c.Min.Y + 4; y < c.Max.Y-4; y++ {
			g.img.SetRGBA(c.Min.X+3, y, gridCursor)
		}
	}
}

// cursor draws guacd's cursor on a cell.
func (g grid) cursor(col, row int) {
	draw.Draw(g.img, gridCell(col, row), image.NewUniform(gridCursor), image.Point{}, draw.Src)
}

// The prompt on guacd's terminal shows as the cursor waiting after "$ ",
// where the cursor stands on no other screen the shell passes through.
func TestShowsPromptPixels(t *testing.T) {
	const row = 9
	for name, c := range map[string]struct {
		paint  func(grid)
		damage image.Rectangle
		want   bool
	}{
		"the prompt": {func(g grid) {
			g.text(row, 0, 22)
			g.cursor(23, row)
		}, wholeScreen, true},
		"the prompt, damaged only around the cursor": {func(g grid) {
			g.text(row, 0, 22)
			g.cursor(23, row)
		}, gridCell(23, row), true},
		"the prompt, outside the damage": {func(g grid) {
			g.text(row, 0, 22)
			g.cursor(23, row)
		}, gridCell(23, row-3), false},
		"a cleared screen": {func(grid) {}, wholeScreen, false},
		"the cursor at the start of a line": {func(g grid) {
			g.text(row-1, 0, 30)
			g.cursor(0, row)
		}, wholeScreen, false},
		"the cursor straight after text": {func(g grid) {
			g.text(row, 0, 23)
			g.cursor(23, row)
		}, wholeScreen, false},
		"the cursor on a blank line": {func(g grid) {
			g.cursor(5, row)
		}, wholeScreen, false},
		"a screen of the cursor's colour": {func(g grid) {
			draw.Draw(g.img, wholeScreen, image.NewUniform(gridCursor), image.Point{}, draw.Src)
		}, wholeScreen, false},
		"no cursor": {func(g grid) {
			g.text(row, 0, 22)
		}, wholeScreen, false},
	} {
		t.Run(name, func(t *testing.T) {
			g := newGrid()
			c.paint(g)
			if got := showsPromptPixels(g.img, c.damage); got != c.want {
				t.Errorf("shows the prompt: %v, want %v", got, c.want)
			}
		})
	}
}

// A v1 terminal shows the prompt once a whole line of it is on screen.
func TestShowsPromptCells(t *testing.T) {
	for in, want := range map[string]bool{
		"":                                 false,
		"\x1b[H\x1b[2J\x1b[5;5H":           false,
		"Linux 2380575f15a7 6.12\n":        false,
		"$ ":                               false,
		testPrompt:                         true,
		"Linux bench\r\n\r\n" + testPrompt: true,
		"\x1b[01;32moccamy@bench\x1b[0m:\x1b[01;34m~\x1b[0m$ ": true,
		"occamy@bench:~":          false,
		testPrompt + "ls":         false,
		"run as occamy@bench:~$ ": false,
	} {
		term := terminal.New(termCols, termRows, terminal.Options{})
		term.Write([]byte(in))
		if got := showsPromptCells(term.Snapshot()); got != want {
			t.Errorf("%q shows the prompt: %v, want %v", in, got, want)
		}
	}
}

// The wallpaper shows in the middle of the screen, also after lossy
// encoding, and a frame that only sized the screen does not show it.
func TestWallpaperMarker(t *testing.T) {
	img := image.NewRGBA(wholeScreen)
	if wallpaper.pixels(img, wholeScreen) {
		t.Error("a screen that was only sized shows the wallpaper")
	}
	paintWallpaper(img)
	if !wallpaper.pixels(img, wholeScreen) {
		t.Error("the wallpaper does not show")
	}
	c := img.RGBAAt(width/2, height/2)
	c.R += 30
	c.B -= 30
	img.SetRGBA(width/2, height/2, c)
	if !wallpaper.pixels(img, wholeScreen) {
		t.Error("the wallpaper does not show after lossy encoding")
	}
	img.SetRGBA(width/2, height/2, white)
	if wallpaper.pixels(img, wholeScreen) {
		t.Error("another picture shows the wallpaper")
	}
}

// scriptConn plays Guacamole instructions to a v0 session, then ends it.
type scriptConn struct{ in chan []byte }

func (c *scriptConn) readRaw() ([]byte, error) {
	b, ok := <-c.in
	if !ok {
		return nil, io.EOF
	}
	return b, nil
}

func (*scriptConn) writeRaw([]byte) error { return nil }
func (*scriptConn) close() error          { return nil }

// v0Script is what a guacd sends.
type v0Script struct{ ins [][]byte }

func (s *v0Script) add(args ...string) {
	s.ins = append(s.ins, []byte(guacamole.NewInstruction(args).String()))
}

func (s *v0Script) size() { s.add("size", "0", strconv.Itoa(width), strconv.Itoa(height)) }

func (s *v0Script) fill(r image.Rectangle, c color.RGBA) {
	s.add("rect", "0", strconv.Itoa(r.Min.X), strconv.Itoa(r.Min.Y), strconv.Itoa(r.Dx()), strconv.Itoa(r.Dy()))
	s.add("cfill", "14", "0", strconv.Itoa(int(c.R)), strconv.Itoa(int(c.G)), strconv.Itoa(int(c.B)), strconv.Itoa(int(c.A)))
}

// stamp draws cells from up to to of frame n's stamp.
func (s *v0Script) stamp(n uint16, from, to int) { eachStampCell(n, from, to, s.fill) }

func (s *v0Script) sync() { s.add("sync", "1") }

func (s *v0Script) conn() *scriptConn {
	c := &scriptConn{in: make(chan []byte, len(s.ins))}
	for _, b := range s.ins {
		c.in <- b
	}
	close(c.in)
	return c
}

// A v0 session opens looking for what its protocol shows, so that G7 ends
// on known content: the wallpaper, not the frame that sizes the screen;
// the prompt, not the screen guacd clears, nor what the shell prints
// first.
func TestV0FirstFrame(t *testing.T) {
	var desktop v0Script
	desktop.size()
	desktop.sync()
	band := image.Rect(0, height/2-8, width, height/2+8)
	desktop.fill(band, probe.Wallpaper(height/2, height))
	desktop.sync()

	var term v0Script
	term.size()
	term.fill(wholeScreen, gridBlank)
	term.sync()
	term.fill(gridCell(0, 0), gridCursor) // a line of text, and the cursor below it
	term.fill(gridCell(0, 1), gridCursor)
	term.sync()
	term.fill(gridCell(0, 1), gridBlank) // the prompt, and the cursor after it
	term.fill(gridCell(6, 1), gridCursor)
	term.fill(gridCell(8, 1), gridCursor)
	term.sync()

	for _, c := range []struct {
		proto  string
		script *v0Script
		want   image.Rectangle
	}{
		{"vnc", &desktop, band},
		{"rdp", &desktop, band},
		{"ssh", &term, gridCell(0, 1).Union(gridCell(8, 1))},
	} {
		t.Run(c.proto, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			open := openV0(func(context.Context, string, config.JWT) (rawConn, error) {
				return c.script.conn(), nil
			})
			s, err := open(ctx, "", c.proto)
			if err != nil {
				t.Fatal(err)
			}
			defer s.close()
			f, err := s.wait(ctx, s.start, func(f frame) bool { return f.marked })
			if err != nil {
				t.Fatal(err)
			}
			if f.damage != c.want {
				t.Errorf("the frame that damaged %v is marked, want %v", f.damage, c.want)
			}
		})
	}
}

// A v0 session counts a scene's frames by their stamps, whichever of
// guacd's frames they arrive in: not one that draws elsewhere, nor one
// with part of a stamp, nor a stamp again.
func TestV0Stamps(t *testing.T) {
	var sc v0Script
	sc.size()
	sc.fill(wholeScreen, gridBlank)
	sc.sync()
	sc.stamp(1, 0, 24)
	sc.sync()
	sc.fill(image.Rect(256, 256, 320, 320), white)
	sc.sync()
	sc.stamp(2, 0, 12)
	sc.sync()
	sc.stamp(2, 12, 24)
	sc.sync()
	sc.stamp(2, 0, 24)
	sc.sync()

	s := newV0Session(sc.conn(), time.Now(), marker{})
	<-s.done
	if err := s.close(); err != nil {
		t.Fatal(err)
	}
	if got, want := s.drawn.Load(), int64(6); got != want {
		t.Errorf("%d frames drew, want %d", got, want)
	}
	if got := s.sceneFrames.Load(); got != 2 {
		t.Errorf("%d frames of the scene, want 2", got)
	}
}
