// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package guacd_test

import (
	"context"
	"errors"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"changkun.de/x/occamy/internal/guacamole"
	"changkun.de/x/occamy/internal/guacd"
)

// fakeGuacd plays guacd's side of the handshake: it answers select with
// args, collects instructions until connect, and answers with ready. It
// returns every instruction it received, as strings.
func fakeGuacd(t *testing.T, nc net.Conn, args []string, ready string) <-chan []string {
	t.Helper()
	got := make(chan []string, 1)
	go func() {
		defer close(got)
		conn := guacamole.NewInstructionIO(nc)
		var received []string
		for {
			ins, err := conn.Read()
			if err != nil {
				t.Errorf("fake guacd read error: %v", err)
				return
			}
			received = append(received, ins.String())
			switch ins.Opcode() {
			case "select":
				conn.Write(guacamole.NewInstruction(append([]string{"args"}, args...)))
			case "connect":
				conn.Write(guacamole.NewInstruction([]string{"ready", ready}))
				got <- received
				return
			}
		}
	}()
	return got
}

func instruction(elements ...string) string {
	return guacamole.NewInstruction(elements).String()
}

func TestHandshakeNewConnection(t *testing.T) {
	cli, srv := net.Pipe()
	defer cli.Close()
	defer srv.Close()
	got := fakeGuacd(t, srv, []string{"VERSION_1_5_0", "hostname", "port", "read-only", "password"}, "$abc")

	conn, err := guacd.Handshake(context.Background(), cli, guacd.Config{
		Protocol: "vnc",
		Params:   map[string]string{"hostname": "vnc", "port": "5900", "password": "secret", "unused": "x"},
		Width:    1280,
		Height:   720,
		DPI:      192,
		Timezone: "Europe/Berlin",
		Name:     "occamy",
	})
	if err != nil {
		t.Fatalf("handshake error: %v", err)
	}
	if conn.ID != "$abc" || conn.Version != "VERSION_1_5_0" {
		t.Fatalf("handshake result wrong, got ID %q, version %q", conn.ID, conn.Version)
	}

	want := []string{
		instruction("select", "vnc"),
		instruction("size", "1280", "720", "192"),
		instruction("audio"),
		instruction("video"),
		instruction("image", "image/png", "image/jpeg", "image/webp"),
		instruction("timezone", "Europe/Berlin"),
		instruction("name", "occamy"),
		instruction("connect", "VERSION_1_5_0", "vnc", "5900", "", "secret"),
	}
	if g := <-got; strings.Join(g, "\n") != strings.Join(want, "\n") {
		t.Fatalf("handshake instructions wrong\ngot:\n%s\nwant:\n%s", strings.Join(g, "\n"), strings.Join(want, "\n"))
	}

	// The connection proxies raw instructions after the handshake.
	go srv.Write([]byte("4.sync,3.123;"))
	raw, err := conn.ReadRaw()
	if err != nil || string(raw) != "4.sync,3.123;" {
		t.Fatalf("read after handshake, got: %q, %v", raw, err)
	}
	go conn.WriteRaw([]byte("4.sync,3.123;"))
	buf := make([]byte, 64)
	n, err := srv.Read(buf)
	if err != nil || string(buf[:n]) != "4.sync,3.123;" {
		t.Fatalf("write after handshake, got: %q, %v", buf[:n], err)
	}
}

func TestHandshakeDefaults(t *testing.T) {
	cli, srv := net.Pipe()
	defer cli.Close()
	defer srv.Close()
	got := fakeGuacd(t, srv, []string{"VERSION_1_5_0", "hostname"}, "$abc")

	_, err := guacd.Handshake(context.Background(), cli, guacd.Config{Protocol: "ssh"})
	if err != nil {
		t.Fatalf("handshake error: %v", err)
	}
	want := []string{
		instruction("select", "ssh"),
		instruction("size", "1024", "768", "96"),
		instruction("audio"),
		instruction("video"),
		instruction("image", "image/png", "image/jpeg", "image/webp"),
		instruction("connect", "VERSION_1_5_0", ""),
	}
	if g := <-got; strings.Join(g, "\n") != strings.Join(want, "\n") {
		t.Fatalf("handshake instructions wrong\ngot:\n%s\nwant:\n%s", strings.Join(g, "\n"), strings.Join(want, "\n"))
	}
}

func TestHandshakeJoin(t *testing.T) {
	cli, srv := net.Pipe()
	defer cli.Close()
	defer srv.Close()
	got := fakeGuacd(t, srv, []string{"VERSION_1_5_0", "read-only"}, "$abc")

	conn, err := guacd.Handshake(context.Background(), cli, guacd.Config{Protocol: "vnc", ConnectionID: "$abc"})
	if err != nil {
		t.Fatalf("handshake error: %v", err)
	}
	if conn.ID != "$abc" {
		t.Fatalf("joined connection ID wrong, got: %q", conn.ID)
	}
	if g := <-got; g[0] != instruction("select", "$abc") {
		t.Fatalf("join must select the connection ID, got: %q", g[0])
	}
}

func TestHandshakeNoSuchConnection(t *testing.T) {
	cli, srv := net.Pipe()
	defer cli.Close()
	defer srv.Close()
	go func() {
		conn := guacamole.NewInstructionIO(srv)
		conn.Read()
		conn.Write(guacamole.NewInstruction([]string{"error", "No such connection.", "519"}))
	}()

	_, err := guacd.Handshake(context.Background(), cli, guacd.Config{ConnectionID: "$gone"})
	var e *guacd.Error
	if !errors.As(err, &e) || e.Status != 519 || e.Message != "No such connection." {
		t.Fatalf("handshake error wrong, got: %v", err)
	}
}

func TestHandshakeTimeout(t *testing.T) {
	cli, srv := net.Pipe()
	defer cli.Close()
	defer srv.Close()
	go guacamole.NewInstructionIO(srv).Read() // read select, never answer

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := guacd.Handshake(ctx, cli, guacd.Config{Protocol: "vnc"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("handshake error wrong, got: %v", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("handshake took %v after the context deadline", d)
	}
}

func TestHandshakeSpecialValues(t *testing.T) {
	cli, srv := net.Pipe()
	defer cli.Close()
	defer srv.Close()
	got := fakeGuacd(t, srv, []string{"VERSION_1_5_0", "password"}, "$abc")

	password := "p;a,s.s 日本🙂"
	_, err := guacd.Handshake(context.Background(), cli, guacd.Config{
		Protocol: "vnc",
		Params:   map[string]string{"password": password},
	})
	if err != nil {
		t.Fatalf("handshake error: %v", err)
	}
	g := <-got
	ins, err := guacamole.ParseInstruction([]byte(g[len(g)-1]))
	if err != nil {
		t.Fatalf("parse connect error: %v", err)
	}
	if args := ins.Args(); len(args) != 2 || args[1] != password {
		t.Fatalf("password did not reach guacd intact, got: %q", args)
	}
}

func TestHandshakeOldGuacd(t *testing.T) {
	cli, srv := net.Pipe()
	defer cli.Close()
	defer srv.Close()
	go func() {
		conn := guacamole.NewInstructionIO(srv)
		conn.Read()
		conn.Write(guacamole.NewInstruction([]string{"args", "hostname", "port"}))
	}()

	_, err := guacd.Handshake(context.Background(), cli, guacd.Config{Protocol: "vnc"})
	if err == nil || !strings.Contains(err.Error(), "protocol version") {
		t.Fatalf("handshake with pre-1.1 guacd, got: %v", err)
	}
}

func TestHandshakeUnexpectedInstruction(t *testing.T) {
	cli, srv := net.Pipe()
	defer cli.Close()
	defer srv.Close()
	go func() {
		conn := guacamole.NewInstructionIO(srv)
		conn.Read()
		conn.Write(guacamole.NewInstruction([]string{"sync", "123"}))
	}()

	_, err := guacd.Handshake(context.Background(), cli, guacd.Config{Protocol: "vnc"})
	if err == nil || !strings.Contains(err.Error(), `"sync"`) {
		t.Fatalf("handshake with unexpected instruction, got: %v", err)
	}
}

// TestDialRealGuacd runs against a real guacd at OCCAMY_GUACD_ADDR, for
// example `podman run -p 4822:4822 docker.io/guacamole/guacd:1.6.0`. guacd
// sends ready before it connects to the target, so the target need not be
// reachable. If OCCAMY_GUACD_VNC names a reachable VNC server (host:port,
// as seen from guacd, with password OCCAMY_GUACD_VNC_PASSWORD), the test
// also joins the new connection.
func TestDialRealGuacd(t *testing.T) {
	addr := os.Getenv("OCCAMY_GUACD_ADDR")
	if addr == "" {
		t.Skip("OCCAMY_GUACD_ADDR not set")
	}
	target := os.Getenv("OCCAMY_GUACD_VNC")
	join := target != ""
	if !join {
		target = "127.0.0.1:5999"
	}
	host, port, err := net.SplitHostPort(target)
	if err != nil {
		t.Fatalf("bad OCCAMY_GUACD_VNC: %v", err)
	}
	params := map[string]string{
		"hostname": host,
		"port":     port,
		"password": os.Getenv("OCCAMY_GUACD_VNC_PASSWORD"),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	owner, err := guacd.Dial(ctx, addr, guacd.Config{Protocol: "vnc", Params: params})
	if err != nil {
		t.Fatalf("dial error: %v", err)
	}
	defer owner.Close()
	if !strings.HasPrefix(owner.ID, "$") || !strings.HasPrefix(owner.Version, "VERSION_") {
		t.Fatalf("handshake result wrong, got ID %q, version %q", owner.ID, owner.Version)
	}
	if !join {
		return
	}

	viewer, err := guacd.Dial(ctx, addr, guacd.Config{ConnectionID: owner.ID, Params: params})
	if err != nil {
		t.Fatalf("join error: %v", err)
	}
	defer viewer.Close()
	if viewer.ID != owner.ID {
		t.Fatalf("joined connection ID wrong, got: %q, want: %q", viewer.ID, owner.ID)
	}
}
