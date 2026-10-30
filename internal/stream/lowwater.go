// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

//go:build linux || darwin

package stream

import (
	"net"
	"syscall"

	"golang.org/x/sys/unix"
)

// SetLowWater limits the bytes c's socket keeps queued but unsent to
// lowWater, with TCP_NOTSENT_LOWAT, so that a write blocks while the
// kernel still holds that much. Without the limit the kernel buffers
// megabytes, and a frame the pacer has decided on waits behind data the
// viewer will never need. The HTTP server sets it on each connection
// before the WebSocket upgrade, since the WebSocket hides its socket.
// Connections that are not TCP sockets are left alone.
func SetLowWater(c net.Conn, lowWater int) error {
	sc, ok := c.(syscall.Conn)
	if !ok {
		return nil
	}
	raw, err := sc.SyscallConn()
	if err != nil {
		return err
	}
	var serr error
	err = raw.Control(func(fd uintptr) {
		serr = unix.SetsockoptInt(int(fd), unix.IPPROTO_TCP, unix.TCP_NOTSENT_LOWAT, lowWater) // #nosec G115 -- a file descriptor fits an int
	})
	if err != nil {
		return err
	}
	return serr
}
