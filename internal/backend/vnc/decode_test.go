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
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"changkun.de/x/occamy/internal/backend/backendtest"
)

var discardLogger = slog.New(slog.DiscardHandler)

// decode applies msgs to a sink of the given screen with a decoder of
// lim, and returns the sink and the first error.
func decode(t *testing.T, screen image.Point, lim limits, msgs ...[]byte) (*backendtest.Sink, error) {
	t.Helper()
	sink := &backendtest.Sink{}
	sink.Resize(screen.X, screen.Y)
	d := newDecoder(context.Background(), bytes.NewReader(bytes.Join(msgs, nil)), sink, screen, lim, discardLogger)
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
	d := newDecoder(context.Background(), bytes.NewReader(msg), sink, image.Pt(64, 48), defaultLimits, discardLogger)
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
		d := newDecoder(context.Background(), bytes.NewReader(tt.msg), sink, image.Pt(64, 48), defaultLimits, discardLogger)
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
	d := newDecoder(context.Background(), strings.NewReader(string(putCutText(text))+string(putBell())), sink, image.Pt(8, 8), defaultLimits,
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

func TestCutTextAllocations(t *testing.T) {
	// Cut text allocates for what is kept: the buffer's growth to it, and
	// the sink's copy, with room for the race detector's allocations. The
	// UTF-8 of 4 MiB of Latin-1 is up to twice that, and is allocated
	// twice: once converted and once copied.
	for _, tt := range []struct {
		name string
		text string
		max  uint64
	}{
		{"4 MiB of UTF-8 and more", strings.Repeat("a", maxClipboard+10), 5 * maxClipboard},
		{"4 MiB of Latin-1 and more", strings.Repeat("\xe9", maxClipboard+10), 8 * maxClipboard},
	} {
		sink := &backendtest.Sink{}
		sink.Resize(8, 8)
		d := newDecoder(context.Background(), strings.NewReader(string(putCutText(tt.text))), sink, image.Pt(8, 8), defaultLimits, discardLogger)
		var err error
		got := allocated(func() { _, err = d.next() })
		if err != nil {
			t.Fatalf("%s: %v", tt.name, err)
		}
		if got > tt.max {
			t.Errorf("%s: allocated %d bytes, want at most %d", tt.name, got, tt.max)
		}
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

func TestDesktopSizeOncePerUpdate(t *testing.T) {
	// Each DesktopSize reallocates the screen, so an update of a few
	// bytes must not resize it more than once.
	two := putUpdate(putDesktopSize(32, 24), putDesktopSize(31, 24))
	sink, err := decode(t, image.Pt(16, 12), defaultLimits, two)
	if err == nil || !strings.Contains(err.Error(), "second DesktopSize") {
		t.Errorf("decode = %v, want an error about a second DesktopSize", err)
	}
	if n := sink.Resizes(); n != 2 {
		t.Errorf("%d resizes, want the test's and one from the update", n)
	}
}

func TestResizeInterval(t *testing.T) {
	// One resize an update is allowed, but resizes wait for each other.
	sink := &backendtest.Sink{}
	sink.Resize(16, 12)
	var msgs []byte
	for i := range 4 {
		msgs = append(msgs, putUpdate(putDesktopSize(32+i%2, 24))...)
	}
	d := newDecoder(context.Background(), bytes.NewReader(msgs), sink, image.Pt(16, 12), defaultLimits, discardLogger)
	d.resizeEvery = 50 * time.Millisecond
	start := time.Now()
	for range 4 {
		if _, err := d.next(); err != nil {
			t.Fatal(err)
		}
	}
	if elapsed := time.Since(start); elapsed < 3*d.resizeEvery {
		t.Errorf("4 resizes took %v, want at least %v", elapsed, 3*d.resizeEvery)
	}
	if n := sink.Resizes(); n != 5 {
		t.Errorf("%d resizes, want 5", n)
	}
}

func TestResizeWaitCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	sink := &backendtest.Sink{}
	sink.Resize(16, 12)
	msgs := append(putUpdate(putDesktopSize(32, 24)), putUpdate(putDesktopSize(33, 24))...)
	d := newDecoder(ctx, bytes.NewReader(msgs), sink, image.Pt(16, 12), defaultLimits, discardLogger)
	d.resizeEvery = time.Hour
	if _, err := d.next(); err != nil {
		t.Fatal(err)
	}
	errc := make(chan error, 1)
	go func() {
		_, err := d.next()
		errc <- err
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-errc:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("next = %v, want context.Canceled", err)
		}
	case <-time.After(timeout):
		t.Fatal("the resize's wait did not end with the context")
	}
	if n := sink.Resizes(); n != 2 {
		t.Errorf("%d resizes, want 2", n)
	}
}

// cancelSink cancels a context at its at-th CopyRect or Bell.
type cancelSink struct {
	*backendtest.Sink
	cancel func()
	at     int
}

func (s *cancelSink) CopyRect(dst image.Rectangle, src image.Point) {
	s.Sink.CopyRect(dst, src)
	if len(s.Copies()) == s.at {
		s.cancel()
	}
}

func (s *cancelSink) Bell() {
	s.Sink.Bell()
	if s.Bells() == s.at {
		s.cancel()
	}
}

func TestDecoderStopsWhenCancelled(t *testing.T) {
	// What is already read is not applied once the context is done: a
	// closed connection does not stop the decoder while its buffer holds
	// messages.
	copies := make([][]byte, 5000)
	for i := range copies {
		copies[i] = putCopy(image.Rect(0, 0, 4, 4), image.Pt(i%8, 0))
	}
	bells := bytes.Repeat(putBell(), 5000)
	for _, tt := range []struct {
		name  string
		msgs  []byte
		count func(*backendtest.Sink) int
	}{
		{"between rectangles", putUpdate(copies...), func(s *backendtest.Sink) int { return len(s.Copies()) }},
		{"between messages", bells, func(s *backendtest.Sink) int { return s.Bells() }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			sink := &cancelSink{Sink: &backendtest.Sink{}, cancel: cancel, at: 3}
			sink.Resize(16, 12)
			d := newDecoder(ctx, bytes.NewReader(tt.msgs), sink, image.Pt(16, 12), defaultLimits, discardLogger)
			var err error
			for err == nil {
				_, err = d.next()
			}
			if !errors.Is(err, context.Canceled) {
				t.Errorf("next = %v, want context.Canceled", err)
			}
			if n := tt.count(sink.Sink); n != 3 {
				t.Errorf("%d applied, want 3: the decoder went on after the context was done", n)
			}
		})
	}
}

func TestCutTextEncoding(t *testing.T) {
	for _, tt := range []struct {
		name string
		sent string
		want string
	}{
		{"Latin-1", "caf\xe9 \xbd", "café ½"},
		{"UTF-8", "naïve ✓", "naïve ✓"},
		{"ASCII", "plain", "plain"},
		// UTF-8 cut within a character by the limit: the rest of the
		// character is dropped, and the text stays UTF-8.
		{"UTF-8 truncated", "ab" + strings.Repeat("✓", maxClipboard/3+1), "ab" + strings.Repeat("✓", (maxClipboard-2)/3)},
		// Latin-1 that ends in what looks like the start of a UTF-8
		// character keeps its last byte.
		{"Latin-1 truncated", strings.Repeat("\xe9", maxClipboard+1), strings.Repeat("é", maxClipboard)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			sink, err := decode(t, image.Pt(8, 8), defaultLimits, putCutText(tt.sent))
			if err != nil {
				t.Fatal(err)
			}
			if _, got := sink.ClipboardContent(); string(got) != tt.want {
				t.Errorf("clipboard of %d bytes, want %d: %.40q", len(got), len(tt.want), got)
			}
		})
	}
}
