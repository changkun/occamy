// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package guacd_test

import (
	"context"
	"errors"
	"image"
	"net"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"changkun.de/x/occamy/internal/backend"
	"changkun.de/x/occamy/internal/backend/backendtest"
)

// trapSink, once armed, returns images without pixels from Lock, so that
// drawing into one panics while the lock is held, as a bug that indexes
// past its data would.
type trapSink struct {
	backend.Sink
	armed *atomic.Bool
}

func (s trapSink) Lock(r image.Rectangle) *image.RGBA {
	img := s.Sink.Lock(r)
	if s.armed.Load() {
		return &image.RGBA{Rect: r}
	}
	return img
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

// inStack reports whether a goroutine runs fn.
func inStack(fn string) bool {
	buf := make([]byte, 1<<20)
	return strings.Contains(string(buf[:runtime.Stack(buf, true)]), fn)
}

// TestPanicWhileDrawing checks that a panic while the bridge copies a
// frame into the sink, which the session recovers from, releases the
// pixels it locked, and that the writer stops and the connection closes
// with it.
func TestPanicWhileDrawing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sink := &backendtest.Sink{}
		var armed atomic.Bool
		h := startWith(t, sink, func(s backend.Sink) backend.Sink { return trapSink{s, &armed} }, nil)
		h.send("size", "0", "4", "4")
		h.frame(100)
		armed.Store(true)
		h.send("rect", "0", "0", "0", "2", "2")
		h.send("cfill", "14", "0", "0", "255", "0", "255")
		h.send("sync", "200")
		var p backend.PanicError
		if err := h.wait(); !errors.As(err, &p) {
			t.Fatalf("Run = %v, want a panic", err)
		}
		if r := sink.Locked(); !r.Empty() {
			t.Errorf("the panic left %v locked", r)
		}
		synctest.Wait()
		if inStack("(*session).writeLoop") {
			t.Error("the writer outlived the reader's panic")
		}
	})
}

// TestWriterPanics checks that a panic of the writer, a goroutine of the
// bridge's own beyond the session's recover, ends Run with the panic
// rather than crashing occamyd, and stops the reader.
func TestWriterPanics(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var armed atomic.Bool
		h := startWith(t, &backendtest.Sink{}, nil, func(c net.Conn) net.Conn { return trapConn{c, &armed} })
		armed.Store(true)
		if err := h.b.Input(backend.Key{Down: true, Keysym: 'a'}); err != nil {
			t.Fatal(err)
		}
		var p backend.PanicError
		if err := h.wait(); !errors.As(err, &p) || p.Value != "the writer's bug" {
			t.Fatalf("Run = %v, want the writer's panic", err)
		}
		synctest.Wait()
		if inStack("(*session).readLoop") {
			t.Error("the reader outlived the writer's panic")
		}
	})
}

// unanswered is a sink whose owner never answers a prompt: Prompt
// returns only when its context ends.
type unanswered struct {
	backend.Sink
	prompted chan struct{}
}

func (s unanswered) Prompt(ctx context.Context, _ backend.Prompt) (backend.PromptReply, error) {
	close(s.prompted)
	<-ctx.Done()
	return backend.PromptReply{}, ctx.Err()
}

// TestWriterPanicsDuringPrompt checks that a panic of the writer while
// the reader waits for the owner's answer to a prompt ends Run at once
// with the panic. Before, Run waited for the answer, which may never
// come (from the review of #117).
func TestWriterPanicsDuringPrompt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var armed atomic.Bool
		prompted := make(chan struct{})
		h := startWith(t, &backendtest.Sink{},
			func(s backend.Sink) backend.Sink { return unanswered{s, prompted} },
			func(c net.Conn) net.Conn { return trapConn{c, &armed} })
		h.send("required", "password")
		<-prompted
		armed.Store(true)
		if err := h.b.Input(backend.Key{Down: true, Keysym: 'a'}); err != nil {
			t.Fatal(err)
		}
		var p backend.PanicError
		if err := h.wait(); !errors.As(err, &p) || p.Value != "the writer's bug" {
			t.Fatalf("Run = %v, want the writer's panic", err)
		}
	})
}
