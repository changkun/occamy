// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package stream

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"changkun.de/x/occamy/internal/backend/backendtest"
	"changkun.de/x/occamy/internal/codec"
	"changkun.de/x/occamy/protocol"
)

// photo is the codec corpus's photograph (bench/testdata/corpus).
func photo(t *testing.T) *image.RGBA {
	t.Helper()
	f, err := os.Open(filepath.Join("..", "..", "bench", "testdata", "corpus", "photo.png"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	m, err := png.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(m.Bounds())
	draw.Draw(img, img.Rect, m, m.Bounds().Min, draw.Src)
	return img
}

// paint draws src's pixels at r into the session's screen.
func paint(b *tapped, r image.Rectangle, src image.Image) {
	img := b.Sink().Lock(r)
	draw.Draw(img, r, src, r.Min, draw.Src)
	b.Sink().Unlock(r)
}

// psnr is the peak signal-to-noise ratio of b against a over red, green
// and blue, in decibels; +Inf if they are the same.
func psnr(a, b *image.RGBA) float64 {
	var sum float64
	n := 0
	for y := range a.Rect.Dy() {
		ra := a.Pix[a.PixOffset(a.Rect.Min.X, a.Rect.Min.Y+y):]
		rb := b.Pix[b.PixOffset(b.Rect.Min.X, b.Rect.Min.Y+y):]
		for i := 0; i < 4*a.Rect.Dx(); i++ {
			if i%4 == 3 {
				continue
			}
			d := float64(ra[i]) - float64(rb[i])
			sum += d * d
			n++
		}
	}
	if sum == 0 {
		return math.Inf(1)
	}
	return 10 * math.Log10(255*255/(sum/float64(n)))
}

// Each viewer gets the codecs it agreed to, and its screen converges on
// the server's: exactly where a tile has up to 256 colours, and close to
// it where a tile goes lossy, in rectangles of several tiles when the
// codec is lossy.
func TestConvergesCodecs(t *testing.T) {
	pic := photo(t)
	const (
		webp    = protocol.CodecWebP
		jpeg    = protocol.CodecJPEG
		deflate = protocol.CodecDeflate
		raw     = protocol.CodecRaw
	)
	for _, c := range []struct {
		name   string
		codecs []protocol.Codec
		sent   []protocol.Codec // the codecs of the tiles it gets
		merged bool             // whether lossy tiles come merged
	}{
		{"WebP", []protocol.Codec{webp, deflate, jpeg, raw, protocol.CodecCached}, []protocol.Codec{webp, deflate}, true},
		{"JPEG without WebP", []protocol.Codec{deflate, jpeg, protocol.CodecPNG, raw}, []protocol.Codec{jpeg, deflate}, true},
		{"deflate", []protocol.Codec{deflate}, []protocol.Codec{deflate}, false},
		{"raw only", []protocol.Codec{raw}, []protocol.Codec{raw}, false},
		{"WebP only", []protocol.Codec{webp}, []protocol.Codec{webp}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				b := &tapped{Backend: backendtest.NewScreen(640, 480, 0)}
				hello := protocol.Hello{Visible: true, Capabilities: protocol.Capabilities{Codecs: c.codecs}}
				r := newRig(t, b, 50e6, time.Millisecond, hello)
				time.Sleep(100 * time.Millisecond)
				// Photographic content over part of the screen, not on
				// tile boundaries, so that its edge tiles are mixed.
				paint(b, image.Rect(40, 70, 520, 330), pic)
				time.Sleep(time.Second)
				synctest.Wait()

				want, got := serverScreen(r.s), r.cl.screen()
				if got.Rect != want.Rect {
					t.Fatalf("client screen %v, server's %v", got.Rect, want.Rect)
				}
				var p codec.Palette
				hasLossy := slices.Contains(c.sent, webp) || slices.Contains(c.sent, jpeg)
				hasLossless := slices.Contains(c.sent, deflate) || slices.Contains(c.sent, raw)
				lossy, worst := 0, math.Inf(1)
				for y := 0; y < want.Rect.Dy(); y += 64 {
					for x := 0; x < want.Rect.Dx(); x += 64 {
						tr := image.Rect(x, y, x+64, y+64).Intersect(want.Rect)
						w, g := want.SubImage(tr).(*image.RGBA), got.SubImage(tr).(*image.RGBA)
						pix := make([]byte, 0, 4*64*64)
						for yy := tr.Min.Y; yy < tr.Max.Y; yy++ {
							pix = append(pix, w.Pix[w.PixOffset(tr.Min.X, yy):w.PixOffset(tr.Max.X, yy)]...)
						}
						exact := !hasLossy ||
							hasLossless && codec.Colours(pix, tr.Dx(), tr.Dy(), &p) <= codec.MaxColours
						switch s := psnr(w, g); {
						case exact && !math.IsInf(s, 1):
							t.Errorf("tile at %v differs: PSNR %.1f dB", tr.Min, s)
						case !exact:
							lossy++
							worst = min(worst, s)
						}
					}
				}

				r.cl.mu.Lock()
				defer r.cl.mu.Unlock()
				var agreed []protocol.Codec
				for _, k := range c.codecs {
					if k != protocol.CodecCached && k != protocol.CodecPNG {
						agreed = append(agreed, k)
					}
				}
				if r.cl.welcome == nil || !slices.Equal(r.cl.welcome.Capabilities.Codecs, agreed) {
					t.Errorf("welcome agreed to %v, want %v", r.cl.welcome.Capabilities.Codecs, agreed)
				}
				for k, n := range r.cl.codecs {
					if !slices.Contains(c.sent, k) {
						t.Errorf("%d tiles in %v, want only %v", n, k, c.sent)
					}
				}
				for _, k := range c.sent {
					if r.cl.codecs[k] == 0 {
						t.Errorf("no tile in %v; got %v", k, r.cl.codecs)
					}
				}
				wide := 0
				for _, f := range r.cl.frames {
					for _, rect := range f.rects {
						if rect.Dx() > 64 {
							wide++
						}
						if rect.Dy() > 64 || rect.Min.X%64 != 0 || rect.Min.Y%64 != 0 {
							t.Errorf("a tile message at %v is not on the tiles", rect)
						}
					}
				}
				if (wide > 0) != c.merged {
					t.Errorf("%d rectangles of several tiles; want them %v", wide, c.merged)
				}
				// At quality 90, the photograph's worst tile scores 37
				// dB as WebP and 35 as JPEG; reading WebP's colours as
				// full-range would cost it about 13.
				if hasLossy && (lossy == 0 || worst < 30) {
					t.Errorf("%d tiles lossy, the worst at %.1f dB", lossy, worst)
				}
				t.Logf("%d tiles lossy, the worst at %.1f dB; tiles by codec %v", lossy, worst, r.cl.codecs)
			})
		})
	}
}

// lossyMark marks a tile that markEncoder sends lossy: its first pixel's
// red.
const lossyMark = 0x01

// markEncoder sends tiles whose first pixel's red is lossyMark as lossy,
// and every message raw, so that a test sees how the writer merges lossy
// tiles.
type markEncoder struct {
	mu    sync.Mutex
	lossy []image.Point // the sizes Lossy was asked for
}

func (e *markEncoder) Tile(dst, pix []byte, w, h int) ([]byte, protocol.Codec, bool) {
	if pix[0] == lossyMark {
		return dst, 0, true
	}
	return append(dst, pix[:4*w*h]...), protocol.CodecRaw, false
}

func (e *markEncoder) Lossy(dst, pix []byte, w, h, _ int) ([]byte, protocol.Codec) {
	e.mu.Lock()
	e.lossy = append(e.lossy, image.Pt(w, h))
	e.mu.Unlock()
	return append(dst, pix[:4*w*h]...), protocol.CodecRaw
}

func (e *markEncoder) Cursor(dst, pix []byte, w, h int) ([]byte, protocol.Codec, bool) {
	return append(dst, pix[:4*w*h]...), protocol.CodecRaw, true
}

// The writer merges the lossy tiles that are side by side in a row of
// tiles and next in order into one rectangle, of at most encoder.MaxRun
// tiles, at most the client's texture size wide, and within its message
// size; every other tile goes alone.
func TestLossyRuns(t *testing.T) {
	// 1250×192 is 20×3 tiles, the last column 34 pixels wide. A run
	// ends with its row, though the next row starts lossy.
	lossy := map[int][]int{
		0: {0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19},
		1: {0, 2, 3, 4, 7, 9, 10, 19},
	}
	run := func(row, from, to int) image.Rectangle { // tiles from to to, inclusive
		return image.Rect(from*64, row*64, min(1250, (to+1)*64), (row+1)*64)
	}
	singles := func(row int, cols ...int) []image.Rectangle {
		var rs []image.Rectangle
		for _, c := range cols {
			rs = append(rs, run(row, c, c))
		}
		return rs
	}
	all := []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19}
	row1 := slices.Concat(singles(1, 0, 1), []image.Rectangle{run(1, 2, 4)}, singles(1, 5, 6, 7, 8),
		[]image.Rectangle{run(1, 9, 10)}, singles(1, 11, 12, 13, 14, 15, 16, 17, 18, 19))
	for _, c := range []struct {
		name string
		caps protocol.Capabilities
		want []image.Rectangle
	}{
		{"default", protocol.Capabilities{},
			slices.Concat([]image.Rectangle{run(0, 0, 15), run(0, 16, 19)}, row1, singles(2, all...))},
		{"200-pixel textures", protocol.Capabilities{MaxTextureSize: 200},
			slices.Concat([]image.Rectangle{run(0, 0, 2), run(0, 3, 5), run(0, 6, 8), run(0, 9, 11),
				run(0, 12, 14), run(0, 15, 17), run(0, 18, 19)}, row1, singles(2, all...))},
		// Raw, a run is 16 KiB a tile, halved until it fits in 64 KiB:
		// 16 tiles to 2, then the rest of the row, 12 to 3, 9 to 3, 6 to
		// 3, and the last 3, 162 pixels wide, fit. So do the second row's.
		{"64 KiB messages", protocol.Capabilities{MaxMessageSize: 64 << 10},
			slices.Concat([]image.Rectangle{run(0, 0, 1), run(0, 2, 3), run(0, 4, 5), run(0, 6, 7),
				run(0, 8, 10), run(0, 11, 13), run(0, 14, 16), run(0, 17, 19)},
				row1, singles(2, all...))},
	} {
		t.Run(c.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				enc := &markEncoder{}
				b := &tapped{Backend: backendtest.NewScreen(1250, 192, 0)}
				caps := c.caps
				caps.Codecs = []protocol.Codec{protocol.CodecRaw}
				hello := protocol.Hello{Visible: true, Capabilities: caps}
				cfg := Config{NewEncoder: func([]protocol.Codec) Encoder { return enc }}
				r := newRigConfig(t, b, 64, 50e6, time.Millisecond, hello, cfg)
				time.Sleep(100 * time.Millisecond)
				// One drawing operation repaints the screen, so that one
				// frame sends it row by row.
				screen := image.NewRGBA(image.Rect(0, 0, 1250, 192))
				for row, cols := range lossy {
					for _, col := range cols {
						tr := run(row, col, col)
						draw.Draw(screen, tr, &image.Uniform{C: lossyColour(col)}, image.Point{}, draw.Src)
					}
				}
				for i := 3; i < len(screen.Pix); i += 4 {
					screen.Pix[i] = 0xff
				}
				r.cl.mu.Lock()
				before := len(r.cl.frames)
				r.cl.mu.Unlock()
				paint(b, screen.Rect, screen)
				time.Sleep(time.Second)
				synctest.Wait()

				if got := r.cl.screen(); !bytes.Equal(got.Pix, screen.Pix) {
					t.Error("the client's screen differs from the server's")
				}
				r.cl.mu.Lock()
				defer r.cl.mu.Unlock()
				// The pacer may end frames early; the messages keep their
				// order across them.
				var got []image.Rectangle
				for _, f := range r.cl.frames[before:] {
					got = append(got, f.rects...)
				}
				if !slices.Equal(got, c.want) {
					t.Errorf("tile messages\n%v\nwant\n%v", got, c.want)
				}
			})
		})
	}
}

// A run of lossy tiles ends where a tile of the row is not dirty, so
// that the tiles after it are drawn in their own place.
func TestLossyRunGaps(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := &tapped{Backend: backendtest.NewScreen(640, 192, 0)}
		hello := protocol.Hello{Visible: true, Capabilities: protocol.Capabilities{Codecs: []protocol.Codec{protocol.CodecRaw}}}
		cfg := Config{NewEncoder: func([]protocol.Codec) Encoder { return &markEncoder{} }}
		r := newRigConfig(t, b, 64, 50e6, time.Millisecond, hello, cfg)
		time.Sleep(100 * time.Millisecond)
		synctest.Wait()
		tile := func(row, col int) image.Rectangle {
			return image.Rect(col*64, row*64, (col+1)*64, (row+1)*64)
		}
		fill := func(row, col int) {
			paint(b, tile(row, col), &image.Uniform{C: lossyColour(col)})
		}
		r.cl.mu.Lock()
		before := len(r.cl.frames)
		r.cl.mu.Unlock()
		// A frame of one tile, so that the pacer holds the next frame
		// until the clock moves, and the tiles drawn meanwhile go in it.
		fill(2, 0)
		synctest.Wait()
		for _, col := range []int{2, 3, 5, 6} {
			fill(0, col)
		}
		time.Sleep(time.Second)
		synctest.Wait()

		if got, want := r.cl.screen(), serverScreen(r.s); !bytes.Equal(got.Pix, want.Pix) {
			t.Error("the client's screen differs from the server's")
		}
		r.cl.mu.Lock()
		defer r.cl.mu.Unlock()
		var got []image.Rectangle
		for _, f := range r.cl.frames[before:] {
			got = append(got, f.rects...)
		}
		want := []image.Rectangle{tile(2, 0), image.Rect(128, 0, 256, 64), image.Rect(320, 0, 448, 64)}
		if !slices.Equal(got, want) {
			t.Errorf("tile messages\n%v\nwant\n%v", got, want)
		}
	})
}

// lossyColour is an opaque colour that markEncoder sends lossy, different
// for each column so that a misplaced tile shows.
func lossyColour(col int) color.RGBA {
	return color.RGBA{R: lossyMark, G: uint8(col * 10), B: 0x80, A: 0xff} // #nosec G115 -- 20 columns
}

// A cursor keeps its alpha in each codec a viewer may get it in, and a
// viewer that decodes none that carries alpha keeps its own pointer.
func TestCursorCodecs(t *testing.T) {
	// A pointer with straight alpha, which the backend hands over as an
	// image.RGBA, premultiplied; the client gets straight alpha back.
	pointer := image.NewNRGBA(image.Rect(0, 0, 16, 16))
	for i := 0; i < len(pointer.Pix); i += 4 {
		x, y := i/4%16, i/4/16
		switch {
		case x == 0 || y == 0:
			copy(pointer.Pix[i:], []byte{0, 0, 0, 0xff})
		case x < y:
			copy(pointer.Pix[i:], []byte{0xff, 0xff, 0xff, 0xff})
		case x == y:
			copy(pointer.Pix[i:], []byte{0x20, 0x40, 0x60, 0}) // transparent
		default:
			copy(pointer.Pix[i:], []byte{0x20, 0x40, 0x60, byte(x * 16)})
		}
	}
	img := image.NewRGBA(pointer.Rect)
	draw.Draw(img, img.Rect, pointer, image.Point{}, draw.Src)
	want := image.NewNRGBA(pointer.Rect)
	draw.Draw(want, want.Rect, img, image.Point{}, draw.Src)
	for _, c := range []struct {
		codecs []protocol.Codec
		want   protocol.Codec
	}{
		{[]protocol.Codec{protocol.CodecWebP, protocol.CodecDeflate, protocol.CodecRaw}, protocol.CodecDeflate},
		{[]protocol.Codec{protocol.CodecRaw}, protocol.CodecRaw},
		{[]protocol.Codec{protocol.CodecWebP}, protocol.CodecWebP},
		{[]protocol.Codec{protocol.CodecJPEG}, 0},
	} {
		synctest.Test(t, func(t *testing.T) {
			b := &tapped{Backend: backendtest.NewScreen(200, 150, 0)}
			hello := protocol.Hello{Visible: true, Capabilities: protocol.Capabilities{Codecs: c.codecs}}
			r := newRig(t, b, 50e6, time.Millisecond, hello)
			time.Sleep(100 * time.Millisecond)
			b.Sink().Cursor(img, image.Pt(1, 1))
			time.Sleep(100 * time.Millisecond)
			r.cl.mu.Lock()
			defer r.cl.mu.Unlock()
			if c.want == 0 {
				if len(r.cl.cursors) != 0 {
					t.Errorf("%v: %d cursors, want none", c.codecs, len(r.cl.cursors))
				}
				return
			}
			if len(r.cl.cursors) != 1 {
				t.Fatalf("%v: %d cursors, want 1", c.codecs, len(r.cl.cursors))
			}
			m := r.cl.cursors[0]
			out := image.NewRGBA(image.Rect(0, 0, int(m.W), int(m.H)))
			if err := decodeTile(out, m.Codec, m.Payload); err != nil || m.Codec != c.want {
				t.Fatalf("%v: cursor in %v: %v", c.codecs, m.Codec, err)
			}
			if !bytes.Equal(out.Pix, want.Pix) {
				t.Errorf("%v: the cursor differs after a round trip in %v", c.codecs, m.Codec)
			}
		})
	}
}

// hookEncoder is markEncoder with a hook in Lossy, which runs once, on
// the writer's goroutine, after the run's lookahead tile was read.
type hookEncoder struct {
	markEncoder
	once sync.Once
	hook func()
}

func (e *hookEncoder) Lossy(dst, pix []byte, w, h, q int) ([]byte, protocol.Codec) {
	e.once.Do(e.hook)
	return e.markEncoder.Lossy(dst, pix, w, h, q)
}

// tileAt is the rectangle of a 64-pixel tile.
func tileAt(row, col int) image.Rectangle {
	return image.Rect(col*64, row*64, (col+1)*64, (row+1)*64)
}

// An input whose effect lands on the lossless tile that ended a lossy run
// goes out with the effect, not with the copy read ahead before it.
func TestLookaheadInputEffect(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := &tapped{Backend: backendtest.NewScreen(640, 192, 0)}
		var r *rig
		oldC := color.RGBA{R: 0x80, A: 0xff}
		newC := color.RGBA{G: 0x80, A: 0xff}
		u := tileAt(1, 2)
		enc := &hookEncoder{}
		enc.hook = func() {
			r.cl.send(t, protocol.Key{Seq: 1, Keysym: 'k'}) // a key-up: the fake paints nothing
			time.Sleep(time.Second)
			paint(b, u, &image.Uniform{C: newC})
		}
		cfg := Config{NewEncoder: func([]protocol.Codec) Encoder { return enc }}
		r = newRigConfig(t, b, 64, 50e6, time.Millisecond, visible, cfg)
		time.Sleep(500 * time.Millisecond)
		synctest.Wait()

		var carrier *frameRecord
		var shown color.RGBA
		r.cl.mu.Lock()
		r.cl.onFrame = func(f frameRecord) {
			if carrier == nil && f.lastSeq >= 1 {
				carrier = &f
				shown = r.cl.img.RGBAAt(u.Min.X+1, u.Min.Y+1)
			}
		}
		r.cl.mu.Unlock()
		// Two lossy tiles, then a lossless one.
		paint(b, image.Rect(0, 64, 128, 128), &image.Uniform{C: lossyColour(0)})
		paint(b, u, &image.Uniform{C: oldC})
		time.Sleep(3 * time.Second)
		synctest.Wait()

		r.cl.mu.Lock()
		defer r.cl.mu.Unlock()
		if carrier == nil {
			t.Fatal("no frame reported last_seq 1")
		}
		if shown != newC {
			t.Errorf("frame %d reports the input shown, but its effect's tile shows %v, not %v", carrier.id, shown, newC)
		}
	})
}

// A tile read ahead in one frame is not sent in the next as it was: the
// tile may have changed while the client hid the screen.
func TestLookaheadAcrossFrames(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := &tapped{Backend: backendtest.NewScreen(640, 192, 0)}
		var r *rig
		newC := color.RGBA{G: 0x80, A: 0xff}
		u := tileAt(1, 2)
		enc := &hookEncoder{}
		enc.hook = func() {
			// The run is in hand and u read ahead: the frame ends at the
			// next message, and u changes before the client looks again.
			r.cl.send(t, protocol.View{})
			time.Sleep(time.Second)
			paint(b, u, &image.Uniform{C: newC})
		}
		cfg := Config{NewEncoder: func([]protocol.Codec) Encoder { return enc }}
		r = newRigConfig(t, b, 64, 50e6, time.Millisecond, visible, cfg)
		time.Sleep(500 * time.Millisecond)
		synctest.Wait()
		r.cl.mu.Lock()
		before := len(r.cl.frames)
		r.cl.mu.Unlock()
		paint(b, image.Rect(0, 64, 128, 128), &image.Uniform{C: lossyColour(0)})
		paint(b, u, &image.Uniform{C: color.RGBA{R: 0x80, A: 0xff}})
		time.Sleep(3 * time.Second)
		synctest.Wait()
		r.cl.send(t, protocol.View{Visible: true})
		time.Sleep(time.Second)
		synctest.Wait()

		if got := r.cl.screen().RGBAAt(u.Min.X+1, u.Min.Y+1); got != newC {
			t.Errorf("the tile read ahead shows %v, not %v", got, newC)
		}
		r.cl.mu.Lock()
		defer r.cl.mu.Unlock()
		n := 0
		for _, f := range r.cl.frames[before:] {
			for _, rect := range f.rects {
				if rect == u {
					n++
				}
			}
		}
		if n != 1 {
			t.Errorf("the tile read ahead went out %d times, want once, as it is now", n)
		}
	})
}

// An input's frame ends before a drawing operation that would take it
// past inputFrameBytes, and a lossy run does not carry the next
// operation across that boundary whole.
func TestInputBudgetAcrossRun(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := &tapped{Backend: backendtest.NewScreen(1280, 192, 0)}
		var r *rig
		enc := &hookEncoder{}
		enc.hook = func() {
			r.cl.send(t, protocol.Key{Seq: 1, Keysym: 'k'})
			time.Sleep(time.Second)
			paint(b, tileAt(1, 0), &image.Uniform{C: lossyColour(0)})                  // A: one tile
			paint(b, image.Rect(64, 64, 1280, 128), &image.Uniform{C: lossyColour(1)}) // B: 19 tiles, 304 KiB raw
		}
		cfg := Config{NewEncoder: func([]protocol.Codec) Encoder { return enc }}
		r = newRigConfig(t, b, 64, 1e9, 20*time.Millisecond, visible, cfg)
		time.Sleep(500 * time.Millisecond)
		synctest.Wait()
		// The input's frame is the first to draw in row 1; the frame
		// whose run runs the hook may report the input too, since the
		// key arrives while it is read.
		var carrier *frameRecord
		r.cl.mu.Lock()
		r.cl.onFrame = func(f frameRecord) {
			if carrier == nil && f.lastSeq >= 1 && slices.ContainsFunc(f.rects, func(rect image.Rectangle) bool { return rect.Min.Y == 64 }) {
				carrier = &f
			}
		}
		r.cl.mu.Unlock()
		paint(b, tileAt(2, 0), &image.Uniform{C: lossyColour(2)}) // the frame whose run runs the hook
		time.Sleep(3 * time.Second)
		synctest.Wait()
		r.cl.mu.Lock()
		defer r.cl.mu.Unlock()
		if carrier == nil {
			t.Fatal("no frame of the input drew row 1")
		}
		if want := []image.Rectangle{tileAt(1, 0)}; !slices.Equal(carrier.rects, want) {
			t.Errorf("the input's frame carried %v, want %v", carrier.rects, want)
		}
	})
}

// The input budget counts the bytes of each tile of a run, not the run's
// for each: a run of four tiles leaves room for an operation of five.
func TestInputBudgetPerTile(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// 16-pixel tiles, 1 KiB each raw.
		b := &tapped{Backend: backendtest.NewScreen(320, 64, 0)}
		var r *rig
		tile := func(row, col int) image.Rectangle { return image.Rect(col*16, row*16, (col+1)*16, (row+1)*16) }
		enc := &hookEncoder{}
		enc.hook = func() {
			r.cl.send(t, protocol.Key{Seq: 1, Keysym: 'k'})
			time.Sleep(time.Second)
			paint(b, image.Rect(0, 16, 64, 32), &image.Uniform{C: lossyColour(0)})            // A: a run of 4 tiles
			paint(b, image.Rect(0, 32, 80, 48), &image.Uniform{C: color.RGBA{G: 1, A: 0xff}}) // B: 5 lossless tiles
		}
		cfg := Config{NewEncoder: func([]protocol.Codec) Encoder { return enc }}
		r = newRigConfig(t, b, 16, 1e9, 20*time.Millisecond, visible, cfg)
		time.Sleep(500 * time.Millisecond)
		synctest.Wait()
		// The input's frame is the first to draw in rows 1 or 2.
		var carrier *frameRecord
		r.cl.mu.Lock()
		r.cl.onFrame = func(f frameRecord) {
			if carrier == nil && f.lastSeq >= 1 && slices.ContainsFunc(f.rects, func(rect image.Rectangle) bool { return rect.Min.Y == 16 || rect.Min.Y == 32 }) {
				carrier = &f
			}
		}
		r.cl.mu.Unlock()
		paint(b, tile(3, 0), &image.Uniform{C: lossyColour(2)}) // the frame whose run runs the hook
		time.Sleep(3 * time.Second)
		synctest.Wait()
		r.cl.mu.Lock()
		defer r.cl.mu.Unlock()
		if carrier == nil {
			t.Fatal("no frame of the input drew rows 1 or 2")
		}
		want := []image.Rectangle{image.Rect(0, 16, 64, 32)}
		for col := range 5 {
			want = append(want, tile(2, col))
		}
		if !slices.Equal(carrier.rects, want) {
			t.Errorf("the input's frame carried %v, want %v", carrier.rects, want)
		}
	})
}

// resizeEncoder is markEncoder that runs a hook when it is first asked
// for a lossy tile, before the writer looks for the rest of the run.
type resizeEncoder struct {
	markEncoder
	once sync.Once
	hook func()
}

func (e *resizeEncoder) Tile(dst, pix []byte, w, h int) ([]byte, protocol.Codec, bool) {
	if pix[0] == lossyMark {
		e.once.Do(e.hook)
	}
	return e.markEncoder.Tile(dst, pix, w, h)
}

// A run ends where the screen was resized under it, rather than take a
// tile of the new size for one of the old.
func TestLossyRunResized(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := &tapped{Backend: backendtest.NewScreen(640, 192, 0)}
		enc := &resizeEncoder{}
		enc.hook = func() {
			// Ten columns become five: tile 11, the next in the run, was
			// at row 1, column 1, and is now at row 2, column 1, lossy.
			b.Sink().Resize(320, 192)
			paint(b, tileAt(2, 1), &image.Uniform{C: lossyColour(1)})
		}
		cfg := Config{NewEncoder: func([]protocol.Codec) Encoder { return enc }}
		r := newRigConfig(t, b, 64, 50e6, time.Millisecond, visible, cfg)
		time.Sleep(500 * time.Millisecond)
		synctest.Wait()
		r.cl.mu.Lock()
		before := len(r.cl.frames)
		r.cl.mu.Unlock()
		paint(b, tileAt(1, 0), &image.Uniform{C: lossyColour(0)})
		paint(b, tileAt(1, 1), &image.Uniform{C: color.RGBA{G: 1, A: 0xff}})
		time.Sleep(time.Second)
		synctest.Wait()

		if got, want := r.cl.screen(), serverScreen(r.s); !bytes.Equal(got.Pix, want.Pix) {
			t.Error("the client's screen differs from the server's")
		}
		r.cl.mu.Lock()
		defer r.cl.mu.Unlock()
		for _, f := range r.cl.frames[before:] {
			for _, rect := range f.rects {
				if rect.Dx() > 64 {
					t.Errorf("a run of %v spans a resize", rect)
				}
			}
		}
	})
}

// A viewer that lists only a lossy codec gets every tile in it, those at
// the screen's odd edges included, in the right place.
func TestLossyOnly(t *testing.T) {
	for _, codecs := range [][]protocol.Codec{{protocol.CodecJPEG}, {protocol.CodecWebP}} {
		synctest.Test(t, func(t *testing.T) {
			pic := photo(t)
			b := &tapped{Backend: backendtest.NewScreen(651, 487, 0)}
			hello := protocol.Hello{Visible: true, Capabilities: protocol.Capabilities{Codecs: codecs}}
			r := newRig(t, b, 50e6, time.Millisecond, hello)
			time.Sleep(100 * time.Millisecond)
			paint(b, image.Rect(0, 0, 651, 487), pic)
			time.Sleep(time.Second)
			synctest.Wait()
			want, got := serverScreen(r.s), r.cl.screen()
			worst := math.Inf(1)
			for y := 0; y < 487; y += 64 {
				for x := 0; x < 651; x += 64 {
					tr := image.Rect(x, y, x+64, y+64).Intersect(want.Rect)
					worst = min(worst, psnr(want.SubImage(tr).(*image.RGBA), got.SubImage(tr).(*image.RGBA)))
				}
			}
			r.cl.mu.Lock()
			defer r.cl.mu.Unlock()
			for k := range r.cl.codecs {
				if k != codecs[0] {
					t.Errorf("%v: tiles in %v", codecs, k)
				}
			}
			if worst < 28 {
				t.Errorf("%v: the worst tile scores %.1f dB", codecs, worst)
			}
		})
	}
}
