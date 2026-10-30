// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package vnc

import (
	"encoding/binary"
	"image"
	"slices"
	"strings"
	"unicode/utf8"

	"changkun.de/x/occamy/internal/backend"
)

// Client-to-server message types (RFC 6143, section 7.5).
const (
	msgSetPixelFormat           = 0
	msgSetEncodings             = 2
	msgFramebufferUpdateRequest = 3
	msgKeyEvent                 = 4
	msgPointerEvent             = 5
	msgClientCutText            = 6
)

// Encodings and pseudo-encodings (RFC 6143, section 7.7).
const (
	encRaw         int32 = 0
	encCopyRect    int32 = 1
	encCursor      int32 = -239
	encDesktopSize int32 = -223
)

// encodings are the encodings the backend asks for, in order of
// preference.
var encodings = []int32{encCopyRect, encRaw, encCursor, encDesktopSize}

// pixelFormat is the PIXEL_FORMAT the backend asks for: 32 bits per
// pixel, depth 24, little-endian true colour with red at bit 0, green at
// 8 and blue at 16. Each pixel's bytes are then red, green, blue and
// padding, which is image.RGBA's layout apart from alpha.
var pixelFormat = [16]byte{
	32, 24, // bits per pixel, depth
	0, 1, // big-endian flag, true-colour flag
	0, 255, 0, 255, 0, 255, // red, green and blue maximum
	0, 8, 16, // red, green and blue shift
	0, 0, 0, // padding
}

// The pointer event's button mask: buttons 1 to 8 are bits 0 to 7 (RFC
// 6143, section 7.5.5). Buttons 4 to 7 are the wheel, and 8 is back.
const (
	maskLeft      = 1 << 0
	maskMiddle    = 1 << 1
	maskRight     = 1 << 2
	maskWheelUp   = 1 << 3
	maskWheelDown = 1 << 4
	maskWheelLeft = 1 << 5
	maskWheelRgt  = 1 << 6
	maskBack      = 1 << 7
)

// Wheel scrolling follows the guacd bridge, which follows
// guacamole-common-js's Mouse: a line is 18 pixels, each 53 pixels
// scrolled press and release a wheel button once, and what remains after
// a press is dropped, so that a notch scrolls once whether a browser
// reports it as 3 lines or 100 pixels.
const (
	pixelsPerLine  = 18
	pixelsPerPress = 53
	// maxPresses bounds the presses one Wheel makes on each axis.
	maxPresses = 32
)

// maxClipboard bounds clipboard text in either direction, the largest
// message protocol v1 carries by default, as in the guacd bridge.
const maxClipboard = 4 << 20

// appendSetup appends the messages sent after the handshake:
// SetPixelFormat and SetEncodings, with the extensions' pseudo-encodings
// after the encodings.
func appendSetup(p []byte) []byte {
	p = append(p, msgSetPixelFormat, 0, 0, 0)
	p = append(p, pixelFormat[:]...)
	p = append(p, msgSetEncodings, 0)
	p = binary.BigEndian.AppendUint16(p, uint16(len(encodings)+len(extensions))) // #nosec G115 -- short constant lists
	for _, e := range slices.Concat(encodings, extensions) {
		p = binary.BigEndian.AppendUint32(p, uint32(e)) // #nosec G115 -- encodings are signed on the wire
	}
	return p
}

// appendUpdateRequest appends a FramebufferUpdateRequest for r, which
// lies within the screen.
func appendUpdateRequest(p []byte, incremental bool, r image.Rectangle) []byte {
	inc := byte(0)
	if incremental {
		inc = 1
	}
	p = append(p, msgFramebufferUpdateRequest, inc)
	p = binary.BigEndian.AppendUint16(p, u16(r.Min.X))
	p = binary.BigEndian.AppendUint16(p, u16(r.Min.Y))
	p = binary.BigEndian.AppendUint16(p, u16(r.Dx()))
	return binary.BigEndian.AppendUint16(p, u16(r.Dy()))
}

// appendKey appends a KeyEvent.
func appendKey(p []byte, keysym uint32, down bool) []byte {
	d := byte(0)
	if down {
		d = 1
	}
	p = append(p, msgKeyEvent, d, 0, 0)
	return binary.BigEndian.AppendUint32(p, keysym)
}

// appendPointer appends a PointerEvent.
func appendPointer(p []byte, buttons uint8, at image.Point) []byte {
	p = append(p, msgPointerEvent, buttons)
	p = binary.BigEndian.AppendUint16(p, u16(at.X))
	return binary.BigEndian.AppendUint16(p, u16(at.Y))
}

// appendCutText appends a ClientCutText with text, which is UTF-8, in
// Latin-1 with LF line ends, as RFC 6143 requires. Characters outside
// Latin-1 become '?', and text beyond maxClipboard bytes is dropped.
func appendCutText(p []byte, text []byte) []byte {
	latin := toLatin1(text, maxClipboard)
	p = append(p, msgClientCutText, 0, 0, 0)
	p = binary.BigEndian.AppendUint32(p, uint32(len(latin))) // #nosec G115 -- at most maxClipboard
	return append(p, latin...)
}

// u16 clamps v into the range of a U16.
func u16(v int) uint16 {
	return uint16(min(max(v, 0), 0xffff)) // #nosec G115 -- clamped
}

// pointer records the pointer's position, clamped to the screen, and
// buttons, and returns the PointerEvent for them.
func (r *run) pointer(ev backend.Pointer) []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	maxX, maxY := 0xffff, 0xffff
	if r.size.X > 0 && r.size.Y > 0 {
		maxX, maxY = r.size.X-1, r.size.Y-1
	}
	r.pos = image.Pt(min(max(ev.X, 0), maxX), min(max(ev.Y, 0), maxY))
	r.buttons = buttonMask(ev.Buttons)
	return appendPointer(nil, r.buttons, r.pos)
}

// buttonMask maps buttons to the pointer event's mask. Back is button 8,
// as in X11; Forward, button 9, has no bit and is dropped.
func buttonMask(b backend.Buttons) uint8 {
	var m uint8
	if b&backend.ButtonLeft != 0 {
		m |= maskLeft
	}
	if b&backend.ButtonMiddle != 0 {
		m |= maskMiddle
	}
	if b&backend.ButtonRight != 0 {
		m |= maskRight
	}
	if b&backend.ButtonBack != 0 {
		m |= maskBack
	}
	return m
}

// wheel turns a scroll into presses and releases of the wheel buttons at
// the pointer, with the buttons held: down and up for DY, right and left
// for DX.
func (r *run) wheel(w backend.Wheel) []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	p := r.presses(nil, &r.scroll.Y, w.DY, w.Mode, maskWheelDown, maskWheelUp)
	return r.presses(p, &r.scroll.X, w.DX, w.Mode, maskWheelRgt, maskWheelLeft)
}

// presses adds d to the distance scrolled on one axis, and appends a
// press and release of the wheel button pos (or neg, for a negative
// distance) for each pixelsPerPress scrolled. r.mu must be held.
func (r *run) presses(p []byte, scrolled *int, d int, mode backend.WheelMode, pos, neg uint8) []byte {
	d = min(max(d, -1<<20), 1<<20)
	if mode == backend.WheelLines {
		d *= pixelsPerLine
	}
	*scrolled += d
	bit, n := pos, *scrolled/pixelsPerPress
	if n < 0 {
		bit, n = neg, -n
	}
	if n == 0 {
		return p
	}
	*scrolled = 0
	for range min(n, maxPresses) {
		p = appendPointer(p, r.buttons|bit, r.pos)
		p = appendPointer(p, r.buttons, r.pos)
	}
	return p
}

// textKeysym returns the keysym that types r, as the guacd bridge and
// guacamole-common-js's Keyboard derive one from a character: Latin-1 as
// it is, other characters as 0x1000000 plus the code point, and C0
// controls as the keysyms 0xff00 plus the control (BackSpace, Tab,
// Return, Escape). A line feed types Return, and DEL and the C1 controls
// type nothing.
func textKeysym(r rune) (uint32, bool) {
	switch {
	case r == '\n':
		return 0xff0d, true
	case r < 0:
		return 0, false
	case r < 0x20:
		return 0xff00 | uint32(r), true // #nosec G115 -- r is in [0, 0x20)
	case r < 0x7f, r >= 0xa0 && r <= 0xff:
		return uint32(r), true // #nosec G115 -- r is in [0x20, 0xff]
	case r <= 0x9f:
		return 0, false
	case r <= utf8.MaxRune:
		return 0x1000000 + uint32(r), true // #nosec G115 -- r is in [0x100, utf8.MaxRune]
	}
	return 0, false
}

// isText reports whether a clipboard MIME type is text, which is all
// that RFB's cut text carries. An empty type is taken as text.
func isText(mime string) bool {
	return mime == "" || strings.HasPrefix(strings.ToLower(mime), "text/")
}

// toLatin1 converts UTF-8 text to at most limit bytes of Latin-1 with LF
// line ends. CR LF and a lone CR become LF, and characters outside
// Latin-1, as well as invalid UTF-8, become '?'.
func toLatin1(text []byte, limit int) []byte {
	out := make([]byte, 0, min(len(text), limit))
	for len(text) > 0 && len(out) < limit {
		c, n := utf8.DecodeRune(text)
		text = text[n:]
		switch {
		case c == '\r':
			if len(text) > 0 && text[0] == '\n' {
				continue // the LF follows
			}
			out = append(out, '\n')
		case c > 0xff: // including utf8.RuneError for invalid UTF-8
			out = append(out, '?')
		default:
			out = append(out, byte(c)) // #nosec G115 -- c is in [0, 0xff]
		}
	}
	return out
}

// fromLatin1 converts Latin-1 text to UTF-8, in a buffer of the size the
// UTF-8 takes: two bytes for each byte from 0x80.
func fromLatin1(text []byte) []byte {
	n := len(text)
	for _, c := range text {
		if c >= utf8.RuneSelf {
			n++
		}
	}
	out := make([]byte, 0, n)
	for _, c := range text {
		out = utf8.AppendRune(out, rune(c))
	}
	return out
}
