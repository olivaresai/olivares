// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package base

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/olivaresai/olivares/appliance/answers"
)

// NetworkProfile is an active NetworkManager profile observed through D-Bus.
type NetworkProfile struct {
	Interface, Filename, IPv4Method, IPv6Method string
	Managed                                     bool
	IPv4, IPv6                                  []string
	Gateway4, Gateway6                          string
	DNSServers, DNSSearch                       []string
}

// NetworkReader obtains current managed-device and active-profile facts, never intent.
type NetworkReader func(context.Context) ([]NetworkProfile, error)

func (s CloudInitHost) verifyNetwork(ctx context.Context, in Input) error {
	if err := secondNetworkOwner(s.Host); err != nil {
		return err
	}
	if in.Answers.Network.Mode == "static" {
		data, err := os.ReadFile(s.Host.path("/var/lib/cloud/instance/network-config.json"))
		var source map[string]json.RawMessage
		if err != nil || len(data) > 65536 || json.Unmarshal(data, &source) != nil || len(source) == 0 {
			return Refuse("static_network_source_missing")
		}
	}
	reader := s.Network
	if reader == nil {
		reader = ReadNetworkManager
	}
	profiles, err := reader(ctx)
	if err != nil || len(profiles) == 0 {
		return Refuse("network_owner_unmeasured")
	}
	rendered := false
	for _, p := range profiles {
		if p.Interface == "lo" {
			continue
		}
		if !p.Managed || !persistentKeyfile(p.Filename) {
			return Refuse("network_renderer_not_networkmanager")
		}
		if strings.HasPrefix(filepath.Base(p.Filename), "cloud-init-") {
			rendered = true
		}
	}
	if !rendered {
		return Refuse("network_renderer_not_networkmanager")
	}
	for _, want := range in.Answers.Network.Interfaces {
		found := false
		for _, got := range profiles {
			if got.Interface != want.Name {
				continue
			}
			found = true
			if !familyMatches(want.IPv4, got.IPv4Method, got.IPv4, got.Gateway4) || !familyMatches(want.IPv6, got.IPv6Method, got.IPv6, got.Gateway6) {
				return Refuse("static_network_profile_mismatch")
			}
			if dns := in.Answers.Network.DNS; dns != nil {
				if dns.Servers != nil && !sameServerOrder(dns.Servers, got.DNSServers) || dns.Search != nil && !sameSearchOrder(dns.Search, got.DNSSearch) {
					return Refuse("static_network_dns_mismatch")
				}
			}
		}
		if !found {
			return Refuse("static_network_profile_unmanaged")
		}
	}
	return nil
}

func persistentKeyfile(name string) bool {
	return filepath.Clean(name) == name && filepath.Dir(name) == "/etc/NetworkManager/system-connections" && strings.HasSuffix(name, ".nmconnection")
}
func familyMatches(want *answers.AddressFamily, method string, addresses []string, gateway string) bool {
	if want == nil {
		return true
	}
	if method != "manual" || !sameStrings(want.Addresses, addresses) {
		return false
	}
	return want.Gateway == nil && gateway == "" || want.Gateway != nil && *want.Gateway == gateway
}

// sameServerOrder compares DNS server precedence within each address family, the order
// NetworkManager keeps; a profile cannot order servers across families.
func sameServerOrder(want, got []string) bool {
	for _, v4 := range []bool{true, false} {
		w, wok := serversOf(want, v4)
		g, gok := serversOf(got, v4)
		if !wok || !gok || !slices.Equal(w, g) {
			return false
		}
	}
	return true
}
func serversOf(servers []string, v4 bool) ([]string, bool) {
	var family []string
	for _, s := range servers {
		a, err := netip.ParseAddr(s)
		if err != nil {
			return nil, false
		}
		if a = a.Unmap(); a.Is4() == v4 {
			family = append(family, a.String())
		}
	}
	return family, true
}

// sameSearchOrder compares search suffixes in precedence order; names compare without case.
func sameSearchOrder(want, got []string) bool {
	return slices.EqualFunc(want, got, strings.EqualFold)
}
func sameStrings(a, b []string) bool {
	a = slices.Clone(a)
	b = slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}

func secondNetworkOwner(h Host) error {
	patterns := []string{"/etc/network/interfaces", "/etc/network/interfaces.d/*", "/etc/sysconfig/network-scripts/ifcfg-*", "/etc/systemd/network/*.network", "/run/systemd/network/*.network", "/usr/lib/systemd/network/*.network", "/etc/netplan/*.yaml", "/etc/netplan/*.yml"}
	for _, pattern := range patterns {
		paths, err := filepath.Glob(h.path(pattern))
		if err != nil {
			return Refuse("network_owner_unmeasured")
		}
		for _, name := range paths {
			data, err := os.ReadFile(name)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil || len(data) > 65536 {
				return Refuse("network_owner_unmeasured")
			}
			content := string(data)
			switch {
			case strings.Contains(pattern, "/network/interfaces"):
				for _, line := range strings.Split(content, "\n") {
					fields := strings.Fields(strings.SplitN(line, "#", 2)[0])
					if len(fields) >= 2 && fields[0] == "iface" && fields[1] != "lo" {
						return Refuse("second_network_owner")
					}
				}
			case strings.Contains(pattern, "ifcfg-"):
				if filepath.Base(name) != "ifcfg-lo" {
					return Refuse("second_network_owner")
				}
			case strings.HasSuffix(pattern, ".network"):
				// A file without an exact loopback-only match can configure non-loopback devices.
				if !strings.Contains(content, "Name=lo\n") || strings.Contains(content, "Name=lo ") {
					return Refuse("second_network_owner")
				}
			case strings.Contains(pattern, "netplan"):
				if strings.Contains(content, "networkd") {
					return Refuse("second_network_owner")
				}
			}
		}
	}
	return nil
}
