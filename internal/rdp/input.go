// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package rdp

import "image"

// Input events go out as fast-path input ([MS-RDPBCGR] 2.2.8.1.2) when
// the server's input capability set allows it, and as slow-path Input
// Event PDUs (2.2.8.1.1.3) otherwise.

// Pointer flags, [MS-RDPBCGR] 2.2.8.1.1.3.1.1.3 and 2.2.8.1.1.3.1.1.4.
const (
	ptrFlagsHWheel        = 0x0400
	ptrFlagsWheel         = 0x0200
	ptrFlagsWheelNegative = 0x0100
	ptrFlagsMove          = 0x0800
	ptrFlagsDown          = 0x8000
	ptrFlagsButton1       = 0x1000 // left
	ptrFlagsButton2       = 0x2000 // right
	ptrFlagsButton3       = 0x4000 // middle
	ptrXFlagsDown         = 0x8000
	ptrXFlagsButton1      = 0x0001 // back
	ptrXFlagsButton2      = 0x0002 // forward
)

// The kinds of input event.
const (
	evScancode = iota
	evUnicode
	evMouse
	evMouseX
	evSync
)

// inputEvent is one input event before encoding.
type inputEvent struct {
	kind  int
	flags uint16 // pointer flags, or for keys release and extended
	a, b  uint16 // key code, or pointer position
}

// Key flags as the client keeps them; encode translates them.
const (
	keyRelease   = 0x1
	keyExtended  = 0x2
	keyExtended1 = 0x4
)

// Button is a mouse button.
type Button int

// The mouse buttons.
const (
	ButtonLeft Button = iota
	ButtonRight
	ButtonMiddle
	ButtonBack
	ButtonForward
)

// encodeFastPath encodes events as a fast-path input PDU.
func encodeFastPath(events []inputEvent) []byte {
	var w writer
	for _, e := range events {
		switch e.kind {
		case evScancode:
			w.u8(0x0<<5 | int(e.flags&0x7))
			w.u8(int(e.a))
		case evUnicode:
			w.u8(0x4<<5 | int(e.flags&keyRelease))
			w.u16(int(e.a))
		case evMouse, evMouseX:
			code := 0x1
			if e.kind == evMouseX {
				code = 0x2
			}
			w.u8(code << 5)
			w.u16(int(e.flags))
			w.u16(int(e.a))
			w.u16(int(e.b))
		case evSync:
			w.u8(0x3<<5 | int(e.flags&0x1f))
		}
	}
	// The header counts events in four bits; more go in a byte after the
	// length.
	var b []byte
	n := len(w.b) + 2
	hdr, extra := len(events)<<2, []byte(nil)
	if len(events) >= 16 {
		hdr, extra = 0, []byte{byte(len(events))} // #nosec G115 -- callers send few events
		n++
	}
	if n > 0x7f {
		n++
		b = append(b, byte(hdr), 0x80|byte(n>>8), byte(n)) // #nosec G115 -- in range
	} else {
		b = append(b, byte(hdr), byte(n)) // #nosec G115 -- in range
	}
	b = append(b, extra...)
	return append(b, w.b...)
}

// encodeSlowPath encodes events as a slow-path Input Event PDU body.
func encodeSlowPath(events []inputEvent) []byte {
	var w writer
	w.u16(len(events))
	w.u16(0)
	for _, e := range events {
		w.u32(0) // eventTime
		switch e.kind {
		case evScancode:
			w.u16(0x0004)
			f := 0
			if e.flags&keyRelease != 0 {
				f |= 0x8000
			}
			if e.flags&keyExtended != 0 {
				f |= 0x0100
			}
			if e.flags&keyExtended1 != 0 {
				f |= 0x0200
			}
			w.u16(f)
			w.u16(int(e.a))
			w.u16(0)
		case evUnicode:
			w.u16(0x0005)
			f := 0
			if e.flags&keyRelease != 0 {
				f = 0x8000
			}
			w.u16(f)
			w.u16(int(e.a))
			w.u16(0)
		case evMouse, evMouseX:
			typ := 0x8001
			if e.kind == evMouseX {
				typ = 0x8002
			}
			w.u16(typ)
			w.u16(int(e.flags))
			w.u16(int(e.a))
			w.u16(int(e.b))
		case evSync:
			w.u16(0x0000)
			w.u16(0)
			w.u32(uint32(e.flags))
		}
	}
	return w.b
}

// sendInput sends events.
func (c *Client) sendInput(events ...inputEvent) error {
	if c.fastPathInput.Load() {
		return c.t.write(encodeFastPath(events))
	}
	return c.sendData(pdu2Input, encodeSlowPath(events))
}

// Key sends a key press or release by its scancode in scan code set 1.
// Extended keys carry 0xe0 in the high byte, as 0xe048 for the up arrow.
func (c *Client) Key(scancode uint16, down bool) error {
	e := inputEvent{kind: evScancode, a: scancode & 0xff}
	switch scancode >> 8 {
	case 0xe0:
		e.flags |= keyExtended
	case 0xe1:
		e.flags |= keyExtended1
	}
	if !down {
		e.flags |= keyRelease
	}
	return c.sendInput(e)
}

// Unicode sends a key press or release of a UTF-16 code unit, for text
// that has no key on the keyboard layout. The server must support it
// (UnicodeInput).
func (c *Client) Unicode(unit uint16, down bool) error {
	e := inputEvent{kind: evUnicode, a: unit}
	if !down {
		e.flags = keyRelease
	}
	return c.sendInput(e)
}

// UnicodeInput reports whether the server takes Unicode keyboard events.
func (c *Client) UnicodeInput() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.caps.inputFlags&inputFlagUnicode != 0
}

// MouseMove moves the pointer to p.
func (c *Client) MouseMove(p image.Point) error {
	x, y := c.clampPoint(p)
	return c.sendInput(inputEvent{kind: evMouse, flags: ptrFlagsMove, a: x, b: y})
}

// MouseButton presses or releases button at p.
func (c *Client) MouseButton(button Button, down bool, p image.Point) error {
	x, y := c.clampPoint(p)
	e := inputEvent{kind: evMouse, a: x, b: y}
	switch button {
	case ButtonLeft:
		e.flags = ptrFlagsButton1
	case ButtonRight:
		e.flags = ptrFlagsButton2
	case ButtonMiddle:
		e.flags = ptrFlagsButton3
	case ButtonBack, ButtonForward:
		e.kind, e.flags = evMouseX, ptrXFlagsButton1
		if button == ButtonForward {
			e.flags = ptrXFlagsButton2
		}
		if down {
			e.flags |= ptrXFlagsDown
		}
		return c.sendInput(e)
	}
	if down {
		e.flags |= ptrFlagsDown
	}
	return c.sendInput(e)
}

// MouseWheel scrolls by delta at p, in units of 120 per notch: positive
// delta scrolls up, or right when horizontal is set.
func (c *Client) MouseWheel(delta int, horizontal bool, p image.Point) error {
	x, y := c.clampPoint(p)
	delta = max(-255, min(255, delta))
	flags := uint16(ptrFlagsWheel)
	if horizontal {
		flags = ptrFlagsHWheel
	}
	if delta < 0 {
		// A nine-bit two's complement rotation.
		flags |= ptrFlagsWheelNegative | uint16(0x100+delta)&0xff // #nosec G115 -- in [1, 0xff]
	} else {
		flags |= uint16(delta) // #nosec G115 -- in [0, 0xff]
	}
	return c.sendInput(inputEvent{kind: evMouse, flags: flags, a: x, b: y})
}

// clampPoint keeps p on the desktop.
func (c *Client) clampPoint(p image.Point) (uint16, uint16) {
	c.mu.Lock()
	w, h := c.width, c.height
	c.mu.Unlock()
	x := max(0, min(p.X, w-1))
	y := max(0, min(p.Y, h-1))
	return uint16(x), uint16(y) // #nosec G115 -- desktops are smaller than 64K
}
