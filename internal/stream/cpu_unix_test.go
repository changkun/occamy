// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

//go:build unix

package stream

import (
	"syscall"
	"time"
)

// cpuTime is the CPU time the process has used, user and system, on
// every goroutine.
func cpuTime() time.Duration {
	var u syscall.Rusage
	if syscall.Getrusage(syscall.RUSAGE_SELF, &u) != nil {
		return 0
	}
	return time.Duration(u.Utime.Nano() + u.Stime.Nano())
}
