// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package protocol_test

import (
	"bytes"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"

	"changkun.de/x/occamy/protocol"
)

var testLimits = goldenLimits.limits()

// receiver returns a registry that accepts messages of type t.
func receiver(t protocol.Type) *protocol.Registry {
	if sender(t) == "server" {
		return protocol.ServerMessages()
	}
	return protocol.ClientMessages()
}

// roundTrip encodes m, decodes it, and checks that the result encodes to
// the same bytes and has the same fields.
func roundTrip(t *testing.T, name string, m protocol.Message) {
	t.Helper()
	msg := m.Append(nil)
	got, err := protocol.Decode(msg, receiver(m.Type()), testLimits)
	if err != nil {
		t.Fatalf("%s: Decode(%x): %v", name, msg, err)
	}
	if got.Type() != m.Type() {
		t.Fatalf("%s: decodes to a %s", name, got.Type())
	}
	if enc := got.Append(nil); !bytes.Equal(enc, msg) {
		t.Fatalf("%s: encodes to %x, then to %x", name, msg, enc)
	}
	want, have := toJSON(reflect.ValueOf(m).Elem()), toJSON(reflect.ValueOf(got).Elem())
	if !reflect.DeepEqual(want, have) {
		t.Fatalf("%s: decodes to %v, want %v", name, have, want)
	}
}

func TestRoundTrip(t *testing.T) {
	for _, nm := range messageValues() {
		roundTrip(t, nm.name, nm.msg)
	}
	// Extremes of every field kind.
	big := bytes.Repeat([]byte{0xa5}, 64<<10)
	for _, nm := range []namedMessage{
		{"ping largest", &protocol.Ping{Nonce: math.MaxUint32, Time: math.MaxUint64}},
		{"close with an unknown code", &protocol.Close{Code: 4999, Reason: "\U0001f600"}},
		{"key largest", &protocol.Key{Seq: math.MaxUint32, Down: true, Keysym: math.MaxUint32, Code: math.MaxUint16}},
		{"pointer with reserved buttons", &protocol.Pointer{Seq: 1, X: math.MaxUint16, Y: math.MaxUint16, Buttons: 0xff}},
		{"wheel smallest", &protocol.Wheel{Seq: 1, DX: math.MinInt32, DY: math.MaxInt32, Mode: protocol.WheelPixels}},
		{"term-input of 64 KiB", &protocol.TermInput{Seq: 1, Data: big}},
		{"term-data empty", &protocol.TermData{}},
		{"prompt-reply with empty answers", &protocol.PromptReply{ID: 1, Answers: []string{"", "", "x"}}},
		{"prompt without fields", &protocol.Prompt{ID: math.MaxUint32, Name: "n", Instruction: "i"}},
		{"prompt with fields", &protocol.Prompt{ID: 1, Fields: []protocol.PromptField{{Label: "a"}, {Label: "b", Secret: true}, {}}}},
		{"tile largest", &protocol.Tile{FrameID: math.MaxUint32, X: math.MaxUint16, Y: math.MaxUint16, W: 4096, H: 4096,
			Codec: protocol.CodecJPEG, Payload: big}},
		{"tile largest in a slot", &protocol.Tile{FrameID: 1, W: 1, H: protocol.MaxCachedPixels,
			Codec: protocol.CodecJPEG, Slot: 1024, Payload: big}},
		{"tile webp", &protocol.Tile{FrameID: 1, W: 1, H: 1, Codec: protocol.CodecWebP, Payload: []byte("RIFF")}},
		{"tile deflate empty payload", &protocol.Tile{FrameID: 1, W: 1, H: 1, Codec: protocol.CodecDeflate}},
		{"cursor png", &protocol.Cursor{HotX: 31, HotY: 31, W: 32, H: 32, Codec: protocol.CodecPNG, Payload: []byte("png")}},
		{"frame-end largest", &protocol.FrameEnd{FrameID: math.MaxUint32, ServerTime: math.MaxUint64, LastSeq: math.MaxUint32}},
		{"viewer-joined owner", &protocol.ViewerJoined{ViewerID: math.MaxUint32, Role: protocol.RoleOwner}},
		{"welcome with no capabilities", &protocol.Welcome{ViewerID: 1, Role: protocol.RoleViewer, Class: protocol.ClassTerminal}},
		{"hello with every capability", &protocol.Hello{Width: math.MaxUint16, Height: 1, Cols: 2, Rows: 3, Name: "é",
			Capabilities: protocol.Capabilities{
				Codecs:         []protocol.Codec{0x80, protocol.CodecRaw, 0xff, 7},
				MaxTextureSize: math.MaxUint16, Terminal: math.MaxUint8, PixelRatio: math.MaxUint16,
				CacheSlots: math.MaxUint16, FrameWindow: math.MaxUint8, MaxMessageSize: math.MaxUint32,
				Unknown: []protocol.Capability{{ID: 8, Value: nil}, {ID: 9, Value: []byte{0}}, {ID: math.MaxUint16, Value: big[:300]}},
			}}},
	} {
		roundTrip(t, nm.name, nm.msg)
	}
}

// TestTruncatedAndTrailing checks that every proper prefix of each
// vector's body is truncated and that a byte after it is trailing data.
func TestTruncatedAndTrailing(t *testing.T) {
	for _, nm := range messageValues() {
		msg := nm.msg.Append(nil)
		r := receiver(nm.msg.Type())
		for n := protocol.HeaderSize; n < len(msg); n++ {
			if _, err := protocol.Decode(msg[:n], r, testLimits); !errors.Is(err, protocol.ErrTruncated) {
				t.Fatalf("%s cut to %d of %d bytes: %v, want %v", nm.name, n, len(msg), err, protocol.ErrTruncated)
			}
		}
		if _, err := protocol.Decode(append(msg, 0), r, testLimits); !errors.Is(err, protocol.ErrTrailingData) {
			t.Fatalf("%s with a byte more: %v, want %v", nm.name, err, protocol.ErrTrailingData)
		}
	}
}

func TestMessageTypes(t *testing.T) {
	client, server := protocol.ClientMessages(), protocol.ServerMessages()
	types := allTypes()
	if len(types) != 29 {
		t.Errorf("%d message types, want 29", len(types))
	}
	for _, typ := range types {
		m := protocol.New(typ)
		if m.Type() != typ {
			t.Errorf("New(%#04x).Type() = %#04x", uint16(typ), uint16(m.Type()))
		}
		_, inClient := client.Lookup(typ)
		_, inServer := server.Lookup(typ)
		switch sender(typ) {
		case "either":
			if !inClient || !inServer {
				t.Errorf("%s is not in both registries", typ)
			}
		case "client":
			if !inClient || inServer {
				t.Errorf("%s from the client: client registry %v, server registry %v", typ, inClient, inServer)
			}
		case "server":
			if inClient || !inServer {
				t.Errorf("%s from the server: client registry %v, server registry %v", typ, inClient, inServer)
			}
		default:
			t.Errorf("%s (%#04x) is outside the v1 ranges", typ, uint16(typ))
		}
		if name, _ := receiver(typ).Lookup(typ); name != typ.String() {
			t.Errorf("%#04x is registered as %q and named %q", uint16(typ), name, typ.String())
		}
		// The header is the type with no flags.
		if h := m.Append(nil); h[0] != byte(typ) || h[1] != byte(typ>>8) || h[2] != 0 || h[3] != 0 {
			t.Errorf("%s starts with %x", typ, h[:4])
		}
	}
	if protocol.New(0) != nil || protocol.New(0x0110) != nil || protocol.New(0xffff) != nil {
		t.Error("New returned a message for a type that is not v1")
	}
	if got := protocol.Type(0x0abc).String(); got != "Type(0x0abc)" {
		t.Errorf("Type(0x0abc).String() = %q", got)
	}
	if protocol.TypeClientClipboard.String() != "clipboard" || protocol.TypeServerClipboard.String() != "clipboard" {
		t.Error("the clipboard types are not named clipboard")
	}
}

func TestDecodeDispatch(t *testing.T) {
	client := protocol.ClientMessages()
	// An unknown ignorable message is skipped.
	msg := protocol.AppendHeader(nil, protocol.Header{Type: 0x01ff, Flags: protocol.FlagIgnorable})
	if m, err := protocol.Decode(append(msg, 1, 2, 3), client, testLimits); m != nil || err != nil {
		t.Fatalf("an unknown ignorable message: %v, %v", m, err)
	}
	// A message for the other direction is unknown to the receiver.
	tile := protocol.Tile{FrameID: 1, W: 1, H: 1, Codec: protocol.CodecPNG}.Append(nil)
	if _, err := protocol.Decode(tile, client, testLimits); !errors.Is(err, protocol.ErrUnsupported) {
		t.Fatalf("a tile sent to the server: %v", err)
	}
	// A type registered beside the v1 types has no message to decode.
	r := protocol.ClientMessages()
	r.Register(0x7001, "note")
	if _, err := protocol.Decode(protocol.AppendHeader(nil, protocol.Header{Type: 0x7001}), r, testLimits); !errors.Is(err, protocol.ErrUnsupported) {
		t.Fatalf("a registered type with no v1 message: %v", err)
	}
	// The size limit comes first.
	key := protocol.Key{Seq: 1}.Append(nil)
	if _, err := protocol.Decode(key, client, protocol.Limits{MaxMessageSize: len(key) - 1}); !errors.Is(err, protocol.ErrTooLarge) {
		t.Fatalf("a message over the limit: %v", err)
	}
	// Errors name the message.
	_, err := protocol.Decode(key[:6], client, testLimits)
	if err == nil || !strings.HasPrefix(err.Error(), "key: ") {
		t.Fatalf("the error does not name the message: %v", err)
	}
}

func TestDecodeReusesMessage(t *testing.T) {
	// Decoding into a message that held other values replaces all of
	// them.
	m := protocol.Prompt{ID: 9, Name: "old", Fields: []protocol.PromptField{{Label: "x"}}}
	_, body, _ := protocol.Split(protocol.Prompt{ID: 1}.Append(nil), testLimits)
	if err := m.Decode(body, testLimits); err != nil {
		t.Fatal(err)
	}
	if m.ID != 1 || m.Name != "" || m.Fields != nil {
		t.Fatalf("decoded into a used prompt: %+v", m)
	}
	h := protocol.Hello{Capabilities: protocol.Capabilities{CacheSlots: 3, Unknown: []protocol.Capability{{ID: 9}}}}
	_, body, _ = protocol.Split(protocol.Hello{Name: "n"}.Append(nil), testLimits)
	if err := h.Decode(body, testLimits); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(h, protocol.Hello{Name: "n"}) {
		t.Fatalf("decoded into a used hello: %+v", h)
	}
}

func TestTileLimits(t *testing.T) {
	decode := func(m protocol.Tile, l protocol.Limits) error {
		_, body, _ := protocol.Split(m.Append(nil), l)
		var got protocol.Tile
		return got.Decode(body, l)
	}
	png := func(w, h uint16) protocol.Tile {
		return protocol.Tile{W: w, H: h, Codec: protocol.CodecPNG, Payload: []byte{1}}
	}
	tests := []struct {
		name string
		m    protocol.Tile
		l    protocol.Limits
		err  error
	}{
		{"default limit", png(protocol.DefaultMaxTextureSize, protocol.DefaultMaxTextureSize), protocol.Limits{}, nil},
		{"over the default limit", png(protocol.DefaultMaxTextureSize+1, 1), protocol.Limits{}, protocol.ErrTooLarge},
		{"negative limit means the default", png(1, protocol.DefaultMaxTextureSize+1), protocol.Limits{MaxTextureSize: -1}, protocol.ErrTooLarge},
		{"at an agreed limit", png(128, 128), protocol.Limits{MaxTextureSize: 128}, nil},
		{"over an agreed limit", png(128, 129), protocol.Limits{MaxTextureSize: 128}, protocol.ErrTooLarge},
		{"agreed limit over the default", png(8192, 1), protocol.Limits{MaxTextureSize: 8192}, nil},
		{"last slot", protocol.Tile{W: 1, H: 1, Codec: protocol.CodecCached, Slot: 16}, protocol.Limits{CacheSlots: 16}, nil},
		{"slot past the last", protocol.Tile{W: 1, H: 1, Codec: protocol.CodecCached, Slot: 17}, protocol.Limits{CacheSlots: 16}, protocol.ErrInvalid},
		{"slot with no cache", protocol.Tile{W: 1, H: 1, Codec: protocol.CodecCached, Slot: 1}, protocol.Limits{CacheSlots: -1}, protocol.ErrInvalid},
		{"raw of the right size", protocol.Tile{W: 3, H: 5, Codec: protocol.CodecRaw, Payload: make([]byte, 60)}, protocol.Limits{}, nil},
		{"raw of the wrong size", protocol.Tile{W: 3, H: 5, Codec: protocol.CodecRaw, Payload: make([]byte, 59)}, protocol.Limits{}, protocol.ErrInvalid},
	}
	for _, tt := range tests {
		if err := decode(tt.m, tt.l); !errors.Is(err, tt.err) || (err == nil) != (tt.err == nil) {
			t.Errorf("%s: %v, want %v", tt.name, err, tt.err)
		}
	}
}

// TestTileDoesNotCopy checks that decoding a tile neither allocates nor
// copies its payload.
func TestTileDoesNotCopy(t *testing.T) {
	msg := protocol.Tile{FrameID: 1, W: 64, H: 64, Codec: protocol.CodecRaw, Slot: 3, Payload: make([]byte, 64*64*4)}.Append(nil)
	var m protocol.Tile
	if n := testing.AllocsPerRun(100, func() {
		_, body, err := protocol.Split(msg, testLimits)
		if err != nil {
			panic(err)
		}
		if err := m.Decode(body, testLimits); err != nil {
			panic(err)
		}
	}); n != 0 {
		t.Errorf("decoding a tile: %v allocations, want 0", n)
	}
	if !within(m.Payload, msg) || cap(m.Payload) != len(m.Payload) {
		t.Error("the payload is a copy, or exposes the bytes after it")
	}
	// Decode allocates only the message it returns.
	r := protocol.ServerMessages()
	if n := testing.AllocsPerRun(100, func() {
		if _, err := protocol.Decode(msg, r, testLimits); err != nil {
			panic(err)
		}
	}); n > 1 {
		t.Errorf("Decode of a tile: %v allocations, want at most 1", n)
	}
	// Encoding into a buffer with room allocates nothing.
	buf := make([]byte, 0, len(msg))
	if n := testing.AllocsPerRun(100, func() { buf = m.Append(buf[:0]) }); n != 0 {
		t.Errorf("appending a tile: %v allocations, want 0", n)
	}
	// A forged list count allocates nothing.
	forged := protocol.AppendHeader(nil, protocol.Header{Type: protocol.TypePromptReply})
	forged = protocol.AppendCount(protocol.AppendUint32(forged, 1), protocol.MaxCount)
	var reply protocol.PromptReply
	if n := testing.AllocsPerRun(100, func() {
		_, body, _ := protocol.Split(forged, testLimits)
		if err := reply.Decode(body, testLimits); !errors.Is(err, protocol.ErrTruncated) {
			panic(err)
		}
	}); n != 0 {
		t.Errorf("a forged count: %v allocations, want 0", n)
	}
}

// sendableStatus reports whether a WebSocket close frame may carry
// status c (RFC 6455, section 7.4).
func sendableStatus(c uint16) bool {
	return c >= 1000 && c <= 1003 || c >= 1007 && c <= 1014 || c >= 3000 && c <= 4999
}

func TestCloseCodes(t *testing.T) {
	codes := map[protocol.CloseCode]string{
		1000: "normal", 1001: "server-shutdown", 1002: "protocol-error", 1003: "unsupported",
		1009: "too-large", 1011: "internal-error", 4000: "unauthorized", 4001: "forbidden",
		4002: "session-closed", 4003: "owner-left", 4004: "idle", 4005: "remote-ended",
		4006: "backend-failed", 4007: "viewer-limit", 4008: "too-slow",
	}
	for c, name := range codes {
		if c.String() != name {
			t.Errorf("%d.String() = %q, want %q", uint16(c), c.String(), name)
		}
		// Every code is a status that a WebSocket close frame may carry.
		if !sendableStatus(uint16(c)) {
			t.Errorf("%s (%d) is not a WebSocket close status", c, uint16(c))
		}
	}
	if got := protocol.CloseCode(4999).String(); got != "CloseCode(4999)" {
		t.Errorf("CloseCode(4999).String() = %q", got)
	}

	var r protocol.Registry
	_, unsupported := r.Accept(protocol.Header{Type: 1})
	_, version := protocol.DecodeHelloQuery("AQEAAAIA")
	for _, tt := range []struct {
		err  error
		want protocol.CloseCode
	}{
		{nil, protocol.CloseNormal},
		{unsupported, protocol.CloseUnsupported},
		{version, protocol.CloseUnsupported},
		{protocol.Limits{}.CheckSize(protocol.DefaultMaxMessageSize + 1), protocol.CloseTooLarge},
		{protocol.ErrTruncated, protocol.CloseProtocolError},
		{protocol.ErrInvalidUTF8, protocol.CloseProtocolError},
		{protocol.ErrTrailingData, protocol.CloseProtocolError},
		{protocol.ErrInvalid, protocol.CloseProtocolError},
		{errors.New("broken pipe"), protocol.CloseInternalError},
	} {
		if got := protocol.CloseCodeFor(tt.err); got != tt.want {
			t.Errorf("CloseCodeFor(%v) = %s, want %s", tt.err, got, tt.want)
		}
	}
}
