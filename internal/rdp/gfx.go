// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package rdp

import (
	"encoding/binary"
	"fmt"
	"image"
	"slices"
)

// The graphics pipeline, [MS-RDPEGFX], on the dynamic channel gfxChannel
// when Config.GFX asks for it. The client advertises its capabilities as
// the channel opens; once the server confirms a set, it creates surfaces,
// maps them to the output, the desktop, and draws on them in frames. The
// client keeps each surface's pixels, opaque RGBA, and at the end of a
// frame copies what the frame changed on the mapped surfaces to the
// output, then acknowledges the frame: the server sends frames as fast
// as they are acknowledged, so the acknowledgements pace it as the
// client decodes. What changed is kept in cells of gfxCell pixels a
// side, the framebuffer's tiles. A copy within a mapped surface, as a
// scroll, goes to the output as Output.CopyRect when the output has what
// it copies.
//
// The client takes solid fills, copies between surfaces, and bitmaps in
// the uncompressed and planar codecs. Bitmaps in other codecs, and the
// bitmap cache, are counted in Stats.Ignored and their frames still
// acknowledged. The capability sets it advertises turn H.264 off. The
// server's messages are segmented and compressed (zgfx.go); the
// client's are neither.
//
// The bitmaps that legacy updates draw on the output come as before: a
// server that does not open the channel, or does not confirm a set,
// draws that way alone.

// gfxChannel is the graphics pipeline's dynamic channel.
const gfxChannel = "Microsoft::Windows::RDS::Graphics"

// RDPGFX_CMDID values ([MS-RDPEGFX] 2.2.1.5).
const (
	gfxWireToSurface1     = 0x0001
	gfxWireToSurface2     = 0x0002
	gfxDeleteEncodingCtx  = 0x0003
	gfxSolidFill          = 0x0004
	gfxSurfaceToSurface   = 0x0005
	gfxSurfaceToCache     = 0x0006
	gfxCacheToSurface     = 0x0007
	gfxEvictCacheEntry    = 0x0008
	gfxCreateSurface      = 0x0009
	gfxDeleteSurface      = 0x000a
	gfxStartFrame         = 0x000b
	gfxEndFrame           = 0x000c
	gfxFrameAcknowledge   = 0x000d
	gfxResetGraphics      = 0x000e
	gfxMapSurfaceToOutput = 0x000f
	gfxCacheImportReply   = 0x0011
	gfxCapsAdvertise      = 0x0012
	gfxCapsConfirm        = 0x0013
)

// RDPGFX_CODECID values ([MS-RDPEGFX] 2.2.2.1).
const (
	gfxCodecUncompressed = 0x0000
	gfxCodecRemoteFX     = 0x0003
	gfxCodecClearCodec   = 0x0008
	gfxCodecProgressive  = 0x0009
	gfxCodecPlanar       = 0x000a
	gfxCodecAVC420       = 0x000b
	gfxCodecAlpha        = 0x000c
	gfxCodecAVC444       = 0x000e
	gfxCodecAVC444v2     = 0x000f
)

// RDPGFX_PIXELFORMAT values ([MS-RDPEGFX] 2.2.1.4).
const (
	gfxPixelXRGB = 0x20
	gfxPixelARGB = 0x21
)

// Capability flags ([MS-RDPEGFX] 2.2.3).
const (
	gfxCapsSmallCache      = 0x02
	gfxCapsAVCDisabled     = 0x20
	gfxCapsScaledMapOff    = 0x80
	gfxCapVersion8         = 0x00080004
	gfxCapVersion81        = 0x00080105
	gfxCapVersion10        = 0x000a0002
	gfxCapVersion101       = 0x000a0100
	gfxCapVersion102       = 0x000a0200
	gfxCapVersion103       = 0x000a0301
	gfxCapVersion104       = 0x000a0400
	gfxCapVersion105       = 0x000a0502
	gfxCapVersion106       = 0x000a0600
	gfxCapVersion107       = 0x000a0701
	gfxCapsDataLength      = 4
	gfxCapsDataLength101   = 16
	gfxQueueDepthUnknown   = 0
	gfxMaxMonitors         = 16
	gfxMonitorDefSize      = 20
	gfxResetGraphicsFields = 12
)

// gfxCapSets are the capability sets the client advertises, newest
// first. Each turns H.264 off where it can and keeps the bitmap cache
// small, 16 MB, which version 10.3 implies. Version 10.1 is left out, as
// it implies AVC444v2, and 10.5 and 10.6, which take scaled mappings
// that 10.7 can turn off; neither 8.0 nor 8.1 sets THINCLIENT, which
// would ask for RemoteFX in place of its progressive form.
var gfxCapSets = []struct{ version, flags uint32 }{
	{gfxCapVersion107, gfxCapsSmallCache | gfxCapsAVCDisabled | gfxCapsScaledMapOff},
	{gfxCapVersion104, gfxCapsSmallCache | gfxCapsAVCDisabled},
	{gfxCapVersion103, gfxCapsAVCDisabled},
	{gfxCapVersion102, gfxCapsSmallCache | gfxCapsAVCDisabled},
	{gfxCapVersion10, gfxCapsSmallCache | gfxCapsAVCDisabled},
	{gfxCapVersion81, gfxCapsSmallCache},
	{gfxCapVersion8, gfxCapsSmallCache},
}

// Limits on what the server makes the client keep.
const (
	// maxGFXData bounds a message on the channel once decompressed. xrdp
	// keeps its messages within 3 MiB.
	maxGFXData = 16 << 20
	// maxGFXMessage bounds a message as it comes: a segment of 65,535
	// bytes may grow by 1,000 compressed ([MS-RDPEGFX] 3.1.9.1.2), and
	// takes 5 bytes of framing.
	maxGFXMessage = maxGFXData + maxGFXData/64
	// maxGFXAcks bounds the frame acknowledgements that wait for the
	// channel writer. The server waits for them before it sends more, so
	// a server that does not read them ends the session.
	maxGFXAcks = 256
	// minSurfacePixels is the least that the surfaces may hold in all,
	// 64 MiB of RGBA; four desktops may hold more, to 256 MiB.
	minSurfacePixels = 1 << 24
	// maxGFXOrigin bounds where a surface is mapped, the largest output
	// [MS-RDPEGFX] 2.2.2.14 allows.
	maxGFXOrigin = 32766
	// gfxCell is the side of the cells the client keeps changes in.
	gfxCell = 64
	// retainGFX bounds each of the channel's buffers that the client
	// keeps between messages, the message reassembled and decompressed:
	// xrdp's largest, of 3 MiB, come every frame.
	retainGFX = 4 << 20
)

// surfaceLimit is the most pixels the surfaces may hold in all on a
// desktop of w×h: four desktops, at least minSurfacePixels.
func surfaceLimit(w, h int) int {
	return min(max(4*w*h, minSurfacePixels), 2*maxPixels)
}

// gfx is the state of the graphics pipeline, owned by the reader.
type gfx struct {
	ch        *dynChannel // the open channel, nil if none
	z         zgfx
	confirmed bool
	version   uint32 // the capability set the server confirmed
	flags     uint32
	surfaces  map[uint16]*surface
	mapped    []*surface // the mapped surfaces, in the order of their mapping
	pixels    int        // the surfaces' pixels in all
	inFrame   bool       // a frame started and has not ended
	decoded   uint32     // the frames acknowledged, totalFramesDecoded
}

// surface is an offscreen surface: opaque RGBA rows, 4*w bytes each.
type surface struct {
	id     uint16
	w, h   int
	pix    []byte
	mapped bool
	at     image.Point // its origin on the output, if mapped
	// The cells that changed since the surface last went to the output,
	// cols across, and their bounds in cells.
	cols  int
	dirty []bool
	drect image.Rectangle
}

// bounds is the surface's rectangle.
func (s *surface) bounds() image.Rectangle { return image.Rect(0, 0, s.w, s.h) }

// offset is the index of the pixel at x, y in pix.
func (s *surface) offset(x, y int) int { return 4 * (y*s.w + x) }

// touch records that r, within the surface, changed, if the surface is
// mapped: an unmapped surface goes to the output whole once mapped.
func (s *surface) touch(r image.Rectangle) {
	if !s.mapped || r.Empty() {
		return
	}
	c := image.Rect(r.Min.X/gfxCell, r.Min.Y/gfxCell, (r.Max.X+gfxCell-1)/gfxCell, (r.Max.Y+gfxCell-1)/gfxCell)
	for y := c.Min.Y; y < c.Max.Y; y++ {
		row := s.dirty[y*s.cols:]
		for x := c.Min.X; x < c.Max.X; x++ {
			row[x] = true
		}
	}
	s.drect = s.drect.Union(c)
}

// newSurface returns a surface of w×h, opaque black.
func newSurface(id uint16, w, h int) *surface {
	s := &surface{id: id, w: w, h: h, pix: make([]byte, 4*w*h), cols: (w + gfxCell - 1) / gfxCell}
	s.dirty = make([]bool, s.cols*((h+gfxCell-1)/gfxCell))
	fill(s, s.bounds(), [4]byte{0, 0, 0, 0xff})
	return s
}

// fill sets the pixels of r, within s, to px.
func fill(s *surface, r image.Rectangle, px [4]byte) {
	if r.Empty() {
		return
	}
	n := 4 * r.Dx()
	first := s.pix[s.offset(r.Min.X, r.Min.Y):][:n]
	copy(first, px[:])
	for k := 4; k < n; k *= 2 {
		copy(first[k:], first[:k])
	}
	for y := r.Min.Y + 1; y < r.Max.Y; y++ {
		copy(s.pix[s.offset(r.Min.X, y):][:n], first)
	}
}

// copyPixels copies the pixels of src's rectangle r to dst at p: both lie
// within their surfaces, which may be one, the rectangles overlapping.
func copyPixels(dst *surface, p image.Point, src *surface, r image.Rectangle) {
	n := 4 * r.Dx()
	h := r.Dy()
	if dst == src && p.Y > r.Min.Y {
		// Down within one surface: from the bottom row up, so that no row
		// is overwritten before it is copied. copy moves a row that
		// overlaps itself as it should.
		for y := h - 1; y >= 0; y-- {
			copy(dst.pix[dst.offset(p.X, p.Y+y):][:n], src.pix[src.offset(r.Min.X, r.Min.Y+y):][:n])
		}
		return
	}
	for y := range h {
		copy(dst.pix[dst.offset(p.X, p.Y+y):][:n], src.pix[src.offset(r.Min.X, r.Min.Y+y):][:n])
	}
}

// Names of the GFX PDUs the client counts in Stats.GFX, and of what it
// ignores in Stats.Ignored, constants so that counting allocates
// nothing.
var gfxNames = map[int]string{
	gfxWireToSurface1:     "wire to surface 1",
	gfxSolidFill:          "solid fill",
	gfxSurfaceToSurface:   "surface to surface",
	gfxCreateSurface:      "create surface",
	gfxDeleteSurface:      "delete surface",
	gfxStartFrame:         "start frame",
	gfxEndFrame:           "end frame",
	gfxResetGraphics:      "reset graphics",
	gfxMapSurfaceToOutput: "map surface to output",
	gfxCapsConfirm:        "caps confirm",
}

var gfxIgnored = map[int]string{
	gfxWireToSurface2:    "GFX progressive bitmaps",
	gfxDeleteEncodingCtx: "GFX codec contexts",
	gfxSurfaceToCache:    "GFX cache PDUs",
	gfxCacheToSurface:    "GFX cache PDUs",
	gfxEvictCacheEntry:   "GFX cache PDUs",
	gfxCacheImportReply:  "GFX cache PDUs",
}

var gfxCodecNames = map[int]string{
	gfxCodecRemoteFX:    "GFX RemoteFX bitmaps",
	gfxCodecClearCodec:  "GFX ClearCodec bitmaps",
	gfxCodecProgressive: "GFX progressive bitmaps",
	gfxCodecAVC420:      "GFX AVC420 bitmaps",
	gfxCodecAlpha:       "GFX alpha bitmaps",
	gfxCodecAVC444:      "GFX AVC444 bitmaps",
	gfxCodecAVC444v2:    "GFX AVC444 bitmaps",
}

// gfxOpened resets the pipeline for the channel dc, which the server
// just opened, and queues the client's capabilities after the answer
// that accepts it. chio.mu must be held.
func (c *Client) gfxOpened(dc *dynChannel) error {
	c.gfx = gfx{ch: dc}
	return c.dvcAnswer(c.gfxData(dc, capsAdvertise()))
}

// gfxClosed drops the pipeline's state with its channel, and the
// acknowledgements for it that wait. What it drew stays on the output.
// chio.mu must be held.
func (c *Client) gfxClosed() {
	c.gfx = gfx{}
	clear(c.dvc.acks)
	c.dvc.acks = c.dvc.acks[:0]
}

// gfxData is a client message on the channel dc as a Data PDU: the
// client's messages are neither segmented nor compressed, and fit.
func (c *Client) gfxData(dc *dynChannel, msg []byte) []byte {
	return dvcPDU(dvcData, 0, sizeCode(dc.id), dc.id, msg)
}

// gfxPDU is an RDPGFX_HEADER of cmd and body.
func gfxPDU(cmd int, body []byte) []byte {
	var w writer
	w.u16(cmd)
	w.u16(0)
	w.u32(uint32(8 + len(body))) // #nosec G115 -- the client's small PDUs
	return append(w.b, body...)
}

// capsAdvertise is RDPGFX_CAPS_ADVERTISE_PDU of gfxCapSets.
func capsAdvertise() []byte {
	var w writer
	w.u16(len(gfxCapSets))
	for _, s := range gfxCapSets {
		w.u32(s.version)
		w.u32(gfxCapsDataLength)
		w.u32(s.flags)
	}
	return gfxPDU(gfxCapsAdvertise, w.b)
}

// gfxMessage applies a message from the server on the channel: one or
// more GFX PDUs, segmented and maybe compressed. Outside a frame, what
// they changed goes to the output at once.
func (c *Client) gfxMessage(out Output, msg []byte) error {
	g := &c.gfx
	data, err := g.z.decompress(msg, maxGFXData)
	if err != nil {
		return err
	}
	for len(data) > 0 {
		r := reader{b: data}
		cmd := int(r.u16())
		r.u16() // flags
		n := int64(r.u32())
		if r.err != nil || n < 8 || n > int64(len(data)) {
			return fmt.Errorf("rdp: GFX PDU of %d bytes in %d", n, len(data))
		}
		if err := c.gfxPDU(out, cmd, data[8:n]); err != nil {
			return err
		}
		data = data[n:]
	}
	if cap(g.z.out) > retainGFX {
		g.z.out = nil
	}
	if !g.inFrame {
		c.gfxFlush(out)
	}
	return nil
}

// gfxPDU applies a GFX PDU of cmd with its body.
func (c *Client) gfxPDU(out Output, cmd int, body []byte) error {
	g := &c.gfx
	if !g.confirmed && cmd != gfxCapsConfirm {
		// [MS-RDPEGFX] 3.3.5.19: until the server confirms, it is ignored.
		c.count(func(s *Stats) { s.Ignored["GFX PDUs before the capabilities"]++ })
		return nil
	}
	r := reader{b: body}
	var err error
	switch cmd {
	case gfxCapsConfirm:
		err = c.gfxCapsConfirm(&r)
	case gfxResetGraphics:
		err = c.gfxReset(out, &r)
	case gfxCreateSurface:
		err = c.gfxCreate(&r)
	case gfxDeleteSurface:
		err = c.gfxDelete(&r)
	case gfxMapSurfaceToOutput:
		err = c.gfxMap(&r)
	case gfxStartFrame:
		r.u32() // timestamp
		r.u32() // frameId
		if r.err == nil && g.inFrame {
			// A frame without its end: what it drew goes with the next.
			c.count(func(s *Stats) { s.Ignored["GFX frames without their end"]++ })
		}
		g.inFrame = true
	case gfxEndFrame:
		id := r.u32()
		if r.err == nil {
			err = c.gfxEndFrame(out, id)
		}
	case gfxSolidFill:
		err = c.gfxSolidFill(&r)
	case gfxSurfaceToSurface:
		err = c.gfxSurfaceToSurface(out, &r)
	case gfxWireToSurface1:
		err = c.gfxWireToSurface1(&r)
	default:
		name, ok := gfxIgnored[cmd]
		if !ok {
			name = "GFX PDUs"
		}
		c.count(func(s *Stats) { s.Ignored[name]++ })
		return nil
	}
	if err == nil && r.err != nil {
		err = r.err
	}
	if err != nil {
		return fmt.Errorf("rdp: GFX PDU %#x: %w", cmd, err)
	}
	name := gfxNames[cmd]
	c.count(func(s *Stats) { s.GFX[name]++ })
	return nil
}

// gfxCapsConfirm takes the capability set the server confirmed. A set
// of a version [MS-RDPEGFX] 2.2.3 does not define is ignored.
func (c *Client) gfxCapsConfirm(r *reader) error {
	version, n := r.u32(), r.u32()
	if r.err != nil {
		return r.err
	}
	data := r.take(int(min(n, 1<<16)))
	if r.err != nil {
		return r.err
	}
	switch version {
	case gfxCapVersion8, gfxCapVersion81, gfxCapVersion10, gfxCapVersion101, gfxCapVersion102,
		gfxCapVersion103, gfxCapVersion104, gfxCapVersion105, gfxCapVersion106, gfxCapVersion107:
	default:
		c.count(func(s *Stats) { s.Ignored["GFX capability sets"]++ })
		return nil
	}
	g := &c.gfx
	g.confirmed, g.version, g.flags = true, version, 0
	if len(data) >= 4 && version != gfxCapVersion101 {
		g.flags = binary.LittleEndian.Uint32(data)
	}
	return nil
}

// gfxReset resizes the output to the size RDPGFX_RESET_GRAPHICS_PDU
// gives, unless the desktop has it already.
func (c *Client) gfxReset(out Output, r *reader) error {
	w, h, monitors := r.u32(), r.u32(), r.u32()
	if r.err != nil {
		return r.err
	}
	if monitors > gfxMaxMonitors || int64(r.len()) < int64(monitors)*gfxMonitorDefSize {
		return fmt.Errorf("%d monitors in %d bytes", monitors, r.len())
	}
	// Clamped, so that the size fits an int anywhere: checkDesktop
	// refuses it then.
	return c.gfxResize(out, int(min(w, 1<<16)), int(min(h, 1<<16)))
}

// gfxResize resizes the output to w×h, as a reactivation does, if the
// desktop has another size, and has the mapped surfaces drawn again.
func (c *Client) gfxResize(out Output, w, h int) error {
	if cw, ch := c.Size(); cw == w && ch == h {
		return nil
	}
	if err := checkDesktop(w, h); err != nil {
		return err
	}
	if out != nil {
		if err := c.waitResize(); err != nil {
			return err
		}
	}
	c.setSize(w, h)
	c.dec.drop()
	if out != nil {
		out.Resize(w, h)
	}
	c.gfxRedraw()
	if c.dynCh != nil {
		c.due() // a size asked for may now be due, or no longer
	}
	return nil
}

// setSize sets the desktop's size, under c.mu, which Size takes.
func (c *Client) setSize(w, h int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.width, c.height = w, h
}

// gfxRedraw has the mapped surfaces go to the output whole, as after a
// resize, which clears it.
func (c *Client) gfxRedraw() {
	for _, s := range c.gfx.mapped {
		s.touch(s.bounds())
	}
}

// surface returns the surface id, or an error if there is none: a PDU
// that names a surface that does not exist is malformed ([MS-RDPEGFX]
// 3.3.5).
func (c *Client) surface(id uint16) (*surface, error) {
	if s := c.gfx.surfaces[id]; s != nil {
		return s, nil
	}
	return nil, fmt.Errorf("no surface %d", id)
}

// gfxCreate applies RDPGFX_CREATE_SURFACE_PDU. A surface of an id that
// exists replaces it.
func (c *Client) gfxCreate(r *reader) error {
	id, w, h, format := r.u16(), int(r.u16()), int(r.u16()), r.u8()
	if r.err != nil {
		return r.err
	}
	if format != gfxPixelXRGB && format != gfxPixelARGB {
		return fmt.Errorf("surface of pixel format %#x", format)
	}
	if err := checkDesktop(w, h); err != nil {
		return fmt.Errorf("surface of %d×%d: %w", w, h, err)
	}
	g := &c.gfx
	pixels := g.pixels + w*h
	old := g.surfaces[id]
	if old != nil {
		pixels -= old.w * old.h
	}
	if limit := surfaceLimit(c.Size()); pixels > limit {
		return fmt.Errorf("surfaces of %d pixels in all, over the bound of %d", pixels, limit)
	}
	if err := c.pace(w * h); err != nil {
		return err
	}
	if old != nil {
		c.gfxRemove(old)
	}
	if g.surfaces == nil {
		g.surfaces = map[uint16]*surface{}
	}
	g.surfaces[id] = newSurface(id, w, h)
	g.pixels += w * h
	return nil
}

// gfxRemove deletes the surface s.
func (c *Client) gfxRemove(s *surface) {
	g := &c.gfx
	delete(g.surfaces, s.id)
	g.pixels -= s.w * s.h
	g.mapped = slices.DeleteFunc(g.mapped, func(m *surface) bool { return m == s })
}

// gfxDelete applies RDPGFX_DELETE_SURFACE_PDU. What the surface drew
// stays on the output.
func (c *Client) gfxDelete(r *reader) error {
	id := r.u16()
	if r.err != nil {
		return r.err
	}
	s := c.gfx.surfaces[id]
	if s == nil {
		c.count(func(s *Stats) { s.Ignored["GFX deletes of no surface"]++ })
		return nil
	}
	c.gfxRemove(s)
	return nil
}

// gfxMap applies RDPGFX_MAP_SURFACE_TO_OUTPUT_PDU: the surface goes to
// the output whole, charged as a bitmap of its size.
func (c *Client) gfxMap(r *reader) error {
	id := r.u16()
	r.u16() // reserved
	x, y := r.u32(), r.u32()
	if r.err != nil {
		return r.err
	}
	s, err := c.surface(id)
	if err != nil {
		return err
	}
	if x > maxGFXOrigin || y > maxGFXOrigin {
		return fmt.Errorf("surface %d mapped at %d,%d", id, x, y)
	}
	if err := c.pace(s.w * s.h); err != nil {
		return err
	}
	g := &c.gfx
	if s.mapped {
		g.mapped = slices.DeleteFunc(g.mapped, func(m *surface) bool { return m == s })
	}
	g.mapped = append(g.mapped, s)
	s.mapped, s.at = true, image.Pt(int(x), int(y))
	s.touch(s.bounds())
	return nil
}

// gfxEndFrame ends the frame id: what it changed goes to the output, and
// the acknowledgement to the server.
func (c *Client) gfxEndFrame(out Output, id uint32) error {
	g := &c.gfx
	c.gfxFlush(out)
	g.inFrame = false
	g.decoded++
	var w writer
	w.u32(gfxQueueDepthUnknown)
	w.u32(id)
	w.u32(g.decoded)
	ack := c.gfxData(g.ch, gfxPDU(gfxFrameAcknowledge, w.b))
	c.chio.mu.Lock()
	defer c.chio.mu.Unlock()
	if len(c.dvc.acks) == maxGFXAcks {
		return c.owed(fmt.Errorf("rdp: more than %d frame acknowledgements await writing", maxGFXAcks))
	}
	c.dvc.acks = append(c.dvc.acks, ack)
	c.due()
	return nil
}

// rect16 reads an RDPGFX_RECT16, whose right and bottom are exclusive,
// and checks that it is not inverted.
func rect16(r *reader) (image.Rectangle, error) {
	rc := image.Rectangle{Min: image.Pt(int(r.u16()), int(r.u16())), Max: image.Pt(int(r.u16()), int(r.u16()))}
	if r.err != nil {
		return rc, r.err
	}
	if rc.Min.X > rc.Max.X || rc.Min.Y > rc.Max.Y {
		return rc, fmt.Errorf("inverted rectangle %d,%d,%d,%d", rc.Min.X, rc.Min.Y, rc.Max.X, rc.Max.Y)
	}
	return rc, nil
}

// gfxSolidFill applies RDPGFX_SOLIDFILL_PDU. Its rectangles are clipped
// to the surface, as FreeRDP clips them, and charged as bitmaps of the
// pixels they fill.
func (c *Client) gfxSolidFill(r *reader) error {
	id := r.u16()
	b, g, red := r.u8(), r.u8(), r.u8()
	r.u8() // XA: the surfaces are opaque
	n := int(r.u16())
	if r.err != nil {
		return r.err
	}
	s, err := c.surface(id)
	if err != nil {
		return err
	}
	if r.len() < 8*n {
		return fmt.Errorf("%d rectangles in %d bytes", n, r.len())
	}
	rects := r.take(8 * n)
	area := 0
	for rr := (reader{b: rects}); rr.len() > 0; {
		rc, err := rect16(&rr)
		if err != nil {
			return err
		}
		rc = rc.Intersect(s.bounds())
		area += rc.Dx() * rc.Dy()
	}
	if err := c.pace(area); err != nil {
		return err
	}
	px := [4]byte{red, g, b, 0xff}
	for rr := (reader{b: rects}); rr.len() > 0; {
		rc, _ := rect16(&rr)
		rc = rc.Intersect(s.bounds())
		fill(s, rc, px)
		s.touch(rc)
	}
	return nil
}

// gfxSurfaceToSurface applies RDPGFX_SURFACE_TO_SURFACE_PDU: a rectangle
// of one surface copied to points of another, or of the same, each copy
// charged as a bitmap of its size. The rectangles must lie within their
// surfaces, as FreeRDP requires.
func (c *Client) gfxSurfaceToSurface(out Output, r *reader) error {
	from, to := r.u16(), r.u16()
	sr, err := rect16(r)
	if err != nil {
		return err
	}
	n := int(r.u16())
	if r.err != nil {
		return r.err
	}
	src, err := c.surface(from)
	if err != nil {
		return err
	}
	dst, err := c.surface(to)
	if err != nil {
		return err
	}
	if !sr.In(src.bounds()) {
		return fmt.Errorf("copy of %v outside surface %d of %d×%d", sr, from, src.w, src.h)
	}
	if r.len() < 4*n {
		return fmt.Errorf("%d points in %d bytes", n, r.len())
	}
	if err := c.pace(n * sr.Dx() * sr.Dy()); err != nil {
		return err
	}
	for range n {
		p := image.Pt(int(r.u16()), int(r.u16()))
		if dr := sr.Add(p.Sub(sr.Min)); !dr.In(dst.bounds()) {
			return fmt.Errorf("copy to %v outside surface %d of %d×%d", dr, to, dst.w, dst.h)
		}
		c.gfxCopy(out, dst, p, src, sr)
	}
	return nil
}

// gfxCopy copies the rectangle r of src to p on dst. A copy within a
// mapped surface whose rectangles lie on the output, where no other
// surface is mapped, goes to the output as a copy, once the output has
// all that the surface changed; another is drawn as it changed.
func (c *Client) gfxCopy(out Output, dst *surface, p image.Point, src *surface, r image.Rectangle) {
	if r.Empty() {
		return
	}
	dr := r.Add(p.Sub(r.Min))
	if dst == src && dst.mapped && out != nil {
		w, h := c.Size()
		desk := image.Rect(0, 0, w, h)
		so, do := r.Add(dst.at), dr.Add(dst.at)
		if so.In(desk) && do.In(desk) && !c.gfxCovered(dst, so.Union(do)) {
			c.gfxFlushSurface(out, dst, desk)
			copyPixels(dst, p, src, r)
			out.CopyRect(do, so.Min)
			return
		}
	}
	copyPixels(dst, p, src, r)
	dst.touch(dr)
}

// gfxCovered reports whether a mapped surface other than s covers part
// of r on the output.
func (c *Client) gfxCovered(s *surface, r image.Rectangle) bool {
	for _, m := range c.gfx.mapped {
		if m != s && m.bounds().Add(m.at).Overlaps(r) {
			return true
		}
	}
	return false
}

// gfxWireToSurface1 applies RDPGFX_WIRE_TO_SURFACE_PDU_1: a bitmap
// decoded onto its rectangle of the surface, which it must lie within.
// Its alpha, if any, is dropped. A bitmap in a codec the client does not
// take is counted and skipped.
func (c *Client) gfxWireToSurface1(r *reader) error {
	id, codec := r.u16(), int(r.u16())
	r.u8() // pixelFormat: the surfaces are opaque either way
	dr, err := rect16(r)
	if err != nil {
		return err
	}
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
	if !dr.In(s.bounds()) {
		return fmt.Errorf("bitmap at %v outside surface %d of %d×%d", dr, id, s.w, s.h)
	}
	if codec != gfxCodecUncompressed && codec != gfxCodecPlanar {
		name, ok := gfxCodecNames[codec]
		if !ok {
			name = "GFX bitmaps of unknown codecs"
		}
		c.count(func(s *Stats) { s.Ignored[name]++ })
		return nil
	}
	if dr.Empty() {
		c.count(func(s *Stats) { s.Ignored["empty bitmaps"]++ })
		return nil
	}
	w, h := dr.Dx(), dr.Dy()
	if err := c.pace(w * h); err != nil {
		return err
	}
	dst := s.pix[s.offset(dr.Min.X, dr.Min.Y):]
	stride := 4 * s.w
	kind := "gfx planar"
	if codec == gfxCodecUncompressed {
		kind = "gfx uncompressed"
		if int64(len(data)) < 4*int64(w)*int64(h) {
			return fmt.Errorf("uncompressed bitmap of %d×%d in %d bytes", w, h, len(data))
		}
		for y := range h {
			from, row := data[4*w*y:][:4*w], dst[stride*y:][:4*w]
			for x := 0; x+4 <= len(row); x += 4 {
				// B, G, R and X or A, to R, G, B and opaque.
				v := binary.LittleEndian.Uint32(from[x:])
				binary.LittleEndian.PutUint32(row[x:], v>>16&0xff|v&0xff00|(v&0xff)<<16|0xff000000)
			}
		}
	} else if err := c.dec.planarDecode(data, w, h, dst, stride, planarGFX); err != nil {
		return err
	}
	s.touch(dr)
	c.count(func(s *Stats) { s.Bitmaps[kind]++ })
	return nil
}

// gfxFlush copies what changed on the mapped surfaces to the output, in
// the order of their mapping. Without an output, during the connection
// sequence, the changes wait.
func (c *Client) gfxFlush(out Output) {
	if out == nil {
		return
	}
	w, h := c.Size()
	desk := image.Rect(0, 0, w, h)
	for _, s := range c.gfx.mapped {
		c.gfxFlushSurface(out, s, desk)
	}
}

// gfxFlushSurface copies the cells of s that changed to the output desk,
// a run of cells of a row at a time.
func (c *Client) gfxFlushSurface(out Output, s *surface, desk image.Rectangle) {
	for cy := s.drect.Min.Y; cy < s.drect.Max.Y; cy++ {
		row := s.dirty[cy*s.cols : (cy+1)*s.cols]
		for cx := s.drect.Min.X; cx < s.drect.Max.X; {
			if !row[cx] {
				cx++
				continue
			}
			x0 := cx
			for ; cx < s.drect.Max.X && row[cx]; cx++ {
				row[cx] = false
			}
			r := image.Rect(x0*gfxCell, cy*gfxCell, cx*gfxCell, (cy+1)*gfxCell).Intersect(s.bounds())
			r = r.Add(s.at).Intersect(desk)
			if r.Empty() {
				continue
			}
			// In strips of whole rows, so that the output's lock is held
			// for at most drawStrip pixels at a time.
			rows := max(1, drawStrip/r.Dx())
			for y0 := r.Min.Y; y0 < r.Max.Y; y0 += rows {
				drawSurface(out, s, image.Rect(r.Min.X, y0, r.Max.X, min(y0+rows, r.Max.Y)))
			}
		}
	}
	s.drect = image.Rectangle{}
}

// drawSurface copies r, on the output, of the mapped surface s to out
// under one lock, released by defer as drawRows releases it.
func drawSurface(out Output, s *surface, r image.Rectangle) {
	img := out.Lock(r)
	defer out.Unlock(r)
	n := 4 * r.Dx()
	for y := r.Min.Y; y < r.Max.Y; y++ {
		copy(img.Pix[img.PixOffset(r.Min.X, y):][:n], s.pix[s.offset(r.Min.X-s.at.X, y-s.at.Y):][:n])
	}
}
