// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package config_test

import (
	"net/netip"
	"testing"

	"changkun.de/x/occamy/internal/config"
)

func TestParseAllowEntry(t *testing.T) {
	prefix := netip.MustParsePrefix
	for _, tt := range []struct {
		in   string
		want config.AllowEntry
	}{
		{"10.0.0.0/8", config.AllowEntry{Prefix: prefix("10.0.0.0/8")}},
		{"10.1.2.3/8", config.AllowEntry{Prefix: prefix("10.0.0.0/8")}},
		{"10.0.0.0/8:22", config.AllowEntry{Prefix: prefix("10.0.0.0/8"), Ports: config.PortRange{First: 22, Last: 22}}},
		{"192.0.2.7", config.AllowEntry{Prefix: prefix("192.0.2.7/32")}},
		{"192.0.2.7:5900-5910", config.AllowEntry{Prefix: prefix("192.0.2.7/32"), Ports: config.PortRange{First: 5900, Last: 5910}}},
		{"::ffff:172.16.0.0/108", config.AllowEntry{Prefix: prefix("172.16.0.0/12")}},
		{"::ffff:192.0.2.7", config.AllowEntry{Prefix: prefix("192.0.2.7/32")}},
		{"fd00::/8", config.AllowEntry{Prefix: prefix("fd00::/8")}},
		{"[fd00::/8]:22", config.AllowEntry{Prefix: prefix("fd00::/8"), Ports: config.PortRange{First: 22, Last: 22}}},
		{"[fd00::1]", config.AllowEntry{Prefix: prefix("fd00::1/128")}},
		{"Build.Example.", config.AllowEntry{Name: "build.example"}},
		{"build.example:2222", config.AllowEntry{Name: "build.example", Ports: config.PortRange{First: 2222, Last: 2222}}},
		{"*.Lab.Internal", config.AllowEntry{Domain: "lab.internal"}},
		{"*.lab.internal:1-65535", config.AllowEntry{Domain: "lab.internal", Ports: config.PortRange{First: 1, Last: 65535}}},
	} {
		got, err := config.ParseAllowEntry(tt.in)
		if err != nil || got != tt.want {
			t.Errorf("ParseAllowEntry(%q) = %+v, %v; want %+v", tt.in, got, err, tt.want)
		}
	}
	for _, in := range []string{"", "*", "*.", "*.10.0.0.1", "10.0.0.0/33", "a.example:0", "a.example:65536", "a.example:+22", "a.example:22-", "[a.example]:22", "fe80::1%en0"} {
		if e, err := config.ParseAllowEntry(in); err == nil {
			t.Errorf("ParseAllowEntry(%q) = %+v, want an error", in, e)
		}
	}
}

func TestAllowsPort(t *testing.T) {
	byDefault := config.AllowEntry{}
	ssh := config.AllowEntry{Ports: config.PortRange{First: 2222, Last: 2222}}
	for _, tt := range []struct {
		e        config.AllowEntry
		protocol string
		port     uint16
		want     bool
	}{
		{byDefault, "ssh", 22, true},
		{byDefault, "ssh", 2222, false},
		{byDefault, "vnc", 5900, true},
		{byDefault, "vnc", 5999, true},
		{byDefault, "vnc", 6000, false},
		{byDefault, "rdp", 3389, true},
		{byDefault, "telnet", 23, true},
		{byDefault, "telnet", 22, false},
		{byDefault, "kubernetes", 443, false},
		{ssh, "ssh", 2222, true},
		{ssh, "ssh", 22, false},
		{ssh, "vnc", 2222, true},
	} {
		if got := tt.e.AllowsPort(tt.protocol, tt.port); got != tt.want {
			t.Errorf("%+v.AllowsPort(%s, %d) = %v, want %v", tt.e, tt.protocol, tt.port, got, tt.want)
		}
	}
}
