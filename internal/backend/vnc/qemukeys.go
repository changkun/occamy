// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package vnc

// QEMU Extended Key Events (rfbproto). A server that wants physical key
// codes, such as QEMU's, whose guest decodes PS/2 scancodes with its own
// layout, sends an empty pseudo-rectangle of the QEMU Extended Key Event
// pseudo-encoding. From then on, a backend.Key whose Code maps to an XT
// scancode is sent as an extended key event, with its keysym, or 0 for
// none; a Key without a mapped code is still a KeyEvent. Text is always
// keysyms, since it has no physical key.
//
// A key released is sent as it was pressed: a press sent as a KeyEvent
// before the server asked for codes is released with a KeyEvent too, so
// that a server that tracks keys by code finds the key it pressed.

import (
	"encoding/binary"

	"changkun.de/x/occamy/internal/backend"
)

const (
	encQEMUExtKey int32 = -258
	// msgQEMU is QEMU's client message, and qemuExtKeyEvent its
	// submessage for an extended key event.
	msgQEMU         = 255
	qemuExtKeyEvent = 0
)

// extKeys is the state of extended key events, under run.mu.
type extKeys struct {
	on   bool      // the server has asked for them
	down [4]uint64 // the codes, by HID usage, pressed with an extended event
}

// hidToXT maps the USB HID usages of the keyboard page (0x07) to XT
// scancodes as RFB keycodes: a scancode below 0x80 as it is, and one
// prefixed by 0xe0 with its high bit set. It is generated from
// keycodemapdb's data/keymaps.csv (the "USB Keycodes" and "AT set1
// keycode" columns), which QEMU and noVNC also derive their tables from,
// for the usages of the HID specification up to 0xe7. As rfbproto
// requires, PrintScreen is 0x54 and Pause 0xc6. Zero means no mapping.
var hidToXT = [0xe8]uint8{
	0x04: 0x1e, // KeyA
	0x05: 0x30, // KeyB
	0x06: 0x2e, // KeyC
	0x07: 0x20, // KeyD
	0x08: 0x12, // KeyE
	0x09: 0x21, // KeyF
	0x0a: 0x22, // KeyG
	0x0b: 0x23, // KeyH
	0x0c: 0x17, // KeyI
	0x0d: 0x24, // KeyJ
	0x0e: 0x25, // KeyK
	0x0f: 0x26, // KeyL
	0x10: 0x32, // KeyM
	0x11: 0x31, // KeyN
	0x12: 0x18, // KeyO
	0x13: 0x19, // KeyP
	0x14: 0x10, // KeyQ
	0x15: 0x13, // KeyR
	0x16: 0x1f, // KeyS
	0x17: 0x14, // KeyT
	0x18: 0x16, // KeyU
	0x19: 0x2f, // KeyV
	0x1a: 0x11, // KeyW
	0x1b: 0x2d, // KeyX
	0x1c: 0x15, // KeyY
	0x1d: 0x2c, // KeyZ
	0x1e: 0x02, // Digit1
	0x1f: 0x03, // Digit2
	0x20: 0x04, // Digit3
	0x21: 0x05, // Digit4
	0x22: 0x06, // Digit5
	0x23: 0x07, // Digit6
	0x24: 0x08, // Digit7
	0x25: 0x09, // Digit8
	0x26: 0x0a, // Digit9
	0x27: 0x0b, // Digit0
	0x28: 0x1c, // Enter
	0x29: 0x01, // Escape
	0x2a: 0x0e, // Backspace
	0x2b: 0x0f, // Tab
	0x2c: 0x39, // Space
	0x2d: 0x0c, // Minus
	0x2e: 0x0d, // Equal
	0x2f: 0x1a, // BracketLeft
	0x30: 0x1b, // BracketRight
	0x31: 0x2b, // Backslash
	0x32: 0x2b, // IntlHash, the ISO key that Backslash's scancode serves
	0x33: 0x27, // Semicolon
	0x34: 0x28, // Quote
	0x35: 0x29, // Backquote
	0x36: 0x33, // Comma
	0x37: 0x34, // Period
	0x38: 0x35, // Slash
	0x39: 0x3a, // CapsLock
	0x3a: 0x3b, // F1
	0x3b: 0x3c, // F2
	0x3c: 0x3d, // F3
	0x3d: 0x3e, // F4
	0x3e: 0x3f, // F5
	0x3f: 0x40, // F6
	0x40: 0x41, // F7
	0x41: 0x42, // F8
	0x42: 0x43, // F9
	0x43: 0x44, // F10
	0x44: 0x57, // F11
	0x45: 0x58, // F12
	0x46: 0x54, // PrintScreen
	0x47: 0x46, // ScrollLock
	0x48: 0xc6, // Pause
	0x49: 0xd2, // Insert
	0x4a: 0xc7, // Home
	0x4b: 0xc9, // PageUp
	0x4c: 0xd3, // Delete
	0x4d: 0xcf, // End
	0x4e: 0xd1, // PageDown
	0x4f: 0xcd, // ArrowRight
	0x50: 0xcb, // ArrowLeft
	0x51: 0xd0, // ArrowDown
	0x52: 0xc8, // ArrowUp
	0x53: 0x45, // NumLock
	0x54: 0xb5, // NumpadDivide
	0x55: 0x37, // NumpadMultiply
	0x56: 0x4a, // NumpadSubtract
	0x57: 0x4e, // NumpadAdd
	0x58: 0x9c, // NumpadEnter
	0x59: 0x4f, // Numpad1
	0x5a: 0x50, // Numpad2
	0x5b: 0x51, // Numpad3
	0x5c: 0x4b, // Numpad4
	0x5d: 0x4c, // Numpad5
	0x5e: 0x4d, // Numpad6
	0x5f: 0x47, // Numpad7
	0x60: 0x48, // Numpad8
	0x61: 0x49, // Numpad9
	0x62: 0x52, // Numpad0
	0x63: 0x53, // NumpadDecimal
	0x64: 0x56, // IntlBackslash
	0x65: 0xdd, // ContextMenu
	0x66: 0xde, // Power
	0x67: 0x59, // NumpadEqual
	0x68: 0x5d, // F13
	0x69: 0x5e, // F14
	0x6a: 0x5f, // F15
	0x6b: 0x55, // F16
	0x6c: 0x83, // F17
	0x6d: 0xf7, // F18
	0x6e: 0x84, // F19
	0x6f: 0x5a, // F20
	0x70: 0x74, // F21
	0x71: 0xf9, // F22
	0x72: 0x6d, // F23
	0x73: 0x6f, // F24
	0x74: 0x64, // Open
	0x75: 0xf5, // Help
	0x76: 0x9e, // Menu
	0x77: 0x8c, // Select
	0x78: 0xe8, // BrowserStop
	0x79: 0x85, // Again
	0x7a: 0x87, // Undo
	0x7b: 0xbc, // Cut
	0x7c: 0xf8, // Copy
	0x7d: 0x65, // Paste
	0x7e: 0xc1, // Find
	0x7f: 0xa0, // AudioVolumeMute
	0x80: 0xb0, // AudioVolumeUp
	0x81: 0xae, // AudioVolumeDown
	0x85: 0x7e, // NumpadComma
	0x87: 0x73, // IntlRo
	0x88: 0x70, // KanaMode
	0x89: 0x7d, // IntlYen
	0x8a: 0x79, // Convert
	0x8b: 0x7b, // NonConvert
	0x8c: 0x5c, // International6, the keypad's Japanese comma
	0x92: 0x78, // Katakana
	0x93: 0x77, // Hiragana
	0x94: 0x76, // Lang5
	0xb6: 0xf6, // NumpadParenLeft
	0xb7: 0xfb, // NumpadParenRight
	0xe0: 0x1d, // ControlLeft
	0xe1: 0x2a, // ShiftLeft
	0xe2: 0x38, // AltLeft
	0xe3: 0xdb, // MetaLeft
	0xe4: 0x9d, // ControlRight
	0xe5: 0x36, // ShiftRight
	0xe6: 0xb8, // AltRight
	0xe7: 0xdc, // MetaRight
}

// xtKeycode returns the RFB keycode of a HID usage, or 0.
func xtKeycode(code uint16) uint32 {
	if int(code) >= len(hidToXT) {
		return 0
	}
	return uint32(hidToXT[code])
}

// qemuKeys implements peer.
func (r *run) qemuKeys() {
	r.mu.Lock()
	r.keys.on = true
	r.mu.Unlock()
}

// key returns the message for a key: an extended key event if the
// server takes them and the code maps, or was pressed with one; a
// KeyEvent if not; nothing for a key with neither a keysym nor an
// extended event.
func (r *run) key(ev backend.Key) []byte {
	xt := xtKeycode(ev.Code)
	ext := false
	if xt != 0 {
		i, bit := ev.Code/64, uint64(1)<<(ev.Code%64)
		r.mu.Lock()
		if ev.Down {
			if ext = r.keys.on; ext {
				r.keys.down[i] |= bit
			}
		} else {
			ext = r.keys.down[i]&bit != 0
			r.keys.down[i] &^= bit
		}
		r.mu.Unlock()
	}
	if ext {
		return appendQEMUKey(nil, ev.Keysym, xt, ev.Down)
	}
	if ev.Keysym == 0 {
		return nil
	}
	return appendKey(nil, ev.Keysym, ev.Down)
}

// appendQEMUKey appends a QEMU Extended Key Event.
func appendQEMUKey(p []byte, keysym, keycode uint32, down bool) []byte {
	d := byte(0)
	if down {
		d = 1
	}
	p = append(p, msgQEMU, qemuExtKeyEvent, 0, d)
	p = binary.BigEndian.AppendUint32(p, keysym)
	return binary.BigEndian.AppendUint32(p, keycode)
}
