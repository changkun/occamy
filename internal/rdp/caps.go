// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package rdp

import (
	"fmt"
)

// Capability set types, [MS-RDPBCGR] 2.2.1.13.1.1.1.
const (
	capGeneral        = 0x0001
	capBitmap         = 0x0002
	capOrder          = 0x0003
	capBitmapCache    = 0x0004
	capControl        = 0x0005
	capActivation     = 0x0007
	capPointer        = 0x0008
	capShare          = 0x0009
	capColorCache     = 0x000a
	capSound          = 0x000c
	capInput          = 0x000d
	capFont           = 0x000e
	capBrush          = 0x000f
	capGlyphCache     = 0x0010
	capOffscreenCache = 0x0011
	capVirtualChannel = 0x0014
	capMultifragment  = 0x001a
)

// Flags of the general capability set, [MS-RDPBCGR] 2.2.7.1.1.
const (
	fastPathOutputSupported = 0x0001
	longCredentialsSupport  = 0x0004
	noBitmapCompressionHdr  = 0x0400
)

// Flags of the input capability set, [MS-RDPBCGR] 2.2.7.1.6.
const (
	inputFlagScancodes      = 0x0001
	inputFlagMouseX         = 0x0004
	inputFlagFastPathInput  = 0x0008
	inputFlagUnicode        = 0x0010
	inputFlagFastPathInput2 = 0x0020
)

// serverCaps is what the client takes from the server's Demand Active.
type serverCaps struct {
	width, height int
	bpp           int
	inputFlags    uint16
	extraFlags    uint16 // of the general capability set
}

// parseDemandActive parses a Demand Active PDU after its share control
// header, [MS-RDPBCGR] 2.2.1.13.1.
func parseDemandActive(p []byte) (shareID uint32, caps serverCaps, err error) {
	r := reader{b: p}
	shareID = r.u32()
	srcLen := int(r.u16())
	r.u16() // lengthCombinedCapabilities
	r.skip(srcLen)
	n := int(r.u16())
	r.u16() // pad2Octets
	for range n {
		typ := r.u16()
		l := int(r.u16())
		if r.err == nil && l < 4 {
			return 0, caps, fmt.Errorf("rdp: capability set %d of length %d", typ, l)
		}
		c := reader{b: r.take(l - 4)}
		switch typ {
		case capGeneral:
			c.skip(10)
			caps.extraFlags = c.u16()
		case capBitmap:
			caps.bpp = int(c.u16())
			c.skip(6)
			caps.width = int(c.u16())
			caps.height = int(c.u16())
		case capInput:
			caps.inputFlags = c.u16()
		}
		if c.err != nil {
			return 0, caps, fmt.Errorf("rdp: capability set %d: %w", typ, c.err)
		}
	}
	if r.err != nil {
		return 0, caps, fmt.Errorf("rdp: demand active: %w", r.err)
	}
	if err := checkDesktop(caps.width, caps.height); err != nil {
		return 0, caps, fmt.Errorf("rdp: demand active: %w", err)
	}
	return shareID, caps, nil
}

// capabilities encodes the client's capability sets for the Confirm
// Active PDU. It advertises no drawing orders, no caches and no bitmap
// codecs, so that the server sends bitmap updates only, with interleaved
// RLE at 24 bits per pixel and below and planar at 32
// ([MS-RDPBCGR] 2.2.9.1.1.3.1.2.2).
func (c *Client) capabilities() (n int, b []byte) {
	var w writer
	set := func(typ int, body func()) {
		start := len(w.b)
		w.u16(typ)
		w.u16(0)
		body()
		w.putU16(start+2, len(w.b)-start)
		n++
	}

	set(capGeneral, func() { // [MS-RDPBCGR] 2.2.7.1.1
		w.u16(1)      // osMajorType: Windows
		w.u16(3)      // osMinorType: Windows NT
		w.u16(0x0200) // protocolVersion
		w.u16(0)      // pad2octetsA
		w.u16(0)      // generalCompressionTypes
		w.u16(fastPathOutputSupported | longCredentialsSupport | noBitmapCompressionHdr)
		w.u16(0) // updateCapabilityFlag
		w.u16(0) // remoteUnshareFlag
		w.u16(0) // generalCompressionLevel
		w.u8(1)  // refreshRectSupport
		w.u8(1)  // suppressOutputSupport
	})
	set(capBitmap, func() { // [MS-RDPBCGR] 2.2.7.1.2
		w.u16(c.cfg.ColorDepth)
		w.u16(1) // receive1BitPerPixel
		w.u16(1) // receive4BitsPerPixel
		w.u16(1) // receive8BitsPerPixel
		w.u16(c.width)
		w.u16(c.height)
		w.u16(0) // pad2octets
		w.u16(1) // desktopResizeFlag
		w.u16(1) // bitmapCompressionFlag
		w.u8(0)  // highColorFlags
		w.u8(0x08)
		w.u16(1) // multipleRectangleSupport
		w.u16(0) // pad2octetsB
	})
	set(capOrder, func() { // [MS-RDPBCGR] 2.2.7.1.3
		w.zero(16)                      // terminalDescriptor
		w.u32(0)                        // pad4octetsA
		w.u16(1)                        // desktopSaveXGranularity
		w.u16(20)                       // desktopSaveYGranularity
		w.u16(0)                        // pad2octetsA
		w.u16(1)                        // maximumOrderLevel: ORD_LEVEL_1_ORDERS
		w.u16(0)                        // numberFonts
		w.u16(0x0002 | 0x0008 | 0x0020) // NEGOTIATEORDERSUPPORT, ZEROBOUNDSDELTASSUPPORT, COLORINDEXSUPPORT
		w.zero(32)                      // orderSupport: none
		w.u16(0)                        // textFlags
		w.u16(0)                        // orderSupportExFlags
		w.u32(0)                        // pad4octetsB
		w.u32(0)                        // desktopSaveSize
		w.u16(0)                        // pad2octetsC
		w.u16(0)                        // pad2octetsD
		w.u16(0)                        // textANSICodePage
		w.u16(0)                        // pad2octetsE
	})
	set(capBitmapCache, func() { // revision 1, [MS-RDPBCGR] 2.2.7.1.4.1: no caches
		w.zero(24)
		for range 3 {
			w.u16(0) // entries
			w.u16(0) // maximum cell size
		}
	})
	set(capPointer, func() { // [MS-RDPBCGR] 2.2.7.1.5
		w.u16(1)  // colorPointerFlag
		w.u16(25) // colorPointerCacheSize
		w.u16(25) // pointerCacheSize, which enables the new pointer update
	})
	set(capInput, func() { // [MS-RDPBCGR] 2.2.7.1.6
		w.u16(inputFlagScancodes | inputFlagMouseX | inputFlagFastPathInput | inputFlagUnicode | inputFlagFastPathInput2)
		w.u16(0) // pad2octetsA
		w.u32(c.cfg.KeyboardLayout)
		w.u32(4)  // keyboardType
		w.u32(0)  // keyboardSubType
		w.u32(12) // keyboardFunctionKey
		w.zero(64)
	})
	set(capBrush, func() { w.u32(0) })                                  // BRUSH_DEFAULT
	set(capGlyphCache, func() { w.zero(10*4 + 4); w.u16(0); w.u16(0) }) // GLYPH_SUPPORT_NONE
	set(capOffscreenCache, func() { w.u32(0); w.u16(0); w.u16(0) })
	set(capVirtualChannel, func() { w.u32(0); w.u32(1600) })
	set(capSound, func() { w.u16(0); w.u16(0) })
	set(capShare, func() { w.u16(0); w.u16(0) })
	set(capFont, func() { w.u16(0x0001); w.u16(0) }) // FONTSUPPORT_FONTLIST
	set(capControl, func() { w.u16(0); w.u16(0); w.u16(2); w.u16(2) })
	set(capActivation, func() { w.u16(0); w.u16(0); w.u16(0); w.u16(0) })
	set(capColorCache, func() { w.u16(6); w.u16(0) })
	set(capMultifragment, func() { // [MS-RDPBCGR] 2.2.7.2.6
		w.u32(uint32(min(c.width*c.height*4+0x10000, maxReassembly))) // #nosec G115 -- bounded
	})
	return n, w.b
}
