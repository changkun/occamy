// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package rdp

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"

	"changkun.de/x/occamy/internal/rdp/rdptest"
)

// hexBytes decodes hex with spaces, as the specifications print it.
func hexBytes(t testing.TB, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(strings.ReplaceAll(s, " ", ""))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

const quickFox = "The quick brown fox jumps over the lazy dog"

// The examples of [MS-RDPEGFX] 4.2.1.1.
func TestZGFXExamples(t *testing.T) {
	stored := make([]byte, 1000)
	randBytes(rand.New(rand.NewPCG(4, 5)), stored)
	for _, tc := range []struct {
		name string
		in   []byte
		want []byte
	}{
		{"example 1", hexBytes(t, "E0 24 CE 9B 19 62 18 00"), hexBytes(t, "01 02 FF 65 65 65 65 65")},
		{"example 2", append(hexBytes(t, "E0 04"), quickFox...), []byte(quickFox)},
		{"example 3", hexBytes(t, "E0 24 20 90 88 71 1F B2 01"), []byte(strings.Repeat("ABC", 20))},
		{"example 4", hexBytes(t, "E1 03 00 2B 00 00 00 11 00 00 00 04 54 68 65 20"+
			"71 75 69 63 6B 20 62 72 6F 77 6E 20 0E 00 00 00"+
			"04 66 6F 78 20 6A 75 6D 70 73 20 6F 76 65 10 00"+
			"00 00 24 39 08 0E 91 F8 D8 61 3D 1E 44 06 43 79"+
			"9C 02"), []byte(quickFox)},
		// Example 5 omits most of its bytes: its stored run of 1,000 bytes
		// with others.
		{"example 5", append(append(hexBytes(t, "E0 24 88 01 F4 00"), stored...), 0), stored},
	} {
		var z zgfx
		got, err := z.decompress(tc.in, maxGFXData)
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if !bytes.Equal(got, tc.want) {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

// A stored segment's bytes join the history: a later message's match
// copies them.
func TestZGFXStoredHistory(t *testing.T) {
	var enc rdptest.ZGFX
	var z zgfx
	first := []byte("stored first: " + quickFox)
	if got, err := z.decompress(enc.Single(first, rdptest.SegStored), maxGFXData); err != nil || !bytes.Equal(got, first) {
		t.Fatalf("got %q, %v", got, err)
	}
	second := []byte(quickFox + quickFox)
	msg := enc.Single(second, rdptest.SegCompressed)
	if len(msg) > 16 {
		t.Fatalf("the second message is %d bytes, not one match", len(msg))
	}
	got, err := z.decompress(msg, maxGFXData)
	if err != nil || !bytes.Equal(got, second) {
		t.Fatalf("got %q, %v", got, err)
	}
	// So does a stored segment of the same message.
	third := []byte("abcdefgh" + quickFox + "abcdefgh")
	got, err = z.decompress(enc.Multipart(third, 8, rdptest.SegStored, rdptest.SegCompressed), maxGFXData)
	if err != nil || !bytes.Equal(got, third) {
		t.Fatalf("got %q, %v", got, err)
	}
}

// bits builds a compressed segment bit by bit, with the trailer.
type bits struct {
	b []byte
	n int
}

func (w *bits) put(v uint32, n int) *bits {
	for i := n - 1; i >= 0; i-- {
		if w.n%8 == 0 {
			w.b = append(w.b, 0)
		}
		if v>>i&1 != 0 {
			w.b[len(w.b)-1] |= 0x80 >> (w.n % 8)
		}
		w.n++
	}
	return w
}

func (w *bits) single() []byte {
	return append(append([]byte{zgfxSingle, zgfxTypeRDP8 | zgfxCompressed}, w.b...), byte((8-w.n%8)%8))
}

// A match reaches back the whole history, around the ring, and no
// further.
func TestZGFXHistoryWrap(t *testing.T) {
	var z zgfx
	rng := rand.New(rand.NewPCG(6, 7))
	var all []byte
	seg := make([]byte, zgfxSegment)
	for len(all) < zgfxHistory+zgfxSegment {
		randBytes(rng, seg)
		msg := append([]byte{zgfxSingle, zgfxTypeRDP8}, seg...)
		if _, err := z.decompress(msg, maxGFXData); err != nil {
			t.Fatal(err)
		}
		all = append(all, seg...)
	}
	// 10111101 and 21 bits: a distance of 2,414,240 and more.
	var w bits
	w.put(0b10111101, 8).put(zgfxHistory-2414240, 21).put(0b10, 2).put(3, 2) // length 7
	got, err := z.decompress(w.single(), maxGFXData)
	if err != nil {
		t.Fatal(err)
	}
	if want := all[len(all)-zgfxHistory:][:7]; !bytes.Equal(got, want) {
		t.Errorf("got %x, want %x", got, want)
	}
	var far bits
	far.put(0b10111101, 8).put(zgfxHistory-2414240+1, 21).put(0, 1)
	if _, err := z.decompress(far.single(), maxGFXData); err == nil {
		t.Error("a match past the history: no error")
	}
}

// Before the history fills, a match reads zeros, as the sample
// decompressor's does.
func TestZGFXHistoryUnwritten(t *testing.T) {
	var z zgfx
	var w bits
	w.put(0b00110001, 9).put(0b10010, 5).put(100-32, 7).put(0, 1) // '1', then 3 bytes 100 back
	got, err := z.decompress(w.single(), maxGFXData)
	if err != nil || !bytes.Equal(got, []byte{'1', 0, 0, 0}) {
		t.Errorf("got %q, %v", got, err)
	}
}

// A match that starts in the history and runs past its end goes on into
// the segment's own bytes, which the history does not hold yet.
func TestZGFXHistoryIntoSegment(t *testing.T) {
	var z zgfx
	if _, err := z.decompress([]byte{zgfxSingle, zgfxTypeRDP8, 'a', 'b'}, maxGFXData); err != nil {
		t.Fatal(err)
	}
	var w bits
	w.put('c', 9).put(0b10001, 5).put(2, 5).put(0b10, 2).put(0, 2) // 'c', then 4 bytes 2 back
	got, err := z.decompress(w.single(), maxGFXData)
	if err != nil || string(got) != "cbcbc" {
		t.Errorf("got %q, %v, want %q", got, err, "cbcbc")
	}
}

func TestZGFXErrors(t *testing.T) {
	big := make([]byte, zgfxSegment+1)
	for _, tc := range []struct {
		name string
		msg  func() []byte
	}{
		{"empty", func() []byte { return nil }},
		{"descriptor", func() []byte { return []byte{0xe2, 0x04} }},
		{"no segment header", func() []byte { return []byte{zgfxSingle} }},
		{"compression type", func() []byte { return []byte{zgfxSingle, 0x05, 'a'} }},
		{"no trailer", func() []byte { return []byte{zgfxSingle, 0x24} }},
		{"unused bits past the stream", func() []byte { return []byte{zgfxSingle, 0x24, 0xff, 9} }},
		{"stored segment too long", func() []byte { return append([]byte{zgfxSingle, 0x04}, big...) }},
		{"reserved code", func() []byte { return new(bits).put(0b10000, 5).put(0, 4).single() }},
		{"distance of 9 bits", func() []byte { return new(bits).put(0b101111100, 9).put(0, 22).put(0, 1).single() }},
		{"undefined code of 9 bits", func() []byte { return new(bits).put(0b101111111, 9).single() }},
		{"length past the code", func() []byte {
			return new(bits).put('a', 9).put(0b10001, 5).put(1, 5).put(0xffff, 16).put(0, 1).single()
		}},
		{"token past the stream", func() []byte { return new(bits).put(0b10010, 5).single() }},
		{"stored run past the stream", func() []byte {
			w := new(bits).put(0b10001, 5).put(0, 5).put(4, 15)
			w.n = 8 * len(w.b)
			return w.put('a', 8).single()
		}},
		{"match past the segment", func() []byte {
			w := new(bits).put('a', 9)
			for range 2 {
				w.put(0b10001, 5).put(1, 5).put(0b111111111111110, 15).put(0x7fff, 15) // 65,535 bytes
			}
			return w.single()
		}},
		{"multipart past its declared length", func() []byte {
			return []byte{zgfxMultipart, 1, 0, 1, 0, 0, 0, 3, 0, 0, 0, 0x04, 'a', 'b'}
		}},
		{"multipart short of its declared length", func() []byte {
			return []byte{zgfxMultipart, 1, 0, 3, 0, 0, 0, 3, 0, 0, 0, 0x04, 'a', 'b'}
		}},
		{"multipart over the bound", func() []byte {
			return []byte{zgfxMultipart, 0xff, 0xff, 0, 0, 0, 0x02, 0}
		}},
		{"multipart longer than its segments hold", func() []byte {
			return []byte{zgfxMultipart, 1, 0, 0, 0, 1, 0}
		}},
		{"multipart segment past the message", func() []byte {
			return []byte{zgfxMultipart, 1, 0, 1, 0, 0, 0, 9, 0, 0, 0, 0x04, 'a'}
		}},
		{"multipart with bytes after its segments", func() []byte {
			return []byte{zgfxMultipart, 1, 0, 1, 0, 0, 0, 2, 0, 0, 0, 0x04, 'a', 'b'}
		}},
	} {
		var z zgfx
		out, err := z.decompress(tc.msg(), maxGFXData)
		if err == nil {
			t.Errorf("%s: %q, no error", tc.name, out)
		}
	}
	// The longest length code has 13 1 bits after the first: 14 begin no
	// length of the code, though any length they began would be too long
	// for a segment as well.
	var z zgfx
	msg := new(bits).put('a', 9).put(0b10001, 5).put(1, 5).put(0x7fff, 15).put(0, 1).put(0, 16).single()
	if _, err := z.decompress(msg, maxGFXData); err == nil || !strings.Contains(err.Error(), "past the code") {
		t.Errorf("a length of 14 1 bits: %v, want it past the code", err)
	}
}

// What the test compressor writes, the client reads, over messages that
// share the history.
func TestZGFXRoundTrip(t *testing.T) {
	rng := rand.New(rand.NewPCG(8, 9))
	for _, stored := range []int{0, 1, 40} {
		enc := rdptest.ZGFX{Stored: stored}
		var z zgfx
		for i := range 40 {
			msg := sample(rng, rng.IntN(200000))
			var p []byte
			switch i % 3 {
			case 0:
				p = enc.Multipart(msg, zgfxSegment, rdptest.SegCompressed)
			case 1:
				p = enc.Multipart(msg, 1000+rng.IntN(60000), rdptest.SegStored, rdptest.SegCompressed)
			default:
				p = enc.Single(msg[:min(len(msg), zgfxSegment)], rdptest.SegCompressed)
				msg = msg[:min(len(msg), zgfxSegment)]
			}
			got, err := z.decompress(p, maxGFXData)
			if err != nil {
				t.Fatalf("stored %d, message %d: %v", stored, i, err)
			}
			if !bytes.Equal(got, msg) {
				t.Fatalf("stored %d, message %d of %d bytes differs", stored, i, len(msg))
			}
		}
	}
}

// randBytes fills b with noise.
func randBytes(rng *rand.Rand, b []byte) {
	for i := range b {
		b[i] = byte(rng.Uint32())
	}
}

// sample returns n bytes that compress: words, runs and noise.
func sample(rng *rand.Rand, n int) []byte {
	words := strings.Fields(quickFox + " \x00\x01\x02\xff\x66 bitmap surface tile")
	var b []byte
	for len(b) < n {
		switch rng.IntN(4) {
		case 0:
			b = append(b, words[rng.IntN(len(words))]...)
		case 1:
			b = append(b, bytes.Repeat([]byte{byte(rng.IntN(256))}, rng.IntN(300))...)
		case 2:
			k := rng.IntN(100)
			for range k {
				b = append(b, byte(rng.IntN(256)))
			}
		default:
			if len(b) > 0 {
				i := rng.IntN(len(b))
				b = append(b, b[i:min(len(b), i+rng.IntN(5000))]...)
			}
		}
	}
	return b[:n]
}

func FuzzZGFX(f *testing.F) {
	for _, s := range []string{
		"E0 24 CE 9B 19 62 18 00",
		"E0 24 20 90 88 71 1F B2 01",
		"E1 03 00 2B 00 00 00 11 00 00 00 04 54 68 65 20 71 75 69 63 6B 20 62 72 6F 77 6E 20 0E 00 00 00 04 66 6F 78 20 6A 75 6D 70 73 20 6F 76 65 10 00 00 00 24 39 08 0E 91 F8 D8 61 3D 1E 44 06 43 79 9C 02",
	} {
		f.Add(hexBytes(f, s), []byte{})
	}
	var enc rdptest.ZGFX
	rng := rand.New(rand.NewPCG(1, 1))
	f.Add(enc.Single(sample(rng, 3000), rdptest.SegStored), enc.Single(sample(rng, 3000), rdptest.SegCompressed))
	f.Fuzz(func(t *testing.T, a, b []byte) {
		var z zgfx
		for _, m := range [][]byte{a, b} {
			out, err := z.decompress(m, 1<<20)
			if err == nil && len(out) > 1<<20 {
				t.Fatalf("%d bytes over the bound", len(out))
			}
		}
	})
}

// The client reads back what the test compressor writes of any input.
func FuzzZGFXRoundTrip(f *testing.F) {
	f.Add([]byte(quickFox), uint8(0), uint16(100))
	f.Add(bytes.Repeat([]byte("ABC"), 100), uint8(3), uint16(7))
	f.Fuzz(func(t *testing.T, msg []byte, stored uint8, size uint16) {
		enc := rdptest.ZGFX{Stored: int(stored % 8)}
		var z zgfx
		for _, kinds := range [][]int{{rdptest.SegCompressed}, {rdptest.SegStored, rdptest.SegCompressed}} {
			got, err := z.decompress(enc.Multipart(msg, max(1, int(size)), kinds...), maxGFXData)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, msg) {
				t.Fatalf("got %x, want %x", got, msg)
			}
		}
	})
}

// BenchmarkZGFX decompresses a message of 1 MiB stored, as xrdp sends,
// and compressed, as Windows does.
func BenchmarkZGFX(b *testing.B) {
	rng := rand.New(rand.NewPCG(2, 3))
	msg := sample(rng, 1<<20)
	for _, kind := range []int{rdptest.SegStored, rdptest.SegCompressed} {
		b.Run(fmt.Sprintf("kind=%d", kind), func(b *testing.B) {
			var enc rdptest.ZGFX
			p := enc.Multipart(msg, zgfxSegment, kind)
			var z zgfx
			b.SetBytes(int64(len(msg)))
			b.ReportAllocs()
			for b.Loop() {
				if _, err := z.decompress(p, maxGFXData); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
