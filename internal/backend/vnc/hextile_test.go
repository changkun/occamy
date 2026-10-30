// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package vnc

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"io"
	"strings"
	"testing"

	"changkun.de/x/occamy/internal/backend/backendtest"
)

// Colours for streams built by hand, with the padding byte that the
// backend's pixel format leaves on the wire set, as some servers do.
var (
	red    = color.RGBA{0xff, 0, 0, 0xff}
	green  = color.RGBA{0, 0xff, 0, 0xff}
	blue   = color.RGBA{0, 0, 0xff, 0xff}
	yellow = color.RGBA{0xff, 0xff, 0, 0xff}
	white  = color.RGBA{0xff, 0xff, 0xff, 0xff}
)

// px is c as a pixel in the backend's format, padding set.
func px(c color.RGBA) []byte { return []byte{c.R, c.G, c.B, 0x5a} }

// fillRect draws c in r on img.
func fillRect(img *image.RGBA, r image.Rectangle, c color.RGBA) {
	draw.Draw(img, r, &image.Uniform{c}, image.Point{}, draw.Src)
}

// decodeOn applies msgs to a screen that starts as base, and returns the
// decoder, which is done, and its sink.
func decodeOn(t *testing.T, base *image.RGBA, msgs ...[]byte) (*decoder, *backendtest.Sink, error) {
	t.Helper()
	sink := &backendtest.Sink{}
	size := base.Rect.Size()
	sink.Resize(size.X, size.Y)
	img := sink.Lock(base.Rect)
	copy(img.Pix, base.Pix)
	sink.Unlock(base.Rect)
	d := newDecoder(context.Background(), bytes.NewReader(bytes.Join(msgs, nil)), sink, size, defaultLimits, discardLogger)
	for {
		if _, err := d.next(); err != nil {
			if errors.Is(err, io.EOF) {
				return d, sink, nil
			}
			return d, sink, err
		}
	}
}

func TestHextileByHand(t *testing.T) {
	// A rectangle of 20×18 at (2, 3) has tiles of 16×16, 4×16, 16×2 and
	// 4×2.
	r := image.Rect(2, 3, 22, 21)
	msg := rectHeader(r, encHextile)
	// Tile 1: a red background, a green foreground and two
	// subrectangles: the top row, and 2×3 at (3, 4).
	msg = append(msg, hextileBackground|hextileForeground|hextileAnySubrects)
	msg = append(append(append(msg, px(red)...), px(green)...), 2, 0x00, 0xf0, 0x34, 0x12)
	// Tile 2: the red background carries over; one blue subrectangle of
	// 3×1 at (1, 15), at the tile's bottom right.
	msg = append(msg, hextileAnySubrects|hextileColoured, 1)
	msg = append(append(msg, px(blue)...), 0x1f, 0x20)
	// Tile 3: raw, 16×2 pixels.
	raw := pattern(16, 2, 9)
	msg = append(msg, hextileRaw)
	for i := 0; i < len(raw.Pix); i += 4 {
		msg = append(msg, raw.Pix[i], raw.Pix[i+1], raw.Pix[i+2], 0)
	}
	// Tile 4: the background carries over the raw tile; a yellow
	// foreground for a subrectangle of 4×1 at (0, 1).
	msg = append(msg, hextileForeground|hextileAnySubrects)
	msg = append(append(msg, px(yellow)...), 1, 0x01, 0x30)
	// A second rectangle of one tile, 3×2 at (0, 0): the background starts
	// black again, and a tile with no bits is all background.
	second := append(rectHeader(image.Rect(0, 0, 3, 2), encHextile), 0)

	base := pattern(24, 24, 1)
	d, sink, err := decodeOn(t, base, putUpdate(msg, second))
	if err != nil {
		t.Fatal(err)
	}
	want := image.NewRGBA(base.Rect)
	copy(want.Pix, base.Pix)
	fillRect(want, image.Rect(2, 3, 18, 19), red)
	fillRect(want, image.Rect(2, 3, 18, 4), green)
	fillRect(want, image.Rect(5, 7, 7, 10), green)
	fillRect(want, image.Rect(18, 3, 22, 19), red)
	fillRect(want, image.Rect(19, 18, 22, 19), blue)
	draw.Draw(want, image.Rect(2, 19, 18, 21), raw, image.Point{}, draw.Src)
	fillRect(want, image.Rect(18, 19, 22, 21), red)
	fillRect(want, image.Rect(18, 20, 22, 21), yellow)
	fillRect(want, image.Rect(0, 0, 3, 2), color.RGBA{A: 0xff})
	equalImages(t, sink.Pixels(), want)
	// One lock per row of tiles.
	if damage := sink.TakeDamage(); len(damage) != 4 || damage[1] != image.Rect(2, 3, 22, 19) || damage[2] != image.Rect(2, 19, 22, 21) {
		t.Errorf("drawn in %v, want the screen, two rows of tiles and the last rectangle", damage)
	}
	want4 := [numKinds]int{hextileRawTile: 1, hextileSolidTile: 1, hextileMonoTile: 2, hextileColourTile: 1}
	if d.enc.counts != want4 {
		t.Errorf("counts %v, want %v", d.enc.counts, want4)
	}
}

func TestHextileRoundTrip(t *testing.T) {
	// The fake's encoder on tiles of every kind, with edge tiles narrower
	// and shorter than 16 pixels.
	img := mixed(16*9+5, 16*2+3, hextileTile)
	r := image.Rect(1, 2, 1+16*9+5, 2+16*2+3)
	screen := image.NewRGBA(image.Rect(0, 0, r.Max.X+3, r.Max.Y+1))
	draw.Draw(screen, r, img, image.Point{}, draw.Src)
	d, sink, err := decodeOn(t, pattern(screen.Rect.Dx(), screen.Rect.Dy(), 4), putUpdate(newEncoder(wantPixelFormat).hextile(screen, r)))
	if err != nil {
		t.Fatal(err)
	}
	want := pattern(screen.Rect.Dx(), screen.Rect.Dy(), 4)
	draw.Draw(want, r, img, image.Point{}, draw.Src)
	equalImages(t, sink.Pixels(), want)
	for k := hextileRawTile; k <= hextileColourTile; k++ {
		if d.enc.counts[k] == 0 {
			t.Errorf("no %v tile in %v", k, d.enc.counts)
		}
	}
}

func TestHextileErrors(t *testing.T) {
	tile := func(r image.Rectangle, b ...byte) []byte { return putUpdate(append(rectHeader(r, encHextile), b...)) }
	for _, tt := range []struct {
		name string
		msg  []byte
		want string
	}{
		{"subrectangle beyond an edge tile", tile(image.Rect(0, 0, 4, 4),
			append(append([]byte{hextileBackground | hextileForeground | hextileAnySubrects}, append(px(red), px(blue)...)...), 1, 0x20, 0x20)...),
			"subrectangle 3×1 at (2, 0) outside the tile of 4×4"},
		{"subrectangle beyond the bottom", tile(image.Rect(0, 0, 16, 2), hextileAnySubrects, 1, 0x01, 0x01), "outside the tile"},
		{"outside the screen", tile(image.Rect(10, 10, 30, 20), 0), "outside the screen"},
		{"truncated raw tile", tile(image.Rect(0, 0, 4, 4), append([]byte{hextileRaw}, make([]byte, 60)...)...), "unexpected EOF"},
		{"truncated subrectangles", tile(image.Rect(0, 0, 4, 4), hextileAnySubrects, 3, 0, 0), "unexpected EOF"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := decode(t, image.Pt(24, 24), defaultLimits, tt.msg)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("decode = %v, want an error with %q", err, tt.want)
			}
		})
	}
}
