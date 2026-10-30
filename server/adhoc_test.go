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
	a, err := newAllowlist([]string{
		"10.0.0.0/8", "192.168.1.5", "::ffff:172.16.0.0/108", "fd00::/8",
		"*.Lab.Internal.", "Build.Example",
		"192.168.2.0/24:2222", "[2001:db8::/32]:5901-5902", "ports.example:8022",
		"0.0.0.0/0:443", "127.0.0.1:2222", "169.254.169.254", "localhost",
	})
	if err != nil {
		t.Fatal(err)
	}
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
		case "host.lab.internal", "a.b.lab.internal", "build.example", "ports.example":
			return []netip.Addr{netip.MustParseAddr("203.0.113.9")}, nil
		case "rebound.lab.internal", "localhost":
			return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
		case "metadata.lab.internal":
			return []netip.Addr{netip.MustParseAddr("169.254.169.254")}, nil
		}
		return nil, errors.New("no such host")
	}
	for _, tt := range []struct {
		protocol, host string
		port           uint16
		dial           string // empty for a target not allowed
		lookup         bool
	}{
		{"ssh", "10.1.2.3", 22, "10.1.2.3", false},
		{"ssh", "10.1.2.3", 2222, "", false}, // not ssh's port
		{"vnc", "10.1.2.3", 5901, "10.1.2.3", false},
		{"vnc", "10.1.2.3", 6000, "", false},
		{"rdp", "10.1.2.3", 3389, "10.1.2.3", false},
		{"telnet", "10.1.2.3", 23, "10.1.2.3", false},
		{"ssh", "::ffff:10.1.2.3", 22, "10.1.2.3", false},
		{"ssh", "172.16.5.5", 22, "172.16.5.5", false},
		{"ssh", "172.32.0.1", 22, "", false},
		{"ssh", "fd00::1", 22, "fd00::1", false},
		{"ssh", "fe80::1%en0", 22, "", false},
		{"ssh", "fd00::1%en0", 22, "", false},
		{"ssh", "192.168.1.5", 22, "192.168.1.5", false},
		{"ssh", "192.168.1.6", 22, "", false},
		{"ssh", "8.8.8.8", 22, "", false},
		{"ssh", "192.168.2.7", 2222, "192.168.2.7", false},
		{"ssh", "192.168.2.7", 22, "", false},
		{"vnc", "2001:db8::7", 5902, "2001:db8::7", false},
		{"vnc", "2001:db8::7", 5900, "", false},
		{"rdp", "8.8.8.8", 443, "8.8.8.8", false},
		// Special addresses: only an entry that is the address opens one.
		{"rdp", "127.0.0.1", 443, "", false},
		{"rdp", "0.0.0.0", 443, "", false},
		{"rdp", "224.0.0.1", 443, "", false},
		{"rdp", "169.254.1.1", 443, "", false},
		{"ssh", "127.0.0.1", 2222, "127.0.0.1", false},
		{"ssh", "127.0.0.1", 22, "", false},
		{"ssh", "169.254.169.254", 22, "169.254.169.254", false},
		{"ssh", "::1", 22, "", false},
		{"ssh", "::ffff:127.0.0.1", 2222, "127.0.0.1", false},
		// Names are resolved, and dialled by the address checked.
		{"ssh", "host.lab.internal", 22, "203.0.113.9", true},
		{"ssh", "HOST.Lab.internal.", 22, "203.0.113.9", true},
		{"ssh", "a.b.lab.internal", 22, "203.0.113.9", true},
		{"ssh", "build.example", 22, "203.0.113.9", true},
		{"ssh", "BUILD.example.", 22, "203.0.113.9", true},
		{"ssh", "build.example", 2222, "", true},
		{"ssh", "ports.example", 8022, "203.0.113.9", true},
		{"ssh", "ports.example", 22, "", true},
		{"ssh", "rebound.lab.internal", 22, "", true},
		{"ssh", "metadata.lab.internal", 22, "169.254.169.254", true},
		{"ssh", "localhost", 22, "", true},
		{"ssh", "localhost", 2222, "127.0.0.1", true},
		{"ssh", "lab.internal", 22, "", true},
		{"ssh", "evillab.internal", 22, "", true},
		{"ssh", "sub.build.example", 22, "", true},
		{"ssh", "inside.example", 22, "10.1.1.1", true},
		{"ssh", "mapped.example", 22, "10.2.2.2", true},
		{"ssh", "mixed.example", 22, "", true},
		{"ssh", "empty.example", 22, "", true},
		{"ssh", "unknown.example", 22, "", true},
	} {
		looked = nil
		dial, err := a.allow(context.Background(), tt.protocol, tt.host, tt.port)
		if tt.dial == "" {
			if !errors.Is(err, errNotAllowed) {
				t.Errorf("%s %s:%d: %v %v, want refused", tt.protocol, tt.host, tt.port, dial, err)
			}
		} else if err != nil || dial.String() != tt.dial {
			t.Errorf("%s %s:%d: %v %v, want %s", tt.protocol, tt.host, tt.port, dial, err, tt.dial)
		}
		if (len(looked) > 0) != tt.lookup {
			t.Errorf("%s %s:%d: looked up %v", tt.protocol, tt.host, tt.port, looked)
		}
	}

	// A list of names alone resolves only the names it lists: no other
	// name could pass.
	names, err := newAllowlist([]string{"build.example"})
	if err != nil {
		t.Fatal(err)
	}
	names.lookup = func(_ context.Context, host string) ([]netip.Addr, error) {
		if host != "build.example" {
			t.Errorf("a list without ranges looked %s up", host)
		}
		return []netip.Addr{netip.MustParseAddr("203.0.113.9")}, nil
	}
	if _, err := names.allow(context.Background(), "ssh", "other.example", 22); !errors.Is(err, errNotAllowed) {
		t.Errorf("other.example: %v", err)
	}
	if dial, err := names.allow(context.Background(), "ssh", "build.example", 22); err != nil || dial.String() != "203.0.113.9" {
		t.Errorf("build.example: %v %v", dial, err)
	}
}
