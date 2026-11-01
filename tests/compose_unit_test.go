// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package tests

import (
	"os/exec"
	"testing"
)

// TestComposeRun checks that composeCmd.run, through which the tests
// that reach into the stack read what a command printed in a container
// and the containers' logs, returns what compose printed to its standard
// output and nothing else, and that it runs compose with
// PODMAN_COMPOSE_WARNING_LOGS=false. podman compose's banner on its
// standard error made the chansrv subtest of TestE2EV1RDP fail to count
// processes in the rdp container.
func TestComposeRun(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	// A compose that prints podman compose's banner to its standard
	// error, its arguments to its standard output, and, as a container's
	// line, "from stderr" to its standard error. Unless
	// PODMAN_COMPOSE_WARNING_LOGS is false, it also prints "warning logs
	// on" to its standard output, which shows whether run sets it.
	c := composeCmd{"sh", "-c", `[ "$PODMAN_COMPOSE_WARNING_LOGS" = false ] || echo 'warning logs on'
echo '>>>> Executing external compose provider "/usr/local/bin/docker-compose". <<<<' >&2
echo "$@"
echo 'from stderr' >&2`, "compose"}
	if got, want := c.run(t, "exec", "-T", "rdp"), "exec -T rdp\n"; got != want {
		t.Errorf("run returned %q, want %q", got, want)
	}
}
