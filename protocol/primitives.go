// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package protocol

import (
	"encoding/binary"
	"math"
	"strings"
	"unicode/utf8"
)

// A body is a sequence of fields without names or tags. Integers are
// fixed-size and little-endian, signed ones in two's complement. Byte
// strings and UTF-8 strings are a u32 length in bytes followed by that
// many bytes.

// lengthSize is the size of the length prefix of a byte or UTF-8 string.
const lengthSize = 4

// AppendUint8 appends v to b.
func AppendUint8(b []byte, v uint8) []byte { return append(b, v) }

// AppendUint16 appends v to b.
func AppendUint16(b []byte, v uint16) []byte { return binary.LittleEndian.AppendUint16(b, v) }

// AppendUint32 appends v to b.
func AppendUint32(b []byte, v uint32) []byte { return binary.LittleEndian.AppendUint32(b, v) }

// AppendUint64 appends v to b.
func AppendUint64(b []byte, v uint64) []byte { return binary.LittleEndian.AppendUint64(b, v) }

// AppendInt8 appends v to b.
func AppendInt8(b []byte, v int8) []byte { return append(b, byte(v)) } //nolint:gosec // two's complement

// AppendInt16 appends v to b.
func AppendInt16(b []byte, v int16) []byte { return AppendUint16(b, uint16(v)) } //nolint:gosec // two's complement

// AppendInt32 appends v to b.
func AppendInt32(b []byte, v int32) []byte { return AppendUint32(b, uint32(v)) } //nolint:gosec // two's complement

// AppendInt64 appends v to b.
func AppendInt64(b []byte, v int64) []byte { return AppendUint64(b, uint64(v)) } //nolint:gosec // two's complement

// AppendBytes appends p to b as a byte string. It panics if p is longer
// than a u32 can count, which no message size allows.
func AppendBytes(b, p []byte) []byte {
	b = appendLength(b, len(p))
	return append(b, p...)
}

// AppendString appends s to b as a UTF-8 string. Each run of invalid
// UTF-8 in s is replaced by U+FFFD, since a receiver rejects the string
// otherwise. It panics if s is longer than a u32 can count.
func AppendString(b []byte, s string) []byte {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "\uFFFD")
	}
	b = appendLength(b, len(s))
	return append(b, s...)
}

func appendLength(b []byte, n int) []byte {
	if n < 0 || int64(n) > math.MaxUint32 {
		panic("protocol: field longer than a u32 length")
	}
	return AppendUint32(b, uint32(n))
}

// A Decoder reads the fields of a message body in order. Its zero value
// reads an empty body.
//
// Errors are sticky. A read that fails consumes nothing and returns the
// zero value, so does every read after it, and Err reports the first
// error. A message decoder can thus read all fields and check once.
type Decoder struct {
	buf []byte
	err error
}

// NewDecoder returns a Decoder that reads body.
func NewDecoder(body []byte) *Decoder { return &Decoder{buf: body} }

// Len returns the number of unread bytes.
func (d *Decoder) Len() int { return len(d.buf) }

// Err returns the first error a read met, or nil.
func (d *Decoder) Err() error { return d.err }

// Finish returns the first error a read met. If every read succeeded but
// bytes are left, it returns ErrTrailingData.
func (d *Decoder) Finish() error {
	if d.err == nil && len(d.buf) > 0 {
		return ErrTrailingData
	}
	return d.err
}

// next consumes n bytes, or fails with ErrTruncated if fewer are left.
func (d *Decoder) next(n int) []byte {
	if d.err != nil {
		return nil
	}
	if len(d.buf) < n {
		d.err = ErrTruncated
		return nil
	}
	p := d.buf[:n:n]
	d.buf = d.buf[n:]
	return p
}

// ReadUint8 reads a u8.
func (d *Decoder) ReadUint8() uint8 {
	p := d.next(1)
	if p == nil {
		return 0
	}
	return p[0]
}

// ReadUint16 reads a little-endian u16.
func (d *Decoder) ReadUint16() uint16 {
	p := d.next(2)
	if p == nil {
		return 0
	}
	return binary.LittleEndian.Uint16(p)
}

// ReadUint32 reads a little-endian u32.
func (d *Decoder) ReadUint32() uint32 {
	p := d.next(4)
	if p == nil {
		return 0
	}
	return binary.LittleEndian.Uint32(p)
}

// ReadUint64 reads a little-endian u64.
func (d *Decoder) ReadUint64() uint64 {
	p := d.next(8)
	if p == nil {
		return 0
	}
	return binary.LittleEndian.Uint64(p)
}

// ReadInt8 reads an i8.
func (d *Decoder) ReadInt8() int8 { return int8(d.ReadUint8()) } //nolint:gosec // two's complement

// ReadInt16 reads a little-endian i16.
func (d *Decoder) ReadInt16() int16 { return int16(d.ReadUint16()) } //nolint:gosec // two's complement

// ReadInt32 reads a little-endian i32.
func (d *Decoder) ReadInt32() int32 { return int32(d.ReadUint32()) } //nolint:gosec // two's complement

// ReadInt64 reads a little-endian i64.
func (d *Decoder) ReadInt64() int64 { return int64(d.ReadUint64()) } //nolint:gosec // two's complement

// ReadBytes reads a byte string without copying it: the result aliases
// the body and stays valid as long as the body does. Its capacity ends at
// its length, so appending to it cannot overwrite the next field.
//
// The length is checked against the rest of the body, which the maximum
// message size bounds, so a forged length allocates nothing.
func (d *Decoder) ReadBytes() []byte {
	if d.err != nil {
		return nil
	}
	if len(d.buf) < lengthSize {
		d.err = ErrTruncated
		return nil
	}
	n := binary.LittleEndian.Uint32(d.buf)
	rest := d.buf[lengthSize:]
	if uint64(n) > uint64(len(rest)) {
		d.err = ErrTruncated
		return nil
	}
	p := rest[:n:n]
	d.buf = rest[n:]
	return p
}

// ReadString reads a UTF-8 string, and fails with ErrInvalidUTF8 if it is
// not valid UTF-8. It copies the string, whose length the rest of the body
// bounds.
func (d *Decoder) ReadString() string {
	buf := d.buf
	p := d.ReadBytes()
	if d.err != nil {
		return ""
	}
	if !utf8.Valid(p) {
		d.buf, d.err = buf, ErrInvalidUTF8
		return ""
	}
	return string(p)
}
