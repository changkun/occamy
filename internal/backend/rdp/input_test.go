// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package rdp

import (
	"errors"
	"fmt"
	"image"
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
		got := text(nil, capabilities{unicode: tc.unicode}, tc.text)
		if g, w := keys(t, got), keys(t, tc.want); strings.Join(g, " ") != strings.Join(w, " ") {
			t.Errorf("%s: %v, want %v", tc.name, g, w)
		}
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
	} {
		got := key(nil, tc.caps, tc.key)
		if g, w := keys(t, got), keys(t, tc.want); strings.Join(g, " ") != strings.Join(w, " ") {
			t.Errorf("%s: %v, want %v", tc.name, g, w)
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
		{backend.Wheel{DY: 1 << 30}, rep(down, maxNotches)},           // capped, and the rest dropped
		{backend.Wheel{DX: 106, DY: 0}, rep(right, 2)},                // the axes add apart
		{backend.Wheel{DY: -1 << 30, Mode: backend.WheelLines}, rep(up, maxNotches)},
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
			var s inputState
			s.buttons = backend.Buttons(^buttons)
			if got := len(s.events(nil, caps, ev)); got > n {
				t.Fatalf("%+v makes %d events, charged %d", ev, got, n)
			}
		}
		if text, ok := ev.(backend.Text); ok && n != 4*utf8.RuneCountInString(text.Text) {
			t.Fatalf("text charged %d", n)
		}
	})
}
