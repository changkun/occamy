// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package rdp

import (
	"encoding/binary"
	"errors"
	"unicode/utf16"
)

// errShort is the error of a reader that ran past its end.
var errShort = errors.New("rdp: truncated PDU")

// reader reads little-endian fields from a PDU. The first read past the
// end sets err, after which every read returns zero, so that a parser
// checks err once at the end.
type reader struct {
	b   []byte
	err error
}

func (r *reader) take(n int) []byte {
	if r.err != nil {
		return nil
	}
	if n < 0 || n > len(r.b) {
		r.err = errShort
		r.b = nil
		return nil
	}
	p := r.b[:n:n]
	r.b = r.b[n:]
	return p
}

func (r *reader) u8() uint8 {
	p := r.take(1)
	if p == nil {
		return 0
	}
	return p[0]
}

func (r *reader) u16() uint16 {
	p := r.take(2)
	if p == nil {
		return 0
	}
	return binary.LittleEndian.Uint16(p)
}

func (r *reader) u16be() uint16 {
	p := r.take(2)
	if p == nil {
		return 0
	}
	return binary.BigEndian.Uint16(p)
}

func (r *reader) u32() uint32 {
	p := r.take(4)
	if p == nil {
		return 0
	}
	return binary.LittleEndian.Uint32(p)
}

func (r *reader) skip(n int) { r.take(n) }

func (r *reader) len() int { return len(r.b) }

// writer appends little-endian fields to a PDU. The methods that take an
// int truncate it to the field's width; callers keep values in range.
type writer struct{ b []byte }

func (w *writer) u8(v int) { w.b = append(w.b, byte(v)) } // #nosec G115 -- see writer

func (w *writer) u16(v int) {
	w.b = binary.LittleEndian.AppendUint16(w.b, uint16(v)) // #nosec G115 -- see writer
}

func (w *writer) u16be(v int) {
	w.b = binary.BigEndian.AppendUint16(w.b, uint16(v)) // #nosec G115 -- see writer
}

func (w *writer) u32(v uint32) { w.b = binary.LittleEndian.AppendUint32(w.b, v) }

func (w *writer) bytes(p []byte) { w.b = append(w.b, p...) }

func (w *writer) zero(n int) { w.b = append(w.b, make([]byte, n)...) }

// putU16 overwrites the little-endian field at off, for lengths known
// only after the rest of a PDU is written.
func (w *writer) putU16(off, v int) {
	binary.LittleEndian.PutUint16(w.b[off:], uint16(v)) // #nosec G115 -- see writer
}

// utf16le encodes s as UTF-16LE, without a terminator.
func utf16le(s string) []byte {
	u := utf16.Encode([]rune(s))
	b := make([]byte, 0, 2*len(u))
	for _, c := range u {
		b = binary.LittleEndian.AppendUint16(b, c)
	}
	return b
}

// fixedUTF16 encodes s as UTF-16LE in a field of n bytes, truncated so
// that a terminator fits, and zero padded.
func fixedUTF16(s string, n int) []byte {
	b := utf16le(s)
	if len(b) > n-2 {
		b = b[:(n-2)&^1]
	}
	return append(b, make([]byte, n-len(b))...)
}
