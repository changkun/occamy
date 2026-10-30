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
// screen was resized since order was made.
func (w *writer) encode(id uint32, order []int, i int, input bool) (n int, send, ok bool) {
	start := time.Now()
	defer func() { w.encodeTime += time.Since(start) }()

	t := order[i]
	if a := &w.ahead; a.ok {
		a.ok = false
		// The tile it holds, read no earlier than the versions order
		// was made from; else the tile changed since, and is read again.
		if a.tile == t && a.version >= w.versions[t] {
			w.payload = append(w.payload[:0], a.payload...)
			w.runVersion = append(w.runVersion[:0], a.version)
			w.message(id, a.r, a.codec)
			return 1, true, true
		}
	}
	r, version := w.fb.ReadTile(t, w.tile(0))
	if r != w.g.rect(t) || w.fb.Bounds() != w.g.bounds {
		return 0, false, false
	}
	w.runVersion = append(w.runVersion[:0], version)
	payload, codec, lossy := w.enc.Tile(w.payload[:0], w.tiles[0], r.Dx(), r.Dy())
	w.payload = payload
	if !lossy {
		w.message(id, r, codec)
		return 1, true, true
	}

	// Gather the run: the tiles to the right, while they are next in
	// order and lossy.
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
		if u != order[i+n-1]+1 || u/w.g.cols != t/w.g.cols || width+ru.Dx() > maxWidth {
			break
		}
		got, v := w.fb.ReadTile(u, w.tile(n))
		if got != ru || w.fb.Bounds() != w.g.bounds {
			break // resized: the next message finds out
		}
		if input && v != w.runVersion[n-1] {
			break // another drawing operation
		}
		b, c, lossy := w.enc.Tile(w.ahead.payload[:0], w.tiles[n], ru.Dx(), ru.Dy())
		if !lossy {
			w.ahead = lookahead{ok: true, tile: u, version: v, r: ru, codec: c, payload: b}
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
		w.payload, codec = w.enc.Lossy(w.payload[:0], w.runPix, rect.Dx(), rect.Dy(), w.p.Quality())
		if codec != 0 {
			w.message(id, rect, codec)
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

// message puts a tile message of w.payload in w.buf.
func (w *writer) message(id uint32, r image.Rectangle, codec protocol.Codec) {
	w.buf = protocol.Tile{
		FrameID: id,
		X:       uint16(r.Min.X), // #nosec G115 -- screens are at most 65535 pixels wide
		Y:       uint16(r.Min.Y), // #nosec G115 -- and high
		W:       uint16(r.Dx()),  // #nosec G115 -- a run is at most the texture size wide
		H:       uint16(r.Dy()),  // #nosec G115 -- a tile is smaller than the screen
		Codec:   codec,
		Payload: w.payload,
	}.Append(w.buf[:0])
}
