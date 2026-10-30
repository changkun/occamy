// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package vnc

// ContinuousUpdates and Fence (rfbproto). With continuous updates, the
// server pushes the changes to an area without update requests, and
// paces itself by its own congestion control, which TigerVNC bases on
// the client's answers to its Fences: it enables continuous updates only
// for a client that takes Fences. The backend uses them once the server
// has sent both a Fence and EndOfContinuousUpdates, which it sends in
// answer to SetEncodings, and enables them while a viewer can see the
// screen and disables them while nobody can.
//
// The backend answers every Fence the server asks for, honouring
// BlockBefore only: the reader has applied every message before the
// Fence when it passes the answer to the writer, which writes the
// answers in order, but it reads on without waiting for the answer to
// be written, so BlockAfter and SyncNext are cleared from the answer.
// The backend sends no Fence of its own: it never changes the pixel
// format, which is what SyncNext is for.

import (
	"encoding/binary"
	"errors"
	"fmt"
	"image"
)

// The pseudo-encodings and messages.
const (
	encContinuousUpdates int32 = -313
	encFence             int32 = -312

	msgEnableContinuousUpdates = 150 // client
	msgEndOfContinuousUpdates  = 150 // server
	msgClientFence             = 248
	msgServerFence             = 248
)

// Fence flags.
const (
	fenceBlockBefore uint32 = 1 << 0
	fenceBlockAfter  uint32 = 1 << 1
	fenceSyncNext    uint32 = 1 << 2
	fenceRequest     uint32 = 1 << 31
)

// maxFencePayload bounds a Fence's payload.
const maxFencePayload = 64

// errWriterStopped is the reader's error when an answer to a Fence
// cannot be sent because the writer has stopped.
var errWriterStopped = errors.New("vnc: the writer has stopped")

// continuous is the state of continuous updates, under run.mu.
type continuous struct {
	fence     bool        // the server has sent a Fence
	supported bool        // the server has sent EndOfContinuousUpdates
	on        bool        // enabled, as last sent
	area      image.Point // the screen they were enabled for
}

// serverFence reads a ServerFence after its type and passes it to the
// peer.
func (d *decoder) serverFence() error {
	if err := d.read(d.hdr[:8]); err != nil {
		return fmt.Errorf("vnc: fence: %w", err)
	}
	flags, n := binary.BigEndian.Uint32(d.hdr[3:]), int(d.hdr[7])
	if n > maxFencePayload {
		return fmt.Errorf("vnc: fence payload of %d bytes exceeds %d", n, maxFencePayload)
	}
	var payload [maxFencePayload]byte
	if err := d.read(payload[:n]); err != nil {
		return fmt.Errorf("vnc: fence: %w", err)
	}
	return d.tell().fence(flags, payload[:n])
}

// fence implements peer. The answer goes to the writer through the
// control queue, which keeps answers in order; when the queue is full,
// the reader waits for the writer, so that a server that sends Fences
// without reading the answers stops being read too.
func (r *run) fence(flags uint32, payload []byte) error {
	r.mu.Lock()
	r.cont.fence = true
	r.mu.Unlock()
	r.poke()
	if flags&fenceRequest == 0 {
		return nil // an answer, though the backend asks nothing
	}
	p := appendFence(nil, flags&fenceBlockBefore, payload)
	select {
	case r.control <- p:
		return nil
	case <-r.wdone:
		return errWriterStopped
	}
}

// continuousUpdates implements peer.
func (r *run) continuousUpdates() {
	r.mu.Lock()
	r.cont.supported = true
	r.mu.Unlock()
	r.poke()
}

// continuousDue appends the EnableContinuousUpdates that is due, if
// any: on for the whole screen while a viewer can see it, again after a
// resize, and off while nobody can. r.mu must be held.
func (r *run) continuousDue(p []byte, visible bool) []byte {
	c := &r.cont
	if !c.fence || !c.supported || r.size == (image.Point{}) {
		return p
	}
	switch {
	case visible && (!c.on || c.area != r.size):
		c.on, c.area = true, r.size
		return appendEnableContinuous(p, true, r.size)
	case !visible && c.on:
		c.on = false
		return appendEnableContinuous(p, false, r.size)
	}
	return p
}

// appendEnableContinuous appends an EnableContinuousUpdates for a
// screen of size.
func appendEnableContinuous(p []byte, enable bool, size image.Point) []byte {
	on := byte(0)
	if enable {
		on = 1
	}
	p = append(p, msgEnableContinuousUpdates, on, 0, 0, 0, 0)
	p = binary.BigEndian.AppendUint16(p, u16(size.X))
	return binary.BigEndian.AppendUint16(p, u16(size.Y))
}

// appendFence appends a ClientFence with a payload of at most
// maxFencePayload bytes.
func appendFence(p []byte, flags uint32, payload []byte) []byte {
	p = append(p, msgClientFence, 0, 0, 0)
	p = binary.BigEndian.AppendUint32(p, flags)
	p = append(p, byte(len(payload))) // #nosec G115 -- at most maxFencePayload
	return append(p, payload...)
}
