// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package server

import (
	"bytes"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"changkun.de/x/occamy/internal/config"
	"changkun.de/x/occamy/internal/protocol"
	"github.com/gorilla/websocket"
)

// fakeGuacd is an in-process guacd. It answers the handshake, numbers new
// connections $1, $2, ..., lets clients join live ones, sends sync after
// ready, and records every instruction it receives afterwards. Selecting
// the protocol "stall" holds the handshake until release is closed;
// "fail" does the same and then fails it with status 520.
type fakeGuacd struct {
	ln      net.Listener
	release chan struct{}

	mu       sync.Mutex
	selects  []string   // select arguments, in order
	sizes    [][]string // size arguments, in order
	received []string   // raw instructions after ready
	users    map[string]int
	nextID   int
}

func newFakeGuacd(t *testing.T) *fakeGuacd {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	g := &fakeGuacd{ln: ln, release: make(chan struct{}), users: map[string]int{}}
	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			go g.handle(nc)
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return g
}

func (g *fakeGuacd) handle(nc net.Conn) {
	defer nc.Close()
	io := protocol.NewInstructionIO(nc)
	send := func(elements ...string) { io.Write(protocol.NewInstruction(elements)) }

	ins, err := io.Read()
	if err != nil || ins.Opcode() != "select" || len(ins.Args()) == 0 {
		return
	}
	id := ins.Args()[0]
	g.mu.Lock()
	g.selects = append(g.selects, id)
	g.mu.Unlock()

	switch id {
	case "stall":
		<-g.release
	case "fail":
		<-g.release
		send("error", "Upstream unavailable.", "520")
		return
	}
	if strings.HasPrefix(id, "$") {
		g.mu.Lock()
		live := g.users[id] > 0
		g.mu.Unlock()
		if !live {
			send("error", "No such connection.", "519")
			return
		}
	}

	send("args", "VERSION_1_5_0", "hostname", "port", "username", "password")
	for {
		ins, err := io.Read()
		if err != nil {
			return
		}
		if ins.Opcode() == "size" {
			g.mu.Lock()
			g.sizes = append(g.sizes, ins.Args())
			g.mu.Unlock()
		}
		if ins.Opcode() == "connect" {
			break
		}
	}

	g.mu.Lock()
	if !strings.HasPrefix(id, "$") {
		g.nextID++
		id = "$" + strconv.Itoa(g.nextID)
	}
	g.users[id]++
	g.mu.Unlock()
	defer func() {
		g.mu.Lock()
		g.users[id]--
		g.mu.Unlock()
	}()

	send("ready", id)
	send("sync", "1000")
	for {
		raw, err := io.ReadRaw()
		if err != nil {
			return
		}
		g.mu.Lock()
		g.received = append(g.received, string(raw))
		g.mu.Unlock()
	}
}

func (g *fakeGuacd) snapshot() (selects []string, sizes [][]string, received []string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Clone(g.selects), slices.Clone(g.sizes), slices.Clone(g.received)
}

// testProxy runs a proxy against a fake guacd behind an HTTP server whose
// handler takes the claims and display hints from the query string.
type testProxy struct {
	p   *proxy
	g   *fakeGuacd
	url string
}

func newTestProxy(t *testing.T) *testProxy {
	g := newFakeGuacd(t)
	p := &proxy{
		guacd:    config.Guacd{Address: g.ln.Addr().String()},
		sessions: make(map[string]*Session),
		upgrader: &websocket.Upgrader{Subprotocols: []string{"guacamole"}},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := p.upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		q := r.URL.Query()
		p.serveConn(ws, &config.JWT{
			Protocol: q.Get("protocol"),
			Host:     q.Get("host"),
			Username: "occamy",
			Password: q.Get("password"),
		}, displayHints(q))
	}))
	tp := &testProxy{p: p, g: g, url: "ws" + strings.TrimPrefix(srv.URL, "http")}
	// Cleanups run last in, first out: clients registered later close
	// first, then every session must leave the table.
	t.Cleanup(func() {
		srv.Close()
		select {
		case <-g.release:
		default:
			close(g.release)
		}
		waitFor(t, "an empty session table", func() bool { return tp.sessions() == 0 })
	})
	return tp
}

func (tp *testProxy) sessions() int {
	tp.p.mu.Lock()
	defer tp.p.mu.Unlock()
	return len(tp.p.sessions)
}

func (tp *testProxy) users(key string) int {
	tp.p.mu.Lock()
	defer tp.p.mu.Unlock()
	if s, ok := tp.p.sessions[key]; ok {
		return s.users
	}
	return 0
}

func claims(proto, password string) url.Values {
	return url.Values{"protocol": {proto}, "host": {"desktop:5900"}, "password": {password}}
}

func key(q url.Values) string {
	j := config.JWT{Protocol: q.Get("protocol"), Host: q.Get("host"), Username: "occamy", Password: q.Get("password")}
	return j.GenerateID()
}

func (tp *testProxy) connect(t *testing.T, q url.Values) *websocket.Conn {
	t.Helper()
	d := websocket.Dialer{Subprotocols: []string{"guacamole"}, HandshakeTimeout: 5 * time.Second}
	ws, _, err := d.Dial(tp.url+"/?"+q.Encode(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { leave(ws) })
	return ws
}

// leave closes the client side of a connection with a normal close frame.
func leave(ws *websocket.Conn) {
	msg := websocket.FormatCloseMessage(websocket.CloseNormalClosure, "")
	ws.WriteControl(websocket.CloseMessage, msg, time.Now().Add(time.Second))
	ws.Close()
}

func read(t *testing.T, ws *websocket.Conn) (string, error) {
	t.Helper()
	ws.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, msg, err := ws.ReadMessage()
	return string(msg), err
}

func mustRead(t *testing.T, ws *websocket.Conn) string {
	t.Helper()
	msg, err := read(t, ws)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return msg
}

// closeReason reads until the connection closes and returns the Guacamole
// status code in the close reason.
func closeReason(t *testing.T, ws *websocket.Conn) string {
	t.Helper()
	for {
		_, err := read(t, ws)
		var ce *websocket.CloseError
		if errors.As(err, &ce) {
			return ce.Text
		}
		if err != nil {
			t.Fatalf("read: %v, want a close frame", err)
		}
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func announce(id string) string {
	return protocol.NewInstruction([]string{"", id}).String()
}

// A connection that fails before reaching guacd must release the proxy
// lock, otherwise every later connection blocks forever.
func TestRouteConnReleasesLockOnJoinError(t *testing.T) {
	p := &proxy{sessions: make(map[string]*Session)}
	jwt := &config.JWT{
		Protocol: "vnc",
		Host:     "127.0.0.1", // no port
		Password: "occamy",
	}

	for range 2 {
		done := make(chan error, 1)
		go func() { done <- p.routeConn(nil, jwt, display{}) }()
		select {
		case err := <-done:
			if !errors.Is(err, errBadRequest) {
				t.Fatalf("routeConn() = %v, want a bad request", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("routeConn deadlocked on the proxy lock")
		}
		if !p.mu.TryLock() {
			t.Fatal("proxy lock still held after a failed connection")
		}
		p.mu.Unlock()
	}
	if len(p.sessions) != 0 {
		t.Fatalf("failed sessions left in the table: %d", len(p.sessions))
	}
}

func TestRouteConnNoLockDuringHandshake(t *testing.T) {
	tp := newTestProxy(t)
	tp.connect(t, claims("stall", "a"))
	waitFor(t, "the stalled handshake", func() bool {
		selects, _, _ := tp.g.snapshot()
		return slices.Contains(selects, "stall")
	})

	ws := tp.connect(t, claims("vnc", "b"))
	if got, want := mustRead(t, ws), announce("$1"); got != want {
		t.Fatalf("first message = %q, want %q", got, want)
	}
}

func TestRouteConnJoin(t *testing.T) {
	tp := newTestProxy(t)
	owner := tp.connect(t, claims("vnc", "a"))
	if got, want := mustRead(t, owner), announce("$1"); got != want {
		t.Fatalf("owner's first message = %q, want %q", got, want)
	}
	joiner := tp.connect(t, claims("vnc", "a"))
	if got, want := mustRead(t, joiner), announce("$1"); got != want {
		t.Fatalf("joiner's first message = %q, want %q", got, want)
	}
	if selects, _, _ := tp.g.snapshot(); !slices.Equal(selects, []string{"vnc", "$1"}) {
		t.Fatalf("guacd saw select %q, want vnc, then $1", selects)
	}
}

func TestRouteConnOwnerFailureFailsJoiners(t *testing.T) {
	tp := newTestProxy(t)
	q := claims("fail", "a")
	owner := tp.connect(t, q)
	waitFor(t, "the owner's handshake", func() bool {
		selects, _, _ := tp.g.snapshot()
		return len(selects) == 1
	})
	joiner := tp.connect(t, q)
	waitFor(t, "the joiner to wait", func() bool { return tp.users(key(q)) == 2 })
	close(tp.g.release)

	for name, ws := range map[string]*websocket.Conn{"owner": owner, "joiner": joiner} {
		if got := closeReason(t, ws); got != "520" {
			t.Errorf("%s closed with status %q, want 520", name, got)
		}
	}
	if selects, _, _ := tp.g.snapshot(); len(selects) != 1 {
		t.Fatalf("guacd saw select %q, want only the owner's", selects)
	}
}

func TestRouteConnLastUserRemovesEntry(t *testing.T) {
	tp := newTestProxy(t)
	q := claims("vnc", "a")
	owner := tp.connect(t, q)
	mustRead(t, owner)
	joiner := tp.connect(t, q)
	mustRead(t, joiner)
	if n := tp.users(key(q)); n != 2 {
		t.Fatalf("session has %d users, want 2", n)
	}

	leave(owner)
	waitFor(t, "the owner to leave", func() bool { return tp.users(key(q)) == 1 })
	if n := tp.sessions(); n != 1 {
		t.Fatalf("%d sessions while the joiner is connected, want 1", n)
	}
	leave(joiner)
	waitFor(t, "the session to go", func() bool { return tp.sessions() == 0 })
}

func TestServeIOInternalOpcodes(t *testing.T) {
	tp := newTestProxy(t)
	ws := tp.connect(t, claims("vnc", "a"))
	if got, want := mustRead(t, ws), announce("$1"); got != want {
		t.Fatalf("first message = %q, want %q", got, want)
	}

	ping := protocol.NewInstruction([]string{"", "ping", "1730000000000"}).String()
	if err := ws.WriteMessage(websocket.TextMessage, []byte(ping)); err != nil {
		t.Fatal(err)
	}
	for {
		msg := mustRead(t, ws)
		if msg == ping {
			break
		}
		if !strings.HasPrefix(msg, "4.sync,") {
			t.Fatalf("got %q while waiting for the ping reply", msg)
		}
	}

	two := "3.key,5.65307,1.1;3.key,5.65307,1.0;"
	if err := ws.WriteMessage(websocket.TextMessage, []byte(two)); err != nil {
		t.Fatal(err)
	}
	want := []string{"3.key,5.65307,1.1;", "3.key,5.65307,1.0;"}
	waitFor(t, "two instructions at guacd", func() bool {
		_, _, received := tp.g.snapshot()
		return len(received) >= 2
	})
	if _, _, received := tp.g.snapshot(); !slices.Equal(received, want) {
		t.Fatalf("guacd received %q, want %q (and no ping)", received, want)
	}
}

func TestDisplayHints(t *testing.T) {
	tests := []struct {
		query string
		want  display
	}{
		{"", display{1024, 768, 96, ""}},
		{"width=1920&height=1080&dpi=192&timezone=Europe/Berlin", display{1920, 1080, 192, "Europe/Berlin"}},
		{"width=0&height=99999&dpi=1", display{1, 8192, 48, ""}},
		{"width=-5&dpi=9000", display{1, 768, 600, ""}},
		{"width=abc&height=1e3&dpi=&timezone=../../etc/passwd", display{1024, 768, 96, ""}},
		{"timezone=Etc/GMT%2B1", display{1024, 768, 96, "Etc/GMT+1"}},
		{"timezone=" + strings.Repeat("A", 65), display{1024, 768, 96, ""}},
	}
	for _, tt := range tests {
		q, err := url.ParseQuery(tt.query)
		if err != nil {
			t.Fatal(err)
		}
		if got := displayHints(q); got != tt.want {
			t.Errorf("displayHints(%q) = %+v, want %+v", tt.query, got, tt.want)
		}
	}
}

func TestDisplayHintsReachGuacd(t *testing.T) {
	tp := newTestProxy(t)
	q := claims("vnc", "a")
	q.Set("width", "1280")
	q.Set("height", "720")
	q.Set("dpi", "144")
	mustRead(t, tp.connect(t, q))
	if _, sizes, _ := tp.g.snapshot(); len(sizes) != 1 || !slices.Equal(sizes[0], []string{"1280", "720", "144"}) {
		t.Fatalf("guacd got size %q, want 1280x720 at 144 DPI", sizes)
	}
}

func TestSessionLogsOmitPassword(t *testing.T) {
	var buf bytes.Buffer
	var mu sync.Mutex
	log.SetOutput(writerFunc(func(p []byte) (int, error) {
		mu.Lock()
		defer mu.Unlock()
		return buf.Write(p)
	}))
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	const password = "correct-horse-battery-staple"
	tp := newTestProxy(t)
	ws := tp.connect(t, claims("vnc", password))
	mustRead(t, ws)
	leave(ws)
	failed := tp.connect(t, claims("fail", password))
	close(tp.g.release)
	closeReason(t, failed)
	waitFor(t, "the sessions to go", func() bool { return tp.sessions() == 0 })

	mu.Lock()
	logs := buf.String()
	mu.Unlock()
	if strings.Contains(logs, password) {
		t.Fatalf("logs contain the password:\n%s", logs)
	}
	if !strings.Contains(logs, "session $1: owner joined") {
		t.Fatalf("logs do not name the session:\n%s", logs)
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

func TestClientError(t *testing.T) {
	tests := []struct {
		err     error
		failure bool
	}{
		{&websocket.CloseError{Code: websocket.CloseNormalClosure}, false},
		{&websocket.CloseError{Code: websocket.CloseGoingAway}, false},
		{&websocket.CloseError{Code: websocket.CloseAbnormalClosure}, false},
		{&net.OpError{Op: "read", Err: errors.New("connection reset by peer")}, false},
		{io.ErrUnexpectedEOF, false},
		{&websocket.CloseError{Code: websocket.CloseProtocolError}, true},
		{&websocket.CloseError{Code: websocket.CloseMessageTooBig}, true},
	}
	for _, tt := range tests {
		if got := clientError(tt.err); (got != nil) != tt.failure {
			t.Errorf("clientError(%v) = %v, want failure %v", tt.err, got, tt.failure)
		}
	}
}

// A client that leaves normally must not be reported as a failed
// connection.
func TestClientLeaveIsNotAFailure(t *testing.T) {
	var buf bytes.Buffer
	var mu sync.Mutex
	log.SetOutput(writerFunc(func(p []byte) (int, error) {
		mu.Lock()
		defer mu.Unlock()
		return buf.Write(p)
	}))
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	tp := newTestProxy(t)
	ws := tp.connect(t, claims("vnc", "a"))
	mustRead(t, ws)
	leave(ws)
	abrupt := tp.connect(t, claims("vnc", "b"))
	mustRead(t, abrupt)
	abrupt.Close() // no close frame
	waitFor(t, "the sessions to go", func() bool { return tp.sessions() == 0 })

	mu.Lock()
	defer mu.Unlock()
	if strings.Contains(buf.String(), "connection failed") {
		t.Fatalf("a client leaving was logged as a failure:\n%s", buf.String())
	}
}
