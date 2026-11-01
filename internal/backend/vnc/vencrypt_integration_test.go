// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

//go:build integration

package vnc

// TestTigerVNC runs the backend against the demo stack's TigerVNC
// (docker/demo/tigervnc), on its screen that offers only VeNCrypt's
// X509Vnc and X509Plain, with a server certificate for localhost and
// 127.0.0.1 signed by a CA of the container's own, and the demo
// desktop's clipboard probe, which answers text that starts "owner: "
// with "echo: " and the rest:
//
//	go test -tags integration -run TestTigerVNC ./internal/backend/vnc
//
// The test builds the image and starts a container with podman, or the
// engine OCCAMY_CONTAINER_ENGINE names, such as docker, and skips
// without one, or with -short. OCCAMY_VNC_TLS_IMAGE names an image to
// use instead of building one. OCCAMY_VNC_TLS_ADDR names a running
// server to use instead of starting a container, with
// OCCAMY_VNC_TLS_CA and OCCAMY_VNC_TLS_CERT the files of its CA and
// server certificates.
//
// The image and the container a run makes are its own, named with the
// time the run ends at the latest, its -timeout, and the container
// listens on a port the engine picks: concurrent runs do not share
// them. A run removes them when it ends; one that panics, as on
// -timeout, leaves them behind, and the next run removes those whose
// time has passed.

import (
	"cmp"
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"image"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"changkun.de/x/occamy/internal/backend"
	"changkun.de/x/occamy/internal/backend/backendtest"
)

// cleanUp removes the containers and images of earlier runs whose time
// has passed (runname_test.go), and nothing else.
func cleanUp(engine string) {
	now := time.Now()
	out, _ := exec.Command(engine, "ps", "-a", "--filter", "name="+runPrefix, "--format", "{{.Names}}").Output()
	for _, name := range strings.Fields(string(out)) {
		if expired(name, now) {
			_ = exec.Command(engine, "rm", "-f", name).Run()
		}
	}
	out, _ = exec.Command(engine, "images", "--format", "{{.Repository}}").Output()
	for _, repo := range strings.Fields(string(out)) {
		if expired(repo, now) {
			_ = exec.Command(engine, "rmi", "-f", repo).Run()
		}
	}
}

// tigerVNC returns the address of the server, and its CA and server
// certificates in PEM.
func tigerVNC(t *testing.T) (addr string, caPEM, certPEM []byte) {
	if addr := os.Getenv("OCCAMY_VNC_TLS_ADDR"); addr != "" {
		caPEM, err := os.ReadFile(os.Getenv("OCCAMY_VNC_TLS_CA"))
		if err != nil {
			t.Fatal(err)
		}
		certPEM, err := os.ReadFile(os.Getenv("OCCAMY_VNC_TLS_CERT"))
		if err != nil {
			t.Fatal(err)
		}
		return addr, caPEM, certPEM
	}
	if testing.Short() {
		t.Skip("-short: no container")
	}
	engine := cmp.Or(os.Getenv("OCCAMY_CONTAINER_ENGINE"), "podman")
	if _, err := exec.LookPath(engine); err != nil {
		t.Skipf("no container engine: %v", err)
	}
	cleanUp(engine)
	name := runName()
	img := os.Getenv("OCCAMY_VNC_TLS_IMAGE")
	if img == "" {
		img = name
		if out, err := exec.Command(engine, "build", "-q", "-t", img, "-f", "../../../docker/demo/tigervnc/Dockerfile", "../../..").CombinedOutput(); err != nil {
			t.Fatalf("building the TigerVNC image: %v: %s", err, out)
		}
		t.Cleanup(func() { _ = exec.Command(engine, "rmi", "-f", img).Run() })
	}
	if out, err := exec.Command(engine, "run", "-d", "--rm", "--name", name, "-p", "127.0.0.1::5901", img).CombinedOutput(); err != nil {
		t.Fatalf("starting %s: %v: %s", img, err, out)
	}
	t.Cleanup(func() { _ = exec.Command(engine, "rm", "-f", name).Run() })
	out, err := exec.Command(engine, "port", name, "5901/tcp").Output()
	if err != nil {
		t.Fatalf("the port of %s: %v", name, err)
	}
	addr = strings.TrimSpace(strings.Split(string(out), "\n")[0])
	// Wait until Xvnc sends its version: the container makes the
	// certificates before it starts Xvnc.
	for deadline := time.Now().Add(60 * time.Second); ; {
		if probeRFB(addr) == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Xvnc at %s does not answer", addr)
		}
		time.Sleep(200 * time.Millisecond)
	}
	file := func(path string) []byte {
		b, err := exec.Command(engine, "exec", name, "cat", path).Output()
		if err != nil {
			t.Fatalf("%s of %s: %v", path, name, err)
		}
		return b
	}
	return addr, file("/etc/vnc-test/ca.pem"), file("/etc/vnc-test/server.pem")
}

// probeRFB reads the server's ProtocolVersion.
func probeRFB(addr string) error {
	c, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		return err
	}
	defer c.Close()
	if err := c.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		return err
	}
	var b [12]byte
	if _, err := io.ReadFull(c, b[:]); err != nil {
		return err
	}
	_, err = parseVersion(b[:])
	return err
}

func TestTigerVNC(t *testing.T) {
	addr, caPEM, certPEM := tigerVNC(t)
	// The image's: VNC Authentication's password, and Plain's user and
	// password.
	const pw, user, plainPW = "vncpassword", "occamy", "plainpassword"
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		t.Fatal("no CA certificate")
	}
	block, _ := pem.Decode(certPEM)
	if block == nil {
		t.Fatal("no server certificate")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(addr)

	// A connection that runs until the screen arrives, for each policy
	// that trusts the server.
	connects := func(t *testing.T, cfg Config, sink *backendtest.Sink) (*Backend, *backendtest.Sink, *runner, *logBuffer) {
		t.Helper()
		log := &logBuffer{}
		if cfg.Username == "" {
			cfg.Password = pw
		}
		cfg.Logger = log.logger()
		b := newBackend(t, cfg)
		b.Demand(backend.Demand{Visible: true})
		if sink == nil {
			sink = &backendtest.Sink{}
		}
		r := start(t, b, sink)
		waitFor(t, "the screen", func() bool {
			select {
			case <-r.done:
				t.Fatalf("Run = %v", r.err)
			default:
			}
			return sink.Size() != (image.Point{}) && sink.Locks() > 0
		})
		return b, sink, r, log
	}
	for _, tt := range []struct {
		name   string
		cfg    Config
		answer string // Plain's password, asked of the owner
		warn   bool
	}{
		{name: "a private CA", cfg: Config{Addr: addr, TLS: TLS{RootCAs: roots}}},
		{name: "a private CA by name", cfg: Config{Addr: net.JoinHostPort("localhost", port), TLS: TLS{RootCAs: roots}}},
		{name: "pinned", cfg: Config{Addr: addr, TLS: TLS{Pins: []string{certFingerprint(cert)}}}},
		{name: "insecure", cfg: Config{Addr: addr, TLS: TLS{InsecureSkipVerify: true}}, warn: true},
		// VNC Authentication with Plain's password would fail.
		{name: "Plain", cfg: Config{Addr: addr, Username: user, Password: plainPW, TLS: TLS{RootCAs: roots}}},
		{name: "Plain asks", cfg: Config{Addr: addr, Username: user, TLS: TLS{RootCAs: roots}}, answer: plainPW},
	} {
		t.Run(tt.name, func(t *testing.T) {
			checkGoroutines(t)
			sink := &backendtest.Sink{}
			if tt.answer != "" {
				sink.Answer = answers(tt.answer)
			}
			_, _, r, log := connects(t, tt.cfg, sink)
			if err := r.stop(t); !errors.Is(err, context.Canceled) {
				t.Errorf("Run = %v, want context.Canceled", err)
			}
			if warned := strings.Contains(log.String(), "unverified certificate"); warned != tt.warn {
				t.Errorf("warned %v, want %v:\n%s", warned, tt.warn, log)
			}
			if n := len(sink.Prompts()); n != min(len(tt.answer), 1) {
				t.Errorf("%d prompts", n)
			}
		})
	}
	for _, tt := range []struct {
		name string
		cfg  Config
		err  error // other than a CertificateError
	}{
		{name: "a wrong host", cfg: Config{Addr: addr, Password: pw, TLS: TLS{RootCAs: roots, ServerName: "vnc.example"}}},
		{name: "the system's roots", cfg: Config{Addr: addr, Password: pw}},
		{name: "another certificate pinned", cfg: Config{Addr: addr, Password: pw, TLS: TLS{Pins: []string{strings.Repeat("00", 32)}}}},
		{name: "a wrong Plain password", cfg: Config{Addr: addr, Username: user, Password: pw, TLS: TLS{RootCAs: roots}}, err: ErrAuthFailed},
		{name: "a wrong VNC password", cfg: Config{Addr: addr, Password: plainPW, TLS: TLS{RootCAs: roots}}, err: ErrAuthFailed},
	} {
		t.Run(tt.name, func(t *testing.T) {
			checkGoroutines(t)
			err := start(t, newBackend(t, tt.cfg), &backendtest.Sink{}).wait(t)
			if tt.err != nil {
				if !errors.Is(err, tt.err) {
					t.Errorf("Run = %v, want %v", err, tt.err)
				}
				return
			}
			var ce *CertificateError
			if !errors.As(err, &ce) || ce.Fingerprint != certFingerprint(cert) {
				t.Errorf("Run = %v, want a CertificateError for %s", err, certFingerprint(cert))
			}
		})
	}

	t.Run("extensions", func(t *testing.T) {
		checkGoroutines(t)
		b, sink, r, _ := connects(t, Config{Addr: addr, TLS: TLS{RootCAs: roots}}, nil)
		state := func() (continuous, desktop, extKeys, clipboard) {
			b.mu.Lock()
			run := b.run
			b.mu.Unlock()
			run.mu.Lock()
			defer run.mu.Unlock()
			return run.cont, run.desk, run.keys, run.clip
		}
		waitFor(t, "the extensions", func() bool {
			cont, desk, keys, clip := state()
			return cont.on && desk.supported && keys.on && clip.flags&clipText != 0
		})
		_, _, _, clip := state()
		t.Logf("clipboard caps %#x", clip.flags)

		// The screen follows the viewer's size.
		if err := b.Input(backend.Resize{Width: 800, Height: 600}); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "the new size", func() bool { return sink.Size() == image.Pt(800, 600) })

		// Text goes to the server's clipboard and comes back.
		if err := b.Input(backend.Clipboard{MIME: "text/plain", Data: []byte("owner: naïve € ✓\nline")}); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "the echo", func() bool {
			_, data := sink.ClipboardContent()
			return string(data) == "echo: naïve € ✓\nline"
		})

		// Nobody can see: continuous updates stop.
		b.Demand(backend.Demand{Visible: false})
		waitFor(t, "continuous updates off", func() bool { cont, _, _, _ := state(); return !cont.on })
		b.Demand(backend.Demand{Visible: true})
		waitFor(t, "continuous updates on", func() bool { cont, _, _, _ := state(); return cont.on })
		// They bring changes: the resize back is drawn.
		if err := b.Input(backend.Resize{Width: 1024, Height: 768}); err != nil {
			t.Fatal(err)
		}
		locks := sink.Locks()
		waitFor(t, "the size back", func() bool { return sink.Size() == image.Pt(1024, 768) && sink.Locks() > locks })
		time.Sleep(200 * time.Millisecond)

		if err := r.stop(t); !errors.Is(err, context.Canceled) {
			t.Errorf("Run = %v, want context.Canceled", err)
		}
	})
}
