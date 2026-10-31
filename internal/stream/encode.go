// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package stream

import (
	"image"
	"time"

	"changkun.de/x/occamy/internal/encoder"
	"changkun.de/x/occamy/protocol"
)

// lookahead is a lossless tile that encode read and encoded while it
// looked for the end of a run of lossy tiles: the next message, unless
// the frame takes another turn first, or the tile changed since.
type lookahead struct {
	ok      bool
	tile    int
	version uint64
	r       image.Rectangle
	codec   protocol.Codec
	payload []byte
	k       int // the buffer in w.tiles holding its pixels
	// key is its content, if keyed: a tile the cache can hold.
	key   cacheKey
	keyed bool
}

// encode reads the tiles of the next message from order[i] on, encodes
// them and puts the message in w.buf, and the tiles' versions in
// w.runVersion. The message is one tile, or a run of tiles that the
// encoder sends lossy and that lie side by side in a row of tiles, in
// order, encoded as one rectangle so that the lossy format's headers are
// paid once. A run holds at most encoder.MaxRun tiles, is at most the
// client's maximum texture size wide, and makes a message within the
// maximum message size.
//
// In an input's frame, a run ends where a drawing operation does, so that
// the frame can end before an operation that would take it past its
// budget.
//
// It reports how many tiles the message holds, and false for send in the
// rare case that the encoder could not fit even one tile in a message the
// client takes: the tile is then taken as sent, so that the writer does
// not try it again until it changes. It reports false for ok if the
// screen was resized since order was made. The message's tiles' pixels
// are in w.tiles from w.msgK on, and the quality the client gets them at
// is w.msgQuality.
//
// A run ends before a tile that a copy-rect of the frame completed, and
// where the tiles' motion changes: tiles in motion go at the pacer's
// lowest quality, the effect of an input at its quality now, and other
// still tiles at the quality their SSIM calls for, up to the pacer's.
//
// With a tile cache, a tile whose content a slot holds goes as a cached
// tile of that slot, and ends a run before it; a tile that goes alone
// and exact is stored in the slot used least recently.
func (w *writer) encode(id uint32, order []int, i int, input bool) (n int, send, ok bool) {
	start := time.Now()
	defer func() { w.encodeTime += time.Since(start) }()

	t := order[i]
	w.msgSlot = 0
	if a := &w.ahead; a.ok {
		a.ok = false
		// The tile it holds, read no earlier than the versions order
		// was made from; else the tile changed since, and is read again.
		if a.tile == t && a.version >= w.versions[t] {
			w.runVersion = append(w.runVersion[:0], a.version)
			w.msgK, w.msgQuality, w.msgGoal = a.k, lossless, lossless
			if w.cached(id, a.r, a.key, a.keyed) {
				return 1, true, true
			}
			w.payload = append(w.payload[:0], a.payload...)
			w.storeIn(a.key, a.keyed, a.codec)
			w.message(id, a.r, a.codec, w.msgSlot)
			return 1, true, true
		}
	}
	r, version := w.fb.ReadTile(t, w.tile(0))
	if r != w.g.rect(t) || w.fb.Bounds() != w.g.bounds {
		return 0, false, false
	}
	w.runVersion = append(w.runVersion[:0], version)
	w.msgK, w.msgQuality, w.msgGoal = 0, lossless, lossless
	key, keyed := w.cache.key(w.tiles[0], r)
	if w.cached(id, r, key, keyed) {
		return 1, true, true
	}
	moving := w.moving(t, input)
	payload, codec, lossy := w.enc.Tile(w.payload[:0], w.tiles[0], r.Dx(), r.Dy(), moving)
	w.payload = payload
	if !lossy {
		w.storeIn(key, keyed, codec)
		w.message(id, r, codec, w.msgSlot)
		return 1, true, true
	}

	// Gather the run: the tiles to the right, while they are next in
	// order, lossy, and moving as the first is.
	w.rects[0] = r
	n = 1
	width := r.Dx()
	maxWidth := w.limits.MaxTextureSize
	if maxWidth <= 0 {
		maxWidth = protocol.DefaultMaxTextureSize
	}
	for i+n < len(order) && n < encoder.MaxRun {
		u := order[i+n]
		ru := w.g.rect(u)
		if u != order[i+n-1]+1 || u/w.g.cols != t/w.g.cols || width+ru.Dx() > maxWidth || w.sent[u] >= w.versions[u] {
			break
		}
		if w.moving(u, input) != moving {
			break // goes at another quality
		}
		got, v := w.fb.ReadTile(u, w.tile(n))
		if got != ru || w.fb.Bounds() != w.g.bounds {
			break // resized: the next message finds out
		}
		if input && v != w.runVersion[n-1] {
			break // another drawing operation
		}
		ku, kuOK := w.cache.key(w.tiles[n], ru)
		if kuOK && w.cache.has(ku) {
			break // goes cached
		}
		b, c, lossy := w.enc.Tile(w.ahead.payload[:0], w.tiles[n], ru.Dx(), ru.Dy(), moving)
		if !lossy {
			w.ahead = lookahead{ok: true, tile: u, version: v, r: ru, codec: c, payload: b, k: n, key: ku, keyed: kuOK}
			break
		}
		w.ahead.payload = b
		w.rects[n] = ru
		w.runVersion = append(w.runVersion, v)
		width += ru.Dx()
		n++
	}

	// Encode it, halving it while the message is too large.
	for {
		rect := w.rects[0].Union(w.rects[n-1])
		w.assemble(n, rect)
		codec = w.lossy(rect, moving, input)
		if codec != 0 {
			w.msgSlot = 0
			if n == 1 {
				w.storeIn(key, keyed, codec)
			}
			w.message(id, rect, codec, w.msgSlot)
			if w.limits.CheckSize(len(w.buf)) == nil {
				return n, true, true
			}
		}
		if n == 1 {
			w.log.Warn("tile not sent: no message the client takes holds it", "rect", rect, "codec", codec, "bytes", len(w.payload))
			return 1, false, true
		}
		// The tile after the run comes next no longer.
		w.ahead.ok = false
		n = (n + 1) / 2
		w.runVersion = w.runVersion[:n]
	}
}

// lossy encodes the run's pixels, assembled in w.runPix as rect, into
// w.payload, and sets the message's quality and goal:
//
//   - tiles in motion at the pacer's lowest quality, since motion loses
//     detail before input loses responsiveness, and refinement raises
//     them once they stop;
//   - an input's effect at the pacer's quality, without the search,
//     which would delay it, and refined like tiles that moved;
//   - other tiles at the lowest quality up to the pacer's whose SSIM
//     reaches the encoder's score, recorded as final; if none does,
//     their goal is above the pacer's quality, for refinement to reach
//     once the link allows.
//
// It returns the codec, 0 if none encodes the pixels.
func (w *writer) lossy(rect image.Rectangle, moving, input bool) protocol.Codec {
	var codec protocol.Codec
	q := w.p.Quality()
	switch {
	case moving || input:
		if moving {
			q = min(q, w.p.MinQuality())
		}
		w.payload, codec = w.enc.Lossy(w.payload[:0], w.runPix, rect.Dx(), rect.Dy(), q)
		w.msgQuality, w.msgGoal = lossyQuality(q), 0
	default:
		var got int
		var reached bool
		w.payload, codec, got, reached = w.enc.Still(w.payload[:0], w.runPix, rect.Dx(), rect.Dy(), w.g.tile, w.p.MinQuality(), q)
		w.msgQuality, w.msgGoal = stillGoal(got, q, reached)
	}
	if exact(codec) {
		w.msgQuality, w.msgGoal = lossless, lossless
	}
	return codec
}

// stillGoal is the quality and goal of pixels that Still encoded at got,
// searching up to hi: final once the score is reached, or else got with
// a goal above hi.
func stillGoal(got, hi int, reached bool) (quality, goal uint8) {
	if reached {
		return final, final
	}
	return lossyQuality(got), lossyQuality(hi + 1)
}

// exact reports whether a codec carries pixels exactly.
func exact(c protocol.Codec) bool {
	return c == protocol.CodecRaw || c == protocol.CodecDeflate || c == protocol.CodecPNG
}

// tile returns the buffer for the pixels of a run's k-th tile.
func (w *writer) tile(k int) []byte {
	if w.tiles[k] == nil {
		w.tiles[k] = make([]byte, 4*w.fb.TileSize()*w.fb.TileSize())
	}
	return w.tiles[k]
}

// assemble puts the pixels of the run's first n tiles, which make up
// rect, side by side in w.runPix, row by row.
func (w *writer) assemble(n int, rect image.Rectangle) {
	stride := 4 * rect.Dx()
	size := stride * rect.Dy()
	if cap(w.runPix) < size {
		w.runPix = make([]byte, size)
	}
	w.runPix = w.runPix[:size]
	for k := range n {
		r := w.rects[k]
		tw := 4 * r.Dx()
		x := 4 * (r.Min.X - rect.Min.X)
		for y := range r.Dy() {
			copy(w.runPix[y*stride+x:y*stride+x+tw], w.tiles[k][y*tw:(y+1)*tw])
		}
	}
}

// cached puts in w.buf a cached tile of the slot that holds key, the
// content of tile r, and reports whether one does.
func (w *writer) cached(id uint32, r image.Rectangle, key cacheKey, keyed bool) bool {
	if !keyed {
		return false
	}
	s, ok := w.cache.lookup(key)
	if !ok {
		return false
	}
	w.payload = w.payload[:0]
	w.message(id, r, protocol.CodecCached, s)
	return true
}

// storeIn makes the next message store its tile, whose content is key,
// in the slot used least recently, if the tile is keyed and its codec
// carries it exactly: a slot holds what the server's screen held.
func (w *writer) storeIn(key cacheKey, keyed bool, codec protocol.Codec) {
	w.msgSlot = 0
	if keyed && exact(codec) {
		w.msgSlot, w.msgKey = w.cache.victim(), key
	}
}

// message puts a tile message of w.payload in w.buf, naming slot: the
// slot the tile is stored in or, cached, drawn from. It sets w.msgFrom
// to the slot drawn from, which drawn marks used once the message is
// written; a message held back leaves nothing for the next one.
func (w *writer) message(id uint32, r image.Rectangle, codec protocol.Codec, slot uint16) {
	w.msgFrom = 0
	if codec == protocol.CodecCached {
		w.msgFrom = slot
	}
	w.buf = protocol.Tile{
		FrameID: id,
		X:       uint16(r.Min.X), // #nosec G115 -- screens are at most 65535 pixels wide
		Y:       uint16(r.Min.Y), // #nosec G115 -- and high
		W:       uint16(r.Dx()),  // #nosec G115 -- a run is at most the texture size wide
		H:       uint16(r.Dy()),  // #nosec G115 -- a tile is smaller than the screen
		Codec:   codec,
		Slot:    slot,
		Payload: w.payload,
	}.Append(w.buf[:0])
}
