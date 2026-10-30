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
	"image/draw"
	"io"
	"net"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"changkun.de/x/occamy/internal/backend"
	"changkun.de/x/occamy/internal/backend/backendtest"
)

// timeout bounds every wait in these tests.
const timeout = 10 * time.Second

// silence is how long a test waits to see that the client sends nothing.
const silence = 100 * time.Millisecond

// checkGoroutines fails the test if goroutines started during it are
// still running when its cleanups have run. Call it first, so that its
// cleanup runs last.
func checkGoroutines(t *testing.T) {
	t.Helper()
	before := runtime.NumGoroutine()
	t.Cleanup(func() {
		deadline := time.Now().Add(timeout)
		for runtime.NumGoroutine() > before {
			if time.Now().After(deadline) {
				t.Errorf("%d goroutines left, %d before the test:\n%s", runtime.NumGoroutine(), before, stacks())
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	})
}

func stacks() []byte {
	buf := make([]byte, 1<<20)
	return buf[:runtime.Stack(buf, true)]
}

// ownFrame reports whether a goroutine's stack runs code of package vnc,
// not of its tests. The location after "created by" is the creator's.
func ownFrame(g string) bool {
	lines := strings.Split(g, "\n")
	for i := 0; i < len(lines); i++ {
		l := lines[i]
		if strings.HasPrefix(l, "created by ") {
			i++
			continue
		}
		if strings.Contains(l, "/internal/backend/vnc/") && strings.Contains(l, ".go:") && !strings.Contains(l, "_test.go:") {
			return true
		}
	}
	return false
}

// checkStopped fails the test if a goroutine is still running code of
// package vnc, which must not outlive Run. A goroutine that has just
// signalled its end may take a moment to return.
func checkStopped(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		var left []string
		for g := range strings.SplitSeq(string(stacks()), "\n\n") {
			if ownFrame(g) {
				left = append(left, g)
			}
		}
		if len(left) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("goroutines outlived Run:\n%s", strings.Join(left, "\n\n"))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// runner runs a backend in the background.
type runner struct {
	cancel context.CancelFunc
	done   chan struct{}
	err    error
}

// start runs b on sink until the test ends or the runner is stopped.
func start(t *testing.T, b *Backend, sink backend.Sink) *runner {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	r := &runner{cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(r.done)
		r.err = b.Run(ctx, sink)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-r.done:
		case <-time.After(timeout):
			t.Errorf("Run did not return after the test")
		}
	})
	return r
}

// wait waits for Run to return, checks that it left no goroutine
// behind, and returns its error.
func (r *runner) wait(t *testing.T) error {
	t.Helper()
	select {
	case <-r.done:
	case <-time.After(timeout):
		t.Fatal("Run did not return")
	}
	checkStopped(t)
	return r.err
}

// stop cancels Run's context and returns Run's error.
func (r *runner) stop(t *testing.T) error {
	t.Helper()
	r.cancel()
	return r.wait(t)
}

// waitFor waits until cond holds.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func newBackend(t *testing.T, cfg Config) *Backend {
	t.Helper()
	b, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// session is a backend connected to a fake server.
type session struct {
	srv  *fakeServer
	b    *Backend
	sink *backendtest.Sink
	run  *runner
	c    *serverConn
}

// connect starts a fake server with opts and a backend connected to it,
// which a viewer can see if visible.
func connect(t *testing.T, opts serverOptions, visible bool) *session {
	t.Helper()
	s := &session{srv: newServer(t, opts), sink: &backendtest.Sink{}}
	s.b = newBackend(t, Config{Addr: s.srv.addr, Password: opts.password})
	s.b.Demand(backend.Demand{Visible: visible})
	s.run = start(t, s.b, s.sink)
	s.c = s.srv.accept(t)
	return s
}

// fullScreen answers the first request with img as the whole screen, and
// takes the request that follows it.
func (s *session) fullScreen(t *testing.T, img *image.RGBA) {
	t.Helper()
	req := expect[requestMsg](t, s.c)
	if req.incremental || req.r != img.Rect {
		t.Fatalf("first request %+v, want the whole screen %v", req, img.Rect)
	}
	s.c.send(t, putUpdate(putRaw(s.c.pf, img, img.Rect)))
	if req := expect[requestMsg](t, s.c); !req.incremental || req.r != img.Rect {
		t.Fatalf("second request %+v, want changes to %v", req, img.Rect)
	}
	equalImages(t, s.sink.Pixels(), img)
}

// equalImages fails the test where got and want differ.
func equalImages(t *testing.T, got, want *image.RGBA) {
	t.Helper()
	if got == nil {
		t.Fatal("no framebuffer")
	}
	if got.Rect != want.Rect {
		t.Fatalf("framebuffer %v, want %v", got.Rect, want.Rect)
	}
	for y := want.Rect.Min.Y; y < want.Rect.Max.Y; y++ {
		for x := want.Rect.Min.X; x < want.Rect.Max.X; x++ {
			if g, w := got.RGBAAt(x, y), want.RGBAAt(x, y); g != w {
				t.Fatalf("pixel (%d, %d) is %v, want %v", x, y, g, w)
			}
		}
	}
}

// wantPixelFormat is the pixel format the backend must ask for:
// image.RGBA's layout.
var wantPixelFormat = pixFormat{
	bpp: 32, depth: 24, trueColour: true,
	rmax: 255, gmax: 255, bmax: 255, rshift: 0, gshift: 8, bshift: 16,
}

func TestNew(t *testing.T) {
	for _, tt := range []struct {
		addr string
		want string // empty for an error
	}{
		{"vnc.example", "vnc.example:5900"},
		{"vnc.example:5901", "vnc.example:5901"},
		{"10.0.0.1", "10.0.0.1:5900"},
		{"[::1]", "[::1]:5900"},
		{"[::1]:5902", "[::1]:5902"},
		{"", ""},
		{":5900", ""},
	} {
		b, err := New(Config{Addr: tt.addr})
		switch {
		case tt.want == "" && err == nil:
			t.Errorf("New(%q) succeeded, want an error", tt.addr)
		case tt.want != "" && err != nil:
			t.Errorf("New(%q) = %v", tt.addr, err)
		case tt.want != "" && b.addr != tt.want:
			t.Errorf("New(%q) dials %q, want %q", tt.addr, b.addr, tt.want)
		}
	}
	if _, err := New(Config{Addr: "x", DialTimeout: -time.Second}); err == nil {
		t.Error("New accepted a negative dial timeout")
	}
	b := newBackend(t, Config{Addr: "x"})
	if b.dialTimeout != DefaultDialTimeout || b.log == nil {
		t.Errorf("defaults: timeout %v, logger %v", b.dialTimeout, b.log)
	}
	if c := b.Class(); c != backend.Graphical {
		t.Errorf("Class() = %v, want graphical", c)
	}
}

func TestHandshake(t *testing.T) {
	for _, tt := range []struct {
		name    string
		version string // the server's
		reply   string // the client's
		types   []byte
		pw      string
	}{
		{"3.3 none", "003.003", "RFB 003.003\n", []byte{secNone}, ""},
		{"3.3 vnc auth", "003.003", "RFB 003.003\n", []byte{secVNCAuth}, "hunter2"},
		{"3.7 none", "003.007", "RFB 003.007\n", []byte{secNone}, ""},
		{"3.7 vnc auth", "003.007", "RFB 003.007\n", []byte{secVNCAuth}, "hunter2"},
		{"3.8 none", "003.008", "RFB 003.008\n", []byte{secNone}, ""},
		{"3.8 vnc auth", "003.008", "RFB 003.008\n", []byte{secVNCAuth}, "hunter2"},
		{"3.5 is 3.3", "003.005", "RFB 003.003\n", []byte{secNone}, ""},
		{"Apple 3.889 is 3.8", "003.889", "RFB 003.008\n", []byte{secVNCAuth}, "hunter2"},
		{"4.1 is 3.8", "004.001", "RFB 003.008\n", []byte{secNone}, ""},
		{"long password", "003.008", "RFB 003.008\n", []byte{secVNCAuth}, "hunter2-and-more"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			checkGoroutines(t)
			s := connect(t, serverOptions{version: tt.version, types: tt.types, password: tt.pw, name: "Desktop ✓", size: image.Pt(40, 30)}, false)
			if got := s.srv.Replies(); len(got) != 1 || got[0] != tt.reply {
				t.Errorf("client replied %q, want %q", got, tt.reply)
			}
			if s.c.security != tt.types[0] {
				t.Errorf("security type %d, want %d", s.c.security, tt.types[0])
			}
			if !s.c.shared {
				t.Error("ClientInit asked for exclusive access, want shared")
			}
			if s.c.pf != wantPixelFormat {
				t.Errorf("pixel format %+v, want %+v", s.c.pf, wantPixelFormat)
			}
			if want := []int32{encCopyRect, encRaw, encCursor, encDesktopSize}; !slices.Equal(s.c.encodings, want) {
				t.Errorf("encodings %v, want %v", s.c.encodings, want)
			}
			waitFor(t, "the title", func() bool { return s.sink.TitleText() == "Desktop ✓" })
			if got := s.sink.Size(); got != image.Pt(40, 30) {
				t.Errorf("size %v, want 40×30", got)
			}
			if n := len(s.sink.Prompts()); n != 0 {
				t.Errorf("%d prompts, want none", n)
			}
			if err := s.run.stop(t); !errors.Is(err, context.Canceled) {
				t.Errorf("Run = %v, want context.Canceled", err)
			}
		})
	}
}

func TestSecurityChoice(t *testing.T) {
	for _, tt := range []struct {
		name    string
		version string
		types   []byte
		pw      string // configured
		want    byte   // the type used; 0 for an error
		prompts int
	}{
		{"auth over none with a password", "003.008", []byte{secNone, secVNCAuth}, "hunter2", secVNCAuth, 0},
		{"none over auth without one", "003.008", []byte{secVNCAuth, secNone}, "", secNone, 0},
		{"auth asks the owner", "003.008", []byte{secVNCAuth}, "", secVNCAuth, 1},
		{"unknown types skipped", "003.007", []byte{16, 30, secVNCAuth}, "hunter2", secVNCAuth, 0},
		{"no supported type", "003.008", []byte{16, 30}, "hunter2", 0, 0},
		{"3.3 server picks an unsupported type", "003.003", []byte{16}, "", 0, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			checkGoroutines(t)
			srv := newServer(t, serverOptions{version: tt.version, types: tt.types, password: "hunter2"})
			sink := &backendtest.Sink{Answer: func(backend.Prompt) (backend.PromptReply, error) {
				return backend.PromptReply{Answers: []string{"hunter2"}}, nil
			}}
			r := start(t, newBackend(t, Config{Addr: srv.addr, Password: tt.pw}), sink)
			if tt.want == 0 {
				err := r.wait(t)
				if !errors.Is(err, ErrNoSecurityType) {
					t.Fatalf("Run = %v, want ErrNoSecurityType", err)
				}
				if sink.Resizes() != 0 {
					t.Error("the screen was resized without a handshake")
				}
				return
			}
			c := srv.accept(t)
			if c.security != tt.want {
				t.Errorf("security type %d, want %d", c.security, tt.want)
			}
			if n := len(sink.Prompts()); n != tt.prompts {
				t.Errorf("%d prompts, want %d", n, tt.prompts)
			}
			if err := r.stop(t); !errors.Is(err, context.Canceled) {
				t.Errorf("Run = %v, want context.Canceled", err)
			}
		})
	}
}

func TestWrongPassword(t *testing.T) {
	for _, tt := range []struct {
		version string
		reason  bool // 3.8 gives a reason
	}{
		{"003.003", false},
		{"003.007", false},
		{"003.008", true},
	} {
		t.Run(tt.version, func(t *testing.T) {
			checkGoroutines(t)
			srv := newServer(t, serverOptions{version: tt.version, types: []byte{secVNCAuth}, password: "hunter2"})
			sink := &backendtest.Sink{}
			err := start(t, newBackend(t, Config{Addr: srv.addr, Password: "hunter3"}), sink).wait(t)
			if !errors.Is(err, ErrAuthFailed) {
				t.Fatalf("Run = %v, want ErrAuthFailed", err)
			}
			if got := strings.Contains(err.Error(), "Authentication failed"); got != tt.reason {
				t.Errorf("Run = %v; the server's reason shown: %v, want %v", err, got, tt.reason)
			}
			if strings.Contains(err.Error(), "hunter") {
				t.Errorf("the error shows a password: %v", err)
			}
			if srv.Failures() != 1 || sink.Resizes() != 0 || len(sink.Prompts()) != 0 {
				t.Errorf("%d failures, %d resizes, %d prompts; want 1, 0, 0", srv.Failures(), sink.Resizes(), len(sink.Prompts()))
			}
		})
	}
}

func TestPasswordPrompt(t *testing.T) {
	for _, tt := range []struct {
		name   string
		answer func(backend.Prompt) (backend.PromptReply, error) // nil: nobody answers
		err    error                                             // nil: connects
	}{
		{"right", answers("hunter2"), nil},
		{"wrong", answers("hunter3"), ErrAuthFailed},
		{"unanswered", nil, backendtest.ErrNoAnswer},
		{"too many answers", answers("hunter2", "again"), errAny},
	} {
		t.Run(tt.name, func(t *testing.T) {
			checkGoroutines(t)
			srv := newServer(t, serverOptions{types: []byte{secVNCAuth}, password: "hunter2"})
			sink := &backendtest.Sink{Answer: tt.answer}
			r := start(t, newBackend(t, Config{Addr: srv.addr}), sink)
			if tt.err == nil {
				srv.accept(t)
				if err := r.stop(t); !errors.Is(err, context.Canceled) {
					t.Errorf("Run = %v, want context.Canceled", err)
				}
			} else if err := r.wait(t); err == nil || !errors.Is(tt.err, errAny) && !errors.Is(err, tt.err) {
				t.Errorf("Run = %v, want %v", err, tt.err)
			}
			prompts := sink.Prompts()
			if len(prompts) != 1 {
				t.Fatalf("%d prompts, want 1", len(prompts))
			}
			p := prompts[0]
			if !strings.Contains(p.Instruction, srv.addr) || len(p.Fields) != 1 || p.Fields[0] != (backend.PromptField{Label: "Password", Secret: true}) {
				t.Errorf("prompt %+v, want one secret password field for %s", p, srv.addr)
			}
		})
	}
}

// errAny stands for any error in tables.
var errAny = errors.New("any error")

func answers(a ...string) func(backend.Prompt) (backend.PromptReply, error) {
	return func(backend.Prompt) (backend.PromptReply, error) {
		return backend.PromptReply{Answers: a}, nil
	}
}

func TestRefused(t *testing.T) {
	for _, version := range []string{"003.003", "003.007", "003.008"} {
		t.Run(version, func(t *testing.T) {
			checkGoroutines(t)
			srv := newServer(t, serverOptions{version: version, refuse: "Too many security failures"})
			err := start(t, newBackend(t, Config{Addr: srv.addr}), &backendtest.Sink{}).wait(t)
			if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "Too many security failures") {
				t.Errorf("Run = %v, want ErrRefused with the reason", err)
			}
		})
	}
}

func TestNotRFB(t *testing.T) {
	checkGoroutines(t)
	addr := listen(t, func(nc net.Conn) {
		nc.Write([]byte("SSH-2.0-OpenSSH_9.9\r\n"))
		io.Copy(io.Discard, nc)
	})
	err := start(t, newBackend(t, Config{Addr: addr}), &backendtest.Sink{}).wait(t)
	if err == nil || !strings.Contains(err.Error(), "not an RFB server") {
		t.Errorf("Run = %v, want an error about the server", err)
	}
}

func TestDialFailure(t *testing.T) {
	checkGoroutines(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	err = start(t, newBackend(t, Config{Addr: addr}), &backendtest.Sink{}).wait(t)
	if err == nil || errors.Is(err, context.Canceled) {
		t.Errorf("Run = %v, want a dial error", err)
	}
}

func TestRaw(t *testing.T) {
	checkGoroutines(t)
	s := connect(t, serverOptions{}, true)
	want := pattern(64, 48, 0)
	s.fullScreen(t, want)

	// A rectangle within the screen changes only its pixels.
	r := image.Rect(10, 5, 30, 35)
	patch := pattern(64, 48, 99)
	s.c.send(t, putUpdate(putRaw(s.c.pf, patch, r), putRaw(s.c.pf, patch, image.Rect(0, 0, 0, 7))))
	expect[requestMsg](t, s.c)
	draw.Draw(want, r, patch, r.Min, draw.Src)
	equalImages(t, s.sink.Pixels(), want)

	if err := s.run.stop(t); !errors.Is(err, context.Canceled) {
		t.Errorf("Run = %v, want context.Canceled", err)
	}
}

func TestCopyRect(t *testing.T) {
	checkGoroutines(t)
	s := connect(t, serverOptions{}, true)
	want := pattern(64, 48, 0)
	s.fullScreen(t, want)

	dst, src := image.Rect(20, 10, 50, 30), image.Pt(4, 12) // overlapping
	s.c.send(t, putUpdate(putCopy(dst, src), putCopy(image.Rect(1, 1, 1, 9), image.Pt(70, 70))))
	expect[requestMsg](t, s.c)
	draw.Draw(want, dst, want, src, draw.Src)
	equalImages(t, s.sink.Pixels(), want)
	if got := s.sink.Copies(); len(got) != 1 || got[0] != (backendtest.Copy{Dst: dst, Src: src}) {
		t.Errorf("copies %v, want one of %v from %v", got, dst, src)
	}
}

func TestResizeMidStream(t *testing.T) {
	checkGoroutines(t)
	s := connect(t, serverOptions{}, true)
	s.fullScreen(t, pattern(64, 48, 0))

	// The update resizes the screen and then draws on it.
	want := pattern(100, 80, 7)
	s.c.send(t, putUpdate(putDesktopSize(100, 80), putRaw(s.c.pf, want, image.Rect(0, 0, 100, 40))))
	req := expect[requestMsg](t, s.c)
	if req.incremental || req.r != want.Rect {
		t.Fatalf("request after the resize %+v, want the whole new screen", req)
	}
	if got := s.sink.Size(); got != image.Pt(100, 80) || s.sink.Resizes() != 2 {
		t.Fatalf("size %v after %d resizes, want 100×80 after 2", got, s.sink.Resizes())
	}
	s.c.send(t, putUpdate(putRaw(s.c.pf, want, want.Rect)))
	if req := expect[requestMsg](t, s.c); !req.incremental || req.r != want.Rect {
		t.Fatalf("request %+v, want changes to the new screen", req)
	}
	equalImages(t, s.sink.Pixels(), want)

	// The pointer is clamped to the new screen.
	if err := s.b.Input(backend.Pointer{X: 500, Y: 79}); err != nil {
		t.Fatal(err)
	}
	if m := expect[pointerMsg](t, s.c); m.at != image.Pt(99, 79) {
		t.Errorf("pointer at %v, want (99, 79)", m.at)
	}
}

func TestCursor(t *testing.T) {
	checkGoroutines(t)
	s := connect(t, serverOptions{}, true)
	s.fullScreen(t, pattern(64, 48, 0))

	img := image.NewRGBA(image.Rect(0, 0, 10, 3)) // two mask bytes a row
	for x := range 10 {
		for y := range 3 {
			if (x+y)%3 != 0 {
				img.SetRGBA(x, y, color.RGBA{R: uint8(20 * x), G: uint8(50 * y), B: 0x80, A: 0xff})
			}
		}
	}
	s.c.send(t, putUpdate(putCursor(s.c.pf, img, image.Pt(2, 1))))
	expect[requestMsg](t, s.c)
	got, hot := s.sink.CursorImage()
	if hot != image.Pt(2, 1) {
		t.Errorf("hot spot %v, want (2, 1)", hot)
	}
	equalImages(t, got, img)

	// A hot spot beyond the cursor is clamped to its edge.
	s.c.send(t, putUpdate(putCursor(s.c.pf, img, image.Pt(10, 7))))
	expect[requestMsg](t, s.c)
	if _, hot := s.sink.CursorImage(); hot != image.Pt(9, 2) {
		t.Errorf("hot spot %v, want (9, 2)", hot)
	}

	// An empty cursor hides the pointer.
	s.c.send(t, putUpdate(rectHeader(image.Rect(0, 0, 0, 0), encCursor)))
	expect[requestMsg](t, s.c)
	if got, _ := s.sink.CursorImage(); got != nil {
		t.Errorf("cursor %v, want hidden", got.Rect)
	}
}

func TestClipboard(t *testing.T) {
	checkGoroutines(t)
	s := connect(t, serverOptions{}, false)

	s.c.send(t, putCutText("caf\xe9 \xbd\n"))
	waitFor(t, "the clipboard", func() bool { _, data := s.sink.ClipboardContent(); return data != nil })
	if mime, data := s.sink.ClipboardContent(); mime != "text/plain" || string(data) != "café ½\n" {
		t.Errorf("clipboard %q %q, want text/plain %q", mime, data, "café ½\n")
	}

	for _, tt := range []struct {
		mime, data string
		want       string // Latin-1; empty for nothing sent
	}{
		{"text/plain;charset=utf-8", "naïve €\r\nok\rend", "na\xefve ?\nok\nend"},
		{"", "plain", "plain"},
		{"image/png", "\x89PNG", ""},
	} {
		if err := s.b.Input(backend.Clipboard{MIME: tt.mime, Data: []byte(tt.data)}); err != nil {
			t.Fatal(err)
		}
		if tt.want == "" {
			s.c.quiet(t, silence)
			continue
		}
		if m := expect[cutTextMsg](t, s.c); m.text != tt.want {
			t.Errorf("cut text %q, want %q", m.text, tt.want)
		}
	}
}

func TestInput(t *testing.T) {
	checkGoroutines(t)
	s := connect(t, serverOptions{}, false)
	in := func(ev backend.InputEvent) {
		t.Helper()
		if err := s.b.Input(ev); err != nil {
			t.Fatalf("Input(%+v) = %v", ev, err)
		}
	}
	key := func(down bool, keysym uint32) {
		t.Helper()
		if m := expect[keyMsg](t, s.c); m != (keyMsg{down, keysym}) {
			t.Errorf("key %+v, want %+v", m, keyMsg{down, keysym})
		}
	}
	pointer := func(buttons uint8, x, y int) {
		t.Helper()
		if m := expect[pointerMsg](t, s.c); m != (pointerMsg{buttons, image.Pt(x, y)}) {
			t.Errorf("pointer %+v, want %+v", m, pointerMsg{buttons, image.Pt(x, y)})
		}
	}

	in(backend.Key{Down: true, Keysym: 'a', Code: 0x04})
	in(backend.Key{Down: false, Keysym: 'a', Code: 0x04})
	in(backend.Key{Down: true}) // no keysym: nothing to send
	key(true, 'a')
	key(false, 'a')

	in(backend.Text{Text: "hé\n€"})
	for _, k := range []uint32{'h', 0xe9, 0xff0d, 0x10020ac} {
		key(true, k)
		key(false, k)
	}

	in(backend.Pointer{X: 10, Y: 20, Buttons: backend.ButtonLeft | backend.ButtonRight | backend.ButtonBack | backend.ButtonForward})
	pointer(maskLeft|maskRight|maskBack, 10, 20)
	in(backend.Pointer{X: -5, Y: 1000, Buttons: backend.ButtonMiddle})
	pointer(maskMiddle, 0, 47)

	// Three lines scroll once, at the pointer with its buttons held.
	in(backend.Wheel{DY: 3, Mode: backend.WheelLines})
	pointer(maskMiddle|maskWheelDown, 0, 47)
	pointer(maskMiddle, 0, 47)
	// 100 pixels left scroll once; the rest is dropped.
	in(backend.Wheel{DX: -100})
	pointer(maskMiddle|maskWheelLeft, 0, 47)
	pointer(maskMiddle, 0, 47)
	// Small scrolls add up.
	in(backend.Wheel{DY: -30})
	in(backend.Wheel{DY: -30})
	pointer(maskMiddle|maskWheelUp, 0, 47)
	pointer(maskMiddle, 0, 47)
	in(backend.Wheel{DX: 3, Mode: backend.WheelLines})
	pointer(maskMiddle|maskWheelRgt, 0, 47)
	pointer(maskMiddle, 0, 47)

	// Without ExtendedDesktopSize, a resize is not sent.
	in(backend.Resize{Width: 800, Height: 600})
	if err := s.b.Input(backend.TermInput{Data: []byte("ls\r")}); !errors.Is(err, ErrUnsupportedInput) {
		t.Errorf("Input(TermInput) = %v, want ErrUnsupportedInput", err)
	}
	s.c.quiet(t, silence)
}

func TestInputNotRunning(t *testing.T) {
	checkGoroutines(t)
	srv := newServer(t, serverOptions{})
	b := newBackend(t, Config{Addr: srv.addr})
	if err := b.Input(backend.Key{Down: true, Keysym: 'a'}); !errors.Is(err, ErrNotRunning) {
		t.Errorf("Input before Run = %v, want ErrNotRunning", err)
	}
	r := start(t, b, &backendtest.Sink{})
	srv.accept(t)
	if err := r.stop(t); !errors.Is(err, context.Canceled) {
		t.Errorf("Run = %v, want context.Canceled", err)
	}
	if err := b.Input(backend.Key{Down: true, Keysym: 'a'}); !errors.Is(err, ErrNotRunning) {
		t.Errorf("Input after Run = %v, want ErrNotRunning", err)
	}
	b.Demand(backend.Demand{Visible: true}) // must not block or panic
}

func TestInputDuringPrompt(t *testing.T) {
	checkGoroutines(t)
	srv := newServer(t, serverOptions{types: []byte{secVNCAuth}, password: "hunter2"})
	b := newBackend(t, Config{Addr: srv.addr})
	typed := make(chan struct{})
	sink := &backendtest.Sink{Answer: func(backend.Prompt) (backend.PromptReply, error) {
		<-typed
		return backend.PromptReply{Answers: []string{"hunter2"}}, nil
	}}
	start(t, b, sink)
	waitFor(t, "the prompt", func() bool { return len(sink.Prompts()) == 1 })
	if err := b.Input(backend.Key{Down: true, Keysym: 'x'}); err != nil {
		t.Fatal(err)
	}
	close(typed)
	c := srv.accept(t)
	if m := expect[keyMsg](t, c); m != (keyMsg{true, 'x'}) {
		t.Errorf("key %+v, want x pressed", m)
	}
}

func TestInputFull(t *testing.T) {
	checkGoroutines(t)
	// The server never speaks, so the handshake never ends and nothing
	// queued is written.
	addr := listen(t, func(nc net.Conn) { io.Copy(io.Discard, nc) })
	b := newBackend(t, Config{Addr: addr})
	r := start(t, b, &backendtest.Sink{})
	key := backend.Key{Down: true, Keysym: 'a'}
	// The first input that Run takes is the first in the queue.
	waitFor(t, "Run", func() bool { return !errors.Is(b.Input(key), ErrNotRunning) })
	for range inputQueue - 1 {
		if err := b.Input(key); err != nil {
			t.Fatalf("Input within the queue = %v", err)
		}
	}
	if err := b.Input(key); !errors.Is(err, ErrInputFull) {
		t.Errorf("Input beyond the queue = %v, want ErrInputFull", err)
	}
	if err := r.stop(t); !errors.Is(err, context.Canceled) {
		t.Errorf("Run = %v, want context.Canceled", err)
	}
}

// megabyte is a clipboard input of 1 MiB, which is 8 bytes more as cut
// text.
var megabyte = backend.Clipboard{MIME: "text/plain", Data: bytes.Repeat([]byte("x"), 1<<20)}

func TestInputBytes(t *testing.T) {
	checkGoroutines(t)
	// The server never speaks, so nothing queued is written: the queue
	// fills by its bytes long before its count.
	addr := listen(t, func(nc net.Conn) { io.Copy(io.Discard, nc) })
	b := newBackend(t, Config{Addr: addr})
	r := start(t, b, &backendtest.Sink{})
	waitFor(t, "Run", func() bool { return !errors.Is(b.Input(megabyte), ErrNotRunning) })
	accepted := 1
	var err error
	for ; accepted < inputQueue; accepted++ {
		if err = b.Input(megabyte); err != nil {
			break
		}
	}
	if want := inputBytes / (1<<20 + 8); !errors.Is(err, ErrInputFull) || accepted != want {
		t.Errorf("%d inputs of 1 MiB accepted, then %v; want %d, then ErrInputFull", accepted, err, want)
	}
	// A key still fits; a Text of 600,000 characters, 9.6 MB of key
	// events, never does.
	if err := b.Input(backend.Key{Down: true, Keysym: 'a'}); err != nil {
		t.Errorf("Input(Key) = %v", err)
	}
	if err := r.stop(t); !errors.Is(err, context.Canceled) {
		t.Errorf("Run = %v, want context.Canceled", err)
	}
}

func TestInputBytesDrain(t *testing.T) {
	checkGoroutines(t)
	s := connect(t, serverOptions{}, false)
	if err := s.b.Input(backend.Text{Text: strings.Repeat("a", 600_000)}); !errors.Is(err, ErrInputFull) {
		t.Errorf("Input(Text of 600,000 characters) = %v, want ErrInputFull", err)
	}
	// Written input leaves the budget: three times its size goes through
	// once the server reads it.
	for i := range 3 * inputBytes / (1 << 20) {
		if err := s.b.Input(megabyte); err != nil {
			t.Fatalf("input %d of 1 MiB = %v", i+1, err)
		}
		if m := expect[cutTextMsg](t, s.c); len(m.text) != 1<<20 {
			t.Fatalf("cut text of %d bytes, want 1 MiB", len(m.text))
		}
	}
}

func TestDemand(t *testing.T) {
	checkGoroutines(t)
	s := connect(t, serverOptions{}, false)
	img := pattern(64, 48, 0)

	// Nobody can see: nothing is asked for.
	s.c.quiet(t, silence)

	s.b.Demand(backend.Demand{Visible: true})
	s.fullScreen(t, img)
	// One request is outstanding, however often Demand repeats itself.
	s.b.Demand(backend.Demand{Visible: true})
	s.c.quiet(t, silence)

	// The update that was asked for is applied after the viewers have
	// gone, and nothing more is asked for.
	s.b.Demand(backend.Demand{Visible: false})
	patch := pattern(64, 48, 50)
	r := image.Rect(0, 0, 8, 8)
	s.c.send(t, putUpdate(putRaw(s.c.pf, patch, r)))
	waitFor(t, "the update", func() bool { return s.sink.Pixels().RGBAAt(0, 0) == patch.RGBAAt(0, 0) })
	s.c.quiet(t, silence)

	// So is an update nobody asked for.
	s.c.send(t, putUpdate(putRaw(s.c.pf, patch, image.Rect(8, 0, 16, 8))))
	waitFor(t, "the unasked update", func() bool { return s.sink.Pixels().RGBAAt(8, 0) == patch.RGBAAt(8, 0) })
	s.c.quiet(t, silence)

	// A viewer is back: the changes since are asked for.
	s.b.Demand(backend.Demand{Visible: true})
	if req := expect[requestMsg](t, s.c); !req.incremental || req.r != img.Rect {
		t.Errorf("request %+v, want changes to the whole screen", req)
	}
	s.c.quiet(t, silence)
}

func TestRequestPerUpdate(t *testing.T) {
	// RFB cannot tell which request an update answers, so the bound is
	// one request per update applied, and none without one: a server
	// that answers one request with several updates gets at most as many
	// requests back.
	checkGoroutines(t)
	s := connect(t, serverOptions{}, true)
	img := pattern(64, 48, 0)
	s.fullScreen(t, img)
	const updates = 5
	var p []byte
	for i := range updates {
		p = append(p, putUpdate(putRaw(s.c.pf, img, image.Rect(i, 0, i+1, 1)))...)
	}
	s.c.send(t, p)
	requests := 0
	for done := false; !done; {
		select {
		case m := <-s.c.msgs:
			if req, ok := m.(requestMsg); !ok || !req.incremental {
				t.Fatalf("client sent %T %+v, want an incremental request", m, m)
			}
			requests++
		case <-time.After(silence):
			done = true
		}
	}
	if requests < 1 || requests > updates {
		t.Errorf("%d requests for %d updates, want 1 to %d", requests, updates, updates)
	}
}

// slowSink takes a millisecond for each CopyRect, as a busy session
// might.
type slowSink struct{ *backendtest.Sink }

func (s slowSink) CopyRect(dst image.Rectangle, src image.Point) {
	time.Sleep(time.Millisecond)
	s.Sink.CopyRect(dst, src)
}

func TestCancelMidUpdate(t *testing.T) {
	checkGoroutines(t)
	srv := newServer(t, serverOptions{})
	b := newBackend(t, Config{Addr: srv.addr})
	sink := slowSink{&backendtest.Sink{}}
	r := start(t, b, sink)
	c := srv.accept(t)
	// An update of 65535 copies, 1 MiB, which takes over a minute to
	// apply: what Run has read when it is cancelled is not applied.
	rects := make([][]byte, 0xffff)
	for i := range rects {
		rects[i] = putCopy(image.Rect(0, 0, 4, 4), image.Pt(i%8, 0))
	}
	go c.nc.Write(putUpdate(rects...)) // fails once Run has closed the connection
	waitFor(t, "copies", func() bool { return len(sink.Copies()) >= 50 })
	start := time.Now()
	if err := r.stop(t); !errors.Is(err, context.Canceled) {
		t.Errorf("Run = %v, want context.Canceled", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("Run returned %v after it was cancelled", d)
	}
}

func TestServerEnds(t *testing.T) {
	for _, tt := range []struct {
		name string
		send func(c *serverConn) []byte
		err  error // nil: a normal end
	}{
		{"between messages", func(*serverConn) []byte { return putBell() }, nil},
		{"within a message header", func(*serverConn) []byte { return []byte{msgFramebufferUpdate, 0} }, io.ErrUnexpectedEOF},
		{"within a rectangle header", func(*serverConn) []byte { return []byte{msgFramebufferUpdate, 0, 0, 1, 0, 0, 0} }, io.ErrUnexpectedEOF},
		{"within a raw rectangle", func(c *serverConn) []byte {
			p := putUpdate(putRaw(c.pf, pattern(64, 48, 0), image.Rect(0, 0, 64, 48)))
			return p[:len(p)/2]
		}, io.ErrUnexpectedEOF},
		{"within a copy-rect", func(*serverConn) []byte {
			p := putUpdate(putCopy(image.Rect(0, 0, 8, 8), image.Pt(1, 1)))
			return p[:len(p)-1]
		}, io.ErrUnexpectedEOF},
		{"within a cursor", func(c *serverConn) []byte {
			p := putUpdate(putCursor(c.pf, pattern(8, 8, 0), image.Pt(1, 1)))
			return p[:len(p)-3]
		}, io.ErrUnexpectedEOF},
		{"within cut text", func(*serverConn) []byte { p := putCutText("hello"); return p[:len(p)-2] }, io.ErrUnexpectedEOF},
		{"within a colour map", func(*serverConn) []byte { p := putColourMap(3); return p[:len(p)-2] }, io.ErrUnexpectedEOF},
	} {
		t.Run(tt.name, func(t *testing.T) {
			checkGoroutines(t)
			s := connect(t, serverOptions{}, false)
			s.c.send(t, tt.send(s.c))
			s.c.nc.Close()
			err := s.run.wait(t)
			if tt.err == nil && err != nil || tt.err != nil && !errors.Is(err, tt.err) {
				t.Errorf("Run = %v, want %v", err, tt.err)
			}
		})
	}
}

func TestProtocolErrors(t *testing.T) {
	for _, tt := range []struct {
		name string
		msg  func(c *serverConn) []byte
		want string // in the error
	}{
		{"unknown message", func(*serverConn) []byte { return []byte{99} }, "unknown message type 99"},
		{"unknown encoding", func(*serverConn) []byte {
			return putUpdate(rectHeader(image.Rect(0, 0, 8, 8), 5)) // Hextile
		}, "unsupported encoding 5"},
		{"raw beyond the screen", func(c *serverConn) []byte {
			return putUpdate(putRaw(c.pf, pattern(80, 80, 0), image.Rect(60, 0, 70, 4)))
		}, "outside the screen"},
		{"raw of the largest size", func(*serverConn) []byte {
			return putUpdate(rectHeader(image.Rect(0, 0, 0xffff, 0xffff), encRaw))
		}, "outside the screen"},
		{"copy-rect source beyond the screen", func(*serverConn) []byte {
			return putUpdate(putCopy(image.Rect(0, 0, 10, 10), image.Pt(60, 0)))
		}, "copy-rect source"},
		{"copy-rect destination beyond the screen", func(*serverConn) []byte {
			return putUpdate(putCopy(image.Rect(0, 40, 10, 50), image.Pt(0, 0)))
		}, "copy-rect destination"},
		{"empty desktop size", func(*serverConn) []byte { return putUpdate(putDesktopSize(0, 10)) }, "empty screen"},
		{"huge desktop size", func(*serverConn) []byte { return putUpdate(putDesktopSize(0xffff, 0xffff)) }, "exceeds the limits"},
		{"many desktop sizes in one update", func(*serverConn) []byte {
			rects := make([][]byte, 200)
			for i := range rects {
				rects[i] = putDesktopSize(640+i%2, 480)
			}
			return putUpdate(rects...)
		}, "rectangle 2 of 200: a second DesktopSize"},
		{"huge cursor", func(*serverConn) []byte {
			return putUpdate(rectHeader(image.Rect(0, 0, 300, 2), encCursor))
		}, "cursor of 300×2"},
		{"extended clipboard", func(*serverConn) []byte {
			return []byte{msgServerCutText, 0, 0, 0, 0xff, 0xff, 0xff, 0xf0}
		}, "cut text of -16 bytes"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			checkGoroutines(t)
			s := connect(t, serverOptions{}, false)
			s.c.send(t, tt.msg(s.c))
			err := s.run.wait(t)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Run = %v, want an error with %q", err, tt.want)
			}
		})
	}
}

func TestBellAndColourMap(t *testing.T) {
	checkGoroutines(t)
	s := connect(t, serverOptions{}, false)
	s.c.send(t, putBell(), putColourMap(256), putBell())
	waitFor(t, "two bells", func() bool { return s.sink.Bells() == 2 })
}

func TestDemandBeforeRun(t *testing.T) {
	checkGoroutines(t)
	s := connect(t, serverOptions{}, true)
	if req := expect[requestMsg](t, s.c); req.incremental || req.r != image.Rect(0, 0, 64, 48) {
		t.Errorf("first request %+v, want the whole screen", req)
	}
}

func TestCancelDuringPrompt(t *testing.T) {
	checkGoroutines(t)
	srv := newServer(t, serverOptions{types: []byte{secVNCAuth}, password: "hunter2"})
	release := make(chan struct{})
	sink := &backendtest.Sink{Answer: func(backend.Prompt) (backend.PromptReply, error) {
		<-release
		return backend.PromptReply{}, errors.New("the owner left")
	}}
	r := start(t, newBackend(t, Config{Addr: srv.addr}), sink)
	waitFor(t, "the prompt", func() bool { return len(sink.Prompts()) == 1 })
	r.cancel()
	close(release)
	if err := r.wait(t); !errors.Is(err, context.Canceled) {
		t.Errorf("Run = %v, want context.Canceled", err)
	}
}

func TestRunTwice(t *testing.T) {
	checkGoroutines(t)
	s := connect(t, serverOptions{}, false)
	if err := s.b.Run(context.Background(), &backendtest.Sink{}); err == nil || !strings.Contains(err.Error(), "while running") {
		t.Errorf("second Run = %v, want an error", err)
	}
	if err := s.run.stop(t); !errors.Is(err, context.Canceled) {
		t.Errorf("Run = %v, want context.Canceled", err)
	}
}
