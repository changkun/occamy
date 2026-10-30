// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package backend_test

import (
	"testing"

	"changkun.de/x/occamy/internal/backend"
)

func TestClassString(t *testing.T) {
	for c, want := range map[backend.Class]string{
		backend.Terminal:  "terminal",
		backend.Graphical: "graphical",
		0:                 "Class(0)",
		7:                 "Class(7)",
	} {
		if got := c.String(); got != want {
			t.Errorf("Class(%d).String() = %q, want %q", int(c), got, want)
		}
	}
}
