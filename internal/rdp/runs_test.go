// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package rdp

import (
	"regexp"
	"strconv"
	"testing"
	"time"
)

// The names of the images and containers of the integration test's runs
// (integration_test.go), whose matcher is tested without the build tag,
// which starts a container.

// runPrefix starts the names of the images and containers that runs
// make, which go on with the Unix time a run ends at the latest and the
// run's process ID.
const runPrefix = "occamy-rdp-it-until"

// runPattern matches the names runs make, alone or after "localhost/",
// which podman puts before the images it builds. Nothing else matches,
// so that a run removes nobody else's image or container.
var runPattern = regexp.MustCompile(`^(?:localhost/)?` + runPrefix + `(\d+)-\d+$`)

// expired reports whether name is a run's image or container whose time
// had passed at now.
func expired(name string, now time.Time) bool {
	m := runPattern.FindStringSubmatch(name)
	if m == nil {
		return false
	}
	until, err := strconv.ParseInt(m[1], 10, 64)
	return err == nil && now.Unix() > until
}

func TestRunExpired(t *testing.T) {
	now := time.Unix(200, 0)
	for name, want := range map[string]bool{
		"occamy-rdp-it-until100-5":                  true,
		"localhost/occamy-rdp-it-until100-5":        true,
		"occamy-rdp-it-until300-5":                  false, // not yet
		"backup-of-occamy-rdp-it-until1-keep":       false,
		"occamy-rdp-it-until1-2-keep":               false,
		"occamy-rdp-it-until1":                      false,
		"docker.io/someone/occamy-rdp-it-until1":    false,
		"docker.io/someone/occamy-rdp-it-until1-2":  false,
		"registry.test/occamy-rdp-it-until1-2":      false,
		"occamy-rdp-it-until99999999999999999999-1": false,
		"occamy-rdp-integration":                    false,
	} {
		if got := expired(name, now); got != want {
			t.Errorf("expired(%q) = %v, want %v", name, got, want)
		}
	}
}
