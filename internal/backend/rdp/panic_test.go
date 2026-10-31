// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package rdp

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"changkun.de/x/occamy/internal/backend"
	"changkun.de/x/occamy/internal/backend/backendtest"
	"changkun.de/x/occamy/internal/rdp/rdptest"
)

// trapSink returns images without pixels from Lock, so that drawing into
// one panics while the lock is held, as a decoder's bug that indexes past
// its data would.
type trapSink struct{ *backendtest.Sink }

func (s trapSink) Lock(r image.Rectangle) *image.RGBA {
	s.Sink.Lock(r)
	return &image.RGBA{Rect: r, Stride: 4 * r.Dx()}
}

// TestReaderPanics checks that a panic of the client while it draws, on
// Run's goroutine, which the session recovers from, releases the pixels
// it locked, and stops the writer and closes the connection: Run's
// cleanup is deferred.
func TestReaderPanics(t *testing.T) {
	checkGoroutines(t)
	s := listen(t, rdptest.Server{Width: 640, Height: 480})
	b := newBackend(t, s.config(t))
	sink := trapSink{&backendtest.Sink{}}
	panicked := make(chan any, 1)
	go func() {
		defer func() { panicked <- recover() }()
		_ = b.Run(context.Background(), sink)
	}()
	c := s.accept(t)
	waitFor(t, "the resize", func() bool { return sink.Size() == image.Pt(640, 480) })
	red := color.RGBA{0xff, 0, 0, 0xff}
	if err := c.Write(rdptest.FastPath(rdptest.Update(rdptest.FPBitmap,
		rdptest.BitmapUpdate(rdptest.RawBitmap(image.Pt(10, 20), solid(64, 64, red)))))); err != nil {
		t.Fatal(err)
	}
	select {
	case v := <-panicked:
		if v == nil {
			t.Fatal("Run returned without the panic")
		}
	case <-time.After(timeout):
		t.Fatal("Run did not panic")
	}
	if l := sink.Locked(); !l.Empty() {
		t.Errorf("the panic left %v locked", l)
	}
	checkStopped(t)
}

// panicConn is a connection whose writes panic once armed.
type panicConn struct {
	net.Conn
	armed *atomic.Bool
}

func (c panicConn) Write(p []byte) (int, error) {
	if c.armed.Load() {
		panic("the writer's bug")
	}
	return c.Conn.Write(p)
}

// TestWriterPanics checks that a panic of the writer, a goroutine of the
// backend's own beyond the session's recover, ends Run with the panic
// rather than crashing occamyd, and stops the client.
func TestWriterPanics(t *testing.T) {
	checkGoroutines(t)
	s := listen(t, rdptest.Server{})
	b := newBackend(t, s.config(t))
	var armed atomic.Bool
	b.dial = func(ctx context.Context) (net.Conn, error) {
		var d net.Dialer
		nc, err := d.DialContext(ctx, "tcp", b.addr)
		if err != nil {
			return nil, err
		}
		return panicConn{nc, &armed}, nil
	}
	r := start(t, b, &backendtest.Sink{})
	s.accept(t)
	armed.Store(true)
	if err := b.Input(backend.Key{Down: true, Code: 0x04}); err != nil {
		t.Fatal(err)
	}
	var p backend.PanicError
	if err := r.wait(t); !errors.As(err, &p) || p.Value != "the writer's bug" {
		t.Errorf("Run returned %v, want the writer's panic", err)
	}
}

// TestClientUnlockDeferred checks that every section under the RDP
// client's lock releases it by defer, so that no panic leaves it locked:
// the reader, the writer's SendInput and SuppressOutput, and Size all
// wait for it (from the review of #117).
func TestClientUnlockDeferred(t *testing.T) {
	backendtest.UnlocksDeferred(t, "../../rdp", "c.mu")
}

// TestEndPrefersPanic checks that the writer's panic is Run's result
// whatever the client returned meanwhile, so that the session logs it
// and ends with its internal reason (from the review of #117).
func TestEndPrefersPanic(t *testing.T) {
	b := &Backend{log: quiet}
	p := backend.PanicError{Value: "the writer's bug"}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, tc := range []struct {
		name string
		ctx  context.Context
		err  error
	}{
		{"closed", context.Background(), fmt.Errorf("read: %w", net.ErrClosed)},
		{"ctx done", canceled, fmt.Errorf("read: %w", net.ErrClosed)},
		{"server hung up first", context.Background(), io.EOF},
		{"protocol error from buffered data", context.Background(), errors.New("rdp: bad PDU")},
		{"server disconnected", context.Background(), nil},
	} {
		if got := b.end(tc.ctx, tc.err, p); !errors.As(got, new(backend.PanicError)) {
			t.Errorf("%s: end = %v, want the writer's panic", tc.name, got)
		}
	}
}
