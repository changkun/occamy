// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package stream

import (
	"context"
	"image"
	"log/slog"
	"sync"
	"time"

	"changkun.de/x/occamy/internal/framebuffer"
	"changkun.de/x/occamy/internal/pacer"
	"changkun.de/x/occamy/internal/session"
	"changkun.de/x/occamy/protocol"
)

// inputTimeout is how long the writer waits for an input's effect to
// show before it stops giving the input's frame priority: a key that
// changes nothing on the screen must not hand an extra frame to whatever
// changes next.
const inputTimeout = time.Second

// inputFrameBytes is what an input's frame carries before it ends at the
// next drawing operation's boundary, so that its frame-end is not held
// behind motion that changed in the same moment. The motion follows in
// the next frames.
const inputFrameBytes = 16 << 10

// activeWindow is how far back the writer looks for motion when an input
// arrives: a tile that changed within it was in motion, and its next
// change is not taken for the input's effect.
const activeWindow = 100 * time.Millisecond

// maxBells bounds the bells sent at once; more ring as one burst anyway.
const maxBells = 3

// writer sends a viewer the session's state. Its run loop owns the
// pacer and what was sent; the reader shares only acks, the latest ping
// and wake-ups.
type writer struct {
	c      Conn
	v      *session.Viewer
	s      *session.Session
	fb     *framebuffer.Framebuffer
	p      *pacer.Pacer
	enc    Encoder
	limits protocol.Limits
	log    *slog.Logger
	start  time.Time

	acks *acks
	poke chan struct{} // holds a token when the reader has news
	mu   sync.Mutex
	ping *protocol.Ping // the latest, not yet answered

	g        grid
	sent     []uint64 // the version of each tile the viewer has
	versions []uint64
	order    []int
	fresh    []int
	ackbuf   []ack
	buf      []byte
	pix      []byte
	payload  []byte
	frame    uint32
	in       *pending // the input whose effect goes first

	// snap holds the versions at snapAt and prev those at least
	// activeWindow before, to find the tiles in motion at an input.
	prev, snap, cur []uint64
	snapAt          time.Time
}

// pending is an input whose effect has not been sent yet. Its effect is
// the change after clock on tiles that were still before it: a tile in
// motion, active, changes after the input anyway, and taking that change
// for the effect would spend the input's priority on the motion.
type pending struct {
	seq     uint32
	clock   uint64
	at      time.Time
	active  []bool
	checked uint64 // the framebuffer's clock when last searched for the effect
	boosted bool   // the pacer has been told
}

// newPending returns the pending input seq, read at clock. A tile was in
// motion if it changed between the last two snapshots, or since the last
// and before the input; a tile that changed since the last snapshot and
// again after the input is taken as still.
func (w *writer) newPending(seq uint32, clock uint64) *pending {
	in := &pending{seq: seq, clock: clock, at: time.Now()}
	w.cur = w.fb.Versions(w.cur[:0])
	if len(w.cur) != len(w.snap) {
		return in
	}
	in.active = make([]bool, len(w.cur))
	before := len(w.prev) == len(w.snap)
	for i, v := range w.cur {
		in.active[i] = v > w.snap[i] && v <= clock || before && w.snap[i] > w.prev[i]
	}
	return in
}

func newWriter(c Conn, v *session.Viewer, p *pacer.Pacer, enc Encoder, l protocol.Limits, log *slog.Logger) *writer {
	s := v.Session()
	fb := s.Framebuffer()
	return &writer{
		c:      c,
		v:      v,
		s:      s,
		fb:     fb,
		p:      p,
		enc:    enc,
		limits: l,
		log:    log,
		start:  time.Now(),
		acks:   newAcks(),
		poke:   make(chan struct{}, 1),
		pix:    make([]byte, 4*fb.TileSize()*fb.TileSize()),
	}
}

// wake tells the run loop that the reader has news.
func (w *writer) wake() {
	select {
	case w.poke <- struct{}{}:
	default:
	}
}

func (w *writer) write(ctx context.Context, m interface{ Append([]byte) []byte }) error {
	w.buf = m.Append(w.buf[:0])
	return w.c.WriteMessage(ctx, w.buf)
}

// run sends frames until the session ends or ctx is done.
func (w *writer) run(ctx context.Context) error {
	for {
		if err := w.small(ctx); err != nil {
			return err
		}
		w.takeAcks()
		if err := w.syncSize(ctx); err != nil {
			return err
		}
		visible, viewport := w.v.Visibility()
		if !visible {
			if err := w.wait(ctx, nil, 0); err != nil {
				return err
			}
			continue
		}
		changed := w.s.Changed()
		fresh, ok := w.collect(viewport)
		if !ok {
			continue // resized meanwhile
		}
		if len(w.order) == 0 {
			if err := w.wait(ctx, changed, 0); err != nil {
				return err
			}
			continue
		}
		if fresh > 0 && !w.in.boosted {
			// The input's effect is on the screen: its frame may go
			// beyond the window, and its bytes unpaced.
			w.p.Input()
			w.in.boosted = true
		}
		ok, retry := w.p.StartFrame(time.Now())
		if !ok {
			// While an input's effect is awaited, a change may be it,
			// and its frame may go beyond the window.
			var effect <-chan struct{}
			if w.in != nil && !w.in.boosted {
				effect = changed
			}
			if err := w.wait(ctx, effect, retry); err != nil {
				return err
			}
			continue
		}
		order, input := w.order, false
		if fresh > 0 {
			order, input = w.order[:fresh], true
			byDamage(order, w.versions)
		}
		if err := w.sendFrame(ctx, order, input); err != nil {
			return err
		}
	}
}

// collect fills w.order with the dirty tiles in the viewport, those that
// show the pending input's effect first, and reports how many those are.
// It reports false if the screen was resized since syncSize.
func (w *writer) collect(viewport image.Rectangle) (fresh int, ok bool) {
	w.versions = w.fb.Versions(w.versions[:0])
	if len(w.versions) != len(w.sent) {
		return 0, false
	}
	now := time.Now()
	switch {
	case len(w.snap) != len(w.versions):
		w.prev = w.prev[:0]
		w.snap, w.snapAt = append(w.snap[:0], w.versions...), now
	case now.Sub(w.snapAt) >= activeWindow:
		w.prev, w.snap = w.snap, w.prev
		w.snap, w.snapAt = append(w.snap[:0], w.versions...), now
	}
	if w.in != nil && !w.in.boosted && now.Sub(w.in.at) > inputTimeout {
		w.resolve()
	}
	var after uint64
	var active []bool
	if w.in != nil {
		after, active = w.in.clock, w.in.active
	}
	w.order, fresh = dirty(w.order, w.g, w.versions, w.sent, viewport, after, active)
	return fresh, true
}

// sendFrame sends tiles as one frame. If input is set, they are the
// tiles that show the pending input's effect, smallest damage first, and
// the frame ends before a drawing operation that would take it past
// inputFrameBytes; the first goes whole however large, since it may be
// the effect itself. Any frame ends early when the pacer caps the bytes
// in flight, when the screen is resized or hidden, or after the tiles of
// an input whose effect appears meanwhile.
func (w *writer) sendFrame(ctx context.Context, order []int, input bool) error {
	w.frame++
	id := w.frame
	readAt := time.Now()
	seq, _ := w.v.LastInput()
	carried := false // tiles showing the pending input's effect went out
	inputBytes := 0  // bytes of the input's tiles in this frame
	tileBytes := 0   // of the last tile
	for i := 0; i < len(order); i++ {
		if err := w.smallNow(ctx); err != nil {
			return err
		}
		if !input {
			// An input that arrived during the frame and whose effect is
			// on the screen: its tiles go next, unpaced, and then the
			// frame ends.
			n, ok := w.effect()
			if !ok {
				break
			}
			if n > 0 {
				order, i, input = w.fresh[:n], 0, true
			}
		}
		if input && inputBytes > 0 && w.versions[order[i]] != w.versions[order[i-1]] &&
			inputBytes+opTiles(order, i, w.versions)*tileBytes > inputFrameBytes {
			break
		}
		if w.fb.Bounds() != w.g.bounds {
			break
		}
		if visible, _ := w.v.Visibility(); !visible {
			break
		}
		t := order[i]
		r, version := w.fb.ReadTile(t, w.pix)
		if r != w.g.rect(t) {
			break // resized since the tile was chosen
		}
		var codec protocol.Codec
		w.payload, codec = w.enc.Encode(w.payload[:0], w.pix, r.Dx(), r.Dy(), w.p.Quality())
		w.buf = protocol.Tile{
			FrameID: id,
			X:       uint16(r.Min.X), // #nosec G115 -- screens are at most 65535 pixels wide
			Y:       uint16(r.Min.Y), // #nosec G115 -- and high
			W:       uint16(r.Dx()),  // #nosec G115 -- a tile is smaller than the screen
			H:       uint16(r.Dy()),  // #nosec G115 -- a tile is smaller than the screen
			Codec:   codec,
			Payload: w.payload,
		}.Append(w.buf[:0])
		wait, more := w.p.Delay(time.Now(), len(w.buf))
		if !more {
			break
		}
		if wait > 0 {
			if err := w.sleep(ctx, wait); err != nil {
				return err
			}
		}
		if err := w.c.WriteMessage(ctx, w.buf); err != nil {
			return err
		}
		w.p.Wrote(time.Now(), len(w.buf))
		w.sent[t] = version
		tileBytes = len(w.buf)
		if input {
			carried = true
			inputBytes += len(w.buf)
		}
	}
	end := protocol.FrameEnd{
		FrameID:    id,
		ServerTime: uint64(readAt.Sub(w.start).Microseconds()), // #nosec G115 -- durations since the start are positive
		LastSeq:    seq,
	}
	if err := w.write(ctx, end); err != nil {
		return err
	}
	w.p.EndFrame(time.Now(), id)
	if carried {
		w.resolve()
	}
	return nil
}

// effect looks for the pending input's effect if the screen changed
// since it last looked, and if it is there, puts its tiles in w.fresh
// smallest damage first, tells the pacer, and reports how many they are.
// It reports false if the screen was resized.
func (w *writer) effect() (n int, ok bool) {
	if w.in == nil || w.in.boosted {
		return 0, true
	}
	clock := w.fb.Clock()
	if clock <= w.in.clock || clock == w.in.checked {
		return 0, true
	}
	w.in.checked = clock
	w.versions = w.fb.Versions(w.versions[:0])
	if len(w.versions) != len(w.sent) {
		return 0, false
	}
	_, viewport := w.v.Visibility()
	w.fresh, n = dirty(w.fresh, w.g, w.versions, w.sent, viewport, w.in.clock, w.in.active)
	if n == 0 {
		return 0, true
	}
	w.p.Input()
	w.in.boosted = true
	byDamage(w.fresh[:n], w.versions)
	return n, true
}

// resolve forgets the pending input once its effect is sent, or given up
// on, and takes up a later input the viewer gave meanwhile.
func (w *writer) resolve() {
	seq, clock := w.v.LastInput()
	if w.in != nil && seq != w.in.seq {
		w.in = w.newPending(seq, clock)
		return
	}
	w.in = nil
}

// syncSize sends resize when the framebuffer's size differs from what
// the viewer has, and forgets what the viewer was sent. It follows the
// framebuffer, not the session's small state, which reports a resize
// only after the framebuffer has taken it.
func (w *writer) syncSize(ctx context.Context) error {
	b := w.fb.Bounds()
	if b == w.g.bounds {
		return nil
	}
	tile := w.fb.TileSize()
	w.g = grid{bounds: b, tile: tile, cols: (b.Dx() + tile - 1) / tile}
	n := w.g.cols * ((b.Dy() + tile - 1) / tile)
	w.sent = append(w.sent[:0], make([]uint64, n)...)
	if b.Empty() {
		return nil
	}
	return w.write(ctx, protocol.Resize{
		Width:  uint16(b.Dx()), // #nosec G115 -- screens are at most 65535 pixels wide
		Height: uint16(b.Dy()), // #nosec G115 -- and high
	})
}

// smallNow sends the small state if any changed, without waiting.
func (w *writer) smallNow(ctx context.Context) error {
	select {
	case <-w.v.Wake():
	case <-w.poke:
	default:
		return nil
	}
	return w.small(ctx)
}

// small sends the small state that changed, which never waits behind
// tiles, and answers the latest ping.
func (w *writer) small(ctx context.Context) error {
	p := w.v.Take()
	if p.Input && w.in == nil {
		w.in = w.newPending(w.v.LastInput())
	}
	if p.Cursor != nil {
		if err := w.cursor(ctx, p.Cursor); err != nil {
			return err
		}
	}
	if p.CursorPos != nil {
		m := protocol.CursorMove{
			X: uint16(max(0, p.CursorPos.X)), // #nosec G115 -- clamped to the screen by the backend
			Y: uint16(max(0, p.CursorPos.Y)), // #nosec G115 -- clamped to the screen by the backend
		}
		if err := w.write(ctx, m); err != nil {
			return err
		}
	}
	if p.Clipboard != nil {
		if err := w.write(ctx, protocol.ServerClipboard{MIME: p.Clipboard.MIME, Data: p.Clipboard.Data}); err != nil {
			return err
		}
	}
	if p.Title != nil {
		if err := w.write(ctx, protocol.Title{Title: *p.Title}); err != nil {
			return err
		}
	}
	for range min(p.Bells, maxBells) {
		if err := w.write(ctx, protocol.Bell{}); err != nil {
			return err
		}
	}
	for _, j := range p.Joined {
		m := protocol.ViewerJoined{ViewerID: uint32(j.ID), Role: role(j.Role)} // #nosec G115 -- viewer IDs count up from 1
		if err := w.write(ctx, m); err != nil {
			return err
		}
	}
	for _, id := range p.Left {
		if err := w.write(ctx, protocol.ViewerLeft{ViewerID: uint32(id)}); err != nil { // #nosec G115 -- viewer IDs count up from 1
			return err
		}
	}
	for _, r := range p.Prompts {
		m := protocol.Prompt{ID: r.ID, Name: r.Prompt.Name, Instruction: r.Prompt.Instruction}
		for _, f := range r.Prompt.Fields {
			m.Fields = append(m.Fields, protocol.PromptField{Label: f.Label, Secret: f.Secret})
		}
		if err := w.write(ctx, m); err != nil {
			return err
		}
	}
	w.mu.Lock()
	ping := w.ping
	w.ping = nil
	w.mu.Unlock()
	if ping != nil {
		if err := w.write(ctx, protocol.Pong{Nonce: ping.Nonce, Time: ping.Time}); err != nil {
			return err
		}
	}
	if p.End != nil {
		if err := w.write(ctx, closeFor(*p.End)); err != nil {
			return err
		}
		return errEnded
	}
	return nil
}

// cursor sends the pointer's image, or an empty one to hide it. An image
// larger than the client decodes is not sent; the client keeps its own.
func (w *writer) cursor(ctx context.Context, c *session.Cursor) error {
	m := protocol.Cursor{
		HotX:  uint16(max(0, c.Hot.X)), // #nosec G115 -- within the cursor image
		HotY:  uint16(max(0, c.Hot.Y)), // #nosec G115 -- within the cursor image
		Codec: protocol.CodecRaw,
	}
	if c.Image != nil {
		b := c.Image.Bounds()
		maxSize := w.limits.MaxTextureSize
		if maxSize <= 0 {
			maxSize = protocol.DefaultMaxTextureSize
		}
		if b.Dx() > maxSize || b.Dy() > maxSize {
			w.log.Debug("cursor too large for the client", "size", b.Size())
			return nil
		}
		m.W, m.H = uint16(b.Dx()), uint16(b.Dy()) // #nosec G115 -- bounded by the texture size
		m.Payload = c.Image.Pix
	}
	return w.write(ctx, m)
}

// takeAcks gives the pacer the acks the reader received.
func (w *writer) takeAcks() {
	w.ackbuf = w.acks.take(w.ackbuf[:0])
	for _, a := range w.ackbuf {
		w.p.Ack(a.at, a.id, a.decode)
	}
}

// wait blocks until there is news: small state, the reader's, an ack, a
// change on changed, or d passing if positive.
func (w *writer) wait(ctx context.Context, changed <-chan struct{}, d time.Duration) error {
	var timer <-chan time.Time
	if d > 0 {
		t := time.NewTimer(d)
		defer t.Stop()
		timer = t.C
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-w.v.Wake():
		return w.small(ctx)
	case <-w.poke:
		return w.small(ctx)
	case <-w.acks.wake:
	case <-changed:
	case <-timer:
	}
	return nil
}

// sleep waits for d while still sending small state as it changes.
func (w *writer) sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-w.v.Wake():
			if err := w.small(ctx); err != nil {
				return err
			}
		case <-w.poke:
			if err := w.small(ctx); err != nil {
				return err
			}
		case <-t.C:
			return nil
		}
	}
}
