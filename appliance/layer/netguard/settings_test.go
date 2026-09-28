// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package netguard

import (
	"github.com/godbus/dbus/v5"
	"testing"
)

func TestDns_SearchDomainsReadFromTheAppliedConnection(t *testing.T) {
	p, err := readIP(map[string]dbus.Variant{"method": dbus.MakeVariant("auto"), "dns-search": dbus.MakeVariant([]string{"z.test", "a.test"})})
	if err != nil || len(p.Search) != 2 || p.Search[0] != "z.test" {
		t.Fatal(p, err)
	}
	reversed := p
	reversed.Search = []string{"a.test", "z.test"}
	if sameIP(p, reversed) {
		t.Fatal("search order ignored")
	}
}
func TestDns_IsWrittenThroughTheOwnerAndNeverToResolvConf(t *testing.T) {
	settings := settingsMap{"ipv4": {"method": dbus.MakeVariant("manual"), "dns": dbus.MakeVariant([]uint32{1})}}
	p := IPSettings{Method: "manual", Addresses: []string{"192.0.2.10/24"}, DNS: []string{"192.0.2.54", "192.0.2.53"}, Search: []string{"z.test", "a.test"}}
	replaceIP(settings, "ipv4", p)
	got, err := readIP(settings["ipv4"])
	if err != nil || !sameIP(p, got) {
		t.Fatal(got, err)
	}
	if _, ok := settings["ipv4"]["dns"]; ok {
		t.Fatal("stale legacy DNS retained")
	}
}
func TestRollback_DynamicLeaseChangeIsNotComparedWithTheOldAddress(t *testing.T) {
	want := IPSettings{Method: "auto", NeverDefault: true}
	for _, addr := range []string{"192.0.2.10/24", "192.0.2.90/24"} {
		if !runtimeMatches(want, runtimeIP{Addresses: []string{addr}, Routes: []Route{{Destination: "192.0.2.0/24"}}}) {
			t.Fatal("lease treated as static", addr)
		}
	}
	if runtimeMatches(want, runtimeIP{}) {
		t.Fatal("missing lease accepted")
	}
	if runtimeMatches(want, runtimeIP{Addresses: []string{"192.0.2.10/24"}}) {
		t.Fatal("lease without a usable route accepted")
	}
	if runtimeMatches(IPSettings{Method: "dhcp"}, runtimeIP{Addresses: []string{"192.0.2.10/24"}, Routes: []Route{{Destination: "192.0.2.0/24"}}}) {
		t.Fatal("lease without its default route accepted")
	}
}
func TestRollback_RuntimeShadowNeverCountsAsPersistent(t *testing.T) {
	p := Profile{Filename: "/run/NetworkManager/system-connections/a.nmconnection"}
	if p.Persistent() {
		t.Fatal("runtime profile called persistent")
	}
	p.Filename = "/etc/NetworkManager/system-connections/a.nmconnection"
	p.Flags = 1
	if p.Persistent() {
		t.Fatal("unsaved profile called persistent")
	}
	p.Flags = 0
	if !p.Persistent() {
		t.Fatal("persistent profile refused")
	}
}
