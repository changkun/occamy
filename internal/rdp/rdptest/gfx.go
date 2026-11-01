// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package rdptest

import (
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
)

// The graphics pipeline ([MS-RDPEGFX]) on a dynamic channel: the
// server's PDUs, a compressor of its bulk data, and parsers for the
// client's PDUs. Nothing here shares code with the client's decoder.

// Graphics is the graphics pipeline's channel name.
const Graphics = "Microsoft::Windows::RDS::Graphics"

// RDPGFX_CMDID values ([MS-RDPEGFX] 2.2.1.5).
const (
	GFXWireToSurface1      = 0x0001
	GFXWireToSurface2      = 0x0002
	GFXDeleteEncodingCtx   = 0x0003
	GFXSolidFill           = 0x0004
	GFXSurfaceToSurface    = 0x0005
	GFXSurfaceToCache      = 0x0006
	GFXCacheToSurface      = 0x0007
	GFXEvictCacheEntry     = 0x0008
	GFXCreateSurface       = 0x0009
	GFXDeleteSurface       = 0x000a
	GFXStartFrame          = 0x000b
	GFXEndFrame            = 0x000c
	GFXFrameAcknowledge    = 0x000d
	GFXResetGraphics       = 0x000e
	GFXMapSurfaceToOutput  = 0x000f
	GFXCacheImportOffer    = 0x0010
	GFXCacheImportReply    = 0x0011
	GFXCapsAdvertise       = 0x0012
	GFXCapsConfirm         = 0x0013
	GFXMapSurfaceToWindow  = 0x0015
	GFXQoEFrameAcknowledge = 0x0016
	GFXMapSurfaceToScaled  = 0x0017
)

// RDPGFX_CODECID values ([MS-RDPEGFX] 2.2.2.1).
const (
	CodecUncompressed = 0x0000
	CodecRemoteFX     = 0x0003
	CodecClearCodec   = 0x0008
	CodecProgressive  = 0x0009
	CodecPlanar       = 0x000a
	CodecAVC420       = 0x000b
	CodecAlpha        = 0x000c
	CodecAVC444       = 0x000e
)

// Pixel formats ([MS-RDPEGFX] 2.2.1.4).
const (
	PixelXRGB = 0x20
	PixelARGB = 0x21
)

// Capability versions ([MS-RDPEGFX] 2.2.3).
const (
	CapVersion8   = 0x00080004
	CapVersion81  = 0x00080105
	CapVersion10  = 0x000a0002
	CapVersion101 = 0x000a0100
	CapVersion102 = 0x000a0200
	CapVersion103 = 0x000a0301
	CapVersion104 = 0x000a0400
	CapVersion105 = 0x000a0502
	CapVersion106 = 0x000a0600
	CapVersion107 = 0x000a0701
)

// GFXPDU is an RDPGFX_HEADER of cmd followed by body.
func GFXPDU(cmd uint16, body []byte) []byte {
	p := binary.LittleEndian.AppendUint16(nil, cmd)
	p = binary.LittleEndian.AppendUint16(p, 0)
	p = binary.LittleEndian.AppendUint32(p, uint32(8+len(body))) // #nosec G115 -- tests send less than 4 GiB
	return append(p, body...)
}

// le builds little-endian fields.
type le []byte

func (b le) u8(v byte) le    { return append(b, v) }
func (b le) u16(v int) le    { return binary.LittleEndian.AppendUint16(b, uint16(v)) } // #nosec G115 -- tests keep fields in range
func (b le) u32(v uint32) le { return binary.LittleEndian.AppendUint32(b, v) }
func (b le) rect(r image.Rectangle) le {
	return b.u16(r.Min.X).u16(r.Min.Y).u16(r.Max.X).u16(r.Max.Y)
}

// CapsConfirm is RDPGFX_CAPS_CONFIRM_PDU of a capability set of version
// with flags.
func CapsConfirm(version, flags uint32) []byte {
	return GFXPDU(GFXCapsConfirm, le{}.u32(version).u32(4).u32(flags))
}

// ResetGraphics is RDPGFX_RESET_GRAPHICS_PDU of an output of w×h with
// the monitors given, padded to 340 bytes.
func ResetGraphics(w, h int, monitors ...image.Rectangle) []byte {
	b := le{}.u32(uint32(w)).u32(uint32(h)).u32(uint32(len(monitors))) // #nosec G115 -- tests keep sizes in range
	for i, m := range monitors {
		var primary uint32
		if i == 0 {
			primary = 1
		}
		// TS_MONITOR_DEF: inclusive bounds.
		b = b.u32(uint32(m.Min.X)).u32(uint32(m.Min.Y)).u32(uint32(m.Max.X - 1)).u32(uint32(m.Max.Y - 1)).u32(primary) // #nosec G115 -- tests keep bounds in range
	}
	for len(b) < 340-8 {
		b = append(b, 0)
	}
	return GFXPDU(GFXResetGraphics, b)
}

// CreateSurface is RDPGFX_CREATE_SURFACE_PDU.
func CreateSurface(id, w, h int, format byte) []byte {
	return GFXPDU(GFXCreateSurface, le{}.u16(id).u16(w).u16(h).u8(format))
}

// DeleteSurface is RDPGFX_DELETE_SURFACE_PDU.
func DeleteSurface(id int) []byte { return GFXPDU(GFXDeleteSurface, le{}.u16(id)) }

// MapSurface is RDPGFX_MAP_SURFACE_TO_OUTPUT_PDU of the surface id at
// the output's point at.
func MapSurface(id int, at image.Point) []byte {
	return GFXPDU(GFXMapSurfaceToOutput, le{}.u16(id).u16(0).u32(uint32(at.X)).u32(uint32(at.Y))) // #nosec G115 -- tests keep points in range
}

// StartFrame is RDPGFX_START_FRAME_PDU of the frame id, without a
// timestamp.
func StartFrame(id uint32) []byte { return GFXPDU(GFXStartFrame, le{}.u32(0).u32(id)) }

// EndFrame is RDPGFX_END_FRAME_PDU of the frame id.
func EndFrame(id uint32) []byte { return GFXPDU(GFXEndFrame, le{}.u32(id)) }

// SolidFill is RDPGFX_SOLIDFILL_PDU of c over rects on the surface id.
func SolidFill(id int, c color.RGBA, rects ...image.Rectangle) []byte {
	b := le{}.u16(id).u8(c.B).u8(c.G).u8(c.R).u8(c.A).u16(len(rects))
	for _, r := range rects {
		b = b.rect(r)
	}
	return GFXPDU(GFXSolidFill, b)
}

// SurfaceToSurface is RDPGFX_SURFACE_TO_SURFACE_PDU of src on the
// surface from to each of pts on the surface to.
func SurfaceToSurface(from, to int, src image.Rectangle, pts ...image.Point) []byte {
	b := le{}.u16(from).u16(to).rect(src).u16(len(pts))
	for _, p := range pts {
		b = b.u16(p.X).u16(p.Y)
	}
	return GFXPDU(GFXSurfaceToSurface, b)
}

// WireToSurface1 is RDPGFX_WIRE_TO_SURFACE_PDU_1 of data in codec for r
// on the surface id.
func WireToSurface1(id, codec int, format byte, r image.Rectangle, data []byte) []byte {
	b := le{}.u16(id).u16(codec).u8(format).rect(r).u32(uint32(len(data))) // #nosec G115 -- tests send less than 4 GiB
	return GFXPDU(GFXWireToSurface1, append(b, data...))
}

// WireToSurface2 is RDPGFX_WIRE_TO_SURFACE_PDU_2 of data in codec in the
// codec context ctx on the surface id.
func WireToSurface2(id, codec int, ctx uint32, format byte, data []byte) []byte {
	b := le{}.u16(id).u16(codec).u32(ctx).u8(format).u32(uint32(len(data))) // #nosec G115 -- tests send less than 4 GiB
	return GFXPDU(GFXWireToSurface2, append(b, data...))
}

// Uncompressed is the uncompressed codec's bitmap of img: 32-bit pixels
// of blue, green, red and alpha bytes, row by row.
func Uncompressed(img *image.RGBA) []byte {
	var b []byte
	r := img.Rect
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			c := img.RGBAAt(x, y)
			b = append(b, c.B, c.G, c.R, c.A)
		}
	}
	return b
}

// Planar is img in the planar codec ([MS-RDPEGDI] 2.2.2.5.1) with raw
// planes, no alpha plane and the rows top-down, as the graphics pipeline
// stores them.
func Planar(img *image.RGBA) []byte {
	b := []byte{0x20} // NA
	r := img.Rect
	for c := range 3 {
		for y := r.Min.Y; y < r.Max.Y; y++ {
			for x := r.Min.X; x < r.Max.X; x++ {
				b = append(b, img.Pix[img.PixOffset(x, y)+c])
			}
		}
	}
	return append(b, 0) // pad
}

// GFX is a PDU of the graphics pipeline a client sent.
type GFX struct {
	Cmd  uint16
	Body []byte
}

// ParseGFX parses the PDUs of a message a client sent on the graphics
// channel, which is neither segmented nor compressed.
func ParseGFX(p []byte) ([]GFX, error) {
	var pdus []GFX
	for len(p) > 0 {
		if len(p) < 8 {
			return nil, errors.New("rdptest: short RDPGFX_HEADER")
		}
		cmd, flags, n := binary.LittleEndian.Uint16(p), binary.LittleEndian.Uint16(p[2:]), binary.LittleEndian.Uint32(p[4:])
		if flags != 0 || n < 8 || int64(n) > int64(len(p)) {
			return nil, fmt.Errorf("rdptest: RDPGFX_HEADER of command %#x, flags %#x and length %d in %d bytes", cmd, flags, n, len(p))
		}
		pdus = append(pdus, GFX{Cmd: cmd, Body: p[8:n]})
		p = p[n:]
	}
	return pdus, nil
}

// GFXCaps is a capability set of RDPGFX_CAPS_ADVERTISE_PDU.
type GFXCaps struct {
	Version, Flags uint32
	Data           []byte
}

// ParseCapsAdvertise parses the body of RDPGFX_CAPS_ADVERTISE_PDU.
func ParseCapsAdvertise(b []byte) ([]GFXCaps, error) {
	if len(b) < 2 {
		return nil, errors.New("rdptest: short caps advertise")
	}
	n := int(binary.LittleEndian.Uint16(b))
	b = b[2:]
	var sets []GFXCaps
	for range n {
		if len(b) < 8 {
			return nil, errors.New("rdptest: short capability set")
		}
		v, l := binary.LittleEndian.Uint32(b), binary.LittleEndian.Uint32(b[4:])
		if int64(l) > int64(len(b)-8) {
			return nil, fmt.Errorf("rdptest: capability set %#x of %d bytes", v, l)
		}
		s := GFXCaps{Version: v, Data: b[8 : 8+l]}
		if l >= 4 {
			s.Flags = binary.LittleEndian.Uint32(s.Data)
		}
		sets = append(sets, s)
		b = b[8+l:]
	}
	if len(b) != 0 {
		return nil, fmt.Errorf("rdptest: %d bytes after the capability sets", len(b))
	}
	return sets, nil
}

// FrameAck is RDPGFX_FRAME_ACKNOWLEDGE_PDU.
type FrameAck struct {
	QueueDepth, FrameID, TotalFramesDecoded uint32
}

// ParseFrameAck parses the body of RDPGFX_FRAME_ACKNOWLEDGE_PDU.
func ParseFrameAck(b []byte) (FrameAck, error) {
	if len(b) != 12 {
		return FrameAck{}, fmt.Errorf("rdptest: frame acknowledge of %d bytes", len(b))
	}
	return FrameAck{binary.LittleEndian.Uint32(b), binary.LittleEndian.Uint32(b[4:]), binary.LittleEndian.Uint32(b[8:])}, nil
}

// ZGFX compresses a graphics channel's messages ([MS-RDPEGFX] 3.1.9.1)
// as a server does, keeping the history the client's decompressor
// mirrors. It looks for matches greedily and keeps all it has written,
// which suits tests: it is correct, not good.
type ZGFX struct {
	hist []byte
	last map[uint32]int // where each 3 bytes were last seen in hist
	// Stored, if set, stores runs of literals of at least this many bytes
	// instead of coding them one by one.
	Stored int
}

// Segment kinds for Message.
const (
	SegStored     = iota // the segment's bytes as they are
	SegCompressed        // coded
)

// Single is msg, at most 65,535 bytes, as RDP_SEGMENTED_DATA of one
// segment of kind.
func (z *ZGFX) Single(msg []byte, kind int) []byte {
	return append([]byte{0xe0}, z.segment(msg, kind)...)
}

// Multipart is msg as RDP_SEGMENTED_DATA of segments of at most size
// bytes, of the kinds given in turn.
func (z *ZGFX) Multipart(msg []byte, size int, kinds ...int) []byte {
	var segs [][]byte
	for i := 0; i < len(msg) || i == 0; i += size {
		segs = append(segs, z.segment(msg[i:min(i+size, len(msg))], kinds[len(segs)%len(kinds)]))
	}
	b := le{0xe1}.u16(len(segs)).u32(uint32(len(msg))) // #nosec G115 -- tests send less than 4 GiB
	for _, s := range segs {
		b = b.u32(uint32(len(s))) // #nosec G115 -- a segment
		b = append(b, s...)
	}
	return b
}

// segment is RDP8_BULK_ENCODED_DATA of msg.
func (z *ZGFX) segment(msg []byte, kind int) []byte {
	if z.last == nil {
		z.last = map[uint32]int{}
	}
	if kind == SegStored {
		for _, c := range msg {
			z.add(c)
		}
		return append([]byte{0x04}, msg...)
	}
	var w bitWriter
	lits := 0 // literals pending, for stored runs
	flush := func(end int) {
		if lits == 0 {
			return
		}
		run := msg[end-lits : end]
		if z.Stored > 0 && lits >= z.Stored {
			for len(run) > 0 {
				k := min(len(run), 0x7fff)
				w.bits(0b10001, 5)
				w.bits(0, 5)
				w.bits(uint32(k), 15) // #nosec G115 -- 15 bits
				w.align()
				w.b = append(w.b, run[:k]...)
				w.align()
				run = run[k:]
			}
		} else {
			for _, c := range run {
				literal(&w, c)
			}
		}
		lits = 0
	}
	for i := 0; i < len(msg); {
		dist, n := z.find(msg[i:])
		if n < 3 {
			z.add(msg[i])
			lits++
			i++
			if z.Stored == 0 {
				flush(i)
			}
			continue
		}
		flush(i)
		matchToken(&w, dist)
		length(&w, n)
		for _, c := range msg[i : i+n] {
			z.add(c)
		}
		i += n
	}
	flush(len(msg))
	return append(append([]byte{0x24}, w.b...), byte((8-w.n%8)%8))
}

// add appends c to the history, as the segment goes, and indexes the 3
// bytes it ends.
func (z *ZGFX) add(c byte) {
	z.hist = append(z.hist, c)
	if n := len(z.hist); n >= 3 {
		z.last[uint32(z.hist[n-3])<<16|uint32(z.hist[n-2])<<8|uint32(c)] = n - 3
	}
}

// find returns the distance and length of the longest match of the
// history's last occurrence of p's first 3 bytes, up to 2,500,000 bytes
// back.
func (z *ZGFX) find(p []byte) (dist, n int) {
	if len(p) < 3 {
		return 0, 0
	}
	at, ok := z.last[uint32(p[0])<<16|uint32(p[1])<<8|uint32(p[2])]
	if !ok || len(z.hist)-at > 2500000 {
		return 0, 0
	}
	dist = len(z.hist) - at
	// The match may run past the history into what it copies.
	for n < len(p) && n < 65535 {
		var c byte
		if at+n < len(z.hist) {
			c = z.hist[at+n]
		} else {
			c = p[at+n-len(z.hist)]
		}
		if c != p[n] {
			break
		}
		n++
	}
	return dist, n
}

// shortLiterals are the literals with codes shorter than 9 bits.
var shortLiterals = map[byte][2]uint32{
	0x00: {0b11000, 5}, 0x01: {0b11001, 5},
	0x02: {0b110100, 6}, 0x03: {0b110101, 6}, 0xff: {0b110110, 6},
	0x04: {0b1101110, 7}, 0x05: {0b1101111, 7}, 0x06: {0b1110000, 7}, 0x07: {0b1110001, 7},
	0x08: {0b1110010, 7}, 0x09: {0b1110011, 7}, 0x0a: {0b1110100, 7}, 0x0b: {0b1110101, 7},
	0x3a: {0b1110110, 7}, 0x3b: {0b1110111, 7}, 0x3c: {0b1111000, 7}, 0x3d: {0b1111001, 7},
	0x3e: {0b1111010, 7}, 0x3f: {0b1111011, 7}, 0x40: {0b1111100, 7}, 0x80: {0b1111101, 7},
	0x0c: {0b11111100, 8}, 0x38: {0b11111101, 8}, 0x39: {0b11111110, 8}, 0x66: {0b11111111, 8},
}

func literal(w *bitWriter, c byte) {
	if s, ok := shortLiterals[c]; ok {
		w.bits(s[0], int(s[1]))
		return
	}
	w.bits(uint32(c), 9) // a 0, then the byte
}

// matchTokens are the match codes: prefix, its bits, value bits, base.
var matchTokens = [][4]uint32{
	{0b10001, 5, 5, 0}, {0b10010, 5, 7, 32}, {0b10011, 5, 9, 160}, {0b10100, 5, 10, 672},
	{0b10101, 5, 12, 1696}, {0b101100, 6, 14, 5792}, {0b101101, 6, 15, 22176},
	{0b1011100, 7, 18, 54944}, {0b1011101, 7, 20, 317088},
	{0b10111100, 8, 20, 1365664}, {0b10111101, 8, 21, 2414240},
}

func matchToken(w *bitWriter, dist int) {
	for _, t := range matchTokens {
		if uint32(dist) < t[3]+1<<t[2] { // #nosec G115 -- at most 2,500,000
			w.bits(t[0], int(t[1]))
			w.bits(uint32(dist)-t[3], int(t[2])) // #nosec G115 -- at most 2,500,000
			return
		}
	}
	panic("rdptest: match distance past the history")
}

func length(w *bitWriter, n int) {
	if n == 3 {
		w.bits(0, 1)
		return
	}
	k := 0
	for 4<<(k+1) <= n {
		k++
	}
	w.bits(uint32(1)<<(k+1)-1, k+1) // k+1 ones
	w.bits(0, 1)
	w.bits(uint32(n-4<<k), k+2) // #nosec G115 -- at most 65,535
}

// bitWriter writes bits, the most significant first.
type bitWriter struct {
	b []byte
	n int // bits written
}

func (w *bitWriter) bits(v uint32, n int) {
	for i := n - 1; i >= 0; i-- {
		if w.n%8 == 0 {
			w.b = append(w.b, 0)
		}
		if v>>i&1 != 0 {
			w.b[len(w.b)-1] |= 0x80 >> (w.n % 8)
		}
		w.n++
	}
}

func (w *bitWriter) align() { w.n = 8 * len(w.b) }

// SendGFX sends msg, RDP_SEGMENTED_DATA, on the graphics channel id, in
// DRDYNVC PDUs of at most 1,500 bytes of data, as xrdp does.
func (c *Conn) SendGFX(id uint32, msg []byte) error {
	return c.SendDVC(DVCMessage(id, msg, 1500)...)
}
