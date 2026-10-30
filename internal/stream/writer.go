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

	"changkun.de/x/occamy/internal/encoder"
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
// arrives: a tile, or a terminal's row or cursor, that changed within it
// was in motion, and its next change is not taken for the input's effect.
const activeWindow = 100 * time.Millisecond

// maxBells bounds the bells sent at once; more ring as one burst anyway.
const maxBells = 3

// maxPings bounds the pings waiting for their pong. A client sends one
// at a time; one that floods loses the oldest.
const maxPings = 16

// closeTimeout bounds the wait to send close, since a client that stopped
// reading would otherwise hold the stream open.
const closeTimeout = time.Second

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

	acks  *acks
	poke  chan struct{} // holds a token when the reader has news
	mu    sync.Mutex
	pings []protocol.Ping // not yet answered, oldest first

	g        grid
	sent     []uint64 // the version of each tile the viewer has
	versions []uint64
	order    []int
	fresh    []int
	ackbuf   []ack
	buf      []byte // the tile being sent
	sbuf     []byte // small messages, which go out while a tile waits in buf
	pongs    []protocol.Ping
	frame    uint32
	in       *pending // the input whose effect goes first

	// What encode works in: the pixels of the tiles of a run, the run's
	// pixels side by side, their versions, the payload, a lossless tile
	// encoded while looking for the run's end, and the time spent in the
	// frame being sent.
	tiles      [encoder.MaxRun][]byte
	rects      [encoder.MaxRun]image.Rectangle
	runVersion []uint64
	runPix     []byte
	payload    []byte
	cbuf       []byte // a cursor's payload
	ahead      lookahead
	encodeTime time.Duration

	// snap holds the versions at snapAt and prev those at least
	// activeWindow before, to find the tiles in motion at an input. In a
	// terminal session the versions are those of the screen's rows, then
	// its cursor's.
	prev, snap, cur []uint64
	snapAt          time.Time
	// effects holds the version of each tile when it last went out as an
	// input's effect: a change that was an effect is not motion.
	effects []uint64
}

// pending holds the inputs whose effect has not been sent yet, from the
// input first through seq. Their effect is the change after clock on
// tiles, or a terminal's rows and cursor, that were still before it: a tile in motion, active, changes
// after the input anyway, and taking that change for the effect would
// spend the inputs' priority on the motion. Inputs that come while the
// effect is awaited join it, since the first input's clock precedes
// their effects too; so a key-up, which shows nothing, does not keep the
// next key from priority.
type pending struct {
	first   uint32
	seq     uint32
	clock   uint64
	at      time.Time // when the last input came
	active  []bool
	checked uint64 // the framebuffer's clock when last searched for the effect
	boosted bool   // the pacer has been told, at the clock checked
}

// newPending returns the pending input seq, whose effect is the change
// after clock. A tile was in motion if it changed since the last snapshot
// and before clock, or between the last two, unless the change went out
// as an earlier input's effect. A tile that changed since the last
// snapshot and again after clock is taken as still; a terminal's rows
// are read at clock, so this does not arise for them. The writer takes a
// snapshot at least every activeWindow while it is visible; older
// snapshots, from while it was hidden or held mid-frame, are history and
// show no motion.
func (w *writer) newPending(seq uint32, clock uint64) *pending {
	in := &pending{first: seq, seq: seq, clock: clock, at: time.Now()}
	if w.fb != nil {
		w.cur = w.fb.Versions(w.cur[:0])
	} else {
		// The screen at clock, when known, says exactly which rows
		// changed before the input.
		s := w.v.InputScreen()
		if s == nil || s.Version != clock {
			s = w.s.Screen()
		}
		w.cur = s.Versions(w.cur[:0])
	}
	if len(w.cur) != len(w.snap) || len(w.effects) != len(w.cur) {
		return in
	}
	in.active = make([]bool, len(w.cur))
	before := len(w.prev) == len(w.snap) && time.Since(w.snapAt) < 2*activeWindow
	for i, v := range w.cur {
		e := w.effects[i]
		recent := v > w.snap[i] && v <= clock && v > e
		earlier := before && w.snap[i] > w.prev[i] && w.snap[i] > e
		in.active[i] = recent || earlier
	}
	return in
}

func newWriter(c Conn, v *session.Viewer, p *pacer.Pacer, enc Encoder, l protocol.Limits, log *slog.Logger) *writer {
	s := v.Session()
	w := &writer{
		c:      c,
		v:      v,
		s:      s,
		fb:     s.Framebuffer(),
		p:      p,
		enc:    enc,
		limits: l,
		log:    log,
		start:  time.Now(),
		acks:   newAcks(),
		poke:   make(chan struct{}, 1),
	}
	return w
}

// wake tells the run loop that the reader has news.
func (w *writer) wake() {
	select {
	case w.poke <- struct{}{}:
	default:
	}
}

// message is a message the writer sends.
type message interface {
	Append([]byte) []byte
	Type() protocol.Type
}

// write sends a small message. One larger than the client takes, such as
// a huge remote clipboard, is dropped.
func (w *writer) write(ctx context.Context, m message) error {
	w.sbuf = m.Append(w.sbuf[:0])
	if err := w.limits.CheckSize(len(w.sbuf)); err != nil {
		w.log.Warn("message dropped", "type", m.Type(), "err", err)
		return nil
	}
	return w.c.WriteMessage(ctx, w.sbuf)
}

// run sends frames until the session ends or ctx is done.
func (w *writer) run(ctx context.Context) error {
	if w.fb == nil {
		return w.runTerminal(ctx)
	}
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
			if err := w.wait(ctx, effect, w.untilSnap(retry)); err != nil {
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
	clock := w.fb.Clock()
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
		if !w.in.boosted {
			w.in.checked = clock
		}
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
	tileBytes := 0   // of the last message, a tile's share
	sent, messages := 0, 0
	w.encodeTime = 0
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
				// The tile read ahead belongs to the order left behind.
				w.ahead.ok = false
				// The rest of the frame shows the inputs through the
				// latest.
				seq = max(seq, w.in.seq)
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
		n, send, ok := w.encode(id, order, i, input)
		if !ok {
			break // resized since the tiles were chosen
		}
		if send {
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
			messages++
		}
		for k, t := range order[i : i+n] {
			w.sent[t] = w.runVersion[k]
			if input {
				w.effects[t] = w.runVersion[k]
			}
		}
		if send {
			tileBytes = len(w.buf) / n
			sent += n
			if input {
				carried = true
				inputBytes += len(w.buf)
			}
		}
		i += n - 1
	}
	w.ahead.ok = false
	end := protocol.FrameEnd{
		FrameID:    id,
		ServerTime: uint64(readAt.Sub(w.start).Microseconds()), // #nosec G115 -- durations since the start are positive
		LastSeq:    seq,
	}
	if err := w.write(ctx, end); err != nil {
		return err
	}
	w.p.EndFrame(time.Now(), id)
	w.log.Debug("frame sent", "frame", id, "tiles", sent, "messages", messages, "encode", w.encodeTime)
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
	w.in.seq, _ = w.v.LastInput()
	byDamage(w.fresh[:n], w.versions)
	return n, true
}

// resolve forgets the pending inputs once their effect is sent, or given
// up on. If several inputs were pending, or more came since, the effect
// of a later one may be still to come; it is the change after the clock
// at which the sent effect was found.
func (w *writer) resolve() {
	in := w.in
	w.in = nil
	if in == nil || !in.boosted {
		return
	}
	if seq, _ := w.v.LastInput(); seq != in.first {
		w.in = w.newPending(seq, in.checked)
	}
}

// untilSnap bounds a wait of retry, 0 for none, by when the next
// snapshot of motion is due, so that the snapshots stay fresh while the
// pacer holds frames back.
func (w *writer) untilSnap(retry time.Duration) time.Duration {
	d := max(time.Millisecond, activeWindow-time.Since(w.snapAt))
	if retry > 0 {
		d = min(d, retry)
	}
	return d
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
	w.effects = append(w.effects[:0], make([]uint64, n)...)
	// The tiles of the old size say nothing about motion on the new.
	w.prev, w.snap = w.prev[:0], w.snap[:0]
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
	if p.Input {
		seq, clock := w.v.LastInput()
		switch {
		case w.in == nil:
			w.in = w.newPending(seq, clock)
		case !w.in.boosted:
			w.in.seq, w.in.at = seq, time.Now()
		}
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
	w.pongs = append(w.pongs[:0], w.pings...)
	w.pings = w.pings[:0]
	w.mu.Unlock()
	for _, ping := range w.pongs {
		if err := w.write(ctx, protocol.Pong(ping)); err != nil {
			return err
		}
	}
	if p.End != nil {
		ctx, cancel := context.WithTimeout(ctx, closeTimeout)
		defer cancel()
		if err := w.write(ctx, closeFor(*p.End)); err != nil {
			return err
		}
		return errEnded
	}
	return nil
}

// cursor sends the pointer's image, or hides it: a hidden cursor has no
// hot spot, codec or payload. An image larger than the client decodes,
// or in no codec it decodes, is not sent; the client keeps its own. The
// hot spot is clamped into the image.
func (w *writer) cursor(ctx context.Context, c *session.Cursor) error {
	if w.enc == nil {
		return nil // a terminal session has no pointer
	}
	if c.Image == nil || c.Image.Bounds().Empty() {
		return w.write(ctx, protocol.Cursor{})
	}
	b := c.Image.Bounds()
	maxSize := w.limits.MaxTextureSize
	if maxSize <= 0 {
		maxSize = protocol.DefaultMaxTextureSize
	}
	if b.Dx() > maxSize || b.Dy() > maxSize {
		w.log.Debug("cursor too large for the client", "size", b.Size())
		return nil
	}
	pix := c.Image.Pix
	if c.Image.Stride != 4*b.Dx() {
		pix = make([]byte, 0, 4*b.Dx()*b.Dy())
		for y := b.Min.Y; y < b.Max.Y; y++ {
			i := c.Image.PixOffset(b.Min.X, y)
			pix = append(pix, c.Image.Pix[i:i+4*b.Dx()]...)
		}
	}
	payload, codec, ok := w.enc.Cursor(w.cbuf[:0], pix, b.Dx(), b.Dy())
	w.cbuf = payload
	if !ok {
		w.log.Debug("no codec for the cursor", "size", b.Size())
		return nil
	}
	return w.write(ctx, protocol.Cursor{
		HotX:    uint16(min(max(0, c.Hot.X), b.Dx()-1)), // #nosec G115 -- within the cursor image
		HotY:    uint16(min(max(0, c.Hot.Y), b.Dy()-1)), // #nosec G115 -- within the cursor image
		W:       uint16(b.Dx()),                         // #nosec G115 -- bounded by the texture size
		H:       uint16(b.Dy()),                         // #nosec G115 -- bounded by the texture size
		Codec:   codec,
		Payload: payload,
	})
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
