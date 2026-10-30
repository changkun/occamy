// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package protocol_test

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
	"unsafe"

	"changkun.de/x/occamy/protocol"
)

// within reports whether p lies inside data, that is, whether reading p
// copied nothing.
func within(p, data []byte) bool {
	if len(p) == 0 {
		return true
	}
	start := uintptr(unsafe.Pointer(unsafe.SliceData(data)))
	q := uintptr(unsafe.Pointer(unsafe.SliceData(p)))
	return q >= start && q+uintptr(len(p)) <= start+uintptr(len(data))
}

// FuzzSplit checks that Split never panics, enforces the size limit and
// the header size, returns a body aliasing the message, and that the
// header encodes back to the bytes it came from. It also checks that
// Accept follows the ignorable flag.
func FuzzSplit(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{1, 0, 0})
	f.Add([]byte{1, 0, 0, 0})
	f.Add([]byte{2, 0, 1, 0, 0xaa})
	f.Add([]byte{2, 0, 0xfe, 0xff})
	f.Add(bytes.Repeat([]byte{0xff}, 65))

	const limit = 64
	var r protocol.Registry
	r.Register(1, "one")
	r.Register(0x0100, "two")
	r.Register(0xffff, "last")

	f.Fuzz(func(t *testing.T, msg []byte) {
		h, body, err := protocol.Split(msg, protocol.Limits{MaxMessageSize: limit})
		switch {
		case len(msg) > limit:
			if !errors.Is(err, protocol.ErrTooLarge) {
				t.Fatalf("a %d-byte message: %v, want %v", len(msg), err, protocol.ErrTooLarge)
			}
			return
		case len(msg) < protocol.HeaderSize:
			if !errors.Is(err, protocol.ErrTruncated) {
				t.Fatalf("a %d-byte message: %v, want %v", len(msg), err, protocol.ErrTruncated)
			}
			return
		case err != nil:
			t.Fatalf("Split(%x): %v", msg, err)
		}
		if !within(body, msg) || len(body) != len(msg)-protocol.HeaderSize {
			t.Fatalf("the body of %x is not the rest of the message: %x", msg, body)
		}
		if got := append(protocol.AppendHeader(nil, h), body...); !bytes.Equal(got, msg) {
			t.Fatalf("%x encodes back to %x", msg, got)
		}

		ok, err := r.Accept(h)
		_, known := r.Lookup(h.Type)
		switch {
		case known:
			if !ok || err != nil {
				t.Fatalf("Accept(%+v) = %v, %v for a known type", h, ok, err)
			}
		case h.Flags&protocol.FlagIgnorable != 0:
			if ok || err != nil {
				t.Fatalf("Accept(%+v) = %v, %v for an ignorable unknown type", h, ok, err)
			}
		default:
			if ok || !errors.Is(err, protocol.ErrUnsupported) {
				t.Fatalf("Accept(%+v) = %v, %v, want %v", h, ok, err, protocol.ErrUnsupported)
			}
		}
	})
}

// FuzzDecoder reads a body with a sequence of primitives that ops
// chooses. Reading must never panic, a byte string must alias the body
// and a string must be valid UTF-8 no longer than what was left, and the
// fields read must encode back to exactly the bytes they consumed.
func FuzzDecoder(f *testing.F) {
	f.Add([]byte{0, 1, 2, 3}, []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10})
	f.Add([]byte{8, 9}, []byte("\x03\x00\x00\x00abc\x02\x00\x00\x00\xc3\xa9"))
	f.Add([]byte{9}, []byte("\x02\x00\x00\x00\xc0\xaf"))
	f.Add([]byte{8}, []byte{0xff, 0xff, 0xff, 0xff, 0})
	f.Add([]byte{4, 5, 6, 7, 3}, bytes.Repeat([]byte{0x80}, 15))

	f.Fuzz(func(t *testing.T, ops, body []byte) {
		if len(ops) > 64 {
			ops = ops[:64]
		}
		d := protocol.NewDecoder(body)
		var out []byte
		for _, op := range ops {
			left, n := d.Len(), len(out)
			switch op % 10 {
			case 0:
				v := d.ReadUint8()
				out = protocol.AppendUint8(out, v)
			case 1:
				v := d.ReadUint16()
				out = protocol.AppendUint16(out, v)
			case 2:
				v := d.ReadUint32()
				out = protocol.AppendUint32(out, v)
			case 3:
				v := d.ReadUint64()
				out = protocol.AppendUint64(out, v)
			case 4:
				v := d.ReadInt8()
				out = protocol.AppendInt8(out, v)
			case 5:
				v := d.ReadInt16()
				out = protocol.AppendInt16(out, v)
			case 6:
				v := d.ReadInt32()
				out = protocol.AppendInt32(out, v)
			case 7:
				v := d.ReadInt64()
				out = protocol.AppendInt64(out, v)
			case 8:
				p := d.ReadBytes()
				if !within(p, body) || cap(p) != len(p) {
					t.Fatalf("ReadBytes copied or exposed more than its field: len %d, cap %d", len(p), cap(p))
				}
				out = protocol.AppendBytes(out, p)
			case 9:
				s := d.ReadString()
				if !utf8.ValidString(s) || len(s) > left {
					t.Fatalf("ReadString = %q with %d bytes left", s, left)
				}
				out = protocol.AppendString(out, s)
			}
			if err := d.Err(); err != nil {
				if !errors.Is(err, protocol.ErrTruncated) && !errors.Is(err, protocol.ErrInvalidUTF8) {
					t.Fatalf("unexpected error %v", err)
				}
				if d.Len() != left {
					t.Fatalf("a failed read consumed %d bytes", left-d.Len())
				}
				// Drop the zero value the failed read returned.
				out = out[:n]
				break
			}
		}
		consumed := len(body) - d.Len()
		if !bytes.Equal(out, body[:consumed]) {
			t.Fatalf("fields read from %x encode to %x, want %x", body, out, body[:consumed])
		}
	})
}

// FuzzRoundTrip encodes a message with every primitive and decodes it
// again.
func FuzzRoundTrip(f *testing.F) {
	f.Add(uint16(1), uint16(0), uint8(0), uint16(0), uint32(0), uint64(0), int8(0), int16(0), int32(0), int64(0), []byte{}, "")
	f.Add(uint16(0xffff), uint16(1), uint8(255), uint16(0x0102), uint32(0x01020304), uint64(1<<63), int8(-128), int16(-1), int32(-1<<31), int64(-1), []byte{1, 2, 3}, "héllo")
	f.Add(uint16(7), uint16(0xfffe), uint8(1), uint16(1), uint32(1), uint64(1), int8(1), int16(1), int32(1), int64(1), []byte{0xff}, "bad\xffutf8")

	f.Fuzz(func(t *testing.T, typ, flags uint16, u8 uint8, u16 uint16, u32 uint32, u64 uint64,
		i8 int8, i16 int16, i32 int32, i64 int64, p []byte, s string) {
		h := protocol.Header{Type: protocol.Type(typ), Flags: protocol.Flags(flags)}
		msg := protocol.AppendHeader(nil, h)
		msg = protocol.AppendUint8(msg, u8)
		msg = protocol.AppendUint16(msg, u16)
		msg = protocol.AppendUint32(msg, u32)
		msg = protocol.AppendUint64(msg, u64)
		msg = protocol.AppendInt8(msg, i8)
		msg = protocol.AppendInt16(msg, i16)
		msg = protocol.AppendInt32(msg, i32)
		msg = protocol.AppendInt64(msg, i64)
		msg = protocol.AppendBytes(msg, p)
		msg = protocol.AppendString(msg, s)

		gotH, body, err := protocol.Split(msg, protocol.Limits{})
		if err != nil || gotH != h {
			t.Fatalf("Split = %+v, %v, want %+v", gotH, err, h)
		}
		d := protocol.NewDecoder(body)
		if v := d.ReadUint8(); v != u8 {
			t.Fatalf("uint8 %d, want %d", v, u8)
		}
		if v := d.ReadUint16(); v != u16 {
			t.Fatalf("uint16 %d, want %d", v, u16)
		}
		if v := d.ReadUint32(); v != u32 {
			t.Fatalf("uint32 %d, want %d", v, u32)
		}
		if v := d.ReadUint64(); v != u64 {
			t.Fatalf("uint64 %d, want %d", v, u64)
		}
		if v := d.ReadInt8(); v != i8 {
			t.Fatalf("int8 %d, want %d", v, i8)
		}
		if v := d.ReadInt16(); v != i16 {
			t.Fatalf("int16 %d, want %d", v, i16)
		}
		if v := d.ReadInt32(); v != i32 {
			t.Fatalf("int32 %d, want %d", v, i32)
		}
		if v := d.ReadInt64(); v != i64 {
			t.Fatalf("int64 %d, want %d", v, i64)
		}
		if v := d.ReadBytes(); !bytes.Equal(v, p) {
			t.Fatalf("bytes %x, want %x", v, p)
		}
		// AppendString replaces invalid UTF-8, and leaves valid UTF-8
		// as it is.
		want := strings.ToValidUTF8(s, "�")
		if v := d.ReadString(); v != want {
			t.Fatalf("string %q, want %q", v, want)
		}
		if err := d.Finish(); err != nil {
			t.Fatalf("Finish: %v", err)
		}
	})
}

// decodeErrors are the errors that decoding a message may return.
var decodeErrors = []error{
	protocol.ErrTruncated, protocol.ErrTooLarge, protocol.ErrInvalidUTF8,
	protocol.ErrTrailingData, protocol.ErrInvalid, protocol.ErrUnsupported,
}

func isDecodeError(err error) bool {
	for _, e := range decodeErrors {
		if errors.Is(err, e) {
			return true
		}
	}
	return false
}

// checkAliases fails if a byte string field of v, a message or a field,
// does not lie within msg.
func checkAliases(t *testing.T, v reflect.Value, msg []byte) {
	switch v.Kind() {
	case reflect.Slice:
		if v.Type() == bytesType {
			if p := v.Bytes(); !within(p, msg) || cap(p) != len(p) {
				t.Fatalf("a byte string of %x is a copy or exposes more than its field", msg)
			}
			return
		}
		for i := range v.Len() {
			checkAliases(t, v.Index(i), msg)
		}
	case reflect.Struct:
		for i := range v.NumField() {
			checkAliases(t, v.Field(i), msg)
		}
	}
}

// FuzzMessage decodes a message as a server and as a client. Decoding
// must never panic and must fail only with this package's errors. A
// message that decodes must encode back to the bytes it came from, save
// the header's flags, which Append leaves zero, and its byte strings must
// alias the message.
func FuzzMessage(f *testing.F) {
	for _, nm := range messageValues() {
		f.Add(nm.msg.Append(nil))
	}
	for _, v := range invalidMessages() {
		raw, err := hex.DecodeString(v.Hex)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(raw)
	}
	f.Add(protocol.AppendHeader(nil, protocol.Header{Type: 0x02ff, Flags: protocol.FlagIgnorable}))

	l := protocol.Limits{MaxMessageSize: 1 << 16, MaxTextureSize: 64, CacheSlots: 8}
	registries := []*protocol.Registry{protocol.ClientMessages(), protocol.ServerMessages()}
	f.Fuzz(func(t *testing.T, msg []byte) {
		for _, r := range registries {
			m, err := protocol.Decode(msg, r, l)
			switch {
			case err != nil:
				if m != nil || !isDecodeError(err) {
					t.Fatalf("Decode(%x) = %v, %v", msg, m, err)
				}
				continue
			case m == nil:
				h, _, _ := protocol.Split(msg, l)
				if _, known := r.Lookup(h.Type); known || !h.Ignorable() {
					t.Fatalf("Decode(%x) skipped a message of type %#04x", msg, uint16(h.Type))
				}
				continue
			}
			out := m.Append(nil)
			if len(out) != len(msg) || !bytes.Equal(out[:2], msg[:2]) || !bytes.Equal(out[4:], msg[4:]) {
				t.Fatalf("%x decodes to %+v, which encodes to %x", msg, m, out)
			}
			checkAliases(t, reflect.ValueOf(m).Elem(), msg)
		}
	})
}

// FuzzHelloQuery decodes a hello query parameter. Decoding must never
// panic, must refuse a value too long for MaxHelloSize, and a hello that
// decodes must encode back to the same value, save the header's flags.
func FuzzHelloQuery(f *testing.F) {
	q, err := protocol.EncodeHelloQuery(webHello)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(q)
	f.Add("AQEAAAEAAAAAAAAAAAAAAAAAAAAA")
	for _, v := range invalidHelloQueries(f) {
		f.Add(v.Query)
	}

	f.Fuzz(func(t *testing.T, s string) {
		h, err := protocol.DecodeHelloQuery(s)
		if len(s) > 2731 {
			if !errors.Is(err, protocol.ErrTooLarge) {
				t.Fatalf("a query of %d characters: %v", len(s), err)
			}
			return
		}
		if err != nil {
			if !isDecodeError(err) {
				t.Fatalf("DecodeHelloQuery(%q): %v", s, err)
			}
			return
		}
		raw, err := base64.RawURLEncoding.DecodeString(s)
		if err != nil {
			t.Fatalf("%q decodes as a hello but not as base64url: %v", s, err)
		}
		raw[2], raw[3] = 0, 0
		got, err := protocol.EncodeHelloQuery(h)
		if err != nil || got != base64.RawURLEncoding.EncodeToString(raw) {
			t.Fatalf("%q decodes to %+v, which encodes to %q, %v", s, h, got, err)
		}
	})
}

const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"

// FuzzTokenSubprotocol checks that every token survives its subprotocol,
// and that parsing any header never panics and never returns an error
// that holds what was offered.
func FuzzTokenSubprotocol(f *testing.F) {
	f.Add("eyJhbGciOiJFZERTQSJ9.e30.c2ln", "occamy.v1, occamy.v1.token.c2VjcmV0")
	f.Add("\x00\xff", "occamy.v1.token.,occamy.v1.token.x")
	f.Add("t", " occamy.v1 ,\toccamy.v1.token.dA= ")

	f.Fuzz(func(t *testing.T, token, header string) {
		got, err := protocol.TokenFromSubprotocols([]string{protocol.Subprotocol, protocol.TokenSubprotocol(token)})
		switch {
		case token == "":
			if !errors.Is(err, protocol.ErrInvalid) {
				t.Fatalf("an empty token: %q, %v", got, err)
			}
		case err != nil || got != token:
			t.Fatalf("token %q comes back as %q, %v", token, got, err)
		}

		got, err = protocol.TokenFromSubprotocols([]string{header})
		if err != nil {
			if !errors.Is(err, protocol.ErrInvalid) {
				t.Fatalf("TokenFromSubprotocols(%q): %v", header, err)
			}
			// An offered token of 12 characters or more from the
			// base64url alphabet cannot appear in an error by chance,
			// since no word of the error messages is that long.
			for p := range strings.SplitSeq(header, ",") {
				enc, ok := strings.CutPrefix(strings.Trim(p, " \t"), "occamy.v1.token.")
				if ok && len(enc) >= 12 && strings.Trim(enc, alphabet) == "" && strings.Contains(err.Error(), enc) {
					t.Fatalf("the error %q shows the offered %q", err, enc)
				}
			}
			return
		}
		if got != "" && !strings.Contains(header, protocol.TokenSubprotocol(got)) {
			t.Fatalf("TokenFromSubprotocols(%q) = %q, which was not offered", header, got)
		}
	})
}
