// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package rdp

import (
	"compress/gzip"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"io"
	"log/slog"
	"net"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"changkun.de/x/occamy/internal/backend"
	"changkun.de/x/occamy/internal/backend/backendtest"
	"changkun.de/x/occamy/internal/rdp"
	"changkun.de/x/occamy/internal/rdp/rdptest"
)

// The tests run the backend against rdptest's fake server on the
// loopback interface, which runs the connection sequence over TLS with
// a certificate the backend pins, and then sends what a test gives it
// and reads what the backend sends.

// timeout bounds every wait in these tests.
const timeout = 10 * time.Second

// silence is how long a test waits to see that the client sends nothing.
const silence = 100 * time.Millisecond

var quiet = slog.New(slog.DiscardHandler)

// fakeServer accepts connections with an rdptest.Server.
type fakeServer struct {
	ln    net.Listener
	conns chan accepted
}

type accepted struct {
	c   *rdptest.Conn
	err error
}

// listen starts a fake server that runs srv on each connection.
func listen(t *testing.T, srv rdptest.Server) *fakeServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &fakeServer{ln: ln, conns: make(chan accepted, 4)}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				c, err := srv.Accept(nc)
				if err != nil && !errors.Is(err, rdptest.ErrCredSSP) {
					nc.Close()
				}
				s.conns <- accepted{c, err}
			}()
		}
	}()
	t.Cleanup(func() {
		ln.Close()
		<-done
	})
	return s
}

// accept returns the next connection whose connection sequence ended.
func (s *fakeServer) accept(t *testing.T) *rdptest.Conn {
	t.Helper()
	a := s.next(t)
	if a.err != nil {
		t.Fatalf("connection sequence: %v", a.err)
	}
	t.Cleanup(func() { a.c.Close() })
	return a.c
}

// next returns the next connection, whether its sequence ended or not.
func (s *fakeServer) next(t *testing.T) accepted {
	t.Helper()
	select {
	case a := <-s.conns:
		return a
	case <-time.After(timeout):
		t.Fatal("no connection")
	}
	return accepted{}
}

// nextOf returns the next connection, as next does, for a Run that
// cannot end before the server has it, and fails the test at once if
// Run ends first, as when it could not connect.
func (s *fakeServer) nextOf(t *testing.T, r *runner) accepted {
	t.Helper()
	select {
	case a := <-s.conns:
		return a
	case <-r.done:
		t.Fatalf("Run returned %v before the server had a connection", r.err)
	case <-time.After(timeout):
		t.Fatal("no connection")
	}
	return accepted{}
}

// config returns a configuration for the fake server: TLS without NLA,
// its certificate pinned.
func (s *fakeServer) config(t *testing.T) Config {
	t.Helper()
	_, fp, err := rdptest.Certificate()
	if err != nil {
		t.Fatal(err)
	}
	return Config{
		Addr:     s.ln.Addr().String(),
		Username: "occamy", Password: "s3cret",
		Security: SecurityTLS,
		TLS:      rdp.TLS{Pins: []string{fp}},
		Logger:   quiet,
	}
}

// newBackend returns a backend for cfg.
func newBackend(t *testing.T, cfg Config) *Backend {
	t.Helper()
	b, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return b
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

// ownFrame reports whether a goroutine's stack runs code of this package
// or of internal/rdp, not of tests or of the fake server. The location
// after "created by" is the creator's.
func ownFrame(g string) bool {
	lines := strings.Split(g, "\n")
	for i := 0; i < len(lines); i++ {
		l := lines[i]
		if strings.HasPrefix(l, "created by ") {
			i++
			continue
		}
		if !strings.Contains(l, ".go:") || strings.Contains(l, "_test.go:") || strings.Contains(l, "/rdptest/") {
			continue
		}
		if strings.Contains(l, "/internal/backend/rdp/") || strings.Contains(l, "/internal/rdp/") {
			return true
		}
	}
	return false
}

// checkStopped fails the test if a goroutine is still running code of
// the backend or the client, which must not outlive Run.
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

// waitFor polls cond until it holds or timeout passes.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(timeout); !cond(); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

// next returns the next message from the backend that is not empty.
func next(t *testing.T, c *rdptest.Conn) rdptest.Message {
	t.Helper()
	c.SetReadDeadline(time.Now().Add(timeout))
	for {
		m, err := c.Next()
		if err != nil {
			t.Fatalf("reading from the backend: %v", err)
		}
		if len(m.Events) > 0 || m.Type2 != 0 {
			return m
		}
	}
}

// events reads input messages until it has n events, skipping Suppress
// Output PDUs.
func events(t *testing.T, c *rdptest.Conn, n int) []rdptest.Event {
	t.Helper()
	var evs []rdptest.Event
	for len(evs) < n {
		m := next(t, c)
		if m.Type2 == rdptest.PDU2SuppressOutput {
			continue
		}
		if m.Type2 != 0 && m.Type2 != rdptest.PDU2Input {
			t.Fatalf("share data PDU %#x, want input", m.Type2)
		}
		evs = append(evs, m.Events...)
	}
	if len(evs) != n {
		t.Fatalf("%d events, want %d: %+v", len(evs), n, evs)
	}
	return evs
}

// quietFor checks that the backend sends nothing but Suppress Output
// PDUs for silence.
func quietFor(t *testing.T, c *rdptest.Conn) {
	t.Helper()
	c.SetReadDeadline(time.Now().Add(silence))
	for {
		m, err := c.Next()
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			return
		}
		if err != nil {
			t.Fatalf("reading from the backend: %v", err)
		}
		if len(m.Events) > 0 {
			t.Fatalf("the backend sent %+v", m.Events)
		}
	}
}

// solid returns a w×h image of one colour.
func solid(w, h int, c color.RGBA) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = c.R, c.G, c.B, c.A
	}
	return img
}

// sinkAt returns the sink's pixel at p.
func sinkAt(sink *backendtest.Sink, p image.Point) color.RGBA {
	img := sink.Pixels()
	if img == nil {
		return color.RGBA{}
	}
	return img.RGBAAt(p.X, p.Y)
}

// loadTrace reads one of internal/rdp's recordings of xrdp: frames, each
// after its length in four bytes, big-endian.
func loadTrace(t *testing.T, name string) [][]byte {
	t.Helper()
	f, err := os.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	var frames [][]byte
	for len(b) >= 4 {
		n := int(binary.BigEndian.Uint32(b))
		if 4+n > len(b) {
			t.Fatalf("%s: truncated", name)
		}
		frames = append(frames, b[4:4+n])
		b = b[4+n:]
	}
	return frames
}

// wallpaper is the scene's background in row y of a screen h pixels high,
// as bench/scene's newWallpaper draws it.
func wallpaper(y, h int) color.RGBA {
	top, bottom := color.RGBA{0x1f, 0x3b, 0x5a, 0xff}, color.RGBA{0x3c, 0x6e, 0x8f, 0xff}
	t := float64(y) / float64(max(1, h-1))
	f := func(a, b uint8) uint8 { return uint8(float64(a) + (float64(b)-float64(a))*t) }
	return color.RGBA{f(top.R, bottom.R), f(top.G, bottom.G), f(top.B, bottom.B), 0xff}
}

// matchesWallpaper counts the pixels outside the swatch that differ from
// the scene's wallpaper by more than tol in a channel.
func matchesWallpaper(img *image.RGBA, tol int) (bad int, first image.Point) {
	if img == nil {
		return -1, image.Point{}
	}
	w, h := img.Rect.Dx(), img.Rect.Dy()
	swatch := image.Rect(16, 16, 48, 48)
	near := func(a, b uint8) bool { d := int(a) - int(b); return -tol <= d && d <= tol }
	for y := range h {
		want := wallpaper(y, h)
		for x := range w {
			if image.Pt(x, y).In(swatch) {
				continue
			}
			got := img.RGBAAt(x, y)
			if !near(got.R, want.R) || !near(got.G, want.G) || !near(got.B, want.B) {
				if bad == 0 {
					first = image.Pt(x, y)
				}
				bad++
			}
		}
	}
	return bad, first
}
