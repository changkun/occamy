// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package config_test

import (
	"bytes"
	"flag"
	"strings"
	"testing"

	"changkun.de/x/occamy/internal/config"
)

// TestUsage checks occamyd's -h: it says what occamyd serves now, not
// the Guacamole protocol of v0 alone, and lists each flag on a line of
// its own after the usage line, which it once ran into.
func TestUsage(t *testing.T) {
	fs := flag.NewFlagSet("occamyd", flag.ContinueOnError)
	config.Flags(fs)
	var b bytes.Buffer
	config.Usage(&b, fs)
	out := b.String()
	if strings.Contains(strings.ToLower(out), "guacamole") {
		t.Errorf("the help describes occamyd as a Guacamole proxy:\n%s", out)
	}
	for _, want := range []string{"API v1", "v0", "\n\toccamyd [-conf file]\n", "\n  -conf file\n", `(default "./conf.yaml")`} {
		if !strings.Contains(out, want) {
			t.Errorf("the help lacks %q:\n%s", want, out)
		}
	}
	for line := range strings.SplitSeq(out, "\n") {
		if strings.Contains(line, "Usage:") && strings.TrimSpace(line) != "Usage:" {
			t.Errorf("the usage line %q runs into what follows it:\n%s", line, out)
		}
	}
}
