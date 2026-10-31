// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package rdp

import (
	"fmt"
	"image"
)

// RemoteFX Progressive ([MS-RDPEGFX] 2.2.4.2, 3.3.8.2), the codec of
// RDPGFX_WIRE_TO_SURFACE_PDU_2 and what xrdp draws a session in: tiles
// of RemoteFX (rfx.go) that a surface's codec context keeps, so that a
// tile may come as the difference from what the client has, and in
// passes, a first one of fewer bits and upgrades that add more.
//
// A surface keeps each tile's coefficients as the specification's
// DecDwtQ (3.3.8.2.1.1), quantized, before the shift of their
// quantization factor, in 16 bits: its sub-band diffing tile context,
// which 3.3.1.3 says MUST be kept until the surface is deleted, so that
// a difference tile adds to them whatever codec context it comes in.
// The first pass sums the LL3 band's differences even for a difference
// tile, as 3.3.8.2.1.1 says and its example in 4.1.2.2.3 does.
//
// While a tile has upgrades to come, it keeps its progressive tile
// context too (3.3.1.2): the sign of each coefficient, which decides
// how an upgrade codes it, and the bit positions of its last pass.
// These go with the codec context of the tile's first pass. A surface
// has one codec context at a time: one of another id ends it, as a
// delete does, and an upgrade after that, of a tile whose first pass
// was in it, has no first pass.
//
// At the end of each region, the tiles that it changed are drawn
// where its rectangles cover them. The specification lets a region's
// rectangles be covered by the tiles of an earlier region of the frame
// too; those are not drawn again for them.

// RFX_PROGRESSIVE block types ([MS-RDPEGFX] 2.2.4.2.1).
const (
	progSync        = 0xccc0
	progFrameBegin  = 0xccc1
	progFrameEnd    = 0xccc2
	progContextType = 0xccc3
	progRegion      = 0xccc4
	progTileSimple  = 0xccc5
	progTileFirst   = 0xccc6
	progTileUpgrade = 0xccc7
	// The headers of the region and of the tiles, with the block's.
	progRegionHeader  = 18
	progSimpleHeader  = 22
	progFirstHeader   = 23
	progUpgradeHeader = 26
	// progFull is the quality of a pass that sends all the bits.
	progFull = 0xff
	// The tile and region flags.
	progDifference  = 0x01
	progExtrapolate = 0x01
)

// progState is a surface's RemoteFX Progressive state: its codec
// context, if it has one, and its tiles, in rows of cols.
type progState struct {
	ctx     uint32 // the codec context's id, if live
	live    bool   // the surface has a codec context
	inFrame bool   // a RFX_PROGRESSIVE_FRAME_BEGIN without its end, in the codec context
	// gen counts the codec contexts that have ended: a tile whose
	// first pass was in the live one has its gen.
	gen    uint32
	region uint32 // the regions applied
	cols   int
	tiles  []*progTile
	n      int // the tiles made
	signed int // the tiles that keep signs
}

// progTile is a tile's state ([MS-RDPEGFX] 3.3.8.2.1).
type progTile struct {
	coef   [3][4096]int16 // DecDwtQ of Y, Cb and Cr
	quant  [3]rfxQuant    // the quantization factors of the last pass
	extra  bool           // the reduce-extrapolate transform of the last pass
	at     image.Point    // its origin on the surface
	region uint32         // the last region that changed it
	// The progressive tile context, of the codec context of the first
	// pass, gen: the sign of each coefficient but LL3's while upgrades
	// are to come, and the bit positions of the last pass.
	gen    uint32
	sign   *[3][4096]int8
	bitPos [3]rfxQuant
}

// progTileLimit bounds the tiles that the surfaces keep, 24 KiB each
// and 12 KiB more while they keep signs: twice the desktop's tiles, at
// least 1024.
func progTileLimit(w, h int) int {
	return max(1024, 2*((w+rfxTile-1)/rfxTile)*((h+rfxTile-1)/rfxTile))
}

// progHighWater returns the tile limit: progTileLimit of the largest
// desktop that the pipeline has had, as the surfaces and their tiles
// outlast a resize. It is raised before each resize, and here.
func (c *Client) progHighWater() int {
	g := &c.gfx
	g.tileLimit = max(g.tileLimit, progTileLimit(c.Size()))
	return g.tileLimit
}

// progDrop lets the tiles of s go, with the surface.
func (c *Client) progDrop(s *surface) {
	if s.prog != nil {
		c.gfx.progTiles -= s.prog.n
		s.prog = nil
	}
}

// progEnd ends the codec context of st: its tiles keep their
// coefficients, and give their signs back. Looking for them is charged
// a pixel a tile.
func (c *Client) progEnd(st *progState) error {
	st.live, st.inFrame = false, false
	st.gen++
	if st.signed == 0 {
		return nil
	}
	if err := c.pace(len(st.tiles)); err != nil {
		return err
	}
	for _, t := range st.tiles {
		if t != nil {
			c.progRelease(st, t)
		}
	}
	return nil
}

// gfxWireToSurface2 applies RDPGFX_WIRE_TO_SURFACE_PDU_2: a RemoteFX
// Progressive bitmap stream in the surface's codec context.
func (c *Client) gfxWireToSurface2(r *reader) error {
	id, codec, ctx := r.u16(), int(r.u16()), r.u32()
	r.u8() // pixelFormat
	n := r.u32()
	if r.err != nil {
		return r.err
	}
	if int64(n) > int64(r.len()) {
		return fmt.Errorf("bitmap of %d bytes in %d", n, r.len())
	}
	data := r.take(int(n))
	s, err := c.surface(id)
	if err != nil {
		return err
	}
	if codec != gfxCodecProgressive {
		c.gfxSkipped("GFX bitmaps of unknown codecs")
		return nil
	}
	st := s.prog
	if st == nil {
		cols := (s.w + rfxTile - 1) / rfxTile
		st = &progState{cols: cols, tiles: make([]*progTile, cols*((s.h+rfxTile-1)/rfxTile))}
		s.prog = st
	}
	if st.live && st.ctx != ctx {
		if err := c.progEnd(st); err != nil {
			return err
		}
	}
	st.ctx, st.live = ctx, true
	return c.progStream(s, data)
}

// gfxDeleteContext applies RDPGFX_DELETE_ENCODING_CONTEXT_PDU. A
// context that the client does not have, or a surface, is ignored.
func (c *Client) gfxDeleteContext(r *reader) error {
	id, ctx := r.u16(), r.u32()
	if r.err != nil {
		return r.err
	}
	s := c.gfx.surfaces[id]
	if s == nil || s.prog == nil || !s.prog.live || s.prog.ctx != ctx {
		c.count(func(s *Stats) { s.Ignored["GFX deletes of no codec context"]++ })
		return nil
	}
	return c.progEnd(s.prog)
}

// progStream applies an RFX_PROGRESSIVE_BITMAP_STREAM's blocks to s.
// Blocks of types the client does not use are skipped; a region outside
// a frame is ignored ([MS-RDPEGFX] 2.2.4.2.1.5).
func (c *Client) progStream(s *surface, data []byte) error {
	ctx := s.prog
	for len(data) > 0 {
		r := reader{b: data}
		typ, n := r.u16(), r.u32()
		if r.err != nil || n < 6 || int64(n) > int64(len(data)) {
			return fmt.Errorf("progressive block of %d bytes in %d", n, len(data))
		}
		body := data[6:n]
		data = data[n:]
		switch typ {
		case progFrameBegin:
			ctx.inFrame = true
		case progFrameEnd:
			ctx.inFrame = false
		case progRegion:
			if !ctx.inFrame {
				c.count(func(s *Stats) { s.Ignored["GFX progressive regions outside a frame"]++ })
				continue
			}
			if err := c.progRegion(s, body); err != nil {
				return err
			}
		}
	}
	return nil
}

// progRegion applies RFX_PROGRESSIVE_REGION: its tiles, then each tile
// it changed drawn where its rectangles cover it.
func (c *Client) progRegion(s *surface, b []byte) error {
	r := reader{b: b}
	tileSize, numRects := r.u8(), int(r.u16())
	numQuant, numProg, flags := int(r.u8()), int(r.u8()), r.u8()
	numTiles, size := int(r.u16()), r.u32()
	if r.err != nil {
		return r.err
	}
	if tileSize != rfxTile {
		return fmt.Errorf("progressive tiles of %d pixels", tileSize)
	}
	if r.len() < 8*numRects+5*numQuant+16*numProg {
		return fmt.Errorf("progressive region of %d rectangles and %d and %d quantization tables in %d bytes", numRects, numQuant, numProg, r.len())
	}
	g := &c.gfx
	clips := g.clips[:0]
	for range numRects {
		x, y, w, h := int(r.u16()), int(r.u16()), int(r.u16()), int(r.u16())
		if rc := image.Rect(x, y, x+w, y+h).Intersect(s.bounds()); !rc.Empty() {
			clips = append(clips, rc)
		}
	}
	g.clips = clips
	quants := g.quants[:0]
	for range numQuant {
		quants = append(quants, parseQuant(r.take(5), &progQuantOrder))
	}
	g.quants = quants
	progs := g.progQuants[:0]
	for range numProg {
		r.u8() // quality
		var q [3]rfxQuant
		for i := range q {
			q[i] = parseQuant(r.take(5), &progQuantOrder)
		}
		progs = append(progs, q)
	}
	g.progQuants = progs
	if int64(size) > int64(r.len()) {
		return fmt.Errorf("progressive tiles of %d bytes in %d", size, r.len())
	}
	data := r.take(int(size))
	st := s.prog
	st.region++
	changed := g.changed[:0]
	defer func() {
		clear(changed)
		g.changed = changed[:0]
	}()
	reg := progRegionData{extra: flags&progExtrapolate != 0, quants: quants, progs: progs}
	for range numTiles {
		if len(data) == 0 {
			break
		}
		t := reader{b: data}
		typ, n := t.u16(), t.u32()
		if t.err != nil || n < 6 || int64(n) > int64(len(data)) {
			return fmt.Errorf("progressive tile of %d bytes in %d", n, len(data))
		}
		block := data[:n]
		data = data[n:]
		if err := c.pace(rfxTileCost); err != nil {
			return err
		}
		var tile *progTile
		var err error
		switch typ {
		case progTileSimple, progTileFirst:
			tile, err = c.progFirst(st, &reg, block, typ == progTileFirst)
		case progTileUpgrade:
			tile, err = c.progUpgrade(st, &reg, block)
		default:
			c.count(func(s *Stats) { s.Ignored["GFX progressive blocks"]++ })
			continue
		}
		if err != nil {
			return err
		}
		if tile.region != st.region {
			tile.region = st.region
			changed = append(changed, tile)
		}
	}
	d := c.gfxDecoder()
	for _, tile := range changed {
		p := tile.at
		if err := c.pace(clipCost(p, clips)); err != nil {
			return err
		}
		d.planesOf(&tile.coef, &tile.quant, tile.extra)
		d.draw(s, p, clips)
	}
	return nil
}

// progRegionData is what a region's tiles take from it.
type progRegionData struct {
	extra  bool
	quants []rfxQuant
	progs  [][3]rfxQuant
}

// quant returns the region's quantization tables of the indices.
func (reg *progRegionData) quant(y, cb, cr int) ([3]rfxQuant, error) {
	if n := len(reg.quants); y >= n || cb >= n || cr >= n {
		return [3]rfxQuant{}, fmt.Errorf("progressive tile of quantization tables %d, %d and %d of %d", y, cb, cr, n)
	}
	return [3]rfxQuant{reg.quants[y], reg.quants[cb], reg.quants[cr]}, nil
}

// bitPos returns the bit positions of the pass of quality q: zeros for
// a full pass.
func (reg *progRegionData) bitPos(q int) ([3]rfxQuant, error) {
	if q == progFull {
		return [3]rfxQuant{}, nil
	}
	if q >= len(reg.progs) {
		return [3]rfxQuant{}, fmt.Errorf("progressive tile of quality %d of %d", q, len(reg.progs))
	}
	return reg.progs[q], nil
}

// progTileAt returns the surface's tile at x, y, made if it is not yet
// and create is set, the tiles within the limit, and whether it was
// made.
func (c *Client) progTileAt(st *progState, x, y int, create bool) (*progTile, bool, error) {
	if x >= st.cols || y*st.cols+x >= len(st.tiles) {
		return nil, false, fmt.Errorf("progressive tile %d,%d outside its surface of %d×%d tiles", x, y, st.cols, len(st.tiles)/st.cols)
	}
	t := st.tiles[y*st.cols+x]
	if t != nil || !create {
		return t, false, nil
	}
	if limit := c.progHighWater(); c.gfx.progTiles >= limit {
		return nil, false, fmt.Errorf("more than %d progressive tiles", limit)
	}
	t = &progTile{at: image.Pt(x*rfxTile, y*rfxTile)}
	st.tiles[y*st.cols+x] = t
	st.n++
	c.gfx.progTiles++
	return t, true, nil
}

// progFirst applies RFX_PROGRESSIVE_TILE_SIMPLE or, with first,
// RFX_PROGRESSIVE_TILE_FIRST: a tile's first pass ([MS-RDPEGFX]
// 3.3.8.2.1.1), or its only one.
func (c *Client) progFirst(st *progState, reg *progRegionData, b []byte, first bool) (*progTile, error) {
	header := progSimpleHeader
	if first {
		header = progFirstHeader
	}
	if len(b) < header {
		return nil, fmt.Errorf("progressive tile of %d bytes", len(b))
	}
	r := reader{b: b[6:]}
	qy, qcb, qcr := int(r.u8()), int(r.u8()), int(r.u8())
	x, y := int(r.u16()), int(r.u16())
	flags := r.u8()
	quality := progFull
	if first {
		quality = int(r.u8())
	}
	var lens [4]int
	for i := range lens {
		lens[i] = int(r.u16())
	}
	body := b[header:]
	if lens[0]+lens[1]+lens[2]+lens[3] > len(body) {
		return nil, fmt.Errorf("progressive tile of %d, %d, %d and %d bytes in %d", lens[0], lens[1], lens[2], lens[3], len(body))
	}
	quant, err := reg.quant(qy, qcb, qcr)
	if err != nil {
		return nil, err
	}
	pos, err := reg.bitPos(quality)
	if err != nil {
		return nil, err
	}
	t, made, err := c.progTileAt(st, x, y, true)
	if err != nil {
		return nil, err
	}
	diff := flags&progDifference != 0
	if diff && !made && t.extra != reg.extra {
		// Its coefficients lie in the other transform's bands.
		return nil, fmt.Errorf("progressive difference tile %d,%d in another transform than the tile's", x, y)
	}
	t.gen = st.gen
	if quality == progFull {
		c.progRelease(st, t)
	} else if t.sign == nil {
		t.sign = c.progSign(st)
	}
	d := c.gfxDecoder()
	layout := layoutOf(reg.extra)
	for i := range 3 {
		v := &d.coef[i]
		if err := rlgrDecode(rlgr1, body[:lens[i]], v[:]); err != nil {
			return nil, err
		}
		body = body[lens[i]:]
		ll := layout[bandLL3]
		undelta(v[ll.off : ll.off+ll.w*ll.h])
		progFirstPass(&t.coef[i], v, signOf(t.sign, i), &pos[i], layout, diff)
	}
	t.bitPos, t.quant, t.extra = pos, quant, reg.extra
	n := 0
	if diff {
		n = 1
	}
	c.count(func(s *Stats) {
		s.Bitmaps["gfx progressive"]++
		s.Bitmaps["gfx progressive difference"] += n
	})
	return t, nil
}

// progFirstPass sets a component's DecDwtQ from a first pass's values,
// v, shifted by their bands' bit positions, or adds them to it for a
// difference tile, and records their signs but LL3's, if sign is not
// nil.
func progFirstPass(coef *[4096]int16, v *[4096]int16, sign *[4096]int8, pos *rfxQuant, layout *[rfxBands]rfxBand, diff bool) {
	for b, band := range layout {
		sh := uint(pos[b])
		lo, hi := band.off, band.off+band.w*band.h
		for i := lo; i < hi; i++ {
			x := int32(v[i]) << sh
			if diff {
				x += int32(coef[i])
			}
			coef[i] = sat16(x)
		}
		if sign == nil || b == bandLL3 {
			continue
		}
		for i := lo; i < hi; i++ {
			sign[i] = sgn(int32(v[i]))
		}
	}
}

// progUpgrade applies RFX_PROGRESSIVE_TILE_UPGRADE ([MS-RDPEGFX]
// 3.3.8.2.1.2) to a tile that a first pass made.
func (c *Client) progUpgrade(st *progState, reg *progRegionData, b []byte) (*progTile, error) {
	if len(b) < progUpgradeHeader {
		return nil, fmt.Errorf("progressive upgrade of %d bytes", len(b))
	}
	r := reader{b: b[6:]}
	qy, qcb, qcr := int(r.u8()), int(r.u8()), int(r.u8())
	x, y := int(r.u16()), int(r.u16())
	quality := int(r.u8())
	var lens [6]int // the SRL and the raw bits of Y, Cb and Cr
	total := 0
	for i := range lens {
		lens[i] = int(r.u16())
		total += lens[i]
	}
	body := b[progUpgradeHeader:]
	if total > len(body) {
		return nil, fmt.Errorf("progressive upgrade of %d bytes in %d", total, len(body))
	}
	quant, err := reg.quant(qy, qcb, qcr)
	if err != nil {
		return nil, err
	}
	pos, err := reg.bitPos(quality)
	if err != nil {
		return nil, err
	}
	t, _, err := c.progTileAt(st, x, y, false)
	if err != nil {
		return nil, err
	}
	if t == nil || t.gen != st.gen {
		return nil, fmt.Errorf("progressive upgrade of tile %d,%d, which has had no first pass in its codec context", x, y)
	}
	if t.extra != reg.extra {
		// The first pass set the signs of the other transform's bands.
		return nil, fmt.Errorf("progressive upgrade of tile %d,%d in another transform than its first pass", x, y)
	}
	if t.sign == nil {
		// A tile whose last pass was full has no bits to upgrade, so its
		// signs are not read; they are cleared all the same.
		t.sign = c.progSign(st)
		*t.sign = [3][4096]int8{}
	}
	layout := layoutOf(reg.extra)
	for i := range 3 {
		srlData := body[:lens[2*i]]
		raw := body[lens[2*i]:][:lens[2*i+1]]
		body = body[lens[2*i]+lens[2*i+1]:]
		if err := progUpgradePass(&t.coef[i], &t.sign[i], &t.bitPos[i], &pos[i], layout, srlData, raw); err != nil {
			return nil, err
		}
	}
	t.bitPos, t.quant, t.extra = pos, quant, reg.extra
	if quality == progFull {
		c.progRelease(st, t)
	}
	c.count(func(s *Stats) { s.Bitmaps["gfx progressive upgrade"]++ })
	return t, nil
}

// progUpgradePass adds an upgrade's bits to a component's DecDwtQ: of
// each band, as many as its bit position falls from prev to pos, at
// pos; LL3's and those of coefficients with a sign raw, the others'
// from the SRL stream, whose values set their signs.
func progUpgradePass(coef *[4096]int16, sign *[4096]int8, prev, pos *rfxQuant, layout *[rfxBands]rfxBand, srlData, rawData []byte) error {
	s := newSRL(srlData)
	raw := bitReader{b: rawData}
	for b, band := range layout {
		n := int(prev[b]) - int(pos[b])
		if n < 0 {
			return fmt.Errorf("progressive upgrade of band %d from bit %d to %d", b, prev[b], pos[b])
		}
		if n == 0 {
			continue
		}
		sh := uint(pos[b])
		lo, hi := band.off, band.off+band.w*band.h
		for i := lo; i < hi; i++ {
			var v int32
			switch {
			case b == bandLL3:
				v = int32(raw.read(uint(n))) // #nosec G115 -- n ≤ 15
			case sign[i] > 0:
				v = int32(raw.read(uint(n))) // #nosec G115 -- n ≤ 15
			case sign[i] < 0:
				v = -int32(raw.read(uint(n))) // #nosec G115 -- n ≤ 15
			default:
				v = s.next(n)
				sign[i] = sgn(v)
			}
			coef[i] = sat16(int32(coef[i]) + v<<sh)
		}
	}
	return nil
}

// layoutOf returns the bands of the transform, reduce-extrapolate's
// with extra.
func layoutOf(extra bool) *[rfxBands]rfxBand {
	if extra {
		return &rfxLayout[1]
	}
	return &rfxLayout[0]
}

func sgn(v int32) int8 {
	switch {
	case v > 0:
		return 1
	case v < 0:
		return -1
	}
	return 0
}

// signOf returns the signs of component i, or nil.
func signOf(s *[3][4096]int8, i int) *[4096]int8 {
	if s == nil {
		return nil
	}
	return &s[i]
}

// maxSignPool bounds the sign arrays kept for tiles to come.
const maxSignPool = 64

// progSign returns a sign array for a tile of st, from those that tiles
// gave back if there are any, as it was. A first pass sets each sign
// that an upgrade in its transform reads.
func (c *Client) progSign(st *progState) *[3][4096]int8 {
	st.signed++
	g := &c.gfx
	if n := len(g.signs); n > 0 {
		s := g.signs[n-1]
		g.signs = g.signs[:n-1]
		return s
	}
	return new([3][4096]int8)
}

// progRelease takes the signs of a tile of st, which a full pass or the
// end of its codec context leaves unused.
func (c *Client) progRelease(st *progState, t *progTile) {
	if t.sign == nil {
		return
	}
	if len(c.gfx.signs) < maxSignPool {
		c.gfx.signs = append(c.gfx.signs, t.sign)
	}
	t.sign = nil
	st.signed--
}
