// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"changkun.de/x/occamy/internal/config"
)

// errNotAllowed reports an ad-hoc target that adhoc_allowlist does not
// cover.
var errNotAllowed = errors.New("the target is not in adhoc_allowlist")

// resolveTimeout bounds the name lookup of an ad-hoc target.
const resolveTimeout = 5 * time.Second

// allowlist is adhoc_allowlist: where ad-hoc targets may point, and on
// which ports. An entry without ports allows the protocol's
// config.AdhocPorts.
//
// A target that is an IP address is allowed if an entry's range or
// address holds it. A host name is resolved, and allowed if the list
// names it, or it ends in the domain of a *.domain entry, or else if every
// address it resolves to lies in the list's ranges. occamyd then dials
// the first address it checked rather than the name, so that a second
// lookup cannot lead elsewhere.
//
// Loopback, link-local (such as a cloud's metadata service at
// 169.254.169.254), unspecified and multicast addresses, the other
// clouds' metadata services, and the addresses and ports that occamyd
// itself listens on or talks to guacd at are refused whatever range or
// name leads to them, unless an entry is that very address.
type allowlist struct {
	entries  []config.AllowEntry
	ranges   bool                    // whether any entry is a range or an address
	reserved map[netip.AddrPort]bool // occamyd's own endpoints, and guacd's
	lookup   func(ctx context.Context, host string) ([]netip.Addr, error)
}

// newAllowlist reads entries, which ParseV1 has checked.
func newAllowlist(entries []string) (*allowlist, error) {
	a := &allowlist{
		reserved: map[netip.AddrPort]bool{},
		lookup: func(ctx context.Context, host string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		},
	}
	for _, s := range entries {
		e, err := config.ParseAllowEntry(s)
		if err != nil {
			return nil, err
		}
		a.entries = append(a.entries, e)
		a.ranges = a.ranges || e.Prefix.IsValid()
	}
	return a, nil
}

// interfaceAddrs lists the host's addresses, a variable for tests.
var interfaceAddrs = net.InterfaceAddrs

// reserve makes each of addresses, a host and port such as guacd's or
// occamyd's own listener, special at its port: a range that holds it
// opens it only through an entry that is that very address. A name is
// resolved now, and a listener on no host stands for every address of
// the host. An address that does not resolve is returned in the error,
// and the others are still reserved.
func (a *allowlist) reserve(ctx context.Context, addresses ...string) error {
	var errs []error
	for _, s := range addresses {
		if s == "" {
			continue
		}
		host, p, err := net.SplitHostPort(s)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		port, err := strconv.ParseUint(p, 10, 16)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", s, err))
			continue
		}
		var ips []netip.Addr
		if ip, err := netip.ParseAddr(host); err == nil && !ip.IsUnspecified() {
			ips = []netip.Addr{ip}
		} else if host == "" || err == nil {
			addrs, err := interfaceAddrs()
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", s, err))
			}
			for _, x := range addrs {
				if pfx, err := netip.ParsePrefix(x.String()); err == nil {
					ips = append(ips, pfx.Addr())
				}
			}
		} else {
			rctx, cancel := context.WithTimeout(ctx, resolveTimeout)
			ips, err = a.lookup(rctx, host)
			cancel()
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", s, err))
			}
		}
		for _, ip := range ips {
			a.reserved[netip.AddrPortFrom(ip.Unmap().WithZone(""), uint16(port))] = true
		}
	}
	return errors.Join(errs...)
}

// canonicalName is a host name in lower case without a trailing dot.
func canonicalName(name string) string {
	return strings.ToLower(strings.TrimSuffix(name, "."))
}

// allow returns the address to dial for an ad-hoc target of protocol at
// host and port, or errNotAllowed.
func (a *allowlist) allow(ctx context.Context, protocol, host string, port uint16) (netip.Addr, error) {
	if ip, err := netip.ParseAddr(host); err == nil {
		if ip.Zone() != "" || !a.covers(protocol, ip.Unmap(), port) {
			return netip.Addr{}, errNotAllowed
		}
		return ip.Unmap(), nil
	}
	name := canonicalName(host)
	listed := a.names(protocol, name, port)
	if !listed && !a.ranges {
		return netip.Addr{}, errNotAllowed // no address could pass
	}
	ctx, cancel := context.WithTimeout(ctx, resolveTimeout)
	defer cancel()
	addrs, err := a.lookup(ctx, name)
	if err != nil || len(addrs) == 0 {
		return netip.Addr{}, errNotAllowed
	}
	for i, ip := range addrs {
		ip = ip.Unmap().WithZone("")
		addrs[i] = ip
		if listed && !a.special(ip, port) || a.covers(protocol, ip, port) {
			continue
		}
		return netip.Addr{}, errNotAllowed
	}
	return addrs[0], nil
}

// names reports whether an entry names host, or its domain, with port.
func (a *allowlist) names(protocol, name string, port uint16) bool {
	for _, e := range a.entries {
		if (e.Name != "" && e.Name == name || e.Domain != "" && strings.HasSuffix(name, "."+e.Domain)) && e.AllowsPort(protocol, port) {
			return true
		}
	}
	return false
}

// covers reports whether an entry's range or address holds ip, with
// port. A special address must be the entry's very address.
func (a *allowlist) covers(protocol string, ip netip.Addr, port uint16) bool {
	sp := a.special(ip, port)
	for _, e := range a.entries {
		if e.Prefix.Contains(ip) && (!sp || e.Prefix.IsSingleIP()) && e.AllowsPort(protocol, port) {
			return true
		}
	}
	return false
}

// special reports whether ip at port is special: special(ip), or one of
// the endpoints that reserve took.
func (a *allowlist) special(ip netip.Addr, port uint16) bool {
	return special(ip) || a.reserved[netip.AddrPortFrom(ip, port)]
}

// metadata are the clouds' metadata services outside the link-local
// range: AWS's over IPv6, in the unique local range, and Alibaba Cloud's,
// in the shared address space that carrier-grade NAT uses. Their
// credentials are the host's, so a range must not open them by accident.
var metadata = map[netip.Addr]bool{
	netip.MustParseAddr("fd00:ec2::254"):   true,
	netip.MustParseAddr("100.100.100.200"): true,
}

// special reports whether ip is loopback, link-local (such as the
// metadata service at 169.254.169.254), unspecified, multicast or a
// metadata service: addresses of the host occamyd runs on, of its link,
// of no one host, or of the host's cloud, which a range should not open
// by accident.
func special(ip netip.Addr) bool {
	return ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() || ip.IsMulticast() || metadata[ip]
}
