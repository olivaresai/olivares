// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package base

import (
	"strings"
	"testing"
)

// onCycle reports whether unit can reach itself through the ordering graph.
func onCycle(before map[string]map[string]bool, unit string) bool {
	seen := map[string]bool{}
	stack := []string{}
	for next := range before[unit] {
		stack = append(stack, next)
	}
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if n == unit {
			return true
		}
		if seen[n] {
			continue
		}
		seen[n] = true
		for next := range before[n] {
			stack = append(stack, next)
		}
	}
	return false
}

// firstBootBeforeDefaultDependencies is the first-boot unit's ordering as it shipped before it
// set DefaultDependencies=no.
const firstBootBeforeDefaultDependencies = `[Unit]
Wants=network-online.target olivares-appliance-readiness.service
After=cloud-final.service network-online.target
Before=olivares.service olivares-appliance-readiness.service
[Install]
WantedBy=multi-user.target
`

// Under the default dependency exactly as systemd.target(5) states it, first boot is on no
// cycle as shipped, and each way of reintroducing the cycle puts it on one.
func TestUnits_TheOrderingModelAsSystemdTargetStatesItCatchesEachReintroducedCycle(t *testing.T) {
	shipped, _ := readUnit(t, firstBootUnitPath)
	readiness, _ := readUnit(t, readinessUnitPath)
	product, _ := readUnit(t, productUnitPath)
	edited := func(key string, values ...string) unitFile {
		u := unitFile{}
		for k, v := range shipped {
			u[k] = append([]string(nil), v...)
		}
		if values == nil {
			delete(u, key)
		} else {
			u[key] = append(u[key], values...)
		}
		return u
	}
	cases := []struct {
		name    string
		unit    unitFile
		onCycle bool
	}{
		{"as shipped", shipped, false},
		{"without DefaultDependencies=no", edited("DefaultDependencies"), true},
		{"with Before=olivares.service restored", edited("Before", "olivares.service"), true},
		{"as it shipped before DefaultDependencies=no", parseUnit(firstBootBeforeDefaultDependencies), true},
	}
	for _, tc := range cases {
		units := map[string]unitFile{
			"olivares-appliance-firstboot.service": tc.unit,
			"olivares-appliance-readiness.service": readiness,
			"olivares.service":                     product,
			"multi-user.target":                    {},
		}
		for name, u := range debianCloudInit {
			units[name] = u
		}
		if got := onCycle(orderingGraph(units), "olivares-appliance-firstboot.service"); got != tc.onCycle {
			t.Fatalf("%s: first boot on an ordering cycle: %v, want %v (%s)", tc.name, got, tc.onCycle,
				strings.Join(tc.unit["After"], " "))
		}
	}
}
