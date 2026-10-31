// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package rdp

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"time"
)

// Dynamic virtual channels, DRDYNVC ([MS-RDPEDYC]), over the static
// channel "drdynvc". The server sends its capabilities, which the client
// answers with the version it speaks, and then opens dynamic channels by
// name; the client accepts those it has a handler for and refuses the
// rest. A message on a dynamic channel goes in one Data PDU, or in a
// Data First PDU that declares its length and Data PDUs after it.
//
// The client speaks version 2 at most: version 3 adds compressed data
// and soft-sync, which the client does not take, so a server that speaks
// it falls back. The only dynamic channel it accepts is Display Control
// (displayControl), and each channel reassembles its messages up to a
// bound of its own. The answers the reader owes wait in a bounded queue
// for the channel writer, as the clipboard's do.

// DRDYNVC commands, the high nibble of a PDU's first byte ([MS-RDPEDYC]
// 2.2).
const (
	dvcCreate          = 0x01
	dvcDataFirst       = 0x02
	dvcData            = 0x03
	dvcClose           = 0x04
	dvcCaps            = 0x05
	dvcDataFirstZ      = 0x06 // compressed, version 3
	dvcDataZ           = 0x07 // compressed, version 3
	dvcSoftSyncRequest = 0x08 // version 3
)

const (
	// dvcVersion is the most DRDYNVC version the client speaks.
	dvcVersion = 2
	// maxDrdynvcMessage bounds a message on the static channel: one
	// DRDYNVC PDU, which servers keep within a chunk of 1,600 bytes
	// ([MS-RDPEDYC] 3.1.5.1.4). One longer than this is dropped.
	maxDrdynvcMessage = 8 << 10
	// maxDynvcAnswers bounds the answers the reader owes the server that
	// wait for the channel writer: a server that opens channels faster
	// than it reads the answers ends the session.
	maxDynvcAnswers = 64
	// maxDynChannelName bounds a channel's name, with its NUL.
	maxDynChannelName = 256
	// statusRefused is the creation status of a channel the client has no
	// handler for: STATUS_UNSUCCESSFUL, as FreeRDP answers.
	statusRefused = 0xc0000001
)

// dynChannel is a dynamic channel the server opened and the client
// accepted, under chio.mu.
type dynChannel struct {
	id    uint32
	limit int // the most bytes of a message the channel takes
	// handle applies a whole message. It runs on the reader, without
	// chio.mu held.
	handle func(c *Client, out Output, msg []byte) error

	// The message being reassembled, owned by the reader.
	buf     []byte
	total   int  // the length its Data First declared
	got     int  // the bytes of its PDUs so far, kept or not
	partial bool // a Data First came, and the message is not whole
	drop    bool // the message is longer than limit and is dropped
}

// dynvc is the state of DRDYNVC, under chio.mu.
type dynvc struct {
	version int // the version the client answered, 0 before the caps
	open    map[uint32]*dynChannel
	answers [][]byte // PDUs owed to the server, for the channel writer
}

// dynHandler returns the handler and the bound of the dynamic channel
// name, and whether the client accepts it.
func (c *Client) dynHandler(name string) (func(*Client, Output, []byte) error, int, bool) {
	if name == displayControl && c.cfg.Resize {
		return (*Client).displayMessage, maxDisplayMessage, true
	}
	return nil, 0, false
}

// drdynvcMessage applies a DRDYNVC PDU from the server.
func (c *Client) drdynvcMessage(out Output, msg []byte, truncated bool) error {
	if truncated {
		c.count(func(s *Stats) { s.Ignored["dynamic channel PDUs"]++ })
		return nil
	}
	r := reader{b: msg}
	hdr := r.u8()
	if r.err != nil {
		return fmt.Errorf("rdp: dynamic channel PDU: %w", r.err)
	}
	cmd, sp, cbID := int(hdr>>4), int(hdr>>2&3), int(hdr&3)
	switch cmd {
	case dvcCaps:
		r.skip(1) // Pad
		v := int(r.u16())
		if r.err != nil {
			return fmt.Errorf("rdp: dynamic channel capabilities: %w", r.err)
		}
		if v < 1 {
			return fmt.Errorf("rdp: dynamic channel capabilities of version %d", v)
		}
		v = min(v, dvcVersion)
		c.chio.mu.Lock()
		c.dvc.version = v
		err := c.dvcAnswer(dvcCapsResponse(v))
		c.chio.mu.Unlock()
		if err != nil {
			return err
		}
		c.due()
	case dvcCreate:
		id := varUint(&r, cbID)
		name := r.b
		if i := bytes.IndexByte(name, 0); i >= 0 && i < maxDynChannelName {
			name = name[:i]
		} else if r.err == nil {
			return fmt.Errorf("rdp: dynamic channel name of %d bytes without its NUL", len(name))
		}
		if r.err != nil {
			return fmt.Errorf("rdp: dynamic channel create request: %w", r.err)
		}
		return c.dynCreate(id, cbID, string(name))
	case dvcDataFirst, dvcData:
		id := varUint(&r, cbID)
		total := -1
		if cmd == dvcDataFirst {
			if sp == 3 {
				return fmt.Errorf("rdp: dynamic channel Data First with a length of size code 3")
			}
			total = int(varUint(&r, sp))
		}
		if r.err != nil {
			return fmt.Errorf("rdp: dynamic channel data: %w", r.err)
		}
		return c.dynData(out, id, total, r.b)
	case dvcClose:
		id := varUint(&r, cbID)
		if r.err != nil {
			return fmt.Errorf("rdp: dynamic channel close: %w", r.err)
		}
		c.chio.mu.Lock()
		defer c.chio.mu.Unlock()
		dc, ok := c.dvc.open[id]
		if !ok {
			c.countLocked("dynamic channel PDUs")
			return nil
		}
		delete(c.dvc.open, id)
		if c.disp.ch == dc {
			c.disp = display{want: c.disp.want, timer: c.disp.timer, wake: c.disp.wake}
		}
		// [MS-RDPEDYC] 3.2.5.2: the client answers with a close of its
		// own, as FreeRDP does.
		if err := c.dvcAnswer(dvcPDU(dvcClose, 0, cbID, id, nil)); err != nil {
			return err
		}
		c.due()
	case dvcDataFirstZ, dvcDataZ:
		return fmt.Errorf("rdp: compressed dynamic channel data, though the client answered version %d", dvcVersion)
	default:
		// Soft-sync is for multiple transports, which the client does not
		// offer; other commands are unknown.
		c.count(func(s *Stats) { s.Ignored["dynamic channel PDUs"]++ })
	}
	return nil
}

// countLocked counts an ignored PDU of kind. c.mu is taken after
// chio.mu, never before.
func (c *Client) countLocked(kind string) {
	c.count(func(s *Stats) { s.Ignored[kind]++ })
}

// dynCreate answers a request to open the channel id named name: the
// client accepts it if it has a handler and the channel is not open. As
// Display Control is the only handler, and one is open at a time, at
// most one dynamic channel is.
func (c *Client) dynCreate(id uint32, cbID int, name string) error {
	handle, limit, ok := c.dynHandler(name)
	c.chio.mu.Lock()
	defer c.chio.mu.Unlock()
	if c.dvc.open == nil {
		c.dvc.open = map[uint32]*dynChannel{}
	}
	_, taken := c.dvc.open[id]
	ok = ok && !taken
	if ok && name == displayControl && c.disp.ch != nil {
		ok = false // one Display Control channel at a time
	}
	var status uint32
	if ok {
		dc := &dynChannel{id: id, limit: limit, handle: handle}
		c.dvc.open[id] = dc
		if name == displayControl {
			c.disp.ch = dc
		}
	} else {
		status = statusRefused
		c.countLocked("refused dynamic channels")
	}
	body := binary.LittleEndian.AppendUint32(nil, status)
	if err := c.dvcAnswer(dvcPDU(dvcCreate, 0, cbID, id, body)); err != nil {
		return err
	}
	c.due()
	return nil
}

// dynData applies a Data First PDU, whose message is total bytes long,
// or a Data PDU, for which total is -1, on the channel id.
func (c *Client) dynData(out Output, id uint32, total int, data []byte) error {
	c.chio.mu.Lock()
	dc := c.dvc.open[id]
	c.chio.mu.Unlock()
	if dc == nil {
		c.count(func(s *Stats) { s.Ignored["dynamic channel PDUs"]++ })
		return nil
	}
	switch {
	case total >= 0:
		if dc.partial {
			c.count(func(s *Stats) { s.Ignored["unfinished dynamic channel messages"]++ })
		}
		dc.buf, dc.total, dc.got, dc.partial, dc.drop = dc.buf[:0], total, 0, true, total > dc.limit
	case !dc.partial:
		// A message in one Data PDU.
		if len(data) > dc.limit {
			c.count(func(s *Stats) { s.Ignored["dynamic channel messages over their bound"]++ })
			return nil
		}
		return dc.handle(c, out, data)
	}
	dc.got += len(data)
	if dc.got > dc.total {
		dc.partial = false
		return fmt.Errorf("rdp: dynamic channel message longer than the %d bytes it declared", dc.total)
	}
	if !dc.drop {
		dc.buf = append(dc.buf, data...)
	}
	if dc.got < dc.total {
		return nil
	}
	dc.partial = false
	if dc.drop {
		c.count(func(s *Stats) { s.Ignored["dynamic channel messages over their bound"]++ })
		return nil
	}
	return dc.handle(c, out, dc.buf)
}

// dvcAnswer queues a PDU for the channel writer. chio.mu must be held.
func (c *Client) dvcAnswer(p []byte) error {
	if len(c.dvc.answers) == maxDynvcAnswers {
		return fmt.Errorf("rdp: more than %d dynamic channel answers await writing", maxDynvcAnswers)
	}
	c.dvc.answers = append(c.dvc.answers, p)
	return nil
}

// dynvcDue appends the DRDYNVC PDUs that are due: the answers the
// reader owes, then a monitor layout. chio.mu must be held.
func (c *Client) dynvcDue(msgs []dueMessage, now time.Time) []dueMessage {
	vc := c.dynCh
	if vc == nil {
		return msgs
	}
	for _, p := range c.dvc.answers {
		msgs = append(msgs, dueMessage{vc, func() []byte { return p }})
	}
	clear(c.dvc.answers)
	c.dvc.answers = c.dvc.answers[:0]
	if p := c.layoutDue(now); p != nil {
		msgs = append(msgs, dueMessage{vc, func() []byte { return p }})
	}
	return msgs
}

// dvcCapsResponse is DYNVC_CAPS_RSP ([MS-RDPEDYC] 2.2.1.2).
func dvcCapsResponse(version int) []byte {
	return binary.LittleEndian.AppendUint16([]byte{dvcCaps << 4, 0}, uint16(version)) // #nosec G115 -- 1 or 2
}

// dvcPDU is a DRDYNVC PDU of cmd with the channel id in the size cbID
// gives, sp in the header's middle bits, and body after the id.
func dvcPDU(cmd, sp, cbID int, id uint32, body []byte) []byte {
	p := []byte{byte(cmd<<4 | sp<<2 | cbID)} // #nosec G115 -- a command of 4 bits, sizes of 2
	p = appendVarUint(p, cbID, id)
	return append(p, body...)
}

// varUint reads an unsigned integer of 1, 2 or 4 bytes as size, a cbId
// or Len field, gives: 0, 1, or 2 or 3.
func varUint(r *reader, size int) uint32 {
	switch size {
	case 0:
		return uint32(r.u8())
	case 1:
		return uint32(r.u16())
	}
	return r.u32()
}

// appendVarUint appends v in the size code size.
func appendVarUint(p []byte, size int, v uint32) []byte {
	switch size {
	case 0:
		return append(p, byte(v)) // #nosec G115 -- the server's own id, of this size
	case 1:
		return binary.LittleEndian.AppendUint16(p, uint16(v)) // #nosec G115 -- the server's own id, of this size
	}
	return binary.LittleEndian.AppendUint32(p, v)
}

// sizeCode returns the smallest size code that holds v.
func sizeCode(v uint32) int {
	switch {
	case v <= 0xff:
		return 0
	case v <= 0xffff:
		return 1
	}
	return 2
}

// Display Control ([MS-RDPEDISP]): the server sends the most monitors
// and the most area it takes, and the client asks for a desktop size
// with a monitor layout of one monitor. The server answers by
// reactivating the session with the new size, or not at all; there is
// no acknowledgement, and xrdp ignores a layout while its login is in
// progress. So the client keeps the newest size asked for until the
// desktop has it. It sends a layout of a new size at most every
// layoutEvery, as guacd does, and the same size again every layoutRetry
// while the desktop differs, at most maxLayoutTries times in all, where
// guacd sends it every half second without end.

// displayControl is the Display Control channel's name.
const displayControl = "Microsoft::Windows::RDS::DisplayControl"

const (
	dispCapsType   = 0x00000005
	dispLayoutType = 0x00000002
	// dispMonitorSize is the size of a DISPLAYCONTROL_MONITOR_LAYOUT.
	dispMonitorSize = 40
	// dispMonitorPrimary flags the primary monitor.
	dispMonitorPrimary = 0x00000001
	// maxDisplayMessage bounds a message on the Display Control channel,
	// whose server PDU is 20 bytes.
	maxDisplayMessage = 1 << 10
	// layoutEvery is the least time between two monitor layouts, guacd's
	// GUAC_RDP_DISP_UPDATE_INTERVAL.
	layoutEvery = 500 * time.Millisecond
	// layoutRetry is the least time before another layout of the size the
	// last one asked for, which the server has not applied.
	layoutRetry = 2 * time.Second
	// maxLayoutTries bounds the layouts of one size.
	maxLayoutTries = 8
	// minLayoutSide is the least side of a monitor ([MS-RDPEDISP]
	// 2.2.2.2.1).
	minLayoutSide = 200
)

// display is the state of Display Control, under chio.mu.
type display struct {
	ch      *dynChannel // the open channel, nil if none
	caps    bool        // the server sent its capabilities
	maxArea int64       // the most pixels of a monitor the server takes
	want    image.Point // the newest size asked for, until the desktop has it
	sent    image.Point // the size of the last layout
	tries   int         // the layouts of that size
	last    time.Time   // when the last went
	timer   *time.Timer // wakes the channel writer for the next layout
	wake    time.Time   // when timer fires, zero if it is not set
}

// displayMessage applies a Display Control PDU from the server: its
// capabilities. Others are ignored.
func (c *Client) displayMessage(_ Output, msg []byte) error {
	r := reader{b: msg}
	typ, n := r.u32(), r.u32()
	if r.err != nil || int64(n) != int64(len(msg)) {
		return fmt.Errorf("rdp: Display Control PDU of %d bytes says %d", len(msg), n)
	}
	if typ != dispCapsType {
		c.count(func(s *Stats) { s.Ignored["Display Control PDUs"]++ })
		return nil
	}
	monitors, a, b := r.u32(), r.u32(), r.u32()
	if r.err != nil {
		return fmt.Errorf("rdp: Display Control capabilities: %w", r.err)
	}
	// The area is MaxNumMonitors × FactorA × FactorB; the client asks for
	// one monitor, of at most maxPixels.
	area := uint64(a) * uint64(b)
	switch {
	case monitors == 0 || area == 0:
		area = 0
	case area >= maxPixels:
		area = maxPixels
	default: // area*monitors < 2^25 * 2^32
		area = min(area*uint64(monitors), maxPixels)
	}
	c.chio.mu.Lock()
	c.disp.caps, c.disp.maxArea = true, int64(area) // #nosec G115 -- at most maxPixels
	c.chio.mu.Unlock()
	c.due()
	return nil
}

// RequestSize asks the server for a desktop of width×height through
// Display Control, and returns at once. The size is clamped to the
// sides a monitor may have, 200 to 8192 pixels, the width made even, as
// [MS-RDPEDISP] requires; a size beyond the area the server takes is
// not asked for. The newest size wins: it goes once the server has
// opened the channel and sent its capabilities, at most one size every
// half second, and again every two seconds, up to eight times, until the
// desktop has it. The server resizes by reactivating the session, which
// Run reports to Output.Resize; it may not resize at all. Without
// Config.Resize, or with a server that does not open the channel,
// RequestSize does nothing.
func (c *Client) RequestSize(width, height int) {
	if !c.cfg.Resize || c.dynCh == nil {
		return
	}
	w := min(max(width, minLayoutSide), maxSide) &^ 1
	h := min(max(height, minLayoutSide), maxSide)
	c.chio.mu.Lock()
	c.disp.want, c.disp.tries = image.Pt(w, h), 0
	c.chio.mu.Unlock()
	c.due()
}

// layoutDue returns the monitor layout PDU that is due, as a Data PDU
// on the Display Control channel, or nil. chio.mu must be held.
func (c *Client) layoutDue(now time.Time) []byte {
	d := &c.disp
	if d.ch == nil || !d.caps || d.want == (image.Point{}) {
		return nil
	}
	w, h := c.Size()
	if d.want == image.Pt(w, h) {
		d.want = image.Point{}
		return nil
	}
	if int64(d.want.X)*int64(d.want.Y) > d.maxArea {
		c.countLocked("sizes beyond the server's area")
		d.want = image.Point{}
		return nil
	}
	every := c.layoutEvery
	if d.want == d.sent {
		if d.tries >= maxLayoutTries {
			c.countLocked("sizes the server did not apply")
			d.want = image.Point{}
			return nil
		}
		every = c.layoutRetry
	}
	if wait := d.last.Add(every).Sub(now); wait > 0 {
		c.layoutAfter(now, wait)
		return nil
	}
	if d.want != d.sent {
		d.sent, d.tries = d.want, 0
	}
	d.tries++
	d.last = now
	// Unless the desktop takes the size by then, it goes again.
	c.layoutAfter(now, c.layoutRetry)
	return dvcPDU(dvcData, 0, sizeCode(d.ch.id), d.ch.id, monitorLayout(d.want))
}

// layoutAfter has the channel writer look at the layout again wait
// after now, unless it is to look by then already. chio.mu must be
// held.
func (c *Client) layoutAfter(now time.Time, wait time.Duration) {
	d := &c.disp
	at := now.Add(wait)
	if !d.wake.IsZero() && !d.wake.After(at) {
		return
	}
	d.wake = at
	if d.timer == nil {
		d.timer = time.AfterFunc(wait, func() {
			c.chio.mu.Lock()
			c.disp.wake = time.Time{}
			c.chio.mu.Unlock()
			c.due()
		})
		return
	}
	d.timer.Reset(wait)
}

// monitorLayout is DISPLAYCONTROL_MONITOR_LAYOUT_PDU ([MS-RDPEDISP]
// 2.2.2.2) of one primary monitor of size at the origin, its physical
// size unknown and its scale 100%.
func monitorLayout(size image.Point) []byte {
	var w writer
	w.u32(dispLayoutType)
	w.u32(8 + 8 + dispMonitorSize)
	w.u32(dispMonitorSize)
	w.u32(1)
	w.u32(dispMonitorPrimary)
	w.u32(0)              // Left
	w.u32(0)              // Top
	w.u32(uint32(size.X)) // #nosec G115 -- 200 to 8192
	w.u32(uint32(size.Y)) // #nosec G115 -- 200 to 8192
	w.u32(0)              // PhysicalWidth: unknown
	w.u32(0)              // PhysicalHeight
	w.u32(0)              // Orientation: landscape
	w.u32(100)            // DesktopScaleFactor
	w.u32(100)            // DeviceScaleFactor
	return w.b
}
