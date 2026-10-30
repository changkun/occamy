// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package codec

import (
	"bytes"
	"compress/flate"
	"errors"
	"runtime"
	"testing"
	"time"
)

// Payloads that are not a container of their rectangle fail, whatever
// they hold.
func TestInflateInvalid(t *testing.T) {
	// A 3×2 tile.
	valid := []byte{0, 1, 0xff, 0, 0, 0, 0, 0xff, 0b0100_0000, 0b1000_0000}
	stored := func(container []byte) []byte {
		// One final stored block: BFINAL 1, BTYPE 00, LEN, NLEN.
		n := len(container)
		b := []byte{1, byte(n), byte(n >> 8), ^byte(n), ^byte(n >> 8)}
		return append(b, container...)
	}
	for _, c := range []struct {
		name    string
		payload []byte
	}{
		{"empty payload", nil},
		{"not DEFLATE", []byte{0xff, 0xff, 0xff, 0xff}},
		{"a truncated stream", deflate(t, valid)[:3]},
		{"a stream without a final block", append([]byte{0}, stored(valid)[1:]...)},
		{"bytes after the stream", append(deflate(t, valid), 0)},
		{"an empty container", deflate(t, nil)},
		{"form 4", deflate(t, append([]byte{4}, valid[1:]...))},
		{"form 0x80", deflate(t, append([]byte{0x80}, valid[1:]...))},
		{"a truncated palette", deflate(t, valid[:5])},
		{"no indices", deflate(t, valid[:8])},
		{"one row of indices", deflate(t, valid[:9])},
		{"an index beyond the palette", deflate(t, []byte{0, 2, 1, 1, 1, 2, 2, 2, 3, 3, 3, 0b1100_0000, 0})},
		{"an extra index row", deflate(t, append(bytes.Clone(valid), 0))},
		{"raw pixels short of the rectangle", deflate(t, append([]byte{1}, make([]byte, 3*6-1)...))},
		{"raw pixels beyond the rectangle", deflate(t, append([]byte{1}, make([]byte, 3*6+1)...))},
		{"raw alpha pixels at 3 bytes", deflate(t, append([]byte{3}, make([]byte, 3*6)...))},
		{"a bomb: 8 MiB of zeros", deflate(t, make([]byte, 8<<20))},
	} {
		var in Inflater
		out := rgba(make([]byte, 4*3*2), 3, 2)
		start := time.Now()
		if err := in.Decode(out, c.payload); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v, want ErrInvalid", c.name, err)
		}
		// Even the bomb stops at the rectangle's size.
		if d := time.Since(start); d > 100*time.Millisecond {
			t.Errorf("%s: took %v", c.name, d)
		}
	}
	var in Inflater
	if err := in.Decode(rgba(make([]byte, 4*6), 3, 2), stored(valid)); err != nil {
		t.Errorf("the valid container in a stored block: %v", err)
	}
	if err := in.Decode(rgba(nil, 0, 0), deflate(t, valid)); !errors.Is(err, ErrInvalid) {
		t.Errorf("an empty rectangle: %v, want ErrInvalid", err)
	}
}

// An Inflater allocates nothing per payload once it has decoded one, and
// nothing that grows with a hostile payload.
func TestInflateAllocations(t *testing.T) {
	var d Deflater
	var in Inflater
	pix := tileWith(64, 64, 17, false, 1)
	payload := d.Append(nil, pix, 64, 64)
	out := rgba(make([]byte, len(pix)), 64, 64)
	if n := testing.AllocsPerRun(20, func() {
		if err := in.Decode(out, payload); err != nil {
			t.Fatal(err)
		}
	}); n != 0 {
		t.Errorf("%v allocations a tile, want 0", n)
	}

	bomb := deflate(t, make([]byte, 8<<20))
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for range 10 {
		if err := in.Decode(out, bomb); !errors.Is(err, ErrInvalid) {
			t.Fatalf("a bomb: %v", err)
		}
	}
	runtime.ReadMemStats(&after)
	if n := after.TotalAlloc - before.TotalAlloc; n > 64<<10 {
		t.Errorf("10 bombs of 8 MiB allocated %d bytes, want at most 64 KiB", n)
	}
}

// Decoding any payload into any small rectangle fails or succeeds without
// panicking, and what decodes is a container that encodes the same
// pixels again.
func FuzzInflate(f *testing.F) {
	var d Deflater
	for _, n := range []int{1, 2, 3, 5, 17, 0} {
		for _, alpha := range []bool{false, true} {
			f.Add(uint8(7), uint8(3), d.Append(nil, tileWith(7, 3, n, alpha, 1), 7, 3))
		}
	}
	f.Add(uint8(3), uint8(2), deflate(f, []byte{0, 1, 0xff, 0, 0, 0, 0, 0xff, 0b0100_0000, 0b1000_0000}))
	f.Add(uint8(1), uint8(1), []byte{})
	f.Fuzz(func(t *testing.T, w, h uint8, payload []byte) {
		if w == 0 || h == 0 || w > 64 || h > 64 {
			return
		}
		var in Inflater
		W, H := int(w), int(h)
		out := rgba(make([]byte, 4*W*H), W, H)
		err := in.Decode(out, payload)
		if err != nil {
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("error %v is not ErrInvalid", err)
			}
			return
		}
		var dd Deflater
		again := rgba(make([]byte, 4*W*H), W, H)
		if err := in.Decode(again, dd.Append(nil, out.Pix, W, H)); err != nil || !bytes.Equal(again.Pix, out.Pix) {
			t.Fatalf("re-encoded pixels differ: %v", err)
		}
	})
}

// Any pixels round-trip exactly.
func FuzzDeflateRoundTrip(f *testing.F) {
	f.Add(uint8(2), []byte{1, 2, 3, 4, 5, 6, 7, 8})
	f.Add(uint8(1), []byte{0xff, 0xff, 0xff, 0xff})
	f.Fuzz(func(t *testing.T, w uint8, pix []byte) {
		W := int(w)
		if W == 0 || len(pix) < 4*W {
			return
		}
		H := len(pix) / (4 * W)
		pix = pix[:4*W*H]
		var d Deflater
		var in Inflater
		out := rgba(make([]byte, len(pix)), W, H)
		if err := in.Decode(out, d.Append(nil, pix, W, H)); err != nil || !bytes.Equal(out.Pix, pix) {
			t.Fatalf("%d×%d: round trip failed: %v", W, H, err)
		}
	})
}

// A deflater reuses its compressor: encoding a tile allocates only what
// the payload needs.
func TestDeflaterReuse(t *testing.T) {
	var d Deflater
	pix := tileWith(64, 64, 17, false, 1)
	buf := d.Append(nil, pix, 64, 64)
	if n := testing.AllocsPerRun(20, func() { buf = d.Append(buf[:0], pix, 64, 64) }); n != 0 {
		t.Errorf("%v allocations a tile, want 0", n)
	}
	// The payload is plain DEFLATE.
	if _, err := new(bytes.Buffer).ReadFrom(flate.NewReader(bytes.NewReader(buf))); err != nil {
		t.Error(err)
	}
}
