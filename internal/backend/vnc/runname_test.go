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

// expired reports whether name, a container's or an image's, is a
// run's whose time has passed by now. An image's name may have a
// registry and a path before it, as podman lists them, and a tag after
// it; what is left must be a run's name, whole.
func expired(name string, now time.Time) bool {
	base := name[strings.LastIndex(name, "/")+1:]
	base, _, _ = strings.Cut(base, ":")
	m := runPattern.FindStringSubmatch(base)
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
		{"localhost/occamy-vnc-it-until999-42", true},
		{"localhost/occamy-vnc-it-until999-42:latest", true},
		{"localhost:5000/occamy-vnc-it-until999-42", true},
		{"occamy-vnc-it-until1000-42", false}, // not yet past
		{"occamy-vnc-it-until1001-42", false},
		// Other people's names that contain the prefix.
		{"backup-of-occamy-vnc-it-until1-keep", false},
		{"backup-of-occamy-vnc-it-until1-2", false},
		{"docker.io/someone/occamy-vnc-it-until1", false},
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
