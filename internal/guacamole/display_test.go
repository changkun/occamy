// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package guacamole_test

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"strconv"
	"testing"

	"changkun.de/x/occamy/internal/guacamole"
)

func apply(t *testing.T, d *guacamole.Display, elements ...string) {
	t.Helper()
	if err := d.Apply(guacamole.NewInstruction(elements)); err != nil {
		t.Fatalf("%v: %v", elements, err)
	}
}

func wantPixel(t *testing.T, img *image.RGBA, x, y int, want color.RGBA) {
	t.Helper()
	if got := img.RGBAAt(x, y); got != want {
		t.Errorf("pixel (%d, %d) = %v, want %v", x, y, got, want)
	}
}

var (
	red         = color.RGBA{R: 0xff, A: 0xff}
	green       = color.RGBA{G: 0xff, A: 0xff}
	blue        = color.RGBA{B: 0xff, A: 0xff}
	transparent = color.RGBA{}
)

// fill paints a rectangle on a layer through rect and cfill.
func fill(t *testing.T, d *guacamole.Display, layer, x, y, w, h int, c color.RGBA) {
	t.Helper()
	itoa := strconv.Itoa
	apply(t, d, "rect", itoa(layer), itoa(x), itoa(y), itoa(w), itoa(h))
	apply(t, d, "cfill", "14", itoa(layer), itoa(int(c.R)), itoa(int(c.G)), itoa(int(c.B)), itoa(int(c.A)))
}

func TestDisplaySizeAndFill(t *testing.T) {
	d := guacamole.NewDisplay()
	apply(t, d, "size", "0", "8", "6")
	if got := d.Image().Bounds(); got != image.Rect(0, 0, 8, 6) {
		t.Fatalf("bounds = %v", got)
	}
	d.TakeDamage()

	fill(t, d, 0, 1, 1, 2, 2, red)
	wantPixel(t, d.Image(), 1, 1, red)
	wantPixel(t, d.Image(), 2, 2, red)
	wantPixel(t, d.Image(), 3, 3, transparent)
	if got := d.TakeDamage(); got != image.Rect(1, 1, 3, 3) {
		t.Errorf("damage = %v, want (1,1)-(3,3)", got)
	}
	if got := d.TakeDamage(); !got.Empty() {
		t.Errorf("damage after TakeDamage = %v, want empty", got)
	}

	// A fill clips to the default layer.
	fill(t, d, 0, 6, 4, 10, 10, green)
	wantPixel(t, d.Image(), 7, 5, green)
	if got := d.TakeDamage(); got != image.Rect(6, 4, 8, 6) {
		t.Errorf("damage = %v, want (6,4)-(8,6)", got)
	}
}

func pngBase64(t *testing.T, w, h int, c color.RGBA) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.SetRGBA(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

func TestDisplayImageStream(t *testing.T) {
	d := guacamole.NewDisplay()
	apply(t, d, "size", "0", "8", "8")
	d.TakeDamage()

	// guacd splits an image into blobs that are each valid base64.
	data, _ := base64.StdEncoding.DecodeString(pngBase64(t, 3, 2, blue))
	half := len(data) / 2
	apply(t, d, "img", "1", "12", "0", "image/png", "4", "5")
	apply(t, d, "blob", "1", base64.StdEncoding.EncodeToString(data[:half]))
	apply(t, d, "blob", "1", base64.StdEncoding.EncodeToString(data[half:]))
	wantPixel(t, d.Image(), 4, 5, transparent) // nothing before end
	apply(t, d, "end", "1")

	wantPixel(t, d.Image(), 4, 5, blue)
	wantPixel(t, d.Image(), 6, 6, blue)
	wantPixel(t, d.Image(), 7, 6, transparent)
	if got := d.TakeDamage(); got != image.Rect(4, 5, 7, 7) {
		t.Errorf("damage = %v, want (4,5)-(7,7)", got)
	}
}

func TestDisplayCopyThroughBuffer(t *testing.T) {
	d := guacamole.NewDisplay()
	apply(t, d, "size", "0", "8", "8")

	// Buffers have no size of their own and grow as they are drawn to.
	fill(t, d, -1, 2, 2, 2, 2, green)
	apply(t, d, "copy", "-1", "2", "2", "2", "2", "12", "0", "5", "1")
	wantPixel(t, d.Image(), 5, 1, green)
	wantPixel(t, d.Image(), 6, 2, green)
	wantPixel(t, d.Image(), 4, 1, transparent)
}

func TestDisplayCopyOverlapping(t *testing.T) {
	d := guacamole.NewDisplay()
	apply(t, d, "size", "0", "1", "4")
	for y, c := range []color.RGBA{red, green, blue, red} {
		fill(t, d, 0, 0, y, 1, 1, c)
	}

	// Scroll up by one row, as a terminal does.
	apply(t, d, "copy", "0", "0", "1", "1", "3", "12", "0", "0", "0")
	for y, c := range []color.RGBA{green, blue, red, red} {
		wantPixel(t, d.Image(), 0, y, c)
	}
}

func TestDisplayTransferXOR(t *testing.T) {
	d := guacamole.NewDisplay()
	apply(t, d, "size", "0", "2", "1")
	fill(t, d, 0, 0, 0, 2, 1, color.RGBA{R: 0xf0, G: 0x0f, A: 0xff})
	fill(t, d, -1, 0, 0, 1, 1, color.RGBA{R: 0xff, G: 0xff, A: 0xff})

	apply(t, d, "transfer", "-1", "0", "0", "1", "1", "6", "0", "1", "0")
	wantPixel(t, d.Image(), 1, 0, color.RGBA{R: 0x0f, G: 0xf0, A: 0xff})
	wantPixel(t, d.Image(), 0, 0, color.RGBA{R: 0xf0, G: 0x0f, A: 0xff})
}

func TestDisplayUnsupported(t *testing.T) {
	d := guacamole.NewDisplay()
	apply(t, d, "size", "0", "2", "2")
	apply(t, d, "lfill", "14", "0", "-1")
	apply(t, d, "img", "1", "12", "0", "image/gif", "0", "0")
	apply(t, d, "end", "1")
	apply(t, d, "sync", "1234") // not drawing, not counted

	want := map[string]int{"lfill": 1, "img image/gif": 1}
	if len(d.Unsupported) != len(want) {
		t.Fatalf("unsupported = %v, want %v", d.Unsupported, want)
	}
	for op, n := range want {
		if d.Unsupported[op] != n {
			t.Errorf("unsupported[%q] = %d, want %d", op, d.Unsupported[op], n)
		}
	}
}

func TestDisplayMalformed(t *testing.T) {
	for _, elements := range [][]string{
		{"size", "0", "x", "1"},
		{"size", "0", "100000", "1"},
		{"copy", "0", "0"},
		{"cfill", "14"},
		{"img", "1", "12", "0", "image/png"},
	} {
		d := guacamole.NewDisplay()
		if err := d.Apply(guacamole.NewInstruction(elements)); err == nil {
			t.Errorf("%v: no error", elements)
		}
	}

	// A broken image fails when its stream ends.
	d := guacamole.NewDisplay()
	apply(t, d, "img", "1", "12", "0", "image/webp", "0", "0")
	apply(t, d, "blob", "1", base64.StdEncoding.EncodeToString([]byte("RIFFnot webp")))
	if err := d.Apply(guacamole.NewInstruction([]string{"end", "1"})); err == nil {
		t.Error("end of a broken WebP stream: no error")
	}
}

// guacd's terminal draws on a layer that it moves into the default layer,
// with the selection as a child and the scrollbar beside it.
func TestDisplayVisibleLayers(t *testing.T) {
	d := guacamole.NewDisplay()
	apply(t, d, "size", "0", "10", "10")
	apply(t, d, "move", "1", "0", "2", "3", "0")
	apply(t, d, "size", "1", "4", "4")
	d.TakeDamage()

	fill(t, d, 1, 1, 1, 1, 1, red)
	wantPixel(t, d.Image(), 3, 4, red)
	wantPixel(t, d.Image(), 1, 1, transparent)
	if got := d.TakeDamage(); got != image.Rect(3, 4, 4, 5) {
		t.Errorf("damage = %v, want (3,4)-(4,5)", got)
	}

	// A child is placed relative to its parent, and above it.
	apply(t, d, "move", "2", "1", "1", "1", "0")
	apply(t, d, "size", "2", "1", "1") // visible layers do not grow
	fill(t, d, 2, 0, 0, 1, 1, green)
	wantPixel(t, d.Image(), 3, 4, green)

	// Siblings stack in z order.
	apply(t, d, "move", "3", "0", "3", "4", "5")
	apply(t, d, "size", "3", "1", "1")
	fill(t, d, 3, 0, 0, 1, 1, blue)
	wantPixel(t, d.Image(), 3, 4, green) // the child of 1 is deeper
	apply(t, d, "move", "2", "0", "3", "4", "0")
	wantPixel(t, d.Image(), 3, 4, blue)

	// Moving a layer damages where it was and where it goes.
	d.TakeDamage()
	apply(t, d, "move", "3", "0", "8", "8", "5")
	if got := d.TakeDamage(); got != image.Rect(3, 4, 9, 9) {
		t.Errorf("damage after move = %v, want (3,4)-(9,9)", got)
	}
	wantPixel(t, d.Image(), 8, 8, blue)

	// A fully transparent layer does not show, and dispose removes one.
	apply(t, d, "shade", "3", "0")
	wantPixel(t, d.Image(), 8, 8, transparent)
	apply(t, d, "dispose", "2")
	wantPixel(t, d.Image(), 3, 4, red)
}

// Channel masks other than SRC and OVER, as guacd uses them to build the
// cursor on buffers.
func TestDisplayMasks(t *testing.T) {
	half := color.RGBA{R: 0x80, A: 0x80} // red at half opacity, premultiplied
	for _, tc := range []struct {
		mask string
		dst  color.RGBA // a pixel of the buffer before the fill
		src  color.RGBA // what cfill paints over it
		want color.RGBA
	}{
		// destination-out: the source's alpha erases the destination.
		{"2", green, red, transparent},
		{"2", green, half, color.RGBA{G: 0x7f, A: 0x7f}},
		{"2", green, transparent, green},
		// destination-atop: the destination where the source is, the
		// source where the destination is not.
		{"9", green, red, green},
		{"9", transparent, red, red},
		{"9", green, transparent, transparent},
		// xor: each where the other is not.
		{"10", green, red, transparent},
		{"10", transparent, blue, blue},
		// copy and source-over, through the same path as before.
		{"12", green, half, half},
		{"14", green, half, color.RGBA{R: 0x80, G: 0x7f, A: 0xff}},
	} {
		d := guacamole.NewDisplay()
		fill(t, d, -1, 0, 0, 1, 1, tc.dst)
		apply(t, d, "rect", "-1", "0", "0", "1", "1")
		c := tc.src
		if c.A != 0 { // cfill takes colours that are not premultiplied
			c.R, c.G, c.B = uint8(int(c.R)*0xff/int(c.A)), uint8(int(c.G)*0xff/int(c.A)), uint8(int(c.B)*0xff/int(c.A))
		}
		apply(t, d, "cfill", tc.mask, "-1", strconv.Itoa(int(c.R)), strconv.Itoa(int(c.G)), strconv.Itoa(int(c.B)), strconv.Itoa(int(c.A)))
		apply(t, d, "size", "0", "1", "1")
		apply(t, d, "copy", "-1", "0", "0", "1", "1", "12", "0", "0", "0")
		if got := d.Image().RGBAAt(0, 0); got != tc.want {
			t.Errorf("mask %s of %v over %v = %v, want %v", tc.mask, tc.src, tc.dst, got, tc.want)
		}
	}
}

// guacd draws the pointer on a buffer and points cursor at it.
func TestDisplayCursor(t *testing.T) {
	d := guacamole.NewDisplay()
	apply(t, d, "size", "0", "4", "4")
	if _, _, ok := d.TakeCursor(); ok {
		t.Fatal("cursor before any cursor instruction")
	}
	fill(t, d, -1, 0, 0, 2, 2, red)
	d.TakeDamage()

	// The region reaches past the buffer, which is transparent there.
	apply(t, d, "cursor", "1", "0", "-1", "1", "0", "2", "2")
	if got := d.TakeDamage(); !got.Empty() {
		t.Errorf("cursor damaged %v", got)
	}
	img, hot, ok := d.TakeCursor()
	if !ok || img == nil {
		t.Fatalf("TakeCursor = %v, %v, %v", img, hot, ok)
	}
	if hot != image.Pt(1, 0) || img.Bounds() != image.Rect(0, 0, 2, 2) {
		t.Errorf("cursor bounds %v, hot spot %v", img.Bounds(), hot)
	}
	wantPixel(t, img, 0, 0, red)
	wantPixel(t, img, 0, 1, red)
	wantPixel(t, img, 1, 0, transparent)
	if _, _, ok := d.TakeCursor(); ok {
		t.Error("cursor taken twice")
	}

	// The image is copied when cursor applies, as guacamole-common-js
	// does.
	apply(t, d, "cursor", "0", "0", "-1", "0", "0", "1", "1")
	fill(t, d, -1, 0, 0, 1, 1, green)
	img, _, _ = d.TakeCursor()
	wantPixel(t, img, 0, 0, red)

	// An empty region hides the pointer, and a layer that does not exist
	// gives a transparent one.
	apply(t, d, "cursor", "3", "4", "-1", "0", "0", "0", "0")
	if img, hot, ok := d.TakeCursor(); !ok || img != nil || hot != image.Pt(3, 4) {
		t.Errorf("empty cursor = %v, %v, %v", img, hot, ok)
	}
	apply(t, d, "cursor", "0", "0", "-7", "0", "0", "1", "1")
	img, _, _ = d.TakeCursor()
	wantPixel(t, img, 0, 0, transparent)

	if err := d.Apply(guacamole.NewInstruction([]string{"cursor", "0", "0", "-1", "0", "0", "-1", "1"})); err == nil {
		t.Error("cursor with a negative width: no error")
	}
}

func TestDisplayCopyTo(t *testing.T) {
	d := guacamole.NewDisplay()
	apply(t, d, "size", "0", "4", "4")
	fill(t, d, 0, 1, 1, 2, 2, red)
	fill(t, d, 0, 3, 3, 1, 1, color.RGBA{G: 0xff, A: 0x80})

	// The destination covers more than the display, as a framebuffer
	// can before a resize reaches it.
	dst := image.NewRGBA(image.Rect(0, 0, 6, 6))
	for i := range dst.Pix {
		dst.Pix[i] = 0x11
	}
	if got := d.CopyTo(dst, image.Rect(0, 0, 6, 6)); got != image.Rect(0, 0, 4, 4) {
		t.Errorf("copied %v, want (0,0)-(4,4)", got)
	}
	wantPixel(t, dst, 1, 1, red)
	wantPixel(t, dst, 0, 0, color.RGBA{A: 0xff})          // transparent over black
	wantPixel(t, dst, 3, 3, color.RGBA{G: 0x80, A: 0xff}) // half green over black
	wantPixel(t, dst, 5, 5, color.RGBA{R: 0x11, G: 0x11, B: 0x11, A: 0x11})

	// Into a sub-image, as a framebuffer's Lock returns one.
	fill(t, d, 0, 2, 2, 1, 1, blue)
	sub := dst.SubImage(image.Rect(2, 2, 3, 3)).(*image.RGBA)
	if got := d.CopyTo(sub, image.Rect(0, 0, 4, 4)); got != image.Rect(2, 2, 3, 3) {
		t.Errorf("copied %v, want (2,2)-(3,3)", got)
	}
	wantPixel(t, dst, 2, 2, blue)
	wantPixel(t, dst, 1, 1, red)
}

func TestDisplayDamageRects(t *testing.T) {
	d := guacamole.NewDisplay()
	apply(t, d, "size", "0", "100", "100")
	if got := d.TakeDamageRects(); len(got) != 1 || got[0] != image.Rect(0, 0, 100, 100) {
		t.Fatalf("damage after size = %v, want the whole display", got)
	}

	// Far apart areas stay apart; adjacent and overlapping ones merge.
	fill(t, d, 0, 0, 0, 2, 2, red)
	fill(t, d, 0, 90, 90, 2, 2, red)
	fill(t, d, 0, 2, 0, 2, 2, red)
	fill(t, d, 0, 1, 1, 2, 1, red)
	got := d.TakeDamageRects()
	want := []image.Rectangle{image.Rect(90, 90, 92, 92), image.Rect(0, 0, 4, 2)}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("damage = %v, want %v", got, want)
	}
	if got := d.TakeDamageRects(); len(got) != 0 {
		t.Errorf("damage after TakeDamageRects = %v", got)
	}

	// Many areas are kept in a bounded number of rectangles that cover
	// them all.
	var all []image.Rectangle
	for i := range 40 {
		r := image.Rect(i*5%97, i*11%93, i*5%97+1, i*11%93+1)
		all = append(all, r)
		fill(t, d, 0, r.Min.X, r.Min.Y, 1, 1, green)
	}
	got = d.TakeDamageRects()
	if len(got) > 16 {
		t.Errorf("%d damage rectangles, want at most 16", len(got))
	}
	for _, r := range all {
		covered := false
		for _, g := range got {
			covered = covered || r.In(g)
		}
		if !covered {
			t.Errorf("%v is not in the damage %v", r, got)
		}
	}

	// TakeDamage takes the same damage as one rectangle.
	fill(t, d, 0, 0, 0, 1, 1, blue)
	fill(t, d, 0, 50, 50, 1, 1, blue)
	if got := d.TakeDamage(); got != image.Rect(0, 0, 51, 51) {
		t.Errorf("TakeDamage = %v", got)
	}
	if got := d.TakeDamageRects(); len(got) != 0 {
		t.Errorf("damage after TakeDamage = %v", got)
	}
}
