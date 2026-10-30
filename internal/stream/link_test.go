// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package stream

import (
	"context"
	"io"
	"slices"
	"sync"
	"time"
)

// link is a simulated network path between a server and a client, for
// tests in a synctest bubble. The server's messages queue at a
// bottleneck of the given rate behind a send buffer of one message, as
// a socket with TCP_NOTSENT_LOWAT does, and then take delay to arrive;
// the client's messages take delay alone.
type link struct {
	rate  float64 // bytes per second
	delay time.Duration

	wire   chan []byte // the send buffer
	toCli  *inbox
	toSrv  *inbox
	closed chan struct{}
	once   sync.Once
}

func newLink(rate float64, delay time.Duration) *link {
	l := &link{
		rate:   rate,
		delay:  delay,
		wire:   make(chan []byte, 1),
		toCli:  newInbox(),
		toSrv:  newInbox(),
		closed: make(chan struct{}),
	}
	go l.bottleneck()
	return l
}

func (l *link) bottleneck() {
	for {
		select {
		case p := <-l.wire:
			// A plain sleep would outlive close: a bubble's time stops
			// once its test returns.
			t := time.NewTimer(time.Duration(float64(len(p)) / l.rate * float64(time.Second)))
			select {
			case <-t.C:
			case <-l.closed:
				t.Stop()
				return
			}
			l.toCli.push(p, time.Now().Add(l.delay))
		case <-l.closed:
			return
		}
	}
}

func (l *link) close() {
	l.once.Do(func() {
		close(l.closed)
		l.toCli.close()
		l.toSrv.close()
	})
}

// server and client are the two ends of the link.
func (l *link) server() Conn { return serverEnd{l} }
func (l *link) client() Conn { return clientEnd{l} }

type serverEnd struct{ l *link }

func (e serverEnd) WriteMessage(ctx context.Context, p []byte) error {
	select {
	case e.l.wire <- slices.Clone(p):
		return nil
	case <-e.l.closed:
		return io.ErrClosedPipe
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (e serverEnd) ReadMessage(ctx context.Context) ([]byte, error) { return e.l.toSrv.pop(ctx) }

type clientEnd struct{ l *link }

func (e clientEnd) WriteMessage(ctx context.Context, p []byte) error {
	select {
	case <-e.l.closed:
		return io.ErrClosedPipe
	default:
	}
	e.l.toSrv.push(slices.Clone(p), time.Now().Add(e.l.delay))
	return nil
}

func (e clientEnd) ReadMessage(ctx context.Context) ([]byte, error) { return e.l.toCli.pop(ctx) }

// inbox delivers messages in order, each no earlier than its time.
type inbox struct {
	mu     sync.Mutex
	q      []timed
	notify chan struct{}
	done   bool
}

type timed struct {
	p  []byte
	at time.Time
}

func newInbox() *inbox { return &inbox{notify: make(chan struct{}, 1)} }

func (b *inbox) push(p []byte, at time.Time) {
	b.mu.Lock()
	b.q = append(b.q, timed{p, at})
	b.mu.Unlock()
	b.poke()
}

func (b *inbox) poke() {
	select {
	case b.notify <- struct{}{}:
	default:
	}
}

func (b *inbox) close() {
	b.mu.Lock()
	b.done = true
	b.mu.Unlock()
	b.poke()
}

func (b *inbox) pop(ctx context.Context) ([]byte, error) {
	for {
		b.mu.Lock()
		if b.done {
			b.mu.Unlock()
			return nil, io.EOF
		}
		var wait time.Duration
		if len(b.q) > 0 {
			if wait = time.Until(b.q[0].at); wait <= 0 {
				p := b.q[0].p
				b.q = b.q[1:]
				b.mu.Unlock()
				return p, nil
			}
		}
		b.mu.Unlock()
		var timer <-chan time.Time
		t := time.NewTimer(wait)
		if wait > 0 {
			timer = t.C
		}
		select {
		case <-b.notify:
		case <-timer:
		case <-ctx.Done():
			t.Stop()
			return nil, ctx.Err()
		}
		t.Stop()
	}
}
