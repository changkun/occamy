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

// InputEvent is a keyboard or mouse event for SendInput, made by
// KeyEvent, UnicodeEvent, MoveEvent, ButtonEvent, WheelEvent or
// PauseEvents.
type InputEvent struct {
	e       inputEvent
	pos     image.Point // a pointer event's position, clamped when sent
	pointer bool
	// joined is the number of events after this one that go in its PDU:
	// a sequence the server reads as one key.
	joined int
}

// KeyEvent is a key press or release by its scancode in scan code set 1.
// Extended keys carry 0xe0 in the high byte, as 0xe048 for the up arrow,
// and keys after the 0xe1 prefix carry 0xe1.
func KeyEvent(scancode uint16, down bool) InputEvent {
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
	return InputEvent{e: e}
}

// PauseEvents are the events of a press of Pause, which has no release:
// Control after the 0xe1 prefix and Num Lock, pressed and released, as
// FreeRDP sends the make code E1 1D 45 E1 9D C5. SendInput puts the four
// in one PDU, as FreeRDP does.
func PauseEvents() []InputEvent {
	evs := []InputEvent{
		KeyEvent(0xe100|scancodeLeftControl, true),
		KeyEvent(scancodeNumLock, true),
		KeyEvent(0xe100|scancodeLeftControl, false),
		KeyEvent(scancodeNumLock, false),
	}
	for i := range evs {
		evs[i].joined = len(evs) - 1 - i
	}
	return evs
}

// UnicodeEvent is a key press or release of a UTF-16 code unit, for text
// that has no key on the keyboard layout. The server must take them
// (Client.UnicodeInput).
func UnicodeEvent(unit uint16, down bool) InputEvent {
	e := inputEvent{kind: evUnicode, a: unit}
	if !down {
		e.flags = keyRelease
	}
	return InputEvent{e: e}
}

// MoveEvent moves the pointer to p.
func MoveEvent(p image.Point) InputEvent {
	return InputEvent{e: inputEvent{kind: evMouse, flags: ptrFlagsMove}, pos: p, pointer: true}
}

// ButtonEvent presses or releases a button at p. The back and forward
// buttons need a server that takes them (Client.MouseXInput).
func ButtonEvent(button Button, down bool, p image.Point) InputEvent {
	e := inputEvent{kind: evMouse}
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
		return InputEvent{e: e, pos: p, pointer: true}
	}
	if down {
		e.flags |= ptrFlagsDown
	}
	return InputEvent{e: e, pos: p, pointer: true}
}

// WheelEvent scrolls by delta at p, in units of 120 per notch: positive
// delta scrolls up, or right when horizontal is set. The delta is
// clamped to its field's nine bits, -256 to 255.
func WheelEvent(delta int, horizontal bool, p image.Point) InputEvent {
	delta = max(-256, min(255, delta))
	flags := uint16(ptrFlagsWheel)
	if horizontal {
		flags = ptrFlagsHWheel
	}
	if delta < 0 {
		// A nine-bit two's complement rotation.
		flags |= ptrFlagsWheelNegative | uint16(0x100+delta)&0xff // #nosec G115 -- in [0, 0xff]
	} else {
		flags |= uint16(delta) // #nosec G115 -- in [0, 0xff]
	}
	return InputEvent{e: inputEvent{kind: evMouse, flags: flags}, pos: p, pointer: true}
}

// maxInputBatch is the most events one input PDU carries: fast-path
// input counts them in a byte.
const maxInputBatch = 255

// SendInput sends events in order: as fast-path input when the server's
// input capability set allows it, and in slow-path Input Event PDUs
// otherwise. It puts up to maxInputBatch events in a PDU, without
// splitting PauseEvents' sequence, and writes the PDUs at once. Pointer
// positions are clamped to the desktop.
func (c *Client) SendInput(events ...InputEvent) error {
	if len(events) == 0 {
		return nil
	}
	c.mu.Lock()
	w, h := c.width, c.height
	c.mu.Unlock()
	fastPath := c.fastPathInput.Load()
	batch := make([]inputEvent, 0, min(len(events), maxInputBatch))
	var frames [][]byte
	for len(events) > 0 {
		n := min(len(events), maxInputBatch)
		// End the PDU before a sequence that would not fit in it; one is
		// far shorter than a PDU.
		for i := n - 1; n < len(events) && i > 0 && i+events[i].joined >= n; i-- {
			n = i
		}
		batch = batch[:0]
		for _, ev := range events[:n] {
			if ev.pointer {
				ev.e.a, ev.e.b = clamp(ev.pos, w, h)
			}
			batch = append(batch, ev.e)
		}
		events = events[n:]
		if fastPath {
			frames = append(frames, encodeFastPath(batch))
		} else {
			frames = append(frames, tpkt(sendDataRequest(c.userID, c.ioChannel, c.shareData(pdu2Input, encodeSlowPath(batch)))))
		}
	}
	return c.t.writeFrames(frames)
}

// Key sends a key press or release by its scancode, as KeyEvent.
func (c *Client) Key(scancode uint16, down bool) error {
	return c.SendInput(KeyEvent(scancode, down))
}

// Unicode sends a key press or release of a UTF-16 code unit, as
// UnicodeEvent.
func (c *Client) Unicode(unit uint16, down bool) error {
	return c.SendInput(UnicodeEvent(unit, down))
}

// UnicodeInput reports whether the server takes Unicode keyboard events.
func (c *Client) UnicodeInput() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.caps.inputFlags&inputFlagUnicode != 0
}

// MouseXInput reports whether the server takes the back and forward
// buttons.
func (c *Client) MouseXInput() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.caps.inputFlags&inputFlagMouseX != 0
}

// MouseMove moves the pointer to p.
func (c *Client) MouseMove(p image.Point) error {
	return c.SendInput(MoveEvent(p))
}

// MouseButton presses or releases button at p, as ButtonEvent.
func (c *Client) MouseButton(button Button, down bool, p image.Point) error {
	return c.SendInput(ButtonEvent(button, down, p))
}

// MouseWheel scrolls by delta at p, as WheelEvent.
func (c *Client) MouseWheel(delta int, horizontal bool, p image.Point) error {
	return c.SendInput(WheelEvent(delta, horizontal, p))
}

// clamp keeps p on a w×h desktop.
func clamp(p image.Point, w, h int) (uint16, uint16) {
	x := max(0, min(p.X, w-1))
	y := max(0, min(p.Y, h-1))
	return uint16(x), uint16(y) // #nosec G115 -- desktops are smaller than 64K
}
