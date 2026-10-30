// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package codecs

import (
	"encoding/binary"
	"errors"
	"image"
)

// QOI is lossless option C of the spec, the Quite OK Image format
// (https://qoiformat.org/qoi-specification.pdf) for opaque pixels: its
// 14-byte header, the six chunk types, and the 8-byte end marker.

const (
	qoiIndex = 0x00
	qoiDiff  = 0x40
	qoiLuma  = 0x80
	qoiRun   = 0xc0
	qoiRGB   = 0xfe
	qoiRGBA  = 0xff
	qoiMask  = 0xc0
)

var qoiEnd = [8]byte{7: 1}

func qoiHash(r, g, b, a byte) byte { return (r*3 + g*5 + b*7 + a*11) % 64 }

// AppendQOI appends img as QOI with three channels.
func AppendQOI(dst []byte, img *image.RGBA) []byte {
	w, h := img.Rect.Dx(), img.Rect.Dy()
	dst = append(dst, 'q', 'o', 'i', 'f')
	dst = binary.BigEndian.AppendUint32(dst, uint32(w)) // #nosec G115 -- a tile's size
	dst = binary.BigEndian.AppendUint32(dst, uint32(h)) // #nosec G115 -- a tile's size
	dst = append(dst, 3, 0)
	var seen [64][4]byte
	pr, pg, pb := byte(0), byte(0), byte(0)
	run := 0
	b := img.Rect
	for y := b.Min.Y; y < b.Max.Y; y++ {
		row := img.Pix[img.PixOffset(b.Min.X, y):img.PixOffset(b.Max.X, y)]
		for i := 0; i < len(row); i += 4 {
			r, g, bl := row[i], row[i+1], row[i+2]
			if r == pr && g == pg && bl == pb {
				run++
				if run == 62 {
					dst = append(dst, qoiRun|byte(run-1))
					run = 0
				}
				continue
			}
			if run > 0 {
				dst = append(dst, qoiRun|byte(run-1)) // #nosec G115 -- below 62
				run = 0
			}
			h := qoiHash(r, g, bl, 0xff)
			switch seen[h] {
			case [4]byte{r, g, bl, 0xff}:
				dst = append(dst, qoiIndex|h)
			default:
				seen[h] = [4]byte{r, g, bl, 0xff}
				dr, dg, db := int8(r-pr), int8(g-pg), int8(bl-pb) // #nosec G115 -- wrapping differences are the format
				drg, dbg := dr-dg, db-dg
				switch {
				case dr >= -2 && dr <= 1 && dg >= -2 && dg <= 1 && db >= -2 && db <= 1:
					dst = append(dst, qoiDiff|byte(dr+2)<<4|byte(dg+2)<<2|byte(db+2)) // #nosec G115 -- 0 to 3
				case dg >= -32 && dg <= 31 && drg >= -8 && drg <= 7 && dbg >= -8 && dbg <= 7:
					dst = append(dst, qoiLuma|byte(dg+32), byte(drg+8)<<4|byte(dbg+8)) // #nosec G115 -- in range
				default:
					dst = append(dst, qoiRGB, r, g, bl)
				}
			}
			pr, pg, pb = r, g, bl
		}
	}
	if run > 0 {
		dst = append(dst, qoiRun|byte(run-1)) // #nosec G115 -- below 62
	}
	return append(dst, qoiEnd[:]...)
}

var errQOI = errors.New("codecs: malformed QOI")

// DecodeQOI decodes src into dst, which must have the image's size at
// the origin.
func DecodeQOI(dst *image.RGBA, src []byte) error {
	if len(src) < 14+len(qoiEnd) || string(src[:4]) != "qoif" {
		return errQOI
	}
	w, h := int(binary.BigEndian.Uint32(src[4:])), int(binary.BigEndian.Uint32(src[8:]))
	if w != dst.Rect.Dx() || h != dst.Rect.Dy() {
		return errQOI
	}
	data := src[14 : len(src)-len(qoiEnd)]
	var seen [64][4]byte
	px := [4]byte{0, 0, 0, 0xff}
	pix := dst.Pix
	run := 0
	p := 0
	for i := 0; i < len(pix); i += 4 {
		if run > 0 {
			run--
		} else {
			if p >= len(data) {
				return errQOI
			}
			b1 := data[p]
			p++
			switch {
			case b1 == qoiRGB:
				if p+3 > len(data) {
					return errQOI
				}
				px[0], px[1], px[2] = data[p], data[p+1], data[p+2]
				p += 3
			case b1 == qoiRGBA:
				if p+4 > len(data) {
					return errQOI
				}
				px = [4]byte{data[p], data[p+1], data[p+2], data[p+3]}
				p += 4
			case b1&qoiMask == qoiIndex:
				px = seen[b1]
			case b1&qoiMask == qoiDiff:
				px[0] += b1>>4&3 - 2
				px[1] += b1>>2&3 - 2
				px[2] += b1&3 - 2
			case b1&qoiMask == qoiLuma:
				if p >= len(data) {
					return errQOI
				}
				b2 := data[p]
				p++
				dg := b1&0x3f - 32
				px[0] += dg - 8 + b2>>4
				px[1] += dg
				px[2] += dg - 8 + b2&0x0f
			default: // run
				run = int(b1 & 0x3f)
			}
			seen[qoiHash(px[0], px[1], px[2], px[3])] = px
		}
		copy(pix[i:i+4], px[:])
	}
	return nil
}
