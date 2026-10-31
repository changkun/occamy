// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package rdp

import (
	"fmt"
	"image"
)

// RemoteFX's tiles ([MS-RDPRFX] 3.1.8.2): 64×64 pixels, each of their
// three components, Y, Cb and Cr, 4096 coefficients of a discrete
// wavelet transform of three levels in ten bands, quantized and coded
// in RLGR (rlgr.go). A tile decodes as the bands dequantized, the
// inverse transform, and the conversion to RGB. RemoteFX Progressive
// ([MS-RDPEGFX] 3.3.8.2, progressive.go) codes the same tiles, in
// passes, and may use the transform's reduce-extrapolate variant, whose
// bands differ in size.
//
// The decoder works in the 11.5 fixed point that [MS-RDPRFX] 4.2.4.3
// gives for RDP 7.1 and 8.0: a coefficient of quantization factor q is
// shifted left by q − 1, which is its scale of 2^(q−6) times 32, the
// transform runs on those integers, and the conversion divides by 32.
// The tests check the decoder against that section's dumps of a tile,
// and the conversion against the matrix in floating point.

// rfxTile is a tile's side in pixels.
const rfxTile = 64

// The bands in their order among a component's coefficients
// ([MS-RDPRFX] 3.1.8.1.6).
const (
	bandHL1 = iota
	bandLH1
	bandHH1
	bandHL2
	bandLH2
	bandHH2
	bandHL3
	bandLH3
	bandHH3
	bandLL3
	rfxBands
)

// rfxBand is a band's place among the coefficients, and its size. A
// band named HL is high horizontally and low vertically.
type rfxBand struct{ off, w, h int }

// rfxLayout gives the bands of the original transform ([MS-RDPRFX]
// 3.1.8.1.4) and of the reduce-extrapolate one ([MS-RDPEGFX]
// 3.2.8.1.2.2), whose first level gives 33 low and 31 high
// coefficients a side.
var rfxLayout = [2][rfxBands]rfxBand{
	{
		{0, 32, 32}, {1024, 32, 32}, {2048, 32, 32},
		{3072, 16, 16}, {3328, 16, 16}, {3584, 16, 16},
		{3840, 8, 8}, {3904, 8, 8}, {3968, 8, 8}, {4032, 8, 8},
	},
	{
		{0, 31, 33}, {1023, 33, 31}, {2046, 31, 31},
		{3007, 16, 17}, {3279, 17, 16}, {3551, 16, 16},
		{3807, 8, 9}, {3879, 9, 8}, {3951, 8, 8}, {4015, 9, 9},
	},
}

// rfxQuant is a component's quantization factor for each band, or the
// bit positions of a progressive pass, by band.
type rfxQuant [rfxBands]uint8

// The orders of the bands in TS_RFX_CODEC_QUANT ([MS-RDPRFX]
// 2.2.2.1.5) and in RFX_COMPONENT_CODEC_QUANT ([MS-RDPEGFX]
// 2.2.4.2.1.5.2), which swap the HL and LH bands of each level.
var (
	rfxQuantOrder  = [rfxBands]int{bandLL3, bandLH3, bandHL3, bandHH3, bandLH2, bandHL2, bandHH2, bandLH1, bandHL1, bandHH1}
	progQuantOrder = [rfxBands]int{bandLL3, bandHL3, bandLH3, bandHH3, bandHL2, bandLH2, bandHH2, bandHL1, bandLH1, bandHH1}
)

// parseQuant reads the 5 bytes of a table of ten 4-bit values, low
// nibble first, in the band order order.
func parseQuant(b []byte, order *[rfxBands]int) rfxQuant {
	var q rfxQuant
	for i, band := range order {
		q[band] = b[i/2] >> (4 * (i % 2)) & 0xf
	}
	return q
}

// rfxDecoder holds the scratch space that decoding a tile takes, kept
// between tiles: the coefficients RLGR gives, the dequantized ones,
// the transform's levels, and the three components decoded.
type rfxDecoder struct {
	coef   [3][4096]int16
	work   [4096]int32
	rows   [65 * 64]int32
	l3     [17 * 17]int32
	l2     [33 * 33]int32
	planes [3][4096]int32
}

// planesOf decodes the components coef, quantized as quant, into
// d.planes: Y, Cb and Cr in 11.5 fixed point, rows of 64.
func (d *rfxDecoder) planesOf(coef *[3][4096]int16, quant *[3]rfxQuant, extra bool) {
	for c := range 3 {
		d.component(&d.planes[c], &coef[c], &quant[c], extra)
	}
}

// component dequantizes a component's coefficients and inverts their
// transform into dst.
func (d *rfxDecoder) component(dst *[4096]int32, coef *[4096]int16, q *rfxQuant, extra bool) {
	x := 0
	if extra {
		x = 1
	}
	b := &rfxLayout[x]
	for i, band := range b {
		// The scale 2^(q−6) in 11.5 fixed point; the specification's
		// factors are 6 to 15, and below 1 they leave a band as it is.
		sh := uint(max(int(q[i])-1, 0)) // #nosec G115 -- at most 14
		n := band.w * band.h
		src, w := coef[band.off:band.off+n], d.work[band.off:band.off+n]
		for j, v := range src {
			w[j] = int32(v) << sh
		}
	}
	band := func(k int) []int32 { return d.work[b[k].off : b[k].off+b[k].w*b[k].h] }
	// Level 3 into l3, level 2 into l2, level 1 into dst: each takes the
	// low band the level below gave, and its own three high bands. With
	// factors of 15, which no encoder sends for content, coefficients
	// near the bounds of 16 bits are 2^29 once shifted, and the
	// lifting's sums of them may pass 32 bits and wrap, as Go defines.
	// The planes go nowhere but rgbOf, which clamps them first, so a
	// wrapped sample is a wrong colour within its tile, as the server
	// could have sent anyway (TestRFXWrap).
	idwtLevel(d.l3[:], band(bandLL3), band(bandHL3), band(bandLH3), band(bandHH3),
		b[bandLL3].w, b[bandHL3].w, b[bandLL3].h, b[bandLH3].h, false, &d.rows)
	idwtLevel(d.l2[:], d.l3[:], band(bandHL2), band(bandLH2), band(bandHH2),
		b[bandLH2].w, b[bandHL2].w, b[bandHL2].h, b[bandLH2].h, false, &d.rows)
	idwtLevel(dst[:], d.l2[:], band(bandHL1), band(bandLH1), band(bandHH1),
		b[bandLH1].w, b[bandHL1].w, b[bandHL1].h, b[bandLH1].h, extra, &d.rows)
}

// idwtLevel inverts a level of the transform: the low band ll, wl×hl,
// with the high bands hl (wh×hl), lh (wl×hh) and hh (wh×hh), each in
// rows of its own width, into out, (wl+wh)×(hl+hh) in rows of wl+wh.
// It inverts the rows first and the columns then, the transform having
// done the columns first ([MS-RDPRFX] 4.2.4.3). With extra, as the
// reduce-extrapolate transform's first level, each direction's high
// coefficients take a zero as one more, and the last sample, an
// extrapolation, is dropped ([MS-RDPEGFX] 3.3.8.2.2).
//
// The columns are inverted a row of them at a time: each step of the
// lifting takes whole rows, in which the columns are independent.
func idwtLevel(out, ll, hl, lh, hh []int32, wl, wh, hL, hH int, extra bool, rows *[65 * 64]int32) {
	w, h := wl+wh, hL+hH
	var hi, x [66]int32
	pad := 0
	if extra {
		pad = 1
	}
	for y := range h {
		var lrow, hrow []int32
		if y < hL {
			lrow, hrow = ll[y*wl:][:wl], hl[y*wh:][:wh]
		} else {
			lrow, hrow = lh[(y-hL)*wl:][:wl], hh[(y-hL)*wh:][:wh]
		}
		if extra {
			copy(hi[:], hrow)
			hi[wh] = 0
			hrow = hi[:wh+1]
		}
		idwt1(x[:w+pad], lrow, hrow)
		copy(rows[y*w:][:w], x[:w])
	}
	// The columns' low coefficients are rows[:hL], their high ones
	// rows[hL:h] and, with extra, a row of zeros; their samples go to
	// out's rows, and the one past them, with extra, to the end of rows.
	var zero [64]int32
	nh := hH + pad
	high := func(n int) []int32 {
		if n >= hH {
			return zero[:w]
		}
		return rows[(hL+n)*w:][:w]
	}
	sample := func(k int) []int32 {
		if k == h {
			return rows[h*w:][:w]
		}
		return out[k*w:][:w]
	}
	for n := range hL {
		prev, cur := high(max(n-1, 0)), high(min(n, nh-1))
		liftEven(sample(2*n), rows[n*w:][:w], prev, cur)
	}
	last := hL + nh - 1
	for n := range nh {
		next := sample(2 * n)
		if 2*n+2 <= last {
			next = sample(2*n + 2)
		}
		liftOdd(sample(2*n+1), high(n), sample(2*n), next)
	}
}

// liftEven sets x to the even samples l − ⌊(a + b + 1) / 2⌋, a and b
// the high coefficients on either side.
func liftEven(x, l, a, b []int32) {
	x, a, b = x[:len(l)], a[:len(l)], b[:len(l)]
	for i, v := range l {
		x[i] = v - (a[i]+b[i]+1)>>1
	}
}

// liftOdd sets x to the odd samples 2h + ⌊(a + b) / 2⌋, a and b the
// even samples on either side.
func liftOdd(x, h, a, b []int32) {
	x, a, b = x[:len(h)], a[:len(h)], b[:len(h)]
	for i, v := range h {
		x[i] = 2*v + (a[i]+b[i])>>1
	}
}

// idwt1 inverts the lifting of [MS-RDPRFX] 3.1.8.2.4 on the low
// coefficients l and the high ones h, as many or one fewer, into x,
// len(l)+len(h) samples. The high coefficients are mirrored at both
// ends, H[−1] being H[0] and H[len(h)] H[len(h)−1], and so is the last
// even sample where there is none after an odd one.
func idwt1(x, l, h []int32) {
	nh, nl := len(h), len(l)
	x = x[:nl+nh]
	x[0] = l[0] - (h[0]+h[0]+1)>>1
	for n := 1; n < nh; n++ {
		x[2*n] = l[n] - (h[n-1]+h[n]+1)>>1
	}
	if nl > nh {
		x[2*nh] = l[nh] - (h[nh-1]+h[nh-1]+1)>>1
	}
	for n := 0; n < nh-1; n++ {
		x[2*n+1] = 2*h[n] + (x[2*n]+x[2*n+2])>>1
	}
	n := nh - 1
	next := x[2*n]
	if 2*n+2 < len(x) {
		next = x[2*n+2]
	}
	x[2*n+1] = 2*h[n] + (x[2*n]+next)>>1
}

// The YCbCr to RGB matrix of [MS-RDPRFX] 3.1.8.2.5, times 2^14: Cr's
// share of R, Cb's and Cr's of G, Cb's of B. Cr's share of B,
// 0.000013, is below a 2^14th and left out.
const (
	rfxCrR = 22979 // 1.402525
	rfxCbG = 5632  // 0.343730
	rfxCrG = 11705 // 0.714401
	rfxCbB = 28998 // 1.769905
	// rfxClamp bounds a component as the conversion takes it, so that
	// it stays within 32 bits: twice the range of a pixel.
	rfxClamp = 8192
)

// rgbOf converts a pixel of Y, Cb and Cr in 11.5 fixed point to RGB:
// Y shifted up by 128, the matrix, rounded and clamped.
func rgbOf(y, cb, cr int32) (r, g, b byte) {
	y = (min(max(y, -rfxClamp), rfxClamp-1) + 128<<5) << 14
	cb = min(max(cb, -rfxClamp), rfxClamp-1)
	cr = min(max(cr, -rfxClamp), rfxClamp-1)
	const half = 1 << 18
	return clamp8(int((y + rfxCrR*cr + half) >> 19)), clamp8(int((y - rfxCbG*cb - rfxCrG*cr + half) >> 19)), clamp8(int((y + rfxCbB*cb + half) >> 19))
}

// draw converts the tile that d.planes holds, whose origin is p on s,
// where it lies within each rectangle of clips, which lie within s,
// and returns the pixels it drew.
func (d *rfxDecoder) draw(s *surface, p image.Point, clips []image.Rectangle) int {
	tile := image.Rectangle{Min: p, Max: p.Add(image.Pt(rfxTile, rfxTile))}
	drawn := 0
	for _, c := range clips {
		r := c.Intersect(tile)
		if r.Empty() {
			continue
		}
		for y := r.Min.Y; y < r.Max.Y; y++ {
			i := (y-p.Y)*rfxTile + r.Min.X - p.X
			row := s.pix[s.offset(r.Min.X, y):][:4*r.Dx()]
			ys, cbs, crs := d.planes[0][i:][:r.Dx()], d.planes[1][i:][:r.Dx()], d.planes[2][i:][:r.Dx()]
			for k := range ys {
				row[4*k], row[4*k+1], row[4*k+2] = rgbOf(ys[k], cbs[k], crs[k])
				row[4*k+3] = 0xff
			}
		}
		s.touch(r)
		drawn += r.Dx() * r.Dy()
	}
	return drawn
}

// clipCost is what drawing a tile within clips costs, in pixels: the
// pixels it draws, and a pixel for each rectangle it is clipped to.
func clipCost(p image.Point, clips []image.Rectangle) int {
	tile := image.Rectangle{Min: p, Max: p.Add(image.Pt(rfxTile, rfxTile))}
	n := len(clips)
	for _, c := range clips {
		r := c.Intersect(tile)
		n += r.Dx() * r.Dy()
	}
	return n
}

// rfxTileCost is what decoding a tile's coefficients and inverting
// their transform costs, charged against the decoding rate before the
// pixels it draws, so that a tile drawn whole is charged rfxPixelCost
// units a pixel. Decoded and drawn, a tile of real content takes 11 to
// 13 ns a pixel (BenchmarkGFXRemoteFX, BenchmarkGFXProgressive), and
// one of a header alone 8 to 10 (BenchmarkHostileTiles): 1.1 to 1.9 ns
// a unit, as a planar bitmap's pixel at 2 units takes 1.1 to 1.3.
const (
	rfxPixelCost = 7
	rfxTileCost  = (rfxPixelCost - 1) * rfxTile * rfxTile
)

// TS_RFX blocks ([MS-RDPRFX] 2.2.2).
const (
	rfxSync          = 0xccc0
	rfxCodecVersions = 0xccc1
	rfxChannels      = 0xccc2
	rfxContext       = 0xccc3
	rfxFrameBegin    = 0xccc4
	rfxFrameEnd      = 0xccc5
	rfxRegion        = 0xccc6
	rfxTileset       = 0xccc7
	rfxTileBlock     = 0xcac3
	rfxRegionType    = 0xcac1
	rfxTilesetType   = 0xcac2
	rfxTileHeader    = 19
)

// gfxRemoteFX decodes a RemoteFX message ([MS-RDPRFX] 2.2.2.3) of the
// wire-to-surface PDU at dr on s: its tiles, at their places from dr's
// origin, where its region's rectangles, also from dr's origin, and dr
// cover them. RemoteFX keeps no state between messages. Its rectangles
// apply to the tile set after them; a tile set before any has dr for
// its region, as a region of no rectangles has.
func (c *Client) gfxRemoteFX(s *surface, dr image.Rectangle, data []byte) error {
	clips := c.gfx.clips[:0]
	clips = append(clips, dr)
	tiles := 0
	for len(data) > 0 {
		r := reader{b: data}
		typ, n := r.u16(), r.u32()
		if r.err != nil || n < 6 || int64(n) > int64(len(data)) {
			return fmt.Errorf("RemoteFX block of %d bytes in %d", n, len(data))
		}
		body := data[6:n]
		data = data[n:]
		var err error
		switch typ {
		case rfxRegion:
			clips, err = rfxRegionRects(body, dr, clips[:0])
		case rfxTileset:
			var k int
			k, err = c.rfxTileset(s, dr, body, clips)
			tiles += k
		}
		if err != nil {
			return err
		}
	}
	c.gfx.clips = clips
	c.count(func(st *Stats) { st.Bitmaps["gfx remotefx"] += tiles })
	return nil
}

// rfxRegionRects reads TS_RFX_REGION's rectangles, after its channel's
// header, as rectangles of the surface within dr, into clips.
func rfxRegionRects(b []byte, dr image.Rectangle, clips []image.Rectangle) ([]image.Rectangle, error) {
	r := reader{b: b}
	r.u8() // codecId
	r.u8() // channelId
	r.u8() // regionFlags
	n := int(r.u16())
	if r.err != nil || r.len() < 8*n {
		return nil, fmt.Errorf("RemoteFX region of %d rectangles in %d bytes", n, r.len())
	}
	if n == 0 {
		return append(clips, dr), nil
	}
	for range n {
		x, y, w, h := int(r.u16()), int(r.u16()), int(r.u16()), int(r.u16())
		if rc := image.Rect(x, y, x+w, y+h).Add(dr.Min).Intersect(dr); !rc.Empty() {
			clips = append(clips, rc)
		}
	}
	return clips, nil
}

// rfxTileset decodes TS_RFX_TILESET's tiles, after its channel's
// header, onto s within clips, and returns how many it decoded.
func (c *Client) rfxTileset(s *surface, dr image.Rectangle, b []byte, clips []image.Rectangle) (int, error) {
	r := reader{b: b}
	r.u8() // codecId
	r.u8() // channelId
	subtype := r.u16()
	r.u16() // idx
	props := r.u16()
	numQuant, tileSize, numTiles := int(r.u8()), r.u8(), int(r.u16())
	size := r.u32()
	if r.err != nil {
		return 0, r.err
	}
	mode := int(props >> 10 & 0xf)
	switch {
	case subtype != rfxTilesetType:
		return 0, fmt.Errorf("RemoteFX tile set of subtype %#x", subtype)
	case tileSize != rfxTile:
		return 0, fmt.Errorf("RemoteFX tiles of %d pixels", tileSize)
	case mode != rlgr1 && mode != rlgr3:
		return 0, fmt.Errorf("RemoteFX entropy algorithm %d", mode)
	case r.len() < 5*numQuant:
		return 0, fmt.Errorf("%d quantization tables in %d bytes", numQuant, r.len())
	}
	quants := c.gfx.quants[:0]
	for range numQuant {
		quants = append(quants, parseQuant(r.take(5), &rfxQuantOrder))
	}
	c.gfx.quants = quants
	if int64(size) > int64(r.len()) {
		return 0, fmt.Errorf("RemoteFX tiles of %d bytes in %d", size, r.len())
	}
	data := r.take(int(size))
	d := c.gfxDecoder()
	tiles := 0
	for ; tiles < numTiles && len(data) > 0; tiles++ {
		t := reader{b: data}
		typ, n := t.u16(), t.u32()
		if t.err != nil || n < rfxTileHeader || int64(n) > int64(len(data)) {
			return tiles, fmt.Errorf("RemoteFX tile of %d bytes in %d", n, len(data))
		}
		if typ != rfxTileBlock {
			return tiles, fmt.Errorf("RemoteFX tile block of type %#x", typ)
		}
		qy, qcb, qcr := int(t.u8()), int(t.u8()), int(t.u8())
		x, y := int(t.u16()), int(t.u16())
		var lens [3]int
		for i := range lens {
			lens[i] = int(t.u16())
		}
		body := data[rfxTileHeader:n]
		data = data[n:]
		if qy >= numQuant || qcb >= numQuant || qcr >= numQuant {
			return tiles, fmt.Errorf("RemoteFX tile of quantization tables %d, %d and %d of %d", qy, qcb, qcr, numQuant)
		}
		if lens[0]+lens[1]+lens[2] > len(body) {
			return tiles, fmt.Errorf("RemoteFX tile of %d, %d and %d bytes in %d", lens[0], lens[1], lens[2], len(body))
		}
		p := dr.Min.Add(image.Pt(x*rfxTile, y*rfxTile))
		if err := c.pace(rfxTileCost + clipCost(p, clips)); err != nil {
			return tiles, err
		}
		for i := range 3 {
			if err := rlgrDecode(mode, body[:lens[i]], d.coef[i][:]); err != nil {
				return tiles, err
			}
			body = body[lens[i]:]
			undelta(d.coef[i][rfxLayout[0][bandLL3].off:])
		}
		quant := [3]rfxQuant{quants[qy], quants[qcb], quants[qcr]}
		d.planesOf(&d.coef, &quant, false)
		d.draw(s, p, clips)
	}
	return tiles, nil
}

// undelta sums the LL3 band's differences into its values ([MS-RDPRFX]
// 3.1.8.2.2), saturating.
func undelta(ll []int16) {
	var acc int32
	for i, v := range ll {
		acc = int32(sat16(acc + int32(v)))
		ll[i] = int16(acc) // #nosec G115 -- saturated
	}
}

// gfxDecoder returns the tile decoder, made on first use.
func (c *Client) gfxDecoder() *rfxDecoder {
	if c.gfx.rfx == nil {
		c.gfx.rfx = new(rfxDecoder)
	}
	return c.gfx.rfx
}
