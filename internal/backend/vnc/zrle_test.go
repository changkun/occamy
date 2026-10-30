// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package vnc

import (
	"bytes"
	"compress/flate"
	"compress/zlib"
	"context"
	"encoding/binary"
	"image"
	"image/color"
	"image/draw"
	"slices"
	"strings"
	"testing"

	"changkun.de/x/occamy/internal/backend/backendtest"
)

// cpx is c as ZRLE's and Tight's compact pixel in the backend's format.
func cpx(c color.RGBA) []byte { return []byte{c.R, c.G, c.B} }

// zrleRaw returns a ZRLE rectangle of r with z, its compressed data.
func zrleRaw(r image.Rectangle, z []byte) []byte {
	p := rectHeader(r, encZRLE)
	p = binary.BigEndian.AppendUint32(p, uint32(len(z)))
	return append(p, z...)
}

func TestZRLEByHand(t *testing.T) {
	// A rectangle of 66×65 at (1, 2) has tiles of 64×64, 2×64, 64×1 and
	// 2×1.
	r := image.Rect(1, 2, 67, 67)
	var data []byte
	// Tile 1: palette RLE of three colours: a run of 64×40 red, which
	// takes a run length of eleven bytes, a single green pixel, and blue
	// for the rest.
	data = append(data, 128+3)
	data = append(append(append(data, cpx(red)...), cpx(green)...), cpx(blue)...)
	data = appendRun(append(data, 0|0x80), 64*40)
	data = append(data, 1)
	data = appendRun(append(data, 2|0x80), 64*24-1)
	// Tile 2: a packed palette of two colours, one bit a pixel and a byte
	// a row: white on the left, yellow on the right.
	data = append(append(append(data, 2), cpx(white)...), cpx(yellow)...)
	for range 64 {
		data = append(data, 0b01_000000)
	}
	// Tile 3: plain RLE, 40 green then 24 red.
	data = appendRun(append(append(data, 128), cpx(green)...), 40)
	data = appendRun(append(data, cpx(red)...), 24)
	// Tile 4: raw.
	data = append(append(append(data, 0), cpx(blue)...), cpx(white)...)

	// A second rectangle, 5×3 at (70, 0), of one solid tile, and a third,
	// 7×2 at (70, 3), packed with 3 colours at 2 bits a pixel; and a
	// fourth, 3×2 at (70, 5), packed with 5 colours at 4 bits.
	second := append([]byte{1}, cpx(yellow)...)
	third := append(append(append([]byte{3}, cpx(red)...), cpx(green)...), cpx(blue)...)
	third = append(third, 0b00_01_10_00, 0b01_10_00_00, 0b10_10_10_10, 0b01_01_01_00)
	five := []color.RGBA{red, green, blue, yellow, white}
	fourth := []byte{5}
	for _, c := range five {
		fourth = append(fourth, cpx(c)...)
	}
	fourth = append(fourth, 0x01, 0x20, 0x34, 0x00)

	e := newEncoder(wantPixelFormat)
	msg := putUpdate(
		zrleRaw(r, e.compress(&e.zstream, data)),
		zrleRaw(image.Rect(70, 0, 75, 3), e.compress(&e.zstream, second)),
		zrleRaw(image.Rect(70, 3, 77, 5), e.compress(&e.zstream, third)),
		zrleRaw(image.Rect(70, 5, 73, 7), e.compress(&e.zstream, fourth)),
	)
	base := pattern(80, 70, 2)
	d, sink, err := decodeOn(t, base, msg)
	if err != nil {
		t.Fatal(err)
	}
	want := image.NewRGBA(base.Rect)
	copy(want.Pix, base.Pix)
	fillRect(want, image.Rect(1, 2, 65, 42), red)
	fillRect(want, image.Rect(1, 42, 2, 43), green)
	fillRect(want, image.Rect(2, 42, 65, 43), blue)
	fillRect(want, image.Rect(1, 43, 65, 66), blue)
	fillRect(want, image.Rect(65, 2, 66, 66), white)
	fillRect(want, image.Rect(66, 2, 67, 66), yellow)
	fillRect(want, image.Rect(1, 66, 41, 67), green)
	fillRect(want, image.Rect(41, 66, 65, 67), red)
	fillRect(want, image.Rect(65, 66, 66, 67), blue)
	fillRect(want, image.Rect(66, 66, 67, 67), white)
	fillRect(want, image.Rect(70, 0, 75, 3), yellow)
	for i, c := range []color.RGBA{red, green, blue, red, green, blue, red, blue, blue, blue, blue, green, green, green} {
		want.SetRGBA(70+i%7, 3+i/7, c)
	}
	for i, c := range []color.RGBA{red, green, blue, yellow, white, red} {
		want.SetRGBA(70+i%3, 5+i/3, c)
	}
	equalImages(t, sink.Pixels(), want)
	want4 := [numKinds]int{zrleRawTile: 1, zrleSolidTile: 1, zrlePackedTile: 3, zrlePlainRLETile: 1, zrlePaletteRLETile: 1}
	if d.enc.counts != want4 {
		t.Errorf("counts %v, want %v", d.enc.counts, want4)
	}
}

func TestZRLERoundTrip(t *testing.T) {
	// The fake's encoder on tiles of every kind, with edge tiles narrower
	// and shorter than 64 pixels, over three updates of one stream, of
	// which the last repeats the first: its data refers back to the
	// first's.
	img := mixed(64*4+9, 64*2+17, zrleTile)
	r := image.Rect(2, 1, 2+img.Rect.Dx(), 1+img.Rect.Dy())
	screen := pattern(r.Max.X+5, r.Max.Y+2, 6)
	draw.Draw(screen, r, img, image.Point{}, draw.Src)
	other := pattern(screen.Rect.Dx(), screen.Rect.Dy(), 77)
	e := newEncoder(wantPixelFormat)
	first := e.zrle(screen, r)
	second := e.zrle(other, image.Rect(0, 0, 10, 10))
	third := e.zrle(screen, r)
	if len(third) > len(first)/2 {
		t.Errorf("the repeated rectangle took %d bytes after %d: it does not refer back", len(third), len(first))
	}
	d, sink, err := decodeOn(t, pattern(screen.Rect.Dx(), screen.Rect.Dy(), 6), putUpdate(first), putUpdate(second, third))
	if err != nil {
		t.Fatal(err)
	}
	draw.Draw(screen, image.Rect(0, 0, 10, 10), other, image.Point{}, draw.Src)
	draw.Draw(screen, r, img, image.Point{}, draw.Src)
	equalImages(t, sink.Pixels(), screen)
	for k := zrleRawTile; k <= zrlePaletteRLETile; k++ {
		if d.enc.counts[k] == 0 {
			t.Errorf("no %v tile in %v", k, d.enc.counts)
		}
	}
}

func TestZRLEForcedModes(t *testing.T) {
	// Each subencoding on the same tiles, so that one the encoder would
	// not pick still decodes.
	img := mixed(64*3+1, 64+30, zrleTile)
	for _, force := range []zrleForce{0, 2, 128, 130} {
		e := newEncoder(wantPixelFormat)
		var data []byte
		tiles(img.Rect, zrleTile, func(tr image.Rectangle) { data = e.zrleTile(data, img, tr, force) })
		_, sink, err := decodeOn(t, pattern(img.Rect.Dx(), img.Rect.Dy(), 1), putUpdate(e.zrleRect(img.Rect, data)))
		if err != nil {
			t.Fatalf("subencoding %d: %v", force, err)
		}
		equalImages(t, sink.Pixels(), img)
	}
}

// zlibBytes compresses p with a new zlib stream and the flush, as a
// server's first rectangle.
func zlibBytes(p []byte) []byte {
	var buf bytes.Buffer
	w := zlib.NewWriter(&buf)
	w.Write(p)
	w.Flush()
	return buf.Bytes()
}

func TestZRLEErrors(t *testing.T) {
	r := image.Rect(0, 0, 4, 2)
	rect := func(data []byte) []byte { return putUpdate(zrleRaw(r, zlibBytes(data))) }
	solid := append([]byte{1}, cpx(red)...)
	// A zlib stream that ends: Close writes a final block.
	var final bytes.Buffer
	w := zlib.NewWriter(&final)
	w.Write(solid)
	w.Close()
	// Corrupt deflate data: a block of the reserved type 3.
	corrupt := []byte{0x78, 0x9c, 0x07, 0, 0, 0, 0}
	// Data padded after the flush, beyond the 16 bytes that are kept.
	padded := append(zlibBytes(solid), make([]byte, 17)...)
	for _, tt := range []struct {
		name string
		msg  []byte
		want string
	}{
		{"unused subencoding 17", rect([]byte{17}), "unused subencoding 17"},
		{"unused subencoding 129", rect([]byte{129}), "unused subencoding 129"},
		{"plain run past the tile", rect(append(append([]byte{128}, cpx(red)...), 8)), "a run of 9 or more pixels with 8 left"},
		{"255s forever", rect(append(append([]byte{128}, cpx(red)...), bytes.Repeat([]byte{255}, 1000)...)), "a run of 256 or more pixels with 8 left"},
		{"palette run past the tile", rect(append(append([]byte{130}, slices.Concat(cpx(red), cpx(blue))...), 0x81, 3, 0x80, 4)), "a run of 5 or more pixels with 4 left"},
		{"palette RLE index", rect(append(append([]byte{130}, slices.Concat(cpx(red), cpx(blue))...), 2)), "palette index 2 of 2 colours"},
		{"packed index", rect(append(append([]byte{3}, slices.Concat(cpx(red), cpx(blue), cpx(green))...), 0b11_00_00_00, 0)), "palette index 3 of 3 colours"},
		{"data short of the pixels", rect([]byte{0, 1, 2, 3}), "ends before the rectangle's pixels"},
		{"data beyond the pixels", rect(append(slices.Clone(solid), 1, 2, 3)), "3 bytes more than the rectangle's pixels"},
		{"length beyond the bound", putUpdate(append(rectHeader(r, encZRLE), 0xff, 0xff, 0xff, 0xff)), "zrle data of 4294967295 bytes"},
		{"invalid zlib header", putUpdate(zrleRaw(r, []byte{0x78, 0x00, 0, 0})), "invalid zlib header"},
		{"preset dictionary", putUpdate(zrleRaw(r, []byte{0x78, 0xbb, 0, 0})), "invalid zlib header"},
		{"final block", putUpdate(zrleRaw(r, final.Bytes()), zrleRaw(r, zlibBytes(solid)[2:])), "the server ended its zlib stream"},
		{"corrupt data", putUpdate(zrleRaw(r, corrupt)), "zlib: flate: corrupt input"},
		{"too much left after the flush", putUpdate(zrleRaw(r, padded)), "bytes of compressed data are left after"},
		{"outside the screen", putUpdate(zrleRaw(image.Rect(20, 0, 30, 2), zlibBytes(solid))), "outside the screen"},
		{"connection ends", putUpdate(zrleRaw(r, zlibBytes(solid)))[:20], "unexpected EOF"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := decode(t, image.Pt(24, 24), defaultLimits, tt.msg)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("decode = %v, want an error with %q", err, tt.want)
			}
		})
	}
}

func TestZRLEEmpty(t *testing.T) {
	// An empty rectangle with no data is skipped; one with only a flush
	// continues the stream.
	e := newEncoder(wantPixelFormat)
	first := zrleRaw(image.Rect(0, 0, 2, 2), e.compress(&e.zstream, append([]byte{1}, cpx(green)...)))
	empty := zrleRaw(image.Rect(3, 3, 3, 9), e.compress(&e.zstream, nil))
	last := zrleRaw(image.Rect(2, 0, 4, 2), e.compress(&e.zstream, append([]byte{1}, cpx(blue)...)))
	_, sink, err := decodeOn(t, pattern(4, 4, 0), putUpdate(first, append(rectHeader(image.Rectangle{}, encZRLE), 0, 0, 0, 0), empty, last))
	if err != nil {
		t.Fatal(err)
	}
	if got := sink.Pixels().RGBAAt(3, 1); got != blue {
		t.Errorf("pixel (3, 1) is %v, want blue", got)
	}
}

func TestZlibStreamLevels(t *testing.T) {
	// Rectangles of one stream at zlib's levels, including 0, whose data
	// is all stored blocks. The inflater uses each rectangle's data up to
	// and including the flush, and leaves nothing for the next.
	for _, level := range []int{flate.NoCompression, flate.BestSpeed, flate.DefaultCompression, flate.BestCompression} {
		e := newEncoder(wantPixelFormat)
		e.level = level
		img := pattern(70, 3, level+3)
		var msgs [][]byte
		for i := range 3 {
			msgs = append(msgs, putUpdate(e.zrle(img, image.Rect(0, i, 70, i+1))))
		}
		d, sink, err := decodeOn(t, pattern(70, 3, 0), msgs...)
		if err != nil {
			t.Fatalf("level %d: %v", level, err)
		}
		equalImages(t, sink.Pixels(), img)
		if n := len(d.enc.zrle.in.tail); n != 0 {
			t.Errorf("level %d: %d bytes left for the next rectangle", level, n)
		}
	}
}

func TestZlibStoredBlockPastRect(t *testing.T) {
	// A ZRLE rectangle of 8×1, 25 bytes of data raw, whose compressed
	// length covers the zlib header, a stored block's header that claims
	// 25 bytes, and 10 of them. The inflater may not read the 15 Bells
	// and the flush that follow as the rest of the block.
	r := image.Rect(0, 0, 8, 1)
	z := slices.Concat([]byte{0x78, 0x01, 0x00, 25, 0, ^byte(25), 0xff}, make([]byte, 10))
	msg := putUpdate(zrleRaw(r, z))
	rest := append(bytes.Repeat([]byte{msgBell}, 15), 0x00, 0x00, 0x00, 0xff, 0xff)
	in := bytes.NewReader(slices.Concat(msg, rest))
	sink := &backendtest.Sink{}
	sink.Resize(8, 8)
	d := newDecoder(context.Background(), in, sink, image.Pt(8, 8), defaultLimits, discardLogger)
	if _, err := d.next(); err == nil || !strings.Contains(err.Error(), "ends before the rectangle's pixels") {
		t.Errorf("next = %v, want the rectangle's data short of its pixels", err)
	}
	if in.Len() != len(rest) {
		t.Errorf("%d bytes after the rectangle are left, want %d", in.Len(), len(rest))
	}
}

func TestZlibTailKept(t *testing.T) {
	// The inflater hands out data at an empty stored block, the flush, so
	// a second flush in a rectangle is left for the next rectangle, and its
	// stream goes on from there.
	var buf bytes.Buffer
	w := zlib.NewWriter(&buf)
	rect := func(r image.Rectangle, data []byte) []byte {
		buf.Reset()
		w.Write(data)
		w.Flush()
		w.Flush()
		return zrleRaw(r, slices.Clone(buf.Bytes()))
	}
	first := rect(image.Rect(0, 0, 2, 2), append([]byte{1}, cpx(green)...))
	second := rect(image.Rect(2, 0, 4, 2), append([]byte{1}, cpx(blue)...))
	sink := &backendtest.Sink{}
	sink.Resize(4, 2)
	d := newDecoder(context.Background(), bytes.NewReader(slices.Concat(putUpdate(first), putUpdate(second))), sink, image.Pt(4, 2), defaultLimits, discardLogger)
	for i := range 2 {
		if _, err := d.next(); err != nil {
			t.Fatal(err)
		}
		if n := len(d.enc.zrle.in.tail); n != 5 {
			t.Errorf("after rectangle %d, %d bytes left, want the 5 of an empty stored block", i+1, n)
		}
	}
	if got := sink.Pixels().RGBAAt(3, 1); got != blue {
		t.Errorf("pixel (3, 1) is %v, want blue", got)
	}
}
