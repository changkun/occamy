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
// backends that its connections compare: ssh on the native SSH backend,
// vnc-native and rdp-native on their protocols' default, native, and
// vnc-guacd and rdp-guacd on the guacd fallback they ask for. It also checks the image's web client, and that
// its user's password is the one docker/occamy-v1.yaml and tests/ give.
func TestDemoV1File(t *testing.T) {
	raw, err := os.ReadFile("../../docker/occamy-v1.yaml")
	if err != nil {
		t.Fatal(err)
	}
	// What the compose service gives it: a tmpfs for the signing key,
	// and the SSH, VNC and RDP passwords.
	raw = []byte(strings.ReplaceAll(string(raw), "/run/occamy/", t.TempDir()+"/"))
	t.Setenv("OCCAMY_DEMO_SSH_PASSWORD", "occamy")
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
		"ssh":        config.BackendNative,
		"vnc-native": config.BackendNative, "vnc-guacd": config.BackendGuacd,
		"rdp-native": config.BackendNative, "rdp-guacd": config.BackendGuacd,
	}
	if !maps.Equal(backends, want) {
		t.Errorf("backends %v, want %v", backends, want)
	}
	// The image's build of the web client, where the Dockerfile puts it.
	if f.WebClient != "client/occamy-web/dist" {
		t.Errorf("web_client %q, want the image's client/occamy-web/dist", f.WebClient)
	}
	if len(f.Auth.Users) != 1 || bcrypt.CompareHashAndPassword([]byte(f.Auth.Users[0].PasswordHash), []byte("occamy")) != nil {
		t.Errorf("users %+v, want demo with the password occamy", f.Auth.Users)
	}
}
