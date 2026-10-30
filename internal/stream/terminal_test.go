// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package stream

import (
	"fmt"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"changkun.de/x/occamy/internal/backend"
	"changkun.de/x/occamy/internal/backend/backendtest"
	"changkun.de/x/occamy/protocol"
)

var termHello = protocol.Hello{Visible: true, Capabilities: protocol.Capabilities{Terminal: 1}}

// The client's terminal ends up as the server's, from one term-snapshot
// and diffs, however much the shell printed.
func TestTerminalConverges(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := &tapped{Backend: backendtest.NewTerminal(80, 24, 0)}
		r := newRig(t, b, 1e6, 10*time.Millisecond, termHello)
		time.Sleep(100 * time.Millisecond)
		sink := b.Sink()
		var out strings.Builder
		for i := range 5000 {
			fmt.Fprintf(&out, "\x1b[3%dmline %d\x1b[m\r\n", i%8, i)
		}
		sink.TermData([]byte(out.String()))
		sink.TermData([]byte("\x1b]2;shell\x07$ "))
		time.Sleep(time.Second)
		synctest.Wait()
		r.cl.mu.Lock()
		defer r.cl.mu.Unlock()
		if r.cl.welcome == nil || r.cl.welcome.Class != protocol.ClassTerminal {
			t.Fatalf("welcome %+v, want a terminal", r.cl.welcome)
		}
		if r.cl.term == nil {
			t.Fatal("no term-snapshot")
		}
		if want, got := r.s.Screen(), r.cl.term.Snapshot(); !got.Equal(want) {
			t.Errorf("client screen\n%s\nserver screen\n%s", got, want)
		}
		if r.cl.snapshots != 1 {
			t.Errorf("%d term-snapshots, want 1", r.cl.snapshots)
		}
		// 5000 lines at once are a handful of frames, not one per line.
		if n := len(r.cl.frames); n > 10 {
			t.Errorf("%d frames for one burst of output", n)
		}
		if r.cl.titled("shell") != 1 {
			t.Errorf("title not sent once: %v", r.cl.titles)
		}
	})
}

// A diff larger than the client's messages goes in pieces.
func TestTerminalChunks(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		hello := termHello
		hello.Capabilities.MaxMessageSize = 512
		b := &tapped{Backend: backendtest.NewTerminal(80, 24, 0)}
		r := newRig(t, b, 1e6, time.Millisecond, hello)
		time.Sleep(100 * time.Millisecond)
		var out strings.Builder
		for i := range 24 {
			fmt.Fprintf(&out, "\x1b[3%dm%s\x1b[m\r\n", i%8, strings.Repeat("é", 70))
		}
		b.Sink().TermData([]byte(out.String()))
		time.Sleep(100 * time.Millisecond)
		synctest.Wait()
		r.cl.mu.Lock()
		defer r.cl.mu.Unlock()
		if !r.cl.term.Snapshot().Equal(r.s.Screen()) {
			t.Errorf("client screen\n%s\nserver\n%s", r.cl.term.Snapshot(), r.s.Screen())
		}
	})
}

// The screen follows the backend's resizes, resize first.
func TestTerminalResize(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := &tapped{Backend: backendtest.NewTerminal(80, 24, 0)}
		r := newRig(t, b, 1e6, 10*time.Millisecond, termHello)
		time.Sleep(100 * time.Millisecond)
		b.Sink().TermData([]byte("before"))
		time.Sleep(100 * time.Millisecond)
		b.Sink().Resize(120, 40)
		b.Sink().TermData([]byte("\r\nafter"))
		time.Sleep(100 * time.Millisecond)
		synctest.Wait()
		r.cl.mu.Lock()
		defer r.cl.mu.Unlock()
		got := r.cl.term.Snapshot()
		if got.Cols != 120 || got.Rows != 40 || !got.Equal(r.s.Screen()) {
			t.Errorf("client screen %dx%d\n%s\nserver\n%s", got.Cols, got.Rows, got, r.s.Screen())
		}
	})
}

// An input's echo goes out at once, beyond a full frame window, and its
// frame reports the input.
func TestTerminalInput(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const delay = 20 * time.Millisecond
		b := &tapped{Backend: backendtest.NewTerminal(80, 24, 0)}
		r := newRig(t, b, 1e6, delay, termHello)
		time.Sleep(100 * time.Millisecond)
		// The client stops acking, and output fills the frame window.
		r.cl.mu.Lock()
		r.cl.noAcks = true
		r.cl.mu.Unlock()
		for i := range 5 {
			b.Sink().TermData(fmt.Appendf(nil, "output %d\r\n", i))
			time.Sleep(50 * time.Millisecond)
		}
		r.cl.mu.Lock()
		before := len(r.cl.frames)
		var arrived time.Duration
		var lastSeq uint32
		r.cl.onFrame = func(f frameRecord) {
			if arrived == 0 && strings.Contains(r.cl.term.Snapshot().String(), "typed") {
				arrived, lastSeq = f.at, f.lastSeq
			}
		}
		r.cl.mu.Unlock()
		pressed := time.Since(r.cl.start)
		r.cl.send(t, protocol.TermInput{Seq: 1, Data: []byte("typed")})
		time.Sleep(time.Second)
		r.cl.mu.Lock()
		defer r.cl.mu.Unlock()
		if arrived == 0 {
			t.Fatalf("the echo never arrived; %d frames before the input, %d after", before, len(r.cl.frames))
		}
		if lag := arrived - pressed; lag > 2*delay+5*time.Millisecond {
			t.Errorf("echo after %v, want about one round trip", lag)
		}
		if lastSeq != 1 {
			t.Errorf("the echo's frame reports last_seq %d, want 1", lastSeq)
		}
	})
}

// A client that renders no terminal is refused a terminal session.
func TestTerminalUnsupported(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRig(t, backendtest.NewTerminal(80, 24, 0), 1e6, time.Millisecond, visible)
		if err := <-r.served; err != nil {
			t.Errorf("Serve returned %v", err)
		}
		r.served <- nil
		time.Sleep(10 * time.Millisecond)
		r.cl.mu.Lock()
		defer r.cl.mu.Unlock()
		if r.cl.close == nil || r.cl.close.Code != protocol.CloseUnsupported {
			t.Errorf("close %+v, want unsupported", r.cl.close)
		}
	})
}

// A hidden terminal client gets no frames, and the screen as it is now
// when it shows again.
func TestTerminalHidden(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := &tapped{Backend: backendtest.NewTerminal(80, 24, 0)}
		r := newRig(t, b, 1e6, time.Millisecond, termHello)
		time.Sleep(100 * time.Millisecond)
		r.cl.send(t, protocol.View{})
		time.Sleep(10 * time.Millisecond)
		r.cl.mu.Lock()
		before := len(r.cl.frames)
		r.cl.mu.Unlock()
		for i := range 100 {
			b.Sink().TermData(fmt.Appendf(nil, "hidden %d\r\n", i))
			time.Sleep(time.Millisecond)
		}
		time.Sleep(100 * time.Millisecond)
		r.cl.mu.Lock()
		hidden := len(r.cl.frames) - before
		r.cl.mu.Unlock()
		if hidden != 0 {
			t.Errorf("%d frames while hidden", hidden)
		}
		r.cl.send(t, protocol.View{Visible: true})
		time.Sleep(100 * time.Millisecond)
		r.cl.mu.Lock()
		defer r.cl.mu.Unlock()
		if n := len(r.cl.frames) - before; n != 1 {
			t.Errorf("%d frames after showing, want 1", n)
		}
		if !r.cl.term.Snapshot().Equal(r.s.Screen()) {
			t.Errorf("client screen differs after showing")
		}
	})
}

var _ backend.Backend = (*tapped)(nil)
