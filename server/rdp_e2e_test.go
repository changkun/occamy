// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package server

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"changkun.de/x/occamy/client"
	"changkun.de/x/occamy/internal/auth"
	"changkun.de/x/occamy/internal/config"
	"changkun.de/x/occamy/internal/rdp/rdptest"
	"changkun.de/x/occamy/protocol"
)

// These tests run the native RDP backend end to end: a client signs in,
// opens or streams an rdp connection, which the native backend serves
// by default, through occamyd's routes, and drives rdptest's fake RDP
// server through it, NLA included.

// rdpServer runs an rdptest.Server on each connection to a loopback
// listener.
type rdpServer struct {
	ln    net.Listener
	conns chan rdpAccepted
}

type rdpAccepted struct {
	c   *rdptest.Conn
	err error
}

func newRDPServer(t *testing.T, srv rdptest.Server) *rdpServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &rdpServer{ln: ln, conns: make(chan rdpAccepted, 4)}
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
				if err != nil {
					nc.Close()
				}
				s.conns <- rdpAccepted{c, err}
			}()
		}
	}()
	t.Cleanup(func() {
		ln.Close()
		<-done
	})
	return s
}

func (s *rdpServer) addr() string { return s.ln.Addr().String() }

// next returns the next connection, whether its connection sequence
// ended or not.
func (s *rdpServer) next(t *testing.T) rdpAccepted {
	t.Helper()
	select {
	case a := <-s.conns:
		if a.c != nil {
			t.Cleanup(func() { a.c.Close() })
		}
		return a
	case <-time.After(vncTimeout):
		t.Fatal("the backend did not connect")
	}
	return rdpAccepted{}
}

// accept returns the next connection whose connection sequence ended.
func (s *rdpServer) accept(t *testing.T) *rdptest.Conn {
	t.Helper()
	a := s.next(t)
	if a.err != nil {
		t.Fatalf("connection sequence: %v", a.err)
	}
	return a.c
}

// rdpConnection is a connection for alice to the RDP server at addr. It
// leaves the backend to rdp's default, the native backend, and pins the
// fake server's certificate.
func rdpConnection(t *testing.T, id, addr string, creds config.Credentials) config.Connection {
	t.Helper()
	_, fp, err := rdptest.Certificate()
	if err != nil {
		t.Fatal(err)
	}
	return config.Connection{
		ID: id, Name: id, Protocol: "rdp", Target: addr,
		TLS:         config.ConnectionTLS{Pins: []string{fp}},
		Credentials: creds, Allow: config.Allow{Users: []string{"alice"}},
	}
}

// nlaServer is a fake server that takes alice's NLA logon.
func nlaServer() rdptest.Server {
	return rdptest.Server{Protocol: rdptest.ProtocolHybrid, Account: &rdptest.Account{Username: "alice", Password: "secret"}}
}

// nextRDP returns the backend's next message to the fake server that ok
// accepts, skipping the others.
func nextRDP(t *testing.T, c *rdptest.Conn, what string, ok func(rdptest.Message) bool) rdptest.Message {
	t.Helper()
	c.SetReadDeadline(time.Now().Add(vncTimeout))
	for {
		m, err := c.Next()
		if err != nil {
			t.Fatalf("waiting for %s: %v", what, err)
		}
		if ok(m) {
			return m
		}
	}
}

// nextEvent returns the backend's next input event of kind.
func nextEvent(t *testing.T, c *rdptest.Conn, kind rdptest.EventKind, ok func(rdptest.Event) bool) rdptest.Event {
	t.Helper()
	var ev rdptest.Event
	nextRDP(t, c, "an input event", func(m rdptest.Message) bool {
		for _, e := range m.Events {
			if e.Kind == kind && ok(e) {
				ev = e
				return true
			}
		}
		return false
	})
	return ev
}

// nextClip returns the backend's next message on the clipboard channel.
func nextClip(t *testing.T, c *rdptest.Conn) rdptest.Cliprdr {
	t.Helper()
	m := nextRDP(t, c, "a clipboard message", func(m rdptest.Message) bool { return m.Channel == "cliprdr" })
	msg, err := rdptest.ParseCliprdr(m.Data)
	if err != nil {
		t.Fatal(err)
	}
	return msg
}

// nextDVC returns the backend's next DRDYNVC PDU.
func nextDVC(t *testing.T, c *rdptest.Conn) rdptest.DVC {
	t.Helper()
	m := nextRDP(t, c, "a DRDYNVC PDU", func(m rdptest.Message) bool { return m.Channel == "drdynvc" })
	d, err := rdptest.ParseDVC(m.Data)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// sendRDP sends frames or channel messages from the fake server.
func sendRDP(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// fill draws r in c on the fake server's desktop.
func fill(t *testing.T, c *rdptest.Conn, r image.Rectangle, col color.RGBA) {
	t.Helper()
	update := rdptest.BitmapUpdate(rdptest.FillBitmap(r, col))
	sendRDP(t, c.Write(rdptest.Fragments(rdptest.FPBitmap, update, 8<<10)...))
}

// TestRDPEndToEnd streams an rdp connection, which leaves the backend to
// its default, with a stored username and password: the backend logs on
// with NLA and asks for the desktop of the client's hello, the screen
// arrives, the client's keys, pointer, text, resize and clipboard reach
// the server, the server's clipboard reaches the client, and the
// server's end of the session ends the stream with remote-ended.
func TestRDPEndToEnd(t *testing.T) {
	rs := newRDPServer(t, nlaServer())
	creds := passwordFile(t, "secret")
	creds.Username = "alice"
	ts, alice := vncTestServer(t, nil, rdpConnection(t, "desk", rs.addr(), creds))
	ctx, cancel := context.WithTimeout(context.Background(), vncTimeout)
	defer cancel()

	cl := ts.streamConnection(t, alice, "desk", client.Options{Width: 640, Height: 480})
	if cl.Role() != protocol.RoleOwner || cl.Class() != protocol.ClassGraphical {
		t.Fatalf("role %v, class %v", cl.Role(), cl.Class())
	}
	c := rs.accept(t)
	if c.Size != image.Pt(640, 480) || c.Authenticates != 1 || c.Delegated.Username != "alice" || c.Delegated.Password != "secret" {
		t.Errorf("desktop %v, %d NLA logons delegating %+v; want the hello's 640×480 and alice's logon", c.Size, c.Authenticates, c.Delegated)
	}
	fill(t, c, image.Rect(0, 0, 640, 480), red)
	awaitPixel(t, cl, image.Pt(639, 479), red)
	r := ts.api(t, http.MethodGet, "/api/v1/sessions/"+cl.SessionID(), alice, "")
	var state sessionStateResponse
	if err := json.Unmarshal(r.body, &state); err != nil || state.Backend != "rdp" || state.Protocol != "rdp" || state.Size == nil || *state.Size != (sessionSize{640, 480}) {
		t.Errorf("state %d %s", r.code, r.body)
	}

	// Input: a key by its physical code, the pointer, and text.
	if _, err := cl.Key(ctx, true, 'a', 0x04); err != nil { // HID usage A
		t.Fatal(err)
	}
	nextEvent(t, c, rdptest.Scancode, func(e rdptest.Event) bool { return e.Code == 0x1e && !e.Release })
	if _, err := cl.Pointer(ctx, 30, 20, protocol.ButtonLeft); err != nil {
		t.Fatal(err)
	}
	nextEvent(t, c, rdptest.Mouse, func(e rdptest.Event) bool { return e.X == 30 && e.Y == 20 && e.Flags&0x9000 == 0x9000 }) // PTRFLAGS_DOWN | BUTTON1
	if _, err := cl.Text(ctx, "é"); err != nil {
		t.Fatal(err)
	}
	nextEvent(t, c, rdptest.Unicode, func(e rdptest.Event) bool { return e.Code == 0xe9 && !e.Release })

	// A resize, through Display Control, which the server applies by
	// reactivating the session at the new size.
	sendRDP(t, c.SendDVC(rdptest.DVCCapsPDU(3)))
	if d := nextDVC(t, c); d.Cmd != rdptest.DVCCaps {
		t.Fatalf("%+v, want the DRDYNVC capabilities", d)
	}
	sendRDP(t, c.SendDVC(rdptest.DVCCreateRequest(3, rdptest.DisplayControl)))
	if d := nextDVC(t, c); d.Cmd != rdptest.DVCCreate || d.Status != 0 {
		t.Fatalf("%+v, want Display Control accepted", d)
	}
	sendRDP(t, c.SendDVC(rdptest.DVCDataPDU(3, rdptest.DisplayCaps(16, 8192, 8192))))
	if _, err := cl.Resize(ctx, 800, 600); err != nil {
		t.Fatal(err)
	}
	d := nextDVC(t, c)
	if ms, err := rdptest.ParseMonitorLayout(d.Data); d.Cmd != rdptest.DVCData || err != nil || len(ms) != 1 || ms[0].Width != 800 || ms[0].Height != 600 {
		t.Fatalf("%+v (%+v, %v), want a layout of 800×600", d, ms, err)
	}
	sendRDP(t, c.Write(append([][]byte{rdptest.DeactivateAll(), rdptest.DemandActive(800, 600, rdptest.DefaultInputFlags)}, rdptest.Finalization()...)...))
	awaitEvent(t, cl, "the resize", func(ev client.Event) bool { return ev == client.Resize{Width: 800, Height: 600} })
	fill(t, c, image.Rect(700, 500, 800, 600), green)
	awaitPixel(t, cl, image.Pt(799, 599), green)

	// The clipboard, both ways, as CF_UNICODETEXT with CR LF on the wire.
	sendRDP(t, c.SendChannel("cliprdr", rdptest.ClipCaps(true)))
	sendRDP(t, c.SendChannel("cliprdr", rdptest.MonitorReady()))
	if m := nextClip(t, c); m.Type != rdptest.CBClipCaps {
		t.Fatalf("CLIPRDR message %d, want the capabilities", m.Type)
	}
	if m := nextClip(t, c); m.Type != rdptest.CBFormatList {
		t.Fatalf("CLIPRDR message %d, want the first Format List", m.Type)
	}
	if _, err := cl.Clipboard(ctx, "text/plain", []byte("café\nau lait")); err != nil {
		t.Fatal(err)
	}
	if m := nextClip(t, c); m.Type != rdptest.CBFormatList {
		t.Fatalf("CLIPRDR message %d, want the client's text announced", m.Type)
	}
	sendRDP(t, c.SendChannel("cliprdr", rdptest.FormatDataRequest(rdptest.CFUnicodeText)))
	m := nextClip(t, c)
	if got, err := rdptest.FromUnicodeText(m.Data); m.Type != rdptest.CBFormatDataResponse || err != nil || got != "café\r\nau lait" {
		t.Errorf("CLIPRDR message %d of %q (%v), want the client's text", m.Type, got, err)
	}
	sendRDP(t, c.SendChannel("cliprdr", rdptest.FormatList(true, rdptest.CFUnicodeText)))
	if m := nextClip(t, c); m.Type != rdptest.CBFormatListResponse || m.Flags != rdptest.CBResponseOK {
		t.Fatalf("%+v, want the server's Format List acknowledged", m)
	}
	if m := nextClip(t, c); m.Type != rdptest.CBFormatDataRequest || binary.LittleEndian.Uint32(m.Data) != rdptest.CFUnicodeText {
		t.Fatalf("%+v, want the server's text asked for", m)
	}
	sendRDP(t, c.SendChannel("cliprdr", rdptest.FormatDataResponse(true, rdptest.UnicodeText("naïve\r\ntext"))))
	ev := awaitEvent(t, cl, "the clipboard", func(ev client.Event) bool { _, ok := ev.(client.Clipboard); return ok })
	if cb := ev.(client.Clipboard); string(cb.Data) != "naïve\ntext" || !strings.HasPrefix(cb.MIME, "text/plain") {
		t.Errorf("clipboard %q %q, want the server's text", cb.MIME, cb.Data)
	}

	// The user logs off.
	sendRDP(t, c.Write(rdptest.SetErrorInfo(0xc), rdptest.DisconnectUltimatum()))
	awaitEnd(t, cl, protocol.CloseRemoteEnded)
}

// TestRDPPrompt opens a session whose connection stores no password. The
// backend asks for it when the session opens, before it connects, so the
// prompt is raised before any stream does and is held for the owner:
// the viewer, whose stream opens first, never gets it, and the server
// takes the owner's answer through NLA. A wrong answer ends the session
// with backend-failed. The backend prompts only before it connects, so
// a prompt raised after a viewer has joined is internal/session's
// TestPrompt.
func TestRDPPrompt(t *testing.T) {
	rs := newRDPServer(t, nlaServer())
	ts, alice := vncTestServer(t, nil, rdpConnection(t, "desk", rs.addr(), config.Credentials{Username: "alice", Prompt: true}))
	ctx, cancel := context.WithTimeout(context.Background(), vncTimeout)
	defer cancel()

	o := ts.openSession(t, alice, "desk")
	r := ts.api(t, http.MethodPost, "/api/v1/sessions/"+o.ID+"/shares", alice, `{"role":"`+auth.RoleViewer+`"}`)
	var share shareResponse
	if err := json.Unmarshal(r.body, &share); err != nil || share.Token == "" {
		t.Fatalf("share: %d %s", r.code, r.body)
	}
	path := "/api/v1/sessions/" + url.PathEscape(o.ID) + "/stream"
	viewer := ts.dialStream(t, path, share.Token, client.Options{})
	owner := ts.dialStream(t, path, o.StreamToken, client.Options{})
	ev := awaitEvent(t, owner, "the prompt", func(ev client.Event) bool { _, ok := ev.(client.Prompt); return ok })
	p := ev.(client.Prompt)
	if len(p.Fields) != 1 || !p.Fields[0].Secret || !strings.Contains(p.Instruction, rs.addr()) {
		t.Fatalf("prompt %+v, want the password for %s", p, rs.addr())
	}
	if err := owner.PromptReply(ctx, p.ID, []string{"secret"}); err != nil {
		t.Fatal(err)
	}
	c := rs.accept(t)
	if c.Delegated.Password != "secret" || c.Size != image.Pt(1024, 768) {
		t.Errorf("delegated %+v, desktop %v; want the owner's answer and the default 1024×768", c.Delegated, c.Size)
	}
	fill(t, c, image.Rect(0, 0, 8, 8), red)
	awaitPixel(t, owner, image.Pt(4, 4), red)
	awaitEvent(t, viewer, "the screen", func(ev client.Event) bool {
		if _, ok := ev.(client.Prompt); ok {
			t.Fatal("the viewer was asked for the password")
		}
		img := viewer.Screenshot()
		_, frame := ev.(client.Frame)
		return frame && img != nil && image.Pt(4, 4).In(img.Rect) && img.RGBAAt(4, 4) == red
	})
	owner.Close()
	viewer.Close()

	cl := ts.streamConnection(t, alice, "desk", client.Options{})
	ev = awaitEvent(t, cl, "the prompt", func(ev client.Event) bool { _, ok := ev.(client.Prompt); return ok })
	if err := cl.PromptReply(ctx, ev.(client.Prompt).ID, []string{"wrong"}); err != nil {
		t.Fatal(err)
	}
	awaitEnd(t, cl, protocol.CloseBackendFailed)
	if a := rs.next(t); !errors.Is(a.err, rdptest.ErrLogonFailure) {
		t.Errorf("the server's Accept returned %v, want the logon refused", a.err)
	}
}

// TestRDPTLS checks a connection's tls policy against the fake server,
// whose certificate signs itself: a pin accepts it, while the system's
// roots and another pin refuse it, which ends the session with
// backend-failed before NLA starts, so that no credential reaches the
// server.
func TestRDPTLS(t *testing.T) {
	rs := newRDPServer(t, nlaServer())
	creds := passwordFile(t, "secret")
	creds.Username = "alice"
	pinned := rdpConnection(t, "pinned", rs.addr(), creds)
	roots := rdpConnection(t, "roots", rs.addr(), creds)
	roots.TLS = config.ConnectionTLS{}
	wrongPin := rdpConnection(t, "wrong-pin", rs.addr(), creds)
	wrongPin.TLS = config.ConnectionTLS{Pins: []string{"SHA256:" + strings.Repeat("A", 43)}}
	ts, alice := vncTestServer(t, nil, pinned, roots, wrongPin)

	cl := ts.streamConnection(t, alice, "pinned", client.Options{})
	c := rs.accept(t)
	fill(t, c, image.Rect(0, 0, 8, 8), green)
	awaitPixel(t, cl, image.Pt(4, 4), green)
	cl.Close()

	for _, id := range []string{"roots", "wrong-pin"} {
		awaitEnd(t, ts.streamConnection(t, alice, id, client.Options{}), protocol.CloseBackendFailed)
		a := rs.next(t)
		if a.err == nil || !strings.Contains(a.err.Error(), "TLS") || a.c != nil && (a.c.Authenticates != 0 || a.c.TSRequest != nil) {
			t.Errorf("%s: the server's Accept returned %v, want the TLS handshake refused before NLA", id, a.err)
		}
	}
}

// TestRDPAdhoc opens an ad-hoc rdp target, which the native backend
// serves by default: its owner is asked for the password before the
// backend connects, and, with no tls policy for an ad-hoc target, the
// fake server's certificate, which signs itself, is refused before NLA
// starts.
func TestRDPAdhoc(t *testing.T) {
	rs := newRDPServer(t, nlaServer())
	ts, alice := vncTestServer(t, func(f *config.File) {
		f.AllowAdhocTargets = true
		f.AdhocAllowlist = []string{"127.0.0.1:1-65535"}
	})
	r := ts.api(t, http.MethodPost, "/api/v1/sessions", alice, `{"protocol":"rdp","target":"`+rs.addr()+`","username":"alice"}`)
	var o openResponse
	if err := json.Unmarshal(r.body, &o); err != nil || r.code != http.StatusCreated {
		t.Fatalf("POST /api/v1/sessions: %d %s", r.code, r.body)
	}
	if r := ts.api(t, http.MethodGet, "/api/v1/sessions/"+o.ID, alice, ""); !strings.Contains(string(r.body), `"backend":"rdp"`) {
		t.Errorf("state %s, want the native backend", r.body)
	}
	cl := ts.dialStream(t, "/api/v1/sessions/"+url.PathEscape(o.ID)+"/stream", o.StreamToken, client.Options{})
	ev := awaitEvent(t, cl, "the prompt", func(ev client.Event) bool { _, ok := ev.(client.Prompt); return ok })
	ctx, cancel := context.WithTimeout(context.Background(), vncTimeout)
	defer cancel()
	if err := cl.PromptReply(ctx, ev.(client.Prompt).ID, []string{"secret"}); err != nil {
		t.Fatal(err)
	}
	awaitEnd(t, cl, protocol.CloseBackendFailed)
	if a := rs.next(t); a.err == nil || !strings.Contains(a.err.Error(), "TLS") {
		t.Errorf("the server's Accept returned %v, want the TLS handshake refused", a.err)
	}
}

// TestRDPClipboardRoles shares a session: a viewer's clipboard reaches
// the server, a read-only viewer cannot send one, and the server's
// reaches every viewer.
func TestRDPClipboardRoles(t *testing.T) {
	rs := newRDPServer(t, nlaServer())
	creds := passwordFile(t, "secret")
	creds.Username = "alice"
	ts, alice := vncTestServer(t, nil, rdpConnection(t, "desk", rs.addr(), creds))
	ctx, cancel := context.WithTimeout(context.Background(), vncTimeout)
	defer cancel()

	o := ts.openSession(t, alice, "desk")
	c := rs.accept(t)
	path := "/api/v1/sessions/" + url.PathEscape(o.ID) + "/stream"
	owner := ts.dialStream(t, path, o.StreamToken, client.Options{})
	viewers := map[string]*client.Session{"owner": owner}
	for _, role := range []string{auth.RoleViewer, auth.RoleReadOnly} {
		r := ts.api(t, http.MethodPost, "/api/v1/sessions/"+o.ID+"/shares", alice, `{"role":"`+role+`"}`)
		var share shareResponse
		if err := json.Unmarshal(r.body, &share); err != nil || share.Token == "" {
			t.Fatalf("share: %d %s", r.code, r.body)
		}
		viewers[role] = ts.dialStream(t, path, share.Token, client.Options{})
	}
	sendRDP(t, c.SendChannel("cliprdr", rdptest.ClipCaps(true)))
	sendRDP(t, c.SendChannel("cliprdr", rdptest.MonitorReady()))
	nextClip(t, c) // the capabilities
	nextClip(t, c) // the first Format List

	// The read-only viewer's client refuses its text; what one sends
	// anyway, the session drops (internal/session's
	// TestReadOnlyInputDropped). The viewer's is offered to the server.
	if _, err := viewers[auth.RoleReadOnly].Clipboard(ctx, "text/plain", []byte("read-only")); !errors.Is(err, client.ErrReadOnly) {
		t.Errorf("the read-only viewer's clipboard: %v, want %v", err, client.ErrReadOnly)
	}
	if _, err := viewers[auth.RoleViewer].Clipboard(ctx, "text/plain", []byte("viewer")); err != nil {
		t.Fatal(err)
	}
	if m := nextClip(t, c); m.Type != rdptest.CBFormatList {
		t.Fatalf("CLIPRDR message %d, want a viewer's text announced", m.Type)
	}
	sendRDP(t, c.SendChannel("cliprdr", rdptest.FormatDataRequest(rdptest.CFUnicodeText)))
	m := nextClip(t, c)
	if got, err := rdptest.FromUnicodeText(m.Data); m.Type != rdptest.CBFormatDataResponse || err != nil || got != "viewer" {
		t.Errorf("the server got %q (%v), want the viewer's text", got, err)
	}

	sendRDP(t, c.SendChannel("cliprdr", rdptest.FormatList(true, rdptest.CFUnicodeText)))
	nextClip(t, c) // the Format List Response
	if m := nextClip(t, c); m.Type != rdptest.CBFormatDataRequest {
		t.Fatalf("%+v, want the server's text asked for", m)
	}
	sendRDP(t, c.SendChannel("cliprdr", rdptest.FormatDataResponse(true, rdptest.UnicodeText("server"))))
	for name, cl := range viewers {
		ev := awaitEvent(t, cl, "the clipboard", func(ev client.Event) bool { _, ok := ev.(client.Clipboard); return ok })
		if cb := ev.(client.Clipboard); string(cb.Data) != "server" {
			t.Errorf("%s: clipboard %q, want the server's", name, cb.Data)
		}
	}
}
