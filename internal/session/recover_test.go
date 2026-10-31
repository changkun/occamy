// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package session_test

import (
	"bytes"
	"context"
	"errors"
	"image"
	"testing"
	"time"

	"changkun.de/x/occamy/internal/backend"
	"changkun.de/x/occamy/internal/session"
	"changkun.de/x/occamy/internal/terminal"
)

// leavesLocked locks the top row of tiles and never unlocks it: it
// panics, as a decoder's bug would, or returns, as a forgotten Unlock on
// an error path would.
type leavesLocked struct {
	locked  chan struct{}
	release chan struct{}
	panics  bool
}

func (*leavesLocked) Class() backend.Class           { return backend.Graphical }
func (*leavesLocked) Input(backend.InputEvent) error { return nil }
func (*leavesLocked) Demand(backend.Demand)          {}

func (b *leavesLocked) Run(ctx context.Context, sink backend.Sink) error {
	sink.Resize(256, 256)
	img := sink.Lock(image.Rect(0, 0, 256, 64))
	close(b.locked)
	<-b.release
	if b.panics {
		past := len(img.Pix)
		img.Pix[past] = 0
	}
	return nil
}

// waitEnded waits for s to end, failing the test if it does not.
func waitEnded(t *testing.T, s *session.Session) session.End {
	t.Helper()
	select {
	case <-s.Done():
		return s.End()
	case <-time.After(5 * time.Second):
		t.Fatal("the session did not end")
		return session.End{}
	}
}

// TestEndReleasesRows checks that a session releases the rows of the
// framebuffer that its backend left locked, however it ended, so that a
// writer waiting for them in ReadTile returns.
func TestEndReleasesRows(t *testing.T) {
	for _, tc := range []struct {
		name   string
		panics bool
		reason session.Reason
	}{
		{"panic", true, session.Internal},
		{"return", false, session.RemoteEnded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := &leavesLocked{locked: make(chan struct{}), release: make(chan struct{}), panics: tc.panics}
			s, _ := start(t, session.Config{Backend: b, TileSize: 64})
			<-b.locked
			read := make(chan image.Rectangle, 1)
			go func() {
				r, _ := s.Framebuffer().ReadTile(2, make([]byte, 4*64*64))
				read <- r
			}()
			select {
			case r := <-read:
				t.Fatalf("ReadTile read %v while the backend held it", r)
			case <-time.After(50 * time.Millisecond):
			}
			close(b.release)
			if got := waitEnded(t, s); got.Reason != tc.reason {
				t.Errorf("ended with %+v, want %v", got, tc.reason)
			}
			select {
			case r := <-read:
				if !r.Empty() {
					t.Errorf("ReadTile read %v after the session ended", r)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("ReadTile still waits for the rows the backend left locked")
			}
		})
	}
}

// slowStop draws once more as it stops, after its context is done, when
// the test lets it.
type slowStop struct {
	stopping chan struct{}
	release  chan struct{}
}

func (*slowStop) Class() backend.Class           { return backend.Graphical }
func (*slowStop) Input(backend.InputEvent) error { return nil }
func (*slowStop) Demand(backend.Demand)          {}

func (b *slowStop) Run(ctx context.Context, sink backend.Sink) error {
	sink.Resize(256, 256)
	<-ctx.Done()
	close(b.stopping)
	<-b.release
	r := image.Rect(0, 0, 64, 64)
	img := sink.Lock(r)
	img.Pix[0] = 0xff
	sink.Unlock(r)
	return ctx.Err()
}

// TestScreenUntilRunReturns checks that the session closes its
// framebuffer only once Run has returned: until then the backend may
// still draw, as it stops, on the screen, whose pixels and rows are its
// own, and only then are the Locks it left released.
func TestScreenUntilRunReturns(t *testing.T) {
	b := &slowStop{stopping: make(chan struct{}), release: make(chan struct{})}
	s, _ := start(t, session.Config{Backend: b, TileSize: 64})
	go s.Close()
	<-b.stopping
	fb := s.Framebuffer()
	if r, _ := fb.ReadTile(0, make([]byte, 4*64*64)); r.Empty() {
		t.Error("the framebuffer was closed before Run returned")
	}
	clock := fb.Clock()
	close(b.release)
	waitEnded(t, s)
	if fb.Clock() == clock {
		t.Error("the backend's last drawing did not reach the screen")
	}
	if r, _ := fb.ReadTile(0, make([]byte, 4*64*64)); !r.Empty() {
		t.Errorf("ReadTile read %v after the session ended", r)
	}
}

// panicky is a terminal emulator with a bug that output can trigger.
type panicky struct{ *terminal.Terminal }

func (p panicky) Write(b []byte) {
	if bytes.Contains(b, []byte("boom")) {
		panic("the emulator's bug")
	}
	p.Terminal.Write(b)
}

// twoReaders feeds its terminal from a goroutine of its own, as SSH's
// readers of stdout and stderr do, recovers a panic there, and returns
// it from Run once its other output, which goes on meanwhile, is done.
type twoReaders struct{}

func (twoReaders) Class() backend.Class           { return backend.Terminal }
func (twoReaders) Input(backend.InputEvent) error { return nil }
func (twoReaders) Demand(backend.Demand)          {}

func (twoReaders) Run(_ context.Context, sink backend.Sink) error {
	sink.Resize(80, 24)
	panicked := make(chan error, 1)
	go func() {
		defer func() { panicked <- backend.Recovered(recover()) }()
		sink.TermData([]byte("boom"))
	}()
	err := <-panicked
	sink.TermData([]byte("after"))
	sink.Resize(100, 30)
	return err
}

// TestTermDataPanics checks that a panic of the terminal emulator on a
// backend's goroutine leaves the session's output unlocked: the
// backend's other output, and a resize, go on until it stops, and the
// session ends with Internal and the panic. Before, the next TermData
// waited forever, and so did Run and the session.
func TestTermDataPanics(t *testing.T) {
	s, _ := start(t, session.Config{
		Backend:  twoReaders{},
		Terminal: panicky{terminal.New(80, 24, terminal.Options{})},
	})
	got := waitEnded(t, s)
	var p session.PanicError
	if got.Reason != session.Internal || !errors.As(got.Err, &p) || p.Value != "the emulator's bug" {
		t.Errorf("ended with %+v, want Internal with the emulator's panic", got)
	}
	screen := s.Screen()
	if screen.Line(0) != "after" || screen.Cols != 100 || screen.Rows != 30 {
		t.Errorf("screen %dx%d %q, want the output and the size after the panic", screen.Cols, screen.Rows, screen.Line(0))
	}
}
