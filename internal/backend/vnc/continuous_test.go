// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package vnc

import (
	"errors"
	"image"
	"strings"
	"testing"

	"changkun.de/x/occamy/internal/backend"
)

// expectBoth returns the client's next two messages, a T and a U in
// either order.
func expectBoth[T, U any](t *testing.T, c *serverConn) (T, U) {
	t.Helper()
	var (
		a       T
		b       U
		gotA    bool
		gotB    bool
		unknown []any
	)
	for range 2 {
		switch m := c.next(t).(type) {
		case T:
			a, gotA = m, true
		case U:
			b, gotB = m, true
		default:
			unknown = append(unknown, m)
		}
	}
	if !gotA || !gotB {
		t.Fatalf("client sent %+v and %+v, want a %T and a %T", a, b, a, b)
	}
	if len(unknown) > 0 {
		t.Fatalf("client sent %+v", unknown)
	}
	return a, b
}

func TestContinuousUpdates(t *testing.T) {
	checkGoroutines(t)
	s := connect(t, serverOptions{}, true)
	img := pattern(64, 48, 0)
	s.fullScreen(t, img)
	screen := img.Rect

	// EndOfContinuousUpdates alone is not enough: TigerVNC enables them
	// only for clients that take Fences.
	s.c.send(t, putEndOfContinuous())
	s.c.quiet(t, silence)
	s.c.send(t, putUpdate(putRaw(s.c.pf, img, image.Rect(0, 0, 1, 1))))
	if req := expect[requestMsg](t, s.c); !req.incremental {
		t.Fatalf("request %+v, want changes", req)
	}

	// A Fence that asks for an answer gets one, with BlockBefore kept,
	// the other flags cleared, and the payload echoed; and continuous
	// updates go on for the whole screen.
	s.c.send(t, putFence(fenceRequest|fenceBlockBefore|fenceBlockAfter|fenceSyncNext, "payload"))
	fence, cu := expectBoth[fenceMsg, continuousMsg](t, s.c)
	if fence != (fenceMsg{fenceBlockBefore, "payload"}) {
		t.Errorf("fence %+v, want BlockBefore and the payload", fence)
	}
	if cu != (continuousMsg{true, screen}) {
		t.Errorf("continuous updates %+v, want on for %v", cu, screen)
	}
	// An answer to the request outstanding is the last request: the
	// server pushes the changes from now on.
	s.c.send(t, putUpdate(putRaw(s.c.pf, img, image.Rect(0, 0, 1, 1))))
	s.c.send(t, putUpdate(putRaw(s.c.pf, img, image.Rect(1, 0, 2, 1))))
	s.c.quiet(t, silence)

	// A Fence without the request flag answers nothing the backend
	// asked, and a Fence with an empty payload is answered as well.
	s.c.send(t, putFence(fenceBlockAfter, "x"), putFence(fenceRequest|fenceBlockAfter, ""))
	if m := expect[fenceMsg](t, s.c); m != (fenceMsg{}) {
		t.Errorf("fence %+v, want no flags and no payload", m)
	}
	s.c.quiet(t, silence)

	// Nobody can see: they stop, and the server's EndOfContinuousUpdates
	// in answer changes nothing.
	s.b.Demand(backend.Demand{Visible: false})
	if m := expect[continuousMsg](t, s.c); m != (continuousMsg{false, screen}) {
		t.Errorf("continuous updates %+v, want off", m)
	}
	s.c.send(t, putEndOfContinuous())
	s.c.quiet(t, silence)

	// A viewer is back: they go on, and the server sends the changes
	// since without a request.
	s.b.Demand(backend.Demand{Visible: true})
	if m := expect[continuousMsg](t, s.c); m != (continuousMsg{true, screen}) {
		t.Errorf("continuous updates %+v, want on", m)
	}
	s.c.quiet(t, silence)

	// A resize asks for the whole new screen, and moves continuous
	// updates to it.
	want := pattern(100, 80, 1)
	s.c.send(t, putUpdate(putDesktopSize(100, 80)))
	if m := expect[continuousMsg](t, s.c); m != (continuousMsg{true, want.Rect}) {
		t.Errorf("continuous updates %+v, want on for %v", m, want.Rect)
	}
	if req := expect[requestMsg](t, s.c); req.incremental || req.r != want.Rect {
		t.Errorf("request %+v, want the whole new screen", req)
	}
	s.c.send(t, putUpdate(putRaw(s.c.pf, want, want.Rect)))
	waitFor(t, "the new screen", func() bool { return s.sink.Pixels().RGBAAt(99, 79) == want.RGBAAt(99, 79) })
	s.c.quiet(t, silence)
	equalImages(t, s.sink.Pixels(), want)
}

func TestContinuousUpdatesWhileHidden(t *testing.T) {
	// A server that offers them while nobody can see the screen has them
	// enabled when a viewer comes, not before.
	checkGoroutines(t)
	s := connect(t, serverOptions{}, false)
	s.c.send(t, putFence(fenceRequest, "1"), putEndOfContinuous())
	if m := expect[fenceMsg](t, s.c); m != (fenceMsg{0, "1"}) {
		t.Errorf("fence %+v, want the payload", m)
	}
	s.c.quiet(t, silence)
	s.b.Demand(backend.Demand{Visible: true})
	// The first request asks for the whole screen, which continuous
	// updates do not bring.
	cu, req := expectBoth[continuousMsg, requestMsg](t, s.c)
	if cu != (continuousMsg{true, image.Rect(0, 0, 64, 48)}) || req.incremental {
		t.Errorf("client sent %+v and %+v, want continuous updates and a request for the whole screen", cu, req)
	}
}

func TestFenceWriterStopped(t *testing.T) {
	r := newRun()
	for range cap(r.control) {
		r.control <- nil
	}
	close(r.wdone)
	if err := r.fence(fenceRequest, nil); !errors.Is(err, errWriterStopped) {
		t.Errorf("fence = %v, want errWriterStopped", err)
	}
	if err := r.fence(0, nil); err != nil {
		t.Errorf("fence without a request = %v", err)
	}
}

func TestFenceErrors(t *testing.T) {
	for _, tt := range []struct {
		name string
		msg  []byte
		want string
	}{
		{"payload beyond 64 bytes", putFence(0, strings.Repeat("x", 65)), "fence payload of 65 bytes exceeds 64"},
		{"payload cut short", putFence(0, "abc")[:10], "fence"},
		{"flags cut short", putFence(0, "")[:5], "fence"},
	} {
		if _, err := decode(t, image.Pt(8, 8), defaultLimits, tt.msg); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: %v, want an error with %q", tt.name, err, tt.want)
		}
	}
}
