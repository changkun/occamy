// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package kubernetes_test

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"changkun.de/x/occamy/internal/backend"
	"changkun.de/x/occamy/internal/backend/backendtest"
	"changkun.de/x/occamy/internal/backend/kubernetes"
	"changkun.de/x/occamy/internal/backend/kubernetes/kubetest"
)

// panicSink is a sink whose terminal has a bug: output with a ^A in it,
// or a resize after the first, or with first set every resize, makes it
// panic.
type panicSink struct {
	*backendtest.Sink
	first bool
}

func (s panicSink) TermData(p []byte) {
	if bytes.IndexByte(p, 1) >= 0 {
		panic("the emulator's bug")
	}
	s.Sink.TermData(p)
}

func (s panicSink) Resize(cols, rows int) {
	if s.first || s.Resizes() > 0 {
		panic("the resize bug")
	}
	s.Sink.Resize(cols, rows)
}

// TestGoroutinePanics checks that a panic on one of the goroutines Run
// starts, as the terminal emulator's on output that anyone with a shell
// in the container can print, or on a resize, ends Run with the panic
// instead of crashing occamyd. The first resize is made on Run's own
// goroutine, before the others start, so its panic unwinds Run, for the
// session to recover. Either way the stream is closed, so that the
// command does not run on, and every goroutine Run started stops.
func TestGoroutinePanics(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input backend.InputEvent
		first bool
		value any
	}{
		{"output", nil, false, "the emulator's bug"},
		{"resize", backend.Resize{Width: 100, Height: 30}, false, "the resize bug"},
		{"first resize", nil, true, "the resize bug"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checkGoroutines(t)
			s := newServer(t, kubetest.Options{})
			b := newBackend(t, config(s))
			sink := panicSink{&backendtest.Sink{}, tc.first}
			// ended gets Run's error, or what it panicked with.
			ended := make(chan any, 1)
			go func() {
				defer func() {
					if v := recover(); v != nil {
						ended <- v
					}
				}()
				ended <- b.Run(context.Background(), sink)
			}()
			e := accept(t, s)
			if !tc.first {
				eventually(t, "the size", func() bool { return sink.Resizes() == 1 })
				if tc.input != nil {
					input(t, b, tc.input)
				} else if err := e.Stdout([]byte("\x01")); err != nil {
					t.Fatal(err)
				}
			}
			var got any
			select {
			case got = <-ended:
			case <-time.After(timeout):
				t.Fatal("Run did not return")
			}
			if tc.first {
				if got != tc.value {
					t.Errorf("Run panicked with %v, want %q", got, tc.value)
				}
			} else {
				err, _ := got.(error)
				var p backend.PanicError
				if !errors.As(err, &p) || p.Value != tc.value || len(p.Stack) == 0 {
					t.Errorf("Run = %v, want the panic %q", got, tc.value)
				}
			}
			select {
			case <-e.Done():
			case <-time.After(timeout):
				t.Error("the stream is still open after Run ended")
			}
			checkStopped(t)
		})
	}
}

// TestLastOutputPanics checks that a panic on the command's last output,
// just before its status, still ends Run with the panic, not as a
// command that exited normally.
func TestLastOutputPanics(t *testing.T) {
	for range 20 {
		checkGoroutines(t)
		s := newServer(t, kubetest.Options{})
		b := newBackend(t, config(s))
		sink := panicSink{Sink: &backendtest.Sink{}}
		r := start(t, b, sink)
		e := accept(t, s)
		_ = e.Stdout([]byte("bye\x01"))
		_ = e.Exit(0)
		var p backend.PanicError
		if err := r.wait(t); !errors.As(err, &p) {
			t.Fatalf("Run = %v, want the panic on the last output", err)
		}
	}
}

var _ backend.Backend = (*kubernetes.Backend)(nil)
