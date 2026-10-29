// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package main

import (
	"io"
	"math"
	"net"
	"slices"
	"testing"
	"time"
)

// echoServer echoes every connection until it closes.
func echoServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				io.Copy(c, c)
			}()
		}
	}()
	return ln.Addr().String()
}

func dialProxy(t *testing.T, cond netCondition) (*proxy, net.Conn) {
	t.Helper()
	p, err := startProxy(echoServer(t), cond)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.close)
	c, err := net.Dial("tcp", p.addr())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return p, c
}

func TestProxyDelay(t *testing.T) {
	p, c := dialProxy(t, netCondition{RTT: 40 * time.Millisecond})
	buf := make([]byte, 4)
	for range 3 {
		start := time.Now()
		c.Write([]byte("ping"))
		if _, err := io.ReadFull(c, buf); err != nil {
			t.Fatal(err)
		}
		// The message crosses the link once each way.
		if rtt := time.Since(start); rtt < 40*time.Millisecond || rtt > 60*time.Millisecond {
			t.Errorf("round trip through the proxy took %v, want about 40ms", rtt)
		}
	}
	if up, down := p.bytes(); up != 12 || down != 12 {
		t.Errorf("bytes = %d up, %d down, want 12 and 12", up, down)
	}
}

func TestProxyRate(t *testing.T) {
	_, c := dialProxy(t, netCondition{Mbps: 20})
	const n = 500 << 10
	go c.Write(make([]byte, n))
	start := time.Now()
	if _, err := io.ReadFull(c, make([]byte, n)); err != nil {
		t.Fatal(err)
	}
	// 500 KiB at 20 Mbit/s takes about 205ms in each direction, and the
	// two directions overlap.
	want := time.Duration(n * 8 * float64(time.Second) / 20e6)
	if got := time.Since(start); got < want || got > 2*want+50*time.Millisecond {
		t.Errorf("500 KiB through 20 Mbit/s took %v, want about %v", got, want)
	}
}

func TestSummarize(t *testing.T) {
	var d []time.Duration
	for i := 100; i >= 1; i-- {
		d = append(d, time.Duration(i)*time.Millisecond)
	}
	got := summarize(d)
	want := percentiles{N: 100, P50: 50, P95: 95, P99: 99, Max: 100}
	if *got != want {
		t.Errorf("summarize = %+v, want %+v", *got, want)
	}
	if summarize(nil) != nil {
		t.Error("summarize(nil) is not nil")
	}
}

func TestTableMedians(t *testing.T) {
	t1 := func(run int, p95 float64) result {
		return result{Scenario: "T1", Protocol: "ssh", Series: "occamy", Net: "N0", Run: run,
			Latency: &percentiles{P50: 2, P95: p95, P99: p95}}
	}
	t3 := func(run int, kib int64) result {
		return result{Scenario: "T3", Protocol: "ssh", Series: "occamy", Net: "N0", Run: run,
			Seconds: 2, BytesDown: kib << 10, CPUMS: map[string]float64{"occamy": 1, "guacd": 2, "ssh": 5}}
	}
	broken := t3(2, 0)
	broken.Seconds, broken.CPUMS, broken.Error = 0, nil, "session ended"
	rows := table([]result{t1(0, 3), t3(0, 10), t1(1, 50), t3(1, 30), t1(2, 4), broken})

	if len(rows) != 2 || rows[0].first.Scenario != "T1" || rows[1].first.Scenario != "T3" {
		t.Fatalf("rows are not T1, then T3: %+v", rows)
	}
	// One disturbed run does not move the median.
	if got := rows[0].cells[colP95]; got != 4 {
		t.Errorf("T1 p95 = %v, want 4", got)
	}
	// A run that failed has no numbers, and the others still count.
	if got := rows[1].cells[colRate]; got != 10 {
		t.Errorf("T3 KiB/s = %v, want 10, the median of 5 and 15", got)
	}
	if got := rows[1].cells[colServerCPU]; got != 3 {
		t.Errorf("T3 server CPU = %v, want 3", got)
	}
	if !math.IsNaN(rows[1].cells[colP50]) {
		t.Errorf("T3 has a latency: %v", rows[1].cells[colP50])
	}
	if want := []string{"run 3 error: session ended"}; !slices.Equal(rows[1].notes, want) {
		t.Errorf("T3 notes = %q, want %q", rows[1].notes, want)
	}
}
