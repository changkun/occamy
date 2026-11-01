// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package rdp

import (
	"bytes"
	"errors"
	"io"
	"net"
	"slices"
	"testing"

	"changkun.de/x/occamy/internal/rdp/rdptest"
)

// replayConn serves data over and over, left bytes of it, and then ends.
type replayConn struct {
	net.Conn
	data []byte
	off  int
	left int
}

func (r *replayConn) Read(p []byte) (int, error) {
	if r.left == 0 {
		return 0, io.EOF
	}
	n := min(copy(p, r.data[r.off:]), r.left)
	r.off = (r.off + n) % len(r.data)
	r.left -= n
	return n, nil
}

func (r *replayConn) Write(p []byte) (int, error) { return len(p), nil }
func (r *replayConn) Close() error                { return nil }

// ignoredWork is what a server can have a client read and do little
// with: each entry's wire holds items of the work it names.
func ignoredWork() []struct {
	name  string
	wire  []byte
	items int
} {
	const ch, n = 1003, 4096
	flow := make([]byte, 8)
	flow[0], flow[1] = 0x00, 0x80
	unknown := rdptest.Update(0xe, nil)
	orphan := rdptest.Update(0xe|fragNext<<4, nil)
	noRects := rdptest.Update(fpBitmap, []byte{1, 0, 0, 0})
	return []struct {
		name  string
		wire  []byte
		items int
	}{
		{"fast-path PDUs of an unknown update", bytes.Repeat(rdptest.FastPath(unknown), n), n},
		{"fast-path PDUs of no updates", bytes.Repeat(rdptest.FastPath(), n), n},
		{"unknown updates in a PDU", rdptest.FastPath(slices.Repeat([][]byte{unknown}, 10000)...), 10000},
		{"orphan fragments in a PDU", rdptest.FastPath(slices.Repeat([][]byte{orphan}, 10000)...), 10000},
		{"ignored orders in a PDU", rdptest.FastPath(slices.Repeat([][]byte{rdptest.Update(fpOrders, nil)}, 10000)...), 10000},
		{"bitmap updates of no rectangles", rdptest.FastPath(slices.Repeat([][]byte{noRects}, 4000)...), 4000},
		{"an update of the most bytes", rdptest.FastPath(rdptest.Update(0xe, make([]byte, rdptest.MaxFastPath-3))), rdptest.MaxFastPath},
		{"flow control PDUs in a frame", rdptest.Indication(ch, bytes.Repeat(flow, 2000)), 2000},
		{"share control PDUs of an unknown type", rdptest.Indication(ch, bytes.Repeat([]byte{6, 0, 0xf, 0, 0, 0}, 2000)), 2000},
		{"frames on an unused channel", bytes.Repeat(rdptest.Indication(1099, nil), n), n},
		{"frames of the most bytes on an unused channel", rdptest.Indication(1099, make([]byte, 0x7fff-16)), 0x7fff - 16},
	}
}

// What a server has the client read and ignore counts against its
// budget, however it is packed.
func TestIgnoredCharged(t *testing.T) {
	for _, tc := range ignoredWork() {
		t.Run(tc.name, func(t *testing.T) {
			c, clock := testClock(1920, 1080)
			c.ioChannel = 1003
			conn := &replayConn{data: tc.wire, left: len(tc.wire)}
			c.conn, c.t = conn, newTransport(conn, nil)
			if err := c.pace(0); err != nil {
				t.Fatal(err)
			}
			tokens, slept := c.tokens, clock.waited()
			if err := c.Run(t.Context(), &screen{}); !errors.Is(err, io.EOF) {
				t.Fatal(err)
			}
			units := tokens - c.tokens + (clock.waited()-slept).Seconds()*decodeRate
			if want := float64(len(tc.wire)) / 2; units < want {
				t.Errorf("%.0f units for %d bytes, want at least %.0f", units, len(tc.wire), want)
			}
		})
	}
}

// BenchmarkIgnored measures what the client's read loop does with what
// it ignores, against what that costs it of decodeRate.
func BenchmarkIgnored(b *testing.B) {
	for _, tc := range ignoredWork() {
		b.Run(tc.name, func(b *testing.B) {
			c, clock := testClock(1920, 1080)
			c.ioChannel = 1003
			conn := &replayConn{data: tc.wire, left: b.N * len(tc.wire)}
			c.conn, c.t = conn, newTransport(conn, nil)
			if err := c.pace(0); err != nil {
				b.Fatal(err)
			}
			tokens, slept := c.tokens, clock.waited()
			b.SetBytes(int64(len(tc.wire)))
			b.ReportAllocs()
			b.ResetTimer()
			err := c.Run(b.Context(), &screen{})
			b.StopTimer()
			if !errors.Is(err, io.EOF) {
				b.Fatal(err)
			}
			units := tokens - c.tokens + (clock.waited()-slept).Seconds()*decodeRate
			ns := float64(b.Elapsed().Nanoseconds())
			b.ReportMetric(ns/float64(b.N)/float64(tc.items), "ns/item")
			b.ReportMetric(units/float64(b.N)/float64(tc.items), "units/item")
			if units > 0 {
				b.ReportMetric(ns/units*decodeRate/1e9, "core")
			}
		})
	}
}
