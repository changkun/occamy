// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// Package race reports whether the race detector is on, for tests whose
// counts it changes. Under -race, sync.Pool drops a quarter of what is
// put back, so code that takes from a pool, as fmt's printing does,
// allocates more often, and a count from testing.AllocsPerRun varies
// from run to run. The standard library's tests skip such counts under
// -race too.
package race
