// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package server

import (
	"context"
	"errors"
	"net"
	"syscall"

	"golang.org/x/sys/unix"

	"changkun.de/x/occamy/internal/config"
)

// listenTCP opens a TCP listener on addr that asks for the congestion
// control cc, unless cc is config.CongestionSystem. Linux gives an
// accepted connection the congestion control its listener asked for, so
// a connection has it from its handshake on. Where the kernel refuses
// cc, the listener keeps the kernel's choice.
func listenTCP(addr, cc string) (net.Listener, congestion, error) {
	var c congestion
	lc := net.ListenConfig{Control: func(_, _ string, raw syscall.RawConn) error {
		return raw.Control(func(fd uintptr) {
			if cc != config.CongestionSystem {
				c.refused = unix.SetsockoptString(int(fd), unix.IPPROTO_TCP, unix.TCP_CONGESTION, cc) // #nosec G115 -- a file descriptor fits an int
			}
			c.used, _ = unix.GetsockoptString(int(fd), unix.IPPROTO_TCP, unix.TCP_CONGESTION) // #nosec G115 -- a file descriptor fits an int
		})
	}}
	ln, err := lc.Listen(context.Background(), "tcp", addr)
	if err != nil {
		return nil, congestion{}, err
	}
	switch {
	case errors.Is(c.refused, unix.ENOENT):
		c.refused = errors.New("the kernel has not loaded it: load its module, tcp_" + cc)
	case errors.Is(c.refused, unix.EPERM):
		c.refused = errors.New("the kernel allows it only to privileged processes: add it to net.ipv4.tcp_allowed_congestion_control")
	}
	return ln, c, nil
}
