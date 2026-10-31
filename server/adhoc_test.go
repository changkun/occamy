// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package server

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strings"
	"testing"
)

// TestAllowlistReserved checks that guacd's address and occamyd's own
// listeners stay out of a range's reach, at their ports, unless an entry
// is that very address, and that a listener on no host stands for every
// address of the host.
func TestAllowlistReserved(t *testing.T) {
	a, err := newAllowlist([]string{"10.0.0.0/8:1-65535", "[fd00::/8]:1-65535", "10.9.9.10:4822", "*.lab.internal:1-65535"})
	if err != nil {
		t.Fatal(err)
	}
	a.lookup = func(_ context.Context, host string) ([]netip.Addr, error) {
		switch host {
		case "guacd", "guacd.lab.internal":
			return []netip.Addr{netip.MustParseAddr("10.9.9.9")}, nil
		}
		return nil, errors.New("no such host")
	}
	interfaceAddrs = func() ([]net.Addr, error) {
		return []net.Addr{
			&net.IPNet{IP: net.ParseIP("10.7.7.7"), Mask: net.CIDRMask(8, 32)},
			&net.IPNet{IP: net.ParseIP("fd00::7"), Mask: net.CIDRMask(64, 128)},
		}, nil
	}
	t.Cleanup(func() { interfaceAddrs = net.InterfaceAddrs })
	err = a.reserve(context.Background(), "guacd:4822", "10.9.9.10:4822", ":8443", "[fd00::5]:6060", "nowhere:1")
	if err == nil || !strings.Contains(err.Error(), "nowhere") {
		t.Errorf("reserve: %v, want the name that did not resolve", err)
	}
	for _, tt := range []struct {
		protocol, host string
		port           uint16
		ok             bool
	}{
		{"rdp", "10.9.9.9", 4822, false},           // guacd, by its name
		{"rdp", "10.9.9.9", 3389, true},            // another port of its host
		{"rdp", "guacd.lab.internal", 4822, false}, // a listed name that leads to it
		{"rdp", "10.9.9.10", 4822, true},           // an entry that is the address
		{"ssh", "10.7.7.7", 8443, false},           // the listener on every address
		{"ssh", "fd00::7", 8443, false},
		{"ssh", "10.7.7.7", 22, true},
		{"ssh", "fd00::5", 6060, false}, // the debug listener
		{"ssh", "fd00::5", 22, true},
	} {
		if _, err := a.allow(context.Background(), tt.protocol, tt.host, tt.port); (err == nil) != tt.ok {
			t.Errorf("%s %s:%d: %v, want allowed %v", tt.protocol, tt.host, tt.port, err, tt.ok)
		}
	}
}

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
		// evillab.internal resolves, so that only the dot before a
		// *.domain entry's domain keeps it out.
		case "host.lab.internal", "a.b.lab.internal", "build.example", "ports.example", "evillab.internal", "xlab.internal":
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
		{"ssh", "xlab.internal.", 22, "", true},
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

	// The clouds' metadata services outside the link-local range are
	// special too.
	{
		a, err := newAllowlist([]string{"fd00::/8", "100.64.0.0/10", "100.100.100.200:80"})
		if err != nil {
			t.Fatal(err)
		}
		for _, tt := range []struct {
			host string
			port uint16
			ok   bool
		}{
			{"fd00:ec2::254", 22, false},
			{"fd00:ec2::253", 22, true},
			{"100.100.100.200", 22, false},
			{"100.100.100.200", 80, true}, // an entry that is the address
			{"100.64.0.1", 22, true},
		} {
			if _, err := a.allow(context.Background(), "ssh", tt.host, tt.port); (err == nil) != tt.ok {
				t.Errorf("%s:%d: %v, want allowed %v", tt.host, tt.port, err, tt.ok)
			}
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
