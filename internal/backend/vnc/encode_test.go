// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package vnc

// The fake server's encoders for Hextile, ZRLE and Tight. Like a server,
// an encoder belongs to one connection and keeps the zlib streams that
// ZRLE and Tight continue from rectangle to rectangle, flushing them at
// the end of each. Pixels are encoded in the pixel format the client
// asked for, compact pixels included, so a wrong request shows as wrong
// pixels.

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"image"
	"image/color"
	"image/jpeg"
	"slices"
)

// encoder encodes rectangles for one connection.
type encoder struct {
	pf      pixFormat
	level   int      // the zlib level
	zstream *zwriter // ZRLE's
	tight   [4]*zwriter
}

// newEncoder returns an encoder in pf that compresses at zlib's default
// level.
func newEncoder(pf pixFormat) *encoder {
	return &encoder{pf: pf, level: zlib.DefaultCompression}
}

// zwriter is one zlib stream of a connection.
type zwriter struct {
	buf bytes.Buffer
	w   *zlib.Writer
}

// compress continues the stream in *z, which it starts if nil, with p,
// and flushes it, as a server does at the end of a rectangle.
func (e *encoder) compress(z **zwriter, p []byte) []byte {
	if *z == nil {
		zw := &zwriter{}
		w, err := zlib.NewWriterLevel(&zw.buf, e.level)
		if err != nil {
			panic(err)
		}
		zw.w = w
		*z = zw
	}
	(*z).buf.Reset()
	(*z).w.Write(p)
	(*z).w.Flush()
	return bytes.Clone((*z).buf.Bytes())
}

// is888 reports whether pf is the format in which Tight's compact pixels
// are 3 bytes of red, green and blue.
func (pf pixFormat) is888() bool {
	return pf.trueColour && pf.bpp == 32 && pf.depth == 24 && pf.rmax == 255 && pf.gmax == 255 && pf.bmax == 255
}

// appendCPixel appends c as ZRLE's compact pixel in pf: the 3 bytes of a
// 32-bit pixel that hold its colour, when they are the least or most
// significant 3, and the whole pixel otherwise.
func (pf pixFormat) appendCPixel(p []byte, c color.RGBA) []byte {
	full := pf.appendPixel(nil, c)
	if !pf.trueColour || pf.bpp != 32 || pf.depth > 24 {
		return append(p, full...)
	}
	maxPixel := uint32(pf.rmax)<<pf.rshift | uint32(pf.gmax)<<pf.gshift | uint32(pf.bmax)<<pf.bshift
	ls, ms := maxPixel < 1<<24, maxPixel&0xff == 0
	switch {
	case ls && !pf.bigEndian, ms && pf.bigEndian:
		return append(p, full[:3]...)
	case ls && pf.bigEndian, ms && !pf.bigEndian:
		return append(p, full[1:]...)
	}
	return append(p, full...)
}

// appendTPixel appends c as Tight's compact pixel in pf.
func (pf pixFormat) appendTPixel(p []byte, c color.RGBA) []byte {
	if pf.is888() {
		return append(p, c.R, c.G, c.B)
	}
	return pf.appendPixel(p, c)
}

// colours returns the distinct colours of img in r, in the order they
// first appear, up to limit+1 of them.
func colours(img *image.RGBA, r image.Rectangle, limit int) []color.RGBA {
	var cs []color.RGBA
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			c := img.RGBAAt(x, y)
			if !slices.Contains(cs, c) {
				if cs = append(cs, c); len(cs) > limit {
					return cs
				}
			}
		}
	}
	return cs
}

// tiles calls f for each tile of side n of r, left to right and top to
// bottom.
func tiles(r image.Rectangle, n int, f func(t image.Rectangle)) {
	for y := r.Min.Y; y < r.Max.Y; y += n {
		for x := r.Min.X; x < r.Max.X; x += n {
			f(image.Rect(x, y, min(x+n, r.Max.X), min(y+n, r.Max.Y)))
		}
	}
}

// subrect is a rectangle of one colour within a tile, relative to it.
type subrect struct {
	r image.Rectangle
	c color.RGBA
}

// cover covers the pixels of tile t that are not bg with rectangles of
// one colour, greedily: as far right, and then as far down, as each
// goes.
func cover(img *image.RGBA, t image.Rectangle, bg color.RGBA) []subrect {
	done := make([]bool, t.Dx()*t.Dy())
	at := func(x, y int) color.RGBA { return img.RGBAAt(t.Min.X+x, t.Min.Y+y) }
	var rs []subrect
	for y := range t.Dy() {
		for x := range t.Dx() {
			c := at(x, y)
			if done[y*t.Dx()+x] || c == bg {
				continue
			}
			x2 := x + 1
			for x2 < t.Dx() && !done[y*t.Dx()+x2] && at(x2, y) == c {
				x2++
			}
			y2 := y + 1
		down:
			for ; y2 < t.Dy(); y2++ {
				for i := x; i < x2; i++ {
					if done[y2*t.Dx()+i] || at(i, y2) != c {
						break down
					}
				}
			}
			for j := y; j < y2; j++ {
				for i := x; i < x2; i++ {
					done[j*t.Dx()+i] = true
				}
			}
			rs = append(rs, subrect{image.Rect(x, y, x2, y2), c})
		}
	}
	return rs
}

// hextile encodes img's pixels in r as a Hextile rectangle, each tile as
// the shortest of raw pixels and a background with subrectangles. Like
// libvncserver's encoder, it leaves out a background or foreground that
// the tiles before set, and sets the foreground again after a tile of
// coloured subrectangles.
func (e *encoder) hextile(img *image.RGBA, r image.Rectangle) []byte {
	p := rectHeader(r, encHextile)
	var bg, fg color.RGBA
	haveBG, haveFG := false, false
	tiles(r, hextileTile, func(t image.Rectangle) {
		cs := colours(img, t, 256)
		// The background is the most common colour.
		count := map[color.RGBA]int{}
		for y := t.Min.Y; y < t.Max.Y; y++ {
			for x := t.Min.X; x < t.Max.X; x++ {
				count[img.RGBAAt(x, y)]++
			}
		}
		b := cs[0]
		for _, c := range cs {
			if count[c] > count[b] {
				b = c
			}
		}
		rs := cover(img, t, b)
		mono := true
		for _, s := range rs {
			mono = mono && s.c == rs[0].c
		}
		var sub byte
		var body []byte
		if !haveBG || b != bg {
			sub |= hextileBackground
			body = e.pf.appendPixel(body, b)
		}
		newFG := haveFG
		if len(rs) > 0 {
			sub |= hextileAnySubrects
			if mono {
				if !haveFG || rs[0].c != fg {
					sub |= hextileForeground
					body = e.pf.appendPixel(body, rs[0].c)
				}
				newFG = true
			} else {
				sub |= hextileColoured
				newFG = false
			}
			body = append(body, byte(len(rs)))
			for _, s := range rs {
				if !mono {
					body = e.pf.appendPixel(body, s.c)
				}
				body = append(body, byte(s.r.Min.X<<4|s.r.Min.Y), byte((s.r.Dx()-1)<<4|(s.r.Dy()-1)))
			}
		}
		if raw := int(e.pf.bpp) / 8 * t.Dx() * t.Dy(); len(rs) > 255 || len(body) > raw {
			p = append(p, hextileRaw)
			for y := t.Min.Y; y < t.Max.Y; y++ {
				for x := t.Min.X; x < t.Max.X; x++ {
					p = e.pf.appendPixel(p, img.RGBAAt(x, y))
				}
			}
			return
		}
		p = append(append(p, sub), body...)
		bg, haveBG = b, true
		if mono && len(rs) > 0 {
			fg = rs[0].c
		}
		haveFG = newFG
	})
	return p
}

// appendRun appends a run length of ZRLE: 255s and a last byte less than
// 255 that add up to n-1.
func appendRun(p []byte, n int) []byte {
	n--
	for ; n >= 255; n -= 255 {
		p = append(p, 255)
	}
	return append(p, byte(n))
}

// zrleForce, when not -1, is the subencoding every ZRLE tile is sent in
// if its colours allow it.
type zrleForce int

// zrle encodes img's pixels in r as a ZRLE rectangle, each tile in the
// shortest subencoding.
func (e *encoder) zrle(img *image.RGBA, r image.Rectangle) []byte {
	var data []byte
	tiles(r, zrleTile, func(t image.Rectangle) { data = e.zrleTile(data, img, t, -1) })
	return e.zrleRect(r, data)
}

// zrleRect compresses the tiles' data and returns the rectangle.
func (e *encoder) zrleRect(r image.Rectangle, data []byte) []byte {
	z := e.compress(&e.zstream, data)
	p := rectHeader(r, encZRLE)
	p = binary.BigEndian.AppendUint32(p, uint32(len(z)))
	return append(p, z...)
}

// zrleTile appends tile t in the shortest subencoding, or in force if its
// colours allow it.
func (e *encoder) zrleTile(p []byte, img *image.RGBA, t image.Rectangle, force zrleForce) []byte {
	cs := colours(img, t, 127)
	if len(cs) == 1 {
		return e.pf.appendCPixel(append(p, 1), cs[0])
	}
	var pixels []color.RGBA
	for y := t.Min.Y; y < t.Max.Y; y++ {
		for x := t.Min.X; x < t.Max.X; x++ {
			pixels = append(pixels, img.RGBAAt(x, y))
		}
	}
	type run struct {
		c color.RGBA
		n int
	}
	var runs []run
	for _, c := range pixels {
		if len(runs) > 0 && runs[len(runs)-1].c == c {
			runs[len(runs)-1].n++
		} else {
			runs = append(runs, run{c, 1})
		}
	}
	candidates := map[zrleForce][]byte{}
	raw := []byte{0}
	for _, c := range pixels {
		raw = e.pf.appendCPixel(raw, c)
	}
	candidates[0] = raw
	plain := []byte{128}
	for _, rn := range runs {
		plain = appendRun(e.pf.appendCPixel(plain, rn.c), rn.n)
	}
	candidates[128] = plain
	if len(cs) <= 127 {
		index := func(c color.RGBA) int { return slices.Index(cs, c) }
		palette := func(sub int) []byte {
			b := []byte{byte(sub)}
			for _, c := range cs {
				b = e.pf.appendCPixel(b, c)
			}
			return b
		}
		rle := palette(128 + len(cs))
		for _, rn := range runs {
			if rn.n == 1 {
				rle = append(rle, byte(index(rn.c)))
			} else {
				rle = appendRun(append(rle, byte(index(rn.c))|0x80), rn.n)
			}
		}
		candidates[zrleForce(128+len(cs))] = rle
		if len(cs) <= 16 {
			bits := 4
			switch {
			case len(cs) == 2:
				bits = 1
			case len(cs) <= 4:
				bits = 2
			}
			packed := palette(len(cs))
			for y := range t.Dy() {
				row := make([]byte, (t.Dx()*bits+7)/8)
				for x := range t.Dx() {
					bit := x * bits
					row[bit/8] |= byte(index(pixels[y*t.Dx()+x])) << (8 - bits - bit%8)
				}
				packed = append(packed, row...)
			}
			candidates[zrleForce(len(cs))] = packed
		}
	}
	if force >= 0 {
		for sub, b := range candidates {
			if sub == force || force >= 130 && sub >= 130 || force > 1 && force <= 16 && sub > 1 && sub <= 16 {
				return append(p, b...)
			}
		}
	}
	var best []byte
	for _, sub := range []zrleForce{0, 128, zrleForce(128 + len(cs)), zrleForce(len(cs))} {
		if b, ok := candidates[sub]; ok && (best == nil || len(b) < len(best)) {
			best = b
		}
	}
	return append(p, best...)
}

// appendCompact appends n as Tight's compact length.
func appendCompact(p []byte, n int) []byte {
	switch {
	case n < 1<<7:
		return append(p, byte(n))
	case n < 1<<14:
		return append(p, byte(n)|0x80, byte(n>>7))
	}
	return append(p, byte(n)|0x80, byte(n>>7)|0x80, byte(n>>14))
}

// tightFill encodes r filled with c as a Tight rectangle, resetting the
// streams in reset.
func (e *encoder) tightFill(r image.Rectangle, c color.RGBA, reset byte) []byte {
	e.resetTight(reset)
	p := append(rectHeader(r, encTight), tightFill<<4|reset)
	return e.pf.appendTPixel(p, c)
}

// tightJPEG encodes img's pixels in r as a Tight JPEG rectangle.
func (e *encoder) tightJPEG(img *image.RGBA, r image.Rectangle, quality int, reset byte) []byte {
	e.resetTight(reset)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img.SubImage(r), &jpeg.Options{Quality: quality}); err != nil {
		panic(err)
	}
	p := append(rectHeader(r, encTight), tightJPEG<<4|reset)
	return append(appendCompact(p, buf.Len()), buf.Bytes()...)
}

// tightOptions choose how tightBasic sends a rectangle.
type tightOptions struct {
	filter   byte // tightFilterCopy, tightFilterPalette or tightFilterGradient
	explicit bool // send the copy filter's ID, which is optional
	stream   int
	reset    byte // the streams to reset first
}

// resetTight starts new streams for those in reset.
func (e *encoder) resetTight(reset byte) {
	for i := range e.tight {
		if reset&(1<<i) != 0 {
			e.tight[i] = nil
		}
	}
}

// tightBasic encodes img's pixels in r as a Tight rectangle of the basic
// compression. The palette filter takes the colours in the order they
// appear.
func (e *encoder) tightBasic(img *image.RGBA, r image.Rectangle, o tightOptions) []byte {
	e.resetTight(o.reset)
	ctl := byte(o.stream) << 4
	if o.filter != tightFilterCopy || o.explicit {
		ctl |= tightExplicitFilter << 4
	}
	p := append(rectHeader(r, encTight), ctl|o.reset)
	if ctl&(tightExplicitFilter<<4) != 0 {
		p = append(p, o.filter)
	}
	var data []byte
	switch o.filter {
	case tightFilterCopy:
		for y := r.Min.Y; y < r.Max.Y; y++ {
			for x := r.Min.X; x < r.Max.X; x++ {
				data = e.pf.appendTPixel(data, img.RGBAAt(x, y))
			}
		}
	case tightFilterPalette:
		cs := colours(img, r, 256)
		if len(cs) > 256 {
			panic("more than 256 colours for the palette filter")
		}
		p = append(p, byte(len(cs)-1))
		for _, c := range cs {
			p = e.pf.appendTPixel(p, c)
		}
		for y := r.Min.Y; y < r.Max.Y; y++ {
			if len(cs) <= 2 {
				row := make([]byte, (r.Dx()+7)/8)
				for x := r.Min.X; x < r.Max.X; x++ {
					if slices.Index(cs, img.RGBAAt(x, y)) == 1 {
						row[(x-r.Min.X)/8] |= 0x80 >> ((x - r.Min.X) % 8)
					}
				}
				data = append(data, row...)
				continue
			}
			for x := r.Min.X; x < r.Max.X; x++ {
				data = append(data, byte(slices.Index(cs, img.RGBAAt(x, y))))
			}
		}
	case tightFilterGradient:
		data = gradientData(img, r)
	}
	if len(data) < tightMinToCompress {
		return append(p, data...)
	}
	z := e.compress(&e.tight[o.stream], data)
	return append(appendCompact(p, len(z)), z...)
}

// gradientData applies the gradient filter to img's pixels in r: each
// component less its prediction from the left, upper and upper left
// neighbours.
func gradientData(img *image.RGBA, r image.Rectangle) []byte {
	at := func(x, y, c int) int {
		if x < 0 || y < 0 {
			return 0
		}
		px := img.RGBAAt(r.Min.X+x, r.Min.Y+y)
		return int([]byte{px.R, px.G, px.B}[c])
	}
	var data []byte
	for y := range r.Dy() {
		for x := range r.Dx() {
			for c := range 3 {
				est := min(max(at(x-1, y, c)+at(x, y-1, c)-at(x-1, y-1, c), 0), 255)
				data = append(data, byte(at(x, y, c)-est))
			}
		}
	}
	return data
}

// mixed returns an image whose tiles of side n each hold a different kind
// of content, which Hextile and ZRLE send in different subencodings: one
// colour, two in a checkerboard, few in noise, stripes of few or many
// colours, and noise of many.
func mixed(w, h, n int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	noise := uint32(1)
	next := func() uint32 {
		noise ^= noise << 13
		noise ^= noise >> 17
		noise ^= noise << 5
		return noise
	}
	palette := func(i int) color.RGBA {
		return color.RGBA{uint8(i * 37), uint8(i*91 + 7), uint8(i*53 + 11), 0xff}
	}
	for y := range h {
		for x := range w {
			t := (y/n*((w+n-1)/n) + x/n) % 8
			var c color.RGBA
			switch t {
			case 0:
				c = palette(1)
			case 1:
				c = palette((x + y) % 2)
			case 2:
				c = palette(int(next() % 4))
			case 3:
				c = palette(int(next() % 12))
			case 4:
				c = palette(y % n / 3 % 20)
			case 5:
				c = palette(200 + y%n)
			case 6:
				c = color.RGBA{uint8(next()), uint8(next()), uint8(next()), 0xff}
			case 7:
				// A shape on a background: a few rectangles.
				c = palette(2)
				if x%n > 3 && x%n < 9 && y%n > 2 {
					c = palette(3)
				}
			}
			img.SetRGBA(x, y, c)
		}
	}
	return img
}
