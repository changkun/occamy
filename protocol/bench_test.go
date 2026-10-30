// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package protocol_test

import (
	"fmt"
	"testing"

	"changkun.de/x/occamy/protocol"
)

var benchHeader = protocol.Header{Type: 0x0102, Flags: protocol.FlagIgnorable}

func BenchmarkAppendHeader(b *testing.B) {
	b.ReportAllocs()
	buf := make([]byte, 0, protocol.HeaderSize)
	for b.Loop() {
		buf = protocol.AppendHeader(buf[:0], benchHeader)
	}
}

func BenchmarkSplit(b *testing.B) {
	b.ReportAllocs()
	msg := protocol.AppendHeader(nil, benchHeader)
	for b.Loop() {
		if _, _, err := protocol.Split(msg, protocol.Limits{}); err != nil {
			b.Fatal(err)
		}
	}
}

// payloadSizes are byte payloads from a small update to a large tile.
var payloadSizes = []int{64, 64 << 10, 1 << 20}

func BenchmarkAppendBytes(b *testing.B) {
	for _, n := range payloadSizes {
		b.Run(fmt.Sprintf("%d", n), func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(n))
			p := make([]byte, n)
			buf := make([]byte, 0, protocol.HeaderSize+4+n)
			for b.Loop() {
				buf = protocol.AppendHeader(buf[:0], benchHeader)
				buf = protocol.AppendBytes(buf, p)
			}
		})
	}
}

func BenchmarkReadBytes(b *testing.B) {
	for _, n := range payloadSizes {
		b.Run(fmt.Sprintf("%d", n), func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(n))
			msg := protocol.AppendBytes(protocol.AppendHeader(nil, benchHeader), make([]byte, n))
			for b.Loop() {
				_, body, err := protocol.Split(msg, protocol.Limits{})
				if err != nil {
					b.Fatal(err)
				}
				d := protocol.NewDecoder(body)
				if len(d.ReadBytes()) != n || d.Finish() != nil {
					b.Fatal("short read")
				}
			}
		})
	}
}

func BenchmarkAppendString(b *testing.B) {
	b.ReportAllocs()
	s := "image/png; a clipboard MIME type or a window title of some length"
	b.SetBytes(int64(len(s)))
	buf := make([]byte, 0, 4+len(s))
	for b.Loop() {
		buf = protocol.AppendString(buf[:0], s)
	}
}

func BenchmarkReadString(b *testing.B) {
	b.ReportAllocs()
	s := "image/png; a clipboard MIME type or a window title of some length"
	b.SetBytes(int64(len(s)))
	raw := protocol.AppendString(nil, s)
	for b.Loop() {
		d := protocol.NewDecoder(raw)
		if d.ReadString() != s {
			b.Fatal("wrong string")
		}
	}
}

// benchTile is a tile of a typical size: a 64×64 PNG of 6 KiB.
var benchTile = protocol.Tile{FrameID: 7, X: 640, Y: 384, W: 64, H: 64, Codec: protocol.CodecPNG, Slot: 12, Payload: make([]byte, 6<<10)}

func BenchmarkTileAppend(b *testing.B) {
	b.ReportAllocs()
	b.SetBytes(int64(len(benchTile.Payload)))
	buf := make([]byte, 0, 8<<10)
	for b.Loop() {
		buf = benchTile.Append(buf[:0])
	}
}

func BenchmarkTileDecode(b *testing.B) {
	b.ReportAllocs()
	b.SetBytes(int64(len(benchTile.Payload)))
	msg := benchTile.Append(nil)
	l := protocol.Limits{CacheSlots: 1024}
	var t protocol.Tile
	for b.Loop() {
		_, body, err := protocol.Split(msg, l)
		if err != nil {
			b.Fatal(err)
		}
		if err := t.Decode(body, l); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDecodeKey(b *testing.B) {
	b.ReportAllocs()
	msg := protocol.Key{Seq: 1, Down: true, Keysym: 0x61, Code: 4}.Append(nil)
	r := protocol.ClientMessages()
	for b.Loop() {
		if _, err := protocol.Decode(msg, r, protocol.Limits{}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkHelloQuery(b *testing.B) {
	b.ReportAllocs()
	q, err := protocol.EncodeHelloQuery(protocol.Hello{Width: 1920, Height: 1080, Visible: true, Name: "occamy-web/1.0",
		Capabilities: protocol.Capabilities{Codecs: []protocol.Codec{protocol.CodecDeflate, protocol.CodecPNG, protocol.CodecJPEG},
			MaxTextureSize: 4096, PixelRatio: 200, CacheSlots: 1024}})
	if err != nil {
		b.Fatal(err)
	}
	for b.Loop() {
		if _, err := protocol.DecodeHelloQuery(q); err != nil {
			b.Fatal(err)
		}
	}
}
