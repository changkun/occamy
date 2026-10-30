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
	"time"
)

// errNotAllowed reports an ad-hoc target that adhoc_allowlist does not
// cover.
var errNotAllowed = errors.New("the target is not in adhoc_allowlist")

// resolveTimeout bounds the name lookup of an ad-hoc target.
const resolveTimeout = 5 * time.Second

// allowlist is adhoc_allowlist: where ad-hoc targets may point.
//
// A target that is an IP address is allowed if a range or address of the
// list holds it. A host name is allowed if the list names it, or if it
// ends in the domain of a *.domain entry; occamyd then dials the name, so
// the list trusts the names' DNS. Any other host name is resolved, and
// allowed only if every address it resolves to lies in the list's ranges;
// occamyd then dials the address it checked rather than the name, so
// that a second lookup cannot lead elsewhere.
type allowlist struct {
	prefixes []netip.Prefix
	names    map[string]bool
	domains  []string // ".lab.internal" for *.lab.internal
	lookup   func(ctx context.Context, host string) ([]netip.Addr, error)
}

// newAllowlist reads entries that ParseV1 has checked.
func newAllowlist(entries []string) *allowlist {
	a := &allowlist{
		names: map[string]bool{},
		lookup: func(ctx context.Context, host string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		},
	}
	for _, e := range entries {
		if p, err := netip.ParsePrefix(e); err == nil {
			// Addresses are compared unmapped, so a range of IPv4-mapped
			// addresses becomes the IPv4 range.
			if addr := p.Addr(); addr.Is4In6() && p.Bits() >= 96 {
				p = netip.PrefixFrom(addr.Unmap(), p.Bits()-96)
			}
			a.prefixes = append(a.prefixes, p.Masked())
			continue
		}
		if ip, err := netip.ParseAddr(e); err == nil {
			ip = ip.Unmap()
			a.prefixes = append(a.prefixes, netip.PrefixFrom(ip, ip.BitLen()))
			continue
		}
		if d, ok := strings.CutPrefix(e, "*."); ok {
			a.domains = append(a.domains, "."+canonicalName(d))
			continue
		}
		a.names[canonicalName(e)] = true
	}
	return a
}

// canonicalName is a host name in lower case without a trailing dot.
func canonicalName(name string) string {
	return strings.ToLower(strings.TrimSuffix(name, "."))
}

// allow returns the host to dial for an ad-hoc target's host, or
// errNotAllowed.
func (a *allowlist) allow(ctx context.Context, host string) (string, error) {
	if ip, err := netip.ParseAddr(host); err == nil {
		if ip.Zone() != "" || !a.covers(ip) {
			return "", errNotAllowed
		}
		return ip.Unmap().String(), nil
	}
	name := canonicalName(host)
	if a.names[name] {
		return host, nil
	}
	for _, d := range a.domains {
		if strings.HasSuffix(name, d) {
			return host, nil
		}
	}
	if len(a.prefixes) == 0 {
		return "", errNotAllowed
	}
	ctx, cancel := context.WithTimeout(ctx, resolveTimeout)
	defer cancel()
	addrs, err := a.lookup(ctx, name)
	if err != nil || len(addrs) == 0 {
		return "", errNotAllowed
	}
	for _, ip := range addrs {
		if !a.covers(ip) {
			return "", errNotAllowed
		}
	}
	return addrs[0].Unmap().WithZone("").String(), nil
}

// covers reports whether a range or address of the list holds ip.
func (a *allowlist) covers(ip netip.Addr) bool {
	ip = ip.Unmap().WithZone("")
	for _, p := range a.prefixes {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}
