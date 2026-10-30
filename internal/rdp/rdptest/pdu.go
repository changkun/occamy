// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// Package rdptest is a fake RDP server for tests: the server's PDUs of
// the connection sequence and of a session, a Server that runs the
// connection sequence over TLS, and a parser for what the client sends
// after it. It writes the PDUs from [MS-RDPBCGR] on its own and does not
// import internal/rdp, so that the client's tests can use it too.
//
// The PDUs are what a client needs to see, not all a server sends: the
// server data blocks hold the core, security and network blocks, the
// Demand Active the general, bitmap and input capability sets, and
// licensing is the STATUS_VALID_CLIENT shortcut. CredSSP is not
// implemented: a Server that selects NLA stops at the client's first
// TSRequest.
package rdptest

import (
	"encoding/binary"
	"image"
	"image/color"
)

// The security protocols of [MS-RDPBCGR] 2.2.1.1.1.
const (
	ProtocolSSL      uint32 = 0x1
	ProtocolHybrid   uint32 = 0x2
	ProtocolHybridEx uint32 = 0x8
)

// The channels and user of a Server's connections.
const (
	// IOChannel is the MCS I/O channel.
	IOChannel uint16 = 1003
	// User is the client's MCS user channel.
	User uint16 = 1007
	// serverChannel is the server's channel, the source of its share
	// control PDUs.
	serverChannel uint16 = 1002
	// baseUser is what PER subtracts from user IDs.
	baseUser uint16 = 1001
)

// Input flags of the input capability set, [MS-RDPBCGR] 2.2.7.1.6.
const (
	InputScancodes      uint16 = 0x0001
	InputMouseX         uint16 = 0x0004
	InputFastPath       uint16 = 0x0008
	InputUnicode        uint16 = 0x0010
	InputFastPath2      uint16 = 0x0020
	DefaultInputFlags          = InputScancodes | InputMouseX | InputFastPath | InputUnicode | InputFastPath2
	shareControlVersion        = 0x10
)

// Share control and share data PDU types, [MS-RDPBCGR] 2.2.8.1.1.1.
const (
	pduDemandActive  = 0x1
	pduConfirmActive = 0x3
	pduDeactivateAll = 0x6
	pduData          = 0x7

	PDU2Update         = 0x02
	PDU2Control        = 0x14
	PDU2Pointer        = 0x1b
	PDU2Input          = 0x1c
	PDU2Synchronize    = 0x1f
	PDU2RefreshRect    = 0x21
	PDU2SuppressOutput = 0x23
	PDU2FontList       = 0x27
	PDU2FontMap        = 0x28
	PDU2SetErrorInfo   = 0x2f
)

// Fast-path update codes, [MS-RDPBCGR] 2.2.9.1.2.1.
const (
	FPBitmap       = 0x1
	FPPtrNull      = 0x5
	FPPtrDefault   = 0x6
	FPPtrPosition  = 0x8
	FPColorPointer = 0x9
	FPCachedPtr    = 0xa
	FPPointer      = 0xb
)

// w appends little-endian fields.
type w []byte

func (b *w) u8(v uint8)   { *b = append(*b, v) }
func (b *w) u16(v uint16) { *b = binary.LittleEndian.AppendUint16(*b, v) }
func (b *w) u32(v uint32) { *b = binary.LittleEndian.AppendUint32(*b, v) }
func (b *w) be16(v uint16) {
	*b = binary.BigEndian.AppendUint16(*b, v)
}
func (b *w) bytes(p []byte) { *b = append(*b, p...) }

// n16 converts a length or coordinate that a test keeps in range.
func n16(n int) uint16 { return uint16(n) } // #nosec G115 -- tests keep values in range

// perLen appends an ALIGNED PER length.
func perLen(b []byte, n int) []byte {
	if n < 0x80 {
		return append(b, byte(n)) // #nosec G115 -- below 0x80
	}
	return append(b, 0x80|byte(n>>8), byte(n)) // #nosec G115 -- below 32 KiB
}

// berLen appends a BER length.
func berLen(b []byte, n int) []byte {
	switch {
	case n < 0x80:
		return append(b, byte(n)) // #nosec G115 -- below 0x80
	case n < 0x100:
		return append(b, 0x81, byte(n))
	}
	return append(b, 0x82, byte(n>>8), byte(n)) // #nosec G115 -- below 64 KiB
}

// TPKT wraps an X.224 Data TPDU's payload in its headers.
func TPKT(payload []byte) []byte {
	n := 7 + len(payload)
	b := []byte{0x03, 0x00, byte(n >> 8), byte(n), 0x02, 0xf0, 0x80} // #nosec G115 -- below 64 KiB
	return append(b, payload...)
}

// ConnectionConfirm is the X.224 Connection Confirm, [MS-RDPBCGR] 2.2.1.2,
// selecting protocol.
func ConnectionConfirm(protocol uint32) []byte {
	b := w{0x03, 0x00, 0x00, 19, 14, 0xd0, 0, 0, 0x12, 0x34, 0}
	b.u8(0x02) // TYPE_RDP_NEG_RSP
	b.u8(0)
	b.u16(8)
	b.u32(protocol)
	return b
}

// ConnectResponse is the MCS Connect Response with the GCC Conference
// Create Response, [MS-RDPBCGR] 2.2.1.4, whose server core data block
// says the server was offered the protocols requested.
func ConnectResponse(requested uint32) []byte {
	var data w
	data.u16(0x0c01) // TS_UD_SC_CORE
	data.u16(16)
	data.u32(0x00080004) // version
	data.u32(requested)  // clientRequestedProtocols
	data.u32(0)          // earlyCapabilityFlags
	data.u16(0x0c02)     // TS_UD_SC_SEC1: no encryption under TLS
	data.u16(12)
	data.u32(0)
	data.u32(0)
	data.u16(0x0c03) // TS_UD_SC_NET: the I/O channel, no others
	data.u16(8)
	data.u16(IOChannel)
	data.u16(0)

	// The GCC Conference Create Response as in [MS-RDPBCGR] 4.1.4.
	ccr := []byte{0x14, 0x76, 0x0a, 0x01, 0x01, 0x00, 0x01, 0xc0, 0x00}
	ccr = append(ccr, "McDn"...)
	ccr = perLen(ccr, len(data))
	ccr = append(ccr, data...)
	gcc := []byte{0x00, 0x05, 0x00, 0x14, 0x7c, 0x00, 0x01}
	gcc = perLen(gcc, len(ccr))
	gcc = append(gcc, ccr...)

	var v []byte
	v = append(v, 0x0a, 0x01, 0x00) // result: rt-successful
	v = append(v, 0x02, 0x01, 0x00) // calledConnectId
	params := []byte{0x02, 0x01, 0x22, 0x02, 0x01, 0x03, 0x02, 0x01, 0x00, 0x02, 0x01, 0x01,
		0x02, 0x01, 0x00, 0x02, 0x01, 0x01, 0x02, 0x03, 0x00, 0xff, 0xf8, 0x02, 0x01, 0x02}
	v = append(v, 0x30)
	v = berLen(v, len(params))
	v = append(v, params...)
	v = append(v, 0x04)
	v = berLen(v, len(gcc))
	v = append(v, gcc...)
	b := []byte{0x7f, 0x66}
	b = berLen(b, len(v))
	return TPKT(append(b, v...))
}

// AttachUserConfirm is [MS-RDPBCGR] 2.2.1.7 for User.
func AttachUserConfirm() []byte {
	b := w{0x2e, 0x00}
	b.be16(User - baseUser)
	return TPKT(b)
}

// ChannelJoinConfirm is [MS-RDPBCGR] 2.2.1.9 for channel.
func ChannelJoinConfirm(channel uint16) []byte {
	b := w{0x3e, 0x00}
	b.be16(User - baseUser)
	b.be16(channel)
	b.be16(channel)
	return TPKT(b)
}

// Indication wraps data for channel in an MCS Send Data Indication.
func Indication(channel uint16, data []byte) []byte {
	b := []byte{0x68}
	b = binary.BigEndian.AppendUint16(b, serverChannel-baseUser)
	b = binary.BigEndian.AppendUint16(b, channel)
	b = append(b, 0x70)
	b = perLen(b, len(data))
	return TPKT(append(b, data...))
}

// License is the License Error PDU with STATUS_VALID_CLIENT, the
// server's shortcut through licensing, [MS-RDPBCGR] 2.2.1.12.
func License() []byte {
	var b w
	b.u16(0x0080) // SEC_LICENSE_PKT
	b.u16(0)
	b.u8(0xff) // ERROR_ALERT
	b.u8(0x03) // PREAMBLE_VERSION_3_0
	b.u16(16)
	b.u32(0x00000007) // STATUS_VALID_CLIENT
	b.u32(0x00000002) // ST_NO_TRANSITION
	b.u16(0x0004)     // BB_ERROR_BLOB
	b.u16(0)
	return Indication(IOChannel, b)
}

// shareControl wraps a body in a share control header.
func shareControl(typ uint16, body []byte) []byte {
	var b w
	b.u16(n16(6 + len(body)))
	b.u16(typ | shareControlVersion)
	b.u16(serverChannel)
	b.bytes(body)
	return b
}

// ShareData is a share data PDU of type typ2 on the I/O channel, with
// body after its header.
func ShareData(typ2 uint8, body []byte) []byte {
	var b w
	b.u32(0x000103ea) // shareId
	b.u8(0)
	b.u8(1) // STREAM_LOW
	b.u16(n16(len(body) + 4))
	b.u8(typ2)
	b.u8(0)
	b.u16(0)
	b.bytes(body)
	return Indication(IOChannel, shareControl(pduData, b))
}

// DemandActive is the Demand Active PDU, [MS-RDPBCGR] 2.2.1.13.1, for a
// w×h desktop at 32 bits per pixel that takes the input flags.
func DemandActive(width, height int, inputFlags uint16) []byte {
	var caps w
	caps.u16(0x0001) // general
	caps.u16(24)
	caps.u16(1)
	caps.u16(3)
	caps.u16(0x0200)
	caps.u16(0)
	caps.u16(0)
	caps.u16(0x0001 | 0x0004 | 0x0400) // FASTPATH_OUTPUT, LONG_CREDENTIALS, NO_BITMAP_COMPRESSION_HDR
	caps.u16(0)
	caps.u16(0)
	caps.u16(0)
	caps.u8(1)
	caps.u8(1)
	caps.u16(0x0002) // bitmap
	caps.u16(28)
	caps.u16(32)
	caps.u16(1)
	caps.u16(1)
	caps.u16(1)
	caps.u16(n16(width))
	caps.u16(n16(height))
	caps.u16(0)
	caps.u16(1)
	caps.u16(1)
	caps.u8(0)
	caps.u8(0)
	caps.u16(1)
	caps.u16(0)
	caps.u16(0x000d) // input
	caps.u16(88)
	caps.u16(inputFlags)
	caps.u16(0)
	caps.bytes(make([]byte, 80))

	src := []byte("RDP\x00")
	var b w
	b.u32(0x000103ea) // shareId
	b.u16(n16(len(src)))
	b.u16(n16(len(caps) + 4))
	b.bytes(src)
	b.u16(3)
	b.u16(0)
	b.bytes(caps)
	b.u32(0) // sessionId
	return Indication(IOChannel, shareControl(pduDemandActive, b))
}

// DeactivateAll is the Deactivate All PDU, [MS-RDPBCGR] 2.2.3.1, after
// which a server sends a Demand Active.
func DeactivateAll() []byte {
	var b w
	b.u32(0x000103ea)
	b.u16(1)
	b.u8(0)
	return Indication(IOChannel, shareControl(pduDeactivateAll, b))
}

// Finalization is the server's part of the connection finalization,
// [MS-RDPBCGR] 1.3.1.1 phase 10: Synchronize, Control Cooperate, Control
// Granted Control and Font Map.
func Finalization() [][]byte {
	var sync w
	sync.u16(1)
	sync.u16(serverChannel)
	control := func(action uint16) []byte {
		var c w
		c.u16(action)
		c.u16(0)
		c.u32(0)
		return ShareData(PDU2Control, c)
	}
	var fontMap w
	fontMap.u16(0)
	fontMap.u16(0)
	fontMap.u16(0x0003)
	fontMap.u16(4)
	return [][]byte{ShareData(PDU2Synchronize, sync), control(0x0004), control(0x0002), ShareData(PDU2FontMap, fontMap)}
}

// SetErrorInfo is the Set Error Info PDU, [MS-RDPBCGR] 2.2.5.1, with a
// reason a server gives before it disconnects.
func SetErrorInfo(code uint32) []byte {
	var b w
	b.u32(code)
	return ShareData(PDU2SetErrorInfo, b)
}

// DisconnectUltimatum is the MCS Disconnect Provider Ultimatum,
// [MS-RDPBCGR] 2.2.2.3, with the reason rn-user-requested.
func DisconnectUltimatum() []byte {
	return TPKT([]byte{0x21, 0x80})
}

// SlowPathUpdate is a slow-path update PDU, [MS-RDPBCGR] 2.2.9.1.1.3,
// carrying a TS_UPDATE_BITMAP_DATA, as BitmapUpdate makes it.
func SlowPathUpdate(bitmapUpdate []byte) []byte {
	return ShareData(PDU2Update, bitmapUpdate)
}

// Update is one fast-path update, unfragmented, for FastPath.
func Update(code uint8, data []byte) []byte {
	b := w{code}
	b.u16(n16(len(data)))
	return append(b, data...)
}

// MaxFastPath is the most bytes of updates a fast-path PDU carries:
// its length has 15 bits, and counts the header's 3 bytes.
const MaxFastPath = 0x7fff - 3

// FastPath is a fast-path update PDU, [MS-RDPBCGR] 2.2.9.1.2, of the
// updates Update and Fragments make. They must fit MaxFastPath.
func FastPath(updates ...[]byte) []byte {
	var data []byte
	for _, u := range updates {
		data = append(data, u...)
	}
	n := len(data) + 3
	return append([]byte{0x00, 0x80 | byte(n>>8), byte(n)}, data...) // #nosec G115 -- below 32 KiB
}

// Fragments splits a fast-path update into PDUs of at most size bytes of
// data each, with the fragmentation flags of [MS-RDPBCGR] 2.2.9.1.2.1.
func Fragments(code uint8, data []byte, size int) [][]byte {
	if len(data) <= size {
		return [][]byte{FastPath(Update(code, data))}
	}
	var pdus [][]byte
	for i := 0; i < len(data); i += size {
		frag := uint8(0x3) // FASTPATH_FRAGMENT_NEXT
		switch {
		case i == 0:
			frag = 0x2 // FIRST
		case i+size >= len(data):
			frag = 0x1 // LAST
		}
		pdus = append(pdus, FastPath(Update(code|frag<<4, data[i:min(i+size, len(data))])))
	}
	return pdus
}

// RawBitmap is an uncompressed TS_BITMAP_DATA, [MS-RDPBCGR]
// 2.2.9.1.1.3.1.2.2, of img at 32 bits per pixel, drawn at at.
func RawBitmap(at image.Point, img *image.RGBA) []byte {
	width, height := img.Rect.Dx(), img.Rect.Dy()
	var b w
	b.u16(n16(at.X))
	b.u16(n16(at.Y))
	b.u16(n16(at.X + width - 1))
	b.u16(n16(at.Y + height - 1))
	b.u16(n16(width))
	b.u16(n16(height))
	b.u16(32)
	b.u16(0) // no compression
	b.u16(n16(4 * width * height))
	// Rows bottom-up, each pixel blue, green, red and a pad byte.
	for y := img.Rect.Max.Y - 1; y >= img.Rect.Min.Y; y-- {
		for x := img.Rect.Min.X; x < img.Rect.Max.X; x++ {
			c := img.RGBAAt(x, y)
			b.bytes([]byte{c.B, c.G, c.R, 0})
		}
	}
	return b
}

// FillBitmap is a TS_BITMAP_DATA of r filled with c, compressed with
// interleaved RLE at 24 bits per pixel: a run of c of up to 65,535
// pixels at a time, so that it describes a bitmap larger than an
// uncompressed one can be.
func FillBitmap(r image.Rectangle, c color.RGBA) []byte {
	var data w
	for n := r.Dx() * r.Dy(); n > 0; n -= 0xffff {
		data = append(data, 0xf3) // MEGA_MEGA_COLOR_RUN
		data.u16(n16(min(n, 0xffff)))
		data = append(data, c.B, c.G, c.R)
	}
	var b w
	b.u16(n16(r.Min.X))
	b.u16(n16(r.Min.Y))
	b.u16(n16(r.Max.X - 1))
	b.u16(n16(r.Max.Y - 1))
	b.u16(n16(r.Dx()))
	b.u16(n16(r.Dy()))
	b.u16(24)
	b.u16(0x0001 | 0x0400) // BITMAP_COMPRESSION, NO_BITMAP_COMPRESSION_HDR
	b.u16(n16(len(data)))
	b.bytes(data)
	return b
}

// BitmapUpdate is a TS_UPDATE_BITMAP_DATA of rectangles that RawBitmap
// and FillBitmap make,
// as a fast-path update of code FPBitmap or a slow-path update carries
// it.
func BitmapUpdate(rects ...[]byte) []byte {
	var b w
	b.u16(0x0001) // UPDATETYPE_BITMAP
	b.u16(n16(len(rects)))
	for _, r := range rects {
		b.bytes(r)
	}
	return b
}

// Pointer is a TS_POINTERATTRIBUTE at 32 bits per pixel, [MS-RDPBCGR]
// 2.2.9.1.1.4.5, of img with its hot spot, for cache index i: a
// fast-path update of code FPPointer carries it.
func Pointer(i int, hot image.Point, img *image.RGBA) []byte {
	width, height := img.Rect.Dx(), img.Rect.Dy()
	andStride := (width + 15) / 16 * 2
	var xor []byte
	and := make([]byte, andStride*height)
	for row, y := 0, img.Rect.Max.Y-1; y >= img.Rect.Min.Y; row, y = row+1, y-1 {
		for i, x := 0, img.Rect.Min.X; x < img.Rect.Max.X; i, x = i+1, x+1 {
			c := img.RGBAAt(x, y)
			xor = append(xor, c.B, c.G, c.R, c.A)
			if c.A == 0 {
				// The AND mask's bit makes a pixel without colour
				// transparent.
				and[row*andStride+i/8] |= 0x80 >> (i % 8)
			}
		}
	}
	var b w
	b.u16(32)
	b.u16(n16(i))
	b.u16(n16(hot.X))
	b.u16(n16(hot.Y))
	b.u16(n16(width))
	b.u16(n16(height))
	b.u16(n16(len(and)))
	b.u16(n16(len(xor)))
	b.bytes(xor)
	b.bytes(and)
	return b
}

// CachedPointer is a TS_CACHEDPOINTERATTRIBUTE: a fast-path update of
// code FPCachedPtr carries it.
func CachedPointer(i int) []byte {
	var b w
	b.u16(n16(i))
	return b
}

// PointerPosition is a TS_POINTERPOSATTRIBUTE: a fast-path update of
// code FPPtrPosition carries it.
func PointerPosition(p image.Point) []byte {
	var b w
	b.u16(n16(p.X))
	b.u16(n16(p.Y))
	return b
}
