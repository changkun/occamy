// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package guacamole

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"image"
	"os"
	"slices"
	"testing"

	"golang.org/x/image/webp"
)

// TestClip8 checks the edges of libwebp's VP8Clip8: a 14-bit value with
// a 6-bit fraction, clipped below 0 and from 256<<6 on, where v>>6 would
// wrap to 0 as a byte.
func TestClip8(t *testing.T) {
	for _, c := range []struct {
		v    int
		want uint8
	}{
		{-1, 0},
		{0, 0},
		{63, 0},
		{64, 1},
		{256<<6 - 1, 255},
		{256 << 6, 255},
		{256<<6 + 64, 255},
	} {
		if got := clip8(c.v); got != c.want {
			t.Errorf("clip8(%d) = %d, want %d", c.v, got, c.want)
		}
	}
}

// TestImageTooLarge sends a valid lossy WebP header for a 16383×16383
// image, which would take 1 GiB as it decodes, and expects the display
// to refuse it before decoding.
func TestImageTooLarge(t *testing.T) {
	data, err := os.ReadFile("testdata/lossy.webp")
	if err != nil {
		t.Fatal(err)
	}
	// The VP8 frame header's 14-bit width and height follow the start
	// code 9d 01 2a.
	i := bytes.Index(data, []byte{0x9d, 0x01, 0x2a})
	if i < 0 {
		t.Fatal("no VP8 start code")
	}
	data = slices.Clone(data)
	binary.LittleEndian.PutUint16(data[i+3:], 16383)
	binary.LittleEndian.PutUint16(data[i+5:], 16383)
	c, err := webp.DecodeConfig(bytes.NewReader(data))
	if err != nil || c.Width != 16383 || c.Height != 16383 {
		t.Fatalf("patched header: %+v, %v", c, err)
	}

	d := NewDisplay()
	for _, elements := range [][]string{
		{"img", "1", "12", "-1", "image/webp", "0", "0"},
		{"blob", "1", base64.StdEncoding.EncodeToString(data)},
	} {
		if err := d.Apply(NewInstruction(elements)); err != nil {
			t.Fatalf("%v: %v", elements[0], err)
		}
	}
	if err := d.Apply(NewInstruction([]string{"end", "1"})); !errors.Is(err, errImageSize) {
		t.Errorf("end of a 16383×16383 WebP = %v, want %v", err, errImageSize)
	}
	if l := d.layers[-1]; l != nil && !l.Bounds().Empty() {
		t.Errorf("the buffer grew to %v", l.Bounds())
	}
}

// webpTile is an opaque 4:2:0 image of the size guacd sends for a large
// update, with every sample different.
func webpTile() *image.YCbCr {
	m := image.NewYCbCr(image.Rect(0, 0, 448, 480), image.YCbCrSubsampleRatio420)
	for i := range m.Y {
		m.Y[i] = uint8(i * 7)
	}
	for i := range m.Cb {
		m.Cb[i] = uint8(i * 5)
		m.Cr[i] = uint8(i * 3)
	}
	return m
}

// BenchmarkWebPOver converts a decoded lossy WebP and draws it with
// OVER, as guacd's img streams do.
func BenchmarkWebPOver(b *testing.B) {
	src := webpTile()
	dst := image.NewRGBA(image.Rect(0, 0, 1024, 768))
	r := image.Rect(64, 0, 64+448, 480)
	b.ReportAllocs()
	for b.Loop() {
		composite(dst, r, fromWebP(src), image.Point{}, maskOver)
	}
}
