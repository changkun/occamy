// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package main

import (
	"errors"
	"io"
	"math"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// netCondition is an emulated network: a round-trip time and a bandwidth
// in each direction, and in a loss study the share of packets to the
// client that it drops.
type netCondition struct {
	Name string
	RTT  time.Duration
	Mbps float64 // 0 is unlimited
	// Loss is the percentage of packets to the client dropped at random.
	// Only a link in the kernel drops any (shapeArgs); the proxy carries
	// a byte stream, which has no packets.
	Loss float64
}

// netConditions are the ladder of specs/008-native-architecture/
// comparison-benchmark.md: local, fast, medium, slow and very slow.
var netConditions = []netCondition{
	{Name: "N0"},
	{Name: "N1", RTT: 20 * time.Millisecond, Mbps: 100},
	{Name: "N2", RTT: 50 * time.Millisecond, Mbps: 20},
	{Name: "N3", RTT: 100 * time.Millisecond, Mbps: 5},
	{Name: "N4", RTT: 250 * time.Millisecond, Mbps: 1},
}

// slowness is how many times longer than on a fast link a sample may take
// on n before it fails: 1 down to 20 Mbit/s, and in proportion below, so
// that a slow link measures how slow it is rather than fail.
func (n netCondition) slowness() int64 {
	if n.Mbps <= 0 || n.Mbps >= 20 {
		return 1
	}
	return int64(math.Ceil(20 / n.Mbps))
}

// perByte is how long the link takes to serialize a byte, 0 when the
// bandwidth is unlimited.
func (n netCondition) perByte() time.Duration {
	if n.Mbps <= 0 {
		return 0
	}
	return time.Duration(float64(time.Second) * 8 / (n.Mbps * 1e6))
}

// queueBytes is how many bytes a direction of the link may hold, those it
// delays included: about twice the bandwidth-delay product, and at least
// 64 KiB, which is more than one read of the proxy.
func (n netCondition) queueBytes() int {
	limit := 64 << 10
	if perByte := n.perByte(); perByte > 0 {
		limit = max(limit, int(2*n.RTT/perByte))
	}
	return limit
}

// mtu is the size of the packets a link in the kernel queues.
const mtu = 1500

// shapeArgs returns the arguments of the link image's shape
// (docker/bench/netem/shape.sh) that emulate n in the kernel on the
// client connections to port: half the RTT each way in microseconds, the
// bandwidth in kbit/s, the loss in percent, and the proxy's queue in
// packets of the MTU.
func (n netCondition) shapeArgs(port int) []string {
	return []string{
		strconv.Itoa(port),
		strconv.FormatInt((n.RTT / 2).Microseconds(), 10),
		strconv.FormatInt(int64(math.Round(n.Mbps*1000)), 10),
		strconv.FormatFloat(n.Loss, 'f', -1, 64),
		strconv.Itoa((n.queueBytes() + mtu - 1) / mtu),
	}
}

// proxy forwards TCP connections to a target through an emulated link. It
// delays each direction by half the RTT and serializes it at the
// bandwidth, with a bounded queue so that a fast sender meets TCP
// backpressure, as on a real link. It does not emulate loss: a loss
// study emulates the whole link in the kernel instead (shapeArgs), and its
// proxy only counts the bytes.
type proxy struct {
	ln     net.Listener
	target string
	cond   netCondition

	up, down atomic.Int64 // bytes from and to the client
	wg       sync.WaitGroup
}

func startProxy(target string, cond netCondition) (*proxy, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	p := &proxy{ln: ln, target: target, cond: cond}
	p.wg.Add(1)
	go p.serve()
	return p, nil
}

func (p *proxy) addr() string { return p.ln.Addr().String() }

func (p *proxy) close() {
	p.ln.Close()
	p.wg.Wait()
}

// bytes returns the bytes forwarded so far from and to the client.
func (p *proxy) bytes() (up, down int64) { return p.up.Load(), p.down.Load() }

func (p *proxy) serve() {
	defer p.wg.Done()
	for {
		c, err := p.ln.Accept()
		if err != nil {
			return
		}
		go p.forward(c)
	}
}

func (p *proxy) forward(c net.Conn) {
	defer c.Close()
	t, err := net.Dial("tcp", p.target)
	if err != nil {
		return
	}
	defer t.Close()
	done := make(chan struct{}, 2)
	go func() { p.link(t, c, &p.up); done <- struct{}{} }()
	go func() { p.link(c, t, &p.down); done <- struct{}{} }()
	<-done
}

type chunk struct {
	b       []byte
	deliver time.Time
}

// budget counts the bytes a link may still queue.
type budget struct {
	mu     sync.Mutex
	cond   sync.Cond
	free   int
	closed bool
}

func newBudget(n int) *budget {
	b := &budget{free: n}
	b.cond.L = &b.mu
	return b
}

// take waits until n bytes are free and takes them. It reports false once
// the budget is closed.
func (b *budget) take(n int) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	for b.free < n && !b.closed {
		b.cond.Wait()
	}
	b.free -= n
	return !b.closed
}

func (b *budget) give(n int) {
	b.mu.Lock()
	b.free += n
	b.mu.Unlock()
	b.cond.Broadcast()
}

func (b *budget) close() {
	b.mu.Lock()
	b.closed = true
	b.mu.Unlock()
	b.cond.Broadcast()
}

// link copies src to dst through one direction of the emulated link.
func (p *proxy) link(dst, src net.Conn, count *atomic.Int64) {
	delay := p.cond.RTT / 2
	perByte := p.cond.perByte()
	free := newBudget(p.cond.queueBytes())
	queue := make(chan chunk, 1024)

	go func() {
		defer close(queue)
		var departed time.Time
		for {
			b := make([]byte, 16<<10)
			n, err := src.Read(b)
			if n > 0 {
				if !free.take(n) {
					return
				}
				count.Add(int64(n))
				departed = later(time.Now(), departed).Add(time.Duration(n) * perByte)
				queue <- chunk{b: b[:n], deliver: departed.Add(delay)}
			}
			if err != nil {
				return
			}
		}
	}()
	for c := range queue {
		if d := time.Until(c.deliver); d > 0 {
			time.Sleep(d)
		}
		_, err := dst.Write(c.b)
		free.give(len(c.b))
		if err != nil {
			break
		}
	}
	// If the writer stopped first, unblock the reader, then end both sides.
	free.close()
	src.Close()
	for range queue {
	}
	if cw, ok := dst.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	}
}

func later(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// errClosed reports a use of a closed connection, which ends a session
// normally.
func errClosed(err error) bool {
	return errors.Is(err, net.ErrClosed) || errors.Is(err, io.EOF)
}
