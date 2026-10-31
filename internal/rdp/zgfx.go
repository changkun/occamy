// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package rdp

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// RDP 8.0 bulk compression, which [MS-RDPEGFX] 3.1.9.1 specifies and
// FreeRDP calls ZGFX: the server's messages on the graphics channel come
// as RDP_SEGMENTED_DATA, one segment or several, each of at most 65,535
// bytes once decoded, and each either stored or compressed with LZ77
// over a history of 2,500,000 bytes and a fixed Huffman code. Every byte
// a segment yields joins the history, stored segments' too, since a later
// match may copy from them. xrdp stores every segment; Windows
// compresses.

// RDP_SEGMENTED_DATA and RDP8_BULK_ENCODED_DATA ([MS-RDPEGFX] 2.2.5).
const (
	zgfxSingle     = 0xe0
	zgfxMultipart  = 0xe1
	zgfxTypeRDP8   = 0x04 // the low nibble of a segment's header
	zgfxCompressed = 0x20 // PACKET_COMPRESSED
)

const (
	// zgfxHistory is the history's size, the longest distance of a
	// match.
	zgfxHistory = 2500000
	// zgfxSegment bounds the bytes a segment yields.
	zgfxSegment = 65535
	// zgfxMaxLengthBits bounds the value bits of a match's length: 15,
	// for lengths of 32,768 to 65,535.
	zgfxMaxLengthBits = 15
)

// zgfxToken is a token of the Huffman code ([MS-RDPEGFX] 3.1.9.1.2.3):
// a prefix of n bits, then vbits bits whose value added to base gives a
// literal or a match's distance.
type zgfxToken struct {
	n     uint8 // the prefix's bits, 0 for a reserved code
	vbits uint8
	match bool
	base  uint32
}

// zgfxTokens maps the next 9 bits of a stream to the token they begin
// with. The codes of 9 bits, which would give distances past the
// history, and the codes the table does not define are reserved: their
// entries have n 0.
var zgfxTokens = func() (lut [512]zgfxToken) {
	for _, t := range []struct {
		n, code, vbits int
		match          bool
		base           uint32
	}{
		{1, 0b0, 8, false, 0},
		{5, 0b10001, 5, true, 0}, // distance 0 is a stored run
		{5, 0b10010, 7, true, 32},
		{5, 0b10011, 9, true, 160},
		{5, 0b10100, 10, true, 672},
		{5, 0b10101, 12, true, 1696},
		{6, 0b101100, 14, true, 5792},
		{6, 0b101101, 15, true, 22176},
		{7, 0b1011100, 18, true, 54944},
		{7, 0b1011101, 20, true, 317088},
		{8, 0b10111100, 20, true, 1365664},
		{8, 0b10111101, 21, true, 2414240},
		{5, 0b11000, 0, false, 0x00},
		{5, 0b11001, 0, false, 0x01},
		{6, 0b110100, 0, false, 0x02},
		{6, 0b110101, 0, false, 0x03},
		{6, 0b110110, 0, false, 0xff},
		{7, 0b1101110, 0, false, 0x04},
		{7, 0b1101111, 0, false, 0x05},
		{7, 0b1110000, 0, false, 0x06},
		{7, 0b1110001, 0, false, 0x07},
		{7, 0b1110010, 0, false, 0x08},
		{7, 0b1110011, 0, false, 0x09},
		{7, 0b1110100, 0, false, 0x0a},
		{7, 0b1110101, 0, false, 0x0b},
		{7, 0b1110110, 0, false, 0x3a},
		{7, 0b1110111, 0, false, 0x3b},
		{7, 0b1111000, 0, false, 0x3c},
		{7, 0b1111001, 0, false, 0x3d},
		{7, 0b1111010, 0, false, 0x3e},
		{7, 0b1111011, 0, false, 0x3f},
		{7, 0b1111100, 0, false, 0x40},
		{7, 0b1111101, 0, false, 0x80},
		{8, 0b11111100, 0, false, 0x0c},
		{8, 0b11111101, 0, false, 0x38},
		{8, 0b11111110, 0, false, 0x39},
		{8, 0b11111111, 0, false, 0x66},
	} {
		shift := 9 - t.n
		for s := range 1 << shift {
			lut[t.code<<shift|s] = zgfxToken{n: uint8(t.n), vbits: uint8(t.vbits), match: t.match, base: t.base} // #nosec G115 -- the table's small values
		}
	}
	return lut
}()

// zgfx decompresses one graphics channel's messages. Its history lives
// as long as the channel.
type zgfx struct {
	hist []byte // the history ring, allocated by the first segment
	pos  int    // where the next byte goes in hist
	out  []byte // the last message
}

var errZGFXShort = errors.New("rdp: GFX bulk data: truncated")

// decompress returns the message that the RDP_SEGMENTED_DATA p carries,
// of at most limit bytes. The message is valid until the next call.
func (z *zgfx) decompress(p []byte, limit int) ([]byte, error) {
	if len(p) < 1 {
		return nil, errZGFXShort
	}
	switch p[0] {
	case zgfxSingle:
		out, err := z.segment(z.out[:0], p[1:])
		z.out = out
		if err != nil {
			return nil, err
		}
		return out, nil
	case zgfxMultipart:
	default:
		return nil, fmt.Errorf("rdp: GFX bulk data with descriptor %#x", p[0])
	}
	r := reader{b: p[1:]}
	count := int(r.u16())
	total := int64(r.u32())
	if r.err != nil {
		return nil, errZGFXShort
	}
	if total > int64(limit) {
		return nil, fmt.Errorf("rdp: GFX bulk data of %d bytes, over the bound of %d", total, limit)
	}
	if total > int64(count)*zgfxSegment {
		return nil, fmt.Errorf("rdp: GFX bulk data of %d bytes in %d segments, which hold %d at most", total, count, count*zgfxSegment)
	}
	if int64(cap(z.out)) < total {
		z.out = make([]byte, 0, total)
	}
	out := z.out[:0]
	for range count {
		size := int64(r.u32())
		if r.err != nil || size > int64(r.len()) {
			z.out = out
			return nil, errZGFXShort
		}
		var err error
		out, err = z.segment(out, r.take(int(size)))
		z.out = out
		if err != nil {
			return nil, err
		}
		if int64(len(out)) > total {
			return nil, fmt.Errorf("rdp: GFX bulk data longer than the %d bytes it declared", total)
		}
	}
	if int64(len(out)) != total || r.len() != 0 {
		return nil, fmt.Errorf("rdp: GFX bulk data of %d bytes and %d left over, though it declared %d", len(out), r.len(), total)
	}
	return out, nil
}

// segment appends what the RDP8_BULK_ENCODED_DATA seg yields to out, and
// records it in the history.
func (z *zgfx) segment(out, seg []byte) ([]byte, error) {
	if len(seg) < 1 {
		return out, errZGFXShort
	}
	if seg[0]&0x0f != zgfxTypeRDP8 {
		return out, fmt.Errorf("rdp: GFX bulk data of compression type %d", seg[0]&0x0f)
	}
	if z.hist == nil {
		z.hist = make([]byte, zgfxHistory)
	}
	start := len(out)
	data := seg[1:]
	if seg[0]&zgfxCompressed == 0 {
		if len(data) > zgfxSegment {
			return out, fmt.Errorf("rdp: GFX bulk data segment of %d bytes", len(data))
		}
		out = append(out, data...)
	} else {
		var err error
		if out, err = z.inflate(out, data); err != nil {
			return out, err
		}
	}
	z.record(out[start:])
	return out, nil
}

// record appends b to the history.
func (z *zgfx) record(b []byte) {
	for len(b) > 0 {
		n := copy(z.hist[z.pos:], b)
		b = b[n:]
		if z.pos += n; z.pos == len(z.hist) {
			z.pos = 0
		}
	}
}

// inflate appends what the compressed segment data yields to out. data's
// last byte counts the bits of the byte before it that are not part of
// the stream.
func (z *zgfx) inflate(out, data []byte) ([]byte, error) {
	if len(data) < 1 {
		return out, errZGFXShort
	}
	bits := 8*(len(data)-1) - int(data[len(data)-1])
	if bits < 0 {
		return out, fmt.Errorf("rdp: GFX bulk data segment of %d bytes with %d bits unused", len(data), data[len(data)-1])
	}
	br := zbits{b: data[:len(data)-1]}
	start := len(out)
	for br.pos < bits {
		t := zgfxTokens[br.peek(9)]
		if t.n == 0 {
			return out, fmt.Errorf("rdp: GFX bulk data with the reserved code %09b", br.peek(9))
		}
		br.pos += int(t.n)
		v := t.base + br.get(int(t.vbits))
		if !t.match {
			out = append(out, byte(v)) // #nosec G115 -- a literal of 8 bits
		} else if v == 0 {
			// A stored run: its length in 15 bits, then its bytes from the
			// next byte boundary.
			n := int(br.get(15))
			br.pos = (br.pos + 7) &^ 7
			if br.pos+8*n > bits || len(out)-start+n > zgfxSegment {
				return out, fmt.Errorf("rdp: GFX bulk data: a stored run of %d bytes overruns the segment", n)
			}
			out = append(out, br.b[br.pos>>3:br.pos>>3+n]...)
			br.pos += 8 * n
		} else {
			n := 3
			if br.get(1) == 1 {
				// A length of 4·2^k to 4·2^(k+1)-1: k more 1 bits, a 0, and
				// k+2 value bits.
				n = 4
				k := 2
				for br.get(1) == 1 {
					if k++; k > zgfxMaxLengthBits {
						return out, errors.New("rdp: GFX bulk data with a match length past the code")
					}
					n *= 2
				}
				n += int(br.get(k))
			}
			var err error
			if out, err = z.match(out, start, int(v), n); err != nil {
				return out, err
			}
		}
		if br.pos > bits {
			return out, errors.New("rdp: GFX bulk data: a token overruns the segment")
		}
		if len(out)-start > zgfxSegment {
			return out, fmt.Errorf("rdp: GFX bulk data segment of over %d bytes", zgfxSegment)
		}
	}
	return out, nil
}

// match appends n bytes copied from dist bytes back to out, whose bytes
// from start are the segment's so far. Bytes before start come from the
// history; a history not yet written holds zeros, as the decompressor of
// [MS-RDPEGFX] 4.2.1.2 reads. A match longer than its distance repeats
// what it copies.
func (z *zgfx) match(out []byte, start, dist, n int) ([]byte, error) {
	if dist > zgfxHistory {
		return out, fmt.Errorf("rdp: GFX bulk data: a match %d bytes back, past the history", dist)
	}
	if len(out)-start+n > zgfxSegment {
		return out, fmt.Errorf("rdp: GFX bulk data segment of over %d bytes", zgfxSegment)
	}
	src := len(out) - start - dist // in the segment, if not negative
	if src < 0 {
		i := z.pos + src
		if i < 0 {
			i += zgfxHistory
		}
		for k := min(-src, n); k > 0; {
			m := min(k, zgfxHistory-i)
			out = append(out, z.hist[i:i+m]...)
			i, k, n = 0, k-m, n-m
		}
		src = 0
	}
	src += start
	for n > 0 {
		// The bytes from src to the end may be fewer than n: each copy
		// takes what is there, and the next takes what it appended.
		m := min(n, len(out)-src)
		out = append(out, out[src:src+m]...)
		src += m
		n -= m
	}
	return out, nil
}

// zbits reads a stream of bits, the most significant first. Past the end
// of b it reads zeros; the caller checks pos against the stream's bits.
type zbits struct {
	b   []byte
	pos int // the bits read
}

// peek returns the next n bits, n at most 32, without reading them.
func (z *zbits) peek(n int) uint32 {
	i := z.pos >> 3
	var v uint64
	if i+8 <= len(z.b) {
		v = binary.BigEndian.Uint64(z.b[i:])
	} else {
		for j := range 8 {
			v <<= 8
			if i+j < len(z.b) {
				v |= uint64(z.b[i+j])
			}
		}
	}
	return uint32(v << (z.pos & 7) >> (64 - n)) // #nosec G115 -- n bits
}

// get reads the next n bits, n at most 32.
func (z *zbits) get(n int) uint32 {
	if n == 0 {
		return 0
	}
	v := z.peek(n)
	z.pos += n
	return v
}
