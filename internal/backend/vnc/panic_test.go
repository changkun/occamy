// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package vnc

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"changkun.de/x/occamy/internal/backend"
	"changkun.de/x/occamy/internal/backend/backendtest"
)

// trapSink returns images without pixels from Lock, so that drawing into
// one panics while the lock is held, as a decoder's bug that indexes past
// its data would.
type trapSink struct{ *backendtest.Sink }

func (s trapSink) Lock(r image.Rectangle) *image.RGBA {
	s.Sink.Lock(r)
	return &image.RGBA{Rect: r}
}

// TestPanicWhileDrawing checks that each way the decoder draws releases
// the rows it locked when it panics while drawing, which the session
// recovers from: viewers' writers wait for those rows.
func TestPanicWhileDrawing(t *testing.T) {
	img := pattern(64, 48, 3)
	two := image.NewRGBA(img.Rect)
	for i := range two.Pix {
		two.Pix[i] = 0xff
	}
	two.SetRGBA(1, 1, color.RGBA{A: 0xff})
	r := image.Rect(0, 0, 32, 16)
	for _, tc := range []struct {
		name string
		rect func(e *encoder) []byte
	}{
		{"raw", func(e *encoder) []byte { return putRaw(e.pf, img, r) }},
		{"hextile", func(e *encoder) []byte { return e.hextile(img, r) }},
		{"zrle", func(e *encoder) []byte { return e.zrle(img, r) }},
		{"tight fill", func(e *encoder) []byte { return e.tightFill(r, color.RGBA{R: 1, A: 0xff}, 0) }},
		{"tight palette", func(e *encoder) []byte {
			return e.tightBasic(two, r, tightOptions{filter: tightFilterPalette, stream: 2})
		}},
		{"tight copy", func(e *encoder) []byte { return e.tightBasic(img, r, tightOptions{}) }},
		{"tight jpeg", func(e *encoder) []byte { return e.tightJPEG(img, r, 80, 0) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sink := trapSink{&backendtest.Sink{}}
			sink.Resize(64, 48)
			msg := putUpdate(tc.rect(newEncoder(wantPixelFormat)))
			d := newDecoder(context.Background(), bytes.NewReader(msg), sink, image.Pt(64, 48), defaultLimits, discardLogger)
			func() {
				defer func() {
					if recover() == nil {
						t.Fatal("drawing into no pixels did not panic")
					}
				}()
				_, err := d.next()
				t.Fatalf("decoded with %v", err)
			}()
			if l := sink.Locked(); !l.Empty() {
				t.Errorf("the panic left %v locked", l)
			}
		})
	}
}

// TestReaderPanics checks that a panic of the reader, on Run's
// goroutine, which the session recovers from, stops the writer and
// closes the connection: Run's cleanup is deferred.
func TestReaderPanics(t *testing.T) {
	checkGoroutines(t)
	srv := newServer(t, serverOptions{})
	b := newBackend(t, Config{Addr: srv.addr})
	b.Demand(backend.Demand{Visible: true})
	panicked := make(chan any, 1)
	go func() {
		defer func() { panicked <- recover() }()
		_ = b.Run(context.Background(), trapSink{&backendtest.Sink{}})
	}()
	c := srv.accept(t)
	expect[requestMsg](t, c)
	img := pattern(64, 48, 1)
	c.send(t, putUpdate(putRaw(c.pf, img, img.Rect)))
	select {
	case v := <-panicked:
		if v == nil {
			t.Fatal("Run returned without the decoder's panic")
		}
	case <-time.After(timeout):
		t.Fatal("Run did not panic")
	}
	checkStopped(t)
}

// trapConn, once armed, panics on Write.
type trapConn struct {
	net.Conn
	armed *atomic.Bool
}

func (c trapConn) Write(p []byte) (int, error) {
	if c.armed.Load() {
		panic("the writer's bug")
	}
	return c.Conn.Write(p)
}

// TestWriterPanics checks that a panic of the writer, a goroutine of the
// backend's own beyond the session's recover, ends Run with the panic
// rather than crashing occamyd, and stops the reader.
func TestWriterPanics(t *testing.T) {
	checkGoroutines(t)
	srv := newServer(t, serverOptions{})
	b := newBackend(t, Config{Addr: srv.addr})
	var armed atomic.Bool
	b.wrap = func(nc net.Conn) net.Conn { return trapConn{nc, &armed} }
	run := start(t, b, &backendtest.Sink{})
	srv.accept(t)
	armed.Store(true)
	if err := b.Input(backend.Key{Down: true, Keysym: 'a'}); err != nil {
		t.Fatal(err)
	}
	var p backend.PanicError
	if err := run.wait(t); !errors.As(err, &p) || p.Value != "the writer's bug" {
		t.Fatalf("Run = %v, want the writer's panic", err)
	}
}

// TestWriterPanicsLocked checks that a panic of the writer while it holds
// the run's state, as a bug in building a message from the server's
// state would make, leaves the state unlocked: Input, which the session
// calls on its owner goroutine and which waited for the state, returns,
// and Run ends with the panic. Before, Input waited forever, and the
// session with it (from the review of #117).
func TestWriterPanicsLocked(t *testing.T) {
	checkGoroutines(t)
	srv := newServer(t, serverOptions{})
	b := newBackend(t, Config{Addr: srv.addr})
	b.Demand(backend.Demand{Visible: true})
	var armed atomic.Bool
	inDue, proceed := make(chan struct{}), make(chan struct{})
	b.dueHook = func() {
		if armed.CompareAndSwap(true, false) {
			close(inDue)
			<-proceed
			panic("the writer's bug")
		}
	}
	run := start(t, b, &backendtest.Sink{})
	c := srv.accept(t)
	expect[requestMsg](t, c)
	armed.Store(true)
	// A resize wakes the writer, which takes the state in due.
	if err := b.Input(backend.Resize{Width: 32, Height: 32}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-inDue:
	case <-time.After(timeout):
		t.Fatal("the writer did not run due")
	}
	input := make(chan error, 1)
	go func() { input <- b.Input(backend.Pointer{X: 1, Y: 1}) }()
	for deadline := time.Now().Add(timeout); !bytes.Contains(stacks(), []byte("(*run).pointer")); time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("Input did not wait for the state")
		}
	}
	close(proceed)
	var p backend.PanicError
	if err := run.wait(t); !errors.As(err, &p) || p.Value != "the writer's bug" {
		t.Errorf("Run = %v, want the writer's panic", err)
	}
	select {
	case <-input:
	case <-time.After(timeout):
		t.Fatal("Input still waits for the state the writer panicked with")
	}
}

// TestStateUnlockDeferred checks that every section under the run's
// state lock releases it by defer, directly or through run.locked, so
// that no panic leaves it locked: the reader, the writer and Input all
// wait for it.
func TestStateUnlockDeferred(t *testing.T) {
	backendtest.UnlocksDeferred(t, ".", "r.mu")
}

// TestEndPrefersPanic checks that the writer's panic is Run's result
// whatever the reader returned meanwhile, so that the session logs it
// and ends with its internal reason (from the review of #117).
func TestEndPrefersPanic(t *testing.T) {
	p := backend.PanicError{Value: "the writer's bug"}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, tc := range []struct {
		name string
		ctx  context.Context
		err  error
	}{
		{"closed", context.Background(), net.ErrClosed},
		{"ctx done", canceled, net.ErrClosed},
		{"server hung up first, between messages", context.Background(), nil},
		{"protocol error from buffered data", context.Background(), errors.New("vnc: bad message")},
	} {
		if got := end(tc.ctx, tc.err, p); !errors.As(got, new(backend.PanicError)) {
			t.Errorf("%s: end = %v, want the writer's panic", tc.name, got)
		}
	}
	// Without a panic, ctx's error comes first, then the writer's error
	// that closed the connection under the reader.
	werr := errors.New("write: broken pipe")
	if got := end(canceled, net.ErrClosed, werr); !errors.Is(got, context.Canceled) {
		t.Errorf("end after ctx = %v, want ctx's error", got)
	}
	if got := end(context.Background(), net.ErrClosed, werr); !errors.Is(got, werr) {
		t.Errorf("end after a failed write = %v, want the write's error", got)
	}
	if got := end(context.Background(), nil, nil); got != nil {
		t.Errorf("end of a server that hung up = %v, want nil", got)
	}
}
