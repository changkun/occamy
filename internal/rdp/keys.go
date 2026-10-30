// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package rdp

import "changkun.de/x/occamy/internal/keymap"

// RDP sends keys as scancodes in scan code set 1, with the 0xe0 and 0xe1
// prefixes as flags, and the server maps them through the keyboard
// layout the client announced. ScancodeForHID and ScancodeForKeysym
// translate the two key identities a viewer sends, USB HID usages and
// X11 keysyms, the second for a US layout.

// Scancodes of keys that the tables treat specially.
const (
	scancodeNumLock     = 0x45
	scancodeLeftControl = 0x1d
)

// ScancodeForHID returns the scancode of a USB HID keyboard usage: its
// set 1 make code (internal/keymap), with 0xe0 in the high byte for a
// code after the prefix. Lang1 and Lang2, the Korean keys, whose set 1
// codes 0xf2 and 0xf1 have no break codes, are 0x72 and 0x71 with the
// release flag doing the break, as FreeRDP sends them. Pause has none,
// since its make code is a sequence: PauseEvents sends it.
func ScancodeForHID(usage uint16) (uint16, bool) {
	sc := keymap.Set1(usage)
	switch {
	case sc == 0 || sc == keymap.Pause:
		return 0, false
	case sc >= 0x80 && sc < 0x100:
		return sc & 0x7f, true
	}
	return sc, true
}

// asciiScancodes maps printable ASCII keysyms, which equal their code
// points, to the key that types them on a US layout. Shifted characters
// share the key of their unshifted one; the viewer sends Shift itself.
var asciiScancodes = map[rune]uint16{
	' ': 0x39, '\'': 0x28, '"': 0x28, ',': 0x33, '<': 0x33, '-': 0x0c, '_': 0x0c,
	'.': 0x34, '>': 0x34, '/': 0x35, '?': 0x35, ';': 0x27, ':': 0x27, '=': 0x0d,
	'+': 0x0d, '[': 0x1a, '{': 0x1a, '\\': 0x2b, '|': 0x2b, ']': 0x1b, '}': 0x1b,
	'`': 0x29, '~': 0x29, '0': 0x0b, ')': 0x0b, '1': 0x02, '!': 0x02, '2': 0x03,
	'@': 0x03, '3': 0x04, '#': 0x04, '4': 0x05, '$': 0x05, '5': 0x06, '%': 0x06,
	'6': 0x07, '^': 0x07, '7': 0x08, '&': 0x08, '8': 0x09, '*': 0x09, '9': 0x0a,
	'(': 0x0a,
}

// keysymScancodes maps the X11 function keysyms (0xff00 to 0xffff).
var keysymScancodes = map[uint32]uint16{
	0xff08: 0x0e, 0xff09: 0x0f, 0xff0d: 0x1c, 0xff13: 0, 0xff14: 0x46, 0xff1b: 0x01,
	0xff50: 0xe047, 0xff51: 0xe04b, 0xff52: 0xe048, 0xff53: 0xe04d, 0xff54: 0xe050,
	0xff55: 0xe049, 0xff56: 0xe051, 0xff57: 0xe04f, 0xff61: 0xe037, 0xff63: 0xe052,
	0xff67: 0xe05d, 0xff7f: 0x45, 0xff8d: 0xe01c, 0xffaa: 0x37, 0xffab: 0x4e,
	0xffad: 0x4a, 0xffae: 0x53, 0xffaf: 0xe035, 0xffb0: 0x52, 0xffb1: 0x4f,
	0xffb2: 0x50, 0xffb3: 0x51, 0xffb4: 0x4b, 0xffb5: 0x4c, 0xffb6: 0x4d,
	0xffb7: 0x47, 0xffb8: 0x48, 0xffb9: 0x49, 0xffbe: 0x3b, 0xffbf: 0x3c,
	0xffc0: 0x3d, 0xffc1: 0x3e, 0xffc2: 0x3f, 0xffc3: 0x40, 0xffc4: 0x41,
	0xffc5: 0x42, 0xffc6: 0x43, 0xffc7: 0x44, 0xffc8: 0x57, 0xffc9: 0x58,
	0xffe1: 0x2a, 0xffe2: 0x36, 0xffe3: 0x1d, 0xffe4: 0xe01d, 0xffe5: 0x3a,
	0xffe7: 0xe05b, 0xffe8: 0xe05c, 0xffe9: 0x38, 0xffea: 0xe038, 0xffeb: 0xe05b,
	0xffec: 0xe05c, 0xffff: 0xe053, 0xfe03: 0xe038, // ISO_Level3_Shift (AltGr)
}

// ScancodeForKeysym returns the scancode of the key that produces an X11
// keysym on a US layout.
func ScancodeForKeysym(keysym uint32) (uint16, bool) {
	switch {
	case 'a' <= keysym && keysym <= 'z':
		return ScancodeForHID(uint16(keysym - 'a' + 0x04))
	case 'A' <= keysym && keysym <= 'Z':
		return ScancodeForHID(uint16(keysym - 'A' + 0x04))
	case keysym < 0x80:
		sc, ok := asciiScancodes[rune(keysym)]
		return sc, ok
	}
	sc, ok := keysymScancodes[keysym]
	return sc, ok && sc != 0
}
