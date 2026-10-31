// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package tests

// Unit tests of the parity matrix's machinery (parity_test.go), which
// need no demo stack.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"changkun.de/x/occamy/client"
	"changkun.de/x/occamy/protocol"
)

// TestParityCell drives a cell with fake checks: when it tries again,
// what it shows, and when it fails the test.
func TestParityCell(t *testing.T) {
	unavailable := fmt.Errorf("%w: waiting for the screen: no such event in 20s", errUnavailable)
	differs := errors.New(`typed "zy", want "yz"`)
	latin1 := parityKnownFailure{"x11vnc: Latin-1 only", `"? ? ??"`}
	garbled := errors.New(`the server took "? ? ??", want "€ ✓ 日本"`)
	tests := []struct {
		name   string
		guacd  bool
		known  *parityKnownFailure
		checks []error // what each try of the check returns
		twins  []error // what each run of the twin returns
		cell   string
		fails  bool // whether the cell fails the test
		pauses int
	}{
		{name: "passes", checks: []error{nil}, cell: "pass"},
		{name: "a native difference stands", checks: []error{differs, nil}, cell: "FAIL: " + differs.Error(), fails: true},
		{
			name:   "native, unavailable, and guacd too: the server's fault",
			checks: []error{unavailable, nil}, twins: []error{unavailable},
			cell: "pass (try 2)", pauses: 1,
		},
		{
			name:   "native, unavailable, but guacd reaches the desktop",
			checks: []error{unavailable, nil}, twins: []error{nil},
			cell: "FAIL: " + unavailable.Error(), fails: true,
		},
		{
			name:   "native, unavailable, but guacd reaches the desktop and fails otherwise",
			checks: []error{unavailable, nil}, twins: []error{differs},
			cell: "FAIL: " + unavailable.Error(), fails: true,
		},
		{
			name:   "native, unavailable at every try",
			checks: []error{unavailable, unavailable, unavailable, nil}, twins: []error{unavailable, unavailable, unavailable},
			cell: "FAIL (try 3): " + unavailable.Error(), fails: true, pauses: 2,
		},
		{
			name:   "native, unavailable, then a difference",
			checks: []error{unavailable, differs, nil}, twins: []error{unavailable},
			cell: "FAIL (try 2): " + differs.Error(), fails: true, pauses: 1,
		},
		{name: "guacd tries again after any failure", guacd: true, checks: []error{differs, differs, nil}, cell: "pass (try 3)", pauses: 2},
		{
			name: "guacd fails at every try", guacd: true,
			checks: []error{differs, differs, differs, nil},
			cell:   "FAIL (try 3): " + differs.Error(), fails: true, pauses: 2,
		},
		{name: "fails as listed", known: &latin1, checks: []error{garbled}, cell: "fail: x11vnc: Latin-1 only"},
		{
			name: "guacd fails as listed after a server fault", guacd: true, known: &latin1,
			checks: []error{unavailable, garbled}, cell: "fail: x11vnc: Latin-1 only (try 2)", pauses: 1,
		},
		{
			name: "listed, but fails otherwise", known: &latin1,
			checks: []error{differs, garbled}, cell: "FAIL: " + differs.Error(), fails: true,
		},
		{
			name: "listed, but unavailable, and guacd too", known: &latin1,
			checks: []error{unavailable, garbled}, twins: []error{unavailable},
			cell: "fail: x11vnc: Latin-1 only (try 2)", pauses: 1,
		},
		{name: "listed, but passes", known: &latin1, checks: []error{nil}, cell: "pass, listed as failing", fails: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var checks, twins, pauses int
			// What the cell logs, shown only if the test fails, since its
			// fake failures read as the matrix's own in go test -v.
			var logged []string
			defer func() {
				if t.Failed() {
					t.Logf("the cell logged:\n%s", strings.Join(logged, "\n"))
				}
			}()
			r := parityCell{
				known: tt.known,
				guacd: tt.guacd,
				check: func() error { checks++; return tt.checks[checks-1] },
				pause: func() { pauses++ },
				logf:  func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) },
			}
			if !tt.guacd {
				r.twin = func() error { twins++; return tt.twins[twins-1] }
			}
			cell, err := r.run()
			if cell != tt.cell {
				t.Errorf("the cell is %q, want %q", cell, tt.cell)
			}
			if (err != nil) != tt.fails {
				t.Errorf("the cell's error is %v, want one: %v", err, tt.fails)
			}
			if pauses != tt.pauses || checks != tt.pauses+1 {
				t.Errorf("checked %d times with %d pauses, want %d and %d", checks, pauses, tt.pauses+1, tt.pauses)
			}
			if tt.guacd && twins != 0 {
				t.Errorf("a guacd cell ran a twin %d times", twins)
			}
			if !tt.guacd && twins > tt.pauses+1 {
				t.Errorf("ran the twin %d times for %d pauses", twins, tt.pauses)
			}
		})
	}
}

// TestParityKnown checks that each entry of parityKnown names a cell
// that runs, and says why and how the cell fails.
func TestParityKnown(t *testing.T) {
	cells := map[string]bool{}
	for _, f := range parityFeatures {
		for _, c := range parityColumns {
			cells[f.name+"|"+c.String()] = f.runsOn(c)
		}
	}
	for cell, k := range parityKnown {
		if !cells[cell] {
			t.Errorf("parityKnown lists %q, which is no cell that runs", cell)
		}
		if k.why == "" || k.want == "" {
			t.Errorf("parityKnown lists %q without why or want: %+v", cell, k)
		}
	}
}

// TestParityMatrixDoc checks that the matrix in guacd-bridge.md is the
// one that parityFeatures and parityKnown give, which is what a run of
// TestVNCParity gives when every cell passes or fails as listed at its
// first try.
func TestParityMatrixDoc(t *testing.T) {
	const doc = "../specs/008-native-architecture/guacd-bridge.md"
	b, err := os.ReadFile(doc)
	if err != nil {
		t.Fatal(err)
	}
	// The table, indented in its list item.
	var table []string
	for line := range strings.SplitSeq(string(b), "\n") {
		if len(table) == 0 && !strings.HasPrefix(line, "  | Feature |") {
			continue
		}
		if !strings.HasPrefix(line, "  |") {
			break
		}
		table = append(table, strings.TrimPrefix(line, "  "))
	}
	if len(table) == 0 {
		t.Fatalf("no matrix in %s", doc)
	}
	got := strings.Join(table, "\n") + "\n"
	if want := parityTable(parityExpected); got != want {
		t.Errorf("the matrix in %s differs from parityFeatures and parityKnown; it should read\n%s", doc, want)
	}
}

// TestParityToken checks that the demo user's token is renewed at
// parityTokenAge.
func TestParityToken(t *testing.T) {
	now := time.Unix(1e9, 0)
	signIns := 0
	p := &parity{
		addr: "occamy-v1",
		signIn: func(addr string) (string, error) {
			signIns++
			if signIns == 3 {
				return "", errors.New("refused")
			}
			return fmt.Sprintf("token %d of %s", signIns, addr), nil
		},
		now: func() time.Time { return now },
	}
	for _, step := range []struct {
		after time.Duration
		token string
		err   bool
	}{
		{0, "token 1 of occamy-v1", false},
		{parityTokenAge - time.Second, "token 1 of occamy-v1", false},
		{time.Second, "token 2 of occamy-v1", false},
		{parityTokenAge, "", true},
		{0, "token 4 of occamy-v1", false},
	} {
		now = now.Add(step.after)
		tok, err := p.token()
		if tok != step.token || (err != nil) != step.err {
			t.Fatalf("after %v: the token is %q, %v; want %q, an error: %v", step.after, tok, err, step.token, step.err)
		}
	}
}

// TestParitySend checks that an input call's error says how the session
// ended.
func TestParitySend(t *testing.T) {
	ended := &paritySession{more: make(chan struct{}), events: []client.Event{
		client.Frame{},
		client.End{Code: protocol.CloseBackendFailed, Reason: "the server closed the connection"},
	}}
	err := ended.send(7, client.ErrClosed)
	if !errors.Is(err, client.ErrClosed) || !strings.Contains(err.Error(), `backend-failed "the server closed the connection"`) {
		t.Errorf("send after the end returned %v, want client.ErrClosed with the end", err)
	}
	if err := ended.send(7, nil); err != nil {
		t.Errorf("send without an error returned %v", err)
	}
	failed := &paritySession{more: make(chan struct{}), err: errors.New("the stream broke")}
	err = failed.send(7, client.ErrClosed)
	if !errors.Is(err, client.ErrClosed) || !strings.Contains(err.Error(), "the stream broke") {
		t.Errorf("send after the stream broke returned %v, want client.ErrClosed with why", err)
	}
}

// TestParityReady checks when a session is ready: once its screen
// arrives, and, where the row answers a password, only after the server
// asked for it.
func TestParityReady(t *testing.T) {
	tests := []struct {
		name        string
		prompt      bool // whether the server asks for a password
		answers     []string
		want        string // a part of the error, or empty for none
		unavailable bool
	}{
		{name: "nothing asked, nothing given"},
		{name: "asked and answered", prompt: true, answers: []string{"vncpassword"}},
		{name: "answers, but nothing asked", answers: []string{"vncpassword"}, want: "without a prompt"},
		{name: "asked, but nothing to answer", prompt: true, want: "where nothing should be asked", unavailable: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, replied := parityServer(t, tt.prompt)
			err := s.ready(tt.answers...)
			switch {
			case tt.want == "" && err != nil:
				t.Fatalf("ready: %v", err)
			case tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)):
				t.Fatalf("ready returned %v, want an error with %q", err, tt.want)
			case errors.Is(err, errUnavailable) != tt.unavailable:
				t.Fatalf("ready returned %v, want errUnavailable: %v", err, tt.unavailable)
			}
			if tt.prompt && len(tt.answers) > 0 {
				select {
				case got := <-replied:
					if !slices.Equal(got, tt.answers) {
						t.Errorf("the server was answered %q, want %q", got, tt.answers)
					}
				default:
					t.Error("the server was not answered")
				}
			}
		})
	}
}

// parityServer joins a paritySession to a server in process that plays
// occamy-v1's side of a stream: welcome, then a prompt for a password
// if prompt, and once the client answers it, a frame of the screen. It
// returns the session and the answers that the server is given.
func parityServer(t *testing.T, prompt bool) (*paritySession, <-chan []string) {
	t.Helper()
	c := &parityPipe{toClient: make(chan []byte, 8), fromClient: make(chan []byte, 8), done: make(chan struct{})}
	replied := make(chan []string, 1)
	send := func(m protocol.Message) { c.toClient <- m.Append(nil) }
	// fromClient returns the client's next message, or nil once the
	// pipe is closed.
	fromClient := func() protocol.Message {
		for {
			select {
			case b := <-c.fromClient:
				if m, err := protocol.Decode(b, protocol.ClientMessages(), protocol.Limits{}); err == nil {
					return m
				}
			case <-c.done:
				return nil
			}
		}
	}
	send(&protocol.Welcome{
		ViewerID: 1, Role: protocol.RoleOwner, Class: protocol.ClassGraphical, SessionID: "s",
		Capabilities: protocol.Capabilities{Codecs: []protocol.Codec{protocol.CodecRaw}, FrameWindow: 2, MaxMessageSize: protocol.DefaultMaxMessageSize},
	})
	go func() {
		if prompt {
			send(&protocol.Prompt{ID: 1, Name: "VNC", Fields: []protocol.PromptField{{Label: "Password", Secret: true}}})
			for {
				m := fromClient()
				if m == nil {
					return
				}
				if r, ok := m.(*protocol.PromptReply); ok {
					replied <- r.Answers
					break
				}
			}
		}
		send(&protocol.Resize{Width: 1024, Height: 768})
		send(&protocol.FrameEnd{FrameID: 1, ServerTime: 1000})
		for fromClient() != nil {
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cl, err := client.New(ctx, c, client.Options{Name: "occamy-parity-test/1", Width: 1024, Height: 768})
	if err != nil {
		t.Fatal(err)
	}
	s := &paritySession{cl: cl, more: make(chan struct{})}
	go s.pump()
	t.Cleanup(func() {
		s.close()
		c.close()
	})
	return s, replied
}

// parityPipe is the client's end of parityServer's connection.
type parityPipe struct {
	toClient, fromClient chan []byte
	done                 chan struct{}
	once                 sync.Once
}

func (c *parityPipe) ReadMessage(ctx context.Context) ([]byte, error) {
	select {
	case b := <-c.toClient:
		return b, nil
	case <-c.done:
		return nil, io.EOF
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c *parityPipe) WriteMessage(ctx context.Context, p []byte) error {
	select {
	case c.fromClient <- slices.Clone(p):
		return nil
	case <-c.done:
		return io.ErrClosedPipe
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *parityPipe) close() { c.once.Do(func() { close(c.done) }) }
