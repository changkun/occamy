// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package rdp

import (
	"bytes"
	"path/filepath"
	"testing"
)

// The fuzzers feed the parsers and decoders what a hostile server could
// send; they must return errors, not panic, overrun or hang. Their seeds
// are the PDUs xrdp sent in the recordings.

// traceData splits the recordings into the data of fast-path PDUs, the
// data of slow-path Send Data Indications, and the bitmaps of both.
func traceData(t testing.TB) (fast, slow [][]byte, bitmaps []bitmapData) {
	names, err := filepath.Glob("testdata/xrdp-*.trace.gz")
	if err != nil || len(names) == 0 {
		t.Fatalf("no recordings: %v", err)
	}
	keep := func(b *bitmapData) error {
		c := *b
		c.data = bytes.Clone(b.data)
		bitmaps = append(bitmaps, c)
		return nil
	}
	for _, name := range names {
		for _, fr := range loadTrace(t, name) {
			if fr[0]&0x3 == 0 { // fast-path
				data := fr[2:]
				if fr[1]&0x80 != 0 {
					data = fr[3:]
				}
				fast = append(fast, data)
				var frag []byte
				r := reader{b: data}
				for r.len() > 0 && r.err == nil {
					hdr := r.u8()
					p := r.take(int(r.u16()))
					switch hdr >> 4 & 0x3 {
					case fragFirst:
						frag = append(frag[:0], p...)
						continue
					case fragNext:
						frag = append(frag, p...)
						continue
					case fragLast:
						p = append(frag, p...)
					}
					if hdr&0xf == fpBitmap && len(p) > 2 {
						parseBitmapUpdate(p[2:], keep)
					}
				}
				continue
			}
			if len(fr) < 8 || fr[5] != 0xf0 {
				continue // the connection confirm
			}
			_, data, err := parseDomainPDU(fr[7:])
			if err != nil {
				continue
			}
			slow = append(slow, data)
			r := reader{b: data}
			for r.len() >= 6 {
				total := int(r.u16())
				typ := r.u16() & 0xf
				if total < 6 || total-4 > r.len() {
					break
				}
				body := r.take(total - 4)[2:]
				if typ != pduData || len(body) < 14 || body[8] != pdu2Update || body[12] != updateBitmap {
					continue
				}
				parseBitmapUpdate(body[14:], keep)
			}
		}
	}
	return fast, slow, bitmaps
}

// The update fuzzers draw on a desktop as wide as the recordings', so
// that xrdp's full-width rows fit, and short, so that the bucket of
// pixels the client decodes before it would wait, and with it an
// execution, stays small: the test client fails where it would wait.
// Rows below the desktop are not decoded.
const fuzzWidth, fuzzHeight = 1024, 64

// singleUpdates returns the fast-path updates of the recordings, the
// fragmented ones reassembled, each as a PDU of its own, since xrdp
// fragments most bitmap updates over several PDUs.
func singleUpdates(fast [][]byte) [][]byte {
	var pdus [][]byte
	var frag []byte
	for _, data := range fast {
		r := reader{b: data}
		for r.len() > 0 && r.err == nil {
			hdr := r.u8()
			p := r.take(int(r.u16()))
			switch hdr >> 4 & 0x3 {
			case fragFirst:
				frag = append(frag[:0], p...)
				continue
			case fragNext:
				frag = append(frag, p...)
				continue
			case fragLast:
				p = append(frag, p...)
			}
			if len(p) <= 0xffff {
				var w writer
				w.u8(int(hdr & 0xf))
				w.u16(len(p))
				w.bytes(p)
				pdus = append(pdus, w.b)
			}
		}
	}
	return pdus
}

func FuzzFastPath(f *testing.F) {
	fast, _, _ := traceData(f)
	for _, d := range append(fast, singleUpdates(fast)...) {
		f.Add(d)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		c := testClient(fuzzWidth, fuzzHeight)
		scr := &screen{}
		scr.Resize(fuzzWidth, fuzzHeight)
		c.handleFastPath(scr, frame{fastPath: true, data: data})
	})
}

func FuzzSlowPath(f *testing.F) {
	_, slow, _ := traceData(f)
	for _, d := range slow {
		f.Add(d)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		c := testClient(fuzzWidth, fuzzHeight)
		scr := &screen{}
		scr.Resize(fuzzWidth, fuzzHeight)
		c.handleShareControl(scr, data)
	})
}

func FuzzRLE(f *testing.F) {
	_, _, bitmaps := traceData(f)
	for _, b := range bitmaps {
		if b.compressed && b.bpp < 32 {
			f.Add(b.data, uint8(b.width), uint8(b.height), uint8(b.bpp))
		}
	}
	f.Fuzz(func(t *testing.T, data []byte, w, h, bpp uint8) {
		if w == 0 || h == 0 {
			return
		}
		dst := make([]uint32, int(w)*int(h))
		rleDecode(data, dst, int(w), int(bpp))
	})
}

func FuzzPlanar(f *testing.F) {
	_, _, bitmaps := traceData(f)
	for _, b := range bitmaps {
		if b.compressed && b.bpp == 32 {
			f.Add(b.data, uint8(b.width), uint8(b.height), false)
		}
	}
	// The graphics pipeline's, in AYCoCg with colour loss and chroma
	// subsampling.
	img := gradient(13, 7)
	for _, hdr := range []byte{planarNA | 3, planarNA | planarCS | 2, planarRLE | planarCS | 7, planarRLE | 1} {
		f.Add(planarYCoCg(img, hdr), uint8(13), uint8(7), true)
	}
	f.Fuzz(func(t *testing.T, data []byte, w, h uint8, gfx bool) {
		if w == 0 || h == 0 {
			return
		}
		var d decoder
		kind, stride := planarBitmap, 4*int(w)
		if gfx {
			// Into a surface wider than the bitmap.
			kind, stride = planarGFX, stride+4*int(h)
		}
		d.planarDecode(data, int(w), int(h), make([]byte, stride*int(h)), stride, kind)
	})
}

func FuzzPointer(f *testing.F) {
	// A 2×2 pointer at 24 bits per pixel, as TS_POINTERATTRIBUTE.
	var w writer
	w.u16(24)
	w.u16(0)
	w.u16(1)
	w.u16(1)
	w.u16(2)
	w.u16(2)
	w.u16(4)
	w.u16(12)
	w.zero(16)
	f.Add(w.b, true, false)
	f.Fuzz(func(t *testing.T, data []byte, withBpp, large bool) {
		a, err := parsePointerAttribute(data, withBpp, large)
		if err != nil {
			return
		}
		var d decoder
		if _, err := d.pointerImage(a.w, a.h, a.bpp, a.xorMask, a.andMask); err != nil {
			t.Fatalf("a shape that parsed did not decode: %v", err)
		}
	})
}
