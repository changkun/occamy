// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

//go:build integration

package vnc

// TestTigerVNC runs the backend against TigerVNC's Xvnc
// (docker/test/tigervnc) that offers only VeNCrypt's X509Vnc and
// X509Plain, with a server certificate for localhost and 127.0.0.1
// signed by a CA of the image's own, and a loop on the display that
// answers clipboard text that starts "owner: " with "echo: " and the
// rest. Start one, copy out its CA and certificate, and point the test
// at them:
//
//	podman build -t occamy-vnc-tigervnc docker/test/tigervnc
//	podman run -d --rm --name occamy-vnc-tigervnc -p 127.0.0.1:15901:5901 occamy-vnc-tigervnc
//	podman cp occamy-vnc-tigervnc:/etc/vnc-test/ca.pem /tmp/occamy-vnc-ca.pem
//	podman cp occamy-vnc-tigervnc:/etc/vnc-test/server.pem /tmp/occamy-vnc-server.pem
//	OCCAMY_VNC_TLS_ADDR=127.0.0.1:15901 \
//	OCCAMY_VNC_TLS_CA=/tmp/occamy-vnc-ca.pem OCCAMY_VNC_TLS_CERT=/tmp/occamy-vnc-server.pem \
//	go test -tags integration -run TestTigerVNC ./internal/backend/vnc
//	podman stop occamy-vnc-tigervnc

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"image"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"changkun.de/x/occamy/internal/backend"
	"changkun.de/x/occamy/internal/backend/backendtest"
)

func TestTigerVNC(t *testing.T) {
	addr := os.Getenv("OCCAMY_VNC_TLS_ADDR")
	if addr == "" {
		t.Skip("OCCAMY_VNC_TLS_ADDR is not set")
	}
	// The image's: VNC Authentication's password, and Plain's user and
	// password.
	const pw, user, plainPW = "vncpassword", "occamy", "plainpassword"
	caPEM, err := os.ReadFile(os.Getenv("OCCAMY_VNC_TLS_CA"))
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		t.Fatal("no CA certificate in OCCAMY_VNC_TLS_CA")
	}
	certPEM, err := os.ReadFile(os.Getenv("OCCAMY_VNC_TLS_CERT"))
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(certPEM)
	if block == nil {
		t.Fatal("no certificate in OCCAMY_VNC_TLS_CERT")
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
