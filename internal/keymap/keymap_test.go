// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package keymap

import "testing"

func TestSet1(t *testing.T) {
	for _, tt := range []struct {
		name  string
		usage uint16
		want  uint16
	}{
		{"KeyA", 0x04, 0x1e},
		{"Digit0", 0x27, 0x0b},
		{"Enter", 0x28, 0x1c},
		{"F12", 0x45, 0x58},
		{"PrintScreen", 0x46, PrintScreen},
		{"Pause", 0x48, Pause},
		{"ArrowUp", 0x52, 0xe048},
		{"NumpadEnter", 0x58, 0xe01c},
		{"F18, after the prefix", 0x6d, 0xe077},
		{"Lang1, a single byte", 0x90, 0xf2},
		{"Lang2, a single byte", 0x91, 0xf1},
		{"ControlRight", 0xe4, 0xe01d},
		{"MetaRight", 0xe7, 0xe05c},
		{"no event", 0x00, 0},
		{"a gap", 0x82, 0},
		{"beyond the table", 0xe8, 0},
		{"largest", 0xffff, 0},
	} {
		if got := Set1(tt.usage); got != tt.want {
			t.Errorf("%s: Set1(%#x) = %#x, want %#x", tt.name, tt.usage, got, tt.want)
		}
	}
}

// Every code is a make code: a byte below 0x80, a byte below 0x80 after
// the 0xe0 prefix, or one of the three exceptions.
func TestSet1Shape(t *testing.T) {
	for usage, code := range set1 {
		switch {
		case code == 0, code < 0x80, code>>8 == 0xe0 && code&0xff < 0x80:
		case code == Pause && usage == 0x48:
		case (code == 0xf1 || code == 0xf2) && (usage == 0x90 || usage == 0x91):
		default:
			t.Errorf("usage %#x: %#x is not a set 1 make code", usage, code)
		}
	}
}
