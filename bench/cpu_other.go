// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

//go:build !unix

package main

import "time"

// processCPU returns 0 where the harness does not read its own CPU time,
// which leaves client_cpu_ms out of the results.
func processCPU() time.Duration { return 0 }
