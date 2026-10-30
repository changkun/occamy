// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package stream

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
	"unicode/utf8"

	"changkun.de/x/occamy/internal/backend"
	"changkun.de/x/occamy/internal/backend/backendtest"
	"changkun.de/x/occamy/internal/pacer"
	"changkun.de/x/occamy/internal/session"
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

// colorful returns output that fills a cols-by-rows screen with one-,
// two- and three-byte characters, each in another colour, so that the
// diff is about 5 bytes a cell.
func colorful(cols, rows int, seed int) []byte {
	var out strings.Builder
	chars := []string{"a", "é", "中"}
	for y := range rows {
		fmt.Fprintf(&out, "\x1b[%d;1H", y+1)
		for x := 0; x < cols; {
			c := chars[(x+y+seed)%3]
			if c == "中" && x == cols-1 {
				c = "a"
			}
			fmt.Fprintf(&out, "\x1b[3%dm%s", (x+seed)%8, c)
			if c == "中" {
				x += 2
			} else {
				x++
			}
		}
	}
	out.WriteString("\x1b[m")
	return []byte(out.String())
}

// A diff larger than the client's messages goes in pieces, each ending at
// a character boundary.
func TestTerminalChunks(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		hello := termHello
		hello.Capabilities.MaxMessageSize = protocol.MinMessageSize
		b := &tapped{Backend: backendtest.NewTerminal(200, 80, 0)}
		r := newRig(t, b, 1e6, time.Millisecond, hello)
		time.Sleep(100 * time.Millisecond)
		r.cl.mu.Lock()
		before := len(r.cl.types)
		r.cl.mu.Unlock()
		b.Sink().TermData(colorful(200, 80, 0))
		time.Sleep(time.Second)
		synctest.Wait()
		r.cl.mu.Lock()
		defer r.cl.mu.Unlock()
		pieces := 0
		for _, ty := range r.cl.types[before:] {
			if ty == protocol.TypeTermData {
				pieces++
			}
		}
		if pieces < 2 {
			t.Errorf("the diff went in %d pieces, want several", pieces)
		}
		if !r.cl.term.Snapshot().Equal(r.s.Screen()) {
			t.Errorf("client screen\n%s\nserver\n%s", r.cl.term.Snapshot(), r.s.Screen())
		}
	})
}

// termChunk ends each piece at a character boundary, whatever the limit,
// and finds no room at a limit below the header.
func TestTermChunk(t *testing.T) {
	data := []byte(strings.Repeat("a中é", 100))
	w := &writer{}
	for limit := termHeader + 1; limit <= termHeader+12; limit++ {
		w.limits.MaxMessageSize = limit
		if n := w.termChunk(data); n <= 0 || n > limit-termHeader || !utf8.Valid(data[:n]) {
			t.Errorf("at a limit of %d, a piece of %d bytes", limit, n)
		}
	}
	w.limits.MaxMessageSize = termHeader - 4
	if n := w.termChunk(data); n != 0 {
		t.Errorf("at a limit below the header, a piece of %d bytes", n)
	}
}

// writes counts a writer's messages and fails once there are too many.
type writes struct {
	mu  sync.Mutex
	n   int
	max int
}

func (c *writes) ReadMessage(ctx context.Context) ([]byte, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (c *writes) WriteMessage(context.Context, []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.n++; c.n > c.max {
		return errors.New("too many writes")
	}
	return nil
}

// A message size with no room for a character ends the frame with an
// error, not with a panic or empty messages without end. The handshake
// refuses such a size; this guards a server configured below it.
func TestTerminalTinyMessages(t *testing.T) {
	for _, limit := range []int{4, termHeader, termHeader + 1} {
		synctest.Test(t, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			s, owner, err := session.Start(ctx, session.Config{Backend: backendtest.NewTerminal(80, 24, 0)})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if err := owner.Input(1, backend.TermInput{Data: []byte("é")}); err != nil {
				t.Fatal(err)
			}
			synctest.Wait()
			c := &writes{max: 100}
			w := newWriter(c, owner, pacer.New(pacer.Config{}), nil, protocol.Limits{MaxMessageSize: limit}, slog.Default())
			w.p.StartFrame(time.Now())
			err = w.sendScreen(ctx, nil, s.Screen(), 0)
			if err == nil || c.n > c.max {
				t.Errorf("limit %d: sendScreen returned %v after %d writes", limit, err, c.n)
			}
		})
	}
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

// A resize-request is an input whose effect is a screen of another size:
// the rows in motion at the input, of the old size, say nothing about the
// new rows.
func TestTerminalResizeRequest(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := &tapped{Backend: backendtest.NewTerminal(80, 24, 0)}
		r := newRig(t, b, 1e6, 10*time.Millisecond, termHello)
		time.Sleep(100 * time.Millisecond)
		r.cl.send(t, protocol.ResizeRequest{Seq: 1, Width: 120, Height: 40})
		time.Sleep(100 * time.Millisecond)
		r.cl.send(t, protocol.TermInput{Seq: 2, Data: []byte("after")})
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

// clockEcho is a terminal backend that redraws a clock on the last row
// every tick, as a status line does, and echoes input after echo, as a
// remote shell does.
type clockEcho struct {
	*backendtest.Terminal
	tick, echo time.Duration
	in         chan []byte
}

func (b *clockEcho) Input(ev backend.InputEvent) error {
	if ti, ok := ev.(backend.TermInput); ok {
		b.in <- ti.Data
	}
	return nil
}

func (b *clockEcho) Run(ctx context.Context, sink backend.Sink) error {
	sink.Resize(80, 24)
	sink.TermData([]byte("$ "))
	tk := time.NewTicker(b.tick)
	defer tk.Stop()
	echoes := make(chan []byte, 16)
	for n := 0; ; {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case p := <-b.in:
			time.AfterFunc(b.echo, func() { echoes <- p })
		case p := <-echoes:
			sink.TermData(p)
		case <-tk.C:
			n++
			sink.TermData(fmt.Appendf(nil, "\x1b7\x1b[24;1Hclock %d\x1b8", n))
		}
	}
}

// A status line that changes all the time does not take an input's
// priority: the frame beyond the window shows the echo, which arrives a
// round trip after the input, and no frame reports the input without it.
func TestTerminalInputAmidOutput(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const delay = 20 * time.Millisecond
		b := &clockEcho{Terminal: backendtest.NewTerminal(80, 24, 0), tick: 5 * time.Millisecond, echo: 30 * time.Millisecond, in: make(chan []byte, 16)}
		r := newRig(t, b, 1e6, delay, termHello)
		time.Sleep(200 * time.Millisecond)
		r.cl.mu.Lock()
		r.cl.noAcks = true
		r.cl.mu.Unlock()
		time.Sleep(200 * time.Millisecond) // the window fills
		r.cl.mu.Lock()
		var without, arrived bool
		r.cl.onFrame = func(f frameRecord) {
			has := strings.Contains(r.cl.term.Snapshot().Line(0), "TYPED")
			if f.lastSeq >= 1 && !has {
				without = true
			}
			arrived = arrived || has
		}
		r.cl.mu.Unlock()
		r.cl.send(t, protocol.TermInput{Seq: 1, Data: []byte("TYPED")})
		time.Sleep(time.Second)
		r.cl.mu.Lock()
		defer r.cl.mu.Unlock()
		if without {
			t.Error("a frame reported the input without its echo")
		}
		if !arrived {
			t.Error("the echo never arrived: the frame beyond the window went to the clock")
		}
	})
}

// A screen redrawn the same costs no frame.
func TestTerminalSameRedraw(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := &tapped{Backend: backendtest.NewTerminal(80, 24, 0)}
		r := newRig(t, b, 1e6, time.Millisecond, termHello)
		time.Sleep(100 * time.Millisecond)
		b.Sink().TermData([]byte("abc\r"))
		time.Sleep(100 * time.Millisecond)
		r.cl.mu.Lock()
		before := len(r.cl.frames)
		r.cl.mu.Unlock()
		for range 10 {
			b.Sink().TermData([]byte("abc\r"))
			time.Sleep(10 * time.Millisecond)
		}
		time.Sleep(100 * time.Millisecond)
		r.cl.mu.Lock()
		defer r.cl.mu.Unlock()
		if n := len(r.cl.frames) - before; n != 0 {
			t.Errorf("%d frames for redraws that changed nothing", n)
		}
	})
}

// A diff that would take the bytes in flight past the cap waits for
// earlier frames to be acknowledged, instead of going out at once.
func TestTerminalCapHolds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		hello := termHello
		hello.Capabilities.MaxMessageSize = protocol.MinMessageSize
		b := &tapped{Backend: backendtest.NewTerminal(300, 120, 0)}
		r := newRig(t, b, 50e6, time.Millisecond, hello)
		time.Sleep(100 * time.Millisecond)
		r.cl.mu.Lock()
		r.cl.noAcks = true
		r.cl.mu.Unlock()
		// About 60 KB in flight, never acknowledged.
		b.Sink().TermData(colorful(300, 40, 1))
		time.Sleep(100 * time.Millisecond)
		r.cl.mu.Lock()
		frames, types := len(r.cl.frames), len(r.cl.types)
		r.cl.mu.Unlock()
		// About 180 KB more in 64 KiB pieces, past the 128 KiB cap: it
		// starts, since the first frame is under the cap, but must not
		// finish.
		b.Sink().TermData(colorful(300, 120, 2))
		time.Sleep(time.Second)
		r.cl.mu.Lock()
		defer r.cl.mu.Unlock()
		if !slices.Contains(r.cl.types[types:], protocol.TypeTermData) {
			t.Fatal("the second diff never started, so the test shows nothing")
		}
		if n := len(r.cl.frames) - frames; n != 0 {
			t.Errorf("%d frames ended past the cap with no acks", n)
		}
	})
}

// A terminal client gets no pointer, and the title once.
func TestTerminalSmallState(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRig(t, backendtest.NewTerminal(80, 24, 0), 1e6, time.Millisecond, termHello)
		time.Sleep(100 * time.Millisecond)
		synctest.Wait()
		r.cl.mu.Lock()
		defer r.cl.mu.Unlock()
		titles := 0
		for _, ty := range r.cl.types {
			switch ty {
			case protocol.TypeCursorMove:
				t.Errorf("a terminal client got cursor-move: %v", r.cl.types)
			case protocol.TypeTitle:
				titles++
			}
		}
		if titles != 1 {
			t.Errorf("%d titles, want 1: %v", titles, r.cl.types)
		}
	})
}

var _ backend.Backend = (*tapped)(nil)
