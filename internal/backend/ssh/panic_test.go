// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package ssh_test

import (
	"bytes"
	"errors"
	"testing"

	"changkun.de/x/occamy/internal/backend"
	"changkun.de/x/occamy/internal/backend/backendtest"
	"changkun.de/x/occamy/internal/backend/ssh"
)

// panicSink is a sink whose terminal has a bug: output with a ^A in it,
// or a resize after the first, makes it panic.
type panicSink struct{ *backendtest.Sink }

func (s panicSink) TermData(p []byte) {
	if bytes.IndexByte(p, 1) >= 0 {
		panic("the emulator's bug")
	}
	s.Sink.TermData(p)
}

func (s panicSink) Resize(cols, rows int) {
	if s.Resizes() > 0 {
		panic("the resize bug")
	}
	s.Sink.Resize(cols, rows)
}

// TestGoroutinePanics checks that a panic on one of the goroutines Run
// starts, as the terminal emulator's on output that anyone with a shell
// can print, or on a resize, ends Run with the panic instead of crashing
// occamyd, and that every goroutine Run started stops.
func TestGoroutinePanics(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input backend.InputEvent
		value any
	}{
		{"output", backend.TermInput{Data: []byte("\x01")}, "the emulator's bug"},
		{"resize", backend.Resize{Width: 100, Height: 30}, "the resize bug"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checkGoroutines(t)
			srv := newServer(t, serverOptions{password: "pw"})
			b := newBackend(t, ssh.Config{Addr: srv.addr, User: "alice", Password: "pw", HostKeys: pin(srv)})
			sink := panicSink{&backendtest.Sink{}}
			r := start(t, b, sink)
			waitShell(t, sink.Sink)
			if err := b.Input(tc.input); err != nil {
				t.Fatal(err)
			}
			err := r.wait(t)
			var p backend.PanicError
			if !errors.As(err, &p) || p.Value != tc.value || len(p.Stack) == 0 {
				t.Errorf("Run = %v, want the panic %q", err, tc.value)
			}
		})
	}
}

// TestLastOutputPanics checks that a panic on the shell's last output,
// which the readers pass on after the shell has exited, still ends Run
// with the panic, not as a shell that exited normally.
func TestLastOutputPanics(t *testing.T) {
	for range 20 {
		checkGoroutines(t)
		srv := newServer(t, serverOptions{password: "pw"})
		b := newBackend(t, ssh.Config{Addr: srv.addr, User: "alice", Password: "pw", HostKeys: pin(srv)})
		sink := panicSink{&backendtest.Sink{}}
		r := start(t, b, sink)
		waitShell(t, sink.Sink)
		srv.end(ending{output: "bye\x01", status: 0})
		var p backend.PanicError
		if err := r.wait(t); !errors.As(err, &p) {
			t.Fatalf("Run = %v, want the panic on the last output", err)
		}
	}
}
