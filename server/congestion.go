// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package server

import (
	"expvar"
	"log"

	"changkun.de/x/occamy/internal/config"
)

// congestion is the TCP congestion control of the public listener's
// connections, which congestion_control asks for.
type congestion struct {
	used    string // the one they use; empty where occamyd cannot tell
	refused error  // why the kernel refused the one asked for, if it did
}

// tcpCongestion is the congestion control the public listener's
// connections use, at /debug/vars, for the benchmark to record.
var tcpCongestion expvar.String

// report logs, at start, the congestion control the public listener's
// connections use, and warns if it is not the one asked for.
func (c congestion) report(asked string) {
	tcpCongestion.Set(c.used)
	switch {
	case c.used == "":
	case c.refused != nil:
		// #nosec G706 -- the operator's configuration and the kernel's answer, not a client's input.
		log.Printf("warning: congestion_control %s: %v; connections use %s", asked, c.refused, c.used)
	case asked == config.CongestionSystem:
		// #nosec G706 -- the kernel's answer, not a client's input.
		log.Printf("TCP congestion control: %s, the kernel's choice", c.used)
	default:
		// #nosec G706 -- the kernel's answer, not a client's input.
		log.Printf("TCP congestion control: %s", c.used)
	}
}
