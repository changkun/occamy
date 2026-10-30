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
