// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package rdp

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"encoding/hex"
	"image"
	"image/color"
	"math"
	"math/rand/v2"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"changkun.de/x/occamy/internal/rdp/rdptest"
)

// The tests of RemoteFX and RemoteFX Progressive: against the
// specifications' examples, against a decoder in floating point, and
// through the graphics pipeline with rdptest's encoder, which is
// written from the specifications' encoding stages and shares no code
// with the decoder.

// rfxVector is [MS-RDPRFX] 4.2.4's tile through each stage of decoding
// (testdata/rdprfx-tile.txt.gz, read from the specification's dumps):
// the TS_RFX_TILESET message, the components as RLGR3 gives them, LL3
// of Y dequantized, Y's level 3 inverted, the components decoded, in
// 11.5 fixed point, and the image as XRGB.
type rfxVector struct {
	tileset []byte
	entropy [3][]int
	ll3, l2 []int
	planes  [3][]int
	xrgb    []uint32
}

func loadRFXVector(t testing.TB) *rfxVector {
	t.Helper()
	f, err := os.Open("testdata/rdprfx-tile.txt.gz")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	v := &rfxVector{}
	sc := bufio.NewScanner(zr)
	sc.Buffer(nil, 1<<20)
	ints := func(fields []string) []int {
		out := make([]int, len(fields))
		for i, s := range fields {
			n, err := strconv.Atoi(s)
			if err != nil {
				t.Fatal(err)
			}
			out[i] = n
		}
		return out
	}
	comp := map[string]int{"y": 0, "cb": 1, "cr": 2}
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 0 || fields[0] == "#" {
			continue
		}
		name, vals := fields[0], fields[1:]
		switch name {
		case "tileset":
			b, err := hex.DecodeString(strings.Join(vals, ""))
			if err != nil {
				t.Fatal(err)
			}
			v.tileset = b
		case "entropy-y", "entropy-cb", "entropy-cr":
			v.entropy[comp[strings.TrimPrefix(name, "entropy-")]] = ints(vals)
		case "ll3":
			v.ll3 = ints(vals)
		case "l2":
			v.l2 = ints(vals)
		case "y", "cb", "cr":
			v.planes[comp[name]] = ints(vals)
		case "xrgb":
			for _, s := range vals {
				n, err := strconv.ParseUint(s, 16, 32)
				if err != nil {
					t.Fatal(err)
				}
				v.xrgb = append(v.xrgb, uint32(n))
			}
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return v
}

// The decoder reproduces [MS-RDPRFX] 4.2.4's tile: RLGR3, LL3's
// differences and dequantization, and the inverse of the transform's
// third level exactly; the components within 2/32 of a level, and the
// image within a level per channel. The specification's dumps of the
// second level's columns differ from its own lifting equations (figure
// 14) in a sixteenth of their samples by 1/32, and its conversion to
// RGB rounds in a way that no single rule reproduces, so those stages
// match within those bounds and not exactly.
func TestRFXSpecTile(t *testing.T) {
	v := loadRFXVector(t)
	b := v.tileset
	// TS_RFX_TILESET: its block's and channel's headers, subtype, idx,
	// properties, numQuant, tileSize, numTiles and tilesDataSize, 22
	// bytes, one table, and a tile of 19 bytes of header.
	if len(b) < 27+rfxTileHeader {
		t.Fatalf("tile set of %d bytes", len(b))
	}
	q := parseQuant(b[22:27], &rfxQuantOrder)
	tile := b[27:]
	data := tile[rfxTileHeader:]
	var d rfxDecoder
	for c := range 3 {
		n := int(binary.LittleEndian.Uint16(tile[13+2*c:]))
		if err := rlgrDecode(rlgr3, data[:n], d.coef[c][:]); err != nil {
			t.Fatal(err)
		}
		data = data[n:]
		for i, want := range v.entropy[c] {
			if int(d.coef[c][i]) != want {
				t.Fatalf("component %d coefficient %d is %d, want %d", c, i, d.coef[c][i], want)
			}
		}
		undelta(d.coef[c][4032:])
	}
	d.component(&d.planes[0], &d.coef[0], &q, false)
	for i, want := range v.ll3 {
		if int(d.work[4032+i]) != want {
			t.Fatalf("LL3 %d dequantized is %d, want %d", i, d.work[4032+i], want)
		}
	}
	for i, want := range v.l2 {
		if int(d.l3[i]) != want {
			t.Fatalf("level 3 inverted, sample %d, is %d, want %d", i, d.l3[i], want)
		}
	}
	quant := [3]rfxQuant{q, q, q}
	d.planesOf(&d.coef, &quant, false)
	for c := range 3 {
		differ := 0
		for i, want := range v.planes[c] {
			diff := int(d.planes[c][i]) - want
			if diff < -2 || diff > 2 {
				t.Fatalf("component %d sample %d is %d, want %d within 2", c, i, d.planes[c][i], want)
			}
			if diff != 0 {
				differ++
			}
		}
		t.Logf("component %d: %d of 4096 samples differ, within 2/32", c, differ)
	}

	// Through the graphics pipeline, onto a surface and the output.
	g := newGFXTest(t, 64, 64)
	g.must(t, surfacePDUs(1, 64, 64, image.Point{})...)
	g.must(t, rdptest.WireToSurface1(1, rdptest.CodecRemoteFX, rdptest.PixelXRGB, image.Rect(0, 0, 64, 64), v.tileset))
	scr := g.out.snapshot()
	differ := 0
	for i, want := range v.xrgb {
		got := scr.RGBAAt(i%64, i/64)
		w := color.RGBA{byte(want >> 16), byte(want >> 8), byte(want), 0xff}
		if !nearRGBA(got, w, 1) {
			t.Fatalf("pixel (%d, %d) is %v, want %v within 1", i%64, i/64, got, w)
		}
		if got != w {
			differ++
		}
	}
	t.Logf("%d of 4096 pixels differ, within 1", differ)
	if n := g.c.Stats().Bitmaps["gfx remotefx"]; n != 1 {
		t.Errorf("%d RemoteFX tiles counted", n)
	}
}

// nearRGBA reports whether each channel of a and b is within tol.
func nearRGBA(a, b color.RGBA, tol int) bool {
	d := func(x, y byte) bool { return int(x)-int(y) <= tol && int(y)-int(x) <= tol }
	return d(a.R, b.R) && d(a.G, b.G) && d(a.B, b.B) && a.A == b.A
}

// [MS-RDPEGFX] 4.1.2's example: a component of 14 coefficients, HL's 5,
// LH's 5 and LL's 4, sent at 25%, 50% and 100% of quality.
var (
	exampleBands = [rfxBands]rfxBand{
		bandHL1: {0, 5, 1}, bandLH1: {5, 5, 1}, bandHH1: {14, 0, 0},
		bandHL2: {14, 0, 0}, bandLH2: {14, 0, 0}, bandHH2: {14, 0, 0},
		bandHL3: {14, 0, 0}, bandLH3: {14, 0, 0}, bandHH3: {14, 0, 0},
		bandLL3: {10, 4, 1},
	}
	exampleLayout = func() *[rdptest.Bands]rdptest.Band {
		var l [rdptest.Bands]rdptest.Band
		for i, b := range exampleBands {
			l[i] = rdptest.Band{Off: b.off, W: b.w, H: b.h}
		}
		return &l
	}()
	example25  = rfxQuant{bandHL1: 4, bandLH1: 3, bandLL3: 2}
	example50  = rfxQuant{bandHL1: 0, bandLH1: 1, bandLL3: 2}
	example100 = rfxQuant{}
	exampleF1  = []int16{-34, -13, 15, -3, 0, 0, -7, 5, -3, 9, 15, 17, -9, 12}
	exampleF2  = []int16{-2, -2, 3, -3, 1, 3, -7, 5, -2, 1, 24, 13, 18, 5}
)

// The decoder's passes reproduce [MS-RDPEGFX] 4.1.2.2 exactly: each
// pass's DecDwtQ and Sign from the bytes of 4.1.2.1, the first pass of
// the difference tile of 4.1.2.2.3 summing its LL3 differences as
// 3.3.8.2.1.1 says. And rdptest's encoder reproduces those bytes,
// but for one pass: the SRL bytes that 4.1.2.1.4 gives for frame 2's
// upgrade to 50% leave out HL's last value, 1, which its table of SRL
// entries and 4.1.2.2.4's decode have, so that pass takes the encoder's
// bytes, which decode to that table.
func TestProgressiveSpecExample(t *testing.T) {
	type pass struct {
		name      string
		first     bool
		diff      bool
		prev, pos rfxQuant
		rlgr      []byte // a first pass's
		srl, raw  []byte // an upgrade's
		coef      []int16
		sign      []int8
	}
	unhex := func(s string) []byte {
		b, err := hex.DecodeString(s)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	passes := []pass{
		{name: "frame 1 at 25%", first: true, pos: example25, rlgr: unhex("a8626dfff700"),
			coef: []int16{-32, 0, 0, 0, 0, 0, 0, 0, 0, 8, 12, 16, -12, 12},
			sign: []int8{-1, 0, 0, 0, 0, 0, 0, 0, 0, 1}},
		{name: "frame 1 at 50%", prev: example25, pos: example50, srl: unhex("a0018000c949e0"), raw: unhex("20"),
			coef: []int16{-34, -13, 15, -3, 0, 0, -6, 4, -2, 8, 12, 16, -12, 12},
			sign: []int8{-1, -1, 1, -1, 0, 0, -1, 1, -1, 1}},
		{name: "frame 2 at 25%", first: true, diff: true, pos: example25, rlgr: unhex("8876bdfffef2"),
			coef: []int16{-2, -13, 15, -3, 0, 0, -6, 4, -2, 8, 24, 12, 16, 4},
			sign: []int8{1, 0, 0, 0, 0, 0, 0, 0, 0, 0}},
		{name: "frame 2 at 50%", prev: example25, pos: example50, srl: nil /* the encoder's */, raw: unhex("00"),
			coef: []int16{-2, -2, 3, -3, 1, 2, -6, 4, -2, 2, 24, 12, 16, 4},
			sign: []int8{1, 1, -1, 0, 1, 1, 0, 0, 0, -1}},
		{name: "frame 2 at 100%", prev: example50, pos: example100, srl: unhex("b0"), raw: unhex("c640"),
			coef: []int16{-2, -2, 3, -3, 1, 3, -7, 5, -2, 1, 24, 13, 18, 5},
			sign: []int8{1, 1, -1, 0, 1, 1, -1, 1, 0, -1}},
	}
	var coef, v [4096]int16
	var sign [4096]int8
	// The encoder's state: what the client has, what the passes since
	// the first sent, and what remains.
	ref, das, drs := make([]int16, 14), make([]int16, 14), make([]int16, 14)
	frames := [][]int16{exampleF1, exampleF2}
	frame := -1
	for _, p := range passes {
		pos, prev := rdptest.Quant(p.pos), rdptest.Quant(p.prev)
		var dts []int16
		if p.first {
			frame++
			sb := slices.Clone(frames[frame])
			if p.diff {
				for i := range sb {
					sb[i] -= ref[i]
				}
			} else {
				clear(ref)
			}
			var data []byte
			data, dts = rdptest.ProgFirst(sb, &pos, exampleLayout)
			if !bytes.Equal(data, p.rlgr) {
				t.Errorf("%s: encoded as % x, want % x", p.name, data, p.rlgr)
			}
			copy(das, dts)
			for i := range drs {
				drs[i] = sb[i] - dts[i]
			}
			if err := rlgrDecode(rlgr1, p.rlgr, v[:14]); err != nil {
				t.Fatal(err)
			}
			undelta(v[10:14])
			progFirstPass(&coef, &v, &sign, &p.pos, &exampleBands, p.diff)
		} else {
			var srlData, raw []byte
			srlData, raw, dts = rdptest.ProgUpgrade(drs, das, &prev, &pos, exampleLayout)
			if p.srl != nil && !bytes.Equal(srlData, p.srl) {
				t.Errorf("%s: SRL encoded as % x, want % x", p.name, srlData, p.srl)
			}
			if !bytes.Equal(raw, p.raw) {
				t.Errorf("%s: raw bits encoded as % x, want % x", p.name, raw, p.raw)
			}
			if p.srl == nil {
				p.srl = srlData
			}
			for i := range das {
				das[i] += dts[i]
				drs[i] -= dts[i]
			}
			if err := progUpgradePass(&coef, &sign, &p.prev, &p.pos, &exampleBands, p.srl, p.raw); err != nil {
				t.Fatal(err)
			}
		}
		for i := range ref {
			ref[i] += dts[i]
		}
		if !slices.Equal(coef[:14], p.coef) {
			t.Errorf("%s: DecDwtQ %v, want %v", p.name, coef[:14], p.coef)
		}
		if !slices.Equal(sign[:10], p.sign) {
			t.Errorf("%s: Sign %v, want %v", p.name, sign[:10], p.sign)
		}
		if !slices.Equal(ref, p.coef) {
			t.Errorf("%s: the encoder's reference %v, want %v", p.name, ref, p.coef)
		}
	}
	// The SRL bytes that 4.1.2.1.4 gives decode otherwise.
	var c [4096]int16
	var s [4096]int8
	copy(c[:], passes[2].coef)
	copy(s[:], passes[2].sign)
	if err := progUpgradePass(&c, &s, &example25, &example50, &exampleBands, unhex("800700169c"), unhex("00")); err != nil {
		t.Fatal(err)
	}
	if slices.Equal(c[:14], passes[3].coef) {
		t.Errorf("4.1.2.1.4's SRL bytes decode to its table; take them in the test")
	}
}

// refDecode decodes a tile's components, quantized as q, in floating
// point: the dequantization's scale, the lifting equations without
// their rounding, and the conversion's matrix with all its terms.
func refDecode(coef *[3][4096]int16, q *[3]rfxQuant, extra bool) *image.RGBA {
	lay := rdptest.Layout(extra)
	var comp [3][]float64
	for c := range 3 {
		var x [4096]float64
		for b, band := range lay {
			for i := band.Off; i < band.Off+band.W*band.H; i++ {
				x[i] = float64(coef[c][i]) * math.Ldexp(1, int(q[c][b])-6)
			}
		}
		band := func(k int) []float64 { return x[lay[k].Off : lay[k].Off+lay[k].W*lay[k].H] }
		ll := band(rdptest.LL3)
		for level := 2; level >= 0; level-- {
			hl, lh, hh := band(3*level+rdptest.HL1), band(3*level+rdptest.LH1), band(3*level+rdptest.HH1)
			wl, wh, hL, hH := lay[3*level+rdptest.LH1].W, lay[3*level+rdptest.HL1].W, lay[3*level+rdptest.HL1].H, lay[3*level+rdptest.LH1].H
			ll = refLevel(ll, hl, lh, hh, wl, wh, hL, hH, extra && level == 0)
		}
		comp[c] = ll
	}
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	cl := func(v float64) byte { return byte(min(max(math.Round(v), 0), 255)) }
	for i := range 4096 {
		y, cb, cr := comp[0][i]+128, comp[1][i], comp[2][i]
		img.SetRGBA(i%64, i/64, color.RGBA{cl(y + 1.402525*cr), cl(y - 0.343730*cb - 0.714401*cr), cl(y + 1.769905*cb + 0.000013*cr), 0xff})
	}
	return img
}

// refLevel inverts a level in floating point, rows then columns; with
// extra, each line takes a zero high coefficient and drops its last
// sample.
func refLevel(ll, hl, lh, hh []float64, wl, wh, hL, hH int, extra bool) []float64 {
	w, h := wl+wh, hL+hH
	rows := make([]float64, w*h)
	pad := 0
	if extra {
		pad = 1
	}
	for y := range h {
		var l, hi []float64
		if y < hL {
			l, hi = ll[y*wl:][:wl], hl[y*wh:][:wh]
		} else {
			l, hi = lh[(y-hL)*wl:][:wl], hh[(y-hL)*wh:][:wh]
		}
		copy(rows[y*w:], refLine(l, append(slices.Clone(hi), make([]float64, pad)...))[:w])
	}
	out := make([]float64, w*h)
	for col := range w {
		l, hi := make([]float64, hL), make([]float64, hH+pad)
		for y := range hL {
			l[y] = rows[y*w+col]
		}
		for y := range hH {
			hi[y] = rows[(hL+y)*w+col]
		}
		for y, v := range refLine(l, hi)[:h] {
			out[y*w+col] = v
		}
	}
	return out
}

func refLine(l, h []float64) []float64 {
	x := make([]float64, len(l)+len(h))
	for n := range l {
		x[2*n] = l[n] - (h[max(n-1, 0)]+h[min(n, len(h)-1)])/2
	}
	for n := range h {
		next := x[2*n]
		if 2*n+2 < len(x) {
			next = x[2*n+2]
		}
		x[2*n+1] = 2*h[n] + (x[2*n]+next)/2
	}
	return x
}

// The quantization tables that xrdp sends, in RFX_COMPONENT_CODEC_QUANT
// (progressive) order, and others.
var (
	xrdpQuant = [3]rdptest.Quant{
		progQuantOf(0x66, 0x66, 0x77, 0x87, 0x98),
		progQuantOf(0x76, 0x77, 0x88, 0x98, 0x99),
		progQuantOf(0x76, 0x77, 0x88, 0x98, 0x99),
	}
	fineQuant   = [3]rdptest.Quant{uniformQuant(6), uniformQuant(6), uniformQuant(6)}
	coarseQuant = [3]rdptest.Quant{uniformQuant(12), uniformQuant(13), uniformQuant(15)}
	// asymQuant sets each level's HL and LH bands apart, so that the
	// order of the bands in a table matters, which xrdp's, equal at
	// every level, leave untested. It is built by band, not parsed.
	asymQuant = [3]rdptest.Quant{
		{rdptest.HL1: 6, rdptest.LH1: 9, rdptest.HH1: 9, rdptest.HL2: 7, rdptest.LH2: 10, rdptest.HH2: 9, rdptest.HL3: 6, rdptest.LH3: 8, rdptest.HH3: 8, rdptest.LL3: 6},
		{rdptest.HL1: 10, rdptest.LH1: 7, rdptest.HH1: 10, rdptest.HL2: 9, rdptest.LH2: 6, rdptest.HH2: 9, rdptest.HL3: 8, rdptest.LH3: 6, rdptest.HH3: 8, rdptest.LL3: 6},
		{rdptest.HL1: 6, rdptest.LH1: 9, rdptest.HH1: 9, rdptest.HL2: 7, rdptest.LH2: 10, rdptest.HH2: 9, rdptest.HL3: 6, rdptest.LH3: 8, rdptest.HH3: 8, rdptest.LL3: 6},
	}
)

func progQuantOf(b ...byte) rdptest.Quant {
	q := parseQuant(b, &progQuantOrder)
	return rdptest.Quant(q)
}

func uniformQuant(v uint8) rdptest.Quant {
	var q rdptest.Quant
	for i := range q {
		q[i] = v
	}
	return q
}

// testImage is a tile's worth of content of each kind: smooth, sharp
// edges and text-like strokes, and noise.
func testImage(kind int, seed uint64) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	rng := rand.New(rand.NewPCG(seed, uint64(kind)))
	for y := range 64 {
		for x := range 64 {
			var c color.RGBA
			switch kind {
			case 0:
				c = color.RGBA{byte(x * 4), byte(y * 4), byte(255 - (x+y)*2), 0xff}
			case 1:
				c = color.RGBA{0xf0, 0xf0, 0xf0, 0xff}
				if (x/3+y/5)%4 == 0 || x%17 == 3 {
					c = color.RGBA{0x10, 0x20, 0x80, 0xff}
				}
			default:
				c = color.RGBA{byte(rng.IntN(256)), byte(rng.IntN(256)), byte(rng.IntN(256)), 0xff}
			}
			img.SetRGBA(x, y, c)
		}
	}
	return img
}

// The fixed-point decoder stays within a level per channel of the
// decoder in floating point, on tiles that rdptest encodes as servers
// do, of every kind of content, in the original and reduce-extrapolate
// transforms, at fine, xrdp's and coarse quantization.
func TestRFXFloatReference(t *testing.T) {
	hist := map[int]int{}
	for _, extra := range []bool{false, true} {
		for qi, q := range [][3]rdptest.Quant{fineQuant, xrdpQuant, coarseQuant} {
			for kind := range 3 {
				img := testImage(kind, uint64(qi))
				coef := rdptest.EncodeTile(img, image.Point{}, &q, extra)
				var d rfxDecoder
				quant := [3]rfxQuant{rfxQuant(q[0]), rfxQuant(q[1]), rfxQuant(q[2])}
				d.planesOf(coef, &quant, extra)
				want := refDecode(coef, &quant, extra)
				for i := range 4096 {
					r, g, b := rgbOf(d.planes[0][i], d.planes[1][i], d.planes[2][i])
					w := want.RGBAAt(i%64, i/64)
					m := max(absInt(int(r)-int(w.R)), absInt(int(g)-int(w.G)), absInt(int(b)-int(w.B)))
					hist[m]++
					if m > 1 {
						t.Fatalf("extra %v, quantization %d, content %d: pixel %d is %d %d %d, want %v within 1", extra, qi, kind, i, r, g, b, w)
					}
				}
			}
		}
	}
	t.Logf("differences from floating point: %v", hist)
}

func absInt(v int) int { return max(v, -v) }

// maxDiff is the largest difference of a channel between a and b, of
// a's size.
func maxDiff(a, b *image.RGBA) int {
	worst := 0
	for y := range a.Rect.Dy() {
		for x := range a.Rect.Dx() {
			p, q := a.RGBAAt(x, y), b.RGBAAt(x, y)
			worst = max(worst, absInt(int(p.R)-int(q.R)), absInt(int(p.G)-int(q.G)), absInt(int(p.B)-int(q.B)))
		}
	}
	return worst
}

// Tiles that rdptest encodes decode, through the graphics pipeline,
// within a level of the decoder in floating point and within what fine
// quantization loses of the image, in RLGR1 and RLGR3, with the
// region's rectangles clipping the tiles and the tiles placed from the
// PDU's rectangle.
func TestRFXRoundTrip(t *testing.T) {
	for _, mode := range []int{rdptest.RLGR1, rdptest.RLGR3} {
		g := newGFXTest(t, 256, 192)
		g.must(t, surfacePDUs(1, 256, 192, image.Point{})...)
		g.must(t, rdptest.SolidFill(1, red, image.Rect(0, 0, 256, 192)))
		img := image.NewRGBA(image.Rect(0, 0, 128, 64))
		for i, kind := range []int{0, 1} {
			src := testImage(kind, 1)
			for y := range 64 {
				for x := range 64 {
					img.SetRGBA(64*i+x, y, src.RGBAAt(x, y))
				}
			}
		}
		// The first tile at fine quantization, the second at tables
		// whose HL and LH bands differ, which pin the tables' band order.
		quants := []rdptest.Quant{fineQuant[0], uniformQuant(9), asymQuant[0], asymQuant[1], asymQuant[2]}
		var tiles []rdptest.RFXTile
		var ref [2]*image.RGBA
		for x, q := range []*[3]rdptest.Quant{&fineQuant, &asymQuant} {
			coef := rdptest.EncodeTile(img, image.Pt(64*x, 0), q, false)
			ref[x] = refDecode(coef, &[3]rfxQuant{rfxQuant(q[0]), rfxQuant(q[1]), rfxQuant(q[2])}, false)
			tile := rdptest.RFXTile{X: x, Quant: [3]int{0, 0, 0}}
			if x == 1 {
				tile.Quant = [3]int{2, 3, 4}
			}
			for c := range 3 {
				tile.Data[c] = rdptest.RLGR(mode, rdptest.Deltas(coef[c][:], rdptest.Layout(false)[rdptest.LL3]))
			}
			tiles = append(tiles, tile)
		}
		// The PDU's rectangle at (32, 16); the region leaves out a strip
		// of the first tile and the second's right half.
		dr := image.Rect(32, 16, 32+128, 16+64)
		rects := []image.Rectangle{image.Rect(0, 0, 64, 40), image.Rect(0, 48, 64, 64), image.Rect(64, 0, 96, 64)}
		g.must(t, gfxFrame(1, rdptest.WireToSurface1(1, rdptest.CodecRemoteFX, rdptest.PixelXRGB, dr, rdptest.RemoteFX(mode, rects, quants, tiles))))
		scr := g.out.snapshot()
		worst := 0
		for y := range 64 {
			for x := range 128 {
				got := scr.RGBAAt(32+x, 16+y)
				in := (x < 64 && (y < 40 || y >= 48)) || (x >= 64 && x < 96)
				if !in {
					if got != red {
						t.Fatalf("mode %d: pixel (%d, %d) outside the region is %v", mode, x, y, got)
					}
					continue
				}
				if w := ref[x/64].RGBAAt(x%64, y); !nearRGBA(got, w, 1) {
					t.Fatalf("mode %d: pixel (%d, %d) is %v, want %v within 1", mode, x, y, got, w)
				}
				if x < 64 {
					w := img.RGBAAt(x, y)
					worst = max(worst, absInt(int(got.R)-int(w.R)), absInt(int(got.G)-int(w.G)), absInt(int(got.B)-int(w.B)))
				}
			}
		}
		// What fine quantization loses, in the first tile.
		if worst > 6 {
			t.Errorf("mode %d: decoded within %d of the image, want 6", mode, worst)
		}
		wantRect(t, scr, image.Rect(0, 0, 256, 16), red)
		if n := g.c.Stats().Bitmaps["gfx remotefx"]; n != 2 {
			t.Errorf("%d RemoteFX tiles counted", n)
		}
	}
}

// A region with no rectangles is the PDU's rectangle, a tile set
// without a region takes it too, and a region's rectangles are clipped
// to it.
func TestRFXRegionBounds(t *testing.T) {
	img := testImage(0, 0)
	coef := rdptest.EncodeTile(img, image.Point{}, &fineQuant, false)
	var tile rdptest.RFXTile
	for c := range 3 {
		tile.Data[c] = rdptest.RLGR(rdptest.RLGR1, rdptest.Deltas(coef[c][:], rdptest.Layout(false)[rdptest.LL3]))
	}
	msg := rdptest.RemoteFX(rdptest.RLGR1, nil, []rdptest.Quant{fineQuant[0]}, []rdptest.RFXTile{tile})
	// The tile set alone: FRAME_BEGIN is 14 bytes, the region of no
	// rectangles 15.
	alone := msg[14+15:]
	past := rdptest.RemoteFX(rdptest.RLGR1, []image.Rectangle{image.Rect(0, 0, 100, 100)}, []rdptest.Quant{fineQuant[0]}, []rdptest.RFXTile{tile})
	for name, m := range map[string][]byte{"empty region": msg, "no region": alone, "region past the rectangle": past} {
		g := newGFXTest(t, 128, 128)
		g.must(t, surfacePDUs(1, 128, 128, image.Point{})...)
		g.must(t, rdptest.SolidFill(1, red, image.Rect(0, 0, 128, 128)))
		g.must(t, rdptest.WireToSurface1(1, rdptest.CodecRemoteFX, rdptest.PixelXRGB, image.Rect(10, 10, 50, 40), m))
		scr := g.out.snapshot()
		for y := range 128 {
			for x := range 128 {
				got := scr.RGBAAt(x, y)
				if !image.Pt(x, y).In(image.Rect(10, 10, 50, 40)) {
					if got != red {
						t.Fatalf("%s: pixel (%d, %d) outside the PDU's rectangle is %v", name, x, y, got)
					}
					continue
				}
				if !nearRGBA(got, img.RGBAAt(x-10, y-10), 3) {
					t.Fatalf("%s: pixel (%d, %d) is %v, want %v", name, x, y, got, img.RGBAAt(x-10, y-10))
				}
			}
		}
	}
}

// progEnc encodes a tile progressively, as a server does, keeping what
// the client has of it ([MS-RDPEGFX] 3.2.8.1.5.3), what the passes
// since the first sent, and what remains.
type progEnc struct {
	lay           *[rdptest.Bands]rdptest.Band
	x, y          int
	ref, das, drs [3][]int16
	pos           [3]rdptest.Quant
}

func newProgEnc(x, y int, extra bool) *progEnc {
	e := &progEnc{lay: rdptest.Layout(extra), x: x, y: y}
	for c := range 3 {
		e.ref[c], e.das[c], e.drs[c] = make([]int16, 4096), make([]int16, 4096), make([]int16, 4096)
	}
	return e
}

// first is the tile's first pass of coef, the difference from what the
// client has if diff, at the quality quality of the bit positions pos,
// all of it if quality is 0xff, simple a TILE_SIMPLE.
func (e *progEnc) first(coef *[3][4096]int16, diff, simple bool, quality int, pos [3]rdptest.Quant) []byte {
	tile := rdptest.ProgTile{Type: rdptest.ProgTileFirst, Quant: [3]int{0, 1, 2}, X: e.x, Y: e.y, Diff: diff, Quality: quality}
	if simple {
		tile.Type = rdptest.ProgTileSimple
	}
	for c := range 3 {
		sb := slices.Clone(coef[c][:])
		if diff {
			for i := range sb {
				sb[i] -= e.ref[c][i]
			}
		} else {
			clear(e.ref[c])
		}
		data, dts := rdptest.ProgFirst(sb, &pos[c], e.lay)
		tile.Data[c] = data
		for i := range sb {
			e.das[c][i] = dts[i]
			e.drs[c][i] = sb[i] - dts[i]
			e.ref[c][i] += dts[i]
		}
	}
	e.pos = pos
	return tile.Block()
}

// upgrade is an upgrade pass to the quality quality of the bit
// positions pos.
func (e *progEnc) upgrade(quality int, pos [3]rdptest.Quant) []byte {
	tile := rdptest.ProgTile{Type: rdptest.ProgTileUpgrade, Quant: [3]int{0, 1, 2}, X: e.x, Y: e.y, Quality: quality}
	for c := range 3 {
		srl, raw, dts := rdptest.ProgUpgrade(e.drs[c], e.das[c], &e.pos[c], &pos[c], e.lay)
		tile.SRL[c], tile.Raw[c] = srl, raw
		for i := range dts {
			e.das[c][i] += dts[i]
			e.drs[c][i] -= dts[i]
			e.ref[c][i] += dts[i]
		}
	}
	e.pos = pos
	return tile.Block()
}

// The qualities of the progressive tests: the bit positions of each
// band, by component.
var progQualities = []rdptest.ProgQuant{
	{Quality: 25, BitPos: [3]rdptest.Quant{bitPos(4, 3), bitPos(5, 3), bitPos(5, 3)}},
	{Quality: 50, BitPos: [3]rdptest.Quant{bitPos(2, 1), bitPos(2, 2), bitPos(2, 2)}},
	{Quality: 75, BitPos: [3]rdptest.Quant{bitPos(1, 0), bitPos(1, 1), bitPos(1, 0)}},
}

// bitPos is the bit positions hi of the first level's bands and lo of
// the others.
func bitPos(hi, lo uint8) rdptest.Quant {
	q := uniformQuant(lo)
	q[rdptest.HL1], q[rdptest.LH1], q[rdptest.HH1] = hi, hi, hi
	return q
}

var fullPass = [3]rdptest.Quant{}

// progImageTolerance is how far from testImage of each kind its tile
// decodes at xrdp's quantization: what the quantization loses.
var progImageTolerance = [3]int{8, 34, 40}

// progSurface is a client with a surface of w×h at the origin, and the
// output, to which W2S2 PDUs of the codec context ctx go.
type progSurface struct {
	*gfxTest
	frame  uint32
	quants []rdptest.Quant // the regions' quantization tables, xrdp's if nil
}

func newProgSurface(t testing.TB, w, h int) *progSurface {
	g := newGFXTest(t, w, h)
	g.must(t, surfacePDUs(1, w, h, image.Point{})...)
	return &progSurface{gfxTest: g}
}

// send sends a frame of a region of the tiles, its rectangles rects,
// or the surface if there are none, in the codec context ctx.
func (p *progSurface) send(ctx uint32, extra bool, rects []image.Rectangle, tiles ...[]byte) error {
	if rects == nil {
		rects = []image.Rectangle{p.c.gfx.surfaces[1].bounds()}
	}
	p.frame++
	quants := p.quants
	if quants == nil {
		quants = xrdpQuant[:]
	}
	region := rdptest.ProgRegion{Rects: rects, Quants: quants, ProgQuants: progQualities, Extrapolate: extra, Tiles: tiles}
	stream := append(rdptest.ProgSyncContext(), rdptest.Progressive(p.frame, region)...)
	return p.gfxTest.send(gfxFrame(p.frame, rdptest.WireToSurface2(1, rdptest.CodecProgressive, ctx, rdptest.PixelXRGB, stream)))
}

func (p *progSurface) must(t testing.TB, ctx uint32, extra bool, rects []image.Rectangle, tiles ...[]byte) {
	t.Helper()
	if err := p.send(ctx, extra, rects, tiles...); err != nil {
		t.Fatal(err)
	}
}

// tile is the client's state of the tile at x, y of the surface.
func (p *progSurface) tile(t testing.TB, x, y int) *progTile {
	t.Helper()
	st := p.c.gfx.surfaces[1].prog
	if st == nil || st.tiles[y*st.cols+x] == nil {
		t.Fatalf("no tile %d,%d", x, y)
	}
	return st.tiles[y*st.cols+x]
}

// wantTile checks that the client's tile at x, y is what e says the
// client has, and its signs those of what the passes since the first
// sent, while it has upgrades to come.
func (p *progSurface) wantTile(t testing.TB, e *progEnc, upgrades bool) {
	t.Helper()
	tile := p.tile(t, e.x, e.y)
	for c := range 3 {
		if !slices.Equal(tile.coef[c][:], e.ref[c]) {
			t.Fatalf("tile %d,%d component %d differs from the encoder's reference", e.x, e.y, c)
		}
	}
	if !upgrades {
		if tile.sign != nil {
			t.Fatalf("tile %d,%d keeps signs after a full pass", e.x, e.y)
		}
		return
	}
	if tile.sign == nil {
		t.Fatalf("tile %d,%d has no signs", e.x, e.y)
	}
	for c := range 3 {
		for i, v := range e.das[c][:e.lay[rdptest.LL3].Off] {
			if tile.sign[c][i] != sgn(int32(v)) {
				t.Fatalf("tile %d,%d component %d coefficient %d: sign %d, sent %d", e.x, e.y, c, i, tile.sign[c][i], v)
			}
		}
	}
}

// screenTile is the 64×64 of the output at x, y in tiles.
func screenTile(scr *image.RGBA, x, y int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for j := range 64 {
		copy(img.Pix[j*img.Stride:][:4*64], scr.Pix[scr.PixOffset(64*x, 64*y+j):][:4*64])
	}
	return img
}

// A tile sent in passes, as an original tile and then as a difference
// tile, decodes after each pass to what the encoder says the client
// has, with the signs of what it sent, and after the last to exactly
// what the tile sent whole in one pass decodes to, and to within a
// level of the decoder in floating point; in the original and the
// reduce-extrapolate transforms, at xrdp's quantization and at tables
// whose HL and LH bands differ, which pin the tables' band order.
func TestProgressivePasses(t *testing.T) {
	for qi, quant := range []*[3]rdptest.Quant{&xrdpQuant, &asymQuant} {
		for _, extra := range []bool{false, true} {
			progPasses(t, quant, qi == 0, extra)
		}
	}
}

// progPasses is TestProgressivePasses at the tables quant, xrdp's if
// xrdp, in the transform extra.
func progPasses(t *testing.T, quant *[3]rdptest.Quant, xrdp, extra bool) {
	p := newProgSurface(t, 128, 64)
	whole := newProgSurface(t, 128, 64)
	p.quants, whole.quants = quant[:], quant[:]
	byBand := [3]rfxQuant{rfxQuant(quant[0]), rfxQuant(quant[1]), rfxQuant(quant[2])}
	e := newProgEnc(1, 0, extra)
	for frame, kind := range []int{0, 1, 2} {
		img := testImage(kind, 7)
		coef := rdptest.EncodeTile(img, image.Point{}, quant, extra)
		diff := frame > 0
		p.must(t, 7, extra, nil, e.first(coef, diff, false, 0, progQualities[0].BitPos))
		p.wantTile(t, e, true)
		p.must(t, 7, extra, nil, e.upgrade(1, progQualities[1].BitPos))
		p.wantTile(t, e, true)
		p.must(t, 7, extra, nil, e.upgrade(2, progQualities[2].BitPos))
		p.wantTile(t, e, true)
		p.must(t, 7, extra, nil, e.upgrade(progFull, fullPass))
		p.wantTile(t, e, false)
		for c := range 3 {
			if !slices.Equal(e.ref[c], coef[c][:]) {
				t.Fatalf("the encoder's reference is not the tile after the full pass")
			}
		}
		w := newProgEnc(1, 0, extra)
		whole.must(t, 3, extra, nil, w.first(coef, false, true, progFull, fullPass))
		got, want := screenTile(p.out.snapshot(), 1, 0), screenTile(whole.out.snapshot(), 1, 0)
		if !bytes.Equal(got.Pix, want.Pix) {
			t.Fatalf("extra %v, frame %d: the passes decode otherwise than the whole tile", extra, frame)
		}
		if d := maxDiff(got, refDecode(coef, &byBand, extra)); d > 1 {
			t.Fatalf("xrdp's tables %v, extra %v, frame %d: decoded within %d of the decoder in floating point, want 1", xrdp, extra, frame, d)
		}
		if d := maxDiff(got, img); xrdp && d > progImageTolerance[kind] {
			t.Fatalf("extra %v, frame %d: decoded within %d of the image, want %d", extra, frame, d, progImageTolerance[kind])
		}
	}
	st := p.c.Stats()
	if st.Bitmaps["gfx progressive"] != 3 || st.Bitmaps["gfx progressive difference"] != 2 || st.Bitmaps["gfx progressive upgrade"] != 9 {
		t.Errorf("stats %+v", st.Bitmaps)
	}
	wantRect(t, p.out.snapshot(), image.Rect(0, 0, 64, 64), black)
}

// A difference tile sent whole, as TILE_SIMPLE or TILE_FIRST of full
// quality, adds to what the client has, and leaves no signs.
func TestProgressiveDifferenceSimple(t *testing.T) {
	p := newProgSurface(t, 64, 64)
	whole := newProgSurface(t, 64, 64)
	e := newProgEnc(0, 0, false)
	a := rdptest.EncodeTile(testImage(0, 1), image.Point{}, &xrdpQuant, false)
	b := rdptest.EncodeTile(testImage(1, 1), image.Point{}, &xrdpQuant, false)
	p.must(t, 1, false, nil, e.first(a, false, false, 0, progQualities[0].BitPos))
	p.must(t, 1, false, nil, e.first(b, true, true, progFull, fullPass))
	p.wantTile(t, e, false)
	p.must(t, 1, false, nil, e.first(a, true, false, progFull, fullPass))
	p.wantTile(t, e, false)
	whole.must(t, 1, false, nil, newProgEnc(0, 0, false).first(a, false, true, progFull, fullPass))
	if !bytes.Equal(p.out.snapshot().Pix, whole.out.snapshot().Pix) {
		t.Fatal("the difference tiles decode otherwise than the whole tile")
	}
}

// A region's tiles are drawn only where its rectangles cover them, and
// a tile that an earlier region changed is not drawn again for a later
// one's rectangles.
func TestProgressiveRegionClip(t *testing.T) {
	p := newProgSurface(t, 128, 64)
	p.must(t, 1, false, nil) // the context
	p.gfxTest.must(t, rdptest.SolidFill(1, red, image.Rect(0, 0, 128, 64)))
	img := testImage(0, 0)
	coef := rdptest.EncodeTile(img, image.Point{}, &fineQuant, false)
	e0, e1 := newProgEnc(0, 0, false), newProgEnc(1, 0, false)
	p.must(t, 1, false, []image.Rectangle{image.Rect(8, 8, 40, 30), image.Rect(70, 0, 80, 64)},
		e0.first(coef, false, true, progFull, fullPass), e1.first(coef, false, true, progFull, fullPass))
	scr := p.out.snapshot()
	for y := range 64 {
		for x := range 128 {
			in := image.Pt(x, y).In(image.Rect(8, 8, 40, 30)) || image.Pt(x, y).In(image.Rect(70, 0, 80, 64))
			got := scr.RGBAAt(x, y)
			switch {
			case !in && got != red:
				t.Fatalf("pixel (%d, %d) outside the rectangles is %v", x, y, got)
			case in && got == red:
				t.Fatalf("pixel (%d, %d) within the rectangles is not drawn", x, y)
			}
		}
	}
	// A frame of two regions: the first changes tile 0 within its
	// rectangle; the second, whose rectangle covers all, changes tile 1
	// only, so tile 0 keeps red outside the first's.
	p.gfxTest.must(t, rdptest.SolidFill(1, red, image.Rect(0, 0, 128, 64)))
	p.frame++
	first := rdptest.ProgRegion{Rects: []image.Rectangle{image.Rect(0, 0, 10, 10)}, Quants: xrdpQuant[:], Tiles: [][]byte{e0.first(coef, false, true, progFull, fullPass)}}
	second := rdptest.ProgRegion{Rects: []image.Rectangle{image.Rect(0, 0, 128, 64)}, Quants: xrdpQuant[:], Tiles: [][]byte{e1.first(coef, false, true, progFull, fullPass)}}
	p.gfxTest.must(t, gfxFrame(p.frame, rdptest.WireToSurface2(1, rdptest.CodecProgressive, 1, rdptest.PixelXRGB, rdptest.Progressive(p.frame, first, second))))
	scr = p.out.snapshot()
	if scr.RGBAAt(5, 5) == red || scr.RGBAAt(20, 20) != red || scr.RGBAAt(100, 20) == red {
		t.Fatalf("pixels %v %v %v", scr.RGBAAt(5, 5), scr.RGBAAt(20, 20), scr.RGBAAt(100, 20))
	}
	// On a surface of 100×50, a rectangle past it is clipped to it, and
	// so are the tiles of its last column and row: it shows what the
	// tiles draw on a surface of 128×64.
	tiles := [][]byte{newProgEnc(0, 0, false).first(coef, false, true, progFull, fullPass), newProgEnc(1, 0, false).first(coef, false, true, progFull, fullPass)}
	ref := newProgSurface(t, 128, 64)
	ref.must(t, 1, false, nil, tiles...)
	want := ref.out.snapshot()
	p = newProgSurface(t, 100, 50)
	p.must(t, 1, false, []image.Rectangle{image.Rect(0, 0, 128, 64)}, tiles...)
	scr = p.out.snapshot()
	for y := range 50 {
		for x := range 100 {
			if scr.RGBAAt(x, y) != want.RGBAAt(x, y) {
				t.Fatalf("pixel (%d, %d) of the small surface is %v, want %v", x, y, scr.RGBAAt(x, y), want.RGBAAt(x, y))
			}
		}
	}
}

// deleteContext is RDPGFX_DELETE_ENCODING_CONTEXT_PDU of surface 1's
// codec context ctx.
func deleteContext(ctx uint32) []byte {
	return rdptest.GFXPDU(rdptest.GFXDeleteEncodingCtx, binary.LittleEndian.AppendUint32([]byte{1, 0}, ctx))
}

// A codec context of another id ends the surface's, as deleting it
// does: the tiles keep their coefficients and give their signs back,
// and an upgrade of a tile whose first pass was in it has none.
// Deleting a context the surface does not have, a region outside a
// frame, and a W2S2 of another codec are ignored and counted; and
// deleting the surface lets its tiles go.
func TestProgressiveContexts(t *testing.T) {
	p := newProgSurface(t, 128, 64)
	coef := rdptest.EncodeTile(testImage(0, 0), image.Point{}, &fineQuant, false)
	e := newProgEnc(0, 0, false)
	p.must(t, 1, false, nil, e.first(coef, false, false, 0, progQualities[0].BitPos), newProgEnc(1, 0, false).first(coef, false, true, progFull, fullPass))
	if p.c.gfx.progTiles != 2 || len(p.c.gfx.signs) != 0 {
		t.Fatalf("%d tiles, %d spare signs", p.c.gfx.progTiles, len(p.c.gfx.signs))
	}
	// Another context: an upgrade of a tile of the old one has no first
	// pass, and the tiles stay, without their signs.
	kept := p.tile(t, 0, 0).coef
	if err := p.send(2, false, nil, e.upgrade(1, progQualities[1].BitPos)); err == nil || !strings.Contains(err.Error(), "no first pass") {
		t.Fatalf("upgrade in a new context: %v", err)
	}
	if st := p.c.gfx.surfaces[1].prog; p.c.gfx.progTiles != 2 || st.ctx != 2 || st.signed != 0 || len(p.c.gfx.signs) != 1 {
		t.Fatalf("%d tiles, context %d, %d with signs and %d spare after the context was replaced", p.c.gfx.progTiles, st.ctx, st.signed, len(p.c.gfx.signs))
	}
	if tile := p.tile(t, 0, 0); tile.coef != kept || tile.sign != nil {
		t.Fatal("the tile lost its coefficients, or kept its signs, with its codec context")
	}
	// A first pass in the new context has upgrades.
	p.must(t, 2, false, nil, e.first(coef, false, false, 0, progQualities[0].BitPos))
	p.must(t, 2, false, nil, e.upgrade(1, progQualities[1].BitPos))
	p.wantTile(t, e, true)
	p.gfxTest.must(t, deleteContext(9))
	if st := p.c.gfx.surfaces[1].prog; !st.live || st.signed != 1 {
		t.Fatal("deleting another context ended the surface's")
	}
	p.gfxTest.must(t, deleteContext(2))
	if st := p.c.gfx.surfaces[1].prog; st.live || st.signed != 0 || p.c.gfx.progTiles != 2 {
		t.Fatal("the deleted context stays")
	}
	// Deleted again, it is a context the surface does not have.
	p.gfxTest.must(t, deleteContext(2))
	if err := p.send(3, false, nil, e.upgrade(2, progQualities[2].BitPos)); err == nil || !strings.Contains(err.Error(), "no first pass") {
		t.Fatalf("upgrade after its context was deleted: %v", err)
	}
	// Regions outside a frame, before one and after one, in a new
	// context, which starts outside a frame, and another codec.
	region := rdptest.ProgRegion{Quants: xrdpQuant[:], Tiles: [][]byte{newProgEnc(0, 0, false).first(coef, false, true, progFull, fullPass)}}
	p.gfxTest.must(t, rdptest.WireToSurface2(1, rdptest.CodecProgressive, 4, rdptest.PixelXRGB, slices.Concat(region.Block(), rdptest.Progressive(1), region.Block())))
	p.gfxTest.must(t, rdptest.WireToSurface2(1, rdptest.CodecRemoteFX, 3, rdptest.PixelXRGB, nil))
	st := p.c.Stats()
	if st.Ignored["GFX deletes of no codec context"] != 2 || st.Ignored["GFX progressive regions outside a frame"] != 2 || st.Ignored["GFX bitmaps of unknown codecs"] != 1 || p.c.gfx.progTiles != 2 {
		t.Fatalf("ignored %+v, %d tiles", st.Ignored, p.c.gfx.progTiles)
	}
	p.gfxTest.must(t, rdptest.DeleteSurface(1))
	if p.c.gfx.progTiles != 0 {
		t.Fatalf("%d tiles after the surface went", p.c.gfx.progTiles)
	}
}

// The tiles' coefficients outlast their codec context ([MS-RDPEGFX]
// 3.3.1.3): after the context is deleted, or another replaces it, a
// difference tile adds to them, and decodes as the tile sent whole.
func TestProgressiveDifferenceAcrossContexts(t *testing.T) {
	a := rdptest.EncodeTile(testImage(0, 1), image.Point{}, &xrdpQuant, false)
	b := rdptest.EncodeTile(testImage(1, 1), image.Point{}, &xrdpQuant, false)
	whole := newProgSurface(t, 64, 64)
	whole.must(t, 1, false, nil, newProgEnc(0, 0, false).first(b, false, true, progFull, fullPass))
	want := whole.out.snapshot()
	for _, end := range []string{"delete", "switch"} {
		p := newProgSurface(t, 64, 64)
		e := newProgEnc(0, 0, false)
		p.must(t, 1, false, nil, e.first(a, false, true, progFull, fullPass))
		if end == "delete" {
			p.gfxTest.must(t, deleteContext(1))
		}
		p.must(t, 2, false, nil, e.first(b, true, true, progFull, fullPass))
		p.wantTile(t, e, false)
		if got := p.out.snapshot(); !bytes.Equal(got.Pix, want.Pix) {
			t.Fatalf("%s: the difference tile decodes %d off the tile sent whole", end, maxDiff(got, want))
		}
	}
}

// An upgrade, or a difference tile, in another transform than the
// tile's coefficients ends the session: the bands lie elsewhere, and
// the signs that a first pass set are another transform's.
func TestProgressiveTransformChange(t *testing.T) {
	var coef [2]*[3][4096]int16
	for i, extra := range []bool{false, true} {
		coef[i] = rdptest.EncodeTile(testImage(0, 0), image.Point{}, &fineQuant, extra)
	}
	for i, extra := range []bool{false, true} {
		p := newProgSurface(t, 64, 64)
		e := newProgEnc(0, 0, extra)
		p.must(t, 1, extra, nil, e.first(coef[i], false, false, 0, progQualities[0].BitPos))
		// The passes' bits are of the tile's transform; the regions say
		// the other.
		if err := p.send(1, !extra, nil, e.upgrade(1, progQualities[1].BitPos)); err == nil || !strings.Contains(err.Error(), "another transform") {
			t.Fatalf("extra %v: upgrade in the other transform: %v", extra, err)
		}
		if err := p.send(1, !extra, nil, e.first(coef[i], true, true, progFull, fullPass)); err == nil || !strings.Contains(err.Error(), "another transform") {
			t.Fatalf("extra %v: difference tile in the other transform: %v", extra, err)
		}
		// Sent whole, the tile may change its transform, and a new tile
		// may come as a difference in either.
		p.must(t, 1, !extra, nil, newProgEnc(0, 0, !extra).first(coef[1-i], false, true, progFull, fullPass))
		q := newProgSurface(t, 64, 64)
		q.must(t, 1, !extra, nil, newProgEnc(0, 0, !extra).first(coef[1-i], true, true, progFull, fullPass))
	}
}

// A full pass gives a tile's signs back, and the next tile to need them
// takes them, up to maxSignPool of them kept.
func TestProgressiveSignPool(t *testing.T) {
	p := newProgSurface(t, 64*80, 64)
	coef := rdptest.EncodeTile(testImage(0, 0), image.Point{}, &fineQuant, false)
	var first, full [][]byte
	var encs []*progEnc
	for x := range 80 {
		e := newProgEnc(x, 0, false)
		encs = append(encs, e)
		first = append(first, e.first(coef, false, false, 0, progQualities[0].BitPos))
		full = append(full, e.upgrade(progFull, fullPass))
	}
	p.must(t, 1, false, nil, first...)
	p.must(t, 1, false, nil, full...)
	if n := len(p.c.gfx.signs); n != maxSignPool {
		t.Fatalf("%d signs kept, want %d", n, maxSignPool)
	}
	kept := p.c.gfx.signs[maxSignPool-1]
	*kept = [3][4096]int8{{1}}
	p.must(t, 1, false, nil, encs[0].first(coef, false, false, 0, progQualities[0].BitPos))
	if p.tile(t, 0, 0).sign != kept || len(p.c.gfx.signs) != maxSignPool-1 {
		t.Fatal("the next tile did not take the signs kept")
	}
	p.wantTile(t, encs[0], true)
}

// Malformed RemoteFX and progressive messages end the session with an
// error, and those beyond the client's bounds too.
func TestRFXMalformed(t *testing.T) {
	// Coarse quantization keeps the tiles' data short, below what the
	// cases make the headers claim.
	coef := rdptest.EncodeTile(testImage(0, 0), image.Point{}, &coarseQuant, false)
	var tile rdptest.RFXTile
	for c := range 3 {
		tile.Data[c] = rdptest.RLGR(rdptest.RLGR3, rdptest.Deltas(coef[c][:], rdptest.Layout(false)[rdptest.LL3]))
	}
	rfx := rdptest.RemoteFX(rdptest.RLGR3, nil, coarseQuant[:1], []rdptest.RFXTile{tile})
	// The offsets: FRAME_BEGIN 14 bytes, the region of no rectangles 15,
	// then the tile set's block, whose header is 22 bytes, a table, and
	// the tile's block.
	set := 14 + 15
	tileAt := set + 22 + 5
	edit := func(b []byte, at int, v ...byte) []byte {
		b = slices.Clone(b)
		copy(b[at:], v)
		return b
	}
	u16 := func(v int) []byte { return binary.LittleEndian.AppendUint16(nil, uint16(v)) }
	u32 := func(v int) []byte { return binary.LittleEndian.AppendUint32(nil, uint32(v)) }
	rfxCases := []struct {
		name, want string
		msg        []byte
	}{
		{"block short", "RemoteFX block of 5", edit(rfx, 2, u32(5)...)},
		{"block long", "RemoteFX block of", edit(rfx, 2, u32(1<<20)...)},
		{"region rectangles", "RemoteFX region of 9 rectangles", edit(rfx, 14+9, u16(9)...)},
		{"subtype", "subtype", edit(rfx, set+8, 0x00, 0xca)},
		{"tile size", "RemoteFX tiles of 32 pixels", edit(rfx, set+15, 32)},
		{"entropy", "entropy algorithm 2", edit(rfx, set+12, u16(2<<10)...)},
		{"tables", "quantization tables in", edit(rfx, set+14, 200)},
		{"tiles data", "RemoteFX tiles of", edit(rfx, set+18, u32(1<<20)...)},
		{"tile short", "RemoteFX tile of 18 bytes", edit(rfx, tileAt+2, u32(18)...)},
		{"tile type", "tile block of type", edit(rfx, tileAt, 0x00, 0xca)},
		{"tile table", "quantization tables 0, 1 and 0 of 1", edit(rfx, tileAt+7, 1)},
		{"component lengths", "RemoteFX tile of", edit(rfx, tileAt+13, 0xff, 0xff)},
		// A value in run-length mode, 10000, that turns to Golomb-Rice
		// mode, then a sum of 2, 110, and a first value of 3, 11.
		{"rlgr3", "RLGR3", edit(rfx, tileAt+19, 0x86, 0xc0)},
	}
	for _, tc := range rfxCases {
		g := newGFXTest(t, 64, 64)
		g.must(t, surfacePDUs(1, 64, 64, image.Point{})...)
		err := g.send(rdptest.WireToSurface1(1, rdptest.CodecRemoteFX, rdptest.PixelXRGB, image.Rect(0, 0, 64, 64), tc.msg))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", tc.name, err, tc.want)
		}
	}

	e := newProgEnc(0, 0, false)
	first := e.first(coef, false, false, 0, progQualities[0].BitPos)
	upgrade := e.upgrade(1, progQualities[1].BitPos)
	// A first pass at 50%, which an upgrade to 25% would raise.
	first50 := newProgEnc(0, 0, false).first(coef, false, false, 1, progQualities[1].BitPos)
	region := func(tiles ...[]byte) []byte {
		r := rdptest.ProgRegion{Rects: []image.Rectangle{image.Rect(0, 0, 64, 64)}, Quants: coarseQuant[:], ProgQuants: progQualities, Tiles: tiles}
		return r.Block()
	}
	frame := func(blocks ...[]byte) []byte {
		b := rdptest.Progressive(1)
		return slices.Concat(b[:12], slices.Concat(blocks...), b[12:])
	}
	// A region's block: 18 bytes of header, a rectangle of 8, three
	// tables of 5 and three qualities of 16, then the tiles.
	tilesAt := 18 + 8 + 3*5 + 3*16
	reg := region(first)
	progCases := []struct {
		name, want string
		msg        []byte
	}{
		{"block short", "progressive block of 3", frame(edit(reg, 2, u32(3)...))},
		{"tile size", "progressive tiles of 16 pixels", frame(edit(reg, 6, 16))},
		{"region tables", "quantization tables in", frame(edit(reg, 9, 255))},
		{"tiles data", "progressive tiles of", frame(edit(reg, 14, u32(1<<20)...))},
		{"tile block", "progressive tile of 4 bytes", frame(edit(reg, tilesAt+2, u32(4)...))},
		{"first short", "progressive tile of 20 bytes", frame(region(edit(first, 2, u32(20)...)[:20]))},
		{"first lengths", "progressive tile of", frame(region(edit(first, 15, 0xff, 0xff)))},
		{"first table", "quantization tables 0, 1 and 3 of 3", frame(region(edit(first, 8, 3)))},
		{"first quality", "quality 3 of 3", frame(region(edit(first, 14, 3)))},
		// The surface is a column of two tiles: x 1 is past its column,
		// though tile 1 of the two, and y 2 below them.
		{"first outside", "outside its surface", frame(region(edit(first, 9, 1)))},
		{"first below", "outside its surface", frame(region(edit(first, 11, 2)))},
		{"upgrade short", "progressive upgrade of 25 bytes", frame(region(first, edit(upgrade, 2, u32(25)...)[:25]))},
		{"upgrade lengths", "progressive upgrade of", frame(region(first, edit(upgrade, 14, 0xff, 0xff)))},
		{"upgrade first", "no first pass", frame(region(upgrade))},
		{"upgrade rises", "from bit", frame(region(first50, edit(upgrade, 13, 0)))},
		{"upgrade quality", "quality 9 of 3", frame(region(first, edit(upgrade, 13, 9)))},
	}
	for _, tc := range progCases {
		g := newGFXTest(t, 64, 128)
		g.must(t, surfacePDUs(1, 64, 128, image.Point{})...)
		err := g.send(rdptest.WireToSurface2(1, rdptest.CodecProgressive, 1, rdptest.PixelXRGB, tc.msg))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", tc.name, err, tc.want)
		}
	}
}

// Each tile is charged rfxTileCost, as is each tile block of a
// progressive region, and the pixels it draws within the rectangles,
// and a pixel for each rectangle.
func TestRFXPace(t *testing.T) {
	coef := rdptest.EncodeTile(testImage(0, 0), image.Point{}, &fineQuant, false)
	var tile rdptest.RFXTile
	for c := range 3 {
		tile.Data[c] = rdptest.RLGR(rdptest.RLGR1, rdptest.Deltas(coef[c][:], rdptest.Layout(false)[rdptest.LL3]))
	}
	t2 := tile
	t2.X = 1
	// Two tiles from (16, 16): the first is within the first rectangle
	// for 64×30 pixels and the second for 64×24, the second tile within
	// the first for 36×30 only.
	rfx := rdptest.WireToSurface1(1, rdptest.CodecRemoteFX, rdptest.PixelXRGB, image.Rect(16, 16, 144, 80),
		rdptest.RemoteFX(rdptest.RLGR1, []image.Rectangle{image.Rect(0, 0, 100, 30), image.Rect(0, 40, 64, 64)}, fineQuant[:1], []rdptest.RFXTile{tile, t2}))
	e0, e1 := newProgEnc(0, 0, false), newProgEnc(1, 0, false)
	region := rdptest.ProgRegion{Rects: []image.Rectangle{image.Rect(0, 0, 100, 100)}, Quants: xrdpQuant[:], ProgQuants: progQualities,
		Tiles: [][]byte{e0.first(coef, false, false, 0, progQualities[0].BitPos), e1.first(coef, false, true, progFull, fullPass)}}
	upgrade := rdptest.ProgRegion{Rects: []image.Rectangle{image.Rect(0, 0, 100, 100)}, Quants: xrdpQuant[:], ProgQuants: progQualities,
		Tiles: [][]byte{e0.upgrade(1, progQualities[1].BitPos)}}
	prog := rdptest.WireToSurface2(1, rdptest.CodecProgressive, 1, rdptest.PixelXRGB, rdptest.Progressive(1, region, upgrade))
	e2 := newProgEnc(0, 0, false)
	twice := rdptest.ProgRegion{Rects: []image.Rectangle{image.Rect(0, 0, 100, 100)}, Quants: xrdpQuant[:], ProgQuants: progQualities,
		Tiles: [][]byte{e2.first(coef, false, false, 0, progQualities[0].BitPos), e2.upgrade(1, progQualities[1].BitPos)}}
	progTwice := rdptest.WireToSurface2(1, rdptest.CodecProgressive, 1, rdptest.PixelXRGB, rdptest.Progressive(1, twice))
	full := rdptest.WireToSurface2(1, rdptest.CodecProgressive, 1, rdptest.PixelXRGB, rdptest.Progressive(1, rdptest.ProgRegion{Quants: xrdpQuant[:], Tiles: [][]byte{newProgEnc(0, 0, false).first(coef, false, true, progFull, fullPass)}}))
	for _, tc := range []struct {
		name  string
		setup []byte // sent before the PDU, uncharged
		pdu   []byte
		want  int
	}{
		{"remotefx", nil, rfx, 2*rfxTileCost + 2 + 64*30 + 64*24 + 2 + 36*30},
		// The first region's two tiles, and the second's upgrade of the
		// first, drawn again.
		{"progressive", nil, prog, 3*rfxTileCost + 1 + 64*64 + 1 + 36*64 + 1 + 64*64},
		// A tile that a region changes twice is drawn once.
		{"progressive twice", nil, progTwice, 2*rfxTileCost + 1 + 64*64},
		// The end of a codec context whose tiles keep signs looks at the
		// surface's 16 tiles; of one whose tiles keep none, at none.
		{"context end, signs", progTwice, deleteContext(1), 16},
		{"context end, no signs", full, deleteContext(1), 0},
	} {
		g := newGFXTest(t, 256, 256)
		g.must(t, surfacePDUs(1, 256, 256, image.Point{})...)
		if tc.setup != nil {
			g.must(t, tc.setup)
		}
		// A second fills the bucket again, to twice the desktop.
		if err := g.clock.sleep(time.Second); err != nil {
			t.Fatal(err)
		}
		if err := g.c.pace(0); err != nil {
			t.Fatal(err)
		}
		before := g.c.tokens
		g.must(t, tc.pdu)
		if got := before - g.c.tokens; got != float64(tc.want) {
			t.Errorf("%s: charged %v pixels, want %d", tc.name, got, tc.want)
		}
	}
}

// The contexts keep at most progTileLimit tiles, across surfaces.
func TestProgressiveTileLimit(t *testing.T) {
	g := newGFXTest(t, 64, 64)
	limit := progTileLimit(64, 64)
	if limit != 1024 {
		t.Fatalf("limit %d for a 64×64 desktop", limit)
	}
	coef := rdptest.EncodeTile(testImage(0, 0), image.Point{}, &fineQuant, false)
	n := 0
	var err error
	for id := 1; n <= limit && err == nil; id++ {
		g.must(t, rdptest.CreateSurface(id, 64*64, 64, rdptest.PixelXRGB))
		var tiles [][]byte
		for x := range 64 {
			tiles = append(tiles, newProgEnc(x, 0, false).first(coef, false, true, progFull, fullPass))
		}
		region := rdptest.ProgRegion{Quants: xrdpQuant[:], Tiles: tiles}
		err = g.send(rdptest.WireToSurface2(id, rdptest.CodecProgressive, 1, rdptest.PixelXRGB, rdptest.Progressive(1, region)))
		n += 64
	}
	if err == nil || !strings.Contains(err.Error(), "more than 1024 progressive tiles") {
		t.Fatalf("%d tiles: %v", g.c.gfx.progTiles, err)
	}
	if g.c.gfx.progTiles != limit {
		t.Fatalf("%d tiles kept", g.c.gfx.progTiles)
	}
	if limit := progTileLimit(2560, 1600); limit != 2*40*25 {
		t.Fatalf("limit %d for 2560×1600", limit)
	}
}

// The tile limit is the largest desktop's that the pipeline has had: a
// desktop that shrinks, by RDPGFX_RESET_GRAPHICS_PDU or a reactivation,
// keeps the surfaces, and their tiles may grow, whether they were made
// before the desktop shrank or after.
func TestProgressiveTileLimitShrink(t *testing.T) {
	coef := rdptest.EncodeTile(testImage(0, 0), image.Point{}, &coarseQuant, false)
	tiles := func(from, n int) []byte {
		var tiles [][]byte
		for i := from; i < from+n; i++ {
			tiles = append(tiles, newProgEnc(i%60, i/60, false).first(coef, false, true, progFull, fullPass))
		}
		region := rdptest.ProgRegion{Rects: []image.Rectangle{image.Rect(0, 0, 8, 8)}, Quants: coarseQuant[:], Tiles: tiles}
		return gfxFrame(1, rdptest.WireToSurface2(1, rdptest.CodecProgressive, 1, rdptest.PixelXRGB, rdptest.Progressive(1, region)))
	}
	if limit := progTileLimit(1920, 1080); limit != 1024 {
		t.Fatalf("limit %d at 1920×1080", limit)
	}
	for _, tc := range []struct {
		name   string
		before int // the tiles made before the desktop shrinks
	}{{"reset", 1100}, {"reset", 0}, {"reactivation", 1100}, {"reactivation", 0}} {
		g := newGFXTest(t, 3840, 2160)
		g.must(t, surfacePDUs(1, 3840, 2160, image.Point{})...)
		if tc.before > 0 {
			g.must(t, tiles(0, tc.before))
		}
		if tc.name == "reset" {
			g.must(t, rdptest.ResetGraphics(1920, 1080))
		} else if _, err := g.c.handleShareControl(g.out, demandActive(1920, 1080)); err != nil {
			t.Fatal(err)
		}
		if w, h := g.c.Size(); w != 1920 || h != 1080 {
			t.Fatalf("%s: desktop of %d×%d", tc.name, w, h)
		}
		if err := g.send(tiles(tc.before, 1101-tc.before)); err != nil {
			t.Fatalf("%s, %d tiles before: a tile after the desktop shrank: %v", tc.name, tc.before, err)
		}
		if g.c.gfx.progTiles != 1101 {
			t.Fatalf("%s: %d tiles", tc.name, g.c.gfx.progTiles)
		}
	}
}

// Decoding a tile allocates nothing, in RemoteFX and in RemoteFX
// Progressive's passes.
func TestRFXAllocs(t *testing.T) {
	coef := rdptest.EncodeTile(testImage(1, 0), image.Point{}, &xrdpQuant, false)
	var tile rdptest.RFXTile
	for c := range 3 {
		tile.Data[c] = rdptest.RLGR(rdptest.RLGR1, rdptest.Deltas(coef[c][:], rdptest.Layout(false)[rdptest.LL3]))
	}
	g := newGFXTest(t, 64, 64)
	g.must(t, surfacePDUs(1, 64, 64, image.Point{})...)
	rfx := rdptest.RemoteFX(rdptest.RLGR1, []image.Rectangle{image.Rect(0, 0, 64, 64)}, xrdpQuant[:], []rdptest.RFXTile{tile})
	s := g.c.gfx.surfaces[1]
	if n := testing.AllocsPerRun(20, func() {
		if err := g.c.gfxRemoteFX(s, s.bounds(), rfx); err != nil {
			t.Fatal(err)
		}
	}); n != 0 {
		t.Errorf("%v allocations a RemoteFX tile", n)
	}
	e := newProgEnc(0, 0, false)
	first := rdptest.Progressive(1, rdptest.ProgRegion{Quants: xrdpQuant[:], ProgQuants: progQualities, Tiles: [][]byte{e.first(coef, false, false, 0, progQualities[0].BitPos)}})
	upgrade := rdptest.Progressive(1, rdptest.ProgRegion{Quants: xrdpQuant[:], ProgQuants: progQualities, Tiles: [][]byte{e.upgrade(1, progQualities[1].BitPos)}})
	full := rdptest.Progressive(1, rdptest.ProgRegion{Quants: xrdpQuant[:], ProgQuants: progQualities, Tiles: [][]byte{e.upgrade(progFull, fullPass)}})
	s.prog = &progState{ctx: 1, live: true, cols: 1, tiles: make([]*progTile, 1)}
	if err := g.c.progStream(s, first); err != nil {
		t.Fatal(err)
	}
	if n := testing.AllocsPerRun(20, func() {
		for _, m := range [][]byte{first, upgrade, full} {
			if err := g.c.progStream(s, m); err != nil {
				t.Fatal(err)
			}
		}
	}); n != 0 {
		t.Errorf("%v allocations a progressive tile's passes", n)
	}
}

// RLGR decodes what rdptest codes, in both modes, whatever the values.
func TestRLGRRoundTrip(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	for trial := range 200 {
		n := 1 + rng.IntN(4096)
		v := make([]int16, n)
		density := rng.Float64()
		scale := 1 << rng.IntN(15)
		for i := range v {
			if rng.Float64() < density {
				v[i] = int16(rng.IntN(2*scale+1) - scale)
			}
		}
		for _, mode := range []int{rlgr1, rlgr3} {
			got := make([]int16, n)
			if err := rlgrDecode(mode, rdptest.RLGR(mode, v), got); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, v) {
				t.Fatalf("trial %d, mode %d: decoded otherwise", trial, mode)
			}
		}
	}
}

// RLGR saturates values beyond 16 bits, and bounds a Golomb-Rice code's
// unary part: a stream of ones, a run of one zero and then a negative
// value of 2^17 ones, decodes to the value saturated, and zeros after.
func TestRLGRSaturates(t *testing.T) {
	ones := bytes.Repeat([]byte{0xff}, 1<<14)
	for _, mode := range []int{rlgr1, rlgr3} {
		out := make([]int16, 64)
		if err := rlgrDecode(mode, ones, out); err != nil {
			t.Fatal(err)
		}
		if out[0] != 0 || out[1] != math.MinInt16 || slices.ContainsFunc(out[2:], func(v int16) bool { return v != 0 }) {
			t.Errorf("mode %d: %v", mode, out)
		}
	}
	var ll [4]int16
	copy(ll[:], []int16{math.MaxInt16, 10, math.MinInt16, math.MinInt16})
	undelta(ll[:])
	if ll != [4]int16{math.MaxInt16, math.MaxInt16, -1, math.MinInt16} {
		t.Errorf("undelta %v", ll)
	}
}

func FuzzRLGR(f *testing.F) {
	f.Add(byte(rlgr1), uint16(64), []byte{0xa8, 0x62, 0x6d, 0xff, 0xf7, 0x00})
	f.Add(byte(rlgr3), uint16(4096), rdptest.RLGR(rdptest.RLGR3, []int16{1, -2, 0, 0, 300, 0, 0, 0, 0, 7}))
	f.Fuzz(func(t *testing.T, mode byte, n uint16, data []byte) {
		m := rlgr1
		if mode&1 != 0 {
			m = rlgr3
		}
		out := make([]int16, int(n)%4097)
		if err := rlgrDecode(m, data, out); err != nil {
			return
		}
		// What decodes codes again to what decodes the same.
		again := make([]int16, len(out))
		if err := rlgrDecode(m, rdptest.RLGR(m, out), again); err != nil || !slices.Equal(again, out) {
			t.Fatalf("recoded: %v", err)
		}
	})
}

// fuzzRFXSeeds are messages of RemoteFX, in W2S1, and of RemoteFX
// Progressive, in W2S2, on a surface of 128×128.
func fuzzRFXSeeds() (rfx, prog [][]byte) {
	coef := rdptest.EncodeTile(testImage(1, 0), image.Point{}, &xrdpQuant, false)
	for _, mode := range []int{rdptest.RLGR1, rdptest.RLGR3} {
		var tile rdptest.RFXTile
		for c := range 3 {
			tile.Data[c] = rdptest.RLGR(mode, rdptest.Deltas(coef[c][:], rdptest.Layout(false)[rdptest.LL3]))
		}
		t2 := tile
		t2.X, t2.Y = 1, 1
		rfx = append(rfx, rdptest.RemoteFX(mode, []image.Rectangle{image.Rect(0, 0, 100, 70)}, xrdpQuant[:], []rdptest.RFXTile{tile, t2}))
	}
	for _, extra := range []bool{false, true} {
		e := newProgEnc(1, 0, extra)
		c := rdptest.EncodeTile(testImage(0, 0), image.Point{}, &xrdpQuant, extra)
		region := func(tiles ...[]byte) rdptest.ProgRegion {
			return rdptest.ProgRegion{Rects: []image.Rectangle{image.Rect(0, 0, 128, 100)}, Quants: xrdpQuant[:], ProgQuants: progQualities, Extrapolate: extra, Tiles: tiles}
		}
		stream := slices.Concat(rdptest.ProgSyncContext(),
			rdptest.Progressive(1, region(e.first(c, false, false, 0, progQualities[0].BitPos), newProgEnc(0, 1, extra).first(coef, false, true, progFull, fullPass))),
			rdptest.Progressive(2, region(e.upgrade(1, progQualities[1].BitPos))),
			rdptest.Progressive(3, region(e.first(coef, true, false, 2, progQualities[2].BitPos))),
			rdptest.Progressive(4, region(e.upgrade(progFull, fullPass))))
		prog = append(prog, stream)
	}
	return rfx, prog
}

func FuzzRemoteFX(f *testing.F) {
	rfx, _ := fuzzRFXSeeds()
	for _, s := range rfx {
		f.Add(uint16(10), uint16(20), s)
	}
	f.Fuzz(func(t *testing.T, x, y uint16, data []byte) {
		g := newGFXTest(t, 128, 128)
		g.must(t, surfacePDUs(1, 128, 128, image.Point{})...)
		r := image.Rect(int(x%128), int(y%128), 128, 128)
		s := g.c.gfx.surfaces[1]
		_ = g.c.gfxRemoteFX(s, r, data)
	})
}

func FuzzProgressive(f *testing.F) {
	_, prog := fuzzRFXSeeds()
	for _, s := range prog {
		f.Add(uint32(1), s, []byte{})
		f.Add(uint32(1), s[:len(s)/2], s[len(s)/2:])
	}
	f.Fuzz(func(t *testing.T, ctx uint32, a, b []byte) {
		g := newGFXTest(t, 128, 128)
		g.must(t, surfacePDUs(1, 128, 128, image.Point{})...)
		for i, m := range [][]byte{a, b} {
			// The second in another context, if a's length is odd.
			err := g.send(rdptest.WireToSurface2(1, rdptest.CodecProgressive, ctx+uint32(i*(len(a)%2)), rdptest.PixelXRGB, m))
			if p := g.c.gfx.progTiles; p > progTileLimit(128, 128) || p < 0 {
				t.Fatalf("%d tiles", p)
			}
			if err != nil {
				return
			}
		}
	})
}

// BenchmarkGFXRemoteFX is a frame of RemoteFX tiles, a PDU each, as
// BenchmarkGFXPlanar's.
func BenchmarkGFXRemoteFX(b *testing.B) {
	benchFrame(b, func(r image.Rectangle, img *image.RGBA) []byte {
		return rdptest.WireToSurface1(1, rdptest.CodecRemoteFX, rdptest.PixelXRGB, r, rfxEncode(img, rdptest.RLGR1))
	})
}

// rfxEncode is img, a tile, in RemoteFX at xrdp's quantization.
func rfxEncode(img *image.RGBA, mode int) []byte {
	coef := rdptest.EncodeTile(img, image.Point{}, &xrdpQuant, false)
	var tile rdptest.RFXTile
	for c := range 3 {
		tile.Data[c] = rdptest.RLGR(mode, rdptest.Deltas(coef[c][:], rdptest.Layout(false)[rdptest.LL3]))
	}
	return rdptest.RemoteFX(mode, nil, xrdpQuant[:1], []rdptest.RFXTile{tile})
}

// BenchmarkGFXProgressive is a frame of RemoteFX Progressive tiles sent
// whole, a W2S2 PDU each, as xrdp sends them.
func BenchmarkGFXProgressive(b *testing.B) {
	img := gradient(64, 64)
	coef := rdptest.EncodeTile(img, image.Point{}, &xrdpQuant, false)
	benchFrame(b, func(r image.Rectangle, _ *image.RGBA) []byte {
		tile := newProgEnc(r.Min.X/64, r.Min.Y/64, false).first(coef, false, true, progFull, fullPass)
		region := rdptest.ProgRegion{Rects: []image.Rectangle{r}, Quants: xrdpQuant[:], Tiles: [][]byte{tile}}
		return rdptest.WireToSurface2(1, rdptest.CodecProgressive, 1, rdptest.PixelXRGB, rdptest.Progressive(1, region))
	})
}

// BenchmarkProgressivePasses is a tile's first pass and three upgrades.
func BenchmarkProgressivePasses(b *testing.B) {
	g := newGFXTest(b, 64, 64)
	g.must(b, surfacePDUs(1, 64, 64, image.Point{})...)
	coef := rdptest.EncodeTile(gradient(64, 64), image.Point{}, &xrdpQuant, false)
	e := newProgEnc(0, 0, false)
	var msgs [][]byte
	for i, tile := range [][]byte{
		e.first(coef, false, false, 0, progQualities[0].BitPos), e.upgrade(1, progQualities[1].BitPos),
		e.upgrade(2, progQualities[2].BitPos), e.upgrade(progFull, fullPass),
	} {
		msgs = append(msgs, rdptest.Progressive(uint32(i), rdptest.ProgRegion{Quants: xrdpQuant[:], ProgQuants: progQualities, Tiles: [][]byte{tile}}))
	}
	s := g.c.gfx.surfaces[1]
	s.prog = &progState{ctx: 1, live: true, cols: 1, tiles: make([]*progTile, 1)}
	b.ReportAllocs()
	for b.Loop() {
		for _, m := range msgs {
			if err := g.c.progStream(s, m); err != nil {
				b.Fatal(err)
			}
		}
	}
}

// BenchmarkRLGR is a component's coefficients decoded.
func BenchmarkRLGR(b *testing.B) {
	coef := rdptest.EncodeTile(testImage(1, 0), image.Point{}, &xrdpQuant, false)
	for _, mode := range []int{rlgr1, rlgr3} {
		data := rdptest.RLGR(mode, coef[0][:])
		var out [4096]int16
		b.Run(strconv.Itoa(mode), func(b *testing.B) {
			b.SetBytes(int64(len(data)))
			for b.Loop() {
				if err := rlgrDecode(mode, data, out[:]); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
