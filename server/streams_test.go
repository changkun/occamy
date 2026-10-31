// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package server

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/golang-jwt/jwt/v5"

	"changkun.de/x/occamy/client"
	"changkun.de/x/occamy/internal/auth"
	"changkun.de/x/occamy/internal/backend"
	"changkun.de/x/occamy/internal/backend/backendtest"
	"changkun.de/x/occamy/internal/config"
	"changkun.de/x/occamy/internal/session"
	"changkun.de/x/occamy/internal/stream"
	"changkun.de/x/occamy/internal/terminal"
	"changkun.de/x/occamy/protocol"
)

// termHello is the hello of a client that renders terminals.
var termHello = protocol.Hello{Visible: true, Name: "occamy-test/1", Capabilities: protocol.Capabilities{Terminal: 1}}

// streamClient is a minimal protocol v1 client, enough to drive the
// stream routes message by message, as the Go client does not let a
// test: it sends what it is told, broken messages included, and
// acknowledges every frame. TestEndToEnd uses the Go client.
type streamClient struct {
	ws      *websocket.Conn
	welcome *protocol.Welcome
}

// dial opens the stream at u with the subprotocols and header, and with
// hello in the query unless it is nil.
func dial(u string, subprotocols []string, hello *protocol.Hello, header http.Header) (*streamClient, int, error) {
	if hello != nil {
		q, err := protocol.EncodeHelloQuery(*hello)
		if err != nil {
			return nil, 0, err
		}
		sep := "?"
		if strings.Contains(u, "?") {
			sep = "&"
		}
		u += sep + protocol.HelloParameter + "=" + q
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws, resp, err := websocket.Dial(ctx, u, &websocket.DialOptions{ //nolint:bodyclose // coder/websocket: the response body need not be closed
		Subprotocols: subprotocols,
		HTTPHeader:   header,
	})
	if err != nil {
		if resp != nil {
			return nil, resp.StatusCode, err
		}
		return nil, 0, err
	}
	ws.SetReadLimit(protocol.DefaultMaxMessageSize)
	return &streamClient{ws: ws}, resp.StatusCode, nil
}

// withToken is the subprotocols that carry token.
func withToken(token string) []string {
	return []string{protocol.Subprotocol, protocol.TokenSubprotocol(token)}
}

// stream opens path's stream on ts with token and hello, and reads its
// welcome.
func (ts *testServer) stream(t *testing.T, path, token string, hello protocol.Hello) *streamClient {
	t.Helper()
	c, status, err := dial(ts.srv.URL+path, withToken(token), &hello, nil)
	if err != nil {
		t.Fatalf("%s: %d %v", path, status, err)
	}
	t.Cleanup(func() { _ = c.ws.CloseNow() })
	if got := c.ws.Subprotocol(); got != protocol.Subprotocol {
		t.Errorf("subprotocol %q, want %s", got, protocol.Subprotocol)
	}
	m, err := c.next()
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	w, ok := m.(*protocol.Welcome)
	if !ok {
		t.Fatalf("%s: %#v, want welcome", path, m)
	}
	c.welcome = w
	return c
}

// next reads the next message, acknowledging a frame's end.
func (c *streamClient) next() (protocol.Message, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		typ, p, err := c.ws.Read(ctx)
		if err != nil {
			return nil, err
		}
		if typ != websocket.MessageBinary {
			return nil, errors.New("a text message")
		}
		m, err := protocol.Decode(p, protocol.ServerMessages(), protocol.Limits{})
		if err != nil {
			return nil, err
		}
		if m == nil {
			continue // an ignorable message this client does not know
		}
		if f, ok := m.(*protocol.FrameEnd); ok {
			if err := c.ws.Write(ctx, websocket.MessageBinary, protocol.FrameAck{FrameID: f.FrameID, DecodeTime: 1}.Append(nil)); err != nil {
				return nil, err
			}
		}
		return m, nil
	}
}

// until reads messages until ok returns true for one.
func (c *streamClient) until(t *testing.T, what string, ok func(protocol.Message) bool) {
	t.Helper()
	for {
		m, err := c.next()
		if err != nil {
			t.Fatalf("waiting for %s: %v", what, err)
		}
		if cl, closed := m.(*protocol.Close); closed {
			t.Fatalf("waiting for %s: close %v %q", what, cl.Code, cl.Reason)
		}
		if ok(m) {
			return
		}
	}
}

// closed reads until close, checks its code, and that the WebSocket then
// closes with the same code.
func (c *streamClient) closed(t *testing.T, code protocol.CloseCode) protocol.Close {
	t.Helper()
	for {
		m, err := c.next()
		if err != nil {
			t.Fatalf("waiting for close %v: %v", code, err)
		}
		cl, ok := m.(*protocol.Close)
		if !ok {
			if _, frame := m.(*protocol.Welcome); frame && code >= 4000 && code < 4002 {
				t.Fatalf("welcome, want close %v", code)
			}
			continue
		}
		if cl.Code != code {
			t.Errorf("close %v %q, want %v", cl.Code, cl.Reason, code)
		}
		_, err = c.next()
		if got := websocket.CloseStatus(err); got != websocket.StatusCode(cl.Code) {
			t.Errorf("WebSocket closed with %v (%v), want %v", got, err, cl.Code)
		}
		return *cl
	}
}

func (c *streamClient) send(t *testing.T, m interface{ Append([]byte) []byte }) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.ws.Write(ctx, websocket.MessageBinary, m.Append(nil)); err != nil {
		t.Fatal(err)
	}
}

// TestStreamWelcome opens a connection's stream and gets welcome and the
// first frame, with the session sized as the owner's hello asks.
func TestStreamWelcome(t *testing.T) {
	ts, fb := sessionServer(t, nil)
	hello := termHello
	hello.Cols, hello.Rows = 100, 30
	c := ts.stream(t, "/api/v1/connections/ops-shell/stream", ts.token(t, "alice", "ops"), hello)
	w := c.welcome
	info := ts.s.session(w.SessionID)
	if info == nil || info.owner != (principal{issuer: auth.Issuer, idp: auth.IdPLocal, subject: "alice"}) || w.Role != protocol.RoleOwner || w.Class != protocol.ClassTerminal || w.ViewerID == 0 {
		t.Fatalf("welcome %+v, session %+v", w, info)
	}
	if w.Capabilities.MaxMessageSize != protocol.DefaultMaxMessageSize {
		t.Errorf("agreed %+v", w.Capabilities)
	}
	if _, sz, _ := fb.last(); sz.cols != 100 || sz.rows != 30 {
		t.Errorf("the backend was made at %+v, want the hello's 100x30", sz)
	}
	// The backend runs on a goroutine of its own, so the first frame, a
	// snapshot, may show the session's terminal before the backend sized
	// it, and the session takes the size just after its terminal: wait
	// for a frame of the hello's size, then for the session's.
	var size *protocol.Resize
	snapshot := false
	c.until(t, "a frame of the hello's size", func(m protocol.Message) bool {
		switch m := m.(type) {
		case *protocol.Resize:
			size = m
		case *protocol.TermSnapshot:
			snapshot = true
		case *protocol.FrameEnd:
			return snapshot && size != nil && size.Width == 100 && size.Height == 30
		}
		return false
	})
	waitFor(t, "the session's size", func() bool { return info.s.Size() == image.Pt(100, 30) })
}

// TestStreamRefused checks what a stream request must carry, and that a
// refusal after the upgrade is close with the same code as the WebSocket's
// close.
func TestStreamRefused(t *testing.T) {
	k := newKeys(t)
	path := writeIssuerKey(t, k.gateway)
	ts, _ := sessionServer(t, func(f *config.File) {
		f.Auth.TrustedIssuers = []config.TrustedIssuer{{Issuer: "gateway", PublicKeyFile: path}}
		f.Origins = []string{"https://App.Example"}
	})
	alice := ts.token(t, "alice", "ops")
	o := ts.openSession(t, alice, "ops-shell")
	expired := access("gateway", "alice", "ops")
	expired.IssuedAt = jwt.NewNumericDate(time.Now().Add(-time.Hour))
	expired.ExpiresAt = jwt.NewNumericDate(time.Now().Add(-time.Minute))
	badVersion := termHello.Append(nil)
	header := len(protocol.AppendHeader(nil, protocol.Header{}))
	badVersion[header] = 9
	// A hello that lists raw twice, which no encoder would write.
	twice := termHello
	twice.Capabilities.Codecs = []protocol.Codec{protocol.CodecRaw, protocol.CodecWebP}
	badCodecs := twice.Append(nil)
	badCodecs[bytes.LastIndex(badCodecs, []byte{byte(protocol.CodecRaw), byte(protocol.CodecWebP)})+1] = byte(protocol.CodecRaw)
	if _, err := protocol.DecodeHelloQuery(base64.RawURLEncoding.EncodeToString(badCodecs)); err == nil || !strings.Contains(err.Error(), "twice") {
		t.Fatalf("a hello with raw twice decodes with %v", err)
	}
	const stream = "/api/v1/connections/ops-shell/stream"
	h := termHello
	for _, tt := range []struct {
		name   string
		path   string
		subs   []string
		hello  *protocol.Hello
		header http.Header
		status int                // an HTTP refusal
		code   protocol.CloseCode // close; 0 for welcome
	}{
		{name: "token", subs: withToken(alice), hello: &h},
		{name: "authorization", subs: []string{protocol.Subprotocol}, hello: &h, header: http.Header{"Authorization": {"Bearer " + alice}}},
		{name: "own origin", subs: withToken(alice), hello: &h, header: http.Header{"Origin": {"http://" + strings.TrimPrefix(ts.srv.URL, "http://")}}},
		{name: "listed origin", subs: withToken(alice), hello: &h, header: http.Header{"Origin": {"https://app.example"}}},
		{name: "no subprotocol", hello: &h, header: http.Header{"Authorization": {"Bearer " + alice}}, status: http.StatusBadRequest},
		{name: "only the token subprotocol", subs: withToken(alice)[1:], hello: &h, status: http.StatusBadRequest},
		{name: "no token", subs: []string{protocol.Subprotocol}, hello: &h, code: protocol.CloseUnauthorized},
		{name: "token in the URL", path: stream + "?token=" + alice + "&access_token=" + alice, subs: []string{protocol.Subprotocol}, hello: &h, code: protocol.CloseUnauthorized},
		{name: "wrong key", subs: withToken(signClaims(t, k.stranger, access("gateway", "alice", "ops"))), hello: &h, code: protocol.CloseUnauthorized},
		{name: "expired", subs: withToken(signClaims(t, k.gateway, expired)), hello: &h, code: protocol.CloseUnauthorized},
		{name: "session token", subs: withToken(o.StreamToken), hello: &h, code: protocol.CloseUnauthorized},
		{name: "a share", subs: withToken("abc"), hello: &h, code: protocol.CloseUnauthorized},
		{name: "token subprotocol not base64url", subs: []string{protocol.Subprotocol, protocol.Subprotocol + ".token.a+b"}, hello: &h, code: protocol.CloseUnauthorized},
		{name: "the subprotocol's token before Authorization", subs: withToken("abc"), hello: &h, header: http.Header{"Authorization": {"Bearer " + alice}}, code: protocol.CloseUnauthorized},
		{name: "foreign origin", subs: withToken(alice), hello: &h, header: http.Header{"Origin": {"https://evil.example"}}, code: protocol.CloseForbidden},
		{name: "listed origin's other scheme", subs: withToken(alice), hello: &h, header: http.Header{"Origin": {"http://app.example"}}, code: protocol.CloseForbidden},
		{name: "own host on another port", subs: withToken(alice), hello: &h, header: http.Header{"Origin": {"http://127.0.0.1:1"}}, code: protocol.CloseForbidden},
		{name: "null origin", subs: withToken(alice), hello: &h, header: http.Header{"Origin": {"null"}}, code: protocol.CloseForbidden},
		{name: "empty origin", subs: withToken(alice), hello: &h, header: http.Header{"Origin": {""}}, code: protocol.CloseForbidden},
		{name: "two origins", subs: withToken(alice), hello: &h, header: http.Header{"Origin": {"https://app.example", "https://evil.example"}}, code: protocol.CloseForbidden},
		{name: "no hello", subs: withToken(alice), code: protocol.CloseProtocolError},
		{name: "hello of another version", path: stream + "?hello=" + base64.RawURLEncoding.EncodeToString(badVersion), subs: withToken(alice), code: protocol.CloseUnsupported},
		{name: "hello with a codec twice", path: stream + "?hello=" + base64.RawURLEncoding.EncodeToString(badCodecs), subs: withToken(alice), code: protocol.CloseProtocolError},
		{name: "another's connection", path: "/api/v1/connections/dev-shell/stream", subs: withToken(alice), hello: &h, code: protocol.CloseForbidden},
		{name: "unknown connection", path: "/api/v1/connections/nope/stream", subs: withToken(alice), hello: &h, code: protocol.CloseForbidden},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if tt.path == "" {
				tt.path = stream
			}
			before := ts.s.registry.Len()
			c, status, err := dial(ts.srv.URL+tt.path, tt.subs, tt.hello, tt.header)
			if tt.status != 0 {
				if err == nil || status != tt.status {
					t.Fatalf("upgrade: %d %v, want %d", status, err, tt.status)
				}
				return
			}
			if err != nil {
				t.Fatalf("upgrade: %d %v", status, err)
			}
			defer c.ws.CloseNow()
			if got := c.ws.Subprotocol(); got != protocol.Subprotocol {
				t.Errorf("subprotocol %q", got)
			}
			if tt.code == 0 {
				m, err := c.next()
				if _, ok := m.(*protocol.Welcome); !ok {
					t.Fatalf("%#v %v, want welcome", m, err)
				}
				return
			}
			cl := c.closed(t, tt.code)
			if strings.Contains(cl.Reason, alice) || cl.Reason == "" {
				t.Errorf("reason %q", cl.Reason)
			}
			if tt.name == "expired" && cl.Reason != "token expired" {
				t.Errorf("reason %q, want token expired", cl.Reason)
			}
			if n := ts.s.registry.Len(); n != before {
				t.Errorf("%d sessions after a refusal, %d before", n, before)
			}
		})
	}
}

// TestStreamRefusalShort checks that a refused client that does not
// answer the closing handshake is dropped after refuseTimeout, not the
// 10 s that coder/websocket would give it.
func TestStreamRefusalShort(t *testing.T) {
	ts, _ := sessionServer(t, nil)
	for i, token := range []string{"", "not-a-share", ts.token(t, "mallory")} {
		c, status, err := dial(ts.srv.URL+"/api/v1/connections/ops-shell/stream", withToken(token), &termHello, nil)
		if err != nil {
			t.Fatalf("%d %v", status, err)
		}
		start := time.Now()
		m, err := c.next()
		if _, ok := m.(*protocol.Close); !ok {
			t.Fatalf("%#v %v, want close", m, err)
		}
		// The client reads no further, so it does not answer the
		// WebSocket's close.
		if !ts.s.streams.wait(3 * refuseTimeout) {
			t.Errorf("token %d: the refused stream is still open after %v", i, time.Since(start))
		}
		_ = c.ws.CloseNow()
	}
}

// TestSessionStream checks who joins a session's stream, and as what.
func TestSessionStream(t *testing.T) {
	ts, _ := sessionServer(t, nil)
	alice := ts.token(t, "alice", "ops")
	o := ts.openSession(t, alice, "ops-shell")
	other := ts.openSession(t, alice, "both")
	share := func(id, role string) string {
		r := ts.api(t, http.MethodPost, "/api/v1/sessions/"+id+"/shares", alice, `{"role":"`+role+`"}`)
		var sh shareResponse
		if err := json.Unmarshal(r.body, &sh); err != nil || sh.Token == "" {
			t.Fatalf("share: %s %v", r.body, err)
		}
		return sh.Token
	}
	sessionToken := func(id, role, sub string) string {
		tok, err := ts.s.signer.Session(id, role, sub, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		return tok
	}
	path := "/api/v1/sessions/" + o.ID + "/stream"

	owner := ts.stream(t, path, o.StreamToken, termHello)
	if owner.welcome.Role != protocol.RoleOwner || owner.welcome.SessionID != o.ID {
		t.Errorf("owner's welcome %+v", owner.welcome)
	}
	for _, tt := range []struct {
		name  string
		token string
		role  protocol.Role
	}{
		{"viewer share", share(o.ID, auth.RoleViewer), protocol.RoleViewer},
		{"read-only share", share(o.ID, auth.RoleReadOnly), protocol.RoleReadOnly},
		{"viewer token", sessionToken(o.ID, auth.RoleViewer, "dave"), protocol.RoleViewer},
		{"read-only token", sessionToken(o.ID, auth.RoleReadOnly, "carol"), protocol.RoleReadOnly},
	} {
		c := ts.stream(t, path, tt.token, termHello)
		if c.welcome.Role != tt.role || c.welcome.SessionID != o.ID {
			t.Errorf("%s: welcome %+v", tt.name, c.welcome)
		}
	}

	for _, tt := range []struct {
		name, path, token string
		code              protocol.CloseCode
	}{
		{"another session's share", path, share(other.ID, auth.RoleViewer), protocol.CloseUnauthorized},
		{"another session's stream token", path, other.StreamToken, protocol.CloseForbidden},
		{"no share", path, "Q1p2Q3p4Q5p6Q7p8Q9p0QQ", protocol.CloseUnauthorized},
		{"bob", path, ts.token(t, "bob", "dev"), protocol.CloseForbidden},
		{"alice's group", path, ts.token(t, "carol", "ops"), protocol.CloseForbidden},
		{"an owner token for another", path, sessionToken(o.ID, auth.RoleOwner, "bob"), protocol.CloseForbidden},
		{"unknown session", "/api/v1/sessions/nope/stream", alice, protocol.CloseForbidden},
		{"unknown session's share", "/api/v1/sessions/nope/stream", share(o.ID, auth.RoleViewer), protocol.CloseUnauthorized},
	} {
		c, status, err := dial(ts.srv.URL+tt.path, withToken(tt.token), &termHello, nil)
		if err != nil {
			t.Fatalf("%s: %d %v", tt.name, status, err)
		}
		c.closed(t, tt.code)
	}
	if got := len(ts.s.session(o.ID).s.Viewers()); got != 5 {
		t.Errorf("%d viewers, want the owner and four others", got)
	}
}

// TestOwnerTakeover checks that the owner's newer stream ends the older:
// it may be on a connection that died unnoticed.
func TestOwnerTakeover(t *testing.T) {
	ts, _ := sessionServer(t, nil)
	alice := ts.token(t, "alice", "ops")
	o := ts.openSession(t, alice, "ops-shell")
	path := "/api/v1/sessions/" + o.ID + "/stream"
	first := ts.stream(t, path, o.StreamToken, termHello)
	first.until(t, "the first frame", func(m protocol.Message) bool { _, ok := m.(*protocol.TermSnapshot); return ok })
	second := ts.stream(t, path, alice, termHello)
	if second.welcome.Role != protocol.RoleOwner {
		t.Errorf("second welcome %+v", second.welcome)
	}
	if cl := first.closed(t, protocol.CloseNormal); cl.Reason != "the owner connected again" {
		t.Errorf("the older stream's close %q", cl.Reason)
	}
	third := ts.stream(t, "/api/v1/connections/ops-shell/stream", alice, termHello)
	if third.welcome.SessionID == o.ID {
		t.Error("a connection's stream joined a running session")
	}
	second.until(t, "a frame after the takeover", func(m protocol.Message) bool { _, ok := m.(*protocol.TermSnapshot); return ok })
	if v := ts.s.session(o.ID).s.Viewers(); len(v) != 1 || v[0].ID != int(second.welcome.ViewerID) {
		t.Errorf("viewers %+v, want the second stream's owner alone", v)
	}
}

// TestOwnerHelloResizes checks that the owner joining a running terminal
// sizes it as their hello asks, and a viewer's hello does not.
func TestOwnerHelloResizes(t *testing.T) {
	ts, fb := sessionServer(t, nil)
	alice := ts.token(t, "alice", "ops")
	o := ts.openSession(t, alice, "ops-shell")
	_, _, b := fb.last()
	path := "/api/v1/sessions/" + o.ID + "/stream"
	hello := termHello
	hello.Cols, hello.Rows = 120, 40
	ts.stream(t, path, o.StreamToken, hello)
	waitFor(t, "the resize", func() bool { return ts.s.session(o.ID).s.Size().X == 120 })
	r := ts.api(t, http.MethodPost, "/api/v1/sessions/"+o.ID+"/shares", alice, `{"role":"viewer"}`)
	var sh shareResponse
	_ = json.Unmarshal(r.body, &sh)
	hello.Cols, hello.Rows = 50, 10
	ts.stream(t, path, sh.Token, hello)
	time.Sleep(50 * time.Millisecond)
	var resizes []backend.Resize
	for _, ev := range b.Inputs() {
		if r, ok := ev.(backend.Resize); ok {
			resizes = append(resizes, r)
		}
	}
	if len(resizes) != 1 || resizes[0] != (backend.Resize{Width: 120, Height: 40}) {
		t.Errorf("resizes %+v, want the owner's alone", resizes)
	}
}

// TestDeleteEndsStreams checks that closing a session ends its streams,
// whatever tokens are still valid, and that none joins it after.
func TestDeleteEndsStreams(t *testing.T) {
	ts, _ := sessionServer(t, nil)
	alice := ts.token(t, "alice", "ops")
	o := ts.openSession(t, alice, "ops-shell")
	path := "/api/v1/sessions/" + o.ID + "/stream"
	r := ts.api(t, http.MethodPost, "/api/v1/sessions/"+o.ID+"/shares", alice, `{"role":"viewer"}`)
	var sh shareResponse
	_ = json.Unmarshal(r.body, &sh)
	owner := ts.stream(t, path, o.StreamToken, termHello)
	viewer := ts.stream(t, path, sh.Token, termHello)
	if r := ts.api(t, http.MethodDelete, "/api/v1/sessions/"+o.ID, alice, ""); r.code != http.StatusNoContent {
		t.Fatalf("DELETE: %d %s", r.code, r.body)
	}
	owner.closed(t, protocol.CloseSessionClosed)
	viewer.closed(t, protocol.CloseSessionClosed)
	for _, token := range []string{o.StreamToken, sh.Token, alice} {
		c, _, err := dial(ts.srv.URL+path, withToken(token), &termHello, nil)
		if err != nil {
			t.Fatal(err)
		}
		if cl := c.closed(t, map[bool]protocol.CloseCode{true: protocol.CloseUnauthorized, false: protocol.CloseForbidden}[token == sh.Token]); cl.Code == 0 {
			t.Error("joined a closed session")
		}
	}
}

// TestStreamProtocolErrors checks that a client breaking the protocol
// gets close with the code of its mistake.
func TestStreamProtocolErrors(t *testing.T) {
	ts, _ := sessionServer(t, nil)
	alice := ts.token(t, "alice", "ops")
	const path = "/api/v1/connections/ops-shell/stream"

	c := ts.stream(t, path, alice, termHello)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.ws.Write(ctx, websocket.MessageText, []byte("hello")); err != nil {
		t.Fatal(err)
	}
	c.closed(t, protocol.CloseUnsupported)

	c = ts.stream(t, path, alice, termHello)
	if err := c.ws.Write(ctx, websocket.MessageBinary, []byte{1}); err != nil {
		t.Fatal(err)
	}
	c.closed(t, protocol.CloseProtocolError)

	// A message over the agreed size is refused by the WebSocket itself.
	small := termHello
	small.Capabilities.MaxMessageSize = protocol.MinMessageSize
	c = ts.stream(t, path, alice, small)
	big := protocol.TermInput{Seq: 1, Data: bytes.Repeat([]byte("x"), protocol.MinMessageSize)}
	if err := c.ws.Write(ctx, websocket.MessageBinary, big.Append(nil)); err != nil {
		t.Fatal(err)
	}
	for {
		_, err := c.next()
		if err != nil {
			if got := websocket.CloseStatus(err); got != websocket.StatusMessageTooBig {
				t.Errorf("an oversized message: %v", err)
			}
			break
		}
	}
}

// TestFinishAfterTooLarge checks that a stream that read a message over
// its limit, which coder/websocket answers with close 1009 itself, waits
// for the client to answer that close. A client that writes before it
// reads the close, as one acking a frame it read before does, must not
// fail with a broken pipe, nor lose the close to the reset that closing
// a socket with the rest of the message unread sends.
func TestFinishAfterTooLarge(t *testing.T) {
	read := make(chan error, 1)
	finished := make(chan struct{})
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := connFrom(r.Context())
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			read <- err
			return
		}
		ws.SetReadLimit(1024)
		c := newWSConn(ws, raw)
		_, err = c.ReadMessage(context.Background())
		read <- err
		c.finish(protocol.Close{Code: protocol.CloseTooLarge}, false)
		close(finished)
	}))
	srv.Config.ConnContext = connContext
	srv.Start()
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, srv.URL, nil) //nolint:bodyclose // coder/websocket: the response body need not be closed
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	if err := ws.Write(ctx, websocket.MessageBinary, make([]byte, 64<<10)); err != nil {
		t.Fatal(err)
	}
	if err := <-read; !errors.Is(err, websocket.ErrMessageTooBig) {
		t.Fatalf("ReadMessage: %v, want ErrMessageTooBig", err)
	}
	// Give a server that drops the connection the time to drop it.
	select {
	case <-finished:
	case <-time.After(100 * time.Millisecond):
	}
	if err := ws.Write(ctx, websocket.MessageBinary, []byte{1}); err != nil {
		t.Errorf("a write before reading close: %v", err)
	}
	if _, _, err := ws.Read(ctx); websocket.CloseStatus(err) != websocket.StatusMessageTooBig {
		t.Errorf("read %v, want close %v", err, websocket.StatusMessageTooBig)
	}
	select {
	case <-finished:
	case <-ctx.Done():
		t.Fatal("finish did not return")
	}
}

// TestShutdownEndsStreams serves through the public listener's server,
// whose shutdown ends the sessions and with them their streams, which it
// could not reach as hijacked connections, and checks that streams get
// TCP_NOTSENT_LOWAT.
func TestShutdownEndsStreams(t *testing.T) {
	var mu sync.Mutex
	var lowWater []int
	setLowWater = func(c net.Conn, n int) error {
		if _, ok := c.(*net.TCPConn); !ok {
			t.Errorf("TCP_NOTSENT_LOWAT set on a %T", c)
		}
		mu.Lock()
		lowWater = append(lowWater, n)
		mu.Unlock()
		return nil
	}
	t.Cleanup(func() { setLowWater = streamLowWaterDefault })

	f := testFile(t)
	sessionFile(f)
	s, err := newServer(f)
	if err != nil {
		t.Fatal(err)
	}
	fb := &fakeBackends{}
	s.backends.register("fake", "fake", fb.make)
	ln := localListener(t)
	stop := serving(t, time.Second, s.publicServer(), ln, nil, nil)
	ts := &testServer{s: s, srv: nil}
	token := ts.token(t, "alice", "ops")
	c, status, err := dial("http://"+ln.Addr().String()+"/api/v1/connections/ops-shell/stream", withToken(token), &termHello, nil)
	if err != nil {
		t.Fatalf("%d %v", status, err)
	}
	defer c.ws.CloseNow()
	if m, err := c.next(); err != nil {
		t.Fatal(err)
	} else if _, ok := m.(*protocol.Welcome); !ok {
		t.Fatalf("%#v, want welcome", m)
	}
	mu.Lock()
	if len(lowWater) != 1 || lowWater[0] != streamLowWater {
		t.Errorf("TCP_NOTSENT_LOWAT %v, want %d once", lowWater, streamLowWater)
	}
	mu.Unlock()

	done := make(chan error, 1)
	go func() { done <- stop() }()
	if cl := c.closed(t, protocol.CloseServerShutdown); cl.Reason == "" {
		t.Error("close without a reason")
	}
	if err := <-done; err != nil {
		t.Errorf("serveUntil: %v", err)
	}
	if !s.streams.wait(5 * time.Second) {
		t.Error("the stream did not end")
	}
	if n := s.registry.Len(); n != 0 {
		t.Errorf("%d sessions after the shutdown", n)
	}
}

// streamLowWaterDefault is setLowWater as the package sets it.
var streamLowWaterDefault = setLowWater

// TestEndToEnd signs in, opens a session, streams it, types, and closes
// it, through the real routes and a fake terminal.
func TestEndToEnd(t *testing.T) {
	ts, _ := sessionServer(t, nil)
	r := ts.signIn(t, "alice", alicePassword)
	var tr tokenResponse
	if err := json.Unmarshal(r.body, &tr); err != nil || r.code != http.StatusOK {
		t.Fatalf("sign-in: %d %s", r.code, r.body)
	}
	r = ts.api(t, http.MethodGet, "/api/v1/connections", tr.AccessToken, "")
	if !strings.Contains(string(r.body), `"id":"ops-shell"`) {
		t.Fatalf("connections: %s", r.body)
	}
	o := ts.openSession(t, tr.AccessToken, "ops-shell")

	// The Go client streams it.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cl, err := client.Dial(ctx, ts.srv.URL+"/api/v1/sessions/"+url.PathEscape(o.ID)+"/stream", o.StreamToken, client.Options{Name: "occamy-test/1"})
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	if cl.Role() != protocol.RoleOwner || cl.Class() != protocol.ClassTerminal || cl.SessionID() != o.ID {
		t.Fatalf("role %v, class %v, session %q", cl.Role(), cl.Class(), cl.SessionID())
	}
	seq, err := cl.TermInput(ctx, []byte("echo occamy\r"))
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	for echoed := false; !echoed; {
		ev, err := cl.Next(ctx)
		if err != nil {
			t.Fatal(err)
		}
		switch ev := ev.(type) {
		case client.TermSnapshot:
			out.Reset()
			out.Write(ev.Data)
		case client.TermData:
			out.Write(ev.Data)
		case client.Frame:
			echoed = ev.LastSeq == seq && bytes.Contains(out.Bytes(), []byte("echo occamy"))
		case client.End:
			t.Fatalf("the session ended: %+v", ev)
		}
	}
	r = ts.api(t, http.MethodGet, "/api/v1/sessions/"+o.ID, tr.AccessToken, "")
	if !strings.Contains(string(r.body), `"viewers":[{"id":`+jsonInt(cl.ViewerID())+`,"role":"owner"}]`) {
		t.Errorf("state %s", r.body)
	}
	if r := ts.api(t, http.MethodDelete, "/api/v1/sessions/"+o.ID, tr.AccessToken, ""); r.code != http.StatusNoContent {
		t.Fatalf("DELETE: %d", r.code)
	}
	for {
		ev, err := cl.Next(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if e, ok := ev.(client.End); ok {
			if e.Code != protocol.CloseSessionClosed {
				t.Errorf("end %+v, want %v", e, protocol.CloseSessionClosed)
			}
			break
		}
	}
}

func jsonInt(n uint32) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func TestOriginAllowed(t *testing.T) {
	s := &server{origins: map[string]bool{"https://app.example": true, "http://localhost:3000": true}}
	for _, tt := range []struct {
		host   string
		origin []string
		want   bool
	}{
		{"occamy.example", nil, true},
		{"occamy.example", []string{"https://occamy.example"}, true},
		{"occamy.example", []string{"http://OCCAMY.example"}, true},
		{"occamy.example:8443", []string{"https://occamy.example:8443"}, true},
		{"occamy.example", []string{"https://occamy.example:8443"}, false},
		{"occamy.example", []string{"https://app.example"}, true},
		{"occamy.example", []string{"https://APP.example"}, true},
		{"occamy.example", []string{"http://app.example"}, false},
		{"occamy.example", []string{"http://localhost:3000"}, true},
		{"occamy.example", []string{"http://localhost"}, false},
		{"occamy.example", []string{"https://evil.example"}, false},
		{"occamy.example", []string{"https://occamy.example.evil"}, false},
		{"occamy.example", []string{"null"}, false},
		{"occamy.example", []string{""}, false},
		{"occamy.example", []string{"file://occamy.example"}, false},
		{"occamy.example", []string{"https://occamy.example", "https://evil.example"}, false},
		{"occamy.example", []string{"https://user@occamy.example"}, true},
	} {
		r, _ := http.NewRequest(http.MethodGet, "http://"+tt.host+"/", nil)
		if tt.origin != nil {
			r.Header["Origin"] = tt.origin
		}
		if got := s.originAllowed(r); got != tt.want {
			t.Errorf("Origin %q at %s: %v, want %v", tt.origin, tt.host, got, tt.want)
		}
	}
	// When occamyd terminates TLS, its own pages are https.
	for origin, want := range map[string]bool{
		"https://occamy.example": true,
		"http://occamy.example":  false,
		"https://app.example":    true,
	} {
		r, _ := http.NewRequest(http.MethodGet, "https://occamy.example/", nil)
		r.Header.Set("Origin", origin)
		r.TLS = &tls.ConnectionState{}
		if got := s.originAllowed(r); got != want {
			t.Errorf("Origin %q over TLS: %v, want %v", origin, got, want)
		}
	}
}

func TestOffers(t *testing.T) {
	for _, tt := range []struct {
		header []string
		want   bool
	}{
		{nil, false},
		{[]string{"occamy.v1"}, true},
		{[]string{"guacamole, occamy.v1"}, true},
		{[]string{"guacamole", " occamy.v1 "}, true},
		{[]string{"occamy.v1.token.YQ"}, false},
		{[]string{"occamy.v2"}, false},
	} {
		r := &http.Request{Header: http.Header{"Sec-Websocket-Protocol": tt.header}}
		if got := offers(r, protocol.Subprotocol); got != tt.want {
			t.Errorf("%q: %v", tt.header, got)
		}
	}
}

func TestTrimReason(t *testing.T) {
	if got := trimReason(strings.Repeat("a", 200)); len(got) != maxCloseReason {
		t.Errorf("%d bytes", len(got))
	}
	long := strings.Repeat("a", maxCloseReason-1) + "é"
	if got := trimReason(long); got != strings.Repeat("a", maxCloseReason-1) {
		t.Errorf("cut inside a character: %q", got[len(got)-3:])
	}
	if got := trimReason("short"); got != "short" {
		t.Error(got)
	}
}

func TestCloseMessage(t *testing.T) {
	m := protocol.Close{Code: protocol.CloseIdle, Reason: "idle"}
	if got, ok := closeMessage(m.Append(nil)); !ok || got != m {
		t.Errorf("%+v %v", got, ok)
	}
	if _, ok := closeMessage(protocol.Ping{}.Append(nil)); ok {
		t.Error("ping taken for close")
	}
	if _, ok := closeMessage(append(m.Append(nil), 0)); ok {
		t.Error("close with trailing data taken for close")
	}
	if _, ok := closeMessage(protocol.TermData{Data: make([]byte, 2000)}.Append(nil)); ok {
		t.Error("term-data taken for close")
	}
}

func TestResizeFor(t *testing.T) {
	sz := screen{width: 1280, height: 720, cols: 100, rows: 30}
	if r, ok := resizeFor(backend.Terminal, sz); !ok || r != (backend.Resize{Width: 100, Height: 30}) {
		t.Errorf("terminal: %+v %v", r, ok)
	}
	if r, ok := resizeFor(backend.Graphical, sz); !ok || r != (backend.Resize{Width: 1280, Height: 720}) {
		t.Errorf("graphical: %+v %v", r, ok)
	}
	if _, ok := resizeFor(backend.Terminal, screen{width: 1280, height: 720, cols: 100}); ok {
		t.Error("a terminal resized without rows")
	}
	if _, ok := resizeFor(backend.Graphical, screen{cols: 100, rows: 30}); ok {
		t.Error("a screen resized to no pixels")
	}
}

func TestTracker(t *testing.T) {
	var tr tracker
	if !tr.wait(0) {
		t.Error("no streams, but wait failed")
	}
	tr.add()
	tr.add()
	if tr.wait(10 * time.Millisecond) {
		t.Error("wait returned with streams open")
	}
	go func() {
		tr.done()
		tr.done()
	}()
	if !tr.wait(5 * time.Second) {
		t.Error("wait did not see the streams end")
	}
}

// TestRev111GrowRowsPanics checks that the owner's stream survives a
// resize-request that grows the terminal by a few rows, as a client's
// does when its window grows. It panicked in the terminal's diff once.
func TestRev111GrowRowsPanics(t *testing.T) {
	ts, _ := sessionServer(t, nil)
	alice := ts.token(t, "alice", "ops")
	c := ts.stream(t, "/api/v1/connections/ops-shell/stream", alice, termHello)
	c.until(t, "the first frame", func(m protocol.Message) bool { _, ok := m.(*protocol.FrameEnd); return ok })
	id := c.welcome.SessionID
	c.send(t, protocol.ResizeRequest{Seq: 1, Width: 80, Height: 30})
	c.until(t, "the resize", func(m protocol.Message) bool {
		r, ok := m.(*protocol.Resize)
		return ok && r.Width == 80 && r.Height == 30
	})
	c.until(t, "a frame after the resize", func(m protocol.Message) bool { _, ok := m.(*protocol.FrameEnd); return ok })
	if v := ts.s.session(id).s.Viewers(); len(v) != 1 || v[0].Role != session.RoleOwner {
		t.Errorf("viewers %+v after the resize, want the owner", v)
	}
}

// TestRev111ViewerKillsOwnerStream checks that the owner's stream
// survives a viewer's resize-request that grows the rows while the
// owner's input still waits for its effect.
func TestRev111ViewerKillsOwnerStream(t *testing.T) {
	ts, _ := sessionServer(t, nil)
	alice := ts.token(t, "alice", "ops")
	o := ts.openSession(t, alice, "ops-shell")
	path := "/api/v1/sessions/" + o.ID + "/stream"
	owner := ts.stream(t, path, o.StreamToken, termHello)
	owner.until(t, "the first frame", func(m protocol.Message) bool { _, ok := m.(*protocol.FrameEnd); return ok })
	r := ts.api(t, http.MethodPost, "/api/v1/sessions/"+o.ID+"/shares", alice, `{"role":"viewer"}`)
	var sh shareResponse
	if err := json.Unmarshal(r.body, &sh); err != nil {
		t.Fatal(err)
	}
	viewer := ts.stream(t, path, sh.Token, termHello)
	viewer.until(t, "the first frame", func(m protocol.Message) bool { _, ok := m.(*protocol.FrameEnd); return ok })
	owner.send(t, protocol.Key{Seq: 1, Down: true, Keysym: 'a'}) // its echo is not in yet
	viewer.send(t, protocol.ResizeRequest{Seq: 1, Width: 80, Height: 30})
	owner.until(t, "the resize", func(m protocol.Message) bool {
		r, ok := m.(*protocol.Resize)
		return ok && r.Height == 30
	})
	owner.until(t, "a frame after the resize", func(m protocol.Message) bool { _, ok := m.(*protocol.FrameEnd); return ok })
	if v := ts.s.session(o.ID).s.Viewers(); len(v) != 2 || v[0].Role != session.RoleOwner {
		t.Errorf("viewers %+v after the viewer's resize, want the owner and the viewer", v)
	}
}

// TestRev111PanicLeaks checks that a stream that panics closes with an
// internal error and leaves nothing behind: no WebSocket reader, no
// owner's stream that a newer one would wait for, and no viewer.
func TestRev111PanicLeaks(t *testing.T) {
	streamServe = func(ctx context.Context, c stream.Conn, _ *session.Viewer, _ protocol.Hello, _ stream.Config) error {
		if _, err := c.ReadMessage(ctx); err != nil { // starts the WebSocket's reader
			return err
		}
		panic("injected")
	}
	t.Cleanup(func() { streamServe = stream.Serve })
	ts, _ := sessionServer(t, nil)
	alice := ts.token(t, "alice", "ops")
	o := ts.openSession(t, alice, "ops-shell")
	path := "/api/v1/sessions/" + o.ID + "/stream"
	start := time.Now()
	for range 3 {
		c, status, err := dial(ts.srv.URL+path, withToken(alice), &termHello, nil)
		if err != nil {
			t.Fatalf("%d %v", status, err)
		}
		c.send(t, protocol.Ping{Nonce: 1})
		c.closed(t, protocol.CloseInternalError)
	}
	if d := time.Since(start); d >= takeoverTimeout {
		t.Errorf("the owner's streams took %v: one waited for a stream that panicked", d)
	}
	info := ts.s.session(o.ID)
	info.mu.Lock()
	own := info.ownerStream
	info.mu.Unlock()
	if own != nil {
		t.Error("the owner's stream that panicked is still the owner's stream")
	}
	if v := info.s.Viewers(); len(v) != 0 {
		t.Errorf("viewers %+v after the streams panicked", v)
	}
	waitFor(t, "the WebSocket readers to end", func() bool {
		buf := make([]byte, 1<<20)
		return !strings.Contains(string(buf[:runtime.Stack(buf, true)]), "server.(*wsConn).read(")
	})
	if !ts.s.streams.wait(5 * time.Second) {
		t.Error("the streams are still tracked")
	}
}

// TestStreamSizeBounds checks that the sizes a client asks for are
// clamped before the backend sees them: the owner's hello, and the
// resize-request of a viewer, for a terminal and for a graphical screen.
func TestStreamSizeBounds(t *testing.T) {
	ts, fb := sessionServer(t, nil)
	alice := ts.token(t, "alice", "ops")
	hello := termHello
	hello.Cols, hello.Rows, hello.Width, hello.Height = 65535, 65535, 65535, 65535
	c := ts.stream(t, "/api/v1/connections/ops-shell/stream", alice, hello)
	if _, s, _ := fb.last(); s != (screen{width: backend.MaxResizeSide, height: backend.MaxResizeSide, cols: terminal.MaxCols, rows: terminal.MaxRows}) {
		t.Errorf("the hello's screen reached the backend as %+v", s)
	}
	share := func(id string) string {
		r := ts.api(t, http.MethodPost, "/api/v1/sessions/"+id+"/shares", alice, `{"role":"viewer"}`)
		var sh shareResponse
		if err := json.Unmarshal(r.body, &sh); err != nil {
			t.Fatal(err)
		}
		return sh.Token
	}
	resized := func(inputs func() []backend.InputEvent, want backend.Resize) {
		t.Helper()
		waitFor(t, "the resize", func() bool {
			for _, ev := range inputs() {
				if r, ok := ev.(backend.Resize); ok && r.Width != 80 {
					if r != want {
						t.Fatalf("resize %+v reached the backend, want %+v", r, want)
					}
					return true
				}
			}
			return false
		})
	}
	_, _, term := fb.last()
	id := c.welcome.SessionID
	viewer := ts.stream(t, "/api/v1/sessions/"+id+"/stream", share(id), termHello)
	viewer.send(t, protocol.ResizeRequest{Seq: 1, Width: 65535, Height: 65535})
	resized(term.Inputs, backend.Resize{Width: terminal.MaxCols, Height: terminal.MaxRows})

	var scr *backendtest.Screen
	ts.s.backends.register("fake", "fake", func(target, screen) (backend.Backend, error) {
		scr = backendtest.NewScreen(64, 48, 0)
		return scr, nil
	})
	graphical := protocol.Hello{Visible: true, Capabilities: protocol.Capabilities{Codecs: []protocol.Codec{protocol.CodecRaw}}}
	o := ts.openSession(t, alice, "ops-shell")
	path := "/api/v1/sessions/" + o.ID + "/stream"
	ts.stream(t, path, o.StreamToken, graphical)
	viewer = ts.stream(t, path, share(o.ID), graphical)
	viewer.send(t, protocol.ResizeRequest{Seq: 1, Width: 65535, Height: 65535})
	resized(scr.Inputs, backend.Resize{Width: backend.MaxResizeSide, Height: backend.MaxResizeSide})
}

// TestStreamCodecs checks that a graphical stream offers the encoder's
// codecs, agrees to those of the client's it has, in the client's order,
// sends tiles in them, and closes a client that decodes none of them
// with unsupported.
func TestStreamCodecs(t *testing.T) {
	ts, _ := sessionServer(t, nil)
	ts.s.backends.register("fake", "fake", func(target, screen) (backend.Backend, error) {
		return backendtest.NewScreen(64, 48, 0), nil
	})
	alice := ts.token(t, "alice", "ops")
	const path = "/api/v1/connections/ops-shell/stream"
	hello := func(codecs ...protocol.Codec) protocol.Hello {
		return protocol.Hello{Visible: true, Capabilities: protocol.Capabilities{Codecs: codecs}}
	}
	const (
		raw     = protocol.CodecRaw
		deflate = protocol.CodecDeflate
		png     = protocol.CodecPNG
		jpeg    = protocol.CodecJPEG
		webp    = protocol.CodecWebP
		cached  = protocol.CodecCached
	)
	for _, tt := range []struct {
		offer, agreed []protocol.Codec
		tile          protocol.Codec // of the fake's few colours
	}{
		{[]protocol.Codec{raw}, []protocol.Codec{raw}, raw},
		{[]protocol.Codec{png, raw, cached, deflate, webp}, []protocol.Codec{raw, deflate, webp}, deflate},
		{[]protocol.Codec{webp, jpeg}, []protocol.Codec{webp, jpeg}, webp},
		{[]protocol.Codec{jpeg}, []protocol.Codec{jpeg}, jpeg},
	} {
		c := ts.stream(t, path, alice, hello(tt.offer...))
		if got := c.welcome.Capabilities.Codecs; !slices.Equal(got, tt.agreed) {
			t.Errorf("offer %v: agreed %v, want %v", tt.offer, got, tt.agreed)
		}
		c.until(t, "a tile", func(m protocol.Message) bool {
			tile, ok := m.(*protocol.Tile)
			if ok && tile.Codec != tt.tile {
				t.Errorf("offer %v: a tile in %v, want %v", tt.offer, tile.Codec, tt.tile)
			}
			return ok
		})
		_ = c.ws.CloseNow()
	}

	// occamyd sends neither png nor cached.
	h := hello(png, cached)
	c, status, err := dial(ts.srv.URL+path, withToken(alice), &h, nil)
	if err != nil {
		t.Fatalf("%d %v", status, err)
	}
	c.closed(t, protocol.CloseUnsupported)
}

// failing is a terminal backend whose remote cannot be reached.
type failing struct{ backend.Backend }

func (failing) Run(context.Context, backend.Sink) error { return errors.New("connection refused") }

// TestStreamBackendFails checks that a stream whose backend fails at
// once, however soon, closes with backend failed: before its welcome as
// after.
func TestStreamBackendFails(t *testing.T) {
	ts, _ := sessionServer(t, nil)
	ts.s.backends.register("fake", "fake", func(target, screen) (backend.Backend, error) {
		return failing{backendtest.NewTerminal(80, 24, 0)}, nil
	})
	alice := ts.token(t, "alice", "ops")
	for range 10 {
		c, status, err := dial(ts.srv.URL+"/api/v1/connections/ops-shell/stream", withToken(alice), &termHello, nil)
		if err != nil {
			t.Fatalf("%d %v", status, err)
		}
		if cl := c.closed(t, protocol.CloseBackendFailed); cl.Reason != "failed" {
			t.Errorf("close %v %q", cl.Code, cl.Reason)
		}
	}
}

// panicsOnInput is a terminal whose input handling panics, as a
// backend's bug that a client's message triggers would.
type panicsOnInput struct{ *backendtest.Terminal }

func (panicsOnInput) Input(backend.InputEvent) error { panic("the backend's input bug") }

// TestBackendPanicEndsSession checks that a client's message that makes
// the backend panic ends the session with internal-error for every
// viewer, and that occamyd goes on serving.
func TestBackendPanicEndsSession(t *testing.T) {
	ts, _ := sessionServer(t, nil)
	ts.s.backends.register("fake", "fake", func(target, screen) (backend.Backend, error) {
		return panicsOnInput{backendtest.NewTerminal(80, 24, 0)}, nil
	})
	alice := ts.token(t, "alice", "ops")
	o := ts.openSession(t, alice, "ops-shell")
	path := "/api/v1/sessions/" + o.ID + "/stream"
	owner := ts.stream(t, path, o.StreamToken, termHello)
	r := ts.api(t, http.MethodPost, "/api/v1/sessions/"+o.ID+"/shares", alice, `{"role":"read-only"}`)
	var sh shareResponse
	if err := json.Unmarshal(r.body, &sh); err != nil {
		t.Fatal(err)
	}
	viewer := ts.stream(t, path, sh.Token, termHello)
	owner.send(t, protocol.TermInput{Seq: 1, Data: []byte("x")})
	if cl := owner.closed(t, protocol.CloseInternalError); cl.Reason != "internal error" {
		t.Errorf("owner's close %q", cl.Reason)
	}
	viewer.closed(t, protocol.CloseInternalError)
	waitFor(t, "the session to go", func() bool { return ts.s.session(o.ID) == nil })
	ts.openSession(t, alice, "ops-shell")
}

// TestReadLimitAfterHello checks that a stream takes messages up to the
// agreed size once the client is let in, though the WebSocket took no
// more than protocol.MinMessageSize before.
func TestReadLimitAfterHello(t *testing.T) {
	ts, _ := sessionServer(t, nil)
	alice := ts.token(t, "alice", "ops")
	o := ts.openSession(t, alice, "ops-shell")
	c := ts.stream(t, "/api/v1/sessions/"+o.ID+"/stream", o.StreamToken, termHello)
	if got := c.welcome.Capabilities.MaxMessageSize; got <= 2*protocol.MinMessageSize {
		t.Fatalf("agreed max_message_size %d, want above %d", got, 2*protocol.MinMessageSize)
	}
	c.send(t, protocol.ClientClipboard{MIME: "text/plain", Data: bytes.Repeat([]byte("x"), 2*protocol.MinMessageSize)})
	c.send(t, protocol.Ping{Nonce: 7})
	for {
		m, err := c.next()
		if err != nil {
			t.Fatalf("waiting for the pong after a message of %d bytes: %v", 2*protocol.MinMessageSize, err)
		}
		if cl, ok := m.(*protocol.Close); ok {
			t.Fatalf("close %v %q after a message of %d bytes", cl.Code, cl.Reason, 2*protocol.MinMessageSize)
		}
		if p, ok := m.(*protocol.Pong); ok && p.Nonce == 7 {
			return
		}
	}
}

// TestStalledViewerTooSlow checks that a viewer that reads its frames
// but acknowledges none is closed with too-slow, over the WebSocket too.
func TestStalledViewerTooSlow(t *testing.T) {
	ts, _ := sessionServer(t, func(f *config.File) { f.Streams.ViewerTimeout = time.Second })
	alice := ts.token(t, "alice", "ops")
	o := ts.openSession(t, alice, "ops-shell")
	c := ts.stream(t, "/api/v1/sessions/"+o.ID+"/stream", o.StreamToken, termHello)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := time.Now()
	for {
		_, p, err := c.ws.Read(ctx)
		if err != nil {
			t.Fatalf("read before close too-slow: %v", err)
		}
		m, err := protocol.Decode(p, protocol.ServerMessages(), protocol.Limits{})
		if err != nil {
			t.Fatal(err)
		}
		if cl, ok := m.(*protocol.Close); ok {
			if cl.Code != protocol.CloseTooSlow {
				t.Fatalf("close %v %q, want too-slow", cl.Code, cl.Reason)
			}
			break
		}
	}
	if d := time.Since(start); d < time.Second {
		t.Errorf("closed after %v, before the viewer timeout", d)
	}
	_, _, err := c.ws.Read(ctx)
	if got := websocket.CloseStatus(err); got != websocket.StatusCode(protocol.CloseTooSlow) {
		t.Errorf("WebSocket closed with %v (%v), want %v", got, err, protocol.CloseTooSlow)
	}
}

// heapInUse is the heap in use after a full collection.
func heapInUse() uint64 {
	runtime.GC()
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapInuse
}

// keepReading reads c and acknowledges its frames until the stream ends,
// with no deadline, as an owner who keeps up does.
func keepReading(c *streamClient) {
	ctx := context.Background()
	for {
		_, p, err := c.ws.Read(ctx)
		if err != nil {
			return
		}
		m, err := protocol.Decode(p, protocol.ServerMessages(), protocol.Limits{})
		if f, ok := m.(*protocol.FrameEnd); ok && err == nil {
			if c.ws.Write(ctx, websocket.MessageBinary, protocol.FrameAck{FrameID: f.FrameID, DecodeTime: 1}.Append(nil)) != nil {
				return
			}
		}
	}
}

// screenful is a session of alice's terminal at 1000x500 cells, the
// largest, whose owner keeps up, and a read-only share of it, for tests
// of what its viewers hold.
type screenful struct {
	ts    *testServer
	info  *sessionInfo
	path  string // of its stream
	share string // a read-only share token
	hello protocol.Hello
	owner *streamClient
	seq   uint32
}

func newScreenful(t *testing.T, viewerTimeout time.Duration) *screenful {
	t.Helper()
	ts, _ := sessionServer(t, func(f *config.File) { f.Streams.ViewerTimeout = viewerTimeout })
	alice := ts.token(t, "alice", "ops")
	sf := &screenful{ts: ts, hello: termHello}
	sf.hello.Cols, sf.hello.Rows = 1000, 500
	o := ts.openSession(t, alice, "ops-shell")
	sf.path = "/api/v1/sessions/" + o.ID + "/stream"
	sf.owner = ts.stream(t, sf.path, o.StreamToken, sf.hello)
	go keepReading(sf.owner)
	sf.info = ts.s.session(o.ID)
	waitFor(t, "the size", func() bool { return sf.info.s.Size().X == 1000 })
	sf.redraw(t, 'a')
	r := ts.api(t, http.MethodPost, "/api/v1/sessions/"+o.ID+"/shares", alice, `{"role":"read-only"}`)
	var sh shareResponse
	if err := json.Unmarshal(r.body, &sh); err != nil {
		t.Fatal(err)
	}
	sf.share = sh.Token
	return sf
}

// redraw fills the screen with c, so that a viewer that has the screen
// from before holds a screen the session no longer does.
func (sf *screenful) redraw(t *testing.T, c byte) {
	t.Helper()
	// Home, then 500 rows of 1000 cells, in messages under 64 KiB.
	body := "\x1b[H" + strings.Repeat(string(c), 1000*500-1)
	for len(body) > 0 {
		n := min(len(body), 60000)
		sf.seq++
		sf.owner.send(t, protocol.TermInput{Seq: sf.seq, Data: []byte(body[:n])})
		body = body[n:]
	}
	time.Sleep(150 * time.Millisecond)
}

// ownerAlone fails t unless the owner is the session's only viewer.
func (sf *screenful) ownerAlone(t *testing.T) {
	t.Helper()
	if vs := sf.info.s.Viewers(); len(vs) != 1 || vs[0].Role != session.RoleOwner {
		t.Errorf("viewers %+v, want the owner alone", vs)
	}
}

// perViewer is the heap in use above base for each of n viewers, in MiB.
func perViewer(now, base uint64, n int) float64 {
	return (float64(now) - float64(base)) / float64(n) / (1 << 20)
}

// TestStalledViewersRelease checks that viewers that stop reading, each
// holding another 1000x500 screen of an owner who redraws it between
// their joins, are dropped and hold nothing once the viewer timeout has
// passed, while the owner, who keeps up, stays.
func TestStalledViewersRelease(t *testing.T) {
	if testing.Short() {
		t.Skip("takes seconds")
	}
	sf := newScreenful(t, 3*time.Second)
	base := heapInUse()
	const n = 8
	for i := range n {
		sf.ts.stream(t, sf.path, sf.share, sf.hello) // reads its welcome, then stops
		sf.redraw(t, byte('b'+i))
	}
	held := heapInUse()
	deadline := time.Now().Add(20 * time.Second)
	for len(sf.info.s.Viewers()) > 1 {
		if time.Now().After(deadline) {
			t.Fatalf("viewers %+v, want the stalled ones gone", sf.info.s.Viewers())
		}
		time.Sleep(50 * time.Millisecond)
	}
	sf.ownerAlone(t)
	after := heapInUse()
	t.Logf("heap in use per stalled viewer: %.1f MiB while they stayed, %.1f MiB once dropped", perViewer(held, base, n), perViewer(after, base, n))
	// A screen of 1000x500 cells takes about 11 MiB.
	if p := perViewer(held, base, n); p < 4 {
		t.Errorf("%.1f MiB held per stalled viewer: the test does not show viewers holding screens", p)
	}
	if p := perViewer(after, base, n); p > 2 {
		t.Errorf("%.1f MiB still held per stalled viewer once they were dropped, want at most 2 MiB", p)
	}
}

// TestHiddenViewersRelease checks that viewers that acknowledge their
// first frame and then hide hold no screen while they stay, though the
// owner redraws between their joins.
func TestHiddenViewersRelease(t *testing.T) {
	if testing.Short() {
		t.Skip("takes seconds")
	}
	sf := newScreenful(t, time.Minute)
	base := heapInUse()
	const n = 8
	for i := range n {
		v := sf.ts.stream(t, sf.path, sf.share, sf.hello)
		v.until(t, "the first frame", func(m protocol.Message) bool { _, ok := m.(*protocol.FrameEnd); return ok })
		v.send(t, protocol.View{Visible: false}) // then reads nothing more
		sf.redraw(t, byte('b'+i))
	}
	held := heapInUse()
	if vs := sf.info.s.Viewers(); len(vs) != n+1 {
		t.Fatalf("%d viewers, want the owner and %d hidden ones", len(vs), n)
	}
	t.Logf("heap in use per hidden viewer: %.1f MiB", perViewer(held, base, n))
	if p := perViewer(held, base, n); p > 2 {
		t.Errorf("%.1f MiB held per hidden viewer, want at most 2 MiB", p)
	}
}
