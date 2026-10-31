// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package kubernetes_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"changkun.de/x/occamy/internal/backend"
	"changkun.de/x/occamy/internal/backend/backendtest"
	"changkun.de/x/occamy/internal/backend/kubernetes"
	"changkun.de/x/occamy/internal/backend/kubernetes/kubetest"
	"github.com/coder/websocket"
)

const timeout = 10 * time.Second

// token is the bearer token of the tests, distinct enough to search
// every error and log line for.
const token = "kube-T0KEN-must-not-leak.5f1c"

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

// ownFrame matches a stack frame in package kubernetes, not in its
// tests.
var ownFrame = regexp.MustCompile(`changkun\.de/x/occamy/internal/backend/kubernetes\.`)

// checkStopped fails the test if a goroutine is still running code of
// package kubernetes, which must not outlive Run.
func checkStopped(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		var left []string
		for g := range strings.SplitSeq(string(stacks()), "\n\n") {
			var frames []string
			for line := range strings.Lines(g) {
				if !strings.HasPrefix(line, "created by ") {
					frames = append(frames, line)
				}
			}
			if ownFrame.MatchString(strings.Join(frames, "")) && !strings.Contains(g, "_test.") {
				left = append(left, g)
			}
		}
		if len(left) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("goroutines of package kubernetes outlived Run:\n%s", strings.Join(left, "\n\n"))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func newServer(t *testing.T, opts kubetest.Options) *kubetest.Server {
	t.Helper()
	s := kubetest.NewServer(opts)
	t.Cleanup(s.Close)
	return s
}

// logs is a logger that keeps what it is given.
type logs struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *logs) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *logs) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

func (l *logs) logger() *slog.Logger {
	return slog.New(slog.NewTextHandler(l, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// config is a configuration for s's pod web-0 in namespace apps, with
// the server's certificate pinned and the token.
func config(s *kubetest.Server) kubernetes.Config {
	return kubernetes.Config{
		Addr:      s.Addr(),
		Namespace: "apps",
		Pod:       "web-0",
		Container: "app",
		Token:     token,
		TLS:       kubernetes.TLS{Pins: []string{s.Fingerprint()}},
	}
}

func newBackend(t *testing.T, cfg kubernetes.Config) *kubernetes.Backend {
	t.Helper()
	b, err := kubernetes.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

type runner struct {
	cancel context.CancelFunc
	done   chan struct{}
	err    error
}

func start(t *testing.T, b *kubernetes.Backend, sink backend.Sink) *runner {
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

// run runs b until it returns, for a backend that fails to connect.
func run(t *testing.T, b *kubernetes.Backend, sink backend.Sink) error {
	t.Helper()
	return start(t, b, sink).wait(t)
}

func accept(t *testing.T, s *kubetest.Server) *kubetest.Exec {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	e, err := s.Accept(ctx)
	if err != nil {
		t.Fatalf("the backend opened no stream: %v", err)
	}
	return e
}

// next returns the backend's next message of type M, skipping the
// others.
func next[M kubetest.Message](t *testing.T, e *kubetest.Exec) M {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	for {
		m, err := e.Next(ctx)
		if err != nil {
			var zero M
			t.Fatalf("waiting for the backend's %T: %v", zero, err)
		}
		if m, ok := m.(M); ok {
			return m
		}
	}
}

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

func input(t *testing.T, b *kubernetes.Backend, ev backend.InputEvent) {
	t.Helper()
	if err := b.Input(ev); err != nil {
		t.Fatal(err)
	}
}

// TestExec runs the default command over v5: the request, the size, the
// output, input both ways, a resize and the exit.
func TestExec(t *testing.T) {
	checkGoroutines(t)
	s := newServer(t, kubetest.Options{Token: token})
	cfg := config(s)
	cfg.Cols, cfg.Rows = 100, 30
	b := newBackend(t, cfg)
	sink := &backendtest.Sink{}
	r := start(t, b, sink)
	e := accept(t, s)

	if e.Protocol != kubernetes.ProtocolV5 {
		t.Errorf("protocol %q, want %q", e.Protocol, kubernetes.ProtocolV5)
	}
	if e.Method != http.MethodGet || e.Path != "/api/v1/namespaces/apps/pods/web-0/exec" {
		t.Errorf("request %s %s", e.Method, e.Path)
	}
	want := url.Values{
		"command": kubernetes.DefaultCommand, "container": {"app"},
		"stdin": {"true"}, "stdout": {"true"}, "stderr": {"false"}, "tty": {"true"},
	}
	if fmt.Sprint(e.Query) != fmt.Sprint(want) {
		t.Errorf("query %v, want %v", e.Query, want)
	}
	if got := e.Header.Get("Authorization"); got != "Bearer "+token {
		t.Errorf("Authorization %q", got)
	}
	if m := next[kubetest.ResizeMessage](t, e); m != (kubetest.ResizeMessage{Width: 100, Height: 30}) {
		t.Errorf("first size %+v, want 100x30", m)
	}
	eventually(t, "the size", func() bool { return sink.Resizes() == 1 })

	if err := e.Stdout([]byte("$ ")); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the prompt", func() bool { return string(sink.Term()) == "$ " })
	input(t, b, backend.TermInput{Data: []byte("ls\r")})
	if m := next[kubetest.StdinMessage](t, e); string(m.Data) != "ls\r" {
		t.Errorf("stdin %q", m.Data)
	}
	input(t, b, backend.Text{Text: "é"})
	if m := next[kubetest.StdinMessage](t, e); string(m.Data) != "é" {
		t.Errorf("stdin %q", m.Data)
	}
	input(t, b, backend.Key{Down: true, Keysym: 'a'}) // ignored
	input(t, b, backend.Resize{Width: 120, Height: 40})
	if m := next[kubetest.ResizeMessage](t, e); m != (kubetest.ResizeMessage{Width: 120, Height: 40}) {
		t.Errorf("size %+v, want 120x40", m)
	}
	eventually(t, "the new size", func() bool { return sink.Size().X == 120 && sink.Size().Y == 40 })
	if err := e.Stdout([]byte("bye\r\n")); err != nil {
		t.Fatal(err)
	}
	if err := e.Exit(0); err != nil {
		t.Fatal(err)
	}
	if err := r.wait(t); err != nil {
		t.Errorf("Run = %v, want nil after exit 0", err)
	}
	if got := string(sink.Term()); got != "$ bye\r\n" {
		t.Errorf("terminal %q", got)
	}
	if err := b.Input(backend.TermInput{Data: []byte("x")}); !errors.Is(err, kubernetes.ErrNotRunning) {
		t.Errorf("Input after Run = %v, want ErrNotRunning", err)
	}
}

// TestV4 checks a server that speaks v4 only, and the exit statuses
// that end a session normally or not.
func TestV4(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  []byte
		wantErr string // empty for nil
	}{
		{"exit 0", kubetest.ExitStatus(0), ""},
		{"exit 3", kubetest.ExitStatus(3), ""},
		{"exit 255", kubetest.ExitStatus(255), ""},
		{"failure", []byte(`{"metadata":{},"status":"Failure","message":"container \"app\" not found in pod \"web-0\"","reason":"InternalError"}`), `kubernetes: the command failed: "InternalError": "container \"app\" not found in pod \"web-0\""`},
		{"exit 256", []byte(`{"status":"Failure","reason":"NonZeroExitCode","details":{"causes":[{"reason":"ExitCode","message":"256"}]}}`), "not a number from 0 to 255"},
		{"no cause", []byte(`{"status":"Failure","reason":"NonZeroExitCode"}`), "has no exit code"},
		{"not JSON", []byte("exec failed\x1b[2J"), `not JSON: "exec failed\x1b[2J"`},
		{"unknown", []byte(`{"status":"Pending"}`), `status is "Pending"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checkGoroutines(t)
			s := newServer(t, kubetest.Options{Protocols: []string{kubetest.ProtocolV4}})
			b := newBackend(t, config(s))
			r := start(t, b, &backendtest.Sink{})
			e := accept(t, s)
			if e.Protocol != kubernetes.ProtocolV4 {
				t.Fatalf("protocol %q", e.Protocol)
			}
			if err := e.Status(tc.status); err != nil {
				t.Fatal(err)
			}
			err := r.wait(t)
			switch {
			case tc.wantErr == "" && err != nil:
				t.Errorf("Run = %v, want nil", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Errorf("Run = %v, want an error with %q", err, tc.wantErr)
			}
		})
	}
}

// TestFailureStatusError checks that a failure that is no exit status
// is a StatusError with what the server said.
func TestFailureStatusError(t *testing.T) {
	s := newServer(t, kubetest.Options{})
	b := newBackend(t, config(s))
	r := start(t, b, &backendtest.Sink{})
	e := accept(t, s)
	if err := e.Status([]byte(`{"status":"Failure","reason":"InternalError","message":"OCI runtime exec failed"}`)); err != nil {
		t.Fatal(err)
	}
	var se *kubernetes.StatusError
	if err := r.wait(t); !errors.As(err, &se) || se.HTTPStatus != 0 || se.Reason != "InternalError" || se.Message != "OCI runtime exec failed" {
		t.Errorf("Run = %v (%+v)", err, se)
	}
}

// TestEndWithoutStatus checks that a stream that ends without a status,
// as a lost connection does, is an error, with or without a close.
func TestEndWithoutStatus(t *testing.T) {
	for _, abort := range []bool{false, true} {
		t.Run(fmt.Sprint("abort=", abort), func(t *testing.T) {
			checkGoroutines(t)
			s := newServer(t, kubetest.Options{})
			b := newBackend(t, config(s))
			r := start(t, b, &backendtest.Sink{})
			e := accept(t, s)
			if abort {
				e.Abort()
			} else if err := e.Close(); err != nil {
				t.Fatal(err)
			}
			if err := r.wait(t); err == nil || !strings.Contains(err.Error(), "without an exit status") {
				t.Errorf("Run = %v, want an error for the missing status", err)
			}
		})
	}
}

// TestCancel checks that cancelling Run ends it with the context's error,
// closing stdin first over v5 so that the shell exits, and not over v4.
func TestCancel(t *testing.T) {
	for _, proto := range []string{kubetest.ProtocolV5, kubetest.ProtocolV4} {
		t.Run(proto, func(t *testing.T) {
			checkGoroutines(t)
			s := newServer(t, kubetest.Options{Protocols: []string{proto}})
			b := newBackend(t, config(s))
			sink := &backendtest.Sink{}
			r := start(t, b, sink)
			e := accept(t, s)
			next[kubetest.ResizeMessage](t, e)
			// The size reaches the server before its write returns, and
			// coder/websocket closes the connection if the write's
			// context ends before then. The sink learns the size once
			// the write has returned.
			eventually(t, "the size", func() bool { return sink.Resizes() == 1 })
			r.cancel()
			if err := r.wait(t); !errors.Is(err, context.Canceled) {
				t.Errorf("Run = %v, want context.Canceled", err)
			}
			var closes []kubetest.CloseMessage
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()
			for {
				m, err := e.Next(ctx)
				if err != nil {
					break
				}
				if c, ok := m.(kubetest.CloseMessage); ok {
					closes = append(closes, c)
				}
			}
			want := []kubetest.CloseMessage{{Channel: kubetest.Stdin}}
			if proto == kubetest.ProtocolV4 {
				want = nil
			}
			if !slices.Equal(closes, want) {
				t.Errorf("close signals %v, want %v", closes, want)
			}
		})
	}
}

// TestCancelBeforeSize cancels Run between the dial and its first write,
// the terminal's size. coder/websocket fails that write in one of two
// ways, at random: before it has the stream's locks, leaving the stream
// open, or once it has them, by closing the stream. Either way Run ends
// with the cancel and the stream is closed, so the test makes the
// cancel 16 times.
func TestCancelBeforeSize(t *testing.T) {
	for range 16 {
		checkGoroutines(t)
		s := newServer(t, kubetest.Options{})
		b := newBackend(t, config(s))
		ctx, cancel := context.WithCancel(context.Background())
		kubernetes.OnDialed(t, cancel)
		if err := b.Run(ctx, &backendtest.Sink{}); !errors.Is(err, context.Canceled) {
			t.Fatalf("Run = %v, want context.Canceled", err)
		}
		e := accept(t, s)
		select {
		case <-e.Done():
		case <-time.After(3 * time.Second):
			t.Fatal("the stream is still open after the size could not be sent")
		}
	}
}

// TestCancelRace reproduces the race in which a cancelled Run ended with
// the reader's error instead of the cancel's. coder/websocket closes the
// stream when the context of a write in progress ends, which ends the
// reader; when that happens before serve waits, serve finds both the
// cancel and the reader's end ready, and picks one at random. The hook
// cancels, closes the stream as coder/websocket does, and waits for the
// reader to end, and the test does so 32 times, so that serve's pick
// goes both ways.
func TestCancelRace(t *testing.T) {
	for range 32 {
		checkGoroutines(t)
		s := newServer(t, kubetest.Options{})
		b := newBackend(t, config(s))
		ctx, cancel := context.WithCancel(context.Background())
		kubernetes.OnServing(t, func(conn *websocket.Conn, ended chan error) {
			cancel()
			_ = conn.CloseNow()
			ended <- <-ended
		})
		if err := b.Run(ctx, &backendtest.Sink{}); !errors.Is(err, context.Canceled) {
			t.Fatalf("Run = %v, want context.Canceled", err)
		}
	}
}

// TestNoTTY checks a command without a terminal: stderr apart, line
// feeds turned into CR LF, and sizes kept to the session.
func TestNoTTY(t *testing.T) {
	checkGoroutines(t)
	s := newServer(t, kubetest.Options{})
	cfg := config(s)
	cfg.NoTTY = true
	cfg.Command = []string{"tail", "-f", "/var/log/app.log"}
	b := newBackend(t, cfg)
	sink := &backendtest.Sink{}
	r := start(t, b, sink)
	e := accept(t, s)
	if got := e.Query; got.Get("tty") != "false" || got.Get("stderr") != "true" || !slices.Equal(got["command"], cfg.Command) {
		t.Errorf("query %v", got)
	}
	eventually(t, "the size", func() bool { return sink.Resizes() == 1 })
	input(t, b, backend.Resize{Width: 90, Height: 20})
	eventually(t, "the new size", func() bool { return sink.Resizes() == 2 })
	if err := e.Stdout([]byte("a\nb")); err != nil {
		t.Fatal(err)
	}
	if err := e.Stderr([]byte("c\n\n")); err != nil {
		t.Fatal(err)
	}
	input(t, b, backend.TermInput{Data: []byte("q")})
	// No size goes to the server without a TTY: stdin comes first.
	m, err := e.Next(context.Background())
	if sm, ok := m.(kubetest.StdinMessage); err != nil || !ok || string(sm.Data) != "q" {
		t.Errorf("first message %#v, %v; want stdin q", m, err)
	}
	if err := e.Exit(1); err != nil {
		t.Fatal(err)
	}
	if err := r.wait(t); err != nil {
		t.Errorf("Run = %v", err)
	}
	if got := string(sink.Term()); got != "a\r\nbc\r\n\r\n" {
		t.Errorf("terminal %q", got)
	}
}

// TestAttach checks that attach goes to its own subresource, with no
// command.
func TestAttach(t *testing.T) {
	s := newServer(t, kubetest.Options{})
	cfg := config(s)
	cfg.Attach = true
	cfg.Container = ""
	b := newBackend(t, cfg)
	r := start(t, b, &backendtest.Sink{})
	e := accept(t, s)
	if e.Path != "/api/v1/namespaces/apps/pods/web-0/attach" || e.Query.Has("command") || e.Query.Has("container") {
		t.Errorf("request %s?%s", e.Path, e.Query.Encode())
	}
	if err := e.Exit(0); err != nil {
		t.Fatal(err)
	}
	if err := r.wait(t); err != nil {
		t.Error(err)
	}
}

// TestQueuedInput checks that input and a resize given while Run
// connects reach the stream once it opens, and the bounds of sizes.
func TestQueuedInput(t *testing.T) {
	checkGoroutines(t)
	hold := make(chan struct{})
	s := newServer(t, kubetest.Options{Hold: hold})
	b := newBackend(t, config(s))
	if err := b.Input(backend.TermInput{Data: []byte("x")}); !errors.Is(err, kubernetes.ErrNotRunning) {
		t.Errorf("Input before Run = %v, want ErrNotRunning", err)
	}
	sink := &backendtest.Sink{}
	r := start(t, b, sink)
	eventually(t, "Run", func() bool { return b.Input(backend.TermInput{Data: []byte("early")}) == nil })
	input(t, b, backend.Resize{Width: 50, Height: 10})
	eventually(t, "the upgrade", func() bool { return len(s.Requests()) == 1 })
	if sink.Resizes() != 0 {
		t.Error("a size reported before the stream opened")
	}
	close(hold)
	e := accept(t, s)
	if m := next[kubetest.ResizeMessage](t, e); m != (kubetest.ResizeMessage{Width: 50, Height: 10}) {
		t.Errorf("first size %+v, want the 50x10 given while connecting", m)
	}
	if m := next[kubetest.StdinMessage](t, e); string(m.Data) != "early" {
		t.Errorf("stdin %q", m.Data)
	}
	eventually(t, "the size", func() bool { return sink.Size().X == 50 && sink.Resizes() == 1 })
	if err := b.Input(backend.Resize{Width: 0, Height: 10}); err == nil {
		t.Error("a zero width was accepted")
	}
	if err := b.Input(backend.Resize{Width: kubernetes.MaxCols + 1, Height: 10}); err == nil {
		t.Error("a width over MaxCols was accepted")
	}
	if err := e.Exit(0); err != nil {
		t.Fatal(err)
	}
	if err := r.wait(t); err != nil {
		t.Error(err)
	}
}

// TestInputFull checks that Input fails once its queue is full, while the
// stream is not open.
func TestInputFull(t *testing.T) {
	hold := make(chan struct{})
	s := newServer(t, kubetest.Options{Hold: hold})
	b := newBackend(t, config(s))
	r := start(t, b, &backendtest.Sink{})
	eventually(t, "Run", func() bool { return b.Input(backend.TermInput{Data: []byte("0")}) == nil })
	var err error
	for i := 0; i < 100 && err == nil; i++ {
		err = b.Input(backend.TermInput{Data: []byte("x")})
	}
	if !errors.Is(err, kubernetes.ErrInputFull) {
		t.Errorf("Input = %v, want ErrInputFull", err)
	}
	r.cancel()
	if err := r.wait(t); !errors.Is(err, context.Canceled) {
		t.Errorf("Run = %v", err)
	}
	close(hold)
}

// TestRunTwice checks that a second Run while one runs fails.
func TestRunTwice(t *testing.T) {
	s := newServer(t, kubetest.Options{})
	b := newBackend(t, config(s))
	r := start(t, b, &backendtest.Sink{})
	e := accept(t, s)
	if err := b.Run(context.Background(), &backendtest.Sink{}); err == nil {
		t.Error("a second Run started")
	}
	if err := e.Exit(0); err != nil {
		t.Fatal(err)
	}
	if err := r.wait(t); err != nil {
		t.Error(err)
	}
}

// TestKeepalive checks that the backend pings the API server.
func TestKeepalive(t *testing.T) {
	kubernetes.SetKeepalive(t, 20*time.Millisecond)
	s := newServer(t, kubetest.Options{})
	b := newBackend(t, config(s))
	r := start(t, b, &backendtest.Sink{})
	e := accept(t, s)
	// The fake reads, and so answers pings, only while the test takes
	// its messages.
	go func() {
		for {
			if _, err := e.Next(context.Background()); err != nil {
				return
			}
		}
	}()
	eventually(t, "three pings", func() bool { return e.Pings() >= 3 })
	if err := e.Exit(0); err != nil {
		t.Fatal(err)
	}
	if err := r.wait(t); err != nil {
		t.Error(err)
	}
}

// pieces is a sink that records the length of each TermData call.
type pieces struct {
	*backendtest.Sink
	mu  sync.Mutex
	max int
}

func (p *pieces) TermData(b []byte) {
	p.mu.Lock()
	p.max = max(p.max, len(b))
	p.mu.Unlock()
	p.Sink.TermData(b)
}

// TestOutputBounds checks that a large message reaches the terminal in
// pieces of at most 32 KiB, and that a message over 1 MiB ends the
// session.
func TestOutputBounds(t *testing.T) {
	checkGoroutines(t)
	s := newServer(t, kubetest.Options{})
	b := newBackend(t, config(s))
	sink := &pieces{Sink: &backendtest.Sink{}}
	r := start(t, b, sink)
	e := accept(t, s)
	big := bytes.Repeat([]byte("0123456789abcdef"), (kubernetes.MaxMessage-1)/16)
	if err := e.Stdout(big); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the output", func() bool { return len(sink.Term()) == len(big) })
	if sink.max > kubernetes.ReadSize {
		t.Errorf("a TermData of %d bytes, more than %d", sink.max, kubernetes.ReadSize)
	}
	if !bytes.Equal(sink.Term(), big) {
		t.Error("the output changed on the way")
	}
	// One byte more than a message may hold, with its channel.
	_ = e.Stdout(make([]byte, kubernetes.MaxMessage))
	if err := r.wait(t); err == nil {
		t.Error("Run = nil after a message over the limit")
	}
}

// TestStatusBound checks that an error channel over 16 KiB, in one
// message or several, ends the session, and that one of exactly 16 KiB
// does not.
func TestStatusBound(t *testing.T) {
	pad := func(n int) []byte {
		st := kubetest.ExitStatus(0)
		return append(st, bytes.Repeat([]byte(" "), n-len(st))...)
	}
	for _, tc := range []struct {
		name   string
		parts  [][]byte
		tooBig bool
	}{
		{"at the bound", [][]byte{pad(kubernetes.MaxStatus)}, false},
		{"over", [][]byte{pad(kubernetes.MaxStatus + 1)}, true},
		{"over in two", [][]byte{pad(kubernetes.MaxStatus)[:100], pad(kubernetes.MaxStatus)[100:], []byte(" ")}, true},
		{"a megabyte", [][]byte{pad(kubernetes.MaxMessage - 1)}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checkGoroutines(t)
			s := newServer(t, kubetest.Options{})
			b := newBackend(t, config(s))
			r := start(t, b, &backendtest.Sink{})
			e := accept(t, s)
			for _, p := range tc.parts {
				_ = e.Send(append([]byte{kubetest.Error}, p...))
			}
			_ = e.Close()
			err := r.wait(t)
			if got := err != nil && strings.Contains(err.Error(), "status is larger than"); got != tc.tooBig {
				t.Errorf("Run = %v", err)
			}
			if !tc.tooBig && err != nil {
				t.Errorf("Run = %v, want nil", err)
			}
		})
	}
}

// TestUnknownMessages checks that messages the backend does not take are
// dropped, and that a text message ends the session.
func TestUnknownMessages(t *testing.T) {
	checkGoroutines(t)
	s := newServer(t, kubetest.Options{})
	b := newBackend(t, config(s))
	sink := &backendtest.Sink{}
	r := start(t, b, sink)
	e := accept(t, s)
	for _, m := range [][]byte{{}, {kubetest.Stdin, 'x'}, {kubetest.Resize, '{'}, {9, 9}, {kubetest.CloseSignal, kubetest.Stdout}, {kubetest.CloseSignal}} {
		if err := e.Send(m); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.Stdout([]byte("ok")); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the output", func() bool { return string(sink.Term()) == "ok" })
	if err := e.SendText([]byte("1hello")); err != nil {
		t.Fatal(err)
	}
	if err := r.wait(t); err == nil || !strings.Contains(err.Error(), "text message") {
		t.Errorf("Run = %v, want an error for the text message", err)
	}
}

// TestSubprotocol checks that a server that picks neither v5 nor v4 is
// refused.
func TestSubprotocol(t *testing.T) {
	checkGoroutines(t)
	s := newServer(t, kubetest.Options{Protocols: []string{"base64.channel.k8s.io"}})
	b := newBackend(t, config(s))
	err := run(t, b, &backendtest.Sink{})
	if err == nil || !strings.Contains(err.Error(), `chose the subprotocol ""`) {
		t.Errorf("Run = %v, want a refusal of the empty subprotocol", err)
	}
}

// TestRefused checks the API server's refusals: a wrong token, no token,
// RBAC, a missing pod; each an error with the status, and none with the
// token in it.
func TestRefused(t *testing.T) {
	for _, tc := range []struct {
		name   string
		opts   kubetest.Options
		token  kubernetes.Token
		code   int
		reason string
		msg    string
	}{
		{"wrong token", kubetest.Options{Token: "other"}, token, 401, "Unauthorized", "Unauthorized"},
		{"no token", kubetest.Options{Token: token}, "", 401, "Unauthorized", "Unauthorized"},
		{"forbidden", kubetest.Options{Refuse: 403, RefuseMessage: `pods "web-0" is forbidden: User "system:serviceaccount:occamy:term" cannot create resource "pods/exec"`}, token, 403, "Forbidden", "cannot create resource"},
		{"not found", kubetest.Options{Refuse: 404, RefuseMessage: `pods "web-0" not found`}, token, 404, "NotFound", "not found"},
		{"no status", kubetest.Options{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, strings.Repeat("x", 5000), http.StatusBadGateway)
		})}, token, 502, "Bad Gateway", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checkGoroutines(t)
			s := newServer(t, tc.opts)
			var l logs
			cfg := config(s)
			cfg.Token = tc.token
			cfg.Logger = l.logger()
			b := newBackend(t, cfg)
			err := run(t, b, &backendtest.Sink{})
			var se *kubernetes.StatusError
			if !errors.As(err, &se) || se.HTTPStatus != tc.code || se.Reason != tc.reason || !strings.Contains(se.Message, tc.msg) {
				t.Fatalf("Run = %v (%+v), want the API server's %d %s", err, se, tc.code, tc.reason)
			}
			if strings.Contains(err.Error(), token) || strings.Contains(l.String(), token) {
				t.Errorf("the token leaked: %v\n%s", err, l.String())
			}
			if len(err.Error()) > 1024 {
				t.Errorf("an error of %d bytes", len(err.Error()))
			}
		})
	}
}

// TestRedirect checks that a redirect is not followed, so that the token
// goes nowhere else.
func TestRedirect(t *testing.T) {
	checkGoroutines(t)
	other := newServer(t, kubetest.Options{})
	s := newServer(t, kubetest.Options{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://"+other.Addr()+r.URL.RequestURI(), http.StatusTemporaryRedirect)
	})})
	cfg := config(s)
	cfg.TLS.Pins = append(cfg.TLS.Pins, other.Fingerprint())
	b := newBackend(t, cfg)
	err := run(t, b, &backendtest.Sink{})
	var se *kubernetes.StatusError
	if !errors.As(err, &se) || se.HTTPStatus != http.StatusTemporaryRedirect {
		t.Errorf("Run = %v, want the redirect refused", err)
	}
	if n := len(other.Requests()); n != 0 {
		t.Errorf("the redirect was followed: %d requests", n)
	}
}

// TestTLSVersion checks that an API server that speaks nothing newer
// than TLS 1.1 is refused before any request, and so the token, reaches
// it.
func TestTLSVersion(t *testing.T) {
	checkGoroutines(t)
	s := newServer(t, kubetest.Options{MaxVersion: tls.VersionTLS11})
	b := newBackend(t, config(s))
	if err := run(t, b, &backendtest.Sink{}); err == nil || !strings.Contains(err.Error(), "protocol version") {
		t.Errorf("Run = %v, want TLS 1.1 refused", err)
	}
	if n := len(s.Requests()); n != 0 {
		t.Errorf("%d requests reached a TLS 1.1 server", n)
	}
}

// TestNoProxy checks that the API server is dialled directly, whatever
// HTTPS_PROXY says, so that no proxy from the environment sees where the
// token goes. A loopback server is never proxied, so the transport is
// checked instead.
func TestNoProxy(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://proxy.invalid:3128")
	if kubernetes.HasProxy(newBackend(t, kubernetes.Config{Addr: "k8s.example.com", Pod: "p"})) {
		t.Error("the backend's requests may go through a proxy")
	}
}

// writeFile writes data to a file in a temporary directory.
func writeFile(t *testing.T, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestCertificate checks the API server's certificate against each
// policy. A refused certificate fails the session before any request,
// and so the token, reaches the server.
func TestCertificate(t *testing.T) {
	s := newServer(t, kubetest.Options{})
	cert, err := kubetest.SelfSigned("127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	other := newServer(t, kubetest.Options{Certificate: cert})
	if other.Fingerprint() == s.Fingerprint() {
		t.Fatal("the servers share a certificate")
	}
	roots := x509.NewCertPool()
	roots.AddCert(s.Certificate())
	for _, tc := range []struct {
		name     string
		tls      kubernetes.TLS
		ok       bool
		insecure bool // whether a warning is logged
	}{
		{"system roots", kubernetes.TLS{}, false, false},
		{"pinned", kubernetes.TLS{Pins: []string{s.Fingerprint()}}, true, false},
		{"pinned in base64", kubernetes.TLS{Pins: []string{base64Pin(s.Certificate())}}, true, false},
		{"pinned in PEM", kubernetes.TLS{Pins: []string{string(s.CertificatePEM())}}, true, false},
		{"another pin", kubernetes.TLS{Pins: []string{other.Fingerprint()}}, false, false},
		{"CA", kubernetes.TLS{RootCAs: roots}, true, false},
		{"CA, wrong name", kubernetes.TLS{RootCAs: roots, ServerName: "api.example.org"}, false, false},
		{"CA, its name", kubernetes.TLS{RootCAs: roots, ServerName: "example.com"}, true, false},
		{"insecure", kubernetes.TLS{InsecureSkipVerify: true}, true, true},
		{"insecure, pinned", kubernetes.TLS{InsecureSkipVerify: true, Pins: []string{s.Fingerprint()}}, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checkGoroutines(t)
			var l logs
			cfg := config(s)
			cfg.TLS = tc.tls
			cfg.Logger = l.logger()
			b := newBackend(t, cfg)
			before := len(s.Requests())
			r := start(t, b, &backendtest.Sink{})
			if !tc.ok {
				err := r.wait(t)
				var ce *kubernetes.CertificateError
				if !errors.As(err, &ce) || ce.Fingerprint != s.Fingerprint() {
					t.Fatalf("Run = %v, want a CertificateError with the fingerprint", err)
				}
				if n := len(s.Requests()) - before; n != 0 {
					t.Errorf("%d requests reached the server after its certificate was refused", n)
				}
				if strings.Contains(err.Error(), token) {
					t.Errorf("the token leaked: %v", err)
				}
				return
			}
			e := accept(t, s)
			if got := e.Header.Get("Authorization"); got != "Bearer "+token {
				t.Errorf("Authorization %q", got)
			}
			if err := e.Exit(0); err != nil {
				t.Fatal(err)
			}
			if err := r.wait(t); err != nil {
				t.Error(err)
			}
			warned := strings.Contains(l.String(), "accepting an unverified certificate") && strings.Contains(l.String(), s.Fingerprint())
			if warned != tc.insecure {
				t.Errorf("warning logged: %v, want %v:\n%s", warned, tc.insecure, l.String())
			}
		})
	}
}

// TestClientCertificate checks login with a client certificate.
func TestClientCertificate(t *testing.T) {
	ca, err := newCA()
	if err != nil {
		t.Fatal(err)
	}
	s := newServer(t, kubetest.Options{ClientCAs: ca.pool()})
	certPEM, keyPEM, err := ca.issue("occamy")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config(s)
	cfg.Token = ""
	cfg.CertFile = writeFile(t, "client.crt", certPEM)
	cfg.KeyFile = writeFile(t, "client.key", keyPEM)
	b := newBackend(t, cfg)
	r := start(t, b, &backendtest.Sink{})
	e := accept(t, s)
	if e.ClientCertificate == nil || e.ClientCertificate.Subject.CommonName != "occamy" {
		t.Errorf("client certificate %v", e.ClientCertificate)
	}
	if e.Header.Get("Authorization") != "" {
		t.Error("an Authorization header without a token")
	}
	if err := e.Exit(0); err != nil {
		t.Fatal(err)
	}
	if err := r.wait(t); err != nil {
		t.Error(err)
	}

	// Without the certificate the handshake fails.
	cfg.CertFile, cfg.KeyFile = "", ""
	if err := run(t, newBackend(t, cfg), &backendtest.Sink{}); err == nil {
		t.Error("Run = nil without the client certificate")
	}
}

// TestPromptToken checks that the owner is asked for the token when the
// configuration says so, and that an empty answer sends none.
func TestPromptToken(t *testing.T) {
	for _, tc := range []struct {
		name, answer string
		header       string
		fails        bool
	}{
		{"token", " " + token + "\n", "Bearer " + token, false},
		{"empty", "", "", false},
		{"bad", "a b", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checkGoroutines(t)
			s := newServer(t, kubetest.Options{})
			cfg := config(s)
			cfg.Token, cfg.PromptToken = "", true
			b := newBackend(t, cfg)
			sink := &backendtest.Sink{Answer: func(p backend.Prompt) (backend.PromptReply, error) {
				return backend.PromptReply{Answers: []string{tc.answer}}, nil
			}}
			r := start(t, b, sink)
			if tc.fails {
				err := r.wait(t)
				if err == nil || strings.Contains(err.Error(), tc.answer) {
					t.Errorf("Run = %v, want an error that does not quote the answer", err)
				}
				if len(s.Requests()) != 0 {
					t.Error("a request went out with a bad token")
				}
				return
			}
			e := accept(t, s)
			if got := e.Header.Get("Authorization"); got != tc.header {
				t.Errorf("Authorization %q, want %q", got, tc.header)
			}
			ps := sink.Prompts()
			if len(ps) != 1 || len(ps[0].Fields) != 1 || !ps[0].Fields[0].Secret || !strings.Contains(ps[0].Instruction, "apps/web-0/app") {
				t.Errorf("prompts %+v", ps)
			}
			if err := e.Exit(0); err != nil {
				t.Fatal(err)
			}
			if err := r.wait(t); err != nil {
				t.Error(err)
			}
		})
	}

	// No answer fails Run.
	s := newServer(t, kubetest.Options{})
	cfg := config(s)
	cfg.Token, cfg.PromptToken = "", true
	if err := run(t, newBackend(t, cfg), &backendtest.Sink{}); !errors.Is(err, backendtest.ErrNoAnswer) {
		t.Errorf("Run = %v, want the prompt's error", err)
	}
}

// TestNew checks the configurations New refuses, among them every name
// that could reach another path of the API server.
func TestNew(t *testing.T) {
	base := kubernetes.Config{Addr: "k8s.example:6443", Pod: "web-0"}
	for _, tc := range []struct {
		name string
		edit func(*kubernetes.Config)
	}{
		{"no address", func(c *kubernetes.Config) { c.Addr = "" }},
		{"a URL", func(c *kubernetes.Config) { c.Addr = "https://k8s.example" }},
		{"a path", func(c *kubernetes.Config) { c.Addr = "k8s.example/api" }},
		{"no pod", func(c *kubernetes.Config) { c.Pod = "" }},
		{"pod with a slash", func(c *kubernetes.Config) { c.Pod = "web-0/exec?command=id&x=" }},
		{"pod going up", func(c *kubernetes.Config) { c.Pod = "../../../api/v1/secrets" }},
		{"pod of dots", func(c *kubernetes.Config) { c.Pod = ".." }},
		{"pod in upper case", func(c *kubernetes.Config) { c.Pod = "Web" }},
		{"pod escaped", func(c *kubernetes.Config) { c.Pod = "web%2F0" }},
		{"pod too long", func(c *kubernetes.Config) { c.Pod = strings.Repeat("a.", 127) + "a" }},
		{"pod with a label too long", func(c *kubernetes.Config) { c.Pod = strings.Repeat("a", 64) + ".b" }},
		{"namespace with a slash", func(c *kubernetes.Config) { c.Namespace = "kube-system/pods/x" }},
		{"namespace with a dot", func(c *kubernetes.Config) { c.Namespace = "a.b" }},
		{"namespace too long", func(c *kubernetes.Config) { c.Namespace = strings.Repeat("a", 64) }},
		{"namespace ending in a dash", func(c *kubernetes.Config) { c.Namespace = "apps-" }},
		{"container with a query", func(c *kubernetes.Config) { c.Container = "app&command=id" }},
		{"container with a space", func(c *kubernetes.Config) { c.Container = "my app" }},
		{"attach and a command", func(c *kubernetes.Config) { c.Attach, c.Command = true, []string{"sh"} }},
		{"empty command", func(c *kubernetes.Config) { c.Command = []string{""} }},
		{"token with a line break", func(c *kubernetes.Config) { c.Token = "abc\r\nX-Evil: 1" }},
		{"token with a space", func(c *kubernetes.Config) { c.Token = "abc def" }},
		{"token and a prompt", func(c *kubernetes.Config) { c.Token, c.PromptToken = "abc", true }},
		{"certificate without a key", func(c *kubernetes.Config) { c.CertFile = "x.crt" }},
		{"key without a certificate", func(c *kubernetes.Config) { c.KeyFile = "x.key" }},
		{"missing certificate", func(c *kubernetes.Config) { c.CertFile, c.KeyFile = "/nonexistent.crt", "/nonexistent.key" }},
		{"bad pin", func(c *kubernetes.Config) { c.TLS.Pins = []string{"nope"} }},
		{"pins and roots", func(c *kubernetes.Config) {
			c.TLS.Pins, c.TLS.RootCAs = []string{strings.Repeat("00", 32)}, x509.NewCertPool()
		}},
		{"zero columns", func(c *kubernetes.Config) { c.Cols, c.Rows = -1, 24 }},
		{"too many rows", func(c *kubernetes.Config) { c.Rows = kubernetes.MaxRows + 1 }},
		{"negative timeout", func(c *kubernetes.Config) { c.DialTimeout = -time.Second }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base
			tc.edit(&cfg)
			if _, err := kubernetes.New(cfg); err == nil {
				t.Errorf("New(%+v) = nil error", cfg)
			} else if strings.Contains(err.Error(), "X-Evil") {
				t.Errorf("the error quotes the token: %v", err)
			}
		})
	}
	for _, ok := range []kubernetes.Config{
		base,
		{Addr: "10.0.0.1", Pod: "a", Namespace: "kube-system", Container: "c-1"},
		{Addr: "[fd00::1]:6443", Pod: "web-0.example.svc", Token: "eyJhbGciOiJSUzI1NiJ9.e30.c2ln"},
		{Addr: "k8s.example", Pod: strings.Repeat("a", 63) + "." + strings.Repeat("b", 63), Namespace: strings.Repeat("n", 63)},
		{Addr: "k8s.example", Pod: "p", Attach: true, NoTTY: true},
	} {
		if _, err := kubernetes.New(ok); err != nil {
			t.Errorf("New(%+v) = %v", ok, err)
		}
	}
}

// TestTokenRedacted checks that a token prints as [redacted] however it
// is formatted, alone or in a Config, and in slog.
func TestTokenRedacted(t *testing.T) {
	cfg := kubernetes.Config{Addr: "k8s.example", Pod: "p", Token: token}
	var out bytes.Buffer
	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%X", "%d", "%T"} {
		fmt.Fprintf(&out, verb+"\n", cfg)
		fmt.Fprintf(&out, verb+"\n", cfg.Token)
		fmt.Fprintf(&out, verb+"\n", &cfg)
	}
	fmt.Fprintln(&out, cfg.Token, []kubernetes.Token{token}, map[string]kubernetes.Token{"t": token})
	l := slog.New(slog.NewJSONHandler(&out, nil))
	l.Info("x", "token", cfg.Token, "config", cfg)
	l = slog.New(slog.NewTextHandler(&out, nil))
	l.Info("x", "token", cfg.Token, "config", cfg)
	if strings.Contains(out.String(), token) || strings.Contains(out.String(), fmt.Sprintf("%x", token)) {
		t.Errorf("the token printed:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "[redacted]") {
		t.Errorf("no [redacted] in\n%s", out.String())
	}
}

func TestUnlocksDeferred(t *testing.T) {
	backendtest.UnlocksDeferred(t, ".", "b.mu")
	backendtest.UnlocksDeferred(t, ".", "r.mu")
}
