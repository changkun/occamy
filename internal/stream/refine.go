// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package stream

import (
	"context"
	"image"
	"math"
	"time"

	"changkun.de/x/occamy/protocol"
)

// Refinement (specs/008-native-architecture/graphics-pipeline.md,
// Implementation notes: Refinement): content picks a tile's codec and
// motion its quality. A tile in motion goes at a low quality, and one of
// 17 to 256 colours goes lossy while it moves; once it has been still
// for the refine delay, it is sent again at its final quality: exact if
// it has up to 256 colours, and at the quality its SSIM calls for if it
// has more. A tile that is still when it changes goes at its final
// quality at once, unless the viewer's link holds the quality down, in
// which case refinement raises it once the link allows.
//
// Refinement is background work. It goes in frames of its own, only when
// no tile is dirty, no input awaits its effect and no other frame awaits
// an ack, a little at a time (pacer.Background), and each of its messages
// gives way to an input or a change on the screen, so that an input's
// frame never waits behind more than one of its tiles.

// motionHeat is the heat at which a tile is in motion. A tile's heat
// rises by one for each snapshot of the versions, at least activeWindow
// apart, in which it changed, and halves every refine delay the writer
// spends between frames: a tile that changes more often than the refine
// delay keeps above it, and one that changes in three snapshots in a row
// reaches it while they are less than about 0.7 refine delays apart. A
// tile that changes less often is not in motion and goes at its final
// quality, since refinement would raise it between its changes anyway.
// The time the writer spends sending a frame does not count: it takes no
// snapshot meanwhile, so a tile that changes in every snapshot is in
// motion however long the frames take.
const motionHeat = 2

// halfLife is the time in which heat halves: the refine delay, so that
// what is in motion and what is refined between its changes do not
// overlap, but no shorter than minHalfLife.
func (w *writer) halfLife() time.Duration { return max(w.refineDelay, minHalfLife) }

// minHalfLife is the shortest half-life at which content that changes in
// every snapshot, at least activeWindow apart, is in motion after three:
// 1 + f + f² reaches motionHeat for f = 2^(-activeWindow/halfLife) from
// a half-life of 144 ms. A refine delay below it makes content that
// changes between the refine delay and about 0.7 half-lives apart both
// in motion and refined between its changes.
const minHalfLife = 150 * time.Millisecond

// busy marks the start of a frame: the time since the last snapshot or
// frame counts toward the heat's cooling at the next snapshot, and the
// frame's own time does not.
func (w *writer) busy() {
	if !w.heatAt.IsZero() {
		w.heatIdle += time.Since(w.heatAt)
	}
}

// rested marks the end of a frame: the heat cools only from now, since
// the writer took no snapshot while it sent the frame.
func (w *writer) rested() {
	if !w.heatAt.IsZero() {
		w.heatAt = time.Now()
	}
}

// warm updates every tile's heat at now, from the versions of the last
// snapshot, w.snap, to the current ones, w.versions, after cooling it by
// the time the writer spent between frames since the last snapshot. A
// change that went out as an input's effect is not motion.
func (w *writer) warm(now time.Time) {
	if len(w.heat) != len(w.versions) || len(w.snap) != len(w.versions) {
		return
	}
	f := float32(1)
	if !w.heatAt.IsZero() {
		idle := w.heatIdle + now.Sub(w.heatAt)
		f = float32(math.Exp2(-idle.Seconds() / w.halfLife().Seconds()))
	}
	w.heatAt, w.heatIdle = now, 0
	for t, v := range w.versions {
		h := w.heat[t] * f
		if v > w.snap[t] && v != w.effects[t] {
			h++
		}
		w.heat[t] = h
	}
}

// moving reports whether tile t is in motion. An input's effect never
// is, and no tile is without refinement, which would never raise it.
func (w *writer) moving(t int, input bool) bool {
	return !input && w.refineDelay >= 0 && t < len(w.heat) && w.heat[t] >= motionHeat
}

// quality is the quality of tile t's pixels on the client: lossless, or
// the lowest lossy quality its rows were drawn at.
func (w *writer) quality(t int) uint8 {
	if w.sc != nil {
		return w.sc.qual[t]
	}
	return w.qual[t]
}

// drawn records that the message in w.buf, of tiles whose pixels are in
// w.tiles from w.msgK on, was written: the client has them at quality q,
// to be refined to goal.
func (w *writer) drawn(tiles []int, q, goal uint8) {
	for k, t := range tiles {
		if w.sc != nil {
			// The shadow takes the buffers the pixels were read into,
			// and gives back its old ones.
			w.tiles[w.msgK+k] = w.sc.keep(t, w.tiles[w.msgK+k], q)
		} else {
			w.qual[t] = q
		}
		w.goal[t] = goal
	}
}

// refines reports whether tile t is to be refined at quality q, the
// pacer's now: the client has it below its goal, which is lossless, or a
// lossy quality that q allows, or final, or not yet known.
func (w *writer) refines(t int, q uint8) bool {
	have, goal := w.quality(t), w.goal[t]
	switch {
	case have == lossless:
		return false
	case goal == 0:
		return true
	}
	return have < goal && (goal >= final || goal <= q)
}

// candidates puts in w.refine the tiles in viewport to refine now: those
// the client has, unchanged since, still for the refine delay, and below
// their goal. It returns how long until the next tile to refine has been
// still for the delay, 0 if none is waiting.
func (w *writer) candidates(viewport image.Rectangle, now time.Time) (wake time.Duration) {
	w.refine = w.refine[:0]
	if w.refineDelay < 0 || len(w.versions) != len(w.sent) {
		return 0
	}
	since := now.Sub(w.start)
	q := lossyQuality(w.p.Quality())
	for t, v := range w.versions {
		// Versions start at 1, so a tile never sent is below its version.
		if w.sent[t] < v || !w.refines(t, q) {
			continue
		}
		if !viewport.Empty() && !w.g.rect(t).Overlaps(viewport) {
			continue
		}
		if left := w.changed[t] + w.refineDelay - since; left > 0 {
			if wake == 0 || left < wake {
				wake = left
			}
			continue
		}
		w.refine = append(w.refine, t)
	}
	return wake
}

// refinement sends a frame of refinement if there is any to send now. It
// reports -1 if it sent one, and otherwise how long to wait for the next
// tile to be still long enough, 0 for no limit: an ack, a change or an
// input ends the wait too.
func (w *writer) refinement(ctx context.Context, viewport image.Rectangle) (time.Duration, error) {
	if w.in != nil {
		// Its effect may come yet. An input that shows nothing is given
		// up after inputTimeout, which collect finds once the writer
		// wakes; one whose effect showed goes with its frame.
		if w.in.boosted {
			return 0, nil
		}
		return max(time.Until(w.in.at.Add(inputTimeout))+time.Millisecond, time.Millisecond), nil
	}
	now := time.Now()
	wake := w.candidates(viewport, now)
	if len(w.refine) == 0 {
		return wake, nil
	}
	if frames, _ := w.p.InFlight(); frames > 0 {
		return wake, nil // the ack wakes the writer
	}
	ok, retry := w.p.StartFrame(now)
	if !ok {
		return max(retry, time.Millisecond), nil
	}
	return -1, w.refineFrame(ctx)
}

// refineFrame sends a frame of the tiles in w.refine, one tile a message,
// for as long as pacer.Background allows. It ends before a tile once an
// input is pending, and once the screen changed after it tried one,
// whether or not that one went, so that the input's frame, or the
// change's, goes next. The message Background turned away is kept for
// the next frame of refinement.
func (w *writer) refineFrame(ctx context.Context) error {
	w.busy()
	defer w.rested()
	w.frame++
	id := w.frame
	readAt := time.Now()
	clock := w.fb.Clock()
	w.encodeTime = 0
	messages := 0
	for i, t := range w.refine {
		if err := w.smallNow(ctx); err != nil {
			return err
		}
		if w.in != nil || i > 0 && w.fb.Clock() != clock {
			break
		}
		if visible, _ := w.v.Visibility(); !visible || w.fb.Bounds() != w.g.bounds {
			break
		}
		send, ok := w.refineTile(id, t)
		if !ok {
			break // resized
		}
		if !send {
			continue
		}
		if !w.p.Background(time.Now(), len(w.buf)) {
			w.held.ok = true
			w.held.payload = append(w.held.payload[:0], w.payload...)
			w.held.quality, w.held.goal = w.msgQuality, w.msgGoal
			break
		}
		if err := w.c.WriteMessage(ctx, w.buf); err != nil {
			return err
		}
		w.p.Wrote(time.Now(), len(w.buf))
		messages++
		w.drawn(w.refine[i:i+1], w.msgQuality, w.msgGoal)
	}
	seq, _ := w.v.LastInput()
	end := protocol.FrameEnd{
		FrameID:    id,
		ServerTime: uint64(readAt.Sub(w.start).Microseconds()), // #nosec G115 -- durations since the start are positive
		LastSeq:    seq,
	}
	if err := w.write(ctx, end); err != nil {
		return err
	}
	w.p.EndFrame(time.Now(), id)
	w.log.Debug("refinement sent", "frame", id, "tiles", messages, "of", len(w.refine), "encode", w.encodeTime)
	return nil
}

// refineTile reads tile t and puts its refinement in w.buf: lossless if
// it has up to 256 colours, or at the quality its SSIM calls for up to
// the pacer's. It reports false for send if the client has it at that
// quality already, or the tile changed since it was sent, which a frame
// sends instead, or no message the client takes holds it; and false for
// ok if the screen was resized.
func (w *writer) refineTile(id uint32, t int) (send, ok bool) {
	start := time.Now()
	defer func() { w.encodeTime += time.Since(start) }()

	r, version := w.fb.ReadTile(t, w.tile(0))
	if r != w.g.rect(t) || w.fb.Bounds() != w.g.bounds {
		return false, false
	}
	if version != w.sent[t] {
		return false, true
	}
	w.runVersion = append(w.runVersion[:0], version)
	w.msgK = 0
	q, lo := w.p.Quality(), w.p.MinQuality()
	if h := &w.held; h.ok {
		// Turned away last time, as encoded then.
		h.ok = false
		if h.tile == t && h.version == version && h.q == q && h.lo == lo {
			w.payload = append(w.payload[:0], h.payload...)
			w.msgQuality, w.msgGoal = h.quality, h.goal
			w.message(id, r, h.codec)
			return true, true
		}
	}
	b, codec, lossy := w.enc.Tile(w.payload[:0], w.tiles[0], r.Dx(), r.Dy(), false)
	w.msgQuality, w.msgGoal = lossless, lossless
	have := w.quality(t)
	if lossy {
		var got int
		var reached bool
		b, codec, got, reached = w.enc.Still(w.payload[:0], w.tiles[0], r.Dx(), r.Dy(), r.Dx(), lo, q)
		if !exact(codec) {
			w.msgQuality, w.msgGoal = stillGoal(got, q, reached)
			if reached && have >= lossyQuality(got) {
				// The client has the pixels at a quality no lower than
				// the score needs, as an input's effect goes: they are
				// final as they are.
				w.setQuality(t, final)
				w.goal[t] = final
				return false, true
			}
		}
	}
	w.payload = b
	if codec == 0 || w.msgQuality <= have {
		// Nothing better to send: the goal is above the pacer's quality
		// now, and above what the client has, so that the tile is
		// refined once the pacer allows more.
		w.goal[t] = max(w.msgGoal, min(have+1, final-1))
		if codec == 0 {
			w.goal[t] = have
		}
		return false, true
	}
	w.message(id, r, codec)
	if w.limits.CheckSize(len(w.buf)) != nil {
		w.goal[t] = have
		return false, true
	}
	w.held = heldTile{tile: t, version: version, q: q, lo: lo, codec: codec, payload: w.held.payload}
	return true, true
}

// heldTile is the refinement of a tile that pacer.Background turned
// away, which the next frame of refinement sends unless the tile or the
// pacer's quality changed since: the tile, its version, the qualities it
// was searched between, and the message's codec, payload, quality and
// goal. ok says it holds one.
type heldTile struct {
	ok            bool
	tile          int
	version       uint64
	q, lo         int
	codec         protocol.Codec
	payload       []byte
	quality, goal uint8
}

// setQuality records that the client has tile t at quality q.
func (w *writer) setQuality(t int, q uint8) {
	if w.sc != nil {
		w.sc.qual[t] = q
	} else {
		w.qual[t] = q
	}
}
