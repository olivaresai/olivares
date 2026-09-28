// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package netguard

import (
	"context"
	"testing"
	"time"
)

func freshReach() Reachability {
	return Reachability{Interface: "ens4", Source: "192.0.2.10", Target: "192.0.2.53", BootID: "boot-a", At: 5 * time.Second}
}

func TestDynamicOracle_AddressWithoutUsableRouteIsRefused(t *testing.T) {
	reach := []Reachability{freshReach()}
	connectedOnly := RuntimeFamily{Addresses: []string{"192.0.2.10/24"}, Routes: []Route{{Destination: "192.0.2.0/24"}}}
	if dynamicUsable("ens4", IPSettings{Method: "auto"}, true, connectedOnly, reach, "boot-a", time.Second) {
		t.Fatal("an address without the default route was accepted")
	}
	planned := IPSettings{Method: "dhcp", NeverDefault: true, Routes: []Route{{Destination: "198.51.100.0/24", NextHop: "192.0.2.1"}}}
	if dynamicUsable("ens4", planned, true, connectedOnly, reach, "boot-a", time.Second) {
		t.Fatal("an address without the planned route was accepted")
	}
	unrouted := RuntimeFamily{Addresses: []string{"192.0.2.10/24"}}
	if dynamicUsable("ens4", IPSettings{Method: "auto", NeverDefault: true}, true, unrouted, reach, "boot-a", time.Second) {
		t.Fatal("a never-default address without its connected route was accepted")
	}
}

func TestDynamicOracle_AddressWithoutDeviceBoundReachabilityIsRefused(t *testing.T) {
	want := IPSettings{Method: "auto"}
	got := RuntimeFamily{Addresses: []string{"192.0.2.10/24"}, Routes: []Route{{Destination: "0.0.0.0/0", NextHop: "192.0.2.1"}, {Destination: "192.0.2.0/24"}}}
	if dynamicUsable("ens4", want, true, got, nil, "boot-a", time.Second) {
		t.Fatal("an address and route without a reachability observation were accepted")
	}
	for name, mutate := range map[string]func(*Reachability){
		"another interface": func(r *Reachability) { r.Interface = "ens5" },
		"another boot":      func(r *Reachability) { r.BootID = "boot-b" },
		"before the window": func(r *Reachability) { r.At = 0 },
		"a foreign source":  func(r *Reachability) { r.Source = "192.0.2.99" },
		"another family":    func(r *Reachability) { r.Source, r.Target = "2001:db8::10", "2001:db8::53" },
		"no target":         func(r *Reachability) { r.Target = "" },
	} {
		r := freshReach()
		mutate(&r)
		if dynamicUsable("ens4", want, true, got, []Reachability{r}, "boot-a", time.Second) {
			t.Fatalf("an observation from %s was accepted", name)
		}
	}
}

func TestDynamicOracle_UsableRouteAndFreshDeviceBoundReachabilityAreAccepted(t *testing.T) {
	reach := []Reachability{freshReach()}
	withDefault := RuntimeFamily{Addresses: []string{"192.0.2.10/24"}, Routes: []Route{{Destination: "0.0.0.0/0", NextHop: "192.0.2.1"}, {Destination: "192.0.2.0/24"}}}
	if !dynamicUsable("ens4", IPSettings{Method: "auto"}, true, withDefault, reach, "boot-a", time.Second) {
		t.Fatal("the default route and a fresh observation were refused")
	}
	planned := IPSettings{Method: "dhcp", NeverDefault: true, Routes: []Route{{Destination: "198.51.100.0/24", NextHop: "192.0.2.1"}}}
	withPlanned := RuntimeFamily{Addresses: []string{"192.0.2.10/24"}, Routes: []Route{{Destination: "198.51.100.0/24", NextHop: "192.0.2.1", Metric: 100}, {Destination: "192.0.2.0/24"}}}
	if !dynamicUsable("ens4", planned, true, withPlanned, reach, "boot-a", time.Second) {
		t.Fatal("the planned route and a fresh observation were refused")
	}
	connected := RuntimeFamily{Addresses: []string{"192.0.2.10/24"}, Routes: []Route{{Destination: "192.0.2.0/24"}}}
	if !dynamicUsable("ens4", IPSettings{Method: "auto", NeverDefault: true}, true, connected, reach, "boot-a", time.Second) {
		t.Fatal("a never-default family with its connected route and a fresh observation was refused")
	}
}

type foreignDeviceProbe struct{ clock *testClock }

func (foreignDeviceProbe) Check(context.Context, Window, Observation, Confirmation) error { return nil }
func (p foreignDeviceProbe) CheckRestoration(_ context.Context, w Window, _ Observation) ([]Reachability, error) {
	boot, now, _ := p.clock.Now()
	return []Reachability{{Interface: "ens9", Source: "2001:db8::10", Target: "2001:db8::53", BootID: boot, At: now}}, nil
}

func TestRecovery_DynamicBaselineNeedsDeviceBoundReachability(t *testing.T) {
	e, _, clock, change := newTestEngine(t)
	e.config.Probe = foreignDeviceProbe{clock: clock}
	if _, _, err := e.Apply(t.Context(), change); err != nil {
		t.Fatal(err)
	}
	clock.now += 61 * time.Second
	_ = e.Tick(t.Context())
	s, _ := e.Status(change.OperationID)
	if s.State == StateRolledBack {
		t.Fatal("a reachability observation from another device finalized a dynamic recovery")
	}
	e.config.Probe = testProbe{ok: true, clock: clock}
	if err := e.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if s, _ = e.Status(change.OperationID); s.State != StateRolledBack {
		t.Fatal("a fresh device-bound observation did not finish the recovery", s)
	}
}
