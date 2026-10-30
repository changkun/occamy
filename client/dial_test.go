// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package client_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"changkun.de/x/occamy/client"
	"changkun.de/x/occamy/internal/backend/backendtest"
	"changkun.de/x/occamy/internal/session"
	"changkun.de/x/occamy/internal/stream"
	"changkun.de/x/occamy/protocol"
)

// wsServer adapts a WebSocket to stream.Conn, as occamyd's handler
// will: a close from the client ends the reads.
type wsServer struct{ c *websocket.Conn }

func (w wsServer) ReadMessage(ctx context.Context) ([]byte, error) {
	typ, p, err := w.c.Read(ctx)
	if websocket.CloseStatus(err) != -1 {
		return nil, io.EOF
	}
	if err == nil && typ != websocket.MessageBinary {
		err = errors.New("a text message")
	}
	return p, err
}

func (w wsServer) WriteMessage(ctx context.Context, p []byte) error {
	return w.c.Write(ctx, websocket.MessageBinary, p)
}

// stack is occamyd's stream route in miniature: it authenticates the
// token in the subprotocols, decodes hello and serves a terminal
// session.
type stack struct {
	srv   *httptest.Server
	hello chan protocol.Hello
	query chan string
	done  chan error
}

func newStack(t *testing.T, token string, subprotocols []string) *stack {
	t.Helper()
	st := &stack{hello: make(chan protocol.Hello, 1), query: make(chan string, 1), done: make(chan error, 1)}
	st.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			// net/http would recover a panic itself, and the test then
			// wait for done forever.
			if p := recover(); p != nil {
				st.done <- fmt.Errorf("the handler panicked: %v", p)
			}
		}()
		st.query <- r.URL.RawQuery
		tok, terr := protocol.TokenFromSubprotocols(r.Header.Values("Sec-WebSocket-Protocol"))
		h, herr := protocol.DecodeHelloQuery(r.URL.Query().Get(protocol.HelloParameter))
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: subprotocols})
		if err != nil {
			st.done <- err
			return
		}
		if terr != nil || herr != nil || tok != token {
			m := protocol.Close{Code: protocol.CloseUnauthorized, Reason: "token refused"}
			_ = c.Write(r.Context(), websocket.MessageBinary, m.Append(nil))
			st.done <- c.Close(websocket.StatusCode(m.Code), m.Reason)
			return
		}
		st.hello <- h
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		s, owner, err := session.Start(ctx, session.Config{Backend: backendtest.NewTerminal(80, 24, 0)})
		if err != nil {
			st.done <- err
			return
		}
		defer s.Close()
		err = stream.Serve(ctx, wsServer{c}, owner, h, stream.Config{SessionID: "dialed"})
		_ = c.Close(websocket.StatusNormalClosure, "")
		st.done <- err
	}))
	t.Cleanup(st.srv.Close)
	return st
}

func (st *stack) url() string {
	return "ws" + strings.TrimPrefix(st.srv.URL, "http") + "/api/v1/connections/lab/stream?x=1"
}

// wait returns what the server's handler returned.
func (st *stack) wait(t *testing.T) error {
	t.Helper()
	select {
	case err := <-st.done:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("the server's handler did not return")
		return nil
	}
}

// checkGoroutines fails t if goroutines that started since n outlive
// the test by a second.
func checkGoroutines(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for runtime.NumGoroutine() > n && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := runtime.NumGoroutine(); got > n {
		buf := make([]byte, 1<<16)
		t.Errorf("%d goroutines, want %d:\n%s", got, n, buf[:runtime.Stack(buf, true)])
	}
}

// Dial authenticates with the token in a subprotocol, sends hello in
// the URL, and runs a session over a real WebSocket.
func TestDial(t *testing.T) {
	st := newStack(t, "s3cret", []string{protocol.Subprotocol})
	n := runtime.NumGoroutine()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cl, err := client.Dial(ctx, st.url(), "s3cret", client.Options{Cols: 100, Rows: 30, Name: "test/1"})
	if err != nil {
		t.Fatal(err)
	}
	if q := <-st.query; strings.Contains(q, "s3cret") || !strings.Contains(q, "x=1") {
		t.Errorf("query %q: the token is in the URL, or the route's query is lost", q)
	}
	if h := <-st.hello; h.Cols != 100 || h.Rows != 30 || h.Name != "test/1" || !h.Visible {
		t.Errorf("hello %+v", h)
	}
	if cl.Class() != protocol.ClassTerminal || cl.SessionID() != "dialed" || cl.Role() != protocol.RoleOwner {
		t.Errorf("class %v, session %q, role %v", cl.Class(), cl.SessionID(), cl.Role())
	}
	if _, err := cl.TermInput(ctx, []byte("over the wire\r")); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	for !bytes.Contains(out.Bytes(), []byte("over the wire")) {
		ev, err := cl.Next(ctx)
		if err != nil {
			t.Fatal(err)
		}
		switch ev := ev.(type) {
		case client.TermSnapshot:
			out.Write(ev.Data)
		case client.TermData:
			out.Write(ev.Data)
		case client.End:
			t.Fatalf("the session ended: %+v", ev)
		}
	}
	// The hello's size is a request that stream.Serve leaves to whoever
	// started the session; resize-request asks the running remote.
	if _, err := cl.Resize(ctx, 120, 40); err != nil {
		t.Fatal(err)
	}
	for cl.Size() != image.Pt(120, 40) {
		if ev, err := cl.Next(ctx); err != nil {
			t.Fatal(err)
		} else if e, ok := ev.(client.End); ok {
			t.Fatalf("the session ended: %+v", e)
		}
	}
	cl.Close()
	if err := st.wait(t); err != nil {
		t.Errorf("Serve: %v", err)
	}
	st.srv.CloseClientConnections()
	checkGoroutines(t, n)
}

// A refused token is Dial's *CloseError with the server's code.
func TestDialRefused(t *testing.T) {
	st := newStack(t, "s3cret", []string{protocol.Subprotocol})
	n := runtime.NumGoroutine()
	_, err := client.Dial(context.Background(), st.url(), "wrong", client.Options{})
	var ce *client.CloseError
	if !errors.As(err, &ce) || ce.Code != protocol.CloseUnauthorized || ce.Reason != "token refused" {
		t.Fatalf("Dial returned %v, want unauthorized", err)
	}
	_ = st.wait(t)
	st.srv.CloseClientConnections()
	checkGoroutines(t, n)
}

// A server that does not speak occamy.v1 is refused.
func TestDialSubprotocol(t *testing.T) {
	st := newStack(t, "s3cret", nil)
	_, err := client.Dial(context.Background(), st.url(), "s3cret", client.Options{})
	if !errors.Is(err, protocol.ErrUnsupported) {
		t.Errorf("Dial returned %v, want unsupported", err)
	}
}

// Options that no server could take are refused before dialing.
func TestDialOptions(t *testing.T) {
	_, err := client.Dial(context.Background(), "ws://127.0.0.1:1/", "", client.Options{MaxMessageSize: 100})
	if err == nil || strings.Contains(err.Error(), "dial") {
		t.Errorf("Dial returned %v", err)
	}
}

// Dial's context bounds the handshake only: cancelling it once Dial
// returned leaves the session running.
func TestDialContextAfterReturn(t *testing.T) {
	st := newStack(t, "s3cret", []string{protocol.Subprotocol})
	ctx, cancel := context.WithCancel(context.Background())
	cl, err := client.Dial(ctx, st.url(), "s3cret", client.Options{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	cancel()
	time.Sleep(50 * time.Millisecond)
	ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := cl.TermInput(ctx, []byte("still here\r")); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	for !bytes.Contains(out.Bytes(), []byte("still here")) {
		ev, err := cl.Next(ctx)
		if err != nil {
			t.Fatal(err)
		}
		switch ev := ev.(type) {
		case client.TermSnapshot:
			out.Write(ev.Data)
		case client.TermData:
			out.Write(ev.Data)
		case client.End:
			t.Fatalf("the session ended once Dial's context was cancelled: %+v", ev)
		}
	}
}

// wsScript serves one WebSocket that speaks occamy.v1 and plays serve,
// then reads until the client closes.
func wsScript(t *testing.T, serve func(ctx context.Context, c *websocket.Conn)) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{protocol.Subprotocol}})
		if err != nil {
			return
		}
		defer c.CloseNow()
		serve(r.Context(), c)
		for {
			if _, _, err := c.Read(r.Context()); err != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

// endOf dials url and returns how the session ended.
func endOf(t *testing.T, url string, opts client.Options) client.End {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cl, err := client.Dial(ctx, url, "", opts)
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	for {
		ev, err := cl.Next(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if e, ok := ev.(client.End); ok {
			return e
		}
	}
}

// A text message is not a protocol message: the client ends the
// session with unsupported rather than decode it.
func TestDialTextMessage(t *testing.T) {
	url := wsScript(t, func(ctx context.Context, c *websocket.Conn) {
		_ = c.Write(ctx, websocket.MessageBinary, graphical.Append(nil))
		_ = c.Write(ctx, websocket.MessageText, (&protocol.Title{Title: "text"}).Append(nil))
	})
	if e := endOf(t, url, client.Options{}); e.Code != protocol.CloseUnsupported || !errors.Is(e.Err, protocol.ErrUnsupported) {
		t.Errorf("End %+v, want unsupported", e)
	}
}

// A message beyond the agreed max_message_size ends the session with
// too-large, which coder/websocket's read limit closes the WebSocket
// with.
func TestDialReadLimit(t *testing.T) {
	url := wsScript(t, func(ctx context.Context, c *websocket.Conn) {
		_ = c.Write(ctx, websocket.MessageBinary, graphical.Append(nil))
		big := protocol.TermData{Data: make([]byte, protocol.MinMessageSize)}
		_ = c.Write(ctx, websocket.MessageBinary, big.Append(nil))
	})
	e := endOf(t, url, client.Options{MaxMessageSize: protocol.MinMessageSize})
	if e.Code != protocol.CloseTooLarge || !errors.Is(e.Err, protocol.ErrTooLarge) {
		t.Errorf("End %+v, want too-large", e)
	}
}
