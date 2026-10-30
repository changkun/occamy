// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package client

import (
	"fmt"
	"image"
	"image/draw"
	"slices"
	"time"

	"changkun.de/x/occamy/protocol"
)

// frame is a frame being read, or read and not yet delivered.
type frame struct {
	ops     []op
	end     protocol.FrameEnd
	arrived time.Time
	bytes   int // what its ops hold
}

type opKind uint8

const (
	opResize opKind = iota + 1
	opTile
	opCopy
	opTermData
	opTermSnapshot
)

// op is a message of a frame.
type op struct {
	kind opKind
	r    image.Rectangle // a tile's, a copy's destination, the screen of a resize
	src  image.Point     // a copy's source
	data []byte          // a raw tile's pixels, a terminal's bytes
	job  *job            // a tile of a plugged codec
}

// read reads the server's messages until the session ends.
func (s *Session) read() {
	defer s.wg.Done()
	for {
		b, err := s.conn.ReadMessage(s.ctx)
		if err == nil {
			err = s.handle(b)
		}
		if err != nil {
			select {
			case <-s.closed: // the session ended, and the connection with it
			default:
				s.fail(err)
			}
			return
		}
	}
}

// handle applies one message from the server. Messages within a frame
// join the open frame, which Next applies once it is complete; the rest
// become events at once.
func (s *Session) handle(b []byte) error {
	m, err := protocol.Decode(b, s.reg, s.limits)
	if err != nil {
		return err
	}
	switch m := m.(type) {
	case nil: // an ignorable message of a later version
		return nil
	case *protocol.Resize:
		return s.resize(m, len(b))
	case *protocol.Tile:
		return s.tile(m, len(b))
	case *protocol.CopyRect:
		return s.copyRect(m, len(b))
	case *protocol.FrameEnd:
		return s.frameEnd(m)
	case *protocol.TermSnapshot:
		return s.term(opTermSnapshot, m.Data, len(b))
	case *protocol.TermData:
		return s.term(opTermData, m.Data, len(b))
	case *protocol.Cursor:
		c, n, err := s.cursor(m)
		if err != nil {
			return err
		}
		return s.event(c, n)
	case *protocol.CursorMove:
		return s.event(CursorMove{X: int(m.X), Y: int(m.Y)}, len(b))
	case *protocol.ServerClipboard:
		return s.event(Clipboard{MIME: m.MIME, Data: m.Data}, len(b))
	case *protocol.Bell:
		return s.event(Bell{}, len(b))
	case *protocol.Title:
		return s.event(Title{Title: m.Title}, len(b))
	case *protocol.Prompt:
		return s.event(Prompt{ID: m.ID, Name: m.Name, Instruction: m.Instruction, Fields: m.Fields}, len(b))
	case *protocol.ViewerJoined:
		return s.event(ViewerJoined{ViewerID: m.ViewerID, Role: m.Role, Name: m.Name}, len(b))
	case *protocol.ViewerLeft:
		return s.event(ViewerLeft{ViewerID: m.ViewerID}, len(b))
	case *protocol.Ping:
		s.out.pong(protocol.Pong(*m))
		return nil
	case *protocol.Pong:
		return nil
	case *protocol.Close:
		return &CloseError{Code: m.Code, Reason: m.Reason}
	case *protocol.Welcome:
		return fmt.Errorf("%w: a second welcome", protocol.ErrInvalid)
	}
	return fmt.Errorf("%w: %v from the server", protocol.ErrUnsupported, m.Type())
}

func (s *Session) graphical() bool { return s.welcome.Class == protocol.ClassGraphical }

func (s *Session) maxPixels() int {
	if s.opts.MaxPixels > 0 {
		return s.opts.MaxPixels
	}
	return DefaultMaxPixels
}

func (s *Session) resize(m *protocol.Resize, n int) error {
	w, h := int(m.Width), int(m.Height)
	if s.graphical() && w*h > s.maxPixels() {
		return fmt.Errorf("%w: a %dx%d screen, more than %d pixels", protocol.ErrUnsupported, w, h, s.maxPixels())
	}
	s.wire, s.sized = image.Pt(w, h), true
	return s.add(op{kind: opResize, r: image.Rect(0, 0, w, h)}, n)
}

// screen checks that r lies within the screen as the messages read so
// far left it.
func (s *Session) screen(what string, r image.Rectangle) error {
	if !s.sized || !r.In(image.Rectangle{Max: s.wire}) {
		return fmt.Errorf("%w: %s %v outside the screen %v", protocol.ErrInvalid, what, r, s.wire)
	}
	return nil
}

// inFrame checks that a message of frame id belongs to the open frame.
func (s *Session) inFrame(id uint32) error {
	if id != s.lastEnd+1 {
		return fmt.Errorf("%w: a message of frame %d in frame %d", protocol.ErrInvalid, id, s.lastEnd+1)
	}
	return nil
}

func (s *Session) tile(m *protocol.Tile, n int) error {
	if !s.graphical() {
		return fmt.Errorf("%w: a tile in a terminal session", protocol.ErrInvalid)
	}
	if err := s.inFrame(m.FrameID); err != nil {
		return err
	}
	r := image.Rect(int(m.X), int(m.Y), int(m.X)+int(m.W), int(m.Y)+int(m.H))
	if err := s.screen("tile", r); err != nil {
		return err
	}
	if !slices.Contains(s.agreed.Codecs, m.Codec) {
		return fmt.Errorf("%w: a tile of codec %v, which was not agreed", protocol.ErrInvalid, m.Codec)
	}
	o := op{kind: opTile, r: r}
	if m.Codec == protocol.CodecRaw {
		o.data = m.Payload
	} else {
		o.job = &job{dec: s.decoders[m.Codec], payload: m.Payload, w: r.Dx(), h: r.Dy(), done: make(chan struct{})}
		n += 4 * r.Dx() * r.Dy()
	}
	if err := s.add(o, n); err != nil {
		return err
	}
	if o.job != nil {
		s.pool.submit(o.job)
	}
	return nil
}

func (s *Session) copyRect(m *protocol.CopyRect, n int) error {
	if !s.graphical() {
		return fmt.Errorf("%w: a copy-rect in a terminal session", protocol.ErrInvalid)
	}
	if err := s.inFrame(m.FrameID); err != nil {
		return err
	}
	dst := image.Rect(int(m.DstX), int(m.DstY), int(m.DstX)+int(m.W), int(m.DstY)+int(m.H))
	src := image.Rect(int(m.SrcX), int(m.SrcY), int(m.SrcX)+int(m.W), int(m.SrcY)+int(m.H))
	if err := s.screen("copy-rect destination", dst); err != nil {
		return err
	}
	if err := s.screen("copy-rect source", src); err != nil {
		return err
	}
	if dst.Empty() {
		return nil // copies nothing
	}
	return s.add(op{kind: opCopy, r: dst, src: src.Min}, n)
}

func (s *Session) term(kind opKind, data []byte, n int) error {
	if s.graphical() {
		return fmt.Errorf("%w: terminal data in a graphical session", protocol.ErrInvalid)
	}
	return s.add(op{kind: kind, data: data}, n)
}

// add adds o, which holds n bytes, to the open frame.
func (s *Session) add(o op, n int) error {
	if s.open == nil {
		s.open = &frame{}
	}
	s.open.ops = append(s.open.ops, o)
	s.open.bytes += n
	s.mu.Lock()
	defer s.mu.Unlock()
	s.backlog += n
	return s.checkBacklog()
}

func (s *Session) frameEnd(m *protocol.FrameEnd) error {
	if m.FrameID != s.lastEnd+1 {
		return fmt.Errorf("%w: frame-end %d, want %d", protocol.ErrInvalid, m.FrameID, s.lastEnd+1)
	}
	s.lastEnd = m.FrameID
	f := s.open
	s.open = nil
	if f == nil {
		f = &frame{}
	}
	f.end, f.arrived = *m, time.Now()
	s.mu.Lock()
	if s.frames > s.window {
		s.mu.Unlock()
		return fmt.Errorf("%w: more than %d frames unacknowledged", protocol.ErrInvalid, s.window+1)
	}
	s.queue = append(s.queue, item{f: f})
	s.frames++
	s.mu.Unlock()
	s.poke()
	return nil
}

// maxBacklog is the most the Session holds of undelivered frames and
// events. A server that follows the protocol keeps at most the frame
// window and one more frame unacknowledged, each about the screen's
// size, and sends the small state as it changes, which the Session
// coalesces; one that sends more than three screens a frame beyond that
// is taken for broken.
func (s *Session) maxBacklog() int {
	screen := int64(4 * s.wire.X * s.wire.Y)
	limit := int64(s.window+2) * 3 * screen
	return int(max(min(limit, 1<<30), minBacklog))
}

func (s *Session) checkBacklog() error {
	if s.backlog > s.maxBacklog() {
		return fmt.Errorf("%w: %d bytes of frames and events undelivered, more than %d", protocol.ErrInvalid, s.backlog, s.maxBacklog())
	}
	return nil
}

// cursor decodes a cursor's image. It reports what the event holds.
func (s *Session) cursor(m *protocol.Cursor) (Cursor, int, error) {
	if m.W == 0 {
		return Cursor{}, 0, nil // hidden
	}
	if !slices.Contains(s.agreed.Codecs, m.Codec) {
		return Cursor{}, 0, fmt.Errorf("%w: a cursor of codec %v, which was not agreed", protocol.ErrInvalid, m.Codec)
	}
	r := image.Rect(0, 0, int(m.W), int(m.H))
	img := &image.NRGBA{Pix: m.Payload, Stride: 4 * r.Dx(), Rect: r}
	if m.Codec != protocol.CodecRaw {
		img = image.NewNRGBA(r)
		if err := s.decoders[m.Codec].Decode(img.Pix, r.Dx(), r.Dy(), m.Payload); err != nil {
			return Cursor{}, 0, fmt.Errorf("%w: %v cursor: %w", errDecode, m.Codec, err)
		}
	}
	return Cursor{Image: img, Hot: image.Pt(int(m.HotX), int(m.HotY))}, len(img.Pix), nil
}

// event queues a small event that holds n bytes. An event that holds
// the latest value replaces an undelivered one of its kind, a viewer
// that left cancels its undelivered join, and bells beyond a few are
// dropped.
func (s *Session) event(ev Event, n int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch ev := ev.(type) {
	case Title, Clipboard, Cursor, CursorMove:
		for i := len(s.queue) - 1; i >= 0; i-- {
			if it := &s.queue[i]; it.f == nil && sameKind(it.ev, ev) {
				s.backlog += n - it.bytes
				it.ev, it.bytes = ev, n
				return s.checkBacklog()
			}
		}
	case Bell:
		bells := 0
		for _, it := range s.queue {
			if _, ok := it.ev.(Bell); ok {
				bells++
			}
		}
		if bells >= maxBells {
			return nil
		}
	case ViewerLeft:
		for i, it := range s.queue {
			if j, ok := it.ev.(ViewerJoined); ok && j.ViewerID == ev.ViewerID {
				s.backlog -= it.bytes
				s.queue = slices.Delete(s.queue, i, i+1)
				return nil
			}
		}
	}
	if len(s.queue)-s.frames >= maxEvents {
		return fmt.Errorf("%w: more than %d events undelivered", protocol.ErrInvalid, maxEvents)
	}
	s.queue = append(s.queue, item{ev: ev, bytes: n})
	s.backlog += n
	s.poke()
	return s.checkBacklog()
}

func sameKind(a, b Event) bool {
	switch a.(type) {
	case Title:
		_, ok := b.(Title)
		return ok
	case Clipboard:
		_, ok := b.(Clipboard)
		return ok
	case Cursor:
		_, ok := b.(Cursor)
		return ok
	case CursorMove:
		_, ok := b.(CursorMove)
		return ok
	}
	return false
}

// apply applies frame f and returns its events. A graphical frame's
// tiles are decoded by then, or soon; the framebuffer is locked only
// while the frame is drawn into it.
func (s *Session) apply(f *frame) ([]Event, error) {
	end := Frame{
		ID:         f.end.FrameID,
		LastSeq:    f.end.LastSeq,
		ServerTime: time.Duration(min(f.end.ServerTime, uint64(1<<63-1)/1000)) * time.Microsecond, // #nosec G115 -- clamped
		arrived:    f.arrived,
	}
	var evs []Event
	if !s.graphical() {
		for _, o := range f.ops {
			switch o.kind {
			case opResize:
				evs = append(evs, Resize{Width: o.r.Dx(), Height: o.r.Dy()})
			case opTermSnapshot:
				evs = append(evs, TermSnapshot{Data: o.data})
			case opTermData:
				evs = append(evs, TermData{Data: o.data})
			}
		}
		return append(evs, end), nil
	}
	for _, o := range f.ops {
		if o.job != nil {
			<-o.job.done
			if o.job.err != nil {
				return nil, o.job.err
			}
		}
	}
	s.fbMu.Lock()
	defer s.fbMu.Unlock()
	resized := false
	for _, o := range f.ops {
		switch o.kind {
		case opResize:
			s.fb = image.NewRGBA(o.r)
			resized = true
		case opTile:
			pix := o.data
			if o.job != nil {
				pix = o.job.pix
			}
			blit(s.fb, o.r, pix)
			if !resized {
				end.Dirty = append(end.Dirty, o.r)
			}
		case opCopy:
			// draw.Draw copies overlapping rectangles of one image
			// correctly.
			draw.Draw(s.fb, o.r, s.fb, o.src, draw.Src)
			if !resized {
				end.Dirty = append(end.Dirty, o.r)
			}
		}
	}
	if resized {
		evs = append(evs, Resize{Width: s.fb.Rect.Dx(), Height: s.fb.Rect.Dy()})
		end.Dirty = []image.Rectangle{s.fb.Rect}
	}
	return append(evs, end), nil
}

// blit copies pix, the pixels of r row by row, into dst.
func blit(dst *image.RGBA, r image.Rectangle, pix []byte) {
	w := 4 * r.Dx()
	for y := range r.Dy() {
		i := dst.PixOffset(r.Min.X, r.Min.Y+y)
		copy(dst.Pix[i:i+w], pix[y*w:(y+1)*w])
	}
}
