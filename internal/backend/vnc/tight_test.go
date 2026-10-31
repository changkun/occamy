// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package vnc

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"io"
	"slices"
	"strings"
	"testing"

	"changkun.de/x/occamy/internal/backend/backendtest"
)

// tightRect returns a Tight rectangle of r with its compression control
// and the rest by hand.
func tightRect(r image.Rectangle, ctl byte, rest ...[]byte) []byte {
	return slices.Concat(append([][]byte{rectHeader(r, encTight), {ctl}}, rest...)...)
}

func TestTightByHand(t *testing.T) {
	e := newEncoder(wantPixelFormat)
	zdata := func(stream int, data []byte) []byte {
		c := e.compress(&e.tight[stream], data)
		return append(appendCompact(nil, len(c)), c...)
	}
	// Two colours, rows of two bytes of bits for 10 pixels, the padding
	// set.
	twoRows := []byte{0b10101010, 0b11_111111, 0b00001111, 0b01_000000}
	// Gradient, 2×2: each component less the left plus the upper less the
	// upper left, clamped; red's prediction at (1, 1) is 490, clamped to
	// 255, and green's -190, clamped to 0. 12 bytes, so compressed.
	gradientPixels := []color.RGBA{{10, 200, 30, 0xff}, {250, 10, 40, 0xff}, {250, 0, 33, 0xff}, {7, 9, 255, 0xff}}
	gradient := []byte{10, 200, 30, 240, 66, 10, 240, 56, 3, 8, 9, 212}
	msg := putUpdate(
		// Fill, 4×3 at (1, 1).
		tightRect(image.Rect(1, 1, 5, 4), tightFill<<4, cpx(yellow)),
		// Copy without a filter ID, 2×1: 6 bytes, sent as they are.
		tightRect(image.Rect(6, 0, 8, 1), 0, cpx(red), cpx(blue)),
		// Copy with the filter's ID, 3×1: 9 bytes, sent as they are.
		tightRect(image.Rect(6, 1, 9, 2), tightExplicitFilter<<4, []byte{tightFilterCopy}, cpx(green), cpx(white), cpx(red)),
		// Copy in stream 2, 4×2: 24 bytes, compressed.
		tightRect(image.Rect(10, 0, 14, 2), 2<<4, zdata(2, slices.Concat(
			cpx(red), cpx(green), cpx(blue), cpx(white),
			cpx(white), cpx(blue), cpx(green), cpx(red)))),
		// Two colours, 10×2: 4 bytes, sent as they are.
		tightRect(image.Rect(12, 10, 22, 12), tightExplicitFilter<<4, []byte{tightFilterPalette, 1}, cpx(blue), cpx(yellow), twoRows),
		// The same, 10×6: 12 bytes, compressed in stream 1.
		tightRect(image.Rect(0, 5, 10, 11), (1|tightExplicitFilter)<<4, []byte{tightFilterPalette, 1}, cpx(blue), cpx(yellow),
			zdata(1, slices.Concat(twoRows, twoRows, twoRows))),
		// One colour, 3×4: rows of one byte, 4 bytes sent as they are.
		tightRect(image.Rect(11, 5, 14, 9), tightExplicitFilter<<4, []byte{tightFilterPalette, 0}, cpx(green), []byte{0, 0x1f, 0, 0}),
		// Three colours, 5×3: a byte a pixel, compressed in stream 3.
		tightRect(image.Rect(15, 5, 20, 8), (3|tightExplicitFilter)<<4, []byte{tightFilterPalette, 2}, cpx(red), cpx(green), cpx(blue),
			zdata(3, []byte{0, 1, 2, 2, 1, 1, 1, 1, 1, 1, 2, 0, 0, 0, 2})),
		// Gradient in stream 0.
		tightRect(image.Rect(7, 12, 9, 14), tightExplicitFilter<<4, []byte{tightFilterGradient}, zdata(0, gradient)),
	)
	base := pattern(24, 16, 3)
	d, sink, err := decodeOn(t, base, msg)
	if err != nil {
		t.Fatal(err)
	}
	want := image.NewRGBA(base.Rect)
	copy(want.Pix, base.Pix)
	fillRect(want, image.Rect(1, 1, 5, 4), yellow)
	set := func(x, y int, cs ...color.RGBA) {
		for i, c := range cs {
			want.SetRGBA(x+i, y, c)
		}
	}
	set(6, 0, red, blue)
	set(6, 1, green, white, red)
	set(10, 0, red, green, blue, white)
	set(10, 1, white, blue, green, red)
	b, y := blue, yellow
	for _, at := range []image.Point{{12, 10}, {0, 5}, {0, 7}, {0, 9}} {
		set(at.X, at.Y, y, b, y, b, y, b, y, b, y, y)
		set(at.X, at.Y+1, b, b, b, b, y, y, y, y, b, y)
	}
	fillRect(want, image.Rect(11, 5, 14, 9), green)
	set(15, 5, red, green, blue, blue, green)
	set(15, 6, green, green, green, green, green)
	set(15, 7, blue, red, red, red, blue)
	set(7, 12, gradientPixels[:2]...)
	set(7, 13, gradientPixels[2:]...)
	equalImages(t, sink.Pixels(), want)
	want4 := [numKinds]int{tightFillRect: 1, tightCopyRect: 3, tightPaletteRect: 4, tightGradientRect: 1, tightRawData: 4, tightZlibData: 4}
	if d.enc.counts != want4 {
		t.Errorf("counts %v, want %v", d.enc.counts, want4)
	}
}

func TestTightRoundTrip(t *testing.T) {
	// The fake's encoder with each filter, in each stream, over updates,
	// on rectangles drawn in strips of 3 rows, so that the gradient
	// filter carries its row over from one strip to the next.
	img := mixed(64*2+3, 40, 16)
	w, h := img.Rect.Dx(), img.Rect.Dy()
	e := newEncoder(wantPixelFormat)
	sink := &backendtest.Sink{}
	sink.Resize(w, h)
	var msgs [][]byte
	want := pattern(w, h, 1)
	copy(sink.Lock(want.Rect).Pix, want.Pix)
	sink.Unlock(want.Rect)
	smooth := image.NewRGBA(img.Rect)
	for y := range h {
		for x := range w {
			smooth.SetRGBA(x, y, color.RGBA{uint8(x * 2), uint8(y*5 + x), uint8(200 - x), 0xff})
		}
	}
	for i, tt := range []struct {
		src *image.RGBA
		r   image.Rectangle
		o   tightOptions
	}{
		{img, image.Rect(0, 0, 50, 17), tightOptions{filter: tightFilterCopy, stream: 0}},
		{smooth, image.Rect(50, 0, 131, 40), tightOptions{filter: tightFilterGradient, stream: 1}},
		{img, image.Rect(0, 17, 16, 40), tightOptions{filter: tightFilterPalette, stream: 2}},  // a few colours
		{img, image.Rect(16, 17, 32, 33), tightOptions{filter: tightFilterPalette, stream: 3}}, // two
		{img, image.Rect(32, 17, 50, 40), tightOptions{filter: tightFilterCopy, explicit: true, stream: 1}},
		{smooth, image.Rect(0, 0, 50, 17), tightOptions{filter: tightFilterGradient, stream: 0}},
	} {
		msgs = append(msgs, putUpdate(e.tightBasic(tt.src, tt.r, tt.o)))
		draw.Draw(want, tt.r, tt.src, tt.r.Min, draw.Src)
		if i == 1 {
			// The same again: the stream refers back to its last
			// rectangle.
			msgs = append(msgs, putUpdate(e.tightBasic(tt.src, tt.r, tt.o)))
		}
	}
	d := newDecoder(context.Background(), bytes.NewReader(bytes.Join(msgs, nil)), sink, img.Rect.Size(), defaultLimits, discardLogger)
	d.strip = make([]byte, 3*3*w)
	for range msgs {
		if _, err := d.next(); err != nil {
			t.Fatal(err)
		}
	}
	equalImages(t, sink.Pixels(), want)
	if c := d.enc.counts; c[tightCopyRect] != 2 || c[tightGradientRect] != 3 || c[tightPaletteRect] != 2 {
		t.Errorf("counts %v", c)
	}
}

func TestTightReset(t *testing.T) {
	// A reset bit starts a stream again, with a new zlib header; the other
	// streams go on.
	img := pattern(40, 4, 8)
	e := newEncoder(wantPixelFormat)
	rect := func(y, stream int, reset byte) []byte {
		return e.tightBasic(img, image.Rect(0, y, 40, y+1), tightOptions{stream: stream, reset: reset})
	}
	msgs := slices.Concat(
		putUpdate(rect(0, 0, 0), rect(1, 1, 0)),
		// Resets of streams 0 and 2, the second unused, in a fill.
		putUpdate(e.tightFill(image.Rect(0, 3, 1, 4), red, 0b0101)),
		putUpdate(rect(2, 0, 0), rect(3, 1, 0b0010)),
	)
	d, sink, err := decodeOn(t, pattern(40, 4, 0), msgs)
	if err != nil {
		t.Fatal(err)
	}
	equalImages(t, sink.Pixels(), img)
	if got := d.enc.counts[tightFillRect]; got != 1 {
		t.Errorf("%d fills", got)
	}

	// Without the reset, the new header is taken for data.
	e = newEncoder(wantPixelFormat)
	first := rect(0, 0, 0)
	e.tight[0] = nil
	if _, _, err := decodeOn(t, pattern(40, 4, 0), putUpdate(first, rect(1, 0, 0))); err == nil {
		t.Error("a new stream without a reset decoded")
	}
}

func TestTightJPEG(t *testing.T) {
	photo := image.NewRGBA(image.Rect(0, 0, 37, 21))
	for y := range 21 {
		for x := range 37 {
			photo.SetRGBA(x, y, color.RGBA{uint8(x * 7), uint8(y * 11), uint8(x*y + 30), 0xff})
		}
	}
	gray := image.NewGray(image.Rect(0, 0, 9, 9))
	for i := range gray.Pix {
		gray.Pix[i] = uint8(i * 3)
	}
	var grayJPEG bytes.Buffer
	if err := jpeg.Encode(&grayJPEG, gray, nil); err != nil {
		t.Fatal(err)
	}
	e := newEncoder(wantPixelFormat)
	r := image.Rect(2, 3, 39, 24)
	screen := image.NewRGBA(image.Rect(0, 0, 40, 30))
	draw.Draw(screen, r, photo, image.Point{}, draw.Src)
	msg := e.tightJPEG(screen, r, 75, 0)
	gr := image.Rect(30, 21, 39, 30)
	grayMsg := tightRect(gr, tightJPEG<<4, appendCompact(nil, grayJPEG.Len()), grayJPEG.Bytes())
	empty := tightRect(image.Rect(0, 0, 0, 5), tightJPEG<<4, appendCompact(nil, 3), []byte{1, 2, 3})
	d, sink, err := decodeOn(t, pattern(40, 30, 5), putUpdate(msg, empty), putUpdate(grayMsg))
	if err != nil {
		t.Fatal(err)
	}
	// The pixels are image/jpeg's, and opaque.
	want := pattern(40, 30, 5)
	draw.Draw(want, r, jpegOf(t, msg), image.Point{}, draw.Src)
	draw.Draw(want, gr, mustDecodeJPEG(t, grayJPEG.Bytes()), image.Point{}, draw.Src)
	equalImages(t, sink.Pixels(), want)
	for y := range 30 {
		for x := range 40 {
			if a := sink.Pixels().RGBAAt(x, y).A; a != 0xff {
				t.Fatalf("pixel (%d, %d) has alpha %d", x, y, a)
			}
		}
	}
	if got := d.enc.counts[tightJPEGRect]; got != 2 {
		t.Errorf("%d JPEG rectangles, want 2", got)
	}
}

func mustDecodeJPEG(t *testing.T, b []byte) image.Image {
	t.Helper()
	img, err := jpeg.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	return img
}

func TestTightErrors(t *testing.T) {
	e := newEncoder(wantPixelFormat)
	small := image.NewRGBA(image.Rect(0, 0, 8, 8))
	var jpeg8 bytes.Buffer
	jpeg.Encode(&jpeg8, small, nil)
	jpegRect := func(r image.Rectangle) []byte {
		return putUpdate(tightRect(r, tightJPEG<<4, appendCompact(nil, jpeg8.Len()), jpeg8.Bytes()))
	}
	for _, tt := range []struct {
		name string
		msg  []byte
		want string
	}{
		{"PNG", putUpdate(tightRect(image.Rect(0, 0, 2, 2), 10<<4)), "tight compression type 10"},
		{"compression type 15", putUpdate(tightRect(image.Rect(0, 0, 2, 2), 15<<4)), "tight compression type 15"},
		{"filter 3", putUpdate(tightRect(image.Rect(0, 0, 2, 2), tightExplicitFilter<<4, []byte{3})), "tight filter 3"},
		{"palette index", putUpdate(tightRect(image.Rect(0, 0, 2, 2), tightExplicitFilter<<4, []byte{tightFilterPalette, 2},
			cpx(red), cpx(green), cpx(blue), []byte{0, 1, 2, 5})), "palette index 5 of 3 colours"},
		{"one colour, index 1", putUpdate(tightRect(image.Rect(0, 0, 2, 2), tightExplicitFilter<<4, []byte{tightFilterPalette, 0},
			cpx(red), []byte{0x00, 0x40})), "palette index 1 of 1 colour"},
		{"jpeg of another size", jpegRect(image.Rect(0, 0, 8, 7)), "a tight jpeg image of 8×8 for a rectangle of 8×7"},
		{"jpeg outside the screen", jpegRect(image.Rect(20, 20, 28, 28)), "outside the screen"},
		{"jpeg cut short", putUpdate(tightRect(image.Rect(0, 0, 8, 8), tightJPEG<<4, appendCompact(nil, jpeg8.Len()), jpeg8.Bytes()[:40])), "unexpected EOF"},
		{"compressed length beyond the bound", putUpdate(tightRect(image.Rect(0, 0, 4, 4), 0, appendCompact(nil, 48+6+1024+1))), "tight data of 48 bytes compressed to 1079"},
		{"compressed data short", putUpdate(tightRect(image.Rect(0, 0, 4, 4), 0, appendCompact(nil, 10), zlibBytes(make([]byte, 48))[:10])), "ends before the rectangle's pixels"},
		{"data short", putUpdate(tightRect(image.Rect(0, 0, 2, 1), 0, []byte{1, 2, 3})), "unexpected EOF"},
		{"basic outside the screen", putUpdate(e.tightBasic(pattern(30, 30, 0), image.Rect(20, 0, 30, 2), tightOptions{})), "outside the screen"},
		{"fill outside the screen", putUpdate(e.tightFill(image.Rect(0, 20, 30, 30), red, 0)), "outside the screen"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := decode(t, image.Pt(24, 24), defaultLimits, tt.msg)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("decode = %v, want an error with %q", err, tt.want)
			}
		})
	}
}

func TestTightJPEGErrors(t *testing.T) {
	// On a screen where the JPEG bounds, not the screen's, are what a
	// rectangle exceeds.
	screen := image.Pt(2100, 300)
	var jpeg8 bytes.Buffer
	jpeg.Encode(&jpeg8, image.NewGray(image.Rect(0, 0, 8, 8)), nil)
	head, rest := jpeg8.Bytes()[:2], jpeg8.Bytes()[2:]
	for _, tt := range []struct {
		name string
		r    image.Rectangle
		data []byte
		want string
	}{
		{"too wide", image.Rect(0, 0, 2049, 1), jpeg8.Bytes(), "exceeds 2048 pixels wide"},
		{"too large", image.Rect(0, 0, 256, 257), jpeg8.Bytes(), "or 65536 in all"},
		{"not a JPEG", image.Rect(0, 0, 8, 8), []byte{1, 2, 3, 4}, "tight jpeg: no start of image"},
		{"no end", image.Rect(0, 0, 8, 8), jpeg8.Bytes()[:jpeg8.Len()-2], "the image ends within a scan"},
		{"a segment past the end", image.Rect(0, 0, 8, 8), slices.Concat(head, []byte{0xff, 0xfe, 0x40, 0}), "beyond the image's"},
		{"progressive", image.Rect(0, 0, 16, 16), progressiveJPEG(16, 16, 0), "SOF2"},
		{"lossless", image.Rect(0, 0, 8, 8), slices.Concat(head, jpegSegment(0xc3, jpegSOF(8, 8)), rest), "SOF3"},
		{"more scans than components", image.Rect(0, 0, 16, 16), baselineJPEG(16, 16, 2), "more scans than components: the frame has 1"},
		{"a scan before the frame", image.Rect(0, 0, 8, 8), slices.Concat(head, jpegSegment(0xda, []byte{1, 1, 0, 0, 63, 0}), rest), "a scan before the frame header"},
		{"two frames", image.Rect(0, 0, 8, 8), slices.Concat(head, jpegSegment(0xc0, jpegSOF(8, 8)), rest), "a second frame"},
		{"arithmetic conditioning", image.Rect(0, 0, 8, 8), slices.Concat(head, jpegSegment(0xcc, []byte{0, 0}), rest), "marker 0xcc"},
		{"no scan", image.Rect(0, 0, 8, 8), slices.Concat(head, jpegSegment(0xc0, jpegSOF(8, 8)), []byte{0xff, 0xd9}), "no scan"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			msg := putUpdate(tightRect(tt.r, tightJPEG<<4, appendCompact(nil, len(tt.data)), tt.data))
			_, err := decode(t, screen, defaultLimits, msg)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("decode = %v, want an error with %q", err, tt.want)
			}
		})
	}
}

func TestTightJPEGScanBombs(t *testing.T) {
	// image/jpeg decodes every scan of an image, each a pass over its
	// blocks, so that thousands of scans of a few bytes each cost seconds.
	// Such images are refused before image/jpeg sees them, which the test
	// counts rather than times: a time bound would also time the sink's
	// screen, and fail on a busy machine.
	decodes := 0
	t.Cleanup(func() { decodeJPEG = jpeg.Decode })
	decodeJPEG = func(r io.Reader) (image.Image, error) {
		decodes++
		return jpeg.Decode(r)
	}
	for _, tt := range []struct {
		name string
		r    image.Rectangle
		data []byte
		want string
	}{
		// 13 s without the bounds. The pixel bound refuses it before its
		// markers are read.
		{"progressive, 2048×2048, past the pixel bound", image.Rect(0, 0, 2048, 2048), progressiveJPEG(2048, 2048, 16000), "or 65536 in all"},
		// Within the pixel bound, the scans are what is refused. 190 ms
		// without the check, 0.6 µs a byte.
		{"progressive, 256×256, 16000 scans", image.Rect(0, 0, 256, 256), progressiveJPEG(256, 256, 16000), "SOF2"},
		// 140 ms without it, 0.5 µs a byte.
		{"baseline, 256×256, 1000 scans", image.Rect(0, 0, 256, 256), baselineJPEG(256, 256, 1000), "more scans than components: the frame has 1"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			decodes = 0
			msg := putUpdate(tightRect(tt.r, tightJPEG<<4, appendCompact(nil, len(tt.data)), tt.data))
			_, err := decode(t, tt.r.Max, defaultLimits, msg)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("decode = %v, want an error with %q", err, tt.want)
			}
			if decodes != 0 {
				t.Errorf("image/jpeg decoded %d images, want none", decodes)
			}
		})
	}
	// The same images of one scan decode.
	decodes = 0
	r := image.Rect(0, 0, 256, 256)
	data := baselineJPEG(256, 256, 1)
	sink, err := decode(t, r.Max, defaultLimits, putUpdate(tightRect(r, tightJPEG<<4, appendCompact(nil, len(data)), data)))
	if err != nil {
		t.Fatalf("a baseline JPEG of one scan: %v", err)
	}
	if decodes != 1 {
		t.Errorf("image/jpeg decoded %d images, want 1", decodes)
	}
	if c := sink.Pixels().RGBAAt(100, 100); c.R != c.G || c.G != c.B || c.A != 0xff {
		t.Errorf("a grey JPEG drew %v", c)
	}
}

// jpegBits writes JPEG entropy-coded data, the most significant bit
// first, with a 0x00 stuffed after each 0xFF.
type jpegBits struct {
	out  []byte
	acc  byte
	nacc uint
}

func (w *jpegBits) bits(v uint32, n uint) {
	for i := int(n) - 1; i >= 0; i-- {
		w.acc = w.acc<<1 | byte(v>>uint(i)&1)
		if w.nacc++; w.nacc == 8 {
			w.out = append(w.out, w.acc)
			if w.acc == 0xff {
				w.out = append(w.out, 0)
			}
			w.acc, w.nacc = 0, 0
		}
	}
}

// flush pads the last byte with ones and returns the data.
func (w *jpegBits) flush() []byte {
	for w.nacc != 0 {
		w.bits(1, 1)
	}
	return w.out
}

// jpegSegment is a JPEG marker segment.
func jpegSegment(marker byte, payload []byte) []byte {
	n := len(payload) + 2
	return append([]byte{0xff, marker, byte(n >> 8), byte(n)}, payload...)
}

// jpegSOF is the frame header of a greyscale image of w×h.
func jpegSOF(w, h int) []byte {
	return []byte{8, byte(h >> 8), byte(h), byte(w >> 8), byte(w), 1, 1, 0x11, 0}
}

// jpegHuffman is a Huffman table of class 0 (DC) or 1 (AC) with one code,
// of one bit, for symbol.
func jpegHuffman(class, symbol byte) []byte {
	return slices.Concat([]byte{class << 4, 1}, make([]byte, 15), []byte{symbol})
}

// grayJPEG is a greyscale JPEG's start, up to its first scan: a
// quantization table of ones, the frame header of type sof, and Huffman
// tables for DC and AC.
func grayJPEG(sof byte, w, h int, ac byte) []byte {
	return slices.Concat([]byte{0xff, 0xd8},
		jpegSegment(0xdb, append([]byte{0}, bytes.Repeat([]byte{1}, 64)...)),
		jpegSegment(sof, jpegSOF(w, h)),
		jpegSegment(0xc4, jpegHuffman(0, 0)), // DC: a difference of 0
		jpegSegment(0xc4, jpegHuffman(1, ac)),
	)
}

// baselineJPEG is a baseline greyscale JPEG of w×h in the given number of
// scans, each of a DC difference of 0 and an end of block for every
// block.
func baselineJPEG(w, h, scans int) []byte {
	b := grayJPEG(0xc0, w, h, 0x00) // AC: end of block
	for range scans {
		b = append(b, jpegSegment(0xda, []byte{1, 1, 0x00, 0, 63, 0})...)
		var bw jpegBits
		for range ((w + 7) / 8) * ((h + 7) / 8) {
			bw.bits(0, 2)
		}
		b = append(b, bw.flush()...)
	}
	return append(b, 0xff, 0xd9)
}

// progressiveJPEG is a progressive greyscale JPEG of w×h: a DC scan, and
// the given number of AC scans of a few bytes each, whose end-of-band
// runs span every block.
func progressiveJPEG(w, h, scans int) []byte {
	b := grayJPEG(0xc2, w, h, 0xe0) // AC: EOB14, a run of 2^14 bands or more
	b = append(b, jpegSegment(0xda, []byte{1, 1, 0x00, 0, 0, 0})...)
	var dc jpegBits
	for range ((w + 7) / 8) * ((h + 7) / 8) {
		dc.bits(0, 1)
	}
	b = append(b, dc.flush()...)
	for range scans {
		b = append(b, jpegSegment(0xda, []byte{1, 1, 0x00, 1, 63, 0})...)
		var bw jpegBits
		for range 3 {
			bw.bits(0, 1)
			bw.bits(0x3fff, 14)
		}
		b = append(b, bw.flush()...)
	}
	return append(b, 0xff, 0xd9)
}

func TestCompactLength(t *testing.T) {
	for _, n := range []int{0, 1, 127, 128, 16383, 16384, 1<<22 - 1} {
		d := newDecoder(context.Background(), bytes.NewReader(appendCompact(nil, n)), nil, image.Pt(1, 1), defaultLimits, discardLogger)
		got, err := d.compactLength()
		if err != nil || got != n {
			t.Errorf("compactLength of %d = %d, %v", n, got, err)
		}
	}
}
