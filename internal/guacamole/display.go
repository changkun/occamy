// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package guacamole

import (
	"bytes"
	"cmp"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"slices"
	"strconv"

	"golang.org/x/image/webp"
)

// Channel masks, the composite operations of img, copy and cfill. Each
// bit adds one Porter-Duff term: 0x8 the source where the destination is
// transparent, 0x4 the source where both are opaque, 0x2 the destination
// where the source is transparent, and 0x1 the destination where both
// are opaque. guacamole-common-js maps them to the canvas's composite
// operations: 0x2 is destination-out, 0x9 destination-atop, 0xA xor.
const (
	maskSrc  = 0xC
	maskOver = 0xE
)

// Display applies drawing instructions the way a client does, so that a
// tool can see what a session shows. It keeps the default layer (0), the
// visible layers (positive indices) that move, shade and size place on
// it, and the off-screen buffers (negative indices) that guacd draws
// through. The zero value is not usable; use NewDisplay.
type Display struct {
	layers  map[int]*image.RGBA
	visible map[int]*placement // layers other than the default
	paths   map[int][]image.Rectangle
	streams map[int]*imageStream
	out     *image.RGBA     // the composited display
	damage  image.Rectangle // since TakeDamage
	dirty   image.Rectangle // not yet composited into out

	// Unsupported counts instructions that draw in ways Display does not
	// implement, by opcode.
	Unsupported map[string]int
}

// placement is where a visible layer shows: relative to its parent, in z
// order among its siblings, and with an opacity.
type placement struct {
	parent, x, y, z int
	opacity         uint8
}

type imageStream struct {
	layer, x, y int
	mask        int
	mimetype    string
	data        []byte
}

// NewDisplay returns an empty display.
func NewDisplay() *Display {
	return &Display{
		layers:      map[int]*image.RGBA{0: image.NewRGBA(image.Rectangle{})},
		visible:     map[int]*placement{},
		paths:       map[int][]image.Rectangle{},
		out:         image.NewRGBA(image.Rectangle{}),
		streams:     map[int]*imageStream{},
		Unsupported: map[string]int{},
	}
}

// Image returns the display: the default layer with the visible layers
// composited on it. It changes as instructions apply.
func (d *Display) Image() *image.RGBA {
	if !d.dirty.Empty() {
		d.composite(d.dirty)
		d.dirty = image.Rectangle{}
	}
	return d.out
}

// TakeDamage returns the area of the display drawn since the last call,
// and resets it.
func (d *Display) TakeDamage() image.Rectangle {
	r := d.damage
	d.damage = image.Rectangle{}
	return r
}

// unsupportedOps draw with paths, strokes, patterns or transforms, which
// guacd's VNC, RDP and SSH support do not use for the default layer.
var unsupportedOps = map[string]bool{
	"arc": true, "curve": true, "line": true, "start": true, "close": true,
	"clip": true, "cstroke": true, "lstroke": true, "lfill": true,
	"push": true, "pop": true, "reset": true, "identity": true,
	"transform": true, "distort": true,
}

// maxDepth bounds the layer tree, so that a malformed move cannot make a
// cycle.
const maxDepth = 16

// origin returns where layer i's origin is on the display, and whether it
// shows at all: buffers and layers whose ancestry does not reach the
// default layer do not.
func (d *Display) origin(i int) (image.Point, bool) {
	var p image.Point
	for range maxDepth {
		if i == 0 {
			return p, true
		}
		pl, ok := d.visible[i]
		if !ok || pl.opacity == 0 {
			return p, false
		}
		p = p.Add(image.Pt(pl.x, pl.y))
		i = pl.parent
	}
	return p, false
}

// damageLayer records that r on layer i changed.
func (d *Display) damageLayer(i int, r image.Rectangle) {
	o, ok := d.origin(i)
	if !ok {
		return
	}
	r = r.Add(o).Intersect(d.layers[0].Bounds())
	d.damage = d.damage.Union(r)
	d.dirty = d.dirty.Union(r)
}

// damageWhole records that all of layer i changed.
func (d *Display) damageWhole(i int) {
	if l, ok := d.layers[i]; ok {
		d.damageLayer(i, l.Bounds())
	}
}

// composite redraws r of the display from the layers, children over their
// parents, siblings in z order.
func (d *Display) composite(r image.Rectangle) {
	r = r.Intersect(d.out.Bounds())
	draw.Draw(d.out, r, d.layers[0], r.Min, draw.Src)
	type item struct {
		i, depth, z int
		at          image.Point
		opacity     uint8
	}
	var items []item
	for i, pl := range d.visible {
		o, ok := d.origin(i)
		if !ok {
			continue
		}
		depth := 0
		for p := pl.parent; p != 0 && depth < maxDepth; depth++ {
			p = d.visible[p].parent
		}
		items = append(items, item{i: i, depth: depth, z: pl.z, at: o, opacity: pl.opacity})
	}
	slices.SortFunc(items, func(a, b item) int {
		return cmp.Or(cmp.Compare(a.depth, b.depth), cmp.Compare(a.z, b.z), cmp.Compare(a.i, b.i))
	})
	for _, it := range items {
		l := d.layers[it.i]
		dr := l.Bounds().Add(it.at).Intersect(r)
		if dr.Empty() {
			continue
		}
		var mask image.Image
		if it.opacity < 0xff {
			mask = image.NewUniform(color.Alpha{A: it.opacity})
		}
		draw.DrawMask(d.out, dr, l, dr.Min.Sub(it.at), mask, image.Point{}, draw.Over)
	}
}

// Apply applies one instruction. Instructions that do not draw are
// ignored.
func (d *Display) Apply(ins *Instruction) error {
	args := ins.Args()
	var err error
	switch op := ins.Opcode(); op {
	case "size":
		err = d.size(args)
	case "img":
		err = d.img(args)
	case "blob":
		err = d.blob(args)
	case "end":
		err = d.end(args)
	case "copy":
		err = d.copy(args, false)
	case "transfer":
		err = d.copy(args, true)
	case "rect":
		err = d.rect(args)
	case "cfill":
		err = d.cfill(args)
	case "dispose":
		err = d.dispose(args)
	case "move":
		err = d.move(args)
	case "shade":
		err = d.shade(args)
	default:
		if unsupportedOps[op] {
			d.Unsupported[op]++
		}
	}
	if err != nil {
		return fmt.Errorf("%s: %w", ins.Opcode(), err)
	}
	return nil
}

var errArgs = errors.New("malformed arguments")

// ints parses the arguments at the given positions.
func ints(args []string, pos ...int) ([]int, error) {
	out := make([]int, len(pos))
	for i, p := range pos {
		if p >= len(args) {
			return nil, errArgs
		}
		v, err := strconv.Atoi(args[p])
		if err != nil {
			return nil, errArgs
		}
		out[i] = v
	}
	return out, nil
}

// maxLayerSize bounds layers, so that a malformed size cannot exhaust
// memory.
const maxLayerSize = 16384

func (d *Display) layer(i int) *image.RGBA {
	l, ok := d.layers[i]
	if !ok {
		l = image.NewRGBA(image.Rectangle{})
		d.layers[i] = l
		if i > 0 {
			// A new layer shows at the origin of the default layer.
			d.visible[i] = &placement{opacity: 0xff}
		}
	}
	return l
}

func (d *Display) resize(i, w, h int) *image.RGBA {
	old := d.layer(i)
	l := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(l, l.Bounds(), old, image.Point{}, draw.Src)
	d.layers[i] = l
	return l
}

// target returns layer i ready for drawing to r: buffers grow to fit,
// visible layers clip.
func (d *Display) target(i int, r image.Rectangle) (*image.RGBA, image.Rectangle) {
	l := d.layer(i)
	if i < 0 && !r.In(l.Bounds()) {
		b := l.Bounds().Union(r).Intersect(image.Rect(0, 0, maxLayerSize, maxLayerSize))
		l = d.resize(i, b.Max.X, b.Max.Y)
	}
	r = r.Intersect(l.Bounds())
	d.damageLayer(i, r)
	return l, r
}

func (d *Display) size(args []string) error {
	v, err := ints(args, 0, 1, 2)
	if err != nil {
		return err
	}
	if v[1] < 0 || v[2] < 0 || v[1] > maxLayerSize || v[2] > maxLayerSize {
		return errArgs
	}
	d.damageWhole(v[0])
	l := d.resize(v[0], v[1], v[2])
	if v[0] == 0 {
		out := image.NewRGBA(l.Bounds())
		draw.Draw(out, out.Bounds(), d.out, image.Point{}, draw.Src)
		d.out = out
		d.damage = l.Bounds()
		d.dirty = l.Bounds()
	}
	d.damageWhole(v[0])
	return nil
}

func (d *Display) img(args []string) error {
	v, err := ints(args, 0, 1, 2, 4, 5)
	if err != nil || len(args) < 6 || !validMask(v[1]) {
		return errArgs
	}
	d.streams[v[0]] = &imageStream{
		mask: v[1], layer: v[2], mimetype: args[3], x: v[3], y: v[4],
	}
	return nil
}

func (d *Display) blob(args []string) error {
	if len(args) < 2 {
		return errArgs
	}
	i, err := strconv.Atoi(args[0])
	if err != nil {
		return errArgs
	}
	s, ok := d.streams[i]
	if !ok {
		return nil // not an image stream
	}
	// Each blob is base64 on its own.
	b, err := base64.StdEncoding.AppendDecode(s.data, []byte(args[1]))
	if err != nil {
		return err
	}
	s.data = b
	return nil
}

func (d *Display) end(args []string) error {
	v, err := ints(args, 0)
	if err != nil {
		return err
	}
	s, ok := d.streams[v[0]]
	if !ok {
		return nil
	}
	delete(d.streams, v[0])

	var img image.Image
	r := bytes.NewReader(s.data)
	switch s.mimetype {
	case "image/png":
		img, err = png.Decode(r)
	case "image/jpeg":
		img, err = jpeg.Decode(r)
	case "image/webp":
		img, err = webp.Decode(r)
	default:
		d.Unsupported["img "+s.mimetype]++
		return nil
	}
	if err != nil {
		return err
	}
	b := img.Bounds()
	l, dr := d.target(s.layer, image.Rectangle{Max: b.Size()}.Add(image.Pt(s.x, s.y)))
	composite(l, dr, img, b.Min.Add(dr.Min.Sub(image.Pt(s.x, s.y))), s.mask)
	return nil
}

func validMask(mask int) bool { return 0 <= mask && mask <= 0xF }

// composite draws src at sp onto r of dst with a channel mask. Outside r
// the destination stays as it is, where a canvas would also clear it for
// masks without the 0x2 bit; guacd uses those on buffers only.
func composite(dst *image.RGBA, r image.Rectangle, src image.Image, sp image.Point, mask int) {
	switch mask {
	case maskSrc:
		draw.Draw(dst, r, src, sp, draw.Src)
		return
	case maskOver:
		draw.Draw(dst, r, src, sp, draw.Over)
		return
	}
	// image.RGBA is premultiplied, as the terms need. src may overlap dst.
	tmp := image.NewRGBA(image.Rectangle{Max: r.Size()})
	draw.Draw(tmp, tmp.Bounds(), src, sp, draw.Src)
	for y := range r.Dy() {
		for x := range r.Dx() {
			sc := tmp.RGBAAt(x, y)
			dc := dst.RGBAAt(r.Min.X+x, r.Min.Y+y)
			as, ad := int(sc.A), int(dc.A)
			f := func(s, d uint8) uint8 {
				var v int
				if mask&0x8 != 0 {
					v += int(s) * (0xff - ad)
				}
				if mask&0x4 != 0 {
					v += int(s) * ad
				}
				if mask&0x2 != 0 {
					v += int(d) * (0xff - as)
				}
				if mask&0x1 != 0 {
					v += int(d) * as
				}
				return clampByte(v / 0xff)
			}
			dst.SetRGBA(r.Min.X+x, r.Min.Y+y, color.RGBA{
				R: f(sc.R, dc.R), G: f(sc.G, dc.G), B: f(sc.B, dc.B), A: f(sc.A, dc.A),
			})
		}
	}
}

// copy implements copy (srcl, sx, sy, w, h, mask, dstl, dx, dy) and
// transfer, which has a transfer function where copy has the mask.
func (d *Display) copy(args []string, transfer bool) error {
	v, err := ints(args, 0, 1, 2, 3, 4, 5, 6, 7, 8)
	if err != nil {
		return err
	}
	if !transfer && !validMask(v[5]) {
		return errArgs
	}
	src := d.layer(v[0])
	sr := image.Rect(v[1], v[2], v[1]+v[3], v[2]+v[4]).Intersect(src.Bounds())
	if sr.Empty() {
		return nil
	}
	dp := image.Pt(v[7], v[8]).Add(sr.Min.Sub(image.Pt(v[1], v[2])))
	l, dr := d.target(v[6], sr.Sub(sr.Min).Add(dp))
	sp := sr.Min.Add(dr.Min.Sub(dp))
	if !transfer {
		composite(l, dr, src, sp, v[5])
		return nil
	}
	transferRect(l, dr, src, sp, v[5])
	return nil
}

// transferRect applies a transfer function bitwise to the colour
// channels. Bit 0 of fn selects src AND dst, bit 1 src AND NOT dst, bit 2
// NOT src AND dst, and bit 3 NOT src AND NOT dst, so that 0x3 is SRC and
// 0x6 is XOR.
func transferRect(dst *image.RGBA, r image.Rectangle, src *image.RGBA, sp image.Point, fn int) {
	f := func(s, d uint8) uint8 {
		var out uint8
		if fn&1 != 0 {
			out |= s & d
		}
		if fn&2 != 0 {
			out |= s &^ d
		}
		if fn&4 != 0 {
			out |= ^s & d
		}
		if fn&8 != 0 {
			out |= ^s &^ d
		}
		return out
	}
	tmp := image.NewRGBA(image.Rectangle{Max: r.Size()})
	draw.Draw(tmp, tmp.Bounds(), src, sp, draw.Src) // src may overlap dst
	for y := range r.Dy() {
		for x := range r.Dx() {
			s := tmp.RGBAAt(x, y)
			p := dst.RGBAAt(r.Min.X+x, r.Min.Y+y)
			dst.SetRGBA(r.Min.X+x, r.Min.Y+y, color.RGBA{
				R: f(s.R, p.R), G: f(s.G, p.G), B: f(s.B, p.B), A: 0xff,
			})
		}
	}
}

func (d *Display) rect(args []string) error {
	v, err := ints(args, 0, 1, 2, 3, 4)
	if err != nil {
		return err
	}
	d.paths[v[0]] = append(d.paths[v[0]], image.Rect(v[1], v[2], v[1]+v[3], v[2]+v[4]))
	return nil
}

func (d *Display) cfill(args []string) error {
	v, err := ints(args, 0, 1, 2, 3, 4, 5)
	if err != nil {
		return err
	}
	if !validMask(v[0]) {
		return errArgs
	}
	c := &image.Uniform{C: color.NRGBA{
		R: clampByte(v[2]), G: clampByte(v[3]), B: clampByte(v[4]), A: clampByte(v[5]),
	}}
	for _, pr := range d.paths[v[1]] {
		l, dr := d.target(v[1], pr)
		composite(l, dr, c, image.Point{}, v[0])
	}
	delete(d.paths, v[1])
	return nil
}

func clampByte(v int) uint8 {
	switch {
	case v < 0:
		return 0
	case v > 255:
		return 255
	}
	return uint8(v)
}

func (d *Display) dispose(args []string) error {
	v, err := ints(args, 0)
	if err != nil {
		return err
	}
	if v[0] != 0 {
		d.damageWhole(v[0])
		delete(d.layers, v[0])
		delete(d.visible, v[0])
	}
	return nil
}

// move places a visible layer: layer, parent, x, y, z.
func (d *Display) move(args []string) error {
	v, err := ints(args, 0, 1, 2, 3, 4)
	if err != nil {
		return err
	}
	if v[0] <= 0 || v[1] < 0 {
		return errArgs
	}
	d.layer(v[0])
	d.damageWhole(v[0])
	pl := d.visible[v[0]]
	pl.parent, pl.x, pl.y, pl.z = v[1], v[2], v[3], v[4]
	d.damageWhole(v[0])
	return nil
}

// shade sets a visible layer's opacity: layer, opacity.
func (d *Display) shade(args []string) error {
	v, err := ints(args, 0, 1)
	if err != nil {
		return err
	}
	if v[0] <= 0 {
		return errArgs
	}
	d.layer(v[0])
	d.damageWhole(v[0])
	d.visible[v[0]].opacity = clampByte(v[1])
	d.damageWhole(v[0])
	return nil
}
