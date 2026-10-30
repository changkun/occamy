// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package rdp

import (
	"errors"
	"fmt"
)

// The interleaved RLE decoder of [MS-RDPBCGR] 2.2.9.1.1.3.1.2.4, written
// from the specification's description and pseudocode in 3.1.9.

// Order codes of the RLE bitmap stream.
const (
	regularBgRun      = 0x0
	regularFgRun      = 0x1
	regularFgBgImage  = 0x2
	regularColorRun   = 0x3
	regularColorImage = 0x4

	liteSetFgFgRun     = 0xc
	liteSetFgFgBgImage = 0xd
	liteDitheredRun    = 0xe

	megaMegaBgRun        = 0xf0
	megaMegaFgRun        = 0xf1
	megaMegaFgBgImage    = 0xf2
	megaMegaColorRun     = 0xf3
	megaMegaColorImage   = 0xf4
	megaMegaSetFgRun     = 0xf6
	megaMegaSetFgBgImage = 0xf7
	megaMegaDitheredRun  = 0xf8

	specialFgBg1 = 0xf9
	specialFgBg2 = 0xfa
	specialWhite = 0xfd
	specialBlack = 0xfe
)

var errRLEOverrun = errors.New("rdp: RLE bitmap overruns its size")

// rleCode returns the order code of an order header, ExtractCodeId.
func rleCode(h byte) int {
	switch h {
	case megaMegaBgRun, megaMegaFgRun, megaMegaFgBgImage, megaMegaColorRun,
		megaMegaColorImage, megaMegaSetFgRun, megaMegaSetFgBgImage,
		megaMegaDitheredRun, specialFgBg1, specialFgBg2, specialWhite, specialBlack:
		return int(h)
	}
	if c := int(h >> 5); c <= regularColorImage {
		return c
	}
	return int(h >> 4)
}

// rleRun returns an order's run length and the size of its header,
// ExtractRunLength.
func rleRun(code int, src []byte) (run, n int, ok bool) {
	switch code {
	case regularFgBgImage, liteSetFgFgBgImage:
		mask := byte(0x1f)
		if code == liteSetFgFgBgImage {
			mask = 0x0f
		}
		if run = int(src[0] & mask); run != 0 {
			return run * 8, 1, true
		}
		if len(src) < 2 {
			return 0, 0, false
		}
		return int(src[1]) + 1, 2, true
	case regularBgRun, regularFgRun, regularColorRun, regularColorImage:
		if run = int(src[0] & 0x1f); run != 0 {
			return run, 1, true
		}
		if len(src) < 2 {
			return 0, 0, false
		}
		return int(src[1]) + 32, 2, true
	case liteSetFgFgRun, liteDitheredRun:
		if run = int(src[0] & 0x0f); run != 0 {
			return run, 1, true
		}
		if len(src) < 2 {
			return 0, 0, false
		}
		return int(src[1]) + 16, 2, true
	case megaMegaBgRun, megaMegaFgRun, megaMegaFgBgImage, megaMegaColorRun,
		megaMegaColorImage, megaMegaSetFgRun, megaMegaSetFgBgImage, megaMegaDitheredRun:
		if len(src) < 3 {
			return 0, 0, false
		}
		return int(src[1]) | int(src[2])<<8, 3, true
	}
	return 1, 1, true // the special orders
}

// rleDecode decodes an interleaved RLE bitmap stream of bpp bits per pixel
// (8, 15, 16 or 24) into dst, one pixel value per element, in the order
// the stream stores the rows: bottom-up, width pixels each.
func rleDecode(src []byte, dst []uint32, width, bpp int) error {
	var bytesPP int
	var white uint32
	switch bpp {
	case 8:
		bytesPP, white = 1, 0xff
	case 15:
		bytesPP, white = 2, 0x7fff
	case 16:
		bytesPP, white = 2, 0xffff
	case 24:
		bytesPP, white = 3, 0xffffff
	default:
		return fmt.Errorf("rdp: RLE bitmap of %d bits per pixel", bpp)
	}
	if width <= 0 {
		return errors.New("rdp: RLE bitmap of no width")
	}
	si, di := 0, 0
	pixel := func() (uint32, bool) {
		if si+bytesPP > len(src) {
			return 0, false
		}
		var v uint32
		for i := range bytesPP {
			v |= uint32(src[si+i]) << (8 * i)
		}
		si += bytesPP
		return v, true
	}
	put := func(v uint32) bool {
		if di >= len(dst) {
			return false
		}
		dst[di] = v
		di++
		return true
	}
	fg := white
	insertFg, firstLine := false, true
	// fgbg writes the n pixels of a foreground/background image from the
	// bits of mask, low bit first.
	fgbg := func(mask byte, n int) bool {
		for i := range n {
			var v uint32
			set := mask&(1<<i) != 0
			switch {
			case firstLine && set:
				v = fg
			case firstLine:
				v = 0
			case set:
				v = dst[di-width] ^ fg
			default:
				v = dst[di-width]
			}
			if !put(v) {
				return false
			}
		}
		return true
	}

	for si < len(src) {
		if firstLine && di >= width {
			firstLine, insertFg = false, false
		}
		code := rleCode(src[si])
		run, n, ok := rleRun(code, src[si:])
		if !ok {
			return errShort
		}
		si += n

		if code == regularBgRun || code == megaMegaBgRun {
			if insertFg {
				v := fg
				if !firstLine {
					v ^= dst[di-width]
				}
				if !put(v) {
					return errRLEOverrun
				}
				run--
			}
			for ; run > 0; run-- {
				var v uint32
				if !firstLine {
					v = dst[di-width]
				}
				if !put(v) {
					return errRLEOverrun
				}
			}
			// A background run that follows needs a foreground pixel
			// inserted.
			insertFg = true
			continue
		}
		insertFg = false

		switch code {
		case regularFgRun, megaMegaFgRun, liteSetFgFgRun, megaMegaSetFgRun:
			if code == liteSetFgFgRun || code == megaMegaSetFgRun {
				if fg, ok = pixel(); !ok {
					return errShort
				}
			}
			for ; run > 0; run-- {
				v := fg
				if !firstLine {
					v ^= dst[di-width]
				}
				if !put(v) {
					return errRLEOverrun
				}
			}
		case liteDitheredRun, megaMegaDitheredRun:
			a, okA := pixel()
			b, okB := pixel()
			if !okA || !okB {
				return errShort
			}
			for ; run > 0; run-- {
				if !put(a) || !put(b) {
					return errRLEOverrun
				}
			}
		case regularColorRun, megaMegaColorRun:
			a, ok := pixel()
			if !ok {
				return errShort
			}
			for ; run > 0; run-- {
				if !put(a) {
					return errRLEOverrun
				}
			}
		case regularFgBgImage, megaMegaFgBgImage, liteSetFgFgBgImage, megaMegaSetFgBgImage:
			if code == liteSetFgFgBgImage || code == megaMegaSetFgBgImage {
				if fg, ok = pixel(); !ok {
					return errShort
				}
			}
			for run > 0 {
				if si >= len(src) {
					return errShort
				}
				mask := src[si]
				si++
				k := min(run, 8)
				if !fgbg(mask, k) {
					return errRLEOverrun
				}
				run -= k
			}
		case regularColorImage, megaMegaColorImage:
			for ; run > 0; run-- {
				v, ok := pixel()
				if !ok {
					return errShort
				}
				if !put(v) {
					return errRLEOverrun
				}
			}
		case specialFgBg1:
			if !fgbg(0x03, 8) {
				return errRLEOverrun
			}
		case specialFgBg2:
			if !fgbg(0x05, 8) {
				return errRLEOverrun
			}
		case specialWhite:
			if !put(white) {
				return errRLEOverrun
			}
		case specialBlack:
			if !put(0) {
				return errRLEOverrun
			}
		default:
			return fmt.Errorf("rdp: unknown RLE order %#02x", src[si-n])
		}
	}
	return nil
}
