// Copyright 2019 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package config_test

import (
	"maps"
	"os"
	"strings"
	"testing"

	"changkun.de/x/occamy/internal/config"
)

func TestJWT_GenerateID(t *testing.T) {
	j := config.JWT{
		Protocol: "vnc",
		Host:     "0.0.0.0:5636",
		Username: "occamy",
		Password: "occamy",
	}
	want := "07b66ddee5267f334f08073d2261960e0dd256e6fc7d5bd593bbbae4ee5f740c"
	if want != j.GenerateID() {
		t.Error("jwt hash error, got: ", j.GenerateID())
		t.FailNow()
	}
}

// TestInit checks that Init reads the v0 file in the repository and
// refuses a v1 file, which the lenient v0 decoder would read with
// defaults.
func TestInit(t *testing.T) {
	raw, err := os.ReadFile("../../conf.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err := config.Init(raw); err != nil {
		t.Fatalf("Init(conf.yaml): %v", err)
	}
	if config.Runtime.Auth.JWTSecret != "occamy" || !config.Runtime.Client {
		t.Errorf("Init(conf.yaml) read %+v", config.Runtime)
	}
	raw, err = os.ReadFile("testdata/v1.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err := config.Init(raw); err == nil || !strings.Contains(err.Error(), "version 1") {
		t.Errorf("Init(v1.yaml) = %v, want a refusal", err)
	}
	if err := config.Init([]byte("version: 1\naddress: :1\nauth: {jwt_secret: x}\n")); err == nil {
		t.Error("Init of a v1 file with v0 keys did not fail")
	}
}

// TestGenerateIDSeparatesClaims checks that claims which concatenate to the
// same string get different session keys, so that one user never joins
// another's session.
func TestGenerateIDSeparatesClaims(t *testing.T) {
	a := config.JWT{Protocol: "vnc", Host: "desktop:5900", Username: "ab", Password: "c"}
	b := config.JWT{Protocol: "vnc", Host: "desktop:5900", Username: "a", Password: "bc"}
	if a.GenerateID() == b.GenerateID() {
		t.Fatalf("claims %+v and %+v share the session key %s", a, b, a.GenerateID())
	}
}

func TestGuacdParams(t *testing.T) {
	j := config.JWT{
		Protocol: "rdp",
		Host:     "rdp:3389",
		Username: "occamy",
		Password: "secret",
	}
	defaults := map[string]map[string]string{
		"rdp": {"ignore-cert": "true", "port": "1", "username": "admin"},
		"vnc": {"color-depth": "16"},
	}
	got, err := j.GuacdParams(defaults)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"hostname":    "rdp",
		"port":        "3389",
		"username":    "occamy",
		"password":    "secret",
		"ignore-cert": "true",
	}
	if !maps.Equal(got, want) {
		t.Errorf("GuacdParams() = %v, want %v", got, want)
	}
	if defaults["rdp"]["port"] != "1" {
		t.Errorf("GuacdParams() changed the defaults: %v", defaults)
	}
}

func TestGuacdParamsMissingPort(t *testing.T) {
	j := config.JWT{Protocol: "vnc", Host: "vnc", Password: "vncpassword"}
	if _, err := j.GuacdParams(nil); err == nil {
		t.Error("GuacdParams() with a host without a port did not fail")
	}
}

func TestGuacdDefaultAddress(t *testing.T) {
	for _, raw := range []string{
		"address: 0.0.0.0:5636\n",
		"guacd:\n  params:\n    rdp:\n      ignore-cert: \"true\"\n",
	} {
		c, err := config.Parse([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		if c.Guacd.Address != config.DefaultGuacdAddress {
			t.Errorf("guacd address for %q = %q, want %q", raw, c.Guacd.Address, config.DefaultGuacdAddress)
		}
	}
	c, err := config.Parse([]byte("guacd:\n  address: guacd:4822\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Guacd.Address != "guacd:4822" {
		t.Errorf("guacd address = %q, want guacd:4822", c.Guacd.Address)
	}
}

// TestMode checks the modes gin knew: debug, also when empty, serves
// pprof, and any other mode fails as gin.SetMode did.
func TestMode(t *testing.T) {
	for _, tt := range []struct {
		raw   string
		debug bool
	}{
		{"address: 0.0.0.0:5636\n", true},
		{"mode: debug\n", true},
		{"mode: release\n", false},
		{"mode: test\n", false},
	} {
		c, err := config.Parse([]byte(tt.raw))
		if err != nil {
			t.Fatalf("%q: %v", tt.raw, err)
		}
		if c.Debug() != tt.debug {
			t.Errorf("%q: Debug() = %v, want %v", tt.raw, c.Debug(), tt.debug)
		}
	}
	if _, err := config.Parse([]byte("mode: production\n")); err == nil {
		t.Error("mode production did not fail")
	}
}
