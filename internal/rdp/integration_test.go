// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

//go:build rdp_integration

package rdp

// The integration test runs the client against xrdp in the bench desktop
// image (docker/bench/desktop), whose sessions show the scene program
// (bench/scene):
//
//	go test -tags rdp_integration -run Integration ./internal/rdp
//
// It builds the image with podman unless OCCAMY_RDP_IMAGE names one, and
// starts a container of its own unless OCCAMY_RDP_ADDR names a running
// server. OCCAMY_CONTAINER_ENGINE selects docker instead of podman.

import (
	"context"
	"crypto/tls"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var (
	rdpAddr string
	engine  = envOr("OCCAMY_CONTAINER_ENGINE", "podman")
)

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func TestMain(m *testing.M) {
	os.Exit(func() int {
		addr, stop, err := startServer()
		if err != nil {
			fmt.Fprintln(os.Stderr, "rdp integration:", err)
			return 1
		}
		defer stop()
		rdpAddr = addr
		return m.Run()
	}())
}

// startServer returns the address of an xrdp server showing the scene.
func startServer() (string, func(), error) {
	if addr := os.Getenv("OCCAMY_RDP_ADDR"); addr != "" {
		return addr, func() {}, nil
	}
	image := os.Getenv("OCCAMY_RDP_IMAGE")
	if image == "" {
		image = "occamy-rdp-integration"
		cmd := exec.Command(engine, "build", "-q", "-t", image, "-f", "docker/bench/desktop/Dockerfile", ".")
		cmd.Dir = "../.."
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return "", nil, fmt.Errorf("building the desktop image: %w", err)
		}
	}
	name := fmt.Sprintf("occamy-rdp-integration-%d-%d", os.Getpid(), time.Now().UnixNano()%1e6)
	out, err := exec.Command(engine, "run", "-d", "--rm", "--name", name,
		"-p", "127.0.0.1::3389", image, "rdp").CombinedOutput()
	if err != nil {
		return "", nil, fmt.Errorf("starting %s: %v: %s", image, err, out)
	}
	stop := func() { exec.Command(engine, "rm", "-f", name).Run() }
	out, err = exec.Command(engine, "port", name, "3389/tcp").Output()
	if err != nil {
		stop()
		return "", nil, fmt.Errorf("port of %s: %w", name, err)
	}
	addr := strings.TrimSpace(strings.Split(string(out), "\n")[0])
	// Wait until xrdp answers the X.224 connection request.
	for deadline := time.Now().Add(60 * time.Second); ; {
		if probeX224(addr) == nil {
			break
		}
		if time.Now().After(deadline) {
			stop()
			return "", nil, fmt.Errorf("xrdp at %s does not answer", addr)
		}
		time.Sleep(500 * time.Millisecond)
	}
	return addr, stop, nil
}

func probeX224(addr string) error {
	c, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		return err
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := c.Write(connectionRequest("", ProtocolSSL)); err != nil {
		return err
	}
	f, _, err := newTransport(c, nil).readFrame(nil)
	if err != nil {
		return err
	}
	_, err = parseConnectionConfirm(f.data)
	return err
}

// session is a client connected to the test server.
type session struct {
	c    *Client
	scr  *screen
	done chan error
}

func dial(t *testing.T, cfg Config) *session {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	conn, err := net.Dial("tcp", rdpAddr)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Username, cfg.Password = "occamy", "occamy"
	cfg.TLSConfig = &tls.Config{InsecureSkipVerify: true}
	cctx, ccancel := context.WithTimeout(ctx, 30*time.Second)
	defer ccancel()
	c, err := Connect(cctx, conn, cfg)
	if err != nil {
		t.Fatal(err)
	}
	s := &session{c: c, scr: &screen{}, done: make(chan error, 1)}
	go func() { s.done <- c.Run(ctx, s.scr) }()
	t.Cleanup(func() {
		cancel()
		<-s.done
	})
	return s
}

// wallpaper is the scene's background at (x, y) on a w×h screen, as
// bench/scene's newWallpaper draws it.
func wallpaper(y, h int) color.RGBA {
	top, bottom := color.RGBA{0x1f, 0x3b, 0x5a, 0xff}, color.RGBA{0x3c, 0x6e, 0x8f, 0xff}
	t := float64(y) / float64(max(1, h-1))
	f := func(a, b uint8) uint8 { return uint8(float64(a) + (float64(b)-float64(a))*t) }
	return color.RGBA{f(top.R, bottom.R), f(top.G, bottom.G), f(top.B, bottom.B), 0xff}
}

func dump(t *testing.T, s *session, name string) {
	dir := os.Getenv("OCCAMY_RDP_DUMP")
	if dir == "" {
		return
	}
	img := s.scr.snapshot()
	if img == nil {
		return
	}
	f, err := os.Create(filepath.Join(dir, name+".png"))
	if err != nil {
		t.Log(err)
		return
	}
	defer f.Close()
	png.Encode(f, img)
}

func TestIntegrationExplore(t *testing.T) {
	s := dial(t, Config{ColorDepth: 32})
	t.Logf("protocol %#x, desktop %v", s.c.Protocol(), image.Pt(s.c.Size()))
	select {
	case err := <-s.done:
		t.Fatalf("Run: %v", err)
	case <-time.After(15 * time.Second):
	}
	dump(t, s, "explore")
	t.Logf("finalized %v, stats %+v", s.c.Finalized(), s.c.Stats())
}
