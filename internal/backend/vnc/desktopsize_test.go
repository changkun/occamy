// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package vnc

import (
	"image"
	"slices"
	"strings"
	"testing"

	"changkun.de/x/occamy/internal/backend"
	"changkun.de/x/occamy/internal/backend/backendtest"
)

// connectLogged is connect with the backend's log kept.
func connectLogged(t *testing.T, opts serverOptions, visible bool) (*session, *logBuffer) {
	t.Helper()
	log := &logBuffer{}
	s := &session{srv: newServer(t, opts), sink: &backendtest.Sink{}}
	s.b = newBackend(t, Config{Addr: s.srv.addr, Password: opts.password, Logger: log.logger()})
	s.b.Demand(backend.Demand{Visible: visible})
	s.run = start(t, s.b, s.sink)
	s.c = s.srv.accept(t)
	return s, log
}

// expectSetSize takes a SetDesktopSize of one screen at the origin of
// the given size, id and flags.
func expectSetSize(t *testing.T, c *serverConn, w, h int, id, flags uint32) {
	t.Helper()
	m := expect[setSizeMsg](t, c)
	want := setSizeMsg{size: image.Pt(w, h), screens: []screenMsg{{id: id, r: image.Rect(0, 0, w, h), flags: flags}}}
	if m.size != want.size || !slices.Equal(m.screens, want.screens) {
		t.Fatalf("SetDesktopSize %+v, want %+v", m, want)
	}
}

func TestExtendedDesktopSize(t *testing.T) {
	checkGoroutines(t)
	s, log := connectLogged(t, serverOptions{}, true)
	resize := func(w, h int) {
		t.Helper()
		if err := s.b.Input(backend.Resize{Width: w, Height: h}); err != nil {
			t.Fatalf("Input(Resize) = %v", err)
		}
	}
	// A resize before the server has shown support waits for it.
	req := expect[requestMsg](t, s.c)
	resize(80, 60)
	s.c.quiet(t, silence)

	// The answer to the first request shows support, with the layout of
	// the screen at the size it has, which changes nothing.
	img := pattern(64, 48, 0)
	layout := screenMsg{id: 7, r: img.Rect, flags: 0x30}
	s.c.send(t, putUpdate(putExtDesktopSize(0, 0, 64, 48, layout), putRaw(s.c.pf, img, req.r)))
	expectSetSize(t, s.c, 80, 60, 7, 0x30)
	if req := expect[requestMsg](t, s.c); !req.incremental {
		t.Fatalf("request %+v, want changes", req)
	}
	if n := s.sink.Resizes(); n != 1 {
		t.Errorf("%d resizes, want the first only", n)
	}

	// TigerVNC sends one in answer to every request for the whole
	// screen; another client's change of layout is not a resize either.
	s.c.send(t, putUpdate(putExtDesktopSize(2, 0, 64, 48, screenMsg{id: 9, r: img.Rect})))
	if req := expect[requestMsg](t, s.c); !req.incremental {
		t.Fatalf("request %+v after a layout of the same size, want changes", req)
	}

	// One SetDesktopSize awaits its answer at a time; the newest resize
	// asked for meanwhile follows it.
	resize(90, 70)
	resize(120, 90)
	s.c.quiet(t, silence)

	// The answer resizes the screen.
	s.c.send(t, putUpdate(putExtDesktopSize(reasonClient, 0, 80, 60, screenMsg{id: 9, r: image.Rect(0, 0, 80, 60), flags: 1})))
	expectSetSize(t, s.c, 120, 90, 9, 1)
	if req := expect[requestMsg](t, s.c); req.incremental || req.r != image.Rect(0, 0, 80, 60) {
		t.Fatalf("request %+v, want the whole new screen", req)
	}
	if got := s.sink.Size(); got != image.Pt(80, 60) {
		t.Errorf("screen %v, want 80×60", got)
	}

	// A refusal changes nothing and is logged.
	s.c.send(t, putUpdate(putExtDesktopSize(reasonClient, 3, 0, 0)))
	if req := expect[requestMsg](t, s.c); !req.incremental || req.r != image.Rect(0, 0, 80, 60) {
		t.Fatalf("request %+v, want changes to the screen", req)
	}
	if got := s.sink.Size(); got != image.Pt(80, 60) {
		t.Errorf("screen %v after a refusal, want 80×60", got)
	}
	if !strings.Contains(log.String(), "invalid screen layout") || !strings.Contains(log.String(), "width=120 height=90") {
		t.Errorf("the refusal is not logged:\n%s", log)
	}

	// Once answered, the next resize is sent; one of the size the
	// screen has, the size just refused, or one beyond the limits, is
	// not.
	resize(80, 60)
	resize(120, 90)
	resize(0, 60)
	resize(1<<20, 60)
	s.c.quiet(t, silence)
	resize(100, 75)
	expectSetSize(t, s.c, 100, 75, 9, 1)

	// A forwarded resize is answered later, and is no warning.
	s.c.send(t, putUpdate(putExtDesktopSize(reasonClient, statusForward, 0, 0)))
	expect[requestMsg](t, s.c)
	if strings.Contains(log.String(), "request forwarded") {
		t.Errorf("a forwarded resize is a warning:\n%s", log)
	}

	// The server, or another client, resizes the screen.
	s.c.send(t, putUpdate(putExtDesktopSize(0, 0, 50, 40)))
	if req := expect[requestMsg](t, s.c); req.incremental || req.r != image.Rect(0, 0, 50, 40) {
		t.Fatalf("request %+v, want the whole new screen", req)
	}
	if got := s.sink.Size(); got != image.Pt(50, 40) {
		t.Errorf("screen %v, want 50×40", got)
	}
	// It kept the first screen's id and flags of the last layout that
	// had one.
	resize(64, 48)
	expectSetSize(t, s.c, 64, 48, 9, 1)

	// Once the server resizes on request, a size it refused is asked
	// for again.
	s.c.send(t, putUpdate(putExtDesktopSize(reasonClient, 0, 64, 48, screenMsg{id: 9, r: image.Rect(0, 0, 64, 48), flags: 1})))
	expect[requestMsg](t, s.c)
	resize(120, 90)
	expectSetSize(t, s.c, 120, 90, 9, 1)
	s.c.send(t, putUpdate(putExtDesktopSize(reasonClient, 3, 0, 0)))
	expect[requestMsg](t, s.c)
	if n := strings.Count(log.String(), "width=120 height=90"); n != 2 {
		t.Errorf("%d refusals of 120×90 logged, want 2:\n%s", n, log)
	}

	// A server that prohibits resizing is asked no more, whatever the
	// size.
	resize(70, 50)
	expectSetSize(t, s.c, 70, 50, 9, 1)
	s.c.send(t, putUpdate(putExtDesktopSize(reasonClient, statusProhibited, 0, 0)))
	expect[requestMsg](t, s.c)
	resize(72, 54)
	resize(74, 56)
	s.c.quiet(t, silence)
	if n := strings.Count(log.String(), "administratively prohibited"); n != 1 {
		t.Errorf("%d prohibitions logged, want 1:\n%s", n, log)
	}
}

func TestResizeWithoutSupport(t *testing.T) {
	// A server without ExtendedDesktopSize keeps its size: the viewers
	// scale it.
	checkGoroutines(t)
	s := connect(t, serverOptions{}, true)
	s.fullScreen(t, pattern(64, 48, 0))
	if err := s.b.Input(backend.Resize{Width: 100, Height: 80}); err != nil {
		t.Fatal(err)
	}
	s.c.send(t, putUpdate(putRaw(s.c.pf, pattern(64, 48, 0), image.Rect(0, 0, 1, 1))))
	expect[requestMsg](t, s.c)
	s.c.quiet(t, silence)
}

func TestExtendedDesktopSizeErrors(t *testing.T) {
	for _, tt := range []struct {
		name string
		msg  []byte
		want string
	}{
		{"beyond the limits", putUpdate(putExtDesktopSize(0, 0, 0xffff, 0xffff)), "exceeds the limits"},
		{"empty", putUpdate(putExtDesktopSize(0, 0, 0, 8)), "empty screen"},
		{"two in one update", putUpdate(putExtDesktopSize(0, 0, 20, 8), putExtDesktopSize(0, 0, 8, 20)), "a second DesktopSize"},
		{"a layout cut short", putUpdate(putExtDesktopSize(0, 0, 8, 8, screenMsg{}))[:30], "EOF"},
		{"no layout", putUpdate(putExtDesktopSize(0, 0, 8, 8))[:18], "EOF"},
	} {
		if _, err := decode(t, image.Pt(8, 8), defaultLimits, tt.msg); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: %v, want an error with %q", tt.name, err, tt.want)
		}
	}
	// A layout of 255 screens is read whole, and the screen keeps its
	// size.
	screens := make([]screenMsg, 255)
	sink, err := decode(t, image.Pt(8, 8), defaultLimits, putUpdate(putExtDesktopSize(0, 0, 8, 8, screens...)), putBell())
	if err != nil || sink.Bells() != 1 || sink.Resizes() != 1 {
		t.Errorf("a layout of 255 screens: %v, %d bells, %d resizes", err, sink.Bells(), sink.Resizes())
	}
}
