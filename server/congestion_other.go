// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

//go:build !linux

package server

import "net"

// listenTCP opens a TCP listener on addr. congestion_control applies on
// Linux only, so here the listener keeps the system's congestion
// control, and listenTCP does not say which that is.
func listenTCP(addr, _ string) (net.Listener, congestion, error) {
	ln, err := net.Listen("tcp", addr)
	return ln, congestion{}, err
}
