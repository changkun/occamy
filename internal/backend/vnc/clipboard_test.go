// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package vnc

import (
	"bytes"
	"context"
	"encoding/binary"
	"image"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"changkun.de/x/occamy/internal/backend"
	"changkun.de/x/occamy/internal/backend/backendtest"
)

// ourCaps is the backend's answer to the server's caps.
var ourCaps = clipMsg{flags: clipCaps | clipText | clipRequest | clipPeek | clipNotify | clipProvide, sizes: []uint32{maxClipboard}}

// expectClip takes an Extended Clipboard message.
func expectClip(t *testing.T, c *serverConn, want clipMsg) {
	t.Helper()
	m := expect[clipMsg](t, c)
	if m.flags != want.flags || !slices.Equal(m.sizes, want.sizes) || m.text != want.text {
		t.Fatalf("extended clipboard %#x %v %q, want %#x %v %q", m.flags, m.sizes, m.text, want.flags, want.sizes, want.text)
	}
}

// clipboardIs waits for the sink's clipboard to hold text.
func clipboardIs(t *testing.T, sink *backendtest.Sink, text string) {
	t.Helper()
	waitFor(t, "the clipboard", func() bool { _, data := sink.ClipboardContent(); return string(data) == text })
	if mime, _ := sink.ClipboardContent(); mime != "text/plain" {
		t.Errorf("clipboard of %q, want text/plain", mime)
	}
}

func TestExtendedClipboard(t *testing.T) {
	checkGoroutines(t)
	s, log := connectLogged(t, serverOptions{}, false)
	copyText := func(text string) {
		t.Helper()
		if err := s.b.Input(backend.Clipboard{MIME: "text/plain", Data: []byte(text)}); err != nil {
			t.Fatal(err)
		}
	}

	// Before the server's caps, the owner's text is Latin-1 cut text.
	copyText("café")
	if m := expect[cutTextMsg](t, s.c); m.text != "caf\xe9" {
		t.Errorf("cut text %q, want Latin-1", m.text)
	}

	// The server's caps are answered with the backend's. The server
	// offers more formats than text, each with a size.
	s.c.send(t, putClipCaps(clipText|1<<1|1<<3|clipRequest|clipPeek|clipNotify|clipProvide, 1<<20, 0, 1<<10))
	expectClip(t, s.c, ourCaps)
	// The server peeks before the owner has copied anything since.
	s.c.send(t, putClip(clipPeek, nil))
	expectClip(t, s.c, clipMsg{flags: clipNotify})

	// The server notifies of new text, which the backend asks for, and
	// gets.
	s.c.send(t, putClip(clipNotify|clipText|1<<1, nil))
	expectClip(t, s.c, clipMsg{flags: clipRequest | clipText})
	s.c.send(t, putClipProvide("naïve €\r\nline\rend\r\n\x00"))
	clipboardIs(t, s.sink, "naïve €\nline\nend\n")
	// New formats other than text are not asked for.
	s.c.send(t, putClip(clipNotify|1<<1, nil))
	s.c.quiet(t, silence)

	// The owner copies: the server is notified, and asks.
	copyText("ünïcode\nlines\r\n")
	expectClip(t, s.c, clipMsg{flags: clipNotify | clipText})
	s.c.send(t, putClip(clipRequest|clipText, nil))
	expectClip(t, s.c, clipMsg{flags: clipProvide | clipText, text: "ünïcode\r\nlines\r\n\x00"})
	// A peek has the notification again; a request for another format
	// is not answered.
	s.c.send(t, putClip(clipPeek, nil))
	expectClip(t, s.c, clipMsg{flags: clipNotify | clipText})
	s.c.send(t, putClip(clipRequest|1<<1, nil))
	s.c.quiet(t, silence)

	// A provide that does not decompress is skipped with a warning; the
	// connection goes on.
	s.c.send(t, putClip(clipProvide|clipText, []byte("not zlib")))
	s.c.send(t, putClipProvide("after\x00"))
	clipboardIs(t, s.sink, "after")
	if !strings.Contains(log.String(), "does not decompress") {
		t.Errorf("no warning of the broken stream:\n%s", log)
	}
	// So is a stream that ends before its text, and a provide without
	// text changes nothing.
	s.c.send(t, putClip(clipProvide|clipText, zlibOf([]byte{0, 0, 1, 0}, []byte("short"))))
	s.c.send(t, putClip(clipProvide|1<<1, zlibOf([]byte{0, 0, 0, 3}, []byte("rtf"))))
	s.c.send(t, putBell())
	waitFor(t, "the bell", func() bool { return s.sink.Bells() == 1 })
	if _, data := s.sink.ClipboardContent(); string(data) != "after" {
		t.Errorf("clipboard %q, want it unchanged", data)
	}
	// An action that is none of them, or several at once, is ignored.
	s.c.send(t, putClip(clipRequest|clipNotify|clipText, nil), putClip(1<<29, []byte("future")), putBell())
	waitFor(t, "the bell", func() bool { return s.sink.Bells() == 2 })
	s.c.quiet(t, silence)
}

func TestExtendedClipboardCaps(t *testing.T) {
	// The server's caps name the formats and actions it takes, and the
	// backend sends no action that they do not name.
	for _, tt := range []struct {
		name    string
		flags   uint32
		copy    clipMsg // what the owner's copy sends; zero for Latin-1 cut text
		peek    uint32  // the flags of the notification a peek has; 0 for none
		asks    bool    // a notification of text is answered
		provide bool    // a request is answered
	}{
		{
			name: "notify and request", flags: clipText | clipNotify | clipRequest | clipPeek,
			copy: clipMsg{flags: clipNotify | clipText}, peek: clipNotify | clipText, asks: true,
		},
		{
			name: "provide only", flags: clipText | clipProvide,
			copy: clipMsg{flags: clipProvide | clipText, text: "a\r\nb\x00"}, provide: true,
		},
		{
			name: "notify only", flags: clipText | clipNotify,
			copy: clipMsg{flags: clipNotify | clipText}, peek: clipNotify | clipText,
		},
		{
			// The owner's text went as Latin-1, so the backend has no
			// text to offer, but asks for the server's.
			name: "no text", flags: 1<<1 | clipNotify | clipProvide | clipRequest,
			peek: clipNotify, asks: true,
		},
		{name: "request only", flags: clipText | clipRequest | clipPeek, asks: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			checkGoroutines(t)
			s := connect(t, serverOptions{}, false)
			sizes := make([]uint32, bitsSet(tt.flags&clipFormats))
			s.c.send(t, putClipCaps(tt.flags, sizes...))
			expectClip(t, s.c, ourCaps)
			if err := s.b.Input(backend.Clipboard{Data: []byte("a\nb")}); err != nil {
				t.Fatal(err)
			}
			if tt.copy.flags == 0 {
				if m := expect[cutTextMsg](t, s.c); m.text != "a\nb" {
					t.Errorf("cut text %q, want %q", m.text, "a\nb")
				}
			} else {
				expectClip(t, s.c, tt.copy)
			}
			s.c.send(t, putClip(clipNotify|clipText, nil))
			if tt.asks {
				expectClip(t, s.c, clipMsg{flags: clipRequest | clipText})
			}
			s.c.send(t, putClip(clipPeek, nil))
			if tt.peek != 0 {
				expectClip(t, s.c, clipMsg{flags: tt.peek})
			}
			s.c.send(t, putClip(clipRequest|clipText, nil))
			if tt.provide {
				expectClip(t, s.c, clipMsg{flags: clipProvide | clipText, text: "a\r\nb\x00"})
			}
			s.c.quiet(t, silence)
		})
	}
}

// bitsSet counts the bits set in v.
func bitsSet(v uint32) int {
	n := 0
	for ; v != 0; v &= v - 1 {
		n++
	}
	return n
}

func TestExtendedText(t *testing.T) {
	for _, tt := range []struct{ in, want string }{
		{"", "\x00"},
		{"a\nb", "a\r\nb\x00"},
		{"a\r\nb", "a\r\nb\x00"},
		{"a\rb\r", "a\r\nb\r\n\x00"},
		{"\n\n", "\r\n\r\n\x00"},
		{"caf\xe9", "caf�\x00"},
		{"naïve €", "naïve €\x00"},
	} {
		if got := toExtendedText([]byte(tt.in)); string(got) != tt.want {
			t.Errorf("toExtendedText(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
	// The text is cut to fit with its NUL, between characters and
	// between a CR and its LF.
	for _, tail := range []string{"", "€", "\n", "\r\n", "\xff"} {
		in := strings.Repeat("a", maxClipboard-2) + tail + "more"
		got := toExtendedText([]byte(in))
		if len(got) > maxClipboard || got[len(got)-1] != 0 || !utf8.Valid(got) || bytes.HasSuffix(got, []byte("\r\x00")) {
			t.Errorf("toExtendedText of %q after %d bytes: %d bytes ending %q", tail, maxClipboard-2, len(got), got[max(0, len(got)-6):])
		}
	}

	for _, tt := range []struct {
		in        string
		truncated bool
		want      string
	}{
		{"a\r\nb\x00junk", false, "a\nb"},
		{"a\rb\r\n", false, "a\nb\n"},
		{"a\x00", true, "a"},
		{"ab\xc3", true, "ab"},
		{"ab\xe2\x82", true, "ab"},
		{"ab\xc3", false, "ab�"},
		{"\xff\xfe", false, "�"},
		{"", false, ""},
	} {
		if got := fromExtendedText([]byte(tt.in), tt.truncated); string(got) != tt.want {
			t.Errorf("fromExtendedText(%q, %v) = %q, want %q", tt.in, tt.truncated, got, tt.want)
		}
	}
}

func TestExtendedClipboardTruncated(t *testing.T) {
	log := &logBuffer{}
	sink := &backendtest.Sink{}
	sink.Resize(8, 8)
	text := strings.Repeat("é", maxClipboard/2) + "and more\x00"
	msg := putClipProvide(text)
	d := newDecoder(context.Background(), bytes.NewReader(slices.Concat(msg, putBell())), sink, image.Pt(8, 8), defaultLimits, log.logger())
	for range 2 {
		if _, err := d.next(); err != nil {
			t.Fatal(err)
		}
	}
	_, data := sink.ClipboardContent()
	if want := strings.Repeat("é", maxClipboard/2); string(data) != want {
		t.Errorf("clipboard of %d bytes, want %d", len(data), len(want))
	}
	if sink.Bells() != 1 {
		t.Error("the message after the text was lost")
	}
	if !strings.Contains(log.String(), "truncated") {
		t.Errorf("no warning in the log: %q", log)
	}
	// A character cut in two is dropped.
	text = "x" + strings.Repeat("é", maxClipboard/2)
	sink, err := decode(t, image.Pt(8, 8), defaultLimits, putClipProvide(text))
	if _, data := sink.ClipboardContent(); err != nil || len(data) != maxClipboard-1 || !utf8.Valid(data) {
		t.Errorf("clipboard of %d bytes (valid %v), error %v; want %d bytes", len(data), utf8.Valid(data), err, maxClipboard-1)
	}
}

func TestExtendedClipboardAllocations(t *testing.T) {
	// A small message that decompresses to much allocates for what is
	// kept, not for what it claims or holds.
	bomb := zlibOf(binary.BigEndian.AppendUint32(nil, 1<<31), make([]byte, 64<<20))
	for _, tt := range []struct {
		name string
		msg  []byte
		max  uint64
	}{
		{"64 MiB of zeros", putClip(clipProvide|clipText, bomb), 3 * maxClipboard},
		{"a size of 2 GiB, and 5 bytes", putClip(clipProvide|clipText, zlibOf(binary.BigEndian.AppendUint32(nil, 1<<31), []byte("short"))), 80 << 10},
		{"a message of 2 GiB", []byte{msgServerCutText, 0, 0, 0, 0x80, 0, 0, 0, 0x10, 0, 0, 1}, 32 << 10},
		{"caps for 16 formats", putClip(clipCaps|clipFormats, make([]byte, 64)), 32 << 10},
	} {
		sink := &backendtest.Sink{}
		sink.Resize(8, 8)
		d := newDecoder(context.Background(), bytes.NewReader(tt.msg), sink, image.Pt(8, 8), defaultLimits, discardLogger)
		got := allocated(func() { _, _ = d.next() })
		if got > tt.max {
			t.Errorf("%s: allocated %d bytes, want at most %d", tt.name, got, tt.max)
		}
	}
	if len(bomb) > 1<<20 {
		t.Fatalf("the bomb is %d bytes", len(bomb))
	}
}

func TestExtendedClipboardErrors(t *testing.T) {
	for _, tt := range []struct {
		name string
		msg  []byte
		want string
	}{
		{"caps cut short", putClip(clipCaps|clipText|1<<1, []byte{0, 0, 0, 1}), "caps of 4 bytes for 2 formats"},
		{"flags cut short", []byte{msgServerCutText, 0, 0, 0, 0xff, 0xff, 0xff, 0xf8, 0, 0}, "extended clipboard"},
		{"provide cut short", putClipProvide("text\x00")[:14], "extended clipboard"},
		{"empty", []byte{msgServerCutText, 0, 0, 0, 0xff, 0xff, 0xff, 0xff}, "extended clipboard message of 1 bytes"},
	} {
		if _, err := decode(t, image.Pt(8, 8), defaultLimits, tt.msg); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: %v, want an error with %q", tt.name, err, tt.want)
		}
	}
}
