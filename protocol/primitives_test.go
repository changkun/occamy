// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package protocol_test

import (
	"bytes"
	"encoding/hex"
	"errors"
	"math"
	"testing"

	"changkun.de/x/occamy/protocol"
)

func unhex(t testing.TB, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestIntegers(t *testing.T) {
	tests := []struct {
		name   string
		append func([]byte) []byte
		read   func(*protocol.Decoder) any
		want   any
		raw    string
	}{
		{"uint8 0", func(b []byte) []byte { return protocol.AppendUint8(b, 0) }, readUint8, uint8(0), "00"},
		{"uint8 max", func(b []byte) []byte { return protocol.AppendUint8(b, math.MaxUint8) }, readUint8, uint8(math.MaxUint8), "ff"},
		{"uint16 order", func(b []byte) []byte { return protocol.AppendUint16(b, 0x0102) }, readUint16, uint16(0x0102), "0201"},
		{"uint16 max", func(b []byte) []byte { return protocol.AppendUint16(b, math.MaxUint16) }, readUint16, uint16(math.MaxUint16), "ffff"},
		{"uint32 order", func(b []byte) []byte { return protocol.AppendUint32(b, 0x01020304) }, readUint32, uint32(0x01020304), "04030201"},
		{"uint32 max", func(b []byte) []byte { return protocol.AppendUint32(b, math.MaxUint32) }, readUint32, uint32(math.MaxUint32), "ffffffff"},
		{"uint64 order", func(b []byte) []byte { return protocol.AppendUint64(b, 0x0102030405060708) }, readUint64, uint64(0x0102030405060708), "0807060504030201"},
		{"uint64 max", func(b []byte) []byte { return protocol.AppendUint64(b, math.MaxUint64) }, readUint64, uint64(math.MaxUint64), "ffffffffffffffff"},
		{"int8 min", func(b []byte) []byte { return protocol.AppendInt8(b, math.MinInt8) }, readInt8, int8(math.MinInt8), "80"},
		{"int8 -1", func(b []byte) []byte { return protocol.AppendInt8(b, -1) }, readInt8, int8(-1), "ff"},
		{"int8 max", func(b []byte) []byte { return protocol.AppendInt8(b, math.MaxInt8) }, readInt8, int8(math.MaxInt8), "7f"},
		{"int16 min", func(b []byte) []byte { return protocol.AppendInt16(b, math.MinInt16) }, readInt16, int16(math.MinInt16), "0080"},
		{"int16 -2", func(b []byte) []byte { return protocol.AppendInt16(b, -2) }, readInt16, int16(-2), "feff"},
		{"int16 max", func(b []byte) []byte { return protocol.AppendInt16(b, math.MaxInt16) }, readInt16, int16(math.MaxInt16), "ff7f"},
		{"int32 min", func(b []byte) []byte { return protocol.AppendInt32(b, math.MinInt32) }, readInt32, int32(math.MinInt32), "00000080"},
		{"int32 -1", func(b []byte) []byte { return protocol.AppendInt32(b, -1) }, readInt32, int32(-1), "ffffffff"},
		{"int32 max", func(b []byte) []byte { return protocol.AppendInt32(b, math.MaxInt32) }, readInt32, int32(math.MaxInt32), "ffffff7f"},
		{"int64 min", func(b []byte) []byte { return protocol.AppendInt64(b, math.MinInt64) }, readInt64, int64(math.MinInt64), "0000000000000080"},
		{"int64 -1", func(b []byte) []byte { return protocol.AppendInt64(b, -1) }, readInt64, int64(-1), "ffffffffffffffff"},
		{"int64 max", func(b []byte) []byte { return protocol.AppendInt64(b, math.MaxInt64) }, readInt64, int64(math.MaxInt64), "ffffffffffffff7f"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := tt.append([]byte{0xee})
			if want := unhex(t, "ee"+tt.raw); !bytes.Equal(raw, want) {
				t.Fatalf("append = %x, want %x", raw, want)
			}
			d := protocol.NewDecoder(raw[1:])
			if got := tt.read(d); got != tt.want || d.Finish() != nil {
				t.Fatalf("read = %v, %v, want %v", got, d.Finish(), tt.want)
			}

			// Every shorter body is truncated, consumes nothing and
			// reads zero.
			for n := range len(raw) - 1 {
				d := protocol.NewDecoder(raw[1 : 1+n])
				got := tt.read(d)
				if !errors.Is(d.Err(), protocol.ErrTruncated) || d.Len() != n {
					t.Fatalf("read of %d bytes: %v, %d left, want %v and %d left", n, d.Err(), d.Len(), protocol.ErrTruncated, n)
				}
				if zero := tt.read(protocol.NewDecoder(make([]byte, 8))); got != zero {
					t.Fatalf("read of %d bytes = %v, want %v", n, got, zero)
				}
			}
		})
	}
}

func readUint8(d *protocol.Decoder) any  { return d.ReadUint8() }
func readUint16(d *protocol.Decoder) any { return d.ReadUint16() }
func readUint32(d *protocol.Decoder) any { return d.ReadUint32() }
func readUint64(d *protocol.Decoder) any { return d.ReadUint64() }
func readInt8(d *protocol.Decoder) any   { return d.ReadInt8() }
func readInt16(d *protocol.Decoder) any  { return d.ReadInt16() }
func readInt32(d *protocol.Decoder) any  { return d.ReadInt32() }
func readInt64(d *protocol.Decoder) any  { return d.ReadInt64() }

func TestBytes(t *testing.T) {
	big := bytes.Repeat([]byte{0x5a}, 300)
	tests := []struct {
		name string
		p    []byte
		raw  string
	}{
		{"empty", []byte{}, "00000000"},
		{"one byte", []byte{0}, "0100000000"},
		{"three bytes", []byte{1, 2, 3}, "03000000010203"},
		{"300 bytes", big, "2c010000" + hex.EncodeToString(big)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := protocol.AppendBytes(nil, tt.p)
			if want := unhex(t, tt.raw); !bytes.Equal(raw, want) {
				t.Fatalf("AppendBytes = %x, want %x", raw, want)
			}
			d := protocol.NewDecoder(raw)
			if got := d.ReadBytes(); !bytes.Equal(got, tt.p) || d.Finish() != nil {
				t.Fatalf("ReadBytes = %x, %v, want %x", got, d.Finish(), tt.p)
			}
		})
	}
}

func TestReadBytesMalformed(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{"no length", ""},
		{"one byte of length", "01"},
		{"three bytes of length", "010000"},
		{"length past the end", "0300000001ff"},
		{"length one past the end", "02000000ff"},
		{"length with no data", "01000000"},
		{"largest length", "ffffffff00"},
		{"length over a signed i32", "00000080"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := unhex(t, tt.raw)
			d := protocol.NewDecoder(raw)
			if p := d.ReadBytes(); p != nil || !errors.Is(d.Err(), protocol.ErrTruncated) {
				t.Fatalf("ReadBytes = %x, %v, want %v", p, d.Err(), protocol.ErrTruncated)
			}
			if d.Len() != len(raw) {
				t.Fatalf("a failed read consumed %d bytes", len(raw)-d.Len())
			}
		})
	}
}

func TestReadBytesAtLength(t *testing.T) {
	// A length equal to the bytes left takes all of them, and the next
	// field then starts at the end.
	raw := unhex(t, "02000000aabb")
	d := protocol.NewDecoder(raw)
	if p := d.ReadBytes(); !bytes.Equal(p, []byte{0xaa, 0xbb}) {
		t.Fatalf("ReadBytes = %x", p)
	}
	if d.Len() != 0 || d.Finish() != nil {
		t.Fatalf("%d bytes left, %v", d.Len(), d.Finish())
	}
	if p := d.ReadBytes(); p != nil || !errors.Is(d.Err(), protocol.ErrTruncated) {
		t.Fatalf("ReadBytes at the end = %x, %v", p, d.Err())
	}
}

func TestReadBytesAliases(t *testing.T) {
	raw := unhex(t, "02000000aabb0100")
	d := protocol.NewDecoder(raw)
	p := d.ReadBytes()
	if cap(p) != len(p) {
		t.Fatalf("cap = %d, want %d", cap(p), len(p))
	}
	raw[4] = 0xcc
	if p[0] != 0xcc {
		t.Fatal("ReadBytes copied its result")
	}
	// Appending must not overwrite the field after it.
	_ = append(p, 0xff, 0xff)
	if got := d.ReadUint16(); got != 1 {
		t.Fatalf("the next field reads %#x after an append, want 1", got)
	}
}

func TestStrings(t *testing.T) {
	tests := []struct {
		s   string
		raw string
	}{
		{"", "00000000"},
		{"a", "0100000061"},
		{"occamy.v1", "090000006f6363616d792e7631"},
		{"héllo", "0600000068c3a96c6c6f"},
		{"世界", "06000000e4b896e7958c"},
		{"\U0001f600", "04000000f09f9880"},
		{"\x00", "0100000000"},
		{"�", "03000000efbfbd"},
		{"\U0010ffff", "04000000f48fbfbf"},
	}
	for _, tt := range tests {
		raw := protocol.AppendString(nil, tt.s)
		if want := unhex(t, tt.raw); !bytes.Equal(raw, want) {
			t.Errorf("AppendString(%q) = %x, want %x", tt.s, raw, want)
		}
		d := protocol.NewDecoder(raw)
		if got := d.ReadString(); got != tt.s || d.Finish() != nil {
			t.Errorf("ReadString(%x) = %q, %v, want %q", raw, got, d.Finish(), tt.s)
		}
	}
}

func TestReadStringMalformed(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		err  error
	}{
		{"truncated length", "0100", protocol.ErrTruncated},
		{"length past the end", "0200000061", protocol.ErrTruncated},
		{"lone continuation byte", "0100000080", protocol.ErrInvalidUTF8},
		{"invalid byte", "02000000ff61", protocol.ErrInvalidUTF8},
		{"overlong slash", "02000000c0af", protocol.ErrInvalidUTF8},
		{"overlong NUL", "02000000c080", protocol.ErrInvalidUTF8},
		{"surrogate", "03000000eda080", protocol.ErrInvalidUTF8},
		{"above U+10FFFF", "04000000f4908080", protocol.ErrInvalidUTF8},
		// A code point cut by the length is invalid even if the rest of
		// it follows in the body.
		{"cut code point", "02000000e282ac", protocol.ErrInvalidUTF8},
		{"truncated code point", "02000000e282", protocol.ErrInvalidUTF8},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := unhex(t, tt.raw)
			d := protocol.NewDecoder(raw)
			if s := d.ReadString(); s != "" || !errors.Is(d.Err(), tt.err) {
				t.Fatalf("ReadString = %q, %v, want %v", s, d.Err(), tt.err)
			}
			if d.Len() != len(raw) {
				t.Fatalf("a failed read consumed %d bytes", len(raw)-d.Len())
			}
		})
	}
}

func TestAppendStringReplacesInvalidUTF8(t *testing.T) {
	tests := []struct{ in, want string }{
		{"a\xffb", "a�b"},
		{"\xff\xfe", "�"},
		{"\xed\xa0\x80", "�"},
		{"ok\xe2\x82", "ok�"},
	}
	for _, tt := range tests {
		raw := protocol.AppendString(nil, tt.in)
		d := protocol.NewDecoder(raw)
		if got := d.ReadString(); got != tt.want || d.Finish() != nil {
			t.Errorf("AppendString(%q) reads back as %q, %v, want %q", tt.in, got, d.Finish(), tt.want)
		}
	}
}

func TestDecoderSticky(t *testing.T) {
	d := protocol.NewDecoder(unhex(t, "0102ffffffff0300"))
	if v := d.ReadUint16(); v != 0x0201 {
		t.Fatalf("ReadUint16 = %#x", v)
	}
	if p := d.ReadBytes(); p != nil || !errors.Is(d.Err(), protocol.ErrTruncated) {
		t.Fatalf("ReadBytes = %x, %v", p, d.Err())
	}
	// Enough bytes are left for these, but the first error stops them.
	if v := d.ReadUint8(); v != 0 {
		t.Fatalf("ReadUint8 after an error = %#x", v)
	}
	if v := d.ReadUint16(); v != 0 {
		t.Fatalf("ReadUint16 after an error = %#x", v)
	}
	if s := d.ReadString(); s != "" {
		t.Fatalf("ReadString after an error = %q", s)
	}
	if d.Len() != 6 || !errors.Is(d.Finish(), protocol.ErrTruncated) {
		t.Fatalf("%d bytes left, Finish = %v", d.Len(), d.Finish())
	}

	d = protocol.NewDecoder(unhex(t, "0100000080ff"))
	d.ReadString()
	d.ReadUint8()
	if !errors.Is(d.Err(), protocol.ErrInvalidUTF8) {
		t.Fatalf("the first error was replaced: %v", d.Err())
	}
}

func TestDecoderFinish(t *testing.T) {
	d := protocol.NewDecoder([]byte{1, 2})
	d.ReadUint8()
	if !errors.Is(d.Finish(), protocol.ErrTrailingData) || d.Err() != nil {
		t.Fatalf("Finish = %v, Err = %v", d.Finish(), d.Err())
	}
	d.ReadUint8()
	if err := d.Finish(); err != nil {
		t.Fatalf("Finish = %v", err)
	}
	var zero protocol.Decoder
	if zero.Len() != 0 || zero.Finish() != nil || zero.ReadBytes() != nil || !errors.Is(zero.Err(), protocol.ErrTruncated) {
		t.Fatal("the zero Decoder does not read an empty body")
	}
}

func TestAppendKeepsPrefix(t *testing.T) {
	b := []byte{0xee}
	b = protocol.AppendBytes(b, []byte{1})
	b = protocol.AppendString(b, "x")
	b = protocol.AppendInt16(b, -1)
	if want := unhex(t, "ee"+"0100000001"+"0100000078"+"ffff"); !bytes.Equal(b, want) {
		t.Fatalf("appends = %x, want %x", b, want)
	}
}

func TestDecoderAllocations(t *testing.T) {
	// A forged length allocates nothing, however large.
	forged := unhex(t, "ffffffff0102")
	if n := testing.AllocsPerRun(100, func() {
		d := protocol.NewDecoder(forged)
		d.ReadBytes()
	}); n != 0 {
		t.Errorf("ReadBytes of a forged length: %v allocations, want 0", n)
	}
	if n := testing.AllocsPerRun(100, func() {
		d := protocol.NewDecoder(forged)
		d.ReadString()
	}); n != 0 {
		t.Errorf("ReadString of a forged length: %v allocations, want 0", n)
	}

	// A large payload is not copied.
	payload := protocol.AppendBytes(nil, make([]byte, 1<<20))
	if n := testing.AllocsPerRun(100, func() {
		d := protocol.NewDecoder(payload)
		if len(d.ReadBytes()) != 1<<20 {
			panic("short read")
		}
	}); n != 0 {
		t.Errorf("ReadBytes of 1 MiB: %v allocations, want 0", n)
	}

	// Invalid UTF-8 is rejected before the string is copied.
	invalid := protocol.AppendBytes(nil, bytes.Repeat([]byte{0xff}, 1<<10))
	if n := testing.AllocsPerRun(100, func() {
		d := protocol.NewDecoder(invalid)
		d.ReadString()
	}); n != 0 {
		t.Errorf("ReadString of invalid UTF-8: %v allocations, want 0", n)
	}

	// Integers and the header allocate nothing.
	msg := protocol.AppendUint64(protocol.AppendHeader(nil, protocol.Header{Type: 1}), 7)
	if n := testing.AllocsPerRun(100, func() {
		_, body, _ := protocol.Split(msg, protocol.Limits{})
		d := protocol.NewDecoder(body)
		d.ReadUint64()
	}); n != 0 {
		t.Errorf("Split and ReadUint64: %v allocations, want 0", n)
	}
}
