// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package tests

import (
	"os/exec"
	"testing"
)

// TestComposeRun checks that composeCmd.run, through which the chansrv
// subtest of TestE2EV1RDP counts processes in the rdp container, returns
// what the command printed on its standard output and nothing else. podman
// compose prints a notice of its compose provider on standard error,
// which made the count unreadable.
func TestComposeRun(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	c := composeCmd{"sh", "-c", `echo '>>>> Executing external compose provider "/usr/local/bin/docker-compose". <<<<' >&2; echo "$@"`, "compose"}
	if got, want := c.run(t, "exec", "-T", "rdp"), "exec -T rdp\n"; got != want {
		t.Errorf("run returned %q, want %q", got, want)
	}
}
