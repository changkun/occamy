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
// and the generator and prime of Apple Remote Desktop's group, and
// disconnects before sending any credential. With OCCAMY_VNC_ARD_USER and
// OCCAMY_VNC_ARD_PASSWORD, a Mac account's, it also logs in with Apple
// Remote Desktop authentication and waits for the screen. Its key
// exchange authenticates no server, so run it on a network you trust.

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
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
	g, n, p, err := ardGroup(addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("Apple Remote Desktop's group: generator %d, key length %d bytes, a prime of %d bits%s",
		g, n, p.BitLen(), map[bool]string{true: ", RFC 2409's group 2"}[p.Cmp(rfc2409Group2) == 0])

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
	c, err := dialProbe(addr)
	if err != nil {
		return "", nil, err
	}
	defer c.Close()
	return readSecurityTypes(c)
}

// ardGroup chooses Apple Remote Desktop authentication, reads the
// server's generator, key length and prime, and disconnects before
// sending anything more.
func ardGroup(addr string) (g, n int, p *big.Int, err error) {
	c, err := dialProbe(addr)
	if err != nil {
		return 0, 0, nil, err
	}
	defer c.Close()
	if _, _, err := readSecurityTypes(c); err != nil {
		return 0, 0, nil, err
	}
	if _, err := c.Write([]byte{secARD}); err != nil {
		return 0, 0, nil, err
	}
	var b [4]byte
	if _, err := io.ReadFull(c, b[:]); err != nil {
		return 0, 0, nil, err
	}
	g, n = int(binary.BigEndian.Uint16(b[:])), int(binary.BigEndian.Uint16(b[2:]))
	prime := make([]byte, n)
	if _, err := io.ReadFull(c, prime); err != nil {
		return g, n, nil, err
	}
	return g, n, new(big.Int).SetBytes(prime), nil
}

func dialProbe(addr string) (net.Conn, error) {
	c, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return nil, err
	}
	if err := c.SetDeadline(time.Now().Add(timeout)); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

// readSecurityTypes reads the server's version, answers it, and reads
// the security types offered, or the one a 3.3 server chose.
func readSecurityTypes(c net.Conn) (version string, types []byte, err error) {
	var b [12]byte
	if _, err := io.ReadFull(c, b[:]); err != nil {
		return "", nil, err
	}
	version = strings.TrimSpace(string(b[:]))
	v, err := parseVersion(b[:])
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
