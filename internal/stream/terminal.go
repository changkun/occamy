// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package stream

import (
	"context"
	"fmt"
	"time"
	"unicode/utf8"

	"changkun.de/x/occamy/internal/terminal"
	"changkun.de/x/occamy/protocol"
)

// runTerminal sends a terminal session's frames until the session ends
// or ctx is done. A frame is the diff from the screen the viewer last got
// to the current one, so screens that changed while the pacer held a
// frame back are skipped, and bulk output costs what changed on the
// screen. The viewer's first frame is a term-snapshot.
func (w *writer) runTerminal(ctx context.Context) error {
	var last *terminal.Screen // what the viewer has; nil before the first frame
	for {
		if err := w.small(ctx); err != nil {
			return err
		}
		w.takeAcks()
		if visible, _ := w.v.Visibility(); !visible {
			if err := w.wait(ctx, nil, 0); err != nil {
				return err
			}
			continue
		}
		if w.in != nil && !w.in.boosted && time.Since(w.in.at) > inputTimeout {
			w.in = nil // it changed nothing on the screen
		}
		changed := w.s.Changed()
		seq, _ := w.v.LastInput() // before the screen, which then shows it
		s := w.s.Screen()
		w.track(s)
		if s == last {
			if err := w.wait(ctx, changed, 0); err != nil {
				return err
			}
			continue
		}
		if last != nil && last.Equal(s) {
			last = s // redrawn the same: nothing for the viewer
			continue
		}
		w.termEffect(s, seq)
		ok, retry := w.p.StartFrame(time.Now())
		if !ok {
			var effect <-chan struct{}
			if w.in != nil && !w.in.boosted {
				effect = changed
			}
			if err := w.wait(ctx, effect, w.untilSnap(retry)); err != nil {
				return err
			}
			continue
		}
		if err := w.sendScreen(ctx, last, s, seq); err != nil {
			return err
		}
		if w.in != nil && w.in.boosted {
			// What the input changed went out as its effect, so it is not
			// motion when the next input comes.
			for i, v := range w.versions {
				if v > w.in.clock && (w.in.active == nil || !w.in.active[i]) {
					w.effects[i] = v
				}
			}
			w.resolve()
		}
		last = s
	}
}

// track keeps the versions of the screen's rows and cursor, and the
// snapshots of them that tell motion from an input's effect, as collect
// does for tiles.
func (w *writer) track(s *terminal.Screen) {
	w.versions = s.Versions(w.versions[:0])
	now := time.Now()
	switch {
	case len(w.snap) != len(w.versions):
		// The rows of the old size say nothing about motion on the new.
		w.prev = w.prev[:0]
		w.snap, w.snapAt = append(w.snap[:0], w.versions...), now
		w.effects = append(w.effects[:0], make([]uint64, len(w.versions))...)
	case now.Sub(w.snapAt) >= activeWindow:
		w.prev, w.snap = w.snap, w.prev
		w.snap, w.snapAt = append(w.snap[:0], w.versions...), now
	}
}

// termEffect tells the pacer when the pending input's effect is on s: a
// row or the cursor changed after the input that was still before it.
// Output that was already scrolling or redrawing a row is not taken for
// the effect, so the frame that goes beyond the window shows the input.
func (w *writer) termEffect(s *terminal.Screen, seq uint32) {
	in := w.in
	if in == nil || in.boosted || s.Version <= in.clock || s.Version == in.checked {
		return
	}
	in.checked = s.Version
	for i, v := range w.versions {
		if v > in.clock && (in.active == nil || !in.active[i]) {
			w.p.Input()
			in.boosted = true
			in.seq = max(in.seq, seq)
			return
		}
	}
}

// sendScreen sends the frame that turns from into to: resize if the size
// changed, the diff, and frame-end. The diff goes whole, as one message
// unless it is larger than the client takes, since the client's screen is
// only right once it has all of it.
func (w *writer) sendScreen(ctx context.Context, from, to *terminal.Screen, seq uint32) error {
	w.frame++
	id := w.frame
	readAt := time.Now()
	if from == nil || from.Title != to.Title {
		if err := w.write(ctx, protocol.Title{Title: to.Title}); err != nil {
			return err
		}
	}
	if from == nil || from.Cols != to.Cols || from.Rows != to.Rows {
		m := protocol.Resize{
			Width:  uint16(to.Cols), // #nosec G115 -- terminals are at most 65535 cells wide
			Height: uint16(to.Rows), // #nosec G115 -- and high
		}
		if err := w.writeFrame(ctx, m); err != nil {
			return err
		}
	}
	data := terminal.Diff(from, to)
	snapshot := from == nil
	for first := true; first || len(data) > 0; first = false {
		n := w.termChunk(data)
		if n == 0 && len(data) > 0 {
			return fmt.Errorf("stream: a %d-byte message has no room for terminal data", w.limits.MaxMessageSize)
		}
		var m message = protocol.TermData{Data: data[:n]}
		if snapshot && first {
			m = protocol.TermSnapshot{Data: data[:n]}
		}
		if err := w.writeFrame(ctx, m); err != nil {
			return err
		}
		data = data[n:]
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
	return nil
}

// termHeader is the size of a term-data message without its data.
var termHeader = len(protocol.TermData{}.Append(nil))

// termChunk reports how much of data fits one message the client takes,
// ending at a character boundary.
func (w *writer) termChunk(data []byte) int {
	limit := w.limits.MaxMessageSize
	if limit <= 0 {
		limit = protocol.DefaultMaxMessageSize
	}
	n := max(0, min(len(data), limit-termHeader))
	for n < len(data) && n > 0 && !utf8.RuneStart(data[n]) {
		n--
	}
	return n
}

// writeFrame sends a message of the open frame at the pace the pacer
// allows, while still sending small state as it changes. Unlike tiles, a
// terminal frame is never cut short: at the cap on bytes in flight it
// waits for acknowledgements of earlier frames.
func (w *writer) writeFrame(ctx context.Context, m message) error {
	w.buf = m.Append(w.buf[:0])
	for w.p.Hold(len(w.buf)) {
		if err := w.wait(ctx, nil, 0); err != nil {
			return err
		}
		w.takeAcks()
	}
	if wait := w.p.Pace(time.Now()); wait > 0 {
		if err := w.sleep(ctx, wait); err != nil {
			return err
		}
	}
	if err := w.c.WriteMessage(ctx, w.buf); err != nil {
		return err
	}
	w.p.Wrote(time.Now(), len(w.buf))
	return nil
}
