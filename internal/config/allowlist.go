// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package config

import (
	"fmt"
	"net/netip"
	"strconv"
	"strings"
)

// AllowEntry is an entry of adhoc_allowlist: a CIDR range, an IP address,
// a host name, or *. and a domain, each with the ports it allows.
type AllowEntry struct {
	// Prefix is the entry's range, or its address as a range of one
	// address. IPv4-mapped addresses are IPv4. It is the zero Prefix for
	// a name.
	Prefix netip.Prefix
	// Name is a host name, and Domain the domain of a *.domain entry,
	// each in lower case without a trailing dot. At most one is set.
	Name, Domain string
	// Ports are the ports the entry allows. The zero PortRange allows
	// the protocol's AdhocPorts.
	Ports PortRange
}

// PortRange holds the ports from First to Last.
type PortRange struct{ First, Last uint16 }

// Contains reports whether r holds port.
func (r PortRange) Contains(port uint16) bool { return r.First <= port && port <= r.Last }

// AdhocPorts are the ports that an adhoc_allowlist entry without ports
// allows, by protocol: each protocol's default port, and for vnc the
// ports of displays 0 to 99.
var AdhocPorts = map[string]PortRange{
	"ssh":    {22, 22},
	"vnc":    {5900, 5999},
	"rdp":    {3389, 3389},
	"telnet": {23, 23},
}

// AllowsPort reports whether e allows port for protocol.
func (e AllowEntry) AllowsPort(protocol string, port uint16) bool {
	r := e.Ports
	if r == (PortRange{}) {
		r = AdhocPorts[protocol]
	}
	return r != (PortRange{}) && r.Contains(port)
}

// ParseAllowEntry parses an entry of adhoc_allowlist: 10.0.0.0/8,
// 192.0.2.7, fd00::/8, build.example or *.lab.internal, any of them with
// :port or :first-last after it, such as 10.0.0.0/8:22 or
// *.lab.internal:5900-5910. An IPv6 range or address with ports goes in
// brackets: [fd00::/8]:22.
func ParseAllowEntry(s string) (AllowEntry, error) {
	var e AllowEntry
	host, ports, err := splitPorts(s)
	if err != nil {
		return e, err
	}
	if ports != "" {
		if e.Ports, err = parsePorts(ports); err != nil {
			return e, fmt.Errorf("%q: %w", s, err)
		}
	}
	notEntry := fmt.Errorf("%q is not a CIDR range, an IP address, a host name or *.domain, with an optional :port or :first-last", s)
	if p, err := netip.ParsePrefix(host); err == nil {
		// Addresses are compared unmapped, so a range of IPv4-mapped
		// addresses becomes the IPv4 range.
		if addr := p.Addr(); addr.Is4In6() && p.Bits() >= 96 {
			p = netip.PrefixFrom(addr.Unmap(), p.Bits()-96)
		}
		e.Prefix = p.Masked()
		return e, nil
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		if ip.Zone() != "" {
			return e, notEntry
		}
		ip = ip.Unmap()
		e.Prefix = netip.PrefixFrom(ip, ip.BitLen())
		return e, nil
	}
	if d, ok := strings.CutPrefix(host, "*."); ok {
		if _, err := netip.ParseAddr(d); err == nil || !validHost(d) {
			return e, notEntry
		}
		e.Domain = strings.ToLower(strings.TrimSuffix(d, "."))
		return e, nil
	}
	if !validHost(host) {
		return e, notEntry
	}
	e.Name = strings.ToLower(strings.TrimSuffix(host, "."))
	return e, nil
}

// splitPorts splits an allowlist entry into its host part and its
// ports, which are empty if it has none.
func splitPorts(s string) (host, ports string, err error) {
	switch {
	case strings.HasPrefix(s, "["):
		end := strings.Index(s, "]")
		if end < 0 || !strings.Contains(s[1:end], ":") {
			return "", "", fmt.Errorf("%q: only an IPv6 range or address goes in brackets", s)
		}
		host, rest := s[1:end], s[end+1:]
		if rest == "" {
			return host, "", nil
		}
		ports, ok := strings.CutPrefix(rest, ":")
		if !ok || ports == "" {
			return "", "", fmt.Errorf("%q: brackets are followed by :port or :first-last", s)
		}
		return host, ports, nil
	case strings.Count(s, ":") == 1:
		host, ports, _ := strings.Cut(s, ":")
		if ports == "" {
			return "", "", fmt.Errorf("%q: the colon is followed by no port", s)
		}
		return host, ports, nil
	}
	return s, "", nil // no port, or an IPv6 range or address without one
}

// parsePorts parses a port or first-last.
func parsePorts(s string) (PortRange, error) {
	first, last, isRange := strings.Cut(s, "-")
	if !isRange {
		last = first
	}
	a, errA := strconv.ParseUint(first, 10, 16)
	b, errB := strconv.ParseUint(last, 10, 16)
	if errA != nil || errB != nil || a == 0 || a > b {
		return PortRange{}, fmt.Errorf("%q is not a port or first-last, from 1 to 65535", s)
	}
	return PortRange{uint16(a), uint16(b)}, nil
}
