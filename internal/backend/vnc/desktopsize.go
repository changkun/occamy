// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package vnc

// ExtendedDesktopSize and SetDesktopSize (rfbproto). A server that
// supports them sends an ExtendedDesktopSize rectangle in answer to every
// update request for the whole screen, so the backend learns of them
// with its first update. Until then, and with servers that never send
// one, a Resize is kept but not sent, and the screen keeps the server's
// size, which viewers scale.
//
// Every ExtendedDesktopSize that reports the size the screen already has
// changes nothing: TigerVNC sends one for each request for the whole
// screen, and the backend asks for the whole screen after each resize,
// so treating it as a resize would loop. One that changes the size is a
// resize like DesktopSize, under the same limits.
//
// One SetDesktopSize at a time awaits its answer, which servers must
// send; the newest Resize that came meanwhile follows it. The layout
// sent is a single screen covering the framebuffer, with the id and
// flags of the server's first screen, as noVNC sends it.

import (
	"encoding/binary"
	"image"
)

const (
	encExtendedDesktopSize int32 = -308

	msgSetDesktopSize = 251
	// screenSize is the size of a SCREEN in a layout.
	screenSize = 16
)

// The reasons and status codes of an ExtendedDesktopSize.
const (
	reasonClient  = 1 // answers this client's SetDesktopSize
	statusForward = 4 // forwarded, as by a hypervisor, to be done later
)

// statusText describes an ExtendedDesktopSize's status code.
var statusText = [...]string{
	0: "no error",
	1: "resize is administratively prohibited",
	2: "out of resources",
	3: "invalid screen layout",
	4: "request forwarded",
}

// screen is what the backend keeps of a SCREEN of a layout.
type screen struct {
	id, flags uint32
}

// desktop is the state of SetDesktopSize, under run.mu.
type desktop struct {
	supported bool        // the server has sent an ExtendedDesktopSize
	first     screen      // the first screen of its latest layout
	want      image.Point // the size asked for by the newest Resize, if not sent
	asked     bool        // a SetDesktopSize awaits its answer
}

// extendedDesktopSize applies an ExtendedDesktopSize rectangle, whose
// header holds the reason and status in r.Min and the screen's size in
// r.Size(). Its layout follows.
func (d *decoder) extendedDesktopSize(r image.Rectangle) error {
	var b [4]byte
	if err := d.read(b[:]); err != nil {
		return err
	}
	var buf [255 * screenSize]byte // the most screens a layout has
	layout := buf[:screenSize*int(b[0])]
	if err := d.read(layout); err != nil {
		return err
	}
	reason, status, size := r.Min.X, r.Min.Y, r.Size()
	if reason == reasonClient && status != 0 {
		// The size and layout are undefined.
		desc := "unknown error"
		if status < len(statusText) {
			desc = statusText[status]
		}
		if status == statusForward {
			d.log.Debug("vnc: the server forwarded the resize")
		} else {
			d.log.Warn("vnc: the server did not resize the screen", "status", status, "reason", desc)
		}
		d.tell().extendedDesktopSize(d.size, nil, true)
		return nil
	}
	if size != d.size {
		if err := d.desktopSize(size.X, size.Y); err != nil {
			return err
		}
	}
	var first *screen
	if len(layout) > 0 {
		first = &screen{id: binary.BigEndian.Uint32(layout[0:]), flags: binary.BigEndian.Uint32(layout[12:])}
	}
	d.tell().extendedDesktopSize(d.size, first, reason == reasonClient)
	return nil
}

// extendedDesktopSize implements peer.
func (r *run) extendedDesktopSize(size image.Point, first *screen, answer bool) {
	r.mu.Lock()
	r.size = size
	r.desk.supported = true
	if first != nil {
		r.desk.first = *first
	}
	if answer {
		r.desk.asked = false
	}
	r.mu.Unlock()
	r.poke()
}

// resize keeps the size of a Resize, unless it is empty or beyond lim,
// for the writer to ask for.
func (r *run) resize(w, h int, lim limits) {
	if lim.check(w, h) != nil {
		return
	}
	r.mu.Lock()
	r.desk.want = image.Pt(w, h)
	r.mu.Unlock()
	r.poke()
}

// desktopSizeDue appends the SetDesktopSize that is due, if any: the
// newest size asked for, once the server supports it and has answered
// the last one, unless the screen has that size. r.mu must be held.
func (r *run) desktopSizeDue(p []byte) []byte {
	d := &r.desk
	if !d.supported || d.asked || d.want == (image.Point{}) {
		return p
	}
	want := d.want
	d.want = image.Point{}
	if want == r.size {
		return p
	}
	d.asked = true
	return appendSetDesktopSize(p, want, d.first)
}

// appendSetDesktopSize appends a SetDesktopSize of one screen covering
// size, with the id and flags of s.
func appendSetDesktopSize(p []byte, size image.Point, s screen) []byte {
	p = append(p, msgSetDesktopSize, 0)
	p = binary.BigEndian.AppendUint16(p, u16(size.X))
	p = binary.BigEndian.AppendUint16(p, u16(size.Y))
	p = append(p, 1, 0)
	p = binary.BigEndian.AppendUint32(p, s.id)
	p = append(p, 0, 0, 0, 0) // at the origin
	p = binary.BigEndian.AppendUint16(p, u16(size.X))
	p = binary.BigEndian.AppendUint16(p, u16(size.Y))
	return binary.BigEndian.AppendUint32(p, s.flags)
}
