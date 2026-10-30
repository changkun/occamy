// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package server

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"changkun.de/x/occamy/internal/config"
)

// TestListenRefusesOverlap checks that listen does not open a debug
// listener on the public listener's port, whatever the File says.
func TestListenRefusesOverlap(t *testing.T) {
	probe := localListener(t)
	port := probe.Addr().(*net.TCPAddr).Port
	probe.Close()
	f := testFile(t)
	f.Listen = "127.0.0.1:" + strconv.Itoa(port)
	f.Debug.Address = "localhost:" + strconv.Itoa(port)
	ln, dln, err := listen(f)
	if err == nil {
		ln.Close()
		dln.Close()
		t.Fatal("listen opened a debug listener on the public port")
	}
	// The refusal, not a failed bind: which pairs bind varies by system.
	if !strings.Contains(err.Error(), "shares a port") {
		t.Errorf("listen: %v, want a refusal", err)
	}
	// The public listener was closed again.
	ln, err = net.Listen("tcp", f.Listen)
	if err != nil {
		t.Fatalf("the public port is still taken: %v", err)
	}
	ln.Close()
}

// The environment of TestRunChild: the v1 file to serve, and the drain.
const (
	childFileEnv  = "OCCAMY_TEST_RUN_FILE"
	childDrainEnv = "OCCAMY_TEST_RUN_DRAIN"
)

// TestRunChild is TestRunSignals's occamyd: run with childFileEnv set, it
// serves that file with Run and exits as occamyd's main does.
func TestRunChild(t *testing.T) {
	path := os.Getenv(childFileEnv)
	if path == "" {
		t.Skip("run by TestRunSignals")
	}
	if d, err := time.ParseDuration(os.Getenv(childDrainEnv)); err == nil {
		drainTimeout = d
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(3)
	}
	f, err := config.ParseV1(raw)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(3)
	}
	if err := Run(f); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

// TestRunSignals runs Run in a child process with a request in flight
// that outlasts the drain: one SIGTERM ends it normally once the drain
// runs out, and a second SIGTERM during the drain ends it at once.
func TestRunSignals(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no SIGTERM on Windows")
	}
	dir := t.TempDir()
	file := filepath.Join(dir, "v1.yaml")
	raw := "version: 1\nlisten: 127.0.0.1:0\ndebug: {address: 127.0.0.1:0}\nauth:\n  signing_key_file: " + filepath.Join(dir, "signing.key") + "\n"
	if err := os.WriteFile(file, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name    string
		drain   time.Duration
		signals int
	}{
		{"drain runs out", 300 * time.Millisecond, 1},
		{"second signal", time.Minute, 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := startChild(t, file, tt.drain)
			// A CPU profile holds its request for as long as asked.
			pc, err := net.Dial("tcp", c.debugAddr)
			if err != nil {
				t.Fatal(err)
			}
			defer pc.Close()
			if _, err := io.WriteString(pc, "GET /debug/pprof/profile?seconds=60 HTTP/1.1\r\nHost: occamy\r\n\r\n"); err != nil {
				t.Fatal(err)
			}
			time.Sleep(200 * time.Millisecond) // for occamyd to read it

			if err := c.cmd.Process.Signal(syscall.SIGTERM); err != nil {
				t.Fatal(err)
			}
			c.waitLine(t, "shutting down")
			start := time.Now()
			if tt.signals == 2 {
				if err := c.cmd.Process.Signal(syscall.SIGTERM); err != nil {
					t.Fatal(err)
				}
			}
			err = c.wait(t, 10*time.Second)
			elapsed := time.Since(start)
			if tt.signals == 1 {
				if err != nil {
					t.Errorf("exit after one signal: %v, want status 0\n%s", err, c.output())
				}
				if !strings.Contains(c.output(), "warning: requests still in flight") {
					t.Errorf("no warning for the cut request:\n%s", c.output())
				}
				return
			}
			var ee *exec.ExitError
			if !errors.As(err, &ee) || !ee.Sys().(syscall.WaitStatus).Signaled() {
				t.Errorf("exit after a second signal: %v, want death by the signal\n%s", err, c.output())
			}
			if elapsed > 5*time.Second {
				t.Errorf("the second signal took %v to end occamyd", elapsed)
			}
		})
	}
}

// child is a test binary running TestRunChild.
type child struct {
	cmd       *exec.Cmd
	lines     chan string
	done      chan error
	debugAddr string

	out strings.Builder // guarded by reading lines in the test goroutine only
}

func startChild(t *testing.T, file string, drain time.Duration) *child {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestRunChild$")
	cmd.Env = append(os.Environ(), childFileEnv+"="+file, childDrainEnv+"="+drain.String())
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	c := &child{cmd: cmd, lines: make(chan string, 100), done: make(chan error, 1)}
	go func() {
		s := bufio.NewScanner(stderr)
		for s.Scan() {
			c.lines <- s.Text()
		}
		close(c.lines)
		c.done <- cmd.Wait()
	}()
	t.Cleanup(func() {
		cmd.Process.Kill()
		for range c.lines {
		}
	})
	line := c.waitLine(t, "serving expvar and pprof at http://")
	addr := line[strings.Index(line, "http://")+len("http://"):]
	c.debugAddr = strings.TrimSuffix(addr, "/debug/")
	return c
}

// waitLine returns the first line of the child's log that contains s.
func (c *child) waitLine(t *testing.T, s string) string {
	t.Helper()
	timeout := time.After(10 * time.Second)
	for {
		select {
		case line, ok := <-c.lines:
			if !ok {
				t.Fatalf("the child exited before %q:\n%s", s, c.out.String())
			}
			c.out.WriteString(line + "\n")
			if strings.Contains(line, s) {
				return line
			}
		case <-timeout:
			t.Fatalf("no %q from the child:\n%s", s, c.out.String())
		}
	}
}

// wait reads the child's log until it exits, and returns how it exited.
func (c *child) wait(t *testing.T, d time.Duration) error {
	t.Helper()
	timeout := time.After(d)
	for {
		select {
		case line, ok := <-c.lines:
			if !ok {
				return <-c.done
			}
			c.out.WriteString(line + "\n")
		case <-timeout:
			t.Fatalf("the child did not exit in %v:\n%s", d, c.out.String())
		}
	}
}

func (c *child) output() string { return c.out.String() }
