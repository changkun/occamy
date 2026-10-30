// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package server

import (
	"context"
	"errors"
	"net/netip"
	"testing"
)

func TestAllowlist(t *testing.T) {
	a := newAllowlist([]string{
		"10.0.0.0/8", "192.168.1.5", "::ffff:172.16.0.0/108", "fd00::/8",
		"*.Lab.Internal.", "Build.Example",
	})
	var looked []string
	a.lookup = func(_ context.Context, host string) ([]netip.Addr, error) {
		looked = append(looked, host)
		switch host {
		case "inside.example":
			return []netip.Addr{netip.MustParseAddr("10.1.1.1"), netip.MustParseAddr("fd00::2")}, nil
		case "mapped.example":
			return []netip.Addr{netip.MustParseAddr("::ffff:10.2.2.2")}, nil
		case "mixed.example":
			return []netip.Addr{netip.MustParseAddr("10.1.1.1"), netip.MustParseAddr("1.1.1.1")}, nil
		case "empty.example":
			return nil, nil
		}
		return nil, errors.New("no such host")
	}
	for _, tt := range []struct {
		host, dial string // dial is empty for a target not allowed
		lookup     bool
	}{
		{"10.1.2.3", "10.1.2.3", false},
		{"::ffff:10.1.2.3", "10.1.2.3", false},
		{"172.16.5.5", "172.16.5.5", false},
		{"172.32.0.1", "", false},
		{"fd00::1", "fd00::1", false},
		{"fe80::1%en0", "", false},
		{"fd00::1%en0", "", false},
		{"192.168.1.5", "192.168.1.5", false},
		{"192.168.1.6", "", false},
		{"8.8.8.8", "", false},
		{"host.lab.internal", "host.lab.internal", false},
		{"HOST.Lab.internal.", "HOST.Lab.internal.", false},
		{"a.b.lab.internal", "a.b.lab.internal", false},
		{"build.example", "build.example", false},
		{"BUILD.example.", "BUILD.example.", false},
		{"lab.internal", "", true},
		{"evillab.internal", "", true},
		{"sub.build.example", "", true},
		{"inside.example", "10.1.1.1", true},
		{"mapped.example", "10.2.2.2", true},
		{"mixed.example", "", true},
		{"empty.example", "", true},
		{"unknown.example", "", true},
	} {
		looked = nil
		dial, err := a.allow(context.Background(), tt.host)
		if tt.dial == "" {
			if !errors.Is(err, errNotAllowed) {
				t.Errorf("%s: %q %v, want refused", tt.host, dial, err)
			}
		} else if err != nil || dial != tt.dial {
			t.Errorf("%s: %q %v, want %q", tt.host, dial, err, tt.dial)
		}
		if (len(looked) > 0) != tt.lookup {
			t.Errorf("%s: looked up %v", tt.host, looked)
		}
	}

	// A list of names alone resolves nothing: no address could pass.
	names := newAllowlist([]string{"build.example"})
	names.lookup = func(context.Context, string) ([]netip.Addr, error) {
		t.Error("a list without ranges looked a name up")
		return nil, nil
	}
	if _, err := names.allow(context.Background(), "other.example"); !errors.Is(err, errNotAllowed) {
		t.Errorf("other.example: %v", err)
	}
}
