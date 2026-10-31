// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package config_test

import (
	"maps"
	"os"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"changkun.de/x/occamy/internal/config"
)

// TestDemoV1File checks that the demo stack's v1 file loads, with the
// backends that its connections compare: vnc-native on vnc's default,
// native, and vnc-guacd on the guacd fallback it asks for. It also
// checks that its user's password is the one docker/occamy-v1.yaml and
// tests/ give.
func TestDemoV1File(t *testing.T) {
	raw, err := os.ReadFile("../../docker/occamy-v1.yaml")
	if err != nil {
		t.Fatal(err)
	}
	// What the compose service gives it: a tmpfs for the signing key,
	// and the VNC and RDP passwords.
	raw = []byte(strings.ReplaceAll(string(raw), "/run/occamy/", t.TempDir()+"/"))
	t.Setenv("OCCAMY_DEMO_VNC_PASSWORD", "vncpassword")
	t.Setenv("OCCAMY_DEMO_RDP_PASSWORD", "occamy")
	f, err := config.ParseV1(raw)
	if err != nil {
		t.Fatal(err)
	}
	backends := map[string]string{}
	for _, c := range f.Connections {
		backends[c.ID] = c.Backend
	}
	want := map[string]string{
		"vnc-native": config.BackendNative, "vnc-guacd": config.BackendGuacd,
		"rdp-native": config.BackendNative, "rdp-guacd": config.BackendGuacd,
	}
	if !maps.Equal(backends, want) {
		t.Errorf("backends %v, want %v", backends, want)
	}
	if len(f.Auth.Users) != 1 || bcrypt.CompareHashAndPassword([]byte(f.Auth.Users[0].PasswordHash), []byte("occamy")) != nil {
		t.Errorf("users %+v, want demo with the password occamy", f.Auth.Users)
	}
}
