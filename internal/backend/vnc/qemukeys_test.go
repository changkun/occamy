// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package vnc

import (
	"image"
	"testing"

	"changkun.de/x/occamy/internal/backend"
)

func TestQEMUKeys(t *testing.T) {
	checkGoroutines(t)
	s := connect(t, serverOptions{}, true)
	in := func(down bool, keysym uint32, code uint16) {
		t.Helper()
		if err := s.b.Input(backend.Key{Down: down, Keysym: keysym, Code: code}); err != nil {
			t.Fatal(err)
		}
	}
	key := func(down bool, keysym uint32) {
		t.Helper()
		if m := expect[keyMsg](t, s.c); m != (keyMsg{down, keysym}) {
			t.Errorf("key %+v, want %+v", m, keyMsg{down, keysym})
		}
	}
	ext := func(down bool, keysym, keycode uint32) {
		t.Helper()
		if m := expect[qemuKeyMsg](t, s.c); m != (qemuKeyMsg{down, keysym, keycode}) {
			t.Errorf("extended key %+v, want %+v", m, qemuKeyMsg{down, keysym, keycode})
		}
	}

	// Before the server shows support, keys are KeyEvents, and a key
	// without a keysym is dropped.
	req := expect[requestMsg](t, s.c)
	in(true, 'b', 0x05)
	key(true, 'b')
	in(true, 0, 0x46)
	s.c.quiet(t, silence)

	img := pattern(64, 48, 0)
	s.c.send(t, putUpdate(putQEMUKeys(), putRaw(s.c.pf, img, req.r)))
	expect[requestMsg](t, s.c)

	in(true, 'a', 0x04)
	ext(true, 'a', 0x1e)
	// The release of a key pressed before goes as it was pressed, so
	// that the server sees it released.
	in(false, 'b', 0x05)
	key(false, 'b')
	in(false, 'a', 0x04)
	ext(false, 'a', 0x1e)
	// A key without a keysym has its keycode.
	in(true, 0, 0x46)
	ext(true, 0, 0x54)
	in(false, 0, 0x46)
	ext(false, 0, 0x54)
	in(true, 0xff53, 0x4f) // Right, an E0 key
	ext(true, 0xff53, 0xcd)
	// A code without a keycode goes as a KeyEvent, or not at all
	// without a keysym.
	in(true, 'x', 0)
	key(true, 'x')
	in(true, 'y', 0x0100)
	key(true, 'y')
	in(true, 0, 0x0100)
	s.c.quiet(t, silence)
	// A release without a press, as of a key held since before Run, is
	// a KeyEvent.
	in(false, 'c', 0x06)
	key(false, 'c')
}

func TestHIDToXT(t *testing.T) {
	// From the USB HID usage tables and the PC's scan code set 1, with E0
	// as the high bit, as QEMU numbers keys; not from keycodemapdb.
	for _, tt := range []struct {
		name string
		code uint16
		want uint32
	}{
		{"A", 0x04, 0x1e},
		{"Z", 0x1d, 0x2c},
		{"1", 0x1e, 0x02},
		{"0", 0x27, 0x0b},
		{"Enter", 0x28, 0x1c},
		{"Escape", 0x29, 0x01},
		{"Backspace", 0x2a, 0x0e},
		{"Tab", 0x2b, 0x0f},
		{"Space", 0x2c, 0x39},
		{"F1", 0x3a, 0x3b},
		{"F10", 0x43, 0x44},
		{"F11", 0x44, 0x57},
		{"F12", 0x45, 0x58},
		{"PrintScreen", 0x46, 0x54},
		{"ScrollLock", 0x47, 0x46},
		{"Pause", 0x48, 0xc6},
		{"Insert", 0x49, 0xd2},
		{"Home", 0x4a, 0xc7},
		{"PageUp", 0x4b, 0xc9},
		{"Delete", 0x4c, 0xd3},
		{"Right", 0x4f, 0xcd},
		{"Left", 0x50, 0xcb},
		{"Down", 0x51, 0xd0},
		{"Up", 0x52, 0xc8},
		{"NumLock", 0x53, 0x45},
		{"KP_Divide", 0x54, 0xb5},
		{"KP_Multiply", 0x55, 0x37},
		{"KP_Enter", 0x58, 0x9c},
		{"KP_1", 0x59, 0x4f},
		{"NonUSBackslash", 0x64, 0x56},
		{"Menu", 0x65, 0xdd},
		{"International1 (Ro)", 0x87, 0x73},
		{"International3 (Yen)", 0x89, 0x7d},
		{"LANG1 (Hangul/English), a single byte", 0x90, 0xf2},
		{"LANG2 (Hanja), a single byte", 0x91, 0xf1},
		{"LANG3 (Katakana)", 0x92, 0x78},
		{"LeftControl", 0xe0, 0x1d},
		{"LeftShift", 0xe1, 0x2a},
		{"LeftAlt", 0xe2, 0x38},
		{"LeftGUI", 0xe3, 0xdb},
		{"RightControl", 0xe4, 0x9d},
		{"RightShift", 0xe5, 0x36},
		{"RightAlt", 0xe6, 0xb8},
		{"RightGUI", 0xe7, 0xdc},
		{"no event", 0x00, 0},
		{"beyond the table", 0xe8, 0},
		{"largest", 0xffff, 0},
	} {
		if got := xtKeycode(tt.code); got != tt.want {
			t.Errorf("%s: xtKeycode(%#x) = %#x, want %#x", tt.name, tt.code, got, tt.want)
		}
	}
	// No two usages share a keycode, but for Backslash and Non-US #,
	// which are one key on a PC keyboard, of US and ISO layouts.
	seen := map[uint32]uint16{}
	for code := range uint16(0x100) {
		xt := xtKeycode(code)
		if xt == 0 || code == 0x32 && xt == xtKeycode(0x31) {
			continue
		}
		if prev, ok := seen[xt]; ok {
			t.Errorf("usages %#x and %#x share keycode %#x", prev, code, xt)
		}
		seen[xt] = code
	}
}

func TestQEMUKeysRect(t *testing.T) {
	// The pseudo-rectangle carries no data: the message after it is read
	// as one.
	sink, err := decode(t, image.Pt(8, 8), defaultLimits, putUpdate(putQEMUKeys()), putBell())
	if err != nil || sink.Bells() != 1 {
		t.Errorf("decode = %v with %d bells, want the bell after it", err, sink.Bells())
	}
}
