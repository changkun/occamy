// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

//go:build !unix

package stream

import "time"

// cpuTime is 0 where the process's CPU time is not measured.
func cpuTime() time.Duration { return 0 }
