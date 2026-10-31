// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package config_test

import (
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"changkun.de/x/occamy/internal/backend/vnc/vnctest"
	"changkun.de/x/occamy/internal/config"
)

// TestDemoV1File checks that the demo stack's v1 file loads, with the
// backends that its connections compare (tests/parity_test.go):
// vnc-native on vnc's default, native, and every other connection on
// the backend it names. It also checks that its user's password is the
// one docker/occamy-v1.yaml and tests/ give.
func TestDemoV1File(t *testing.T) {
	raw, err := os.ReadFile("../../docker/occamy-v1.yaml")
	if err != nil {
		t.Fatal(err)
	}
	// What the compose service gives it: a tmpfs for the signing key,
	// the tigervnc service's CA, and the passwords.
	ca, err := vnctest.SelfSigned("tigervnc")
	if err != nil {
		t.Fatal(err)
	}
	caDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(caDir, "ca.pem"), ca.PEM, 0o600); err != nil {
		t.Fatal(err)
	}
	raw = []byte(strings.NewReplacer("/run/occamy/", t.TempDir()+"/", "/run/occamy-vnc-ca/", caDir+"/").Replace(string(raw)))
	t.Setenv("OCCAMY_DEMO_VNC_PASSWORD", "vncpassword")
	t.Setenv("OCCAMY_DEMO_PLAIN_PASSWORD", "plainpassword")
	f, err := config.ParseV1(raw)
	if err != nil {
		t.Fatal(err)
	}
	backends := map[string]string{}
	for _, c := range f.Connections {
		backends[c.ID] = c.Backend
	}
	// Every connection but vnc-native names its backend, so that the
	// comparison does not change with vnc's default; vnc-native checks
	// the default.
	want := map[string]string{}
	for _, id := range []string{"vnc", "vnc-none", "vnc-de", "vnc-fr", "tigervnc", "tigervnc-prompt", "tigervnc-plain", "tigervnc-none", "tigervnc-de", "tigervnc-fr"} {
		want[id+"-native"] = config.BackendNative
		want[id+"-guacd"] = config.BackendGuacd
	}
	if !maps.Equal(backends, want) {
		t.Errorf("backends %v, want %v", backends, want)
	}
	if n := strings.Count(string(raw), "\n    backend: "); n != len(want)-1 {
		t.Errorf("%d connections name their backend, want all %d but vnc-native", n, len(want)-1)
	}
	if len(f.Auth.Users) != 1 || bcrypt.CompareHashAndPassword([]byte(f.Auth.Users[0].PasswordHash), []byte("occamy")) != nil {
		t.Errorf("users %+v, want demo with the password occamy", f.Auth.Users)
	}
	// The debug listener is off but where make run OCCAMY_DEBUG=1 sets
	// OCCAMY_V1_DEBUG_ADDRESS (docker-compose.yaml passes it on, empty
	// by default).
	for _, address := range []string{"", "0.0.0.0:5638"} {
		t.Setenv("OCCAMY_V1_DEBUG_ADDRESS", address)
		f, err := config.ParseV1(raw)
		if err != nil {
			t.Fatal(err)
		}
		if f.Debug.Address != address {
			t.Errorf("with OCCAMY_V1_DEBUG_ADDRESS=%q, the debug listener is on %q", address, f.Debug.Address)
		}
	}
}

// TestDemoV0Files checks the demo stack's v0 files: occamy.yaml, in
// release mode, which serves no pprof, and occamy-debug.yaml, which make
// run OCCAMY_DEBUG=1 mounts in its place for the pressure test, the same
// but in debug mode.
func TestDemoV0Files(t *testing.T) {
	read := func(name string) string {
		raw, err := os.ReadFile("../../docker/" + name)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	release, debug := read("occamy.yaml"), read("occamy-debug.yaml")
	for _, tt := range []struct {
		name, raw string
		debug     bool
	}{
		{"occamy.yaml", release, false},
		{"occamy-debug.yaml", debug, true},
	} {
		c, err := config.Parse([]byte(tt.raw))
		if err != nil {
			t.Fatalf("%s: %v", tt.name, err)
		}
		if c.Debug() != tt.debug {
			t.Errorf("%s: debug mode %v, want %v", tt.name, c.Debug(), tt.debug)
		}
	}
	// The settings, past the comments on top, but for the mode.
	settings := func(raw string) string {
		_, body, _ := strings.Cut(raw, "\n---\n")
		var lines []string
		for line := range strings.SplitSeq(body, "\n") {
			if !strings.HasPrefix(line, "mode:") {
				lines = append(lines, line)
			}
		}
		return strings.Join(lines, "\n")
	}
	if settings(release) != settings(debug) {
		t.Errorf("occamy-debug.yaml differs from occamy.yaml in more than the mode:\n%s\n---\n%s", settings(release), settings(debug))
	}
}
