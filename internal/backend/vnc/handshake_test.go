// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package vnc

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"image"
	"io"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestVNCAuthResponse(t *testing.T) {
	// From OpenSSL, not from this package: the challenge is the bytes 0
	// to 15, and the key "hunter2" padded with a zero byte, each byte's
	// bits reversed (16ae762ea64e4c00):
	//
	//	openssl enc -des-ecb -provider legacy -K 16ae762ea64e4c00 -nopad
	want, _ := hex.DecodeString("ae2ffb6b2fdd58fb77567977d8d5ece4")
	var challenge [16]byte
	for i := range challenge {
		challenge[i] = byte(i)
	}
	for _, pw := range []string{"hunter2", "hunter2\x00", "hunter2\x00and the rest is ignored"} {
		if got := vncAuthResponse(pw, challenge); !bytes.Equal(got[:], want) {
			t.Errorf("vncAuthResponse(%q) = %x, want %x", pw, got, want)
		}
	}
	if got := vncAuthResponse("hunter3", challenge); bytes.Equal(got[:], want) {
		t.Error("another password gives the same response")
	}
}

func TestParseVersion(t *testing.T) {
	for _, tt := range []struct {
		in   string
		want int // 0 for an error
	}{
		{"RFB 003.003\n", 3},
		{"RFB 003.007\n", 7},
		{"RFB 003.008\n", 8},
		{"RFB 003.005\n", 3},
		{"RFB 003.006\n", 3},
		{"RFB 003.889\n", 8},
		{"RFB 004.001\n", 8},
		{"RFB 002.000\n", 0},
		{"RFB 003.00x\n", 0},
		{"RFB +03.008\n", 0},
		{"RFB 003.008\r", 0},
		{"RFB 003-008\n", 0},
		{"HTTP/1.1 400", 0},
		{"short", 0},
	} {
		got, err := parseVersion([]byte(tt.in))
		if tt.want == 0 && err == nil || tt.want != 0 && (err != nil || got != tt.want) {
			t.Errorf("parseVersion(%q) = %d, %v; want %d", tt.in, got, err, tt.want)
		}
	}
}

// script builds what a server sends in a handshake.
type script []byte

func (s script) version(v string) script { return append(s, "RFB "+v+"\n"...) }
func (s script) u8(b ...byte) script     { return append(s, b...) }
func (s script) u32(v uint32) script     { return binary.BigEndian.AppendUint32(s, v) }
func (s script) str(v string) script     { return script(appendString(s, v)) }

// init appends a ServerInit.
func (s script) init(w, h int, name string) script {
	s = binary.BigEndian.AppendUint16(s, uint16(w))
	s = binary.BigEndian.AppendUint16(s, uint16(h))
	s = append(s, serverPixelFormat.bytes()...)
	return s.str(name)
}

// none38 is the handshake of a 3.8 server with no security, up to
// ServerInit.
func none38() script { return script{}.version("003.008").u8(1, secNone).u32(0) }

func TestHandshakeScripts(t *testing.T) {
	small := limits{side: 100, pixels: 5000}
	for _, tt := range []struct {
		name  string
		in    script
		lim   limits
		sent  string      // what the client sends, if not empty
		size  image.Point // on success
		title string
		err   string // in the error; empty for success
		is    error
	}{
		{
			name: "3.8 none", in: none38().init(64, 48, "x"), lim: small,
			sent: "RFB 003.008\n\x01\x01", size: image.Pt(64, 48), title: "x",
		},
		{
			name: "3.7 none has no result", in: script{}.version("003.007").u8(1, secNone).init(10, 10, ""), lim: small,
			sent: "RFB 003.007\n\x01\x01", size: image.Pt(10, 10),
		},
		{
			name: "3.3 none", in: script{}.version("003.003").u32(secNone).init(10, 10, ""), lim: small,
			sent: "RFB 003.003\n\x01", size: image.Pt(10, 10),
		},
		{name: "Latin-1 name", in: none38().init(10, 10, "caf\xe9"), lim: small, size: image.Pt(10, 10), title: "café"},
		{name: "empty screen", in: none38().init(0, 0, ""), lim: small, err: "empty screen"},
		{name: "too wide", in: none38().init(101, 1, ""), lim: small, err: "exceeds the limits"},
		{name: "too large", in: none38().init(100, 51, ""), lim: small, err: "exceeds the limits"},
		{name: "largest screen", in: none38().init(0xffff, 0xffff, ""), lim: defaultLimits, err: "exceeds the limits"},
		{
			name: "huge name", in: none38().init(10, 10, "")[:len(none38())+20].u32(1 << 31), lim: small,
			err: "desktop name of 2147483648 bytes",
		},
		{name: "name cut short", in: none38().init(10, 10, "abcdef")[:len(none38())+26], lim: small, is: io.ErrUnexpectedEOF},
		{name: "silent server", in: script{}, lim: small, is: io.ErrUnexpectedEOF},
		{name: "no ServerInit", in: none38(), lim: small, is: io.ErrUnexpectedEOF},
		{name: "3.8 refused", in: script{}.version("003.008").u8(0).str("go away"), lim: small, is: ErrRefused, err: `"go away"`},
		{name: "3.3 refused", in: script{}.version("003.003").u32(0).str("go away"), lim: small, is: ErrRefused, err: `"go away"`},
		{
			name: "refused with a long reason", in: script{}.version("003.008").u8(0).str(strings.Repeat("a", maxReason+100)), lim: small,
			is: ErrRefused, err: `"` + strings.Repeat("a", maxReason) + `"`,
		},
		{
			name: "refused with a reason cut short", in: script{}.version("003.008").u8(0).u32(1<<30).u8('a', 'b'), lim: small,
			is: ErrRefused, err: `"ab"`,
		},
		{name: "3.8 none fails", in: script{}.version("003.008").u8(1, secNone).u32(1).str("blacklisted"), lim: small, is: ErrAuthFailed, err: "blacklisted"},
		{name: "3.3 unknown type", in: script{}.version("003.003").u32(30), lim: small, is: ErrNoSecurityType, err: "type 30"},
		{name: "types cut short", in: script{}.version("003.008").u8(3, 1), lim: small, is: io.ErrUnexpectedEOF},
		{name: "challenge cut short", in: script{}.version("003.008").u8(1, secVNCAuth).u8(1, 2, 3), lim: small, is: io.ErrUnexpectedEOF},
		{name: "no security result", in: script{}.version("003.008").u8(1, secVNCAuth).u8(make([]byte, 16)...), lim: small, is: io.ErrUnexpectedEOF},
		{
			name: "3.7 auth", in: script{}.version("003.007").u8(1, secVNCAuth).u8(make([]byte, 16)...).u32(0).init(10, 10, ""), lim: small,
			size: image.Pt(10, 10),
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var sent bytes.Buffer
			pw := func() (string, error) { return "hunter2", nil }
			init, err := handshake(bytes.NewReader(tt.in), &sent, pw, false, tt.lim)
			if tt.err == "" && tt.is == nil {
				if err != nil {
					t.Fatalf("handshake = %v", err)
				}
				if init.size != tt.size || init.name != tt.title {
					t.Errorf("handshake = %v %q, want %v %q", init.size, init.name, tt.size, tt.title)
				}
				if tt.sent != "" && sent.String() != tt.sent {
					t.Errorf("client sent %q, want %q", sent.String(), tt.sent)
				}
				return
			}
			if err == nil {
				t.Fatalf("handshake succeeded, want an error")
			}
			if tt.err != "" && !strings.Contains(err.Error(), tt.err) {
				t.Errorf("handshake = %v, want an error with %q", err, tt.err)
			}
			if tt.is != nil && !errors.Is(err, tt.is) {
				t.Errorf("handshake = %v, want %v", err, tt.is)
			}
		})
	}
}

func TestHandshakePasswordError(t *testing.T) {
	errOwner := errors.New("the owner left")
	in := script{}.version("003.008").u8(1, secVNCAuth).u8(make([]byte, 16)...)
	_, err := handshake(bytes.NewReader(in), io.Discard, func() (string, error) { return "", errOwner }, false, defaultLimits)
	if !errors.Is(err, errOwner) {
		t.Errorf("handshake = %v, want the password's error", err)
	}
}

func TestHandshakeWriteError(t *testing.T) {
	errWrite := errors.New("broken pipe")
	for _, in := range []script{
		script{}.version("003.008"),
		script{}.version("003.008").u8(1, secNone),
		script{}.version("003.003").u32(secVNCAuth).u8(make([]byte, 16)...),
		script{}.version("003.003").u32(secNone),
	} {
		// Each write after the first n fails, for every n until the
		// handshake has no more writes.
		for n := 0; n < 4; n++ {
			w := &failingWriter{n: n, err: errWrite}
			_, err := handshake(bytes.NewReader(in), w, func() (string, error) { return "", nil }, false, defaultLimits)
			if w.failed && !errors.Is(err, errWrite) {
				t.Errorf("handshake after %d writes = %v, want the write's error", n, err)
			}
		}
	}
}

// failingWriter fails every write after its first n.
type failingWriter struct {
	n      int
	err    error
	failed bool
}

func (w *failingWriter) Write(p []byte) (int, error) {
	if w.n == 0 {
		w.failed = true
		return 0, w.err
	}
	w.n--
	return len(p), nil
}

func TestHandshakeAllocations(t *testing.T) {
	// Lengths the server claims allocate nothing before the bytes arrive.
	for _, in := range []script{
		none38().init(10, 10, "")[:len(none38())+20].u32(maxName), // a name that never comes
		script{}.version("003.008").u8(0).u32(1 << 31),            // a reason that never comes
		none38().init(0xffff, 0xffff, ""),
	} {
		got := allocated(func() {
			handshake(bytes.NewReader(in), io.Discard, func() (string, error) { return "", nil }, false, defaultLimits)
		})
		if got > 16<<10 {
			t.Errorf("handshake of %q allocated %d bytes", in, got)
		}
	}
}

// allocated returns the bytes that f allocates.
func allocated(f func()) uint64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

func TestChoose(t *testing.T) {
	for _, tt := range []struct {
		offered    []byte
		preferAuth bool
		want       byte
	}{
		{[]byte{secNone}, true, secNone},
		{[]byte{secVNCAuth}, false, secVNCAuth},
		{[]byte{secNone, secVNCAuth}, true, secVNCAuth},
		{[]byte{secNone, secVNCAuth}, false, secNone},
		{[]byte{19, 30}, true, secInvalid},
		{nil, true, secInvalid},
	} {
		if got := choose(tt.offered, tt.preferAuth); got != tt.want {
			t.Errorf("choose(%v, %v) = %d, want %d", tt.offered, tt.preferAuth, got, tt.want)
		}
	}
	if !slices.Equal(appendSetup(nil)[4:20], pixelFormat[:]) {
		t.Error("SetPixelFormat does not carry the pixel format")
	}
}
