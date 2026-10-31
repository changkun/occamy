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
	"changkun.de/x/occamy/internal/keymap"
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

// xtKeycode returns the RFB keycode of a HID usage, or 0: its set 1 make
// code from keymap, a byte below 0x80 as it is and one after the 0xe0
// prefix with its high bit set. Lang1 and Lang2 are the single bytes
// they send, 0xf2 and 0xf1. As rfbproto requires, PrintScreen is 0x54
// and Pause 0xc6.
func xtKeycode(code uint16) uint32 {
	sc := keymap.Set1(code)
	switch sc {
	case 0:
		return 0
	case keymap.PrintScreen:
		return 0x54
	case keymap.Pause:
		return 0xc6
	}
	if sc>>8 == 0xe0 {
		return 0x80 | uint32(sc&0x7f)
	}
	return uint32(sc)
}

// qemuKeys implements peer.
func (r *run) qemuKeys() {
	r.locked(func() { r.keys.on = true })
}

// key returns the message for a key: an extended key event if the
// server takes them and the code maps, or was pressed with one; a
// KeyEvent if not; nothing for a key with neither a keysym nor an
// extended event, or for Power and the power keysyms
// (backend.PowerKey), which a server such as QEMU's would give the
// whole machine.
func (r *run) key(ev backend.Key) []byte {
	if backend.PowerKey(ev) {
		return nil
	}
	xt := xtKeycode(ev.Code)
	ext := false
	if xt != 0 {
		i, bit := ev.Code/64, uint64(1)<<(ev.Code%64)
		r.locked(func() {
			if ev.Down {
				if ext = r.keys.on; ext {
					r.keys.down[i] |= bit
				}
			} else {
				ext = r.keys.down[i]&bit != 0
				r.keys.down[i] &^= bit
			}
		})
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
