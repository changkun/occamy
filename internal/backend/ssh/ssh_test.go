// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package ssh_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"log/slog"
	"net"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	gossh "golang.org/x/crypto/ssh"

	"changkun.de/x/occamy/internal/backend"
	"changkun.de/x/occamy/internal/backend/backendtest"
	"changkun.de/x/occamy/internal/backend/ssh"
)

// timeout bounds every wait in these tests.
const timeout = 10 * time.Second

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

// ownFrame matches a stack frame in package ssh, not in its tests.
var ownFrame = regexp.MustCompile(`changkun\.de/x/occamy/internal/backend/ssh\.`)

// checkStopped fails the test if a goroutine is still running code of
// package ssh, which must not outlive Run. A goroutine that has just
// signalled its end may take a moment to return.
func checkStopped(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		var left []string
		for g := range strings.SplitSeq(string(stacks()), "\n\n") {
			// A goroutine that package ssh created but that runs only
			// x/crypto or runtime code is the library's own.
			var frames []string
			for line := range strings.Lines(g) {
				if !strings.HasPrefix(line, "created by ") {
					frames = append(frames, line)
				}
			}
			if ownFrame.MatchString(strings.Join(frames, "")) {
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

// runner runs a backend in the background.
type runner struct {
	cancel context.CancelFunc
	done   chan struct{}
	err    error
}

// start runs b on sink until the test ends or the runner is stopped.
func start(t *testing.T, b *ssh.Backend, sink backend.Sink) *runner {
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

// waitFor waits until cond holds.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// waitShell waits until the backend has started the shell.
func waitShell(t *testing.T, sink *backendtest.Sink) {
	t.Helper()
	waitFor(t, "the shell", func() bool { return sink.Resizes() > 0 })
}

// logBuffer collects log output from several goroutines.
type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *logBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *logBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

func newLogger(l *logBuffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(l, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// pin returns the host key entry for the server's first host key.
func pin(s *testServer) []string {
	return []string{authorizedKey(s.hostKeys[0].PublicKey())}
}

func newBackend(t *testing.T, cfg ssh.Config) *ssh.Backend {
	t.Helper()
	b, err := ssh.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestPasswordAndEcho(t *testing.T) {
	checkGoroutines(t)
	srv := newServer(t, serverOptions{password: "hunter2", stderr: "motd on stderr\r\n"})
	var logs logBuffer
	b := newBackend(t, ssh.Config{
		Addr:     srv.addr,
		User:     "alice",
		Password: "hunter2",
		HostKeys: pin(srv),
		Logger:   newLogger(&logs),
	})
	if c := b.Class(); c != backend.Terminal {
		t.Errorf("Class() = %v, want terminal", c)
	}
	sink := &backendtest.Sink{}
	r := start(t, b, sink)
	waitShell(t, sink)

	if got, want := sink.Size(), image.Pt(ssh.DefaultCols, ssh.DefaultRows); got != want {
		t.Errorf("size %v, want %v", got, want)
	}
	if got := srv.Term(); got != ssh.DefaultTerm {
		t.Errorf("TERM %q, want %q", got, ssh.DefaultTerm)
	}
	if got := srv.Sizes(); len(got) != 1 || got[0] != image.Pt(80, 24) {
		t.Errorf("pty-req size %v, want [(80,24)]", got)
	}
	waitFor(t, "stderr", func() bool { return bytes.Contains(sink.Term(), []byte("motd on stderr")) })

	if err := b.Input(backend.TermInput{Data: []byte("echo hi\r")}); err != nil {
		t.Fatal(err)
	}
	if err := b.Input(backend.Text{Text: "grüße"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the echo", func() bool { return bytes.Contains(sink.Term(), []byte("echo hi\rgrüße")) })

	// Inputs that mean nothing to a shell are ignored.
	for _, ev := range []backend.InputEvent{
		backend.Key{Down: true, Keysym: 'a'},
		backend.Pointer{X: 1, Y: 2},
		backend.Wheel{DY: 1},
		backend.Clipboard{MIME: "text/plain", Data: []byte("x")},
	} {
		if err := b.Input(ev); err != nil {
			t.Errorf("Input(%T) = %v", ev, err)
		}
	}
	b.Demand(backend.Demand{Visible: false})

	if err := r.stop(t); !errors.Is(err, context.Canceled) {
		t.Errorf("Run = %v, want context.Canceled", err)
	}
	if n := len(sink.Prompts()); n != 0 {
		t.Errorf("%d prompts, want none", n)
	}
	if strings.Contains(logs.String(), "hunter2") {
		t.Errorf("the password was logged:\n%s", logs.String())
	}
}

func TestPublicKey(t *testing.T) {
	checkGoroutines(t)
	key := newEd25519(t)
	srv := newServer(t, serverOptions{clientKey: newSigner(t, key).PublicKey()})
	b := newBackend(t, ssh.Config{
		Addr:     srv.addr,
		User:     "alice",
		KeyFiles: []string{writeKeyFile(t, key, "")},
		HostKeys: pin(srv),
	})
	sink := &backendtest.Sink{}
	r := start(t, b, sink)
	waitShell(t, sink)
	if err := r.stop(t); !errors.Is(err, context.Canceled) {
		t.Errorf("Run = %v, want context.Canceled", err)
	}
	if n := len(sink.Prompts()); n != 0 {
		t.Errorf("%d prompts, want none", n)
	}
}

func TestKeyboardInteractive(t *testing.T) {
	checkGoroutines(t)
	srv := newServer(t, serverOptions{questions: []question{
		{prompt: "Password: ", echo: false, answer: "hunter2"},
		{prompt: "Pet's name: ", echo: true, answer: "Rex"},
	}})
	b := newBackend(t, ssh.Config{Addr: srv.addr, User: "alice", HostKeys: pin(srv)})
	sink := &backendtest.Sink{Answer: func(p backend.Prompt) (backend.PromptReply, error) {
		return backend.PromptReply{Answers: []string{"hunter2", "Rex"}}, nil
	}}
	r := start(t, b, sink)
	waitShell(t, sink)
	if err := r.stop(t); !errors.Is(err, context.Canceled) {
		t.Errorf("Run = %v, want context.Canceled", err)
	}

	prompts := sink.Prompts()
	if len(prompts) != 1 {
		t.Fatalf("%d prompts, want 1", len(prompts))
	}
	p := prompts[0]
	if p.Name != "Login" || p.Instruction != "Answer the questions." {
		t.Errorf("prompt %q, %q; want the server's name and instruction", p.Name, p.Instruction)
	}
	want := []backend.PromptField{{Label: "Password: ", Secret: true}, {Label: "Pet's name: ", Secret: false}}
	if len(p.Fields) != len(want) || p.Fields[0] != want[0] || p.Fields[1] != want[1] {
		t.Errorf("fields %+v, want %+v", p.Fields, want)
	}
}

func TestKeyboardInteractiveUnanswered(t *testing.T) {
	checkGoroutines(t)
	srv := newServer(t, serverOptions{questions: []question{{prompt: "Password: ", answer: "hunter2"}}})
	b := newBackend(t, ssh.Config{Addr: srv.addr, User: "alice", HostKeys: pin(srv)})
	sink := &backendtest.Sink{} // no Answer
	err := start(t, b, sink).wait(t)
	if !errors.Is(err, backendtest.ErrNoAnswer) {
		t.Errorf("Run = %v, want ErrNoAnswer", err)
	}
	if n := srv.Shells(); n != 0 {
		t.Errorf("%d shells started without authentication", n)
	}
}

func TestPromptAnswerCount(t *testing.T) {
	checkGoroutines(t)
	srv := newServer(t, serverOptions{questions: []question{
		{prompt: "Password: ", answer: "hunter2"},
		{prompt: "Code: ", answer: "123"},
	}})
	b := newBackend(t, ssh.Config{Addr: srv.addr, User: "alice", HostKeys: pin(srv)})
	sink := &backendtest.Sink{Answer: func(backend.Prompt) (backend.PromptReply, error) {
		return backend.PromptReply{Answers: []string{"hunter2"}}, nil
	}}
	err := start(t, b, sink).wait(t)
	if err == nil || !strings.Contains(err.Error(), "1 answers for 2 questions") {
		t.Errorf("Run = %v, want an error about the answer count", err)
	}
}

func TestPasswordPrompt(t *testing.T) {
	checkGoroutines(t)
	srv := newServer(t, serverOptions{password: "hunter2"})
	b := newBackend(t, ssh.Config{Addr: srv.addr, User: "alice", HostKeys: pin(srv), Cols: 100, Rows: 30})
	sink := &backendtest.Sink{}
	sink.Answer = func(p backend.Prompt) (backend.PromptReply, error) {
		// The owner resizes and types while asked; both apply once the
		// shell starts.
		if err := b.Input(backend.Resize{Width: 120, Height: 50}); err != nil {
			t.Errorf("Input(Resize) while connecting = %v", err)
		}
		if err := b.Input(backend.TermInput{Data: []byte("typed ahead")}); err != nil {
			t.Errorf("Input(TermInput) while connecting = %v", err)
		}
		return backend.PromptReply{Answers: []string{"hunter2"}}, nil
	}
	r := start(t, b, sink)
	waitShell(t, sink)
	waitFor(t, "the typed-ahead echo", func() bool { return bytes.Contains(sink.Term(), []byte("typed ahead")) })

	prompts := sink.Prompts()
	if len(prompts) != 1 || len(prompts[0].Fields) != 1 || !prompts[0].Fields[0].Secret {
		t.Fatalf("prompts %+v, want one with a secret field", prompts)
	}
	if got, want := srv.Sizes(), []image.Point{image.Pt(120, 50)}; len(got) != 1 || got[0] != want[0] {
		t.Errorf("server sizes %v, want %v", got, want)
	}
	if got := sink.Size(); got != image.Pt(120, 50) {
		t.Errorf("size %v, want 120x50", got)
	}
	if err := r.stop(t); !errors.Is(err, context.Canceled) {
		t.Errorf("Run = %v, want context.Canceled", err)
	}
}

func TestHostKeyAccepted(t *testing.T) {
	ed := newSigner(t, newEd25519(t))
	ec := newSigner(t, newECDSA(t))
	other := newSigner(t, newEd25519(t))
	for _, tt := range []struct {
		name string
		pins []string
	}{
		// x/crypto prefers ECDSA by default; the Ed25519 pin must still
		// match a server that has both.
		{"ed25519 key", []string{authorizedKey(ed.PublicKey())}},
		{"ecdsa key", []string{authorizedKey(ec.PublicKey())}},
		{"ed25519 fingerprint", []string{gossh.FingerprintSHA256(ed.PublicKey())}},
		{"one of several", []string{authorizedKey(other.PublicKey()), gossh.FingerprintSHA256(ed.PublicKey())}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			checkGoroutines(t)
			srv := newServer(t, serverOptions{password: "pw", hostKeys: []gossh.Signer{ec, ed}})
			b := newBackend(t, ssh.Config{Addr: srv.addr, User: "alice", Password: "pw", HostKeys: tt.pins})
			sink := &backendtest.Sink{}
			r := start(t, b, sink)
			waitShell(t, sink)
			if err := r.stop(t); !errors.Is(err, context.Canceled) {
				t.Errorf("Run = %v, want context.Canceled", err)
			}
		})
	}
}

func TestHostKeyRefused(t *testing.T) {
	other := newSigner(t, newEd25519(t)).PublicKey()
	for _, tt := range []struct {
		name string
		pins []string
	}{
		{"key", []string{authorizedKey(other)}},
		{"fingerprint", []string{gossh.FingerprintSHA256(other)}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			checkGoroutines(t)
			srv := newServer(t, serverOptions{password: "pw"})
			b := newBackend(t, ssh.Config{Addr: srv.addr, User: "alice", Password: "pw", HostKeys: tt.pins})
			err := start(t, b, &backendtest.Sink{}).wait(t)
			fp := gossh.FingerprintSHA256(srv.hostKeys[0].PublicKey())
			var hk *ssh.HostKeyError
			if !errors.As(err, &hk) {
				t.Fatalf("Run = %v, want a HostKeyError", err)
			}
			if hk.Fingerprint != fp || hk.Type != gossh.KeyAlgoED25519 {
				t.Errorf("HostKeyError %+v, want %s %s", hk, gossh.KeyAlgoED25519, fp)
			}
			if !strings.Contains(err.Error(), fp) {
				t.Errorf("error %q does not name the fingerprint %s", err, fp)
			}
			if n := srv.Shells(); n != 0 {
				t.Errorf("%d shells started", n)
			}
		})
	}
}

func TestInsecureHostKey(t *testing.T) {
	checkGoroutines(t)
	srv := newServer(t, serverOptions{password: "pw"})
	var logs logBuffer
	b := newBackend(t, ssh.Config{
		Addr:                  srv.addr,
		User:                  "alice",
		Password:              "pw",
		InsecureIgnoreHostKey: true,
		Logger:                newLogger(&logs),
	})
	sink := &backendtest.Sink{}
	r := start(t, b, sink)
	waitShell(t, sink)
	if err := r.stop(t); !errors.Is(err, context.Canceled) {
		t.Errorf("Run = %v, want context.Canceled", err)
	}
	fp := gossh.FingerprintSHA256(srv.hostKeys[0].PublicKey())
	out := logs.String()
	if n := strings.Count(out, "level=WARN"); n != 1 {
		t.Errorf("%d warnings, want 1:\n%s", n, out)
	}
	if !strings.Contains(out, fp) {
		t.Errorf("the warning does not name the fingerprint %s:\n%s", fp, out)
	}
}

func TestResize(t *testing.T) {
	checkGoroutines(t)
	srv := newServer(t, serverOptions{password: "pw"})
	b := newBackend(t, ssh.Config{Addr: srv.addr, User: "alice", Password: "pw", HostKeys: pin(srv)})
	sink := &backendtest.Sink{}
	r := start(t, b, sink)
	waitShell(t, sink)

	if err := b.Input(backend.Resize{Width: 132, Height: 43}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the window change", func() bool {
		s := srv.Sizes()
		return len(s) == 2 && s[1] == image.Pt(132, 43) && sink.Size() == image.Pt(132, 43)
	})
	for _, bad := range []backend.Resize{{Width: 0, Height: 10}, {Width: 10, Height: -1}, {Width: ssh.MaxSize + 1, Height: 10}} {
		if err := b.Input(bad); err == nil {
			t.Errorf("Input(%+v) = nil, want an error", bad)
		}
	}
	if err := r.stop(t); !errors.Is(err, context.Canceled) {
		t.Errorf("Run = %v, want context.Canceled", err)
	}
}

func TestShellExit(t *testing.T) {
	for _, status := range []uint32{0, 3} {
		t.Run(fmt.Sprint("status ", status), func(t *testing.T) {
			checkGoroutines(t)
			srv := newServer(t, serverOptions{password: "pw"})
			b := newBackend(t, ssh.Config{Addr: srv.addr, User: "alice", Password: "pw", HostKeys: pin(srv)})
			sink := &backendtest.Sink{}
			r := start(t, b, sink)
			waitShell(t, sink)
			srv.end(ending{output: "logout\r\n", status: status})
			if err := r.wait(t); err != nil {
				t.Errorf("Run = %v after exit status %d, want nil", err, status)
			}
			// The shell's last output arrives before Run returns.
			if !bytes.HasSuffix(sink.Term(), []byte("logout\r\n")) {
				t.Errorf("terminal %q lacks the last output", sink.Term())
			}
			if err := b.Input(backend.TermInput{Data: []byte("x")}); !errors.Is(err, ssh.ErrNotRunning) {
				t.Errorf("Input after Run = %v, want ErrNotRunning", err)
			}
		})
	}
}

func TestSessionLost(t *testing.T) {
	for _, tt := range []struct {
		name string
		end  func(*testServer)
	}{
		{"no exit status", func(s *testServer) { s.end(ending{noStatus: true}) }},
		{"connection dropped", (*testServer).drop},
	} {
		t.Run(tt.name, func(t *testing.T) {
			checkGoroutines(t)
			srv := newServer(t, serverOptions{password: "pw"})
			b := newBackend(t, ssh.Config{Addr: srv.addr, User: "alice", Password: "pw", HostKeys: pin(srv)})
			sink := &backendtest.Sink{}
			r := start(t, b, sink)
			waitShell(t, sink)
			tt.end(srv)
			err := r.wait(t)
			if err == nil || errors.Is(err, context.Canceled) {
				t.Errorf("Run = %v, want an error", err)
			}
		})
	}
}

func TestCancelDuringHandshake(t *testing.T) {
	checkGoroutines(t)
	// A server that accepts and never speaks.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err == nil {
			accepted <- c
		}
		close(accepted)
	}()
	t.Cleanup(func() {
		ln.Close()
		for c := range accepted {
			c.Close()
		}
	})

	b := newBackend(t, ssh.Config{Addr: ln.Addr().String(), User: "alice", InsecureIgnoreHostKey: true})
	r := start(t, b, &backendtest.Sink{})
	waitFor(t, "the connection", func() bool { return len(accepted) > 0 })
	begin := time.Now()
	if err := r.stop(t); !errors.Is(err, context.Canceled) {
		t.Errorf("Run = %v, want context.Canceled", err)
	}
	// Generous for loaded machines; without the cancel, Run would wait
	// for the server forever.
	if d := time.Since(begin); d > 5*time.Second {
		t.Errorf("Run took %v to return after cancel", d)
	}
}

func TestDialFailure(t *testing.T) {
	checkGoroutines(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	b := newBackend(t, ssh.Config{Addr: addr, User: "alice", InsecureIgnoreHostKey: true})
	err = start(t, b, &backendtest.Sink{}).wait(t)
	if err == nil || errors.Is(err, context.Canceled) {
		t.Errorf("Run = %v, want a dial error", err)
	}
}

func TestInputQueueFull(t *testing.T) {
	checkGoroutines(t)
	srv := newServer(t, serverOptions{password: "pw", ignoreInput: true})
	b := newBackend(t, ssh.Config{Addr: srv.addr, User: "alice", Password: "pw", HostKeys: pin(srv)})
	sink := &backendtest.Sink{}
	r := start(t, b, sink)
	waitShell(t, sink)

	// The server never reads, so once the SSH window (2 MiB) is used up
	// the writer blocks and the queue fills.
	chunk := bytes.Repeat([]byte("x"), 32<<10)
	full := false
	for range 1000 {
		begin := time.Now()
		err := b.Input(backend.TermInput{Data: chunk})
		// Generous for loaded machines; a blocking Input would never
		// return, since the server never reads.
		if d := time.Since(begin); d > 5*time.Second {
			t.Fatalf("Input blocked for %v", d)
		}
		if errors.Is(err, ssh.ErrInputFull) {
			full = true
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if !full {
		t.Fatal("the input queue never filled")
	}
	if err := r.stop(t); !errors.Is(err, context.Canceled) {
		t.Errorf("Run = %v, want context.Canceled", err)
	}
}

func TestInputNotRunning(t *testing.T) {
	b := newBackend(t, ssh.Config{Addr: "127.0.0.1:1", User: "alice", InsecureIgnoreHostKey: true})
	for _, ev := range []backend.InputEvent{
		backend.TermInput{Data: []byte("x")},
		backend.Text{Text: "x"},
		backend.Resize{Width: 80, Height: 24},
	} {
		if err := b.Input(ev); !errors.Is(err, ssh.ErrNotRunning) {
			t.Errorf("Input(%T) before Run = %v, want ErrNotRunning", ev, err)
		}
	}
}

func TestRunTwice(t *testing.T) {
	checkGoroutines(t)
	srv := newServer(t, serverOptions{password: "pw"})
	b := newBackend(t, ssh.Config{Addr: srv.addr, User: "alice", Password: "pw", HostKeys: pin(srv)})
	sink := &backendtest.Sink{}
	r := start(t, b, sink)
	waitShell(t, sink)
	if err := b.Run(context.Background(), sink); err == nil {
		t.Error("a second Run while running = nil, want an error")
	}
	if err := r.stop(t); !errors.Is(err, context.Canceled) {
		t.Errorf("Run = %v, want context.Canceled", err)
	}
}

func TestNewRejects(t *testing.T) {
	key := writeKeyFile(t, newEd25519(t), "")
	encrypted := writeKeyFile(t, newEd25519(t), "secret")
	hostKey := authorizedKey(newSigner(t, newEd25519(t)).PublicKey())
	valid := ssh.Config{Addr: "example.com", User: "alice", HostKeys: []string{hostKey}, KeyFiles: []string{key}}
	if _, err := ssh.New(valid); err != nil {
		t.Fatalf("New(valid) = %v", err)
	}
	for _, tt := range []struct {
		name   string
		change func(*ssh.Config)
		want   string // in the error
	}{
		{"no host key policy", func(c *ssh.Config) { c.HostKeys = nil }, "host key"},
		{"unparsable host key", func(c *ssh.Config) { c.HostKeys = []string{"ssh-ed25519 notbase64"} }, "host key 0"},
		{"bad fingerprint", func(c *ssh.Config) { c.HostKeys = []string{"SHA256:abc"} }, "fingerprint"},
		{"no address", func(c *ssh.Config) { c.Addr = "" }, "address"},
		{"no host", func(c *ssh.Config) { c.Addr = ":22" }, "host"},
		{"no user", func(c *ssh.Config) { c.User = "" }, "user"},
		{"missing key file", func(c *ssh.Config) { c.KeyFiles = []string{key + ".missing"} }, "id_test.missing"},
		{"encrypted key file", func(c *ssh.Config) { c.KeyFiles = []string{encrypted} }, "passphrase"},
		{"negative size", func(c *ssh.Config) { c.Cols = -1 }, "size"},
		{"huge size", func(c *ssh.Config) { c.Rows = ssh.MaxSize + 1 }, "size"},
		{"negative timeout", func(c *ssh.Config) { c.DialTimeout = -time.Second }, "timeout"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := valid
			tt.change(&cfg)
			_, err := ssh.New(cfg)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("New = %v, want an error about %q", err, tt.want)
			}
		})
	}
}
