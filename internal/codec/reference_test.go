// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package codec

import (
	"bytes"
	"compress/flate"
	"errors"
	"io"
	"testing"
)

// errReference is the reference decoder's only error.
var errReference = errors.New("not a container of the rectangle")

// reference decodes a deflate payload of a w×h rectangle as
// docs/protocol.md describes it, plainly: it inflates the whole stream,
// up to a bound well past any container of a rectangle up to 64×64, and
// then reads the container from memory.
func reference(payload []byte, w, h int) ([]byte, error) {
	src := bytes.NewReader(payload)
	c, err := io.ReadAll(io.LimitReader(flate.NewReader(src), 1<<16))
	if err != nil || len(c) >= 1<<16 || src.Len() != 0 || len(c) < 1 || c[0] > 3 {
		return nil, errReference
	}
	isRaw, alpha := c[0]&1 != 0, c[0]&2 != 0
	size := 3
	if alpha {
		size = 4
	}
	px := func(b []byte) []byte {
		if alpha {
			return b[:4]
		}
		return []byte{b[0], b[1], b[2], 0xff}
	}
	rest := c[1:]
	var out []byte
	if isRaw {
		if len(rest) != size*w*h {
			return nil, errReference
		}
		for i := range w * h {
			out = append(out, px(rest[i*size:])...)
		}
		return out, nil
	}
	if len(rest) < 1 {
		return nil, errReference
	}
	n := int(rest[0]) + 1
	rest = rest[1:]
	if len(rest) < n*size {
		return nil, errReference
	}
	palette := rest[:n*size]
	rest = rest[n*size:]
	bits := 8
	switch {
	case n == 1:
		bits = 0
	case n <= 2:
		bits = 1
	case n <= 4:
		bits = 2
	case n <= 16:
		bits = 4
	}
	stride := (w*bits + 7) / 8
	if len(rest) != stride*h {
		return nil, errReference
	}
	for y := range h {
		for x := range w {
			v := 0
			if bits > 0 {
				bit := x * bits
				v = int(rest[y*stride+bit/8]>>(8-bits-bit%8)) & (1<<bits - 1)
			}
			if v >= n {
				return nil, errReference
			}
			out = append(out, px(palette[v*size:])...)
		}
	}
	return out, nil
}

// An Inflater takes exactly the payloads that the reference decoder
// does, and decodes them to the same pixels.
func FuzzInflateReference(f *testing.F) {
	var d Deflater
	for _, n := range []int{1, 2, 3, 5, 17, 300} {
		for _, alpha := range []bool{false, true} {
			f.Add(uint8(7), uint8(3), d.Append(nil, tileWith(7, 3, n, alpha, 1), 7, 3))
		}
	}
	f.Add(uint8(3), uint8(2), deflate(f, []byte{0, 1, 0xff, 0, 0, 0, 0, 0xff, 0b1011_1111, 0b0101_1111}))
	f.Fuzz(func(t *testing.T, w, h uint8, payload []byte) {
		if w == 0 || h == 0 || w > 64 || h > 64 {
			return
		}
		W, H := int(w), int(h)
		var in Inflater
		out := rgba(make([]byte, 4*W*H), W, H)
		err := in.Decode(out, payload)
		want, werr := reference(payload, W, H)
		if (err == nil) != (werr == nil) {
			t.Fatalf("Inflater: %v; the reference: %v", err, werr)
		}
		if err == nil && !bytes.Equal(out.Pix, want) {
			t.Fatal("the Inflater's pixels differ from the reference's")
		}
	})
}
