// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package rdp

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"math/rand/v2"
	"slices"
	"sync"
	"testing"
	"time"
)

// Each order of the interleaved RLE stream, [MS-RDPBCGR] 2.2.9.1.1.3.1.2.4,
// at 8 bits per pixel. Rows are listed in stream order, bottom-up.
func TestRLEOrders(t *testing.T) {
	for _, tc := range []struct {
		name  string
		width int
		src   []byte
		want  []uint32
	}{
		{"colour image then background run copies the row",
			4, []byte{0x84, 1, 2, 3, 4, 0x04}, []uint32{1, 2, 3, 4, 1, 2, 3, 4}},
		{"background run on the first row is black, foreground run xors white",
			4, []byte{0x04, 0x24}, []uint32{0, 0, 0, 0, 0xff, 0xff, 0xff, 0xff}},
		{"a background run after a background run starts with the foreground",
			4, []byte{0x02, 0x02}, []uint32{0, 0, 0xff, 0}},
		{"a background run after the first row inserts above xor foreground",
			2, []byte{0x82, 1, 2, 0x01, 0x01}, []uint32{1, 2, 1, 2 ^ 0xff}},
		{"set foreground and run (lite)",
			4, []byte{0xc4, 7, 0x24}, []uint32{7, 7, 7, 7, 0, 0, 0, 0}},
		{"dithered run (lite)",
			4, []byte{0xe2, 1, 2}, []uint32{1, 2, 1, 2}},
		{"colour run",
			4, []byte{0x64, 9}, []uint32{9, 9, 9, 9}},
		{"foreground/background image on the first row",
			8, []byte{0x41, 0xa5}, []uint32{0xff, 0, 0xff, 0, 0, 0xff, 0, 0xff}},
		{"foreground/background image with its length in the next byte",
			3, []byte{0x40, 2, 0x05}, []uint32{0xff, 0, 0xff}},
		{"set foreground and foreground/background image (lite)",
			4, []byte{0xd0, 3, 7, 0x09}, []uint32{7, 0, 0, 7}},
		{"mega-mega colour run",
			4, []byte{0xf3, 4, 0, 5}, []uint32{5, 5, 5, 5}},
		{"mega-mega colour image",
			3, []byte{0xf4, 3, 0, 4, 5, 6}, []uint32{4, 5, 6}},
		{"mega-mega set foreground run",
			2, []byte{0xf6, 2, 0, 0x33}, []uint32{0x33, 0x33}},
		{"mega-mega dithered run",
			4, []byte{0xf8, 2, 0, 8, 9}, []uint32{8, 9, 8, 9}},
		{"mega-mega background and foreground runs",
			2, []byte{0x82, 1, 2, 0xf0, 2, 0, 0xf1, 2, 0}, []uint32{1, 2, 1, 2, 1 ^ 0xff, 2 ^ 0xff}},
		{"special white and black",
			4, []byte{0xfd, 0xfe, 0xfd, 0xfe}, []uint32{0xff, 0, 0xff, 0}},
		{"special foreground/background 1 on the second row",
			8, []byte{0x88, 1, 2, 3, 4, 5, 6, 7, 8, 0xf9}, []uint32{1, 2, 3, 4, 5, 6, 7, 8, 1 ^ 0xff, 2 ^ 0xff, 3, 4, 5, 6, 7, 8}},
		// The rest, from the review, worked by hand from the pseudocode
		// of [MS-RDPBCGR] 3.1.9. An order that starts on the first row
		// keeps its first-row semantics past the end of the row.
		{"mega-mega foreground/background image across the first row",
			4, []byte{0xf2, 10, 0, 0x0f, 0x02}, []uint32{0xff, 0xff, 0xff, 0xff, 0, 0, 0, 0, 0, 0xff, 0, 0}},
		{"mega-mega set foreground and foreground/background image",
			4, []byte{0xf7, 3, 0, 0x11, 0x05}, []uint32{0x11, 0, 0x11, 0}},
		{"foreground/background image on the second row xors the row above",
			2, []byte{0x82, 1, 2, 0x40, 1, 0x01}, []uint32{1, 2, 0xfe, 2}},
		{"set foreground and foreground/background image (lite) with its length in the next byte",
			4, []byte{0xd0, 1, 0x22, 0x02}, []uint32{0, 0x22, 0, 0}},
		{"special foreground/background 1 on the first row",
			8, []byte{0xf9}, []uint32{0xff, 0xff, 0, 0, 0, 0, 0, 0}},
		{"foreground/background image of 9 pixels takes two masks",
			9, []byte{0x40, 8, 0x01, 0x01}, []uint32{0xff, 0, 0, 0, 0, 0, 0, 0, 0xff}},
		{"special foreground/background 2 on the second row",
			8, []byte{0x88, 1, 2, 3, 4, 5, 6, 7, 8, 0xfa}, []uint32{1, 2, 3, 4, 5, 6, 7, 8, 1 ^ 0xff, 2, 3 ^ 0xff, 4, 5, 6, 7, 8}},
		{"colour run with its length in the next byte",
			40, []byte{0x60, 8, 3}, slices.Repeat([]uint32{3}, 40)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := make([]uint32, len(tc.want))
			if err := rleDecode(tc.src, got, tc.width, 8); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("got %x, want %x", got, tc.want)
			}
		})
	}
}

func TestRLEPixelSizes(t *testing.T) {
	// A colour image of two pixels, then a set-foreground run that xors
	// them, at each depth.
	for _, tc := range []struct {
		bpp   int
		src   []byte
		want  []uint32
		white uint32
	}{
		{15, []byte{0x82, 0x34, 0x12, 0x78, 0x56, 0xfd}, []uint32{0x1234, 0x5678}, 0x7fff},
		{16, []byte{0x82, 0x34, 0x12, 0x78, 0x56, 0xfd}, []uint32{0x1234, 0x5678}, 0xffff},
		{24, []byte{0x82, 0x56, 0x34, 0x12, 0xbc, 0x9a, 0x78, 0xfd}, []uint32{0x123456, 0x789abc}, 0xffffff},
	} {
		got := make([]uint32, 3)
		if err := rleDecode(tc.src, got, 2, tc.bpp); err != nil {
			t.Fatal(err)
		}
		if want := append(tc.want, tc.white); !slices.Equal(got, want) {
			t.Errorf("%d bpp: got %x, want %x", tc.bpp, got, want)
		}
	}
}

func TestRLEErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  []byte
	}{
		{"overrun", []byte{0x64, 1, 0x64, 1}},
		{"truncated pixel", []byte{0x84, 1, 2}},
		{"truncated run length", []byte{0x60}},
		{"truncated mega-mega length", []byte{0xf3, 4}},
		{"unknown order", []byte{0xa0}},
	} {
		if err := rleDecode(tc.src, make([]uint32, 4), 4, 8); err == nil {
			t.Errorf("%s: no error", tc.name)
		}
	}
	if err := rleDecode([]byte{0}, make([]uint32, 4), 4, 32); err == nil {
		t.Error("32 bits per pixel: no error")
	}
}

// planarEncode is a planar encoder for the tests: RLE planes in the
// order the decoder expects, each row as values (the first) or as
// differences, in raw bytes and runs.
func planarEncode(planes [][]byte, w, h int, alpha bool) []byte {
	hdr := byte(planarRLE)
	if !alpha {
		hdr |= planarNA
		planes = planes[1:]
	}
	out := []byte{hdr}
	for _, p := range planes {
		for y := range h {
			vals := make([]byte, w)
			for x := range w {
				if y == 0 {
					vals[x] = p[x]
					continue
				}
				d := int8(p[y*w+x] - p[(y-1)*w+x])
				if d >= 0 {
					vals[x] = byte(d) * 2
				} else {
					vals[x] = byte(-2*int(d) - 1)
				}
			}
			for x := 0; x < w; {
				// Raw bytes up to where a run of three or more starts.
				start := x
				for x < w && x-start < 15 {
					x++
					k := x
					for k < w && vals[k] == vals[x-1] {
						k++
					}
					if k-x >= 3 {
						break
					}
				}
				raw := vals[start:x]
				run := 0
				for x < w && vals[x] == vals[x-1] && run < 47 {
					x++
					run++
				}
				switch {
				case run == 0:
					out = append(out, byte(len(raw))<<4)
				case run < 3:
					x -= run
					out = append(out, byte(len(raw))<<4)
				case run <= 15:
					out = append(out, byte(len(raw))<<4|byte(run))
				default:
					out = append(out, byte(len(raw))<<4)
					out = append(out, raw...)
					raw = nil
					if run < 32 {
						out = append(out, byte(run-16)<<4|1)
					} else {
						out = append(out, byte(run-32)<<4|2)
					}
				}
				out = append(out, raw...)
			}
		}
	}
	return out
}

func TestPlanarRoundTrip(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	for _, tc := range []struct {
		w, h  int
		alpha bool
		gen   func(x, y int) byte
	}{
		{64, 64, false, func(x, y int) byte { return byte(y) }},                      // a gradient, all runs
		{17, 5, true, func(x, y int) byte { return byte(rng.IntN(256)) }},            // noise, all raw
		{100, 3, false, func(x, y int) byte { return byte(x / 40 * 50) }},            // long runs
		{9, 9, true, func(x, y int) byte { return byte((x*7 + y*13) % 5 * 60) }},     // mixed
		{40, 2, false, func(x, y int) byte { return byte(200 - y*150 + x%2*(y*3)) }}, // negative differences
	} {
		planes := make([][]byte, 4)
		for i := range planes {
			planes[i] = make([]byte, tc.w*tc.h)
			for y := range tc.h {
				for x := range tc.w {
					planes[i][y*tc.w+x] = tc.gen(x+i, y)
				}
			}
		}
		src := planarEncode(planes, tc.w, tc.h, tc.alpha)
		var d decoder
		dst := make([]byte, 4*tc.w*tc.h)
		if err := d.planarDecode(src, tc.w, tc.h, dst, 4*tc.w, planarGFX); err != nil {
			t.Fatalf("%dx%d: %v", tc.w, tc.h, err)
		}
		for y := range tc.h {
			for x := range tc.w {
				i := y*tc.w + x
				got := color.RGBA{dst[4*i], dst[4*i+1], dst[4*i+2], dst[4*i+3]}
				want := color.RGBA{planes[1][i], planes[2][i], planes[3][i], 0xff}
				if got != want {
					t.Fatalf("%dx%d: pixel (%d, %d) is %v, want %v", tc.w, tc.h, x, y, got, want)
				}
			}
		}
	}
}

func TestPlanarRawAndBottomUp(t *testing.T) {
	// Two rows of two pixels, raw planes with alpha, stored bottom-up.
	src := []byte{0,
		0xff, 0xff, 0xff, 0xff, // alpha
		1, 2, 3, 4, // red
		5, 6, 7, 8, // green
		9, 10, 11, 12, // blue
		0} // pad
	var d decoder
	dst := make([]byte, 16)
	if err := d.planarDecode(src, 2, 2, dst, 8, planarBitmap); err != nil {
		t.Fatal(err)
	}
	want := []byte{3, 7, 11, 0xff, 4, 8, 12, 0xff, 1, 5, 9, 0xff, 2, 6, 10, 0xff}
	if !bytes.Equal(dst, want) {
		t.Errorf("got %v, want %v", dst, want)
	}
	if err := d.planarDecode(src[:10], 2, 2, dst, 8, planarBitmap); err == nil {
		t.Error("truncated raw planes: no error")
	}
	for _, hdr := range []byte{planarNA | 1, planarNA | planarCS | 1} {
		if err := d.planarDecode(planarYCoCg(gradient(2, 2), hdr), 2, 2, dst, 8, planarBitmap); err == nil {
			t.Errorf("AYCoCg of header %#02x in a bitmap update: no error", hdr)
		}
		if err := d.planarDecode(planarYCoCg(gradient(2, 2), hdr), 2, 2, dst, 8, planarGFX); err != nil {
			t.Errorf("AYCoCg of header %#02x on a surface: %v", hdr, err)
		}
	}
	if err := d.planarDecode([]byte{planarRLE | planarNA, 0x03}, 2, 2, dst, 8, planarBitmap); err == nil {
		t.Error("run past the row: no error")
	}
}

// bitmapUpdate encodes a TS_UPDATE_BITMAP_DATA with one rectangle.
func bitmapUpdate(dest image.Rectangle, w, h, bpp int, flags uint16, data []byte) []byte {
	var b writer
	b.u16(updateBitmap)
	b.u16(1)
	b.u16(dest.Min.X)
	b.u16(dest.Min.Y)
	b.u16(dest.Max.X - 1)
	b.u16(dest.Max.Y - 1)
	b.u16(w)
	b.u16(h)
	b.u16(bpp)
	b.u16(int(flags))
	b.u16(len(data))
	b.bytes(data)
	return b.b
}

func TestBitmapUncompressedAndClipped(t *testing.T) {
	// A 4×2 bitmap at 16 bits per pixel, of which the destination shows
	// 3×2, rows bottom-up.
	px := func(r, g, b uint16) []byte {
		return binary.LittleEndian.AppendUint16(nil, r<<11|g<<5|b)
	}
	var data []byte
	for _, p := range [][]byte{px(31, 0, 0), px(0, 63, 0), px(0, 0, 31), px(31, 63, 31), // bottom
		px(1, 2, 3), px(4, 5, 6), px(7, 8, 9), px(0, 0, 0)} { // top
		data = append(data, p...)
	}
	c := testClient(16, 16)
	scr := &screen{}
	scr.Resize(16, 16)
	err := parseBitmapUpdate(bitmapUpdate(image.Rect(5, 6, 8, 8), 4, 2, 16, 0, data)[2:],
		func(b *bitmapData) error { return c.drawBitmap(scr, b) })
	if err != nil {
		t.Fatal(err)
	}
	if got, want := scr.at(5, 7), (color.RGBA{0xff, 0, 0, 0xff}); got != want {
		t.Errorf("bottom left %v, want %v", got, want)
	}
	if got, want := scr.at(7, 7), (color.RGBA{0, 0, 0xff, 0xff}); got != want {
		t.Errorf("bottom right %v, want %v", got, want)
	}
	if got, want := scr.at(6, 6), (color.RGBA{expand5(4), expand6(5), expand5(6), 0xff}); got != want {
		t.Errorf("top middle %v, want %v", got, want)
	}
	if got := scr.at(8, 7); got != (color.RGBA{}) {
		t.Errorf("the clipped column was drawn: %v", got)
	}
	if c.Stats().Bitmaps["raw16"] != 1 {
		t.Errorf("stats %v", c.Stats().Bitmaps)
	}
}

func TestBitmapCompressionHeader(t *testing.T) {
	// A compressed 24-bit bitmap with a TS_CD_HEADER, which the decoder
	// skips.
	rle := []byte{0x62, 0x10, 0x20, 0x30}
	data := append(make([]byte, 8), rle...)
	var got *bitmapData
	err := parseBitmapUpdate(bitmapUpdate(image.Rect(0, 0, 2, 1), 2, 1, 24, bitmapCompression, data)[2:],
		func(b *bitmapData) error { got = b; return nil })
	if err != nil || got == nil || !bytes.Equal(got.data, rle) {
		t.Fatalf("got %+v, %v", got, err)
	}
	var d decoder
	rgba, err := d.decode(got)
	if err != nil {
		t.Fatal(err)
	}
	if want := []byte{0x30, 0x20, 0x10, 0xff, 0x30, 0x20, 0x10, 0xff}; !bytes.Equal(rgba, want) {
		t.Errorf("got %v, want %v", rgba, want)
	}
}

// TestBitmapKind checks the names that the statistics count bitmaps
// under, which the integration and replay tests look up by codec.
func TestBitmapKind(t *testing.T) {
	for _, tc := range []struct {
		bpp        int
		compressed bool
		want       string
	}{
		{8, false, "raw8"}, {15, false, "raw15"}, {16, false, "raw16"}, {24, false, "raw24"}, {32, false, "raw32"},
		{8, true, "rle8"}, {15, true, "rle15"}, {16, true, "rle16"}, {24, true, "rle24"}, {32, true, "planar"},
		// Depths that decode does not take, which never reach the count.
		{0, false, "raw0"}, {12, true, "rle12"}, {33, false, "raw33"}, {-1, true, "rle-1"},
	} {
		if got := bitmapKind(&bitmapData{bpp: tc.bpp, compressed: tc.compressed}); got != tc.want {
			t.Errorf("bitmapKind(%d, compressed %t) = %q, want %q", tc.bpp, tc.compressed, got, tc.want)
		}
	}
	b := &bitmapData{bpp: 16, compressed: true}
	if a := testing.AllocsPerRun(10, func() { _ = bitmapKind(b) }); a != 0 {
		t.Errorf("naming a bitmap allocated %v times, want 0", a)
	}
}

func TestPointerShapes(t *testing.T) {
	var d decoder
	// 2×2 at 24 bits: XOR rows padded to 2 bytes (6 → 6), AND rows to 2.
	// Bottom row: red opaque, transparent; top row: inverted, white opaque.
	xor := []byte{0, 0, 0xff, 0, 0, 0, // bottom
		0xff, 0xff, 0xff, 0xff, 0xff, 0xff} // top
	and := []byte{0x40, 0, // bottom: second pixel transparent
		0x80, 0} // top: first pixel transparent, xor white: inverted
	img, err := d.pointerImage(2, 2, 24, xor, and)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		x, y int
		want color.RGBA
	}{
		{0, 1, color.RGBA{0xff, 0, 0, 0xff}},
		{1, 1, color.RGBA{}},
		{0, 0, color.RGBA{0, 0, 0, 0xff}},
		{1, 0, color.RGBA{0xff, 0xff, 0xff, 0xff}},
	} {
		if got := img.RGBAAt(tc.x, tc.y); got != tc.want {
			t.Errorf("(%d, %d) is %v, want %v", tc.x, tc.y, got, tc.want)
		}
	}
	// 32 bits with alpha: premultiplied.
	img, err = d.pointerImage(1, 1, 32, []byte{0, 0, 0xff, 0x80, 0, 0}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := img.RGBAAt(0, 0), (color.RGBA{0x80, 0, 0, 0x80}); got != want {
		t.Errorf("alpha pointer %v, want %v", got, want)
	}
	if _, err := d.pointerImage(4, 4, 24, xor, and); err == nil {
		t.Error("short XOR mask: no error")
	}
}

// testClient is a client in the active state with a w×h desktop, whose
// writes go nowhere. Where it would wait, as for its decoding budget, it
// fails with errPaced, so that a fuzzer's execution decodes at most one
// bucket of pixels.
func testClient(w, h int) *Client {
	c, clock := testClock(w, h)
	clock.err = errPaced
	return c
}

// errPaced is the error of a test client that would wait.
var errPaced = errors.New("the client would wait")

// testClock returns a client as testClient does, whose waits pass at
// once on the clock it returns.
func testClock(w, h int) (*Client, *fakeClock) {
	clock := &fakeClock{t: time.Unix(1<<30, 0)}
	c := &Client{
		width: w, height: h,
		stats: newStats(),
		t:     &transport{conn: discardConn{}},
		now:   clock.now,
		sleep: clock.sleep,
	}
	return c, clock
}

// fakeClock is a client's clock in the tests. Its sleeps pass at once
// and add up, or fail with err. The reader sleeps while the channel
// writer reads the time, so both take mu; a test reads slept once the
// client is done.
type fakeClock struct {
	mu    sync.Mutex
	t     time.Time
	slept time.Duration
	err   error
}

func (f *fakeClock) now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.t
}

func (f *fakeClock) sleep(d time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.t = f.t.Add(d)
	f.slept += d
	return nil
}
