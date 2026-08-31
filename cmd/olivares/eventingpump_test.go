// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"testing"
	"time"
)

// eventingpump_test.go pins the pump's environment contract (default 15s;
// "0" disables, loudly; a typo never silently changes the cadence — the
// retention-sweep posture) and the tenant-enumeration pass over the real
// composition root (system tenant skipped; a pass over idle tenants is a
// silent no-op).

func TestEventingPumpIntervalEnv(t *testing.T) {
	log := discardLog()
	cases := []struct {
		raw  string
		want time.Duration
		ok   bool
	}{
		{"", defaultEventingPumpInterval, true},
		{"30s", 30 * time.Second, true},
		{" 2m ", 2 * time.Minute, true},
		{"0", 0, false},
		{"0s", 0, false},
		{"garbage", defaultEventingPumpInterval, true},
		{"-5s", defaultEventingPumpInterval, true},
	}
	for _, c := range cases {
		got, ok := eventingPumpInterval(c.raw, log)
		if ok != c.ok || (ok && got != c.want) {
			t.Fatalf("eventingPumpInterval(%q) = (%v, %v), want (%v, %v)", c.raw, got, ok, c.want, c.ok)
		}
	}
}

func TestNewEventingPumpDisableSemantics(t *testing.T) {
	env := map[string]string{}
	getenv := func(k string) string { return env[k] }

	if p := newEventingPump(getenv, nil, nil, discardLog()); p == nil || p.interval != defaultEventingPumpInterval {
		t.Fatalf("unset env must yield the default-cadence pump, got %+v", p)
	}
	env[eventingPumpIntervalEnv] = "0"
	if p := newEventingPump(getenv, nil, nil, discardLog()); p != nil {
		t.Fatal("\"0\" must disable the pump (nil)")
	}
	env[eventingPumpIntervalEnv] = "45s"
	if p := newEventingPump(getenv, nil, nil, discardLog()); p == nil || p.interval != 45*time.Second {
		t.Fatalf("explicit interval not honored: %+v", p)
	}
}

// One runOnce pass over the full composition root: enumerates the business
// tenants (never the system tenant), runs DispatchDue+PruneExpired per tenant,
// and an idle estate is a clean no-op.
func TestEventingPumpRunOncePassesAllBusinessTenants(t *testing.T) {
	h := newHarness(t)
	p := &eventingPump{st: h.st, evt: h.set.eventing, interval: time.Second, log: discardLog()}

	tenants, err := p.businessTenants(context.Background())
	if err != nil {
		t.Fatalf("businessTenants: %v", err)
	}
	if len(tenants) < 2 {
		t.Fatalf("expected the harness's business tenants, got %d", len(tenants))
	}
	for _, tid := range tenants {
		if tid.IsSystem() || tid.IsZero() {
			t.Fatal("the reserved system tenant must never be pumped")
		}
	}
	if err := p.runOnce(context.Background()); err != nil {
		t.Fatalf("runOnce over an idle estate must no-op cleanly: %v", err)
	}
}
