// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package server

import (
	"net"
	"os"
	"slices"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"

	"changkun.de/x/occamy/internal/config"
)

// TestListenCongestion checks that the public listener asks for the
// congestion control it is given and that the connections it accepts
// use it: reno and cubic, which Linux kernels have, and bbr where its
// module is loaded.
func TestListenCongestion(t *testing.T) {
	for _, cc := range []string{"reno", "cubic", "bbr"} {
		t.Run(cc, func(t *testing.T) {
			if !slices.Contains(kernelList(t, "tcp_available_congestion_control"), cc) {
				t.Skipf("the kernel has not loaded %s", cc)
			}
			ln, c, err := listenTCP("127.0.0.1:0", cc)
			if err != nil {
				t.Fatal(err)
			}
			defer ln.Close()
			if c.refused != nil || c.used != cc {
				t.Fatalf("listener: %q, refused: %v; want %s", c.used, c.refused, cc)
			}
			if got := acceptedCongestion(t, ln); got != cc {
				t.Errorf("an accepted connection uses %q, want %s, its listener's", got, cc)
			}
		})
	}
}

// TestListenCongestionFallback checks that a listener the kernel refuses
// a congestion control keeps the kernel's choice and says why, and that
// system asks for none.
func TestListenCongestionFallback(t *testing.T) {
	def := kernelList(t, "tcp_congestion_control")[0]
	for _, tt := range []struct {
		cc, refused string
	}{
		{"nosuchcc", "load its module, tcp_nosuchcc"},
		{config.CongestionSystem, ""},
	} {
		ln, c, err := listenTCP("127.0.0.1:0", tt.cc)
		if err != nil {
			t.Fatal(err)
		}
		if c.used != def {
			t.Errorf("%s: the listener uses %q, want the kernel's %s", tt.cc, c.used, def)
		}
		if tt.refused == "" && c.refused != nil || tt.refused != "" && (c.refused == nil || !strings.Contains(c.refused.Error(), tt.refused)) {
			t.Errorf("%s: refused %v, want %q", tt.cc, c.refused, tt.refused)
		}
		if got := acceptedCongestion(t, ln); got != def {
			t.Errorf("%s: an accepted connection uses %q, want the kernel's %s", tt.cc, got, def)
		}
		ln.Close()
	}
}

// kernelList reads a list of congestion controls from
// /proc/sys/net/ipv4.
func kernelList(t *testing.T, name string) []string {
	t.Helper()
	b, err := os.ReadFile("/proc/sys/net/ipv4/" + name)
	if err != nil {
		t.Skip(err)
	}
	return strings.Fields(string(b))
}

// acceptedCongestion dials ln and returns the congestion control of the
// connection it accepts.
func acceptedCongestion(t *testing.T, ln net.Listener) string {
	t.Helper()
	d, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	c, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	raw, err := c.(syscall.Conn).SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var cc string
	var serr error
	if err := raw.Control(func(fd uintptr) {
		cc, serr = unix.GetsockoptString(int(fd), unix.IPPROTO_TCP, unix.TCP_CONGESTION) // #nosec G115 -- a file descriptor fits an int
	}); err != nil {
		t.Fatal(err)
	}
	if serr != nil {
		t.Fatal(serr)
	}
	return cc
}
