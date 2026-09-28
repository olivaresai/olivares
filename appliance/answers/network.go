// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package answers

import (
	"net/netip"
	"sort"
	"strconv"
	"strings"
)

// Network describes host settings cloud-init must have rendered. Build validates it;
// constructing this value directly does not establish validation or authorize an effect.
type Network struct {
	Mode       string             `json:"mode"`
	Interfaces []NetworkInterface `json:"interfaces,omitempty"`
	DNS        *DNS               `json:"dns,omitempty"`
}

// NetworkInterface names static address families on one existing interface.
type NetworkInterface struct {
	Name string         `json:"name"`
	IPv4 *AddressFamily `json:"ipv4,omitempty"`
	IPv6 *AddressFamily `json:"ipv6,omitempty"`
}

// AddressFamily carries addresses with prefix lengths and an optional on-link gateway.
type AddressFamily struct {
	Addresses []string `json:"addresses"`
	Gateway   *string  `json:"gateway,omitempty"`
}

// DNS carries optional literal server addresses and DNS search suffixes.
type DNS struct {
	Servers []string `json:"servers,omitempty"`
	Search  []string `json:"search,omitempty"`
}

func validateNetwork(n *Network) error {
	const root = "$.host.network"
	switch n.Mode {
	case "dhcp":
		if n.Interfaces != nil {
			return invalid(root+".interfaces", "static interfaces conflict with dhcp mode")
		}
	case "static":
		if len(n.Interfaces) == 0 {
			return invalid(root+".mode", "static mode requires interfaces")
		}
	default:
		return invalid(root+".mode", "expected dhcp or static")
	}
	names := map[string]bool{}
	addresses := map[netip.Addr]bool{}
	for i := range n.Interfaces {
		iface := &n.Interfaces[i]
		p := root + ".interfaces[" + strconv.Itoa(i) + "]"
		if !interfaceName(iface.Name) || iface.Name == "lo" || names[iface.Name] {
			return invalid(p+".name", "expected a distinct non-loopback interface name")
		}
		names[iface.Name] = true
		if iface.IPv4 == nil && iface.IPv6 == nil {
			return invalid(p, "at least one address family is required")
		}
		for _, family := range []struct {
			value *AddressFamily
			ipv4  bool
			path  string
		}{{iface.IPv4, true, p + ".ipv4"}, {iface.IPv6, false, p + ".ipv6"}} {
			if family.value == nil {
				continue
			}
			if err := validateFamily(family.value, family.ipv4, family.path, addresses); err != nil {
				return err
			}
		}
	}
	sort.Slice(n.Interfaces, func(i, j int) bool { return n.Interfaces[i].Name < n.Interfaces[j].Name })
	if n.DNS != nil {
		return validateDNS(n.DNS, root+".dns")
	}
	return nil
}

func validateFamily(f *AddressFamily, ipv4 bool, p string, seen map[netip.Addr]bool) error {
	if len(f.Addresses) == 0 {
		return invalid(p+".addresses", "at least one address is required")
	}
	var prefixes []netip.Prefix
	for i, value := range f.Addresses {
		prefix, err := netip.ParsePrefix(value)
		if err != nil || prefix.Bits() == 0 || !usableAddress(prefix.Addr()) || prefix.Addr().Is4() != ipv4 || seen[prefix.Addr()] {
			return invalid(p+".addresses["+strconv.Itoa(i)+"]", "expected a distinct unicast address and nonzero prefix of this family")
		}
		seen[prefix.Addr()] = true
		prefixes = append(prefixes, prefix)
		f.Addresses[i] = prefix.String()
	}
	sort.Strings(f.Addresses)
	if f.Gateway == nil {
		return nil
	}
	gateway, err := netip.ParseAddr(*f.Gateway)
	if err != nil || !usableAddress(gateway) || gateway.Is4() != ipv4 {
		return invalid(p+".gateway", "expected a unicast gateway of this family")
	}
	onLink := false
	for _, prefix := range prefixes {
		if gateway == prefix.Addr() {
			return invalid(p+".gateway", "gateway conflicts with an interface address")
		}
		onLink = onLink || prefix.Contains(gateway)
	}
	if !onLink {
		return invalid(p+".gateway", "gateway must be within an interface prefix")
	}
	normal := gateway.String()
	f.Gateway = &normal
	return nil
}

func usableAddress(a netip.Addr) bool {
	return a.IsValid() && a.IsGlobalUnicast() && !a.IsLoopback() && !a.Is4In6() && a.Zone() == ""
}

func validateDNS(d *DNS, p string) error {
	if d.Servers == nil && d.Search == nil {
		return invalid(p, "at least one DNS field is required")
	}
	for i, value := range d.Servers {
		a, err := netip.ParseAddr(value)
		if err != nil || !usableAddress(a) {
			return invalid(p+".servers["+strconv.Itoa(i)+"]", "expected a literal unicast DNS address")
		}
		d.Servers[i] = a.String()
	}
	if hasDNSDuplicate(d.Servers) {
		return invalid(p+".servers", "duplicate DNS server")
	}
	for i, value := range d.Search {
		normal, ok := endpointHost(value)
		if !ok || strings.ContainsAny(normal, ":/") {
			return invalid(p+".search["+strconv.Itoa(i)+"]", "expected a DNS search suffix")
		}
		if _, err := netip.ParseAddr(normal); err == nil {
			return invalid(p+".search["+strconv.Itoa(i)+"]", "expected a DNS search suffix")
		}
		d.Search[i] = normal
	}
	if hasDNSDuplicate(d.Search) {
		return invalid(p+".search", "duplicate DNS search suffix")
	}
	return nil
}

func hasDNSDuplicate(values []string) bool {
	seen := map[string]bool{}
	for _, v := range values {
		if seen[v] {
			return true
		}
		seen[v] = true
	}
	return false
}
