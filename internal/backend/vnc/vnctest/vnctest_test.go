// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package vnctest_test

import (
	"context"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"testing"
	"time"

	"changkun.de/x/occamy/internal/backend"
	"changkun.de/x/occamy/internal/backend/backendtest"
	"changkun.de/x/occamy/internal/backend/vnc"
	"changkun.de/x/occamy/internal/backend/vnc/vnctest"
)

const timeout = 5 * time.Second

// run runs b against sink until the test ends, and returns Run's error.
func run(t *testing.T, b *vnc.Backend, sink backend.Sink) <-chan error {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	returned := make(chan struct{})
	go func() {
		defer close(returned)
		done <- b.Run(ctx, sink)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-returned:
		case <-time.After(timeout):
			t.Error("Run did not return")
		}
	})
	return done
}

func newServer(t *testing.T, opts vnctest.Options) *vnctest.Server {
	t.Helper()
	s, err := vnctest.NewServer(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func accept(t *testing.T, s *vnctest.Server) *vnctest.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	c, err := s.Accept(ctx)
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	return c
}

// next returns the client's next message of type M, skipping others.
func next[M vnctest.Message](t *testing.T, c *vnctest.Conn) M {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	for {
		m, err := c.Next(ctx)
		if err != nil {
			var zero M
			t.Fatalf("waiting for a %T: %v", zero, err)
		}
		if m, ok := m.(M); ok {
			return m
		}
	}
}

// eventually waits for cond.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func fill(r image.Rectangle, c color.RGBA) *image.RGBA {
	img := image.NewRGBA(r)
	draw.Draw(img, r, &image.Uniform{c}, image.Point{}, draw.Src)
	return img
}

func newBackend(t *testing.T, cfg vnc.Config) *vnc.Backend {
	t.Helper()
	b, err := vnc.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	b.Demand(backend.Demand{Visible: true})
	return b
}

func TestSession(t *testing.T) {
	srv := newServer(t, vnctest.Options{Size: image.Pt(40, 30), Name: "desk", Password: "secret"})
	sink := &backendtest.Sink{}
	b := newBackend(t, vnc.Config{Addr: srv.Addr(), Password: "secret"})
	run(t, b, sink)
	c := accept(t, srv)

	// The screen arrives, in the pixel format the client asked for.
	red := color.RGBA{R: 0xff, A: 0xff}
	if err := c.Draw(fill(image.Rect(10, 10, 20, 20), red)); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the screen", func() bool {
		px := sink.Pixels()
		return px != nil && px.Rect.Size() == image.Pt(40, 30) && px.RGBAAt(15, 15) == red && px.RGBAAt(5, 5) == color.RGBA{A: 0xff}
	})

	// Input reaches the server.
	if err := b.Input(backend.Key{Down: true, Keysym: 'a'}); err != nil {
		t.Fatal(err)
	}
	if k := next[vnctest.KeyEvent](t, c); !k.Down || k.Keysym != 'a' {
		t.Errorf("key %+v", k)
	}
	if err := b.Input(backend.Pointer{X: 7, Y: 9, Buttons: backend.ButtonLeft}); err != nil {
		t.Fatal(err)
	}
	if p := next[vnctest.PointerEvent](t, c); p.At != image.Pt(7, 9) || p.Buttons != 1 {
		t.Errorf("pointer %+v", p)
	}

	// The clipboard, both ways.
	if err := b.Input(backend.Clipboard{MIME: "text/plain", Data: []byte("café")}); err != nil {
		t.Fatal(err)
	}
	if ct := next[vnctest.CutText](t, c); ct.Text != "café" {
		t.Errorf("cut text %q", ct.Text)
	}
	if err := c.CutText("naïve"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the server's clipboard", func() bool {
		_, data := sink.ClipboardContent()
		return string(data) == "naïve"
	})

	// The cursor.
	cur := fill(image.Rect(0, 0, 4, 4), color.RGBA{B: 0xff, A: 0xff})
	if err := c.Cursor(cur, image.Pt(1, 2)); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the cursor", func() bool {
		img, hot := sink.CursorImage()
		return img != nil && img.Rect.Size() == image.Pt(4, 4) && hot == image.Pt(1, 2)
	})
}

func TestResize(t *testing.T) {
	srv := newServer(t, vnctest.Options{ExtendedDesktopSize: true})
	sink := &backendtest.Sink{}
	b := newBackend(t, vnc.Config{Addr: srv.Addr(), Width: 80, Height: 60})
	run(t, b, sink)
	c := accept(t, srv)

	// The configured size is asked for once the server has shown it can
	// resize.
	if m := next[vnctest.SetDesktopSize](t, c); m.Size != image.Pt(80, 60) {
		t.Errorf("SetDesktopSize %v", m.Size)
	}
	eventually(t, "the configured size", func() bool { return sink.Size() == image.Pt(80, 60) })

	if err := b.Input(backend.Resize{Width: 100, Height: 50}); err != nil {
		t.Fatal(err)
	}
	if m := next[vnctest.SetDesktopSize](t, c); m.Size != image.Pt(100, 50) {
		t.Errorf("SetDesktopSize %v", m.Size)
	}
	eventually(t, "the resize", func() bool { return sink.Size() == image.Pt(100, 50) && c.Size() == image.Pt(100, 50) })

	// The server resizes on its own.
	if err := c.Resize(image.Pt(30, 20)); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the server's resize", func() bool { return sink.Size() == image.Pt(30, 20) })
}

func TestServerWithoutExtendedDesktopSize(t *testing.T) {
	srv := newServer(t, vnctest.Options{})
	sink := &backendtest.Sink{}
	b := newBackend(t, vnc.Config{Addr: srv.Addr(), Width: 80, Height: 60})
	run(t, b, sink)
	c := accept(t, srv)
	next[vnctest.UpdateRequest](t, c)
	eventually(t, "the screen", func() bool { return sink.Size() == image.Pt(64, 48) })
	// A server resize goes out as DesktopSize.
	if err := c.Resize(image.Pt(32, 24)); err != nil {
		t.Fatal(err)
	}
	eventually(t, "DesktopSize", func() bool { return sink.Size() == image.Pt(32, 24) })
}

func TestAuthFailure(t *testing.T) {
	srv := newServer(t, vnctest.Options{Password: "secret"})
	b := newBackend(t, vnc.Config{Addr: srv.Addr(), Password: "wrong"})
	select {
	case err := <-run(t, b, &backendtest.Sink{}):
		if !errors.Is(err, vnc.ErrAuthFailed) {
			t.Errorf("Run = %v, want ErrAuthFailed", err)
		}
	case <-time.After(timeout):
		t.Fatal("Run did not fail")
	}
	if n, c := srv.Failures(), srv.Credentials(); n != 1 || c != 1 {
		t.Errorf("%d failures of %d credentials, want 1 of 1", n, c)
	}
}

func TestPrompt(t *testing.T) {
	srv := newServer(t, vnctest.Options{Password: "secret"})
	sink := &backendtest.Sink{Answer: func(p backend.Prompt) (backend.PromptReply, error) {
		return backend.PromptReply{Answers: []string{"secret"}}, nil
	}}
	run(t, newBackend(t, vnc.Config{Addr: srv.Addr()}), sink)
	accept(t, srv)
	if n := len(sink.Prompts()); n != 1 {
		t.Errorf("%d prompts, want 1", n)
	}
}

func TestServerEnds(t *testing.T) {
	srv := newServer(t, vnctest.Options{})
	done := run(t, newBackend(t, vnc.Config{Addr: srv.Addr()}), &backendtest.Sink{})
	c := accept(t, srv)
	next[vnctest.UpdateRequest](t, c)
	c.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run = %v after the server ended the session, want nil", err)
		}
	case <-time.After(timeout):
		t.Fatal("Run did not return")
	}
}

func TestVeNCrypt(t *testing.T) {
	cert, err := vnctest.SelfSigned("127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name               string
		username, password string
	}{
		{"X509None", "", ""},
		{"X509Vnc", "", "secret"},
		{"X509Plain", "alice", "secret"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv := newServer(t, vnctest.Options{Certificate: &cert.TLS, Username: tt.username, Password: tt.password})
			sink := &backendtest.Sink{}
			b := newBackend(t, vnc.Config{Addr: srv.Addr(), Username: tt.username, Password: tt.password, TLS: vnc.TLS{Pins: []string{cert.Fingerprint}}})
			run(t, b, sink)
			c := accept(t, srv)
			if err := c.Draw(fill(image.Rect(0, 0, 8, 8), color.RGBA{G: 0xff, A: 0xff})); err != nil {
				t.Fatal(err)
			}
			eventually(t, "the screen", func() bool {
				px := sink.Pixels()
				return px != nil && px.RGBAAt(4, 4) == color.RGBA{G: 0xff, A: 0xff}
			})
		})
	}

	// The system's roots do not know the certificate, so no credential
	// is sent.
	srv := newServer(t, vnctest.Options{Certificate: &cert.TLS, Password: "secret"})
	select {
	case err := <-run(t, newBackend(t, vnc.Config{Addr: srv.Addr()}), &backendtest.Sink{}):
		var ce *vnc.CertificateError
		if !errors.As(err, &ce) || ce.Fingerprint == "" {
			t.Errorf("Run = %v, want a CertificateError", err)
		}
	case <-time.After(timeout):
		t.Fatal("Run did not fail")
	}
	if n := srv.Credentials(); n != 0 {
		t.Errorf("%d credentials sent to a server whose certificate was refused", n)
	}
}
