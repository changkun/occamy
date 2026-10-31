// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package rdp

import (
	"context"
	"errors"
	"io"
	"math"
	"net"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"changkun.de/x/occamy/internal/backend"
	"changkun.de/x/occamy/internal/backend/backendtest"
	"changkun.de/x/occamy/internal/rdp"
	"changkun.de/x/occamy/internal/rdp/rdptest"
)

// The writer's tests: how it batches input, and how a write that fails
// decides Run's result.

// fakeClient records what the writer writes, as an rdp.Client.
type fakeClient struct {
	unicode bool
	// fail, if set, is what a write returns. If called is set, the first
	// write closes it, and if release is set, writes wait for it to be
	// closed.
	fail    error
	called  chan struct{}
	release chan struct{}
	once    sync.Once

	mu       sync.Mutex
	writes   [][]rdp.InputEvent
	caps     []int
	suppress []bool
	events   int
}

func (f *fakeClient) SendInput(evs ...rdp.InputEvent) error {
	if f.called != nil {
		f.once.Do(func() { close(f.called) })
	}
	if f.release != nil {
		<-f.release
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes = append(f.writes, slices.Clone(evs))
	f.caps = append(f.caps, cap(evs))
	f.events += len(evs)
	return f.fail
}

func (f *fakeClient) SuppressOutput(suppress bool) error {
	if f.release != nil {
		<-f.release
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.suppress = append(f.suppress, suppress)
	return f.fail
}

func (f *fakeClient) UnicodeInput() bool { return f.unicode }
func (f *fakeClient) MouseXInput() bool  { return false }

func (f *fakeClient) written() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.events
}

// writer runs a writer on c until stop is called.
type writer struct {
	r       *run
	done    chan struct{}
	visible atomic.Bool
	result  chan error
}

func startWriter(r *run, c client, visible bool) *writer {
	w := &writer{r: r, done: make(chan struct{}), result: make(chan error, 1)}
	w.visible.Store(visible)
	go func() { w.result <- r.writeLoop(c, w.done, &w.visible) }()
	return w
}

func (w *writer) wait(t *testing.T) error {
	t.Helper()
	select {
	case err := <-w.result:
		return err
	case <-time.After(timeout):
		t.Fatal("the writer did not return")
	}
	return nil
}

func (w *writer) stop(t *testing.T) error {
	t.Helper()
	close(w.done)
	return w.wait(t)
}

// The writer holds and writes at most maxBatch events at once: inputs
// whole, as many as are ready, and a Text a part at a time, in order.
func TestWriterBatches(t *testing.T) {
	checkGoroutines(t)
	r := newRun()
	typed := backend.Text{Text: strings.Repeat("é", 3000)} // 6,000 Unicode events
	inputs := []backend.InputEvent{typed, backend.Key{Down: true, Code: 0x04}}
	for range 200 {
		inputs = append(inputs, backend.Wheel{DX: 1 << 20, DY: 1 << 20}) // 64 events each
	}
	var want []rdp.InputEvent
	var s inputState
	for _, ev := range inputs {
		n, err := cost(ev)
		if err != nil {
			t.Fatal(err)
		}
		if err := r.send(ev, n); err != nil {
			t.Fatal(err)
		}
		if ev, ok := ev.(backend.Text); ok {
			want, _ = text(want, capabilities{unicode: true}, ev.Text, math.MaxInt)
			continue
		}
		want = s.events(want, capabilities{unicode: true}, ev)
	}
	c := &fakeClient{unicode: true}
	w := startWriter(r, c, true)
	waitFor(t, "every event written", func() bool { return c.written() == len(want) })
	if err := w.stop(t); err != nil {
		t.Fatal(err)
	}
	var got []rdp.InputEvent
	for i, evs := range c.writes {
		if len(evs) > maxBatch || c.caps[i] != maxBatch {
			t.Errorf("write %d: %d events in a batch of %d, want at most %d in one of %d", i, len(evs), c.caps[i], maxBatch, maxBatch)
		}
		got = append(got, evs...)
	}
	if strings.Join(keys(t, got), " ") != strings.Join(keys(t, want), " ") {
		t.Errorf("the writes do not make the inputs' events in order")
	}
	// Everything was ready at once, so the writes are nearly full: six
	// of the text, then the key and the wheels sixteen to a write.
	if len(c.writes) > 20 {
		t.Errorf("%d writes of %d events, want them nearly full", len(c.writes), len(want))
	}
	if r.queued.Load() != 0 {
		t.Errorf("%d events left in the budget", r.queued.Load())
	}
}

// A write that fails stops the writer with the error, unless the
// connection was closed under it or Run is done: then the reader's
// result is Run's.
func TestWriterFails(t *testing.T) {
	checkGoroutines(t)
	broken := errors.New("broken")
	closed := &net.OpError{Op: "write", Net: "tcp", Err: net.ErrClosed}
	for _, tc := range []struct {
		name    string
		fail    error
		visible bool // false: the write is Suppress Output
		want    error
	}{
		{"input", broken, true, broken},
		{"Suppress Output", broken, false, broken},
		{"closed under it", closed, true, nil},
		{"Suppress Output closed under it", closed, false, nil},
	} {
		r := newRun()
		c := &fakeClient{fail: tc.fail}
		if tc.visible {
			r.send(backend.Key{Down: true, Code: 0x04}, keyEvents)
		}
		w := startWriter(r, c, tc.visible)
		err := w.wait(t)
		if tc.want == nil && err != nil || tc.want != nil && (!errors.Is(err, tc.want) || !strings.HasPrefix(err.Error(), "rdp: write: ")) {
			t.Errorf("%s: the writer returned %v, want %v", tc.name, err, tc.want)
		}
	}

	// A write that fails once Run is done, as it closes the connection,
	// is not the writer's failure, whatever the error.
	r := newRun()
	c := &fakeClient{fail: broken, called: make(chan struct{}), release: make(chan struct{})}
	r.send(backend.Key{Down: true, Code: 0x04}, keyEvents)
	w := startWriter(r, c, true)
	<-c.called
	close(w.done)
	close(c.release)
	if err := w.wait(t); err != nil {
		t.Errorf("a write that failed after Run was done: %v, want nil", err)
	}
}

// Input that does not fit the queue leaves the budget as it was.
func TestSendFull(t *testing.T) {
	r := newRun()
	for i := range inputQueue {
		if err := r.send(backend.Key{}, keyEvents); err != nil {
			t.Fatalf("input %d: %v", i, err)
		}
	}
	if err := r.send(backend.Key{}, keyEvents); !errors.Is(err, ErrInputFull) || r.queued.Load() != inputQueue*keyEvents {
		t.Errorf("a full queue: %v, %d events in the budget, want ErrInputFull and %d", err, r.queued.Load(), inputQueue*keyEvents)
	}
	if err := r.send(backend.Text{}, inputEvents); !errors.Is(err, ErrInputFull) || r.queued.Load() != inputQueue*keyEvents {
		t.Errorf("over the budget: %v, %d events in the budget", err, r.queued.Load())
	}
}

// end decides Run's result from the reader's and the writer's.
func TestEnd(t *testing.T) {
	b := newBackend(t, Config{Addr: "rdp.test", TLS: rdp.TLS{InsecureSkipVerify: true}, Logger: quiet})
	bg := context.Background()
	cancelled, cancel := context.WithCancel(bg)
	cancel()
	closed := &net.OpError{Op: "read", Net: "tcp", Err: net.ErrClosed}
	werr := errors.New("rdp: write: broken")
	for _, tc := range []struct {
		name      string
		ctx       context.Context
		err, werr error
		want      error // nil: end returns nil
	}{
		{"cancelled", cancelled, nil, nil, context.Canceled},
		{"cancelled as the server ended", cancelled, rdp.ErrorInfo(0x1), werr, context.Canceled},
		{"the failed write closed the reader", bg, closed, werr, werr},
		{"the server ended as a write failed", bg, nil, werr, nil},
		{"an end by an administrator as a write failed", bg, rdp.ErrorInfo(0x1), werr, nil},
		{"a failure as a write failed", bg, rdp.ErrorInfo(0x9), werr, rdp.ErrorInfo(0x9)},
		{"a hang-up as a write failed", bg, io.EOF, werr, ErrConnectionClosed},
		{"a session replaced", bg, rdp.ErrorInfo(0x5), nil, nil},
		{"out of memory", bg, rdp.ErrorInfo(0x6), nil, rdp.ErrorInfo(0x6)},
		{"denied", bg, rdp.ErrorInfo(0x7), nil, rdp.ErrorInfo(0x7)},
	} {
		err := b.end(tc.ctx, tc.err, tc.werr)
		if tc.want == nil && err != nil || tc.want != nil && !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, err, tc.want)
		}
	}
}

// fillUntilStalled queues input until the writer is blocked on a server
// that does not read: Input keeps failing with ErrInputFull.
func fillUntilStalled(t *testing.T, b *Backend) {
	t.Helper()
	text := backend.Text{Text: strings.Repeat("A", 1<<14)} // four events a character without Unicode
	var full time.Time
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); {
		switch err := b.Input(text); {
		case err == nil:
			full = time.Time{}
		case errors.Is(err, ErrInputFull):
			if full.IsZero() {
				full = time.Now()
			}
			if time.Since(full) > 3*silence {
				return
			}
			time.Sleep(5 * time.Millisecond)
		default:
			t.Fatal(err)
		}
	}
	t.Fatal("the writer never stalled")
}

// The server ends the session while the writer is blocked writing input
// it does not read: Run's result is the server's reason, not the write
// that Run's own close failed.
func TestRunEndWhileWriterStalled(t *testing.T) {
	for _, tc := range []struct {
		code uint32
		want error // nil: Run returns nil
	}{
		{0x1, nil},
		{0x9, rdp.ErrorInfo(0x9)},
	} {
		t.Run(rdp.ErrorInfo(tc.code).Error(), func(t *testing.T) {
			checkGoroutines(t)
			s := listen(t, rdptest.Server{InputFlags: rdptest.InputScancodes})
			b := newBackend(t, s.config(t))
			b.Demand(backend.Demand{Visible: true})
			r := start(t, b, &backendtest.Sink{})
			c := s.accept(t)
			fillUntilStalled(t, b)
			if err := c.Write(rdptest.SetErrorInfo(tc.code), rdptest.DisconnectUltimatum()); err != nil {
				t.Fatal(err)
			}
			if err := r.wait(t); tc.want == nil && err != nil || tc.want != nil && !errors.Is(err, tc.want) {
				t.Errorf("Run returned %v, want %v", err, tc.want)
			}
		})
	}
}

// Cancelling while the writer is blocked on a server that does not read
// ends Run at once.
func TestRunCancelWhileWriterStalled(t *testing.T) {
	checkGoroutines(t)
	s := listen(t, rdptest.Server{InputFlags: rdptest.InputScancodes})
	b := newBackend(t, s.config(t))
	b.Demand(backend.Demand{Visible: true})
	r := start(t, b, &backendtest.Sink{})
	s.accept(t)
	fillUntilStalled(t, b)
	begin := time.Now()
	if err := r.stop(t); !errors.Is(err, context.Canceled) || time.Since(begin) > time.Second {
		t.Errorf("Run returned %v after %v", err, time.Since(begin))
	}
}

// failConn is a connection whose writes fail once armed, while its reads
// go on.
type failConn struct {
	net.Conn
	armed  atomic.Bool
	once   sync.Once
	failed chan struct{} // closed at the first write that fails
}

var errInjected = errors.New("injected write failure")

func (c *failConn) Write(p []byte) (int, error) {
	if c.armed.Load() {
		c.once.Do(func() { close(c.failed) })
		return 0, errInjected
	}
	return c.Conn.Write(p)
}

// failing returns a backend for s whose connection's writes fail once
// armed.
func failing(t *testing.T, s *fakeServer) (*Backend, chan *failConn) {
	t.Helper()
	b := newBackend(t, s.config(t))
	conns := make(chan *failConn, 1)
	b.dial = func(ctx context.Context) (net.Conn, error) {
		var d net.Dialer
		nc, err := d.DialContext(ctx, "tcp", b.addr)
		if err != nil {
			return nil, err
		}
		fc := &failConn{Conn: nc, failed: make(chan struct{})}
		conns <- fc
		return fc, nil
	}
	return b, conns
}

// A write that fails on its own stops Run with its error, once the
// reader has had its grace to end on its own.
func TestRunWriteFails(t *testing.T) {
	checkGoroutines(t)
	s := listen(t, rdptest.Server{})
	b, conns := failing(t, s)
	b.grace = silence
	b.Demand(backend.Demand{Visible: true})
	r := start(t, b, &backendtest.Sink{})
	s.accept(t)
	fc := <-conns
	fc.armed.Store(true)
	if err := b.Input(backend.Key{Down: true, Code: 0x04}); err != nil {
		t.Fatal(err)
	}
	if err := r.wait(t); !errors.Is(err, errInjected) {
		t.Errorf("Run returned %v, want the write's error", err)
	}
}

// A write fails as the server hangs up, before the reader has read the
// reason the server sent: the reader goes on, and Run's result is the
// server's end.
func TestRunWriteFailsThenServerEnds(t *testing.T) {
	checkGoroutines(t)
	s := listen(t, rdptest.Server{})
	b, conns := failing(t, s)
	b.Demand(backend.Demand{Visible: true})
	r := start(t, b, &backendtest.Sink{})
	c := s.accept(t)
	fc := <-conns
	fc.armed.Store(true)
	if err := b.Input(backend.Key{Down: true, Code: 0x04}); err != nil {
		t.Fatal(err)
	}
	<-fc.failed
	time.Sleep(silence)
	if err := c.Write(rdptest.SetErrorInfo(0x1), rdptest.DisconnectUltimatum()); err != nil {
		t.Fatal(err)
	}
	if err := r.wait(t); err != nil {
		t.Errorf("Run returned %v, want nil for an administrator's disconnect", err)
	}
}
