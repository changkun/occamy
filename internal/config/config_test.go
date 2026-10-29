// Copyright 2019 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package config_test

import (
	"maps"
	"os"
	"testing"

	"changkun.de/x/occamy/internal/config"
)

func TestJWT_GenerateID(t *testing.T) {
	os.Args[1] = "-conf=../../conf.yaml"
	config.Init()

	j := config.JWT{
		Protocol: "vnc",
		Host:     "0.0.0.0:5636",
		Username: "occamy",
		Password: "occamy",
	}
	want := "d742d2c10082f08506028cfb09cd1674"
	if want != j.GenerateID() {
		t.Error("jwt hash error, got: ", j.GenerateID())
		t.FailNow()
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
