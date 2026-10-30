// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package codec

import (
	"bytes"
	"compress/flate"
	"errors"
	"image"
	"io"
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

// countingReader counts the bytes an Inflater inflates.
type countingReader struct {
	io.ReadCloser
	n int
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.ReadCloser.Read(p)
	c.n += n
	return n, err
}

func (c *countingReader) Reset(r io.Reader, dict []byte) error {
	c.n = 0
	return c.ReadCloser.(flate.Resetter).Reset(r, dict)
}

// counting returns an Inflater that counts the bytes it inflates.
func counting() (*Inflater, *countingReader) {
	c := &countingReader{ReadCloser: flate.NewReader(bytes.NewReader(nil))}
	return &Inflater{fr: c}, c
}

// maxContainer is the longest container of a w×h rectangle: raw pixels
// with alpha, or a palette of 256 colours with alpha and a byte an index.
func maxContainer(w, h int) int { return max(1+4*w*h, 1026+w*h) }

// A receiver inflates at most one byte more than the longest container
// of the rectangle, max(1 + 4wh, 1026 + wh) bytes; the byte more shows
// that a container is longer. The bound is not 1 + 4wh: a palette of 256
// colours goes past that for a rectangle of up to 341 pixels, and so may
// occamyd's own container of an edge tile.
func TestInflateCap(t *testing.T) {
	// The longest container of a 3×2 rectangle, which decodes.
	at := []byte{2, 255}
	for i := range 256 {
		at = append(at, byte(i), byte(255-i), byte(i*7), byte(i|1))
	}
	at = append(at, 0, 255, 17, 128, 64, 200)
	if len(at) != maxContainer(3, 2) || len(at) <= 1+4*3*2 {
		t.Fatalf("the container is %d bytes, not the cap", len(at))
	}
	in, c := counting()
	if err := in.Decode(rgba(make([]byte, 4*6), 3, 2), deflate(t, at)); err != nil || c.n != len(at) {
		t.Errorf("a container at the cap: %v after %d bytes, want it decoded after %d", err, c.n, len(at))
	}
	over := append(bytes.Clone(at), 0)
	if err := in.Decode(rgba(make([]byte, 4*6), 3, 2), deflate(t, over)); !errors.Is(err, ErrInvalid) || c.n != len(at)+1 {
		t.Errorf("a byte over the cap: %v after %d bytes, want ErrInvalid after %d", err, c.n, len(at)+1)
	}

	// occamyd's container of a 1×64 edge tile of 64 colours is 258
	// bytes, over 1 + 4wh.
	pix := make([]byte, 0, 4*64)
	for i := range 64 {
		pix = append(pix, byte(i), byte(255-i), byte(i*3), 0xff)
	}
	var p Palette
	if !p.Build(pix, 1, 64) {
		t.Fatal("64 colours make no palette")
	}
	edge := AppendContainer(nil, pix, 1, 64, &p)
	if len(edge) <= 1+4*64 || len(edge) > maxContainer(1, 64) {
		t.Errorf("the edge tile's container is %d bytes; want over %d and at most %d", len(edge), 1+4*64, maxContainer(1, 64))
	}
	if err := in.Decode(rgba(make([]byte, len(pix)), 1, 64), deflate(t, edge)); err != nil || c.n != len(edge) {
		t.Errorf("the edge tile: %v after %d bytes", err, c.n)
	}

	// Bombs of each form inflate one byte past the longest container of
	// their form, never more than one past the cap.
	for _, size := range []image.Point{{1, 1}, {3, 2}, {1, 64}, {17, 5}, {64, 64}} {
		w, h := size.X, size.Y
		for _, head := range [][]byte{{0, 255}, {1}, {2, 255}, {3}} {
			bomb := deflate(t, append(bytes.Clone(head), make([]byte, 1<<20)...))
			if err := in.Decode(rgba(make([]byte, 4*w*h), w, h), bomb); !errors.Is(err, ErrInvalid) {
				t.Errorf("%dx%d, a bomb of form %d: %v", w, h, head[0], err)
			}
			if c.n > maxContainer(w, h)+1 {
				t.Errorf("%dx%d, a bomb of form %d: %d bytes inflated, over %d", w, h, head[0], c.n, maxContainer(w, h)+1)
			}
		}
		_ = in.Decode(rgba(make([]byte, 4*w*h), w, h), deflate(t, append([]byte{2, 255}, make([]byte, 1<<20)...)))
		if w*h < 342 && c.n != maxContainer(w, h)+1 {
			t.Errorf("%dx%d, a bomb of 256 colours: %d bytes inflated, want %d", w, h, c.n, maxContainer(w, h)+1)
		}
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
// panicking and without inflating more than one byte past the
// rectangle's longest container, and what decodes is a container that
// encodes the same pixels again.
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
		in, c := counting()
		W, H := int(w), int(h)
		out := rgba(make([]byte, 4*W*H), W, H)
		err := in.Decode(out, payload)
		if c.n > maxContainer(W, H)+1 {
			t.Fatalf("%d bytes inflated for %d×%d pixels, over %d", c.n, W, H, maxContainer(W, H)+1)
		}
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
