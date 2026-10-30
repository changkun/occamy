// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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

	"github.com/coder/websocket"

	"changkun.de/x/occamy/internal/config"
	"changkun.de/x/occamy/internal/guacamole"
	"changkun.de/x/occamy/internal/guacd"
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
	claims   map[string]string // protocol/password each connection was created with
	nextID   int
	// unknown answers a join to a connection that is not live, as guacd
	// 1.6 does by default; nil closes the socket without a word.
	unknown []string
}

func newFakeGuacd(t *testing.T) *fakeGuacd {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	g := &fakeGuacd{
		ln:      ln,
		release: make(chan struct{}),
		users:   map[string]int{},
		claims:  map[string]string{},
		unknown: []string{"error", "No such connection.", "516"},
	}
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
	io := guacamole.NewInstructionIO(nc)
	send := func(elements ...string) { io.Write(guacamole.NewInstruction(elements)) }

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
		live, reply := g.users[id] > 0, g.unknown
		g.mu.Unlock()
		if !live {
			if reply != nil {
				send(reply...)
			}
			return
		}
	}

	send("args", "VERSION_1_5_0", "hostname", "port", "username", "password")
	var password string
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
			if args := ins.Args(); len(args) == 5 {
				password = args[4]
			}
			break
		}
	}

	g.mu.Lock()
	if !strings.HasPrefix(id, "$") {
		g.nextID++
		protocol := id
		id = "$" + strconv.Itoa(g.nextID)
		g.claims[id] = protocol + "/" + password
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
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := accept(w, r)
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

func (tp *testProxy) dial(q url.Values) (*websocket.Conn, error) {
	ws, _, err := dialWS(tp.url+"/?"+q.Encode(), nil)
	return ws, err
}

// dialWS opens a WebSocket as guacamole-common-js does, with the given
// handshake headers. A failed handshake returns the server's status code,
// or 0 if there was no response.
func dialWS(u string, header http.Header) (*websocket.Conn, int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws, resp, err := websocket.Dial(ctx, u, &websocket.DialOptions{ //nolint:bodyclose // coder/websocket: the response body need not be closed
		Subprotocols: []string{"guacamole"},
		HTTPHeader:   header,
	})
	if err != nil {
		if resp != nil {
			return nil, resp.StatusCode, err
		}
		return nil, 0, err
	}
	ws.SetReadLimit(guacamole.MaxInstructionSize)
	return ws, resp.StatusCode, nil
}

func (tp *testProxy) connect(t *testing.T, q url.Values) *websocket.Conn {
	t.Helper()
	ws, err := tp.dial(q)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { leave(ws) })
	return ws
}

// visit connects with the claims in q, checks what the client gets first,
// and leaves. A "fail" session must close with the owner's status 520;
// any other must announce a connection created with the same claims.
func (tp *testProxy) visit(q url.Values) error {
	ws, err := tp.dial(q)
	if err != nil {
		return err
	}
	defer leave(ws)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, msg, err := ws.Read(ctx)
	if q.Get("protocol") == "fail" {
		if err == nil {
			return fmt.Errorf("got %q, want a close with status 520", msg)
		}
		var ce websocket.CloseError
		if !errors.As(err, &ce) || ce.Reason != "520" {
			return fmt.Errorf("want a close with status 520: %w", err)
		}
		return nil
	}
	if err != nil {
		return err
	}
	ins, err := guacamole.ParseInstruction(msg)
	if err != nil || ins.Opcode() != "" || len(ins.Args()) != 1 {
		return fmt.Errorf("first message %q, want the connection ID", msg)
	}
	id := ins.Args()[0]
	tp.g.mu.Lock()
	got := tp.g.claims[id]
	tp.g.mu.Unlock()
	if want := q.Get("protocol") + "/" + q.Get("password"); got != want {
		return fmt.Errorf("got connection %s, created for %q, want one for %q", id, got, want)
	}
	return nil
}

// leave closes the client side of a connection with a normal close frame,
// and waits for the server's.
func leave(ws *websocket.Conn) {
	ws.Close(websocket.StatusNormalClosure, "")
}

// read reads a message. On a timeout, coder/websocket closes the
// connection.
func read(t *testing.T, ws *websocket.Conn) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, msg, err := ws.Read(ctx)
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
		var ce websocket.CloseError
		if errors.As(err, &ce) {
			return ce.Reason
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
	return guacamole.NewInstruction([]string{"", id}).String()
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
	// The server does not read during the handshake, so it would answer
	// a close frame only once guacd releases the handshake, after this
	// cleanup. Drop the connection instead.
	stalled, err := tp.dial(claims("stall", "a"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stalled.CloseNow() })
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

// A client that joins a session as guacd ends it starts a new session
// instead of being disconnected (specs/003-session-lifecycle-hardening.md,
// defect 6). guacd either answers the join with 516 or closes the socket.
// Any other failure reaches the client as it is.
func TestRouteConnJoinEndedSession(t *testing.T) {
	for _, tt := range []struct {
		name   string
		reply  []string // guacd's answer to the join
		status string   // the client's close status, or "" for a new session
	}{
		{"removed", []string{"error", "No such connection.", "516"}, ""},
		{"closing", nil, ""},
		{"failed", []string{"error", "Server error.", "512"}, "512"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tp := newTestProxy(t)
			tp.g.mu.Lock()
			tp.g.unknown = tt.reply
			tp.g.mu.Unlock()

			// The session's last user is still leaving, but guacd has
			// already ended its connection.
			q := claims("vnc", "a")
			stale, _ := tp.p.enter(key(q))
			tp.p.started(stale, &guacd.Conn{ID: "$9"}, nil)
			var once sync.Once
			leaveStale := func() { once.Do(func() { tp.p.leave(stale) }) }
			t.Cleanup(leaveStale)

			ws := tp.connect(t, q)
			if tt.status != "" {
				if got := closeReason(t, ws); got != tt.status {
					t.Fatalf("closed with status %q, want %q", got, tt.status)
				}
				if selects, _, _ := tp.g.snapshot(); !slices.Equal(selects, []string{"$9"}) {
					t.Fatalf("guacd saw select %q, want only $9", selects)
				}
				return
			}

			if got, want := mustRead(t, ws), announce("$1"); got != want {
				t.Fatalf("first message = %q, want %q", got, want)
			}
			if selects, _, _ := tp.g.snapshot(); !slices.Equal(selects, []string{"$9", "vnc"}) {
				t.Fatalf("guacd saw select %q, want $9, then vnc", selects)
			}
			tp.p.mu.Lock()
			s := tp.p.sessions[key(q)]
			tp.p.mu.Unlock()
			if s == nil || s == stale || s.ConnID != "$1" {
				t.Fatal("the table does not hold the new session $1")
			}

			// The old session's last user leaves the new session alone.
			leaveStale()
			if n := tp.users(key(q)); n != 1 {
				t.Fatalf("new session has %d users, want 1", n)
			}
		})
	}
}

// Many clients come and go at once on a few shared keys, and the owners
// of some sessions fail. Every client must get a connection created for
// its own claims, or its owner's error, and the table must be empty once
// all have left. Run with -race.
func TestRouteConnStress(t *testing.T) {
	tp := newTestProxy(t)
	close(tp.g.release) // "fail" fails at once
	keys := []url.Values{
		claims("vnc", "a"),
		claims("vnc", "b"),
		claims("ssh", "a"),
		claims("fail", "a"),
		claims("fail", "b"),
	}
	const clients, rounds = 40, 10
	var wg sync.WaitGroup
	for i := range clients {
		wg.Go(func() {
			for r := range rounds {
				q := keys[(i+r)%len(keys)]
				if err := tp.visit(q); err != nil {
					t.Errorf("client %d, round %d, %s/%s: %v", i, r, q.Get("protocol"), q.Get("password"), err)
				}
			}
		})
	}
	wg.Wait()
	waitFor(t, "an empty session table", func() bool { return tp.sessions() == 0 })

	selects, _, _ := tp.g.snapshot()
	joins := 0
	for _, s := range selects {
		if strings.HasPrefix(s, "$") {
			joins++
		}
	}
	t.Logf("%d connections, %d handshakes with guacd, %d of them joins", clients*rounds, len(selects), joins)
	if joins == 0 {
		t.Fatal("no client joined a session")
	}
}

func TestServeIOInternalOpcodes(t *testing.T) {
	tp := newTestProxy(t)
	ws := tp.connect(t, claims("vnc", "a"))
	if got, want := mustRead(t, ws), announce("$1"); got != want {
		t.Fatalf("first message = %q, want %q", got, want)
	}

	ping := guacamole.NewInstruction([]string{"", "ping", "1730000000000"}).String()
	if err := ws.Write(context.Background(), websocket.MessageText, []byte(ping)); err != nil {
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
	if err := ws.Write(context.Background(), websocket.MessageText, []byte(two)); err != nil {
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
	// coder/websocket wraps what it returns, as Read does here.
	wrap := func(err error) error { return fmt.Errorf("failed to get reader: %w", err) }
	tests := []struct {
		err     error
		failure bool
	}{
		{wrap(websocket.CloseError{Code: websocket.StatusNormalClosure}), false},
		{wrap(websocket.CloseError{Code: websocket.StatusGoingAway}), false},
		{wrap(websocket.CloseError{Code: websocket.StatusNoStatusRcvd}), false},
		// A dropped connection: coder/websocket has no close code 1006.
		{wrap(&net.OpError{Op: "read", Err: errors.New("connection reset by peer")}), false},
		{wrap(io.EOF), false},
		{io.ErrUnexpectedEOF, false},
		{wrap(net.ErrClosed), false},
		{wrap(websocket.CloseError{Code: websocket.StatusProtocolError}), true},
		{wrap(websocket.CloseError{Code: websocket.StatusMessageTooBig}), true},
		// Over the read limit, coder/websocket closes with 1009 itself.
		{fmt.Errorf("failed to read: %w", fmt.Errorf("%w: read limited at 9 bytes", websocket.ErrMessageTooBig)), true},
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
	abrupt.CloseNow() // no close frame
	waitFor(t, "the sessions to go", func() bool { return tp.sessions() == 0 })

	mu.Lock()
	defer mu.Unlock()
	if strings.Contains(buf.String(), "connection failed") {
		t.Fatalf("a client leaving was logged as a failure:\n%s", buf.String())
	}
}
