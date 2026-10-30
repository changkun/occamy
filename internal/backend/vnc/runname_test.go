// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package vnc

// The names of the images and containers that integration runs make
// (vencrypt_integration_test.go), here without the build tag so that
// the matcher, which decides what a run removes, is tested everywhere.

import (
	"flag"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// runPrefix starts the names of the images and containers that runs
// make, which go on with the Unix time a run ends at the latest, a
// hyphen, and the process id.
const runPrefix = "occamy-vnc-it-until"

// runPattern matches a run's name exactly; the time is its group.
var runPattern = regexp.MustCompile(`^` + regexp.QuoteMeta(runPrefix) + `([0-9]+)-[0-9]+$`)

// listedPattern matches a run's name as an engine lists the image or
// the container: bare, as docker lists both and podman containers, or
// after localhost/, as podman lists an image built here, and with a
// tag after it or none. A name from any other registry or path is
// someone else's, even if it ends in a run's name. The time is its
// group.
var listedPattern = regexp.MustCompile(`^(?:localhost/)?` + regexp.QuoteMeta(runPrefix) +
	`([0-9]+)-[0-9]+(?::[A-Za-z0-9_][A-Za-z0-9_.-]{0,127})?$`)

// runName returns this run's name for its image and container.
func runName() string {
	timeout := 24 * time.Hour
	if f := flag.Lookup("test.timeout"); f != nil {
		if d, err := time.ParseDuration(f.Value.String()); err == nil && d > 0 {
			timeout = d
		}
	}
	return fmt.Sprintf("%s%d-%d", runPrefix, time.Now().Add(timeout+time.Minute).Unix(), os.Getpid())
}

// expired reports whether name, a container's or an image's as an
// engine lists it (listedPattern), is a run's whose time has passed by
// now.
func expired(name string, now time.Time) bool {
	m := listedPattern.FindStringSubmatch(name)
	if m == nil {
		return false
	}
	until, err := strconv.ParseInt(m[1], 10, 64)
	return err == nil && now.Unix() > until
}

func TestExpired(t *testing.T) {
	now := time.Unix(1000, 0)
	for _, tt := range []struct {
		name string
		want bool
	}{
		{"occamy-vnc-it-until999-42", true},
		{"occamy-vnc-it-until999-42:latest", true},
		{"localhost/occamy-vnc-it-until999-42", true},
		{"localhost/occamy-vnc-it-until999-42:latest", true},
		{"localhost/occamy-vnc-it-until999-42:v1.2_rc-3", true},
		{"occamy-vnc-it-until1000-42", false}, // not yet past
		{"occamy-vnc-it-until1001-42", false},
		{"localhost/occamy-vnc-it-until1001-42:latest", false},
		// Other people's names that contain the prefix.
		{"backup-of-occamy-vnc-it-until1-keep", false},
		{"backup-of-occamy-vnc-it-until1-2", false},
		{"docker.io/someone/occamy-vnc-it-until1", false},
		// A run's name pulled from a registry, or under a path, is not a
		// run's: only a bare name or localhost/ is.
		{"docker.io/someone/occamy-vnc-it-until1-2", false},
		{"docker.io/someone/occamy-vnc-it-until1-2:latest", false},
		{"docker.io/library/occamy-vnc-it-until1-2", false},
		{"ghcr.io/changkun/occamy-vnc-it-until1-2", false},
		{"localhost:5000/occamy-vnc-it-until999-42", false},
		{"localhost/someone/occamy-vnc-it-until1-2", false},
		{"someone/occamy-vnc-it-until1-2", false},
		{"/occamy-vnc-it-until1-2", false},
		{"localhostx/occamy-vnc-it-until1-2", false},
		{"LOCALHOST/occamy-vnc-it-until1-2", false},
		// Tags that are not tags.
		{"occamy-vnc-it-until1-2:", false},
		{"occamy-vnc-it-until1-2:.latest", false},
		{"occamy-vnc-it-until1-2:a/b", false},
		{"occamy-vnc-it-until1-2:a:b", false},
		{"occamy-vnc-it-until1-2@sha256:0123", false},
		{"occamy-vnc-it-until1-2:" + strings.Repeat("a", 129), false},
		{"occamy-vnc-it-until+1-2", false},
		{"occamy-vnc-it-until-1-2", false},
		{"occamy-vnc-it-until1-2-keep", false},
		{"occamy-vnc-it-until1-", false},
		{"occamy-vnc-it-until1", false},
		{"occamy-vnc-it-until 1-2", false},
		{"occamy-vnc-it-until١-2", false}, // a digit, but not ASCII
		{"occamy-vnc-it-until1-2/x", false},
		{"occamy-vnc-it-until99999999999999999999-2", false}, // beyond int64
		{"occamy-rdp-it-until1-2", false},
		{"", false},
	} {
		if got := expired(tt.name, now); got != tt.want {
			t.Errorf("expired(%q) = %v, want %v", tt.name, got, tt.want)
		}
	}
	// A run's own name matches, and has not expired.
	name := runName()
	if !runPattern.MatchString(name) || expired(name, time.Now()) {
		t.Errorf("runName() = %q: matches %v, expired %v", name, runPattern.MatchString(name), expired(name, time.Now()))
	}
}
