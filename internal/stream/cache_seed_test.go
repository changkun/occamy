// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package stream

import (
	"bytes"
	"image"
	"testing"

	"github.com/zeebo/xxh3"
)

// xxh3Secret is xxh3's default secret, the reference's kSecret, which
// unseeded hashes of more than 240 bytes use as it is.
var xxh3Secret = []byte{
	0xb8, 0xfe, 0x6c, 0x39, 0x23, 0xa4, 0x4b, 0xbe, 0x7c, 0x01, 0x81, 0x2c, 0xf7, 0x21, 0xad, 0x1c,
	0xde, 0xd4, 0x6d, 0xe9, 0x83, 0x90, 0x97, 0xdb, 0x72, 0x40, 0xa4, 0xa4, 0xb7, 0xb3, 0x67, 0x1f,
	0xcb, 0x79, 0xe6, 0x4e, 0xcc, 0xc0, 0xe5, 0x78, 0x82, 0x5a, 0xd0, 0x7d, 0xcc, 0xff, 0x72, 0x21,
	0xb8, 0x08, 0x46, 0x74, 0xf7, 0x43, 0x24, 0x8e, 0xe0, 0x35, 0x90, 0xe6, 0x81, 0x3a, 0x26, 0x4c,
	0x3c, 0x28, 0x52, 0xbb, 0x91, 0xc3, 0x00, 0xcb, 0x88, 0xd0, 0x65, 0x8b, 0x1b, 0x53, 0x2e, 0xa3,
	0x71, 0x64, 0x48, 0x97, 0xa2, 0x0d, 0xf9, 0x4e, 0x38, 0x19, 0xef, 0x46, 0xa9, 0xde, 0xac, 0xd8,
	0xa8, 0xfa, 0x76, 0x3f, 0xe3, 0x9c, 0x34, 0x3f, 0xf9, 0xdc, 0xbb, 0xc7, 0xc7, 0x0b, 0x4f, 0x1d,
	0x8a, 0x51, 0xe0, 0x4b, 0xcd, 0xb4, 0x59, 0x31, 0xc8, 0x9f, 0x7e, 0xc9, 0xd9, 0x78, 0x73, 0x64,
	0xea, 0xc5, 0xac, 0x83, 0x34, 0xd3, 0xeb, 0xc3, 0xc5, 0x81, 0xa0, 0xff, 0xfa, 0x13, 0x63, 0xeb,
	0x17, 0x0d, 0xdd, 0x51, 0xb7, 0xf0, 0xda, 0x49, 0xd3, 0x16, 0x55, 0x26, 0x29, 0xd4, 0x68, 0x9e,
	0x2b, 0x16, 0xbe, 0x58, 0x7d, 0x47, 0xa1, 0xfc, 0x8f, 0xf8, 0xb8, 0xd1, 0x7a, 0xd0, 0x31, 0xce,
	0x45, 0xcb, 0x3a, 0x8f, 0x95, 0x16, 0x04, 0x28, 0xaf, 0xd7, 0xfb, 0xca, 0xbb, 0x4b, 0x40, 0x7e,
}

// collidingTiles returns two opaque 64×64 tiles that differ in two
// pixels and have the same unseeded xxh3-128, as screen content a remote
// host draws can: in the first block of 16 stripes of 64 bytes, lane i
// of stripe n is mixed with secret word n+i. Each lane adds its data to
// the next lane's accumulator, and the product of the halves of data^key
// to its own. With a word's first pixel chosen so that the low 24 bits
// of the product's low half are zero, the second pixel's colour moves
// the product by a multiple of the key's top byte. One tile adds delta
// to that colour in stripe n1 and the other in stripe n2, so the sums
// agree, and a delta whose products cancel is found in a moment.
func collidingTiles(tb testing.TB) (a, b []byte) {
	tb.Helper()
	u24 := func(b []byte) int64 { return int64(b[0]) | int64(b[1])<<8 | int64(b[2])<<16 }
	tile := func(i, n1, n2 int, x1, x2 int64) []byte {
		pix := make([]byte, 64*64*4)
		for p := 0; p < len(pix); p += 4 {
			pix[p], pix[p+1], pix[p+2], pix[p+3] = 0x20, 0x40, 0x60, 0xff
		}
		for _, s := range []struct {
			n int
			x int64
		}{{n1, x1}, {n2, x2}} {
			off, w := 64*s.n+8*i, 8*(s.n+i)
			copy(pix[off:off+3], xxh3Secret[w:w+3])
			pix[off+3] = 0xff
			pix[off+4], pix[off+5], pix[off+6], pix[off+7] = byte(s.x), byte(s.x>>8), byte(s.x>>16), 0xff
		}
		return pix
	}
	for i := range 8 {
		for n1 := range 16 {
			for n2 := n1 + 1; n2 < 16; n2++ {
				w1, w2 := 8*(n1+i), 8*(n2+i)
				m1, m2 := int64(0xff^xxh3Secret[w1+3]), int64(0xff^xxh3Secret[w2+3])
				c1, c2 := u24(xxh3Secret[w1+4:]), u24(xxh3Secret[w2+4:])
				for delta := int64(1); delta < 1<<24; delta++ {
					if m1*((delta^c1)-c1)+m2*(c2-(delta^c2)) == 0 {
						return tile(i, n1, n2, 0, delta), tile(i, n1, n2, delta, 0)
					}
				}
			}
		}
	}
	tb.Fatal("no colliding tiles")
	return nil, nil
}

// Content a remote host draws can collide under xxh3-128 with its public
// secret; each mirror hashes with a secret seed of its own, which the
// remote host never learns, so that it cannot make a viewer draw one
// tile for another.
func TestCacheKeySeeded(t *testing.T) {
	a, b := collidingTiles(t)
	if bytes.Equal(a, b) || xxh3.Hash128(a) != xxh3.Hash128(b) {
		t.Fatal("the tiles do not collide unseeded")
	}
	r := image.Rect(0, 0, 64, 64)
	c := newTileCache(DefaultCacheSlots)
	ka, _ := c.key(a, r)
	kb, _ := c.key(b, r)
	if ka == kb {
		t.Errorf("the mirror keys two different tiles alike, seed %#x", c.seed)
	}
	if d := newTileCache(DefaultCacheSlots); d.seed == c.seed {
		t.Errorf("two mirrors share the seed %#x", c.seed)
	}
}
