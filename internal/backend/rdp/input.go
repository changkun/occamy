// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package rdp

import (
	"fmt"
	"image"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"changkun.de/x/occamy/internal/backend"
	"changkun.de/x/occamy/internal/rdp"
)

// Input mapping. The writer maps each viewer input into RDP input events
// when it takes the input from the queue, with what the server's input
// capability set allows.

// Wheel scaling, as the VNC backend and the guacd bridge scale a scroll
// into presses of the wheel buttons: a line is 18 pixels and a notch,
// 120 units of an RDP wheel event, 53. One Wheel makes at most
// maxNotches notches on each axis.
const (
	pixelsPerLine  = 18
	pixelsPerNotch = 53
	maxNotches     = 32
	notch          = 120
)

// USB HID usage and keysym of Pause, whose make code is a sequence.
const (
	hidPause    = 0x48
	keysymPause = 0xff13
)

// Scancodes of the Control keys, whose state decides what Pause sends.
const (
	scancodeLeftControl  = 0x1d
	scancodeRightControl = 0xe01d
)

// The most RDP input events an input makes, for the queue's budget.
const (
	// keyEvents is Pause's sequence.
	keyEvents = 4
	// charEvents is a character of Text: two UTF-16 units pressed and
	// released, or a key with Shift.
	charEvents = 4
	// pointerEvents is a button event for each button that changed.
	pointerEvents = 5
	// wheelEvents is maxNotches on each axis.
	wheelEvents = 2 * maxNotches
	// maxInputEvents is the most of any input but Text, which the
	// writer types a part at a time.
	maxInputEvents = max(keyEvents, pointerEvents, wheelEvents)
)

// cost returns the most RDP input events ev makes, for the queue's
// budget, or 0 for input that makes none.
func cost(ev backend.InputEvent) (int, error) {
	switch ev := ev.(type) {
	case backend.Key:
		return keyEvents, nil
	case backend.Text:
		return charEvents * utf8.RuneCountInString(ev.Text), nil
	case backend.Pointer:
		return pointerEvents, nil
	case backend.Wheel:
		return wheelEvents, nil
	case backend.Clipboard:
		// No events: Input queues it apart, charged by its bytes.
		return 0, nil
	case backend.Resize:
		// The display control channel comes in a later phase
		// (native-rdp-backend.md); until then viewers scale.
		return 0, nil
	}
	return 0, fmt.Errorf("%w: %T", ErrUnsupportedInput, ev)
}

// capabilities is what the server's input capability set allows.
type capabilities struct {
	unicode bool // Unicode keyboard events
	mouseX  bool // the back and forward buttons
}

// inputState is the keyboard and the pointer as the writer last sent
// them.
type inputState struct {
	ctrl    [2]bool // the left and right Control keys are down
	brk     bool    // Pause went down as Break, which needs its release
	buttons backend.Buttons
	pos     image.Point
	moved   bool        // a position was sent
	scroll  image.Point // pixels scrolled toward the next notch
}

// events appends the events of a Key, Pointer or Wheel. The writer types
// Text itself, a part at a time, with text.
func (s *inputState) events(evs []rdp.InputEvent, caps capabilities, ev backend.InputEvent) []rdp.InputEvent {
	switch ev := ev.(type) {
	case backend.Key:
		return s.key(evs, caps, ev)
	case backend.Pointer:
		return s.pointer(evs, caps, ev)
	case backend.Wheel:
		return s.wheel(evs, ev)
	}
	return evs
}

// key appends a key's events. A key with a physical code goes out as the
// scancode of that key, which the server maps through its keyboard
// layout. A key without one goes out by its keysym: a function key or
// modifier as its scancode on a US layout, and a character as a Unicode
// keyboard event if the server takes them, or as its key on a US layout
// if not. Pause, whose make code is a sequence, is sent whole on its
// press, except while Control is down: then it is Break, an ordinary
// key, as FreeRDP sends it. Power and the power keysyms are dropped
// (backend.PowerKey): Windows applies its power button action to the
// whole machine.
func (s *inputState) key(evs []rdp.InputEvent, caps capabilities, k backend.Key) []rdp.InputEvent {
	switch {
	case backend.PowerKey(k):
		return evs
	case k.Code == hidPause || k.Code == 0 && k.Keysym == keysymPause:
		switch {
		case k.Down && (s.ctrl[0] || s.ctrl[1]):
			s.brk = true
			return append(evs, rdp.KeyEvent(rdp.ScancodeBreak, true))
		case k.Down:
			return append(evs, rdp.PauseEvents()...)
		case s.brk:
			s.brk = false
			return append(evs, rdp.KeyEvent(rdp.ScancodeBreak, false))
		}
		return evs
	}
	if sc, ok := rdp.ScancodeForHID(k.Code); ok {
		return s.scancode(evs, sc, k.Down)
	}
	if r, ok := keysymRune(k.Keysym); ok && caps.unicode {
		for _, u := range utf16.Encode([]rune{r}) {
			evs = append(evs, rdp.UnicodeEvent(u, k.Down))
		}
		return evs
	}
	if sc, ok := rdp.ScancodeForKeysym(k.Keysym); ok {
		return s.scancode(evs, sc, k.Down)
	}
	return evs
}

// scancode appends a key's event by its scancode, and keeps the state of
// the Control keys as sent.
func (s *inputState) scancode(evs []rdp.InputEvent, sc uint16, down bool) []rdp.InputEvent {
	switch sc {
	case scancodeLeftControl:
		s.ctrl[0] = down
	case scancodeRightControl:
		s.ctrl[1] = down
	}
	return append(evs, rdp.KeyEvent(sc, down))
}

// keysymRune returns the character a keysym types, for Latin-1 keysyms,
// which equal their code points, and Unicode keysyms, 0x1000000 plus
// the code point. Controls and other keysyms type none.
func keysymRune(k uint32) (rune, bool) {
	if k >= 0x1000000 {
		k -= 0x1000000
	} else if k > 0xff {
		return 0, false
	}
	r := rune(k) // #nosec G115 -- checked below
	if k > utf8.MaxRune || !utf8.ValidRune(r) || r < 0x20 || r >= 0x7f && r < 0xa0 {
		return 0, false
	}
	return r, true
}

// Scancodes of the left Shift key and of Return.
const (
	scancodeShift  = 0x2a
	scancodeReturn = 0x1c
)

// text appends the events that type s, as many whole characters as keep
// evs within limit events, and returns the rest of s. It types a press
// and a release of each character, as Unicode keyboard events if the
// server takes them, and as its key on a US layout, with Shift where the
// character needs it, if not. A line feed and the C0 controls with a
// key, carriage return, tab, backspace and escape, type that key, as the
// VNC backend and the guacd bridge send them. Other characters type
// nothing.
func text(evs []rdp.InputEvent, caps capabilities, s string, limit int) ([]rdp.InputEvent, string) {
	tap := func(sc uint16) {
		evs = append(evs, rdp.KeyEvent(sc, true), rdp.KeyEvent(sc, false))
	}
	for i, r := range s {
		if len(evs)+charEvents > limit {
			return evs, s[i:]
		}
		switch {
		case r == '\n':
			tap(scancodeReturn)
		case r < 0x20:
			if sc, ok := rdp.ScancodeForKeysym(0xff00 | uint32(r)); ok { // #nosec G115 -- r is in [0, 0x20)
				tap(sc)
			}
		case r >= 0x7f && r < 0xa0:
		case caps.unicode:
			for _, u := range utf16.Encode([]rune{r}) {
				evs = append(evs, rdp.UnicodeEvent(u, true), rdp.UnicodeEvent(u, false))
			}
		case r < 0x7f:
			sc, ok := rdp.ScancodeForKeysym(uint32(r)) // #nosec G115 -- ASCII
			if !ok {
				continue
			}
			if shifted(r) {
				evs = append(evs, rdp.KeyEvent(scancodeShift, true))
				tap(sc)
				evs = append(evs, rdp.KeyEvent(scancodeShift, false))
			} else {
				tap(sc)
			}
		}
	}
	return evs, ""
}

// shifted reports whether a US layout types r with Shift.
func shifted(r rune) bool {
	return 'A' <= r && r <= 'Z' || strings.ContainsRune(`~!@#$%^&*()_+{}|:"<>?`, r)
}

// buttonOrder lists the buttons as backend.Buttons and rdp.Button.
var buttonOrder = [...]struct {
	bit backend.Buttons
	rdp rdp.Button
}{
	{backend.ButtonLeft, rdp.ButtonLeft},
	{backend.ButtonMiddle, rdp.ButtonMiddle},
	{backend.ButtonRight, rdp.ButtonRight},
	{backend.ButtonBack, rdp.ButtonBack},
	{backend.ButtonForward, rdp.ButtonForward},
}

// pointer appends the events that bring the server's pointer to p: an
// event for each button that changed, at p, or a move if none did. The
// back and forward buttons are dropped for a server that does not take
// them. The client clamps the position to the desktop.
func (s *inputState) pointer(evs []rdp.InputEvent, caps capabilities, p backend.Pointer) []rdp.InputEvent {
	pos := image.Pt(p.X, p.Y)
	changed := s.buttons ^ p.Buttons
	sent := false
	for _, b := range buttonOrder {
		if changed&b.bit == 0 {
			continue
		}
		if (b.bit == backend.ButtonBack || b.bit == backend.ButtonForward) && !caps.mouseX {
			continue
		}
		evs = append(evs, rdp.ButtonEvent(b.rdp, p.Buttons&b.bit != 0, pos))
		sent = true
	}
	if !sent && (pos != s.pos || !s.moved) {
		evs = append(evs, rdp.MoveEvent(pos))
	}
	s.buttons, s.pos, s.moved = p.Buttons, pos, true
	return evs
}

// wheel appends a wheel event of a notch, at the pointer, for each
// pixelsPerNotch scrolled on each axis. Positive DY scrolls down, which
// is a negative rotation in RDP, and positive DX right, a positive one.
func (s *inputState) wheel(evs []rdp.InputEvent, w backend.Wheel) []rdp.InputEvent {
	evs = s.notches(evs, &s.scroll.Y, w.DY, w.Mode, -notch, false)
	return s.notches(evs, &s.scroll.X, w.DX, w.Mode, notch, true)
}

// notches adds d to the distance scrolled on one axis and appends an
// event of delta, or of -delta for a negative distance, for each notch
// scrolled, at most maxNotches. The distance left over carries to the
// next Wheel, but none beyond the events made.
func (s *inputState) notches(evs []rdp.InputEvent, scrolled *int, d int, mode backend.WheelMode, delta int, horizontal bool) []rdp.InputEvent {
	d = min(max(d, -1<<20), 1<<20)
	if mode == backend.WheelLines {
		d *= pixelsPerLine
	}
	*scrolled += d
	n := *scrolled / pixelsPerNotch
	if n == 0 {
		return evs
	}
	*scrolled -= n * pixelsPerNotch
	if n < 0 {
		delta, n = -delta, -n
	}
	for range min(n, maxNotches) {
		evs = append(evs, rdp.WheelEvent(delta, horizontal, s.pos))
	}
	return evs
}
