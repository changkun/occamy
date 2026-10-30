// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package client

import (
	"context"
	"fmt"
	"image"
	"sync"

	"changkun.de/x/occamy/protocol"
)

// maxPongs bounds the pongs waiting to be sent. A server sends one ping
// at a time; one that floods loses the oldest.
const maxPongs = 16

// Key sends a key press (down) or release: keysym is the X11 keysym the
// key produced, and code the physical key as a USB HID usage from the
// keyboard page, or 0. It returns the input's sequence number.
//
// Input methods queue their message for the writer and return; they
// wait only while more than a message's worth of input is queued and
// the network has not taken it, until ctx is done. A read-only viewer's
// input is refused with ErrReadOnly, and input after the session ended
// with ErrClosed.
func (s *Session) Key(ctx context.Context, down bool, keysym uint32, code uint16) (uint32, error) {
	return s.input(ctx, func(seq uint32) protocol.Message {
		return &protocol.Key{Seq: seq, Down: down, Keysym: keysym, Code: code}
	})
}

// Text sends text that an input method composed. Ordinary typing goes
// as Key.
func (s *Session) Text(ctx context.Context, text string) (uint32, error) {
	return s.input(ctx, func(seq uint32) protocol.Message {
		return &protocol.Text{Seq: seq, Text: text}
	})
}

// TermInput sends the bytes the caller's terminal emulator produced for
// a terminal session, such as xterm.js's onData.
func (s *Session) TermInput(ctx context.Context, data []byte) (uint32, error) {
	return s.input(ctx, func(seq uint32) protocol.Message {
		return &protocol.TermInput{Seq: seq, Data: data}
	})
}

// Pointer sends the pointer's position on the remote screen, in
// pixels, and the buttons held.
func (s *Session) Pointer(ctx context.Context, x, y uint16, buttons protocol.Buttons) (uint32, error) {
	return s.input(ctx, func(seq uint32) protocol.Message {
		return &protocol.Pointer{Seq: seq, X: x, Y: y, Buttons: buttons}
	})
}

// Wheel sends a scroll: positive dx scrolls right and positive dy down.
func (s *Session) Wheel(ctx context.Context, dx, dy int32, mode protocol.WheelMode) (uint32, error) {
	return s.input(ctx, func(seq uint32) protocol.Message {
		return &protocol.Wheel{Seq: seq, DX: dx, DY: dy, Mode: mode}
	})
}

// Resize asks the remote for a screen size: pixels for a graphical
// session, character cells for a terminal. A Resize event reports what
// the remote did.
func (s *Session) Resize(ctx context.Context, width, height uint16) (uint32, error) {
	return s.input(ctx, func(seq uint32) protocol.Message {
		return &protocol.ResizeRequest{Seq: seq, Width: width, Height: height}
	})
}

// Clipboard sends the caller's clipboard for the remote's.
func (s *Session) Clipboard(ctx context.Context, mime string, data []byte) (uint32, error) {
	return s.input(ctx, func(seq uint32) protocol.Message {
		return &protocol.ClientClipboard{Seq: seq, MIME: mime, Data: data}
	})
}

// View tells the server whether the client can be seen, and which part
// of the screen it shows, clamped to the protocol's coordinates; an
// empty viewport is the whole screen, and one that shows nothing of a
// screen, such as one at negative coordinates only, is refused. A
// hidden client gets no frames, and one frame of the current screen
// when it shows again.
func (s *Session) View(ctx context.Context, visible bool, viewport image.Rectangle) error {
	m := protocol.View{Visible: visible}
	if !viewport.Empty() {
		clamped := viewport.Intersect(image.Rect(0, 0, 1<<16-1, 1<<16-1))
		if clamped.Empty() {
			// An empty viewport on the wire is the whole screen.
			return fmt.Errorf("client: viewport %v shows nothing of a screen", viewport)
		}
		viewport = clamped
		m.X, m.Y = uint16(viewport.Min.X), uint16(viewport.Min.Y) // #nosec G115 -- clamped above
		m.W, m.H = uint16(viewport.Dx()), uint16(viewport.Dy())   // #nosec G115 -- clamped above
	}
	_, err := s.send(ctx, func(uint32) protocol.Message { return &m }, false)
	return err
}

// PromptReply answers the owner's prompt id, with one answer per field.
// The answers may be secrets and are not logged.
func (s *Session) PromptReply(ctx context.Context, id uint32, answers []string) error {
	if s.welcome.Role != protocol.RoleOwner {
		return ErrNotOwner
	}
	_, err := s.send(ctx, func(uint32) protocol.Message {
		return &protocol.PromptReply{ID: id, Answers: answers}
	}, false)
	return err
}

func (s *Session) input(ctx context.Context, build func(seq uint32) protocol.Message) (uint32, error) {
	if s.welcome.Role == protocol.RoleReadOnly {
		return 0, ErrReadOnly
	}
	return s.send(ctx, build, true)
}

// send queues the message that build returns for the writer. An input
// takes the next sequence number, in the order of the queue and so of
// the wire, wrapping from 2³²−1 to 1.
func (s *Session) send(ctx context.Context, build func(seq uint32) protocol.Message, input bool) (uint32, error) {
	o := &s.out
	for {
		o.mu.Lock()
		if o.stop {
			o.mu.Unlock()
			return 0, ErrClosed
		}
		seq := uint32(0)
		if input {
			// After 2³²−1 comes 1: 0 is never an input's number, so
			// last_seq 0 always means none.
			seq = max(o.seq+1, 1)
		}
		msg := build(seq).Append(nil)
		if err := s.limits.CheckSize(len(msg)); err != nil {
			o.mu.Unlock()
			return 0, err
		}
		if o.bytes == 0 || o.bytes+len(msg) <= o.limit {
			if input {
				o.seq = seq
			}
			o.q = append(o.q, msg)
			o.bytes += len(msg)
			o.mu.Unlock()
			o.poke()
			return seq, nil
		}
		space := o.space
		o.mu.Unlock()
		select {
		case <-space:
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
}

// outbox holds what the writer sends: the latest frame ack and pongs
// first, then the queued messages in order, and close last.
type outbox struct {
	mu    sync.Mutex
	acked *protocol.FrameAck
	pongs []protocol.Pong
	q     [][]byte
	bytes int // in q
	limit int // of bytes in q, beyond one message
	seq   uint32
	stop  bool   // the session ended: nothing more is queued
	flush bool   // send q before last
	last  []byte // close, sent last

	wake    chan struct{} // holds a token when there is news
	space   chan struct{} // closed when bytes drop, and replaced
	flushed chan struct{} // closed when the writer exits
}

func (o *outbox) init(limit int) {
	o.limit = limit
	o.wake = make(chan struct{}, 1)
	o.space = make(chan struct{})
	o.flushed = make(chan struct{})
}

func (o *outbox) poke() {
	select {
	case o.wake <- struct{}{}:
	default:
	}
}

// ack replaces the waiting ack with a, which acknowledges every frame
// before it too.
func (o *outbox) ack(a protocol.FrameAck) {
	o.mu.Lock()
	o.acked = &a
	o.mu.Unlock()
	o.poke()
}

func (o *outbox) pong(p protocol.Pong) {
	o.mu.Lock()
	if len(o.pongs) == maxPongs {
		o.pongs = append(o.pongs[:0], o.pongs[1:]...)
	}
	o.pongs = append(o.pongs, p)
	o.mu.Unlock()
	o.poke()
}

// finish stops the queue. The writer then sends last, after what is
// queued if flush is set, and exits.
func (o *outbox) finish(last []byte, flush bool) {
	o.mu.Lock()
	o.stop, o.flush, o.last = true, flush, last
	close(o.space)
	o.space = make(chan struct{})
	o.mu.Unlock()
	o.poke()
}

// take returns the next message to send, encoding an ack or a pong into
// buf, and how many queued bytes it frees. It reports false once the
// writer is done, or ctx is.
func (o *outbox) take(ctx context.Context, buf []byte) (msg []byte, n int, ok bool) {
	for {
		o.mu.Lock()
		switch {
		case o.stop && !o.flush:
		case o.acked != nil:
			msg, o.acked = o.acked.Append(buf[:0]), nil
		case len(o.pongs) > 0:
			msg = o.pongs[0].Append(buf[:0])
			o.pongs = append(o.pongs[:0], o.pongs[1:]...)
		case len(o.q) > 0:
			msg = o.q[0]
			o.q[0] = nil
			o.q = o.q[1:]
			n = len(msg)
		}
		if msg == nil && o.stop {
			msg, o.last = o.last, nil
			o.mu.Unlock()
			return msg, 0, msg != nil
		}
		o.mu.Unlock()
		if msg != nil {
			return msg, n, true
		}
		select {
		case <-o.wake:
		case <-ctx.Done():
			return nil, 0, false
		}
	}
}

// sent records that the writer sent n queued bytes.
func (o *outbox) sent(n int) {
	if n == 0 {
		return
	}
	o.mu.Lock()
	o.bytes -= n
	close(o.space)
	o.space = make(chan struct{})
	o.mu.Unlock()
}

// write sends the outbox until the session ends.
func (s *Session) write() {
	defer s.wg.Done()
	defer close(s.out.flushed)
	var buf []byte
	for {
		msg, n, ok := s.out.take(s.ctx, buf)
		if !ok {
			return
		}
		if n == 0 {
			buf = msg // an ack or pong, encoded into buf
		}
		err := s.conn.WriteMessage(s.ctx, msg)
		s.out.sent(n)
		if err != nil {
			select {
			case <-s.closed:
			default:
				s.fail(err)
			}
			return
		}
	}
}
