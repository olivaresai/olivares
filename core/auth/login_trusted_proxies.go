// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"fmt"
	"net/netip"
	"strings"
)

// TrustedLoginProxies is an immutable parsed set of proxy networks. Its zero value
// trusts no forwarded address. It governs the login throttle, never peer authority.
type TrustedLoginProxies struct {
	prefixes []netip.Prefix
}

// ParseTrustedLoginProxies validates a complete comma-separated CIDR list before
// returning a set. Empty input trusts none; empty list entries are errors.
func ParseTrustedLoginProxies(raw string) (TrustedLoginProxies, error) {
	var trust TrustedLoginProxies
	if strings.TrimSpace(raw) == "" {
		return trust, nil
	}
	seen := make(map[netip.Prefix]bool)
	for i, item := range strings.Split(raw, ",") {
		prefix, err := netip.ParsePrefix(strings.TrimSpace(item))
		if err != nil {
			return TrustedLoginProxies{}, fmt.Errorf("auth: trusted login proxies: invalid CIDR at entry %d", i+1)
		}
		if prefix.Addr().Is4In6() && prefix.Bits() >= 96 {
			prefix = netip.PrefixFrom(prefix.Addr().Unmap(), prefix.Bits()-96)
		}
		prefix = prefix.Masked()
		if !seen[prefix] {
			trust.prefixes = append(trust.prefixes, prefix)
			seen[prefix] = true
		}
	}
	return trust, nil
}

// SetTrustedLoginProxies installs a parsed set before serving. As with the other
// login configuration, callers must not replace it while requests are running.
func (a *Authenticator) SetTrustedLoginProxies(trust TrustedLoginProxies) {
	a.trustedLoginProxies = trust
}

const (
	maxLoginForwardedBytes   = 8192
	maxLoginForwardedEntries = 64
)

func (p TrustedLoginProxies) contains(addr netip.Addr) bool {
	for _, prefix := range p.prefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

// clientAddress reads only the suffix attested by trusted proxies. Values are in
// received header order. Validate their combined bounds before joining or walking.
func (p TrustedLoginProxies) clientAddress(peer string, values []string) string {
	if len(p.prefixes) == 0 || len(values) == 0 || len(values) > maxLoginForwardedEntries {
		return peer
	}
	peerAddr, ok := loginProxyAddress(peer)
	if !ok || !p.contains(peerAddr) {
		return peer
	}
	bytes, entries := len(values)-1, len(values)
	for _, value := range values {
		if len(value) > maxLoginForwardedBytes-bytes {
			return peer
		}
		bytes += len(value)
		entries += strings.Count(value, ",")
		if entries > maxLoginForwardedEntries {
			return peer
		}
	}
	remaining := strings.Join(values, ",")
	for {
		separator := strings.LastIndexByte(remaining, ',')
		addr, ok := loginProxyAddress(strings.TrimSpace(remaining[separator+1:]))
		if !ok {
			return peer
		}
		if !p.contains(addr) {
			return addr.String()
		}
		if separator < 0 {
			return peer
		}
		remaining = remaining[:separator]
	}
}

func loginProxyAddress(value string) (netip.Addr, bool) {
	addr, err := netip.ParseAddr(value)
	if err != nil {
		if strings.HasPrefix(value, "[") && strings.HasSuffix(value, "]") {
			value += ":0" // ParseAddrPort validates the complete bracketed IPv6 literal.
		}
		withPort, portErr := netip.ParseAddrPort(value)
		if portErr != nil {
			return netip.Addr{}, false
		}
		addr = withPort.Addr()
	}
	if addr.Zone() != "" {
		return netip.Addr{}, false
	}
	return addr.Unmap(), true
}
