// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package netguard

import (
	"strings"
	"testing"
)

func validChange() Change {
	return Change{ProfileUUID: "11111111-1111-1111-1111-111111111111", OperationID: "0123456789abcdef0123456789abcdef", Interface: "ens4",
		IPv4: &IPSettings{Method: "manual", Addresses: []string{"192.0.2.20/24"}}, WindowSeconds: 60}
}

func TestChange_InterfaceNameHasTheAnswersShape(t *testing.T) {
	for _, name := range []string{"ens4", "eth0", "enp0s3", "nic1", "br-lan", "vlan.10", "wlan_1", "A1"} {
		c := validChange()
		c.Interface = name
		if err := c.Validate(); err != nil {
			t.Fatalf("%q refused: %v", name, err)
		}
	}
	for _, name := range []string{"", "lo", ".eth0", "-eth0", "_eth0", "eth0%1", "eth 0", "eth0:1", "eth/0", "ethé", "abcdefghijklmnop", "eth0\x00", "eth0\n"} {
		c := validChange()
		c.Interface = name
		if err := c.Validate(); err == nil {
			t.Fatalf("interface %q accepted", name)
		}
	}
}

func TestChange_SearchSuffixHasTheAnswersShape(t *testing.T) {
	for _, suffix := range []string{"example.test", "a.example", "sub-domain.example", "EXAMPLE.test", "x1"} {
		c := validChange()
		c.IPv4.Search = []string{suffix}
		if err := c.Validate(); err != nil {
			t.Fatalf("%q refused: %v", suffix, err)
		}
	}
	for _, suffix := range []string{"", ".", "..", "a..b", ".example", "example.", "-a.example", "a-.example", "a_b.example", "exa mple", "é.example",
		"192.0.2.1", "2001:db8::1", "1.2.3", "localhost", "x.localhost", strings.Repeat("a", 64) + ".example", strings.Repeat("a.", 127) + "a"} {
		c := validChange()
		c.IPv4.Search = []string{suffix}
		if err := c.Validate(); err == nil {
			t.Fatalf("search suffix %q accepted", suffix)
		}
	}
}
