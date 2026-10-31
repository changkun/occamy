// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package rdp

import (
	"errors"
	"fmt"
	"image"
	"math"
	"strings"
	"testing"
	"unicode/utf8"

	"changkun.de/x/occamy/internal/backend"
	"changkun.de/x/occamy/internal/rdp"
)

// The mapping is checked here by the events it makes; TestRunInput
// checks how they reach a server.

func TestKeysymRune(t *testing.T) {
	for k, want := range map[uint32]rune{
		'a': 'a', ' ': ' ', 0xe9: 'é', 0xa0: 0xa0, 0x10020ac: '€', 0x101f600: '😀',
		0x1f: -1, 0x7f: -1, 0x9f: -1, 0xff0d: -1, 0x20ac: -1, 0x100d800: -1, 0x1110000: -1, 0x1000000: -1,
	} {
		r, ok := keysymRune(k)
		if ok != (want >= 0) || ok && r != want {
			t.Errorf("keysym %#x: %q, %v, want %q", k, r, ok, want)
		}
	}
}

// keys prints events for comparison: rdp.InputEvent is opaque, but
// equal events print equal. TestRunInput checks what they encode.
func keys(t *testing.T, evs []rdp.InputEvent) []string {
	t.Helper()
	var out []string
	for _, ev := range evs {
		out = append(out, fmt.Sprintf("%+v", ev))
	}
	return out
}

func TestText(t *testing.T) {
	tap := func(sc uint16) []rdp.InputEvent {
		return []rdp.InputEvent{rdp.KeyEvent(sc, true), rdp.KeyEvent(sc, false)}
	}
	uni := func(u uint16) []rdp.InputEvent {
		return []rdp.InputEvent{rdp.UnicodeEvent(u, true), rdp.UnicodeEvent(u, false)}
	}
	cat := func(parts ...[]rdp.InputEvent) []rdp.InputEvent {
		var all []rdp.InputEvent
		for _, p := range parts {
			all = append(all, p...)
		}
		return all
	}
	shift := func(sc uint16) []rdp.InputEvent {
		return cat([]rdp.InputEvent{rdp.KeyEvent(scancodeShift, true)}, tap(sc), []rdp.InputEvent{rdp.KeyEvent(scancodeShift, false)})
	}
	for _, tc := range []struct {
		name    string
		unicode bool
		text    string
		want    []rdp.InputEvent
	}{
		{"unicode", true, "aé😀", cat(uni('a'), uni(0xe9), uni(0xd83d), uni(0xde00))},
		{"controls", true, "\n\r\t\b\x1b\x01\u0085", cat(tap(0x1c), tap(0x1c), tap(0x0f), tap(0x0e), tap(0x01))},
		{"US keys", false, "aA1!/?", cat(tap(0x1e), shift(0x1e), tap(0x02), shift(0x02), tap(0x35), shift(0x35))},
		{"off the US layout", false, "é€", nil},
	} {
		got, rest := text(nil, capabilities{unicode: tc.unicode}, tc.text, math.MaxInt)
		if g, w := keys(t, got), keys(t, tc.want); strings.Join(g, " ") != strings.Join(w, " ") || rest != "" {
			t.Errorf("%s: %v, want %v; %q left", tc.name, g, w, rest)
		}
	}
	// A Text is typed a part at a time: whole characters, as many as
	// keep the events within the limit.
	evs, rest := text(nil, capabilities{}, "aAb", 5)
	if g, w := keys(t, evs), keys(t, tap(0x1e)); strings.Join(g, " ") != strings.Join(w, " ") || rest != "Ab" {
		t.Errorf("limited to 5: %v, %q left, want %v, \"Ab\" left", g, rest, w)
	}
	evs, rest = text(evs, capabilities{}, rest, 6)
	if g, w := keys(t, evs), keys(t, cat(tap(0x1e), shift(0x1e))); strings.Join(g, " ") != strings.Join(w, " ") || rest != "b" {
		t.Errorf("then 6: %v, %q left, want %v, \"b\" left", g, rest, w)
	}
}

func TestKey(t *testing.T) {
	uni := capabilities{unicode: true}
	for _, tc := range []struct {
		name string
		caps capabilities
		key  backend.Key
		want []rdp.InputEvent
	}{
		{"by code", uni, backend.Key{Down: true, Keysym: 'q', Code: 0x04}, []rdp.InputEvent{rdp.KeyEvent(0x1e, true)}},
		{"code wins over keysym", uni, backend.Key{Keysym: 0xe9, Code: 0x1f}, []rdp.InputEvent{rdp.KeyEvent(0x03, false)}},
		{"Lang1", uni, backend.Key{Down: true, Code: 0x90}, []rdp.InputEvent{rdp.KeyEvent(0x72, true)}},
		{"Pause by code", uni, backend.Key{Down: true, Code: hidPause}, rdp.PauseEvents()},
		{"Pause by keysym", uni, backend.Key{Down: true, Keysym: keysymPause}, rdp.PauseEvents()},
		{"Pause released", uni, backend.Key{Keysym: keysymPause}, nil},
		{"character", uni, backend.Key{Down: true, Keysym: 0x101f600}, []rdp.InputEvent{rdp.UnicodeEvent(0xd83d, true), rdp.UnicodeEvent(0xde00, true)}},
		{"character without Unicode", capabilities{}, backend.Key{Down: true, Keysym: 'q'}, []rdp.InputEvent{rdp.KeyEvent(0x10, true)}},
		{"function key", uni, backend.Key{Down: true, Keysym: 0xffbe}, []rdp.InputEvent{rdp.KeyEvent(0x3b, true)}},
		{"unknown code, known keysym", uni, backend.Key{Down: true, Keysym: 0xff52, Code: 0x3ff}, []rdp.InputEvent{rdp.KeyEvent(0xe048, true)}},
		{"nothing", capabilities{}, backend.Key{Down: true, Keysym: 0x20ac}, nil},
		{"Power", uni, backend.Key{Down: true, Keysym: 0x1008ff2a, Code: backend.HIDPower}, nil},
		{"Power by code", uni, backend.Key{Down: true, Code: backend.HIDPower}, nil},
		{"Power by keysym", uni, backend.Key{Down: true, Keysym: 0x1008ff2a}, nil},
		{"Sleep by keysym", uni, backend.Key{Down: true, Keysym: 0x1008ff2f}, nil},
		{"Break", uni, backend.Key{Down: true, Keysym: 0xff6b}, []rdp.InputEvent{rdp.KeyEvent(0xe046, true)}},
	} {
		var s inputState
		got := s.key(nil, tc.caps, tc.key)
		if g, w := keys(t, got), keys(t, tc.want); strings.Join(g, " ") != strings.Join(w, " ") {
			t.Errorf("%s: %v, want %v", tc.name, g, w)
		}
	}
}

// Pause is Break while either Control key is down, by the scancodes
// sent: E0 46 pressed and released, as FreeRDP sends it. A Break that
// went down is released even if Control went up first.
func TestKeyBreak(t *testing.T) {
	var s inputState
	key := func(sc uint16, down bool) rdp.InputEvent { return rdp.KeyEvent(sc, down) }
	for i, step := range []struct {
		key  backend.Key
		want []rdp.InputEvent
	}{
		{backend.Key{Down: true, Code: hidPause}, rdp.PauseEvents()},
		{backend.Key{Code: hidPause}, nil},
		{backend.Key{Down: true, Code: 0xe0}, []rdp.InputEvent{key(0x1d, true)}}, // Left Control
		{backend.Key{Down: true, Code: hidPause}, []rdp.InputEvent{key(0xe046, true)}},
		{backend.Key{Code: hidPause}, []rdp.InputEvent{key(0xe046, false)}},
		{backend.Key{Code: 0xe0}, []rdp.InputEvent{key(0x1d, false)}},
		{backend.Key{Down: true, Keysym: keysymPause}, rdp.PauseEvents()},
		{backend.Key{Keysym: keysymPause}, nil},
		{backend.Key{Down: true, Keysym: 0xffe4}, []rdp.InputEvent{key(0xe01d, true)}}, // Right Control by keysym
		{backend.Key{Down: true, Keysym: keysymPause}, []rdp.InputEvent{key(0xe046, true)}},
		{backend.Key{Keysym: 0xffe4}, []rdp.InputEvent{key(0xe01d, false)}},
		{backend.Key{Keysym: keysymPause}, []rdp.InputEvent{key(0xe046, false)}},
		{backend.Key{Down: true, Code: hidPause}, rdp.PauseEvents()},
	} {
		got := s.key(nil, capabilities{unicode: true}, step.key)
		if g, w := keys(t, got), keys(t, step.want); strings.Join(g, " ") != strings.Join(w, " ") {
			t.Errorf("step %d (%+v): %v, want %v", i, step.key, g, w)
		}
	}
}

func TestPointer(t *testing.T) {
	var s inputState
	both := capabilities{mouseX: true}
	steps := []struct {
		p    backend.Pointer
		caps capabilities
		want []rdp.InputEvent
	}{
		{backend.Pointer{X: 1, Y: 2}, both, []rdp.InputEvent{rdp.MoveEvent(image.Pt(1, 2))}},
		{backend.Pointer{X: 1, Y: 2}, both, nil},
		{backend.Pointer{X: 3, Y: 4, Buttons: backend.ButtonLeft | backend.ButtonRight}, both, []rdp.InputEvent{
			rdp.ButtonEvent(rdp.ButtonLeft, true, image.Pt(3, 4)), rdp.ButtonEvent(rdp.ButtonRight, true, image.Pt(3, 4)),
		}},
		{backend.Pointer{X: 3, Y: 4, Buttons: backend.ButtonRight | backend.ButtonMiddle | backend.ButtonForward}, both, []rdp.InputEvent{
			rdp.ButtonEvent(rdp.ButtonLeft, false, image.Pt(3, 4)), rdp.ButtonEvent(rdp.ButtonMiddle, true, image.Pt(3, 4)),
			rdp.ButtonEvent(rdp.ButtonForward, true, image.Pt(3, 4)),
		}},
		{backend.Pointer{X: 5, Y: 6, Buttons: backend.ButtonRight | backend.ButtonMiddle}, capabilities{}, []rdp.InputEvent{rdp.MoveEvent(image.Pt(5, 6))}},
	}
	for i, step := range steps {
		got := s.pointer(nil, step.caps, step.p)
		if g, w := keys(t, got), keys(t, step.want); strings.Join(g, " ") != strings.Join(w, " ") {
			t.Errorf("step %d: %v, want %v", i, g, w)
		}
	}
	// The first pointer is sent wherever it is, the origin too.
	var fresh inputState
	if got := fresh.pointer(nil, both, backend.Pointer{}); len(got) != 1 || keys(t, got)[0] != keys(t, []rdp.InputEvent{rdp.MoveEvent(image.Point{})})[0] {
		t.Errorf("the first pointer, at the origin: %v, want a move", keys(t, got))
	}
}

func TestWheel(t *testing.T) {
	var s inputState
	s.pos = image.Pt(7, 8)
	up := rdp.WheelEvent(notch, false, s.pos)
	down := rdp.WheelEvent(-notch, false, s.pos)
	right := rdp.WheelEvent(notch, true, s.pos)
	rep := func(ev rdp.InputEvent, n int) []rdp.InputEvent {
		var evs []rdp.InputEvent
		for range n {
			evs = append(evs, ev)
		}
		return evs
	}
	for i, step := range []struct {
		w    backend.Wheel
		want []rdp.InputEvent
	}{
		{backend.Wheel{DY: 52}, nil},
		{backend.Wheel{DY: 1}, rep(down, 1)},                          // 53 pixels carried
		{backend.Wheel{DY: -2, Mode: backend.WheelLines}, nil},        // -36
		{backend.Wheel{DY: -1, Mode: backend.WheelLines}, rep(up, 1)}, // -54: one notch, -1 left
		{backend.Wheel{DY: -52}, rep(up, 1)},                          // -53 with what was left
		{backend.Wheel{DY: 1 << 30}, rep(down, maxNotches)},           // capped, and the rest dropped
		{backend.Wheel{DX: 106, DY: 0}, rep(right, 2)},                // the axes add apart
		{backend.Wheel{DY: -1 << 30, Mode: backend.WheelLines}, rep(up, maxNotches)},
		// Distances too large to scale do not wrap around.
		{backend.Wheel{DY: math.MaxInt, Mode: backend.WheelLines}, rep(down, maxNotches)},
		{backend.Wheel{DY: math.MinInt, Mode: backend.WheelLines}, rep(up, maxNotches)},
		{backend.Wheel{DX: math.MinInt}, rep(rdp.WheelEvent(-notch, true, s.pos), maxNotches)},
	} {
		got := s.wheel(nil, step.w)
		if g, w := keys(t, got), keys(t, step.want); strings.Join(g, " ") != strings.Join(w, " ") {
			t.Errorf("step %d (%+v): %v, want %v", i, step.w, g, w)
		}
	}
}

func TestCost(t *testing.T) {
	if _, err := cost(backend.TermInput{}); !errors.Is(err, ErrUnsupportedInput) {
		t.Errorf("TermInput: %v", err)
	}
	for _, ev := range []backend.InputEvent{backend.Clipboard{}, backend.Resize{Width: 1, Height: 1}, backend.Text{}} {
		if n, err := cost(ev); n != 0 || err != nil {
			t.Errorf("%T: %d, %v", ev, n, err)
		}
	}
}

// FuzzInput checks that no input makes more events than cost charges
// it, which is what bounds the queue.
func FuzzInput(f *testing.F) {
	f.Add(uint8(0), true, uint32(0xff13), uint16(0x48), 0, 0, uint8(0), "")
	f.Add(uint8(1), false, uint32(0), uint16(0), 0, 0, uint8(0), "a€😀\n\x01\u0085")
	f.Add(uint8(1), false, uint32(0), uint16(0), 0, 0, uint8(0), "A") // with Shift
	f.Add(uint8(1), false, uint32(0), uint16(0), 0, 0, uint8(0), "😀") // two UTF-16 units
	f.Add(uint8(2), true, uint32(0), uint16(0), 5, 6, uint8(0x1f), "")
	f.Add(uint8(3), false, uint32(0), uint16(0), 1<<30, -1<<30, uint8(1), "")
	f.Fuzz(func(t *testing.T, kind uint8, flag bool, keysym uint32, code uint16, x, y int, buttons uint8, s string) {
		var ev backend.InputEvent
		switch kind % 4 {
		case 0:
			ev = backend.Key{Down: flag, Keysym: keysym, Code: code}
		case 1:
			ev = backend.Text{Text: s}
		case 2:
			ev = backend.Pointer{X: x, Y: y, Buttons: backend.Buttons(buttons)}
		case 3:
			mode := backend.WheelPixels
			if flag {
				mode = backend.WheelLines
			}
			ev = backend.Wheel{DX: x, DY: y, Mode: mode}
		}
		n, err := cost(ev)
		if err != nil {
			t.Fatal(err)
		}
		for _, caps := range []capabilities{{}, {unicode: true, mouseX: true}} {
			if ev, ok := ev.(backend.Text); ok {
				// Typed a part at a time, as the writer types it, the
				// same events in parts within the limit.
				whole, _ := text(nil, caps, ev.Text, math.MaxInt)
				var parts []rdp.InputEvent
				for rest := ev.Text; rest != ""; {
					var part []rdp.InputEvent
					part, rest = text(nil, caps, rest, maxBatch)
					if len(part) > maxBatch || len(part) == 0 && rest != "" {
						t.Fatalf("a part of %d events, %d characters left", len(part), utf8.RuneCountInString(rest))
					}
					parts = append(parts, part...)
				}
				if len(whole) > n || strings.Join(keys(t, parts), " ") != strings.Join(keys(t, whole), " ") {
					t.Fatalf("%q makes %d events, charged %d; in parts %d", ev.Text, len(whole), n, len(parts))
				}
				continue
			}
			var s inputState
			s.buttons = backend.Buttons(^buttons)
			s.ctrl = [2]bool{flag, flag}
			if got := len(s.events(nil, caps, ev)); got > n || got > maxInputEvents {
				t.Fatalf("%+v makes %d events, charged %d", ev, got, n)
			}
		}
	})
}
