// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package protocol_test

import (
	"encoding/base64"
	"errors"
	"math"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"changkun.de/x/occamy/protocol"
)

func TestHelloQuery(t *testing.T) {
	q, err := protocol.EncodeHelloQuery(webHello)
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(q, "+/=") {
		t.Fatalf("the query %q is not unpadded base64url", q)
	}
	h, err := protocol.DecodeHelloQuery(q)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(h, webHello) {
		t.Fatalf("DecodeHelloQuery = %+v, want %+v", h, webHello)
	}

	// The largest hello fits, and one byte more does not.
	fixed := len(protocol.Hello{}.Append(nil))
	largest := protocol.Hello{Name: strings.Repeat("n", protocol.MaxHelloSize-fixed)}
	q, err = protocol.EncodeHelloQuery(largest)
	if err != nil {
		t.Fatalf("a hello of MaxHelloSize: %v", err)
	}
	if len(q) != 2731 {
		t.Errorf("a hello of MaxHelloSize takes %d characters, want 2731", len(q))
	}
	if h, err := protocol.DecodeHelloQuery(q); err != nil || h.Name != largest.Name {
		t.Fatalf("decoding a hello of MaxHelloSize: %v", err)
	}
	largest.Name += "n"
	if _, err := protocol.EncodeHelloQuery(largest); !errors.Is(err, protocol.ErrTooLarge) {
		t.Fatalf("a hello over MaxHelloSize: %v", err)
	}
	over := base64.RawURLEncoding.EncodeToString(largest.Append(nil))
	if _, err := protocol.DecodeHelloQuery(over); !errors.Is(err, protocol.ErrTooLarge) {
		t.Fatalf("decoding a hello over MaxHelloSize: %v", err)
	}

	// The size is checked before anything is decoded: refusing a 1 MiB
	// query allocates its error and nothing in proportion to the query.
	// The count of allocations would not show that, and varies with the
	// toolchain.
	huge := strings.Repeat("A", 1<<20)
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for range 10 {
		if _, err := protocol.DecodeHelloQuery(huge); !errors.Is(err, protocol.ErrTooLarge) {
			t.Fatal(err)
		}
	}
	runtime.ReadMemStats(&after)
	if n := (after.TotalAlloc - before.TotalAlloc) / 10; n > 1<<10 {
		t.Errorf("refusing a 1 MiB query allocates %d bytes", n)
	}

	// A hello of another version is refused before the rest is read.
	future := base64.RawURLEncoding.EncodeToString(append(
		protocol.AppendUint16(protocol.AppendHeader(nil, protocol.Header{Type: protocol.TypeHello}), 2),
		"a layout of the future"...))
	if _, err := protocol.DecodeHelloQuery(future); !errors.Is(err, protocol.ErrUnsupported) {
		t.Fatalf("a hello of version 2: %v", err)
	}

	// Reserved flags are ignored, as in any message.
	flagged := protocol.Hello{Name: "f"}.Append(nil)
	flagged[3] = 0x80
	if h, err := protocol.DecodeHelloQuery(base64.RawURLEncoding.EncodeToString(flagged)); err != nil || h.Name != "f" {
		t.Fatalf("a hello with a reserved flag: %+v, %v", h, err)
	}
}

func TestTokenSubprotocol(t *testing.T) {
	const token = "eyJhbGciOiJFZERTQSJ9.eyJzdWIiOiJhbGljZSJ9.c2ln"
	sp := protocol.TokenSubprotocol(token)
	if !strings.HasPrefix(sp, "occamy.v1.token.") {
		t.Fatalf("TokenSubprotocol = %q", sp)
	}
	// A subprotocol is an HTTP token: no separators or spaces.
	for _, c := range sp {
		if c <= ' ' || c >= 0x7f || strings.ContainsRune("()<>@,;:\\\"/[]?={}", c) {
			t.Fatalf("TokenSubprotocol = %q holds %q", sp, c)
		}
	}
	got, err := protocol.TokenFromSubprotocols([]string{protocol.Subprotocol + ", " + sp})
	if err != nil || got != token {
		t.Fatalf("TokenFromSubprotocols = %q, %v", got, err)
	}

	// Errors never hold the token, whatever is wrong.
	secret := protocol.TokenSubprotocol("s3cr3t-t0ken")
	enc := strings.TrimPrefix(secret, "occamy.v1.token.")
	for _, offered := range [][]string{
		{secret},
		{"occamy.v1", secret, secret},
		{"occamy.v1", secret + "="},
		{"occamy.v1", secret + "+"},
		{"occamy.v1", secret + "A"},
	} {
		_, err := protocol.TokenFromSubprotocols(offered)
		if !errors.Is(err, protocol.ErrInvalid) {
			t.Errorf("%q: %v, want %v", offered, err, protocol.ErrInvalid)
			continue
		}
		if strings.Contains(err.Error(), "s3cr3t") || strings.Contains(err.Error(), enc) {
			t.Errorf("the error shows the token: %v", err)
		}
	}
}

func TestAgree(t *testing.T) {
	a := protocol.Agree(webCaps, serverCaps)
	want := protocol.Capabilities{
		Codecs:         []protocol.Codec{protocol.CodecDeflate, protocol.CodecJPEG, protocol.CodecPNG, protocol.CodecRaw, protocol.CodecCached},
		MaxTextureSize: 4096,
		Terminal:       1,
		PixelRatio:     200,
		CacheSlots:     1024,
		FrameWindow:    2,
		MaxMessageSize: protocol.DefaultMaxMessageSize,
	}
	if !reflect.DeepEqual(a, want) {
		t.Fatalf("Agree = %+v, want %+v", a, want)
	}

	a = protocol.Agree(
		protocol.Capabilities{Codecs: []protocol.Codec{protocol.CodecWebP, protocol.CodecPNG, 0x80}, MaxTextureSize: 8192,
			FrameWindow: 4, MaxMessageSize: 16 << 20, CacheSlots: 10,
			Unknown: []protocol.Capability{{ID: 99, Value: []byte{1}}}},
		protocol.Capabilities{Codecs: []protocol.Codec{protocol.CodecPNG, protocol.CodecWebP}, MaxTextureSize: 2048, Terminal: 1,
			MaxMessageSize: 1 << 20},
	)
	want = protocol.Capabilities{
		Codecs:         []protocol.Codec{protocol.CodecWebP, protocol.CodecPNG},
		MaxTextureSize: 2048,
		FrameWindow:    2,
		MaxMessageSize: 1 << 20,
	}
	if !reflect.DeepEqual(a, want) {
		t.Fatalf("Agree = %+v, want %+v", a, want)
	}

	// Nothing stated agrees on the defaults, stated explicitly.
	a = protocol.Agree(protocol.Capabilities{}, protocol.Capabilities{})
	if !reflect.DeepEqual(a, protocol.Capabilities{FrameWindow: 2, MaxMessageSize: protocol.DefaultMaxMessageSize}) {
		t.Fatalf("Agree of nothing = %+v", a)
	}
	// The agreed capabilities encode and decode.
	roundTrip(t, "agreed", &protocol.Welcome{ViewerID: 1, Role: protocol.RoleOwner, Class: protocol.ClassGraphical,
		Capabilities: protocol.Agree(webCaps, serverCaps)})
}

func TestCapabilitiesLimits(t *testing.T) {
	l := protocol.Capabilities{MaxTextureSize: 256, CacheSlots: 64, MaxMessageSize: 1 << 20}.Limits()
	if l != (protocol.Limits{MaxMessageSize: 1 << 20, MaxTextureSize: 256, CacheSlots: 64}) {
		t.Fatalf("Limits = %+v", l)
	}
	if l := (protocol.Capabilities{}).Limits(); l != (protocol.Limits{}) {
		t.Fatalf("Limits of nothing = %+v", l)
	}
	if l := (protocol.Capabilities{MaxMessageSize: math.MaxUint32}).Limits(); l.MaxMessageSize != math.MaxInt32 {
		t.Fatalf("Limits of the largest message size = %+v", l)
	}
}

func TestEnumStrings(t *testing.T) {
	for _, tt := range []struct{ got, want string }{
		{protocol.RoleOwner.String(), "owner"},
		{protocol.RoleViewer.String(), "viewer"},
		{protocol.RoleReadOnly.String(), "read-only"},
		{protocol.Role(0).String(), "Role(0)"},
		{protocol.ClassTerminal.String(), "terminal"},
		{protocol.ClassGraphical.String(), "graphical"},
		{protocol.Class(9).String(), "Class(9)"},
		{protocol.CodecRaw.String(), "raw"},
		{protocol.CodecDeflate.String(), "deflate"},
		{protocol.CodecPNG.String(), "png"},
		{protocol.CodecJPEG.String(), "jpeg"},
		{protocol.CodecWebP.String(), "webp"},
		{protocol.CodecCached.String(), "cached"},
		{protocol.Codec(0x80).String(), "Codec(128)"},
	} {
		if tt.got != tt.want {
			t.Errorf("String() = %q, want %q", tt.got, tt.want)
		}
	}
}
