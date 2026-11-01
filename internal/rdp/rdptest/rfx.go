// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package rdptest

import (
	"image"
	"math"
	mbits "math/bits"
)

// An encoder of RemoteFX ([MS-RDPRFX] 3.1.8.1) and RemoteFX Progressive
// ([MS-RDPEGFX] 3.2.8.1), written from the specifications' encoding
// stages for the tests: the colour conversion, the forward transform,
// quantization, the entropy coders and the progressive passes, and the
// messages that carry them. It follows the text, not speed.

// The bands of a tile's component, in the order of its coefficients
// ([MS-RDPRFX] 3.1.8.1.6). A band named HL is high horizontally and low
// vertically.
const (
	HL1 = iota
	LH1
	HH1
	HL2
	LH2
	HH2
	HL3
	LH3
	HH3
	LL3
	Bands
)

// Band is a band's place among a component's coefficients, and its
// size, its coefficients in rows of W.
type Band struct{ Off, W, H int }

// Layout returns the bands of the transform: of the original one
// ([MS-RDPRFX] 3.1.8.1.4), or of the reduce-extrapolate one with extra
// ([MS-RDPEGFX] 3.2.8.1.2.2), whose first level takes a 65th sample
// and gives 33 low and 31 high coefficients a side.
func Layout(extra bool) *[Bands]Band {
	var l [Bands]Band
	n, off := 64, 0
	for level := range 3 {
		pad := 0
		if extra && level == 0 {
			pad = 1
		}
		nl, nh := (n+pad+1)/2, (n+pad)/2-pad
		for i, wh := range [3][2]int{{nh, nl}, {nl, nh}, {nh, nh}} {
			l[3*level+i] = Band{off, wh[0], wh[1]}
			off += wh[0] * wh[1]
		}
		n = nl
	}
	l[LL3] = Band{off, n, n}
	return &l
}

// Quant is a component's quantization factors, or a progressive pass's
// bit positions, by band.
type Quant [Bands]uint8

// QuantOf returns a Quant of the factors in TS_RFX_CODEC_QUANT's order,
// LL3 first.
func QuantOf(f [Bands]uint8) Quant {
	var q Quant
	for i, b := range rfxOrder {
		q[b] = f[i]
	}
	return q
}

// The orders of the bands in TS_RFX_CODEC_QUANT ([MS-RDPRFX] 2.2.2.1.5)
// and in RFX_COMPONENT_CODEC_QUANT ([MS-RDPEGFX] 2.2.4.2.1.5.2).
var (
	rfxOrder  = [Bands]int{LL3, LH3, HL3, HH3, LH2, HL2, HH2, LH1, HL1, HH1}
	progOrder = [Bands]int{LL3, HL3, LH3, HH3, HL2, LH2, HH2, HL1, LH1, HH1}
)

// table is q as 5 bytes of 4-bit values, low nibble first, in order.
func (q *Quant) table(order *[Bands]int) []byte {
	b := make([]byte, 5)
	for i, band := range order {
		b[i/2] |= q[band] & 0xf << (4 * (i % 2))
	}
	return b
}

// Components converts the tile of img at p, black outside img, to Y,
// Cb and Cr ([MS-RDPRFX] 3.1.8.1.3), Y less 128, in 11.5 fixed point:
// 32 times their values, rounded.
func Components(img *image.RGBA, p image.Point) *[3][4096]int32 {
	var c [3][4096]int32
	for y := range 64 {
		for x := range 64 {
			var r, g, b float64
			if q := p.Add(image.Pt(x, y)); q.In(img.Rect) {
				px := img.RGBAAt(q.X, q.Y)
				r, g, b = float64(px.R), float64(px.G), float64(px.B)
			}
			i := y*64 + x
			c[0][i] = int32(math.Round(32 * (0.299*r + 0.587*g + 0.114*b - 128)))
			c[1][i] = int32(math.Round(32 * (-0.168935*r - 0.331665*g + 0.50059*b)))
			c[2][i] = int32(math.Round(32 * (0.499813*r - 0.418531*g - 0.081282*b)))
		}
	}
	return &c
}

// DWT transforms a component, in rows of 64, by three levels of the 5/3
// wavelet's lifting ([MS-RDPRFX] 3.1.8.1.4), each the columns first and
// the rows then, into its bands in the order of Layout(extra).
func DWT(src *[4096]int32, extra bool) *[4096]int32 {
	var out [4096]int32
	lay := Layout(extra)
	x, n := src[:], 64
	for level := range 3 {
		ll, hl, lh, hh := dwt2(x, n, extra && level == 0)
		for i, band := range [3][]int32{hl, lh, hh} {
			copy(out[lay[3*level+i].Off:], band)
		}
		x, n = ll, lay[3*level+1].W
	}
	copy(out[lay[LL3].Off:], x)
	return &out
}

// dwt2 is a level of the transform of x, n×n: the columns, then the
// rows of the low and the high halves. With extra, each line takes a
// 65th sample, extrapolated from the last two, and gives up its last
// high coefficient, which is then zero.
func dwt2(x []int32, n int, extra bool) (ll, hl, lh, hh []int32) {
	pad := 0
	if extra {
		pad = 1
	}
	nl, nh := (n+pad+1)/2, (n+pad)/2-pad
	lo, hi := make([]int32, nl*n), make([]int32, nh*n)
	line := make([]int32, n+pad)
	l, h := make([]int32, nl), make([]int32, nh+pad)
	for col := range n {
		for y := range n {
			line[y] = x[y*n+col]
		}
		dwt1(l, h, line, extra)
		for y := range nl {
			lo[y*n+col] = l[y]
		}
		for y := range nh {
			hi[y*n+col] = h[y]
		}
	}
	rows := func(src []int32, rows int) (low, high []int32) {
		low, high = make([]int32, rows*nl), make([]int32, rows*nh)
		for y := range rows {
			copy(line, src[y*n:][:n])
			dwt1(l, h, line, extra)
			copy(low[y*nl:], l)
			copy(high[y*nh:], h[:nh])
		}
		return low, high
	}
	ll, hl = rows(lo, nl)
	lh, hh = rows(hi, nh)
	return ll, hl, lh, hh
}

// dwt1 lifts the samples x, mirrored at both ends, into the low
// coefficients l and the high ones h:
//
//	H[n] = (X[2n+1] − ⌊(X[2n] + X[2n+2]) / 2⌋) / 2
//	L[n] = X[2n] + ⌊(H[n−1] + H[n] + 1) / 2⌋
//
// With extra, x's last sample is extrapolated from the two before it.
func dwt1(l, h, x []int32, extra bool) {
	n := len(x)
	if extra {
		x[n-1] = 2*x[n-2] - x[n-3]
	}
	for i := range h {
		next := x[2*i]
		if 2*i+2 < n {
			next = x[2*i+2]
		}
		h[i] = (x[2*i+1] - (x[2*i]+next)>>1) >> 1
	}
	for i := range l {
		prev, cur := h[max(i-1, 0)], h[min(i, len(h)-1)]
		l[i] = x[2*i] + (prev+cur+1)>>1
	}
}

// Quantize divides each coefficient of a component, in 11.5 fixed
// point, by its band's scale ([MS-RDPRFX] 3.1.8.1.5), 2^(q−6) of the
// value and so 2^(q−1) of the coefficient, rounding half away from
// zero.
func Quantize(c *[4096]int32, q *Quant, extra bool) *[4096]int16 {
	var out [4096]int16
	for b, band := range Layout(extra) {
		scale := math.Ldexp(1, int(q[b])-1)
		for i := band.Off; i < band.Off+band.W*band.H; i++ {
			out[i] = int16(math.Round(float64(c[i]) / scale)) // #nosec G115 -- the tests' values stay within 16 bits
		}
	}
	return &out
}

// EncodeTile returns the tile of img at p transformed and quantized by
// q, its components in Y, Cb and Cr: DwtQ.
func EncodeTile(img *image.RGBA, p image.Point, q *[3]Quant, extra bool) *[3][4096]int16 {
	comps := Components(img, p)
	var out [3][4096]int16
	for i := range comps {
		out[i] = *Quantize(DWT(&comps[i], extra), &q[i], extra)
	}
	return &out
}

// Deltas returns c with the values of its LL3 band given as the
// differences from the one before, as the entropy coders take them.
func Deltas(c []int16, ll Band) []int16 {
	out := append([]int16(nil), c...)
	var prev int16
	for i := ll.Off; i < ll.Off+ll.W*ll.H; i++ {
		out[i] = c[i] - prev
		prev = c[i]
	}
	return out
}

// The entropy algorithms of TS_RFX_TILESET ([MS-RDPRFX] 2.2.2.3.4).
const (
	RLGR1 = 1
	RLGR3 = 4
)

// RLGR codes v in RLGR1 or RLGR3 ([MS-RDPRFX] 3.1.8.1.7.3.2). Zeros at
// the end are a run, its remainder written as a run that ends the data.
func RLGR(mode int, v []int16) []byte {
	var w bitWriter
	k, kp, krp := 1, 8, 8
	for i := 0; i < len(v); {
		if k != 0 {
			nz := 0
			for i < len(v) && v[i] == 0 {
				nz++
				i++
			}
			for nz >= 1<<k {
				w.bits(0, 1)
				nz -= 1 << k
				k = updateParam(&kp, 4)
			}
			if i == len(v) && nz == 0 {
				break
			}
			w.bits(1, 1)
			w.bits(uint32(nz), k) // #nosec G115 -- below 2^k
			if i == len(v) {
				break
			}
			in := int32(v[i])
			i++
			sign, mag := uint32(0), in
			if in < 0 {
				sign, mag = 1, -in
			}
			w.bits(sign, 1)
			codeGR(&w, &krp, uint32(mag-1)) // #nosec G115 -- mag ≥ 1
			k = updateParam(&kp, -6)
			continue
		}
		a := twoMagSign(v[i])
		i++
		if mode == RLGR1 {
			codeGR(&w, &krp, a)
			if a == 0 {
				k = updateParam(&kp, 3)
			} else {
				k = updateParam(&kp, -3)
			}
			continue
		}
		var b uint32
		if i < len(v) {
			b = twoMagSign(v[i])
			i++
		}
		codeGR(&w, &krp, a+b)
		w.bits(a, mbits.Len32(a+b))
		switch {
		case a != 0 && b != 0:
			k = updateParam(&kp, -6)
		case a == 0 && b == 0:
			k = updateParam(&kp, 6)
		}
	}
	return w.b
}

// updateParam adds d to *p, within [0, 80], and returns *p / 8.
func updateParam(p *int, d int) int {
	*p = min(max(*p+d, 0), 80)
	return *p >> 3
}

// codeGR writes val in the Golomb-Rice code of *krp / 8 and adapts *krp.
func codeGR(w *bitWriter, krp *int, val uint32) {
	kr := *krp >> 3
	vk := val >> kr
	for range vk {
		w.bits(1, 1)
	}
	w.bits(0, 1)
	w.bits(val&(1<<kr-1), kr)
	switch {
	case vk == 0:
		updateParam(krp, -2)
	case vk > 1:
		updateParam(krp, int(vk)) // #nosec G115 -- small
	}
}

// twoMagSign is 2·|v| − (1 if v < 0).
func twoMagSign(v int16) uint32 {
	if v < 0 {
		return uint32(-2*int32(v) - 1) // #nosec G115 -- positive
	}
	return uint32(2 * int32(v)) // #nosec G115 -- positive
}

// ProgFirst is the first progressive pass ([MS-RDPEGFX] 3.2.8.1.5.1) of
// a component's coefficients sb, bands lay, at the bit positions pos:
// ProgQ, sb divided by 2^pos, toward zero but LL3 toward minus
// infinity, LL3 as differences, in RLGR1; and DTS, what it sends,
// ProgQ times 2^pos.
func ProgFirst(sb []int16, pos *Quant, lay *[Bands]Band) (data []byte, dts []int16) {
	progQ := make([]int16, len(sb))
	dts = make([]int16, len(sb))
	for b, band := range lay {
		sh := pos[b]
		for i := band.Off; i < band.Off+band.W*band.H; i++ {
			v := int32(sb[i])
			q := v / (1 << sh)
			if b == LL3 {
				q = v >> sh
			}
			progQ[i], dts[i] = int16(q), int16(q<<sh) // #nosec G115 -- within sb's range
		}
	}
	return RLGR(RLGR1, Deltas(progQ, lay[LL3])), dts
}

// ProgUpgrade is an upgrade pass ([MS-RDPEGFX] 3.2.8.1.5.2) of what
// remains of a component's coefficients, drs, bands lay, from the bit
// positions prev to pos, das being what the passes before sent:
// UpgradeQ, drs divided by 2^pos toward zero, of each band whose bit
// position falls, LL3's and those whose das is not zero as raw bits of
// their magnitudes, the others in SRL; and DTS, UpgradeQ times 2^pos.
func ProgUpgrade(drs, das []int16, prev, pos *Quant, lay *[Bands]Band) (srl, raw []byte, dts []int16) {
	s := srlWriter{kp: 8}
	var r bitWriter
	dts = make([]int16, len(drs))
	for b, band := range lay {
		n := int(prev[b]) - int(pos[b])
		if n <= 0 {
			continue
		}
		sh := pos[b]
		for i := band.Off; i < band.Off+band.W*band.H; i++ {
			q := int32(drs[i]) / (1 << sh)
			dts[i] = int16(q << sh) // #nosec G115 -- within drs's range
			if b == LL3 || das[i] != 0 {
				r.bits(uint32(max(q, -q)), n) // #nosec G115 -- a magnitude
				continue
			}
			s.put(q, n)
		}
	}
	return s.flush(), r.b, dts
}

// srlWriter writes the simplified run-length code ([MS-RDPEGFX]
// 3.1.8.1.5).
type srlWriter struct {
	w  bitWriter
	kp int
	nz int // the zeros of the run that is not written yet
}

// put writes v, whose magnitude takes at most nBits.
func (s *srlWriter) put(v int32, nBits int) {
	if v == 0 {
		s.nz++
		return
	}
	s.runs()
	s.w.bits(1, 1)
	s.w.bits(uint32(s.nz), s.kp>>3) // #nosec G115 -- below 2^k
	s.kp = max(s.kp-6, 0)
	s.nz = 0
	sign, mag := uint32(0), v
	if v < 0 {
		sign, mag = 1, -v
	}
	s.w.bits(sign, 1)
	for range mag - 1 {
		s.w.bits(0, 1)
	}
	if mag < 1<<nBits-1 {
		s.w.bits(1, 1)
	}
}

// runs writes the whole runs of the zeros that wait.
func (s *srlWriter) runs() {
	for s.nz >= 1<<(s.kp>>3) {
		s.w.bits(0, 1)
		s.nz -= 1 << (s.kp >> 3)
		s.kp = min(s.kp+4, 80)
	}
}

// flush writes the zeros that wait, the remainder of their runs as a
// run that ends the data, and returns the code.
func (s *srlWriter) flush() []byte {
	s.runs()
	if s.nz > 0 {
		s.w.bits(1, 1)
		s.w.bits(uint32(s.nz), s.kp>>3) // #nosec G115 -- below 2^k
	}
	return s.w.b
}

// TS_RFX block types ([MS-RDPRFX] 2.2.2.1.1).
const (
	rfxFrameBegin = 0xccc4
	rfxFrameEnd   = 0xccc5
	rfxRegion     = 0xccc6
	rfxTileset    = 0xccc7
	rfxTile       = 0xcac3
)

// RFXTile is a tile of TS_RFX_TILESET: the indices of its quantization
// tables, its place in tiles, and its components' data.
type RFXTile struct {
	Quant [3]int
	X, Y  int
	Data  [3][]byte
}

// RemoteFX is a RemoteFX message ([MS-RDPRFX] 2.2.2.3) of a frame:
// TS_RFX_FRAME_BEGIN, TS_RFX_REGION of rects, TS_RFX_TILESET of quants
// and tiles in the entropy algorithm mode, and TS_RFX_FRAME_END. The
// rectangles and the tiles are from the wire-to-surface PDU's
// rectangle.
func RemoteFX(mode int, rects []image.Rectangle, quants []Quant, tiles []RFXTile) []byte {
	block := func(typ int, body le) []byte {
		return append(le{}.u16(typ).u32(uint32(6+len(body))), body...) // #nosec G115 -- tests send less than 4 GiB
	}
	msg := block(rfxFrameBegin, le{}.u8(1).u8(0).u32(0).u16(1))
	region := le{}.u8(1).u8(0).u8(1).u16(len(rects))
	for _, r := range rects {
		region = region.u16(r.Min.X).u16(r.Min.Y).u16(r.Dx()).u16(r.Dy())
	}
	msg = append(msg, block(rfxRegion, region.u16(0xcac1).u16(1))...)
	var data []byte
	for _, t := range tiles {
		b := le{}.u8(byte(t.Quant[0])).u8(byte(t.Quant[1])).u8(byte(t.Quant[2])).u16(t.X).u16(t.Y) // #nosec G115 -- small
		b = b.u16(len(t.Data[0])).u16(len(t.Data[1])).u16(len(t.Data[2]))
		for _, d := range t.Data {
			b = append(b, d...)
		}
		data = append(data, block(rfxTile, b)...)
	}
	// lt, CODEC_MODE, ICT, the 5/3 wavelet, the entropy algorithm and
	// scalar quantization.
	props := 1 | 2<<1 | 1<<4 | 1<<6 | mode<<10 | 1<<14
	set := le{}.u8(1).u8(0).u16(0xcac2).u16(0).u16(props).u8(byte(len(quants))).u8(64).u16(len(tiles)).u32(uint32(len(data))) // #nosec G115 -- small
	for i := range quants {
		set = append(set, quants[i].table(&rfxOrder)...)
	}
	msg = append(msg, block(rfxTileset, append(set, data...))...)
	return append(msg, block(rfxFrameEnd, le{}.u8(1).u8(0))...)
}

// RFX_PROGRESSIVE block types ([MS-RDPEGFX] 2.2.4.2.1).
const (
	ProgSync        = 0xccc0
	ProgFrameBegin  = 0xccc1
	ProgFrameEnd    = 0xccc2
	ProgContext     = 0xccc3
	ProgRegionBlock = 0xccc4
	ProgTileSimple  = 0xccc5
	ProgTileFirst   = 0xccc6
	ProgTileUpgrade = 0xccc7
)

// ProgTile is a tile block of RFX_PROGRESSIVE_REGION: of Type
// ProgTileSimple, with Data, ProgTileFirst, with Data and Quality, or
// ProgTileUpgrade, with Quality, SRL and Raw.
type ProgTile struct {
	Type     int
	Quant    [3]int
	X, Y     int
	Diff     bool // a difference tile
	Quality  int
	Data     [3][]byte
	SRL, Raw [3][]byte
}

// Block is the tile's block.
func (t *ProgTile) Block() []byte {
	b := le{}.u8(byte(t.Quant[0])).u8(byte(t.Quant[1])).u8(byte(t.Quant[2])).u16(t.X).u16(t.Y) // #nosec G115 -- small
	var data []byte
	switch t.Type {
	case ProgTileUpgrade:
		b = b.u8(byte(t.Quality)) // #nosec G115 -- small
		for i := range 3 {
			b = b.u16(len(t.SRL[i])).u16(len(t.Raw[i]))
			data = append(append(data, t.SRL[i]...), t.Raw[i]...)
		}
	default:
		flags := byte(0)
		if t.Diff {
			flags = 1
		}
		b = b.u8(flags)
		if t.Type == ProgTileFirst {
			b = b.u8(byte(t.Quality)) // #nosec G115 -- small
		}
		for i := range 3 {
			b = b.u16(len(t.Data[i]))
			data = append(data, t.Data[i]...)
		}
		b = b.u16(0) // tail
	}
	return progBlock(t.Type, append(b, data...))
}

func progBlock(typ int, body []byte) []byte {
	return append(le{}.u16(typ).u32(uint32(6+len(body))), body...) // #nosec G115 -- tests send less than 4 GiB
}

// ProgQuant is RFX_PROGRESSIVE_CODEC_QUANT: the bit positions of a
// quality's pass, by component.
type ProgQuant struct {
	Quality byte
	BitPos  [3]Quant
}

// ProgRegion is RFX_PROGRESSIVE_REGION: its rectangles, of the surface,
// quantization tables and qualities, whether its tiles take the
// reduce-extrapolate transform, and its tiles' blocks.
type ProgRegion struct {
	Rects       []image.Rectangle
	Quants      []Quant
	ProgQuants  []ProgQuant
	Extrapolate bool
	Tiles       [][]byte
}

// Block is the region's block.
func (r *ProgRegion) Block() []byte {
	var tiles []byte
	for _, t := range r.Tiles {
		tiles = append(tiles, t...)
	}
	flags := byte(0)
	if r.Extrapolate {
		flags = 1
	}
	b := le{}.u8(64).u16(len(r.Rects)).u8(byte(len(r.Quants))).u8(byte(len(r.ProgQuants))).u8(flags) // #nosec G115 -- small
	b = b.u16(len(r.Tiles)).u32(uint32(len(tiles)))                                                  // #nosec G115 -- tests send less than 4 GiB
	for _, rc := range r.Rects {
		b = b.u16(rc.Min.X).u16(rc.Min.Y).u16(rc.Dx()).u16(rc.Dy())
	}
	for i := range r.Quants {
		b = append(b, r.Quants[i].table(&progOrder)...)
	}
	for _, p := range r.ProgQuants {
		b = b.u8(p.Quality)
		for i := range p.BitPos {
			b = append(b, p.BitPos[i].table(&progOrder)...)
		}
	}
	return progBlock(ProgRegionBlock, append(b, tiles...))
}

// ProgSyncContext is the blocks that begin a codec context's stream:
// RFX_PROGRESSIVE_SYNC and RFX_PROGRESSIVE_CONTEXT, of sub-band diffing.
func ProgSyncContext() []byte {
	return append(progBlock(ProgSync, le{}.u32(0xcaccacca).u16(0x0100)), progBlock(ProgContext, le{}.u8(0).u16(64).u8(1))...)
}

// Progressive is an RFX_PROGRESSIVE_BITMAP_STREAM of a frame of regions.
func Progressive(frame uint32, regions ...ProgRegion) []byte {
	msg := progBlock(ProgFrameBegin, le{}.u32(frame).u16(len(regions)))
	for i := range regions {
		msg = append(msg, regions[i].Block()...)
	}
	return append(msg, progBlock(ProgFrameEnd, nil)...)
}
