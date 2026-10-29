// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package main

import (
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// netCondition is an emulated network: a round-trip time and a bandwidth
// in each direction.
type netCondition struct {
	Name string
	RTT  time.Duration
	Mbps float64 // 0 is unlimited
}

var netConditions = []netCondition{
	{Name: "N0"},
	{Name: "N1", RTT: 20 * time.Millisecond, Mbps: 100},
	{Name: "N2", RTT: 50 * time.Millisecond, Mbps: 20},
}

// proxy forwards TCP connections to a target through an emulated link. It
// delays each direction by half the RTT and serializes it at the
// bandwidth, with a bounded queue so that a fast sender meets TCP
// backpressure, as on a real link. It does not emulate loss.
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
	var perByte time.Duration
	if p.cond.Mbps > 0 {
		perByte = time.Duration(float64(time.Second) * 8 / (p.cond.Mbps * 1e6))
	}
	// The queue holds about twice the bandwidth-delay product, and at
	// least 64 KiB, which is more than one read.
	limit := 64 << 10
	if perByte > 0 {
		limit = max(limit, int(2*p.cond.RTT/perByte))
	}
	free := newBudget(limit)
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
