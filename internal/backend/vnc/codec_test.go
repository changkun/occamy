// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package vnc

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"io"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
	"time"

	"changkun.de/x/occamy/internal/backend/backendtest"
)

// jpegOf decodes the JPEG image in a Tight JPEG rectangle.
func jpegOf(t testing.TB, rect []byte) image.Image {
	t.Helper()
	data := rect[12+1:]
	n, k := int(data[0]&0x7f), 1
	if data[0]&0x80 != 0 {
		n |= int(data[1]&0x7f) << 7
		k = 2
		if data[1]&0x80 != 0 {
			n |= int(data[2]) << 14
			k = 3
		}
	}
	img, err := jpeg.Decode(bytes.NewReader(data[k : k+n]))
	if err != nil {
		t.Fatal(err)
	}
	return img
}

func TestEncodingsOverRun(t *testing.T) {
	// The fake server sends each encoding to a running backend, which
	// asked for all of them.
	checkGoroutines(t)
	s := connect(t, serverOptions{size: image.Pt(160, 100)}, true)
	for _, enc := range []int32{encHextile, encZRLE, encTight} {
		if !slices.Contains(s.c.encodings, enc) {
			t.Errorf("encoding %d not asked for in %v", enc, s.c.encodings)
		}
	}
	want := pattern(160, 100, 0)
	s.fullScreen(t, want)
	e := newEncoder(s.c.pf)
	img := mixed(160, 100, 16)
	smooth := image.NewRGBA(img.Rect)
	for y := range 100 {
		for x := range 160 {
			smooth.SetRGBA(x, y, color.RGBA{uint8(x), uint8(y * 2), uint8(x + y), 0xff})
		}
	}
	jpegRect := e.tightJPEG(smooth, image.Rect(100, 70, 160, 100), 90, 0)
	for _, u := range []struct {
		rects [][]byte
		draw  func()
	}{
		{[][]byte{e.hextile(img, image.Rect(0, 0, 70, 50))}, func() {
			draw.Draw(want, image.Rect(0, 0, 70, 50), img, image.Point{}, draw.Src)
		}},
		{[][]byte{e.zrle(img, image.Rect(70, 0, 160, 70)), e.tightBasic(img, image.Rect(0, 50, 70, 100), tightOptions{})}, func() {
			draw.Draw(want, image.Rect(70, 0, 160, 70), img, image.Pt(70, 0), draw.Src)
			draw.Draw(want, image.Rect(0, 50, 70, 100), img, image.Pt(0, 50), draw.Src)
		}},
		{[][]byte{
			e.tightBasic(smooth, image.Rect(70, 70, 100, 100), tightOptions{filter: tightFilterGradient, stream: 1}),
			e.tightBasic(img, image.Rect(0, 0, 16, 16), tightOptions{filter: tightFilterPalette, stream: 2, reset: 0b1111}),
			e.tightFill(image.Rect(150, 0, 160, 10), yellow, 0),
			jpegRect,
		}, func() {
			draw.Draw(want, image.Rect(70, 70, 100, 100), smooth, image.Pt(70, 70), draw.Src)
			draw.Draw(want, image.Rect(0, 0, 16, 16), img, image.Point{}, draw.Src)
			fillRect(want, image.Rect(150, 0, 160, 10), yellow)
			draw.Draw(want, image.Rect(100, 70, 160, 100), jpegOf(t, jpegRect), image.Point{}, draw.Src)
		}},
	} {
		s.c.send(t, putUpdate(u.rects...))
		if req := expect[requestMsg](t, s.c); !req.incremental {
			t.Fatalf("request %+v, want changes", req)
		}
		u.draw()
		equalImages(t, s.sink.Pixels(), want)
	}
}

func TestUpdateDrawBudget(t *testing.T) {
	// A Tight fill of the whole screen is 16 bytes. An update may draw
	// four times the screen's pixels, and the next is refused before it
	// is drawn.
	screen := image.Rect(0, 0, 1024, 768)
	e := newEncoder(wantPixelFormat)
	fills := func(n int) [][]byte {
		var rects [][]byte
		for i := range n {
			rects = append(rects, e.tightFill(screen, []color.RGBA{red, green, blue, yellow}[i%4], 0))
		}
		return rects
	}
	// Each update has a budget of its own, and pseudo-encodings cost
	// nothing.
	ok := slices.Concat(fills(3), [][]byte{
		putCopy(image.Rect(0, 0, 1024, 767), image.Pt(0, 1)),
		rectHeader(image.Rectangle{}, encCursor),
	})
	if _, err := decode(t, screen.Max, defaultLimits, putUpdate(ok...), putUpdate(fills(4)...)); err != nil {
		t.Fatalf("updates of four screens: %v", err)
	}
	// A DesktopSize starts the budget again, at the new screen's size.
	small := image.Rect(0, 0, 64, 48)
	resized := append(fills(4), putDesktopSize(64, 48), e.tightFill(small, red, 0))
	if _, err := decode(t, screen.Max, defaultLimits, putUpdate(resized...)); err != nil {
		t.Fatalf("four screens, a smaller screen and a fill of it: %v", err)
	}
	msg := putUpdate(fills(1000)...)
	in := bytes.NewReader(msg)
	sink := &backendtest.Sink{}
	sink.Resize(screen.Dx(), screen.Dy())
	d := newDecoder(context.Background(), in, sink, screen.Size(), defaultLimits, discardLogger)
	_, err := d.next()
	if err == nil || !strings.Contains(err.Error(), "rectangle 5 of 1000: the update draws more than 4 times the screen's 1024×768 pixels") {
		t.Errorf("next = %v, want the fifth fill refused", err)
	}
	// Four fills and the fifth's header are read, and nothing more.
	if want := len(msg) - 4 - 4*16 - 12; in.Len() != want {
		t.Errorf("%d bytes left, want %d", in.Len(), want)
	}
}

func TestUnexpectedEOFIsTheConnections(t *testing.T) {
	// Run reports io.ErrUnexpectedEOF when the connection closes within a
	// message. Data that arrived whole but ends too soon in its own format
	// is another error.
	var jbuf bytes.Buffer
	if err := jpeg.Encode(&jbuf, pattern(16, 16, 0), nil); err != nil {
		t.Fatal(err)
	}
	img := jbuf.Bytes()
	sos := bytes.Index(img, []byte{0xff, 0xda})
	tightJPEGOf := func(data []byte) []byte {
		return tightRect(image.Rect(0, 0, 16, 16), tightJPEG<<4, appendCompact(nil, len(data)), data)
	}
	var final bytes.Buffer
	w := zlib.NewWriter(&final)
	w.Write([]byte{1, 2})
	w.Close()
	for _, tt := range []struct {
		name string
		rect []byte
	}{
		{"jpeg cut in half", tightJPEGOf(img[:len(img)/2])},
		{"jpeg scan cut short", tightJPEGOf(slices.Concat(img[:sos+20], []byte{0xff, 0xd9}))},
		{"zrle stream ended within the pixels", zrleRaw(image.Rect(0, 0, 4, 1), final.Bytes())},
		{"tight stream ended within the pixels", tightRect(image.Rect(0, 0, 4, 4), 0, appendCompact(nil, final.Len()), final.Bytes())},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := decode(t, image.Pt(16, 16), defaultLimits, putUpdate(tt.rect), putBell())
			if err == nil || errors.Is(err, io.ErrUnexpectedEOF) {
				t.Errorf("decode = %v, want an error other than io.ErrUnexpectedEOF", err)
			}
			t.Log(err)
		})
	}
}

func TestDataBeyondThePixels(t *testing.T) {
	// Inflated data that the pixels do not take is an error, also when a
	// rectangle's data is larger than the buffer it is read through, or
	// than the inflater's window, unless the window fills exactly where
	// the pixels end (zstream.end).
	zrleData := func(r image.Rectangle) []byte {
		var data []byte
		tiles(r, zrleTile, func(t image.Rectangle) {
			data = append(append(data, 0), make([]byte, 3*t.Dx()*t.Dy())...)
		})
		return data
	}
	for _, r := range []image.Rectangle{image.Rect(0, 0, 8, 8), image.Rect(0, 0, 128, 64), image.Rect(0, 0, 500, 250)} {
		for _, enc := range []string{"zrle", "tight"} {
			var msg []byte
			if enc == "zrle" {
				msg = zrleRaw(r, zlibBytes(append(zrleData(r), make([]byte, 100)...)))
			} else {
				z := zlibBytes(make([]byte, 3*r.Dx()*r.Dy()+100))
				msg = tightRect(r, 0, appendCompact(nil, len(z)), z)
			}
			_, err := decode(t, r.Max, defaultLimits, putUpdate(msg))
			if err == nil || !strings.Contains(err.Error(), "100 bytes more than the rectangle's pixels") {
				t.Errorf("%s of %v with 100 bytes more: %v", enc, r, err)
			}
		}
	}
}

func TestEncodingAllocations(t *testing.T) {
	// Lengths that a server claims allocate nothing beyond the decoder's
	// fixed state before the bytes arrive, and a stream that inflates to
	// more than the rectangle needs is not inflated further.
	e := newEncoder(wantPixelFormat)
	jpeg8 := e.tightJPEG(pattern(8, 8, 0), image.Rect(0, 0, 8, 8), 50, 0)
	// The same JPEG, its frame header claiming 65535×65535 pixels.
	huge := slices.Clone(jpeg8)
	for i := 13; i+9 < len(huge); i++ {
		if huge[i] == 0xff && huge[i+1] == 0xc0 { // SOF0
			binary.BigEndian.PutUint16(huge[i+5:], 0xffff)
			binary.BigEndian.PutUint16(huge[i+7:], 0xffff)
			break
		}
	}
	// A bomb: 8 MiB of zeros, 8 KiB compressed and within the bound, after
	// a tile that needs 4 bytes.
	var bomb bytes.Buffer
	w, _ := zlib.NewWriterLevel(&bomb, zlib.BestCompression)
	w.Write(append([]byte{1}, cpx(red)...))
	w.Write(make([]byte, 8<<20))
	w.Flush()
	for _, tt := range []struct {
		name  string
		msg   []byte
		limit uint64
	}{
		{"zrle of 4 GiB", putUpdate(append(rectHeader(image.Rect(0, 0, 8, 8), encZRLE), 0xff, 0xff, 0xff, 0xff)), 4 << 10},
		{"zrle cut short", putUpdate(zrleRaw(image.Rect(0, 0, 64, 48), zlibBytes([]byte{1, 2, 3}))[:12+4+2+1]), 128 << 10},
		{"zrle bomb", putUpdate(zrleRaw(image.Rect(0, 0, 64, 48), bomb.Bytes())), 128 << 10},
		{"hextile of the largest size", putUpdate(rectHeader(image.Rect(0, 0, 0xffff, 0xffff), encHextile)), 4 << 10},
		{"tight jpeg of 4 MiB", putUpdate(tightRect(image.Rect(0, 0, 8, 8), tightJPEG<<4, appendCompact(nil, 1<<22-1), make([]byte, 100))), 96 << 10},
		{"tight jpeg of 65535×65535", putUpdate(huge), 32 << 10},
		{"tight data of 4 MiB", putUpdate(tightRect(image.Rect(0, 0, 64, 48), 0, appendCompact(nil, 1<<22-1))), 4 << 10},
	} {
		t.Run(tt.name, func(t *testing.T) {
			sink := &backendtest.Sink{}
			sink.Resize(64, 48)
			d := newDecoder(context.Background(), bytes.NewReader(tt.msg), sink, image.Pt(64, 48), defaultLimits, discardLogger)
			var err error
			start := time.Now()
			got := allocated(func() { _, err = d.next() })
			if err == nil {
				t.Error("no error")
			}
			if got > tt.limit {
				t.Errorf("allocated %d bytes, more than %d (%v)", got, tt.limit, err)
			}
			if d := time.Since(start); d > time.Second {
				t.Errorf("took %v", d)
			}
			t.Logf("%v; allocated %d bytes", err, got)
		})
	}
}

// benchSink is a sink that draws into an image and records nothing, for
// benchmarks.
type benchSink struct {
	*backendtest.Sink
	img *image.RGBA
}

func (s *benchSink) Resize(w, h int) { s.img = image.NewRGBA(image.Rect(0, 0, w, h)) }

func (s *benchSink) Lock(r image.Rectangle) *image.RGBA { return s.img.SubImage(r).(*image.RGBA) }

func (s *benchSink) Unlock(image.Rectangle) {}

func (s *benchSink) CopyRect(dst image.Rectangle, src image.Point) {
	draw.Draw(s.img, dst, s.img, src, draw.Src)
}

// desktopImage returns a desktop of w×h: a gradient wallpaper, a window of
// text, and a photograph of smooth noise and grain.
func desktopImage(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	rng := rand.New(rand.NewPCG(1, 2))
	for y := range h {
		c := color.RGBA{uint8(0x1f + 0x1d*y/h), uint8(0x3b + 0x33*y/h), uint8(0x5a + 0x35*y/h), 0xff}
		for x := range w {
			img.SetRGBA(x, y, c)
		}
	}
	text := image.Rect(w/16, h/12, w/2, h*5/6)
	fillRect(img, text, white)
	for y := text.Min.Y + 8; y < text.Max.Y-8; y += 16 {
		for x := text.Min.X + 8; x < text.Max.X-8; {
			word := 8 + rng.IntN(40)
			for i := x; i < min(x+word, text.Max.X-8); i++ {
				for j := y; j < y+10; j++ {
					if rng.IntN(3) == 0 {
						img.SetRGBA(i, j, color.RGBA{0x20, 0x20, 0x20, 0xff})
					}
				}
			}
			x += word + 6
		}
	}
	photo := image.Rect(w*9/16, h/6, w*15/16, h*3/4)
	for y := photo.Min.Y; y < photo.Max.Y; y++ {
		for x := photo.Min.X; x < photo.Max.X; x++ {
			g := rng.IntN(13) - 6
			v := func(base, a, b int) uint8 { return uint8(min(max(base+a*x/7%64+b*y/5%48+g, 0), 255)) }
			img.SetRGBA(x, y, color.RGBA{v(60, 1, 1), v(90, 2, 1), v(40, 1, 2), 0xff})
		}
	}
	return img
}

// tightFrame encodes img as a server's Tight encoder splits it, into
// rectangles of at most 2048 pixels wide and 65536 in all: one colour as
// a fill, up to 256 colours with the palette filter, and more as JPEG
// if quality is not 0 and with the copy filter otherwise.
func tightFrame(e *encoder, img *image.RGBA, quality int) [][]byte {
	var rects [][]byte
	w := min(img.Rect.Dx(), 2048)
	h := 65536 / w
	for y := 0; y < img.Rect.Dy(); y += h {
		for x := 0; x < img.Rect.Dx(); x += w {
			r := image.Rect(x, y, min(x+w, img.Rect.Dx()), min(y+h, img.Rect.Dy()))
			switch cs := colours(img, r, 256); {
			case len(cs) == 1:
				rects = append(rects, e.tightFill(r, cs[0], 0))
			case len(cs) <= 256:
				rects = append(rects, e.tightBasic(img, r, tightOptions{filter: tightFilterPalette, stream: 2}))
			case quality > 0:
				rects = append(rects, e.tightJPEG(img, r, quality, 0))
			default:
				rects = append(rects, e.tightBasic(img, r, tightOptions{stream: 0}))
			}
		}
	}
	return rects
}

// BenchmarkDecode decodes a desktop of 1024×768 in each encoding. Its
// throughput is in bytes of pixels drawn, four a pixel.
func BenchmarkDecode(b *testing.B) {
	img := desktopImage(1024, 768)
	r := img.Rect
	for _, enc := range []struct {
		name   string
		encode func(e *encoder) [][]byte
	}{
		{"raw", func(e *encoder) [][]byte { return [][]byte{putRaw(e.pf, img, r)} }},
		{"hextile", func(e *encoder) [][]byte { return [][]byte{e.hextile(img, r)} }},
		{"zrle", func(e *encoder) [][]byte { return [][]byte{e.zrle(img, r)} }},
		{"tight", func(e *encoder) [][]byte { return tightFrame(e, img, 0) }},
		{"tight-jpeg", func(e *encoder) [][]byte { return tightFrame(e, img, 80) }},
	} {
		b.Run(enc.name, func(b *testing.B) {
			// The frame starts the zlib streams, which are reset before
			// each decode.
			msg := putUpdate(enc.encode(newEncoder(wantPixelFormat))...)
			sink := &benchSink{Sink: &backendtest.Sink{}}
			sink.Resize(r.Dx(), r.Dy())
			src := bytes.NewReader(msg)
			d := newDecoder(context.Background(), src, sink, r.Size(), defaultLimits, discardLogger)
			b.SetBytes(int64(4 * r.Dx() * r.Dy()))
			for b.Loop() {
				src.Reset(msg)
				d.enc.zrle.reset()
				for i := range d.enc.tight {
					d.enc.tight[i].reset()
				}
				if _, err := d.next(); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportMetric(float64(len(msg)), "wire-bytes/frame")
		})
	}
}
