// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package server

import (
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"changkun.de/x/occamy/client"
	"changkun.de/x/occamy/internal/backend/vnc/vnctest"
	"changkun.de/x/occamy/internal/config"
	"changkun.de/x/occamy/protocol"
)

// These tests run the native VNC backend end to end: a client signs in,
// opens or streams a connection with backend: native through occamyd's
// routes, and drives a fake VNC server through it.

const vncTimeout = 10 * time.Second

func newVNCServer(t *testing.T, opts vnctest.Options) *vnctest.Server {
	t.Helper()
	s, err := vnctest.NewServer(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// vncConnection is a connection for alice to the VNC server at addr,
// served by the native backend.
func vncConnection(id, addr string, creds config.Credentials) config.Connection {
	return config.Connection{
		ID: id, Name: id, Protocol: "vnc", Backend: config.BackendNative, Target: addr,
		Credentials: creds, Allow: config.Allow{Users: []string{"alice"}},
	}
}

// passwordFile stores password in a file for a connection's credentials.
func passwordFile(t *testing.T, password string) config.Credentials {
	t.Helper()
	p := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(p, []byte(password+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return config.Credentials{PasswordFile: p}
}

// vncTestServer is a test server with conns and no other connection,
// and alice's access token.
func vncTestServer(t *testing.T, edit func(*config.File), conns ...config.Connection) (*testServer, string) {
	t.Helper()
	ts := newTestServer(t, func(f *config.File) {
		f.Connections = conns
		if edit != nil {
			edit(f)
		}
	})
	return ts, ts.token(t, "alice", "ops")
}

// streamConnection streams connection id with token.
func (ts *testServer) streamConnection(t *testing.T, token, id string, opts client.Options) *client.Session {
	t.Helper()
	return ts.dialStream(t, "/api/v1/connections/"+url.PathEscape(id)+"/stream", token, opts)
}

func (ts *testServer) dialStream(t *testing.T, path, token string, opts client.Options) *client.Session {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), vncTimeout)
	defer cancel()
	if opts.Name == "" {
		opts.Name = "occamy-test/1"
	}
	cl, err := client.Dial(ctx, ts.srv.URL+path, token, opts)
	if err != nil {
		t.Fatalf("dialling %s: %v", path, err)
	}
	t.Cleanup(func() { cl.Close() })
	return cl
}

// acceptVNC returns the next client of the fake VNC server.
func acceptVNC(t *testing.T, s *vnctest.Server) *vnctest.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), vncTimeout)
	defer cancel()
	c, err := s.Accept(ctx)
	if err != nil {
		t.Fatalf("the backend did not connect: %v", err)
	}
	return c
}

// nextVNC returns the backend's next message to the fake server of type
// M, skipping the others.
func nextVNC[M vnctest.Message](t *testing.T, c *vnctest.Conn) M {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), vncTimeout)
	defer cancel()
	for {
		m, err := c.Next(ctx)
		if err != nil {
			var zero M
			t.Fatalf("waiting for the backend's %T: %v", zero, err)
		}
		if m, ok := m.(M); ok {
			return m
		}
	}
}

// awaitEvent reads cl's events until ok accepts one, which it returns.
// The session's end fails the test unless ok accepts it.
func awaitEvent(t *testing.T, cl *client.Session, what string, ok func(client.Event) bool) client.Event {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), vncTimeout)
	defer cancel()
	for {
		ev, err := cl.Next(ctx)
		if err != nil {
			t.Fatalf("waiting for %s: %v", what, err)
		}
		if ok(ev) {
			return ev
		}
		if e, end := ev.(client.End); end {
			t.Fatalf("waiting for %s: the session ended: %+v", what, e)
		}
	}
}

// awaitEnd waits for the session's end, and checks its code.
func awaitEnd(t *testing.T, cl *client.Session, code protocol.CloseCode) client.End {
	t.Helper()
	e := awaitEvent(t, cl, "the end", func(ev client.Event) bool {
		_, ok := ev.(client.End)
		return ok
	}).(client.End)
	if e.Code != code {
		t.Errorf("the session ended with %v %q (%v), want %v", e.Code, e.Reason, e.Err, code)
	}
	return e
}

// awaitPixel waits for a frame that shows want at p.
func awaitPixel(t *testing.T, cl *client.Session, p image.Point, want color.RGBA) {
	t.Helper()
	awaitEvent(t, cl, "the screen", func(ev client.Event) bool {
		if _, ok := ev.(client.Frame); !ok {
			return false
		}
		img := cl.Screenshot()
		return img != nil && p.In(img.Rect) && img.RGBAAt(p.X, p.Y) == want
	})
}

func uniform(r image.Rectangle, c color.RGBA) *image.RGBA {
	img := image.NewRGBA(r)
	draw.Draw(img, r, &image.Uniform{c}, image.Point{}, draw.Src)
	return img
}

var (
	red   = color.RGBA{R: 0xff, A: 0xff}
	green = color.RGBA{G: 0xff, A: 0xff}
)

// TestVNCEndToEnd streams a vnc connection with backend: native and a
// stored password: the screen arrives at the size of the client's
// hello, the client's input, resizes and clipboard reach the server, the
// server's clipboard and cursor reach the client, and the server's end
// of the session ends the stream with remote-ended.
func TestVNCEndToEnd(t *testing.T) {
	vs := newVNCServer(t, vnctest.Options{Name: "desk", Password: "secret", ExtendedDesktopSize: true})
	ts, alice := vncTestServer(t, nil, vncConnection("desk", vs.Addr(), passwordFile(t, "secret")))
	ctx, cancel := context.WithTimeout(context.Background(), vncTimeout)
	defer cancel()

	cl := ts.streamConnection(t, alice, "desk", client.Options{Width: 80, Height: 60})
	if cl.Role() != protocol.RoleOwner || cl.Class() != protocol.ClassGraphical {
		t.Fatalf("role %v, class %v", cl.Role(), cl.Class())
	}
	c := acceptVNC(t, vs)

	// The hello's size is asked for once the server shows it can resize.
	if m := nextVNC[vnctest.SetDesktopSize](t, c); m.Size != image.Pt(80, 60) {
		t.Errorf("SetDesktopSize %v, want the hello's 80×60", m.Size)
	}
	if err := c.Draw(uniform(image.Rect(0, 0, 80, 60), red)); err != nil {
		t.Fatal(err)
	}
	awaitPixel(t, cl, image.Pt(79, 59), red)
	if got := cl.Size(); got != image.Pt(80, 60) {
		t.Errorf("client screen %v, want 80×60", got)
	}
	r := ts.api(t, http.MethodGet, "/api/v1/sessions/"+cl.SessionID(), alice, "")
	var state sessionStateResponse
	if err := json.Unmarshal(r.body, &state); err != nil || state.Backend != "vnc" || state.Protocol != "vnc" || state.Size == nil || *state.Size != (sessionSize{80, 60}) {
		t.Errorf("state %d %s", r.code, r.body)
	}

	// Input.
	if _, err := cl.Key(ctx, true, 'a', 0); err != nil {
		t.Fatal(err)
	}
	if k := nextVNC[vnctest.KeyEvent](t, c); !k.Down || k.Keysym != 'a' {
		t.Errorf("key %+v", k)
	}
	if _, err := cl.Pointer(ctx, 30, 20, protocol.ButtonLeft); err != nil {
		t.Fatal(err)
	}
	if p := nextVNC[vnctest.PointerEvent](t, c); p.At != image.Pt(30, 20) || p.Buttons != 1 {
		t.Errorf("pointer %+v", p)
	}
	if _, err := cl.Text(ctx, "é"); err != nil {
		t.Fatal(err)
	}
	if k := nextVNC[vnctest.KeyEvent](t, c); !k.Down || k.Keysym != 0xe9 {
		t.Errorf("text's key %+v, want é's keysym pressed", k)
	}

	// A resize, which the server answers with ExtendedDesktopSize.
	if _, err := cl.Resize(ctx, 100, 50); err != nil {
		t.Fatal(err)
	}
	if m := nextVNC[vnctest.SetDesktopSize](t, c); m.Size != image.Pt(100, 50) {
		t.Errorf("SetDesktopSize %v, want 100×50", m.Size)
	}
	awaitEvent(t, cl, "the resize", func(ev client.Event) bool { return ev == client.Resize{Width: 100, Height: 50} })
	if err := c.Draw(uniform(image.Rect(90, 40, 100, 50), green)); err != nil {
		t.Fatal(err)
	}
	awaitPixel(t, cl, image.Pt(99, 49), green)

	// The clipboard, both ways, in Latin-1 on the wire.
	if _, err := cl.Clipboard(ctx, "text/plain", []byte("café")); err != nil {
		t.Fatal(err)
	}
	if ct := nextVNC[vnctest.CutText](t, c); ct.Text != "café" {
		t.Errorf("the server's clipboard %q, want café", ct.Text)
	}
	if err := c.CutText("naïve"); err != nil {
		t.Fatal(err)
	}
	ev := awaitEvent(t, cl, "the clipboard", func(ev client.Event) bool { _, ok := ev.(client.Clipboard); return ok })
	if cb := ev.(client.Clipboard); string(cb.Data) != "naïve" || !strings.HasPrefix(cb.MIME, "text/plain") {
		t.Errorf("clipboard %q %q, want naïve", cb.MIME, cb.Data)
	}

	// The cursor.
	if err := c.Cursor(uniform(image.Rect(0, 0, 4, 4), green), image.Pt(1, 2)); err != nil {
		t.Fatal(err)
	}
	ev = awaitEvent(t, cl, "the cursor", func(ev client.Event) bool { _, ok := ev.(client.Cursor); return ok })
	if cur := ev.(client.Cursor); cur.Image == nil || cur.Image.Rect.Size() != image.Pt(4, 4) || cur.Hot != image.Pt(1, 2) {
		t.Errorf("cursor %+v", cur)
	}

	// The server ends the session.
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	awaitEnd(t, cl, protocol.CloseRemoteEnded)
}

// TestVNCSessionStream opens a session with POST, then streams it as a
// hidden owner: nothing is asked of the server until the client shows,
// and the owner's hello resizes the screen, as the connection stream's
// does. Closing the session ends the stream with session-closed.
func TestVNCSessionStream(t *testing.T) {
	vs := newVNCServer(t, vnctest.Options{Password: "secret", ExtendedDesktopSize: true})
	ts, alice := vncTestServer(t, nil, vncConnection("desk", vs.Addr(), passwordFile(t, "secret")))
	o := ts.openSession(t, alice, "desk")
	c := acceptVNC(t, vs)

	cl := ts.dialStream(t, "/api/v1/sessions/"+url.PathEscape(o.ID)+"/stream", o.StreamToken, client.Options{Width: 90, Height: 70, Hidden: true})
	// The handshake sets the pixel format and the encodings; nobody can
	// see the screen, so no update is asked for.
	quiet, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	for {
		m, err := c.Next(quiet)
		if errors.Is(err, context.DeadlineExceeded) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		switch m.(type) {
		case vnctest.SetPixelFormat, vnctest.SetEncodings:
		default:
			t.Fatalf("the backend sent %#v while nobody could see", m)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), vncTimeout)
	defer cancel()
	if err := cl.View(ctx, true, image.Rectangle{}); err != nil {
		t.Fatal(err)
	}
	if req := nextVNC[vnctest.UpdateRequest](t, c); req.Incremental {
		t.Errorf("first request %+v, want the whole screen", req)
	}
	if m := nextVNC[vnctest.SetDesktopSize](t, c); m.Size != image.Pt(90, 70) {
		t.Errorf("SetDesktopSize %v, want the owner's hello's 90×70", m.Size)
	}
	if err := c.Draw(uniform(image.Rect(0, 0, 90, 70), green)); err != nil {
		t.Fatal(err)
	}
	awaitPixel(t, cl, image.Pt(89, 69), green)

	if r := ts.api(t, http.MethodDelete, "/api/v1/sessions/"+o.ID, alice, ""); r.code != http.StatusNoContent {
		t.Fatalf("DELETE: %d %s", r.code, r.body)
	}
	awaitEnd(t, cl, protocol.CloseSessionClosed)
	// The backend hangs up.
	for {
		if _, err := c.Next(ctx); err != nil {
			if !errors.Is(err, io.EOF) {
				t.Errorf("the backend's connection ended with %v, want EOF", err)
			}
			break
		}
	}
}

// TestVNCPrompt asks the owner for the password that the connection
// does not store, and ends the session with backend-failed when the
// server refuses the answer.
func TestVNCPrompt(t *testing.T) {
	vs := newVNCServer(t, vnctest.Options{Password: "secret"})
	ts, alice := vncTestServer(t, nil, vncConnection("desk", vs.Addr(), config.Credentials{Prompt: true}))
	ctx, cancel := context.WithTimeout(context.Background(), vncTimeout)
	defer cancel()
	for _, answer := range []string{"secret", "wrong"} {
		cl := ts.streamConnection(t, alice, "desk", client.Options{})
		ev := awaitEvent(t, cl, "the prompt", func(ev client.Event) bool { _, ok := ev.(client.Prompt); return ok })
		p := ev.(client.Prompt)
		if len(p.Fields) != 1 || !p.Fields[0].Secret {
			t.Fatalf("prompt %+v, want one secret field", p)
		}
		if err := cl.PromptReply(ctx, p.ID, []string{answer}); err != nil {
			t.Fatal(err)
		}
		if answer == "wrong" {
			awaitEnd(t, cl, protocol.CloseBackendFailed)
			break
		}
		c := acceptVNC(t, vs)
		if err := c.Draw(uniform(image.Rect(0, 0, 8, 8), red)); err != nil {
			t.Fatal(err)
		}
		awaitPixel(t, cl, image.Pt(4, 4), red)
		cl.Close()
	}
	if n := vs.Failures(); n != 1 {
		t.Errorf("%d failed authentications, want 1", n)
	}
}

// TestVNCTLS checks a connection's tls policy against a server whose
// certificate signs itself: a pin accepts it, while the system's roots
// and another pin refuse it, which ends the session with backend-failed
// before any credential is sent. The server counts credentials whether
// they are right or not, and the refused connections store a wrong
// password too, so a credential that reached it would also fail.
func TestVNCTLS(t *testing.T) {
	cert, err := vnctest.SelfSigned("127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	other, err := vnctest.SelfSigned("127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	vs := newVNCServer(t, vnctest.Options{Certificate: &cert.TLS, Username: "alice", Password: "secret"})
	creds := passwordFile(t, "secret")
	creds.Username = "alice"
	wrong := passwordFile(t, "not-the-password")
	wrong.Username = "alice"
	pinned := vncConnection("pinned", vs.Addr(), creds)
	pinned.TLS = config.ConnectionTLS{Pins: []string{cert.Fingerprint}}
	roots := vncConnection("roots", vs.Addr(), wrong)
	roots.TLS = config.ConnectionTLS{Require: true}
	wrongPin := vncConnection("wrong-pin", vs.Addr(), wrong)
	wrongPin.TLS = config.ConnectionTLS{Pins: []string{other.Fingerprint}}
	ts, alice := vncTestServer(t, nil, pinned, roots, wrongPin)

	cl := ts.streamConnection(t, alice, "pinned", client.Options{})
	c := acceptVNC(t, vs)
	if err := c.Draw(uniform(image.Rect(0, 0, 8, 8), green)); err != nil {
		t.Fatal(err)
	}
	awaitPixel(t, cl, image.Pt(4, 4), green)
	cl.Close()
	if n := vs.Credentials(); n != 1 {
		t.Fatalf("%d credentials sent for the pinned connection, want 1", n)
	}

	for _, id := range []string{"roots", "wrong-pin"} {
		awaitEnd(t, ts.streamConnection(t, alice, id, client.Options{}), protocol.CloseBackendFailed)
	}
	if n, f := vs.Credentials(), vs.Failures(); n != 1 || f != 0 {
		t.Errorf("%d credentials sent and %d failed in all, want the pinned connection's 1 and none failed: credentials went to a server the policy refused", n, f)
	}
}

// TestVNCAdhoc opens an ad-hoc vnc target, which adhoc_backends sends
// to the native backend, and asks its owner for the password.
func TestVNCAdhoc(t *testing.T) {
	vs := newVNCServer(t, vnctest.Options{Password: "secret"})
	ts, alice := vncTestServer(t, func(f *config.File) {
		f.AllowAdhocTargets = true
		f.AdhocAllowlist = []string{"127.0.0.1:1-65535"}
		f.AdhocBackends = map[string]string{"vnc": config.BackendNative}
	})
	r := ts.api(t, http.MethodPost, "/api/v1/sessions", alice, `{"protocol":"vnc","target":"`+vs.Addr()+`"}`)
	var o openResponse
	if err := json.Unmarshal(r.body, &o); err != nil || r.code != http.StatusCreated {
		t.Fatalf("POST /api/v1/sessions: %d %s", r.code, r.body)
	}
	if r := ts.api(t, http.MethodGet, "/api/v1/sessions/"+o.ID, alice, ""); !strings.Contains(string(r.body), `"backend":"vnc"`) {
		t.Errorf("state %s, want the native backend", r.body)
	}
	cl := ts.dialStream(t, "/api/v1/sessions/"+url.PathEscape(o.ID)+"/stream", o.StreamToken, client.Options{})
	ev := awaitEvent(t, cl, "the prompt", func(ev client.Event) bool { _, ok := ev.(client.Prompt); return ok })
	ctx, cancel := context.WithTimeout(context.Background(), vncTimeout)
	defer cancel()
	if err := cl.PromptReply(ctx, ev.(client.Prompt).ID, []string{"secret"}); err != nil {
		t.Fatal(err)
	}
	c := acceptVNC(t, vs)
	if err := c.Draw(uniform(image.Rect(0, 0, 8, 8), red)); err != nil {
		t.Fatal(err)
	}
	awaitPixel(t, cl, image.Pt(4, 4), red)
}
