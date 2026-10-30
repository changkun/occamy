// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package vnc

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"io"
	"log/slog"
	"strings"
	"testing"

	"changkun.de/x/occamy/internal/backend/backendtest"
)

var discardLogger = slog.New(slog.DiscardHandler)

// decode applies msgs to a sink of the given screen with a decoder of
// lim, and returns the sink and the first error.
func decode(t *testing.T, screen image.Point, lim limits, msgs ...[]byte) (*backendtest.Sink, error) {
	t.Helper()
	sink := &backendtest.Sink{}
	sink.Resize(screen.X, screen.Y)
	d := newDecoder(bytes.NewReader(bytes.Join(msgs, nil)), sink, screen, lim, discardLogger)
	for {
		if _, err := d.next(); err != nil {
			if errors.Is(err, io.EOF) {
				return sink, nil
			}
			return sink, err
		}
	}
}

func TestRawInStrips(t *testing.T) {
	sink := &backendtest.Sink{}
	sink.Resize(64, 48)
	img := pattern(64, 48, 3)
	msg := putUpdate(putRaw(wantPixelFormat, img, image.Rect(0, 0, 64, 48)))
	d := newDecoder(bytes.NewReader(msg), sink, image.Pt(64, 48), defaultLimits, discardLogger)
	d.strip = make([]byte, 5*4*64) // five rows
	if _, err := d.next(); err != nil {
		t.Fatal(err)
	}
	equalImages(t, sink.Pixels(), img)
	damage := sink.TakeDamage()
	if len(damage) != 10 || damage[0] != image.Rect(0, 0, 64, 5) || damage[9] != image.Rect(0, 45, 64, 48) {
		t.Errorf("drawn in %v, want 10 strips of 5 rows", damage)
	}
}

func TestDecoderAllocations(t *testing.T) {
	// Lengths the server claims allocate nothing before the bytes arrive,
	// and nothing beyond the limits.
	for _, tt := range []struct {
		name string
		msg  []byte
	}{
		{"cut text of 2 GiB", []byte{msgServerCutText, 0, 0, 0, 0x7f, 0xff, 0xff, 0xff, 'a'}},
		{"colour map of 65535 entries", []byte{msgSetColourMapEntries, 0, 0, 0, 0xff, 0xff}},
		{"65535 rectangles", []byte{msgFramebufferUpdate, 0, 0xff, 0xff}},
		{"largest cursor", putUpdate(rectHeader(image.Rect(0, 0, 0xffff, 0xffff), encCursor))},
		{"largest raw rectangle", putUpdate(rectHeader(image.Rect(0, 0, 0xffff, 0xffff), encRaw))},
		{"largest copy-rect", putUpdate(putCopy(image.Rect(0, 0, 0xffff, 0xffff), image.Pt(0, 0)))},
		{"largest desktop size", putUpdate(putDesktopSize(0xffff, 0xffff))},
	} {
		sink := &backendtest.Sink{}
		sink.Resize(64, 48)
		d := newDecoder(bytes.NewReader(tt.msg), sink, image.Pt(64, 48), defaultLimits, discardLogger)
		var err error
		got := allocated(func() { _, err = d.next() })
		if err == nil {
			t.Errorf("%s: no error", tt.name)
		}
		if got > 32<<10 {
			t.Errorf("%s: allocated %d bytes", tt.name, got)
		}
	}
}

func TestCutTextTruncated(t *testing.T) {
	var logs strings.Builder
	sink := &backendtest.Sink{}
	sink.Resize(8, 8)
	text := strings.Repeat("\xe9", maxClipboard+10)
	d := newDecoder(strings.NewReader(string(putCutText(text))+string(putBell())), sink, image.Pt(8, 8), defaultLimits,
		slog.New(slog.NewTextHandler(&logs, nil)))
	for range 2 {
		if _, err := d.next(); err != nil {
			t.Fatal(err)
		}
	}
	_, data := sink.ClipboardContent()
	if want := strings.Repeat("é", maxClipboard); string(data) != want {
		t.Errorf("clipboard of %d bytes, want %d", len(data), len(want))
	}
	if sink.Bells() != 1 {
		t.Error("the message after the cut text was lost")
	}
	if !strings.Contains(logs.String(), "truncated") {
		t.Errorf("no warning in the log: %q", logs.String())
	}
}

func TestCursorTransparency(t *testing.T) {
	// One row of 9 pixels needs two mask bytes; set bits are opaque.
	pix := make([]byte, 4*9)
	for i := range pix {
		pix[i] = byte(i + 1)
	}
	msg := putUpdate(append(append(rectHeader(image.Rect(4, 0, 13, 1), encCursor), pix...), 0b10100000, 0b10000000))
	sink, err := decode(t, image.Pt(8, 8), defaultLimits, msg)
	if err != nil {
		t.Fatal(err)
	}
	img, hot := sink.CursorImage()
	if hot != image.Pt(4, 0) {
		t.Errorf("hot spot %v, want (4, 0)", hot)
	}
	for x := range 9 {
		got := img.RGBAAt(x, 0)
		opaque := x == 0 || x == 2 || x == 8
		switch {
		case opaque && (got.R != pix[4*x] || got.G != pix[4*x+1] || got.B != pix[4*x+2] || got.A != 0xff):
			t.Errorf("pixel %d is %v, want opaque %v", x, got, pix[4*x:4*x+3])
		case !opaque && got != (color.RGBA{}):
			t.Errorf("pixel %d is %v, want transparent", x, got)
		}
	}
}

func TestTextKeysym(t *testing.T) {
	for _, tt := range []struct {
		r    rune
		want uint32 // 0 for none
	}{
		{'a', 'a'},
		{'\n', 0xff0d},
		{'\r', 0xff0d},
		{'\t', 0xff09},
		{'\b', 0xff08},
		{0x1b, 0xff1b},
		{0x7f, 0},
		{0x85, 0},
		{0xa0, 0xa0},
		{'ÿ', 0xff},
		{'€', 0x10020ac},
		{0x10ffff, 0x110ffff},
		{-1, 0},
		{0x110000, 0},
	} {
		got, ok := textKeysym(tt.r)
		if ok != (tt.want != 0) || got != tt.want {
			t.Errorf("textKeysym(%U) = %#x, %v; want %#x", tt.r, got, ok, tt.want)
		}
	}
}

func TestLatin1(t *testing.T) {
	if got := toLatin1([]byte("aé€b"), 3); string(got) != "a\xe9?" {
		t.Errorf("toLatin1 to 3 bytes = %q", got)
	}
	if got := toLatin1([]byte("a\xffb\r\n"), 10); string(got) != "a?b\n" {
		t.Errorf("toLatin1 of invalid UTF-8 = %q", got)
	}
	if got := string(fromLatin1([]byte("\x00\x7f\x80\xff"))); got != "\x00\x7f\u0080ÿ" {
		t.Errorf("fromLatin1 = %q", got)
	}
	if got := text([]byte("✓")); got != "✓" {
		t.Errorf("text of UTF-8 = %q", got)
	}
	for _, mime := range []string{"", "text/plain", "TEXT/HTML; charset=utf-8"} {
		if !isText(mime) {
			t.Errorf("isText(%q) = false", mime)
		}
	}
	if isText("image/png") {
		t.Error("isText(image/png) = true")
	}
}
