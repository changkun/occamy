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
// backends that its connections compare (tests/parity_test.go): ssh
// on the native SSH backend, vnc-native and rdp-native on their
// protocols' default, native, and every other connection on the backend
// it names. It also checks the image's web client, and that its user's
// password is the one docker/occamy-v1.yaml and tests/ give.
func TestDemoV1File(t *testing.T) {
	raw, err := os.ReadFile("../../docker/occamy-v1.yaml")
	if err != nil {
		t.Fatal(err)
	}
	// What the compose service gives it: a tmpfs for the signing key,
	// the tigervnc service's CA, and the SSH, VNC and RDP passwords.
	ca, err := vnctest.SelfSigned("tigervnc")
	if err != nil {
		t.Fatal(err)
	}
	caDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(caDir, "ca.pem"), ca.PEM, 0o600); err != nil {
		t.Fatal(err)
	}
	raw = []byte(strings.NewReplacer("/run/occamy/", t.TempDir()+"/", "/run/occamy-vnc-ca/", caDir+"/").Replace(string(raw)))
	t.Setenv("OCCAMY_DEMO_SSH_PASSWORD", "occamy")
	t.Setenv("OCCAMY_DEMO_VNC_PASSWORD", "vncpassword")
	t.Setenv("OCCAMY_DEMO_PLAIN_PASSWORD", "plainpassword")
	t.Setenv("OCCAMY_DEMO_RDP_PASSWORD", "occamy")
	f, err := config.ParseV1(raw)
	if err != nil {
		t.Fatal(err)
	}
	backends := map[string]string{}
	for _, c := range f.Connections {
		backends[c.ID] = c.Backend
	}
	// Every connection but ssh, vnc-native and rdp-native names its
	// backend, so that the comparison does not change with a protocol's
	// default; vnc-native and rdp-native check the defaults, and ssh has
	// only the native backend.
	want := map[string]string{
		"ssh":        config.BackendNative,
		"rdp-native": config.BackendNative, "rdp-guacd": config.BackendGuacd,
	}
	for _, id := range []string{"vnc", "vnc-none", "vnc-de", "vnc-fr", "tigervnc", "tigervnc-prompt", "tigervnc-plain", "tigervnc-none", "tigervnc-de", "tigervnc-fr"} {
		want[id+"-native"] = config.BackendNative
		want[id+"-guacd"] = config.BackendGuacd
	}
	if !maps.Equal(backends, want) {
		t.Errorf("backends %v, want %v", backends, want)
	}
	if n := strings.Count(string(raw), "\n    backend: "); n != len(want)-3 {
		t.Errorf("%d connections name their backend, want %d: all but ssh, vnc-native and rdp-native", n, len(want)-3)
	}
	// The image's build of the web client, where the Dockerfile puts it.
	if f.WebClient != "client/occamy-web/dist" {
		t.Errorf("web_client %q, want the image's client/occamy-web/dist", f.WebClient)
	}
	if len(f.Auth.Users) != 1 || bcrypt.CompareHashAndPassword([]byte(f.Auth.Users[0].PasswordHash), []byte("occamy")) != nil {
		t.Errorf("users %+v, want demo with the password occamy", f.Auth.Users)
	}
}
