// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

//go:build linux || darwin

package stream

import (
	"net"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

func TestSetLowWater(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := SetLowWater(c, 16<<10); err != nil {
		t.Fatal(err)
	}
	raw, err := c.(syscall.Conn).SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var got int
	var gerr error
	if err := raw.Control(func(fd uintptr) {
		got, gerr = unix.GetsockoptInt(int(fd), unix.IPPROTO_TCP, unix.TCP_NOTSENT_LOWAT) // #nosec G115 -- a file descriptor fits an int
	}); err != nil {
		t.Fatal(err)
	}
	if gerr != nil || got != 16<<10 {
		t.Errorf("TCP_NOTSENT_LOWAT %d, %v; want %d", got, gerr, 16<<10)
	}
	// A connection that is not a socket is left alone.
	p, q := net.Pipe()
	defer p.Close()
	defer q.Close()
	if err := SetLowWater(p, 16<<10); err != nil {
		t.Errorf("pipe: %v", err)
	}
}
