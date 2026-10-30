// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

//go:build integration

package vnc

// TestMacOS checks the backend by hand against macOS Screen Sharing,
// which no CI runner offers. Turn Screen Sharing on, on a Mac you own,
// and run from another machine:
//
//	OCCAMY_VNC_ARD_ADDR=mac.local:5900 go test -tags integration -run TestMacOS -v ./internal/backend/vnc
//
// That reads the server's version and the security types it offers,
// and disconnects before choosing one. With OCCAMY_VNC_ARD_USER and
// OCCAMY_VNC_ARD_PASSWORD, a Mac account's, it also logs in with Apple
// Remote Desktop authentication and waits for the screen. Its key
// exchange authenticates no server, so run it on a network you trust.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"changkun.de/x/occamy/internal/backend"
	"changkun.de/x/occamy/internal/backend/backendtest"
)

func TestMacOS(t *testing.T) {
	addr := os.Getenv("OCCAMY_VNC_ARD_ADDR")
	if addr == "" {
		t.Skip("OCCAMY_VNC_ARD_ADDR is not set")
	}
	version, types, err := securityTypes(addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%s speaks %q and offers the security types %v", addr, version, types)
	if !slices.Contains(types, secARD) {
		t.Fatalf("no Apple Remote Desktop authentication among %v", types)
	}

	// Without ARD, a Mac that offers none of the other types the backend
	// takes is refused before any credential is asked for.
	if !slices.ContainsFunc(types, func(t byte) bool { return t == secNone || t == secVNCAuth || t == secVeNCrypt }) {
		sink := &backendtest.Sink{}
		err := start(t, newBackend(t, Config{Addr: addr}), sink).wait(t)
		if !errors.Is(err, ErrNoSecurityType) || !strings.Contains(err.Error(), "Config.ARD") {
			t.Errorf("Run without ARD = %v, want ErrNoSecurityType naming Config.ARD", err)
		}
		if n := len(sink.Prompts()); n != 0 {
			t.Errorf("%d prompts without ARD", n)
		}
	}

	user, password := os.Getenv("OCCAMY_VNC_ARD_USER"), os.Getenv("OCCAMY_VNC_ARD_PASSWORD")
	if user == "" || password == "" {
		t.Log("OCCAMY_VNC_ARD_USER or OCCAMY_VNC_ARD_PASSWORD is not set: not logging in")
		return
	}
	checkGoroutines(t)
	var log logBuffer
	b := newBackend(t, Config{
		Addr: addr, ARD: true, Username: user, Password: password,
		Logger: slog.New(slog.NewTextHandler(&log, &slog.HandlerOptions{Level: slog.LevelDebug})),
	})
	b.Demand(backend.Demand{Visible: true})
	sink := &backendtest.Sink{}
	r := start(t, b, sink)
	waitFor(t, "the screen", func() bool { return len(sink.TakeDamage()) > 0 })
	t.Logf("screen %v, title %q", sink.Size(), sink.TitleText())
	if err := r.stop(t); !errors.Is(err, context.Canceled) {
		t.Errorf("Run = %v, want context.Canceled", err)
	}
	logged := log.String()
	if !strings.Contains(logged, fmt.Sprintf("security=%d", secARD)) {
		t.Errorf("not logged in with Apple Remote Desktop authentication; log:\n%s", logged)
	}
	if strings.Contains(logged, password) {
		t.Error("the log shows the password")
	}
}

// securityTypes reads a server's ProtocolVersion and, after the
// client's, the security types it offers, and disconnects before
// choosing one.
func securityTypes(addr string) (version string, types []byte, err error) {
	c, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return "", nil, err
	}
	defer c.Close()
	if err := c.SetDeadline(time.Now().Add(timeout)); err != nil {
		return "", nil, err
	}
	var b [256]byte
	if _, err := io.ReadFull(c, b[:12]); err != nil {
		return "", nil, err
	}
	version = strings.TrimSpace(string(b[:12]))
	v, err := parseVersion(b[:12])
	if err != nil {
		return version, nil, err
	}
	if _, err := fmt.Fprintf(c, "RFB 003.%03d\n", v); err != nil {
		return version, nil, err
	}
	if v == 3 {
		if _, err := io.ReadFull(c, b[:4]); err != nil {
			return version, nil, err
		}
		return version, []byte{b[3]}, nil
	}
	if _, err := io.ReadFull(c, b[:1]); err != nil {
		return version, nil, err
	}
	if b[0] == 0 {
		return version, nil, refused(c)
	}
	types = make([]byte, b[0])
	_, err = io.ReadFull(c, types)
	return version, types, err
}
