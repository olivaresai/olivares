// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"testing"
	"time"
)

// orchcadencepump_test.go pins the pump's environment contract (default
// 1m; "0" disables, loudly; a typo never silently changes the cadence — the
// retention-sweep posture) and the tenant-enumeration pass over the real
// composition root (system tenant skipped; an idle estate is a clean no-op).
// The scan's detection semantics are pinned in modules/orchestration
// (TestS431_RunCadenceScanExportedSeam and the cadence tests).

func TestOrchCadencePumpIntervalEnv(t *testing.T) {
	log := discardLog()
	cases := []struct {
		raw  string
		want time.Duration
		ok   bool
	}{
		{"", defaultOrchCadencePumpInterval, true},
		{"30s", 30 * time.Second, true},
		{" 2m ", 2 * time.Minute, true},
		{"0", 0, false},
		{"0s", 0, false},
		{"garbage", defaultOrchCadencePumpInterval, true},
		{"-5s", defaultOrchCadencePumpInterval, true},
	}
	for _, c := range cases {
		got, ok := orchCadencePumpInterval(c.raw, log)
		if ok != c.ok || (ok && got != c.want) {
			t.Fatalf("orchCadencePumpInterval(%q) = (%v, %v), want (%v, %v)", c.raw, got, ok, c.want, c.ok)
		}
	}
}

func TestNewOrchCadencePumpDisableSemantics(t *testing.T) {
	env := map[string]string{}
	getenv := func(k string) string { return env[k] }

	if p := newOrchCadencePump(getenv, nil, nil, discardLog()); p == nil || p.interval != defaultOrchCadencePumpInterval {
		t.Fatalf("unset env must yield the default-cadence pump, got %+v", p)
	}
	env[orchCadencePumpIntervalEnv] = "0"
	if p := newOrchCadencePump(getenv, nil, nil, discardLog()); p != nil {
		t.Fatal("\"0\" must disable the pump (nil)")
	}
	env[orchCadencePumpIntervalEnv] = "45s"
	if p := newOrchCadencePump(getenv, nil, nil, discardLog()); p == nil || p.interval != 45*time.Second {
		t.Fatalf("explicit interval not honored: %+v", p)
	}
}

// One runOnce pass over the full composition root: enumerates the business
// tenants (never the system tenant), runs the scan per tenant, and an idle
// estate is a clean no-op. Also the wire-proof that the composition root
// actually captures the orchestration module for the pump (set.orchestration).
func TestOrchCadencePumpRunOncePassesAllBusinessTenants(t *testing.T) {
	h := newHarness(t)
	if h.set.orchestration == nil {
		t.Fatal("moduleSet must capture the orchestration module for the cadence pump")
	}
	p := &orchCadencePump{st: h.st, orch: h.set.orchestration, interval: time.Second, log: discardLog()}

	tenants, err := p.businessTenants(context.Background())
	if err != nil {
		t.Fatalf("businessTenants: %v", err)
	}
	if len(tenants) < 2 {
		t.Fatalf("expected the harness's business tenants, got %d", len(tenants))
	}
	for _, tid := range tenants {
		if tid.IsSystem() || tid.IsZero() {
			t.Fatal("the reserved system tenant must never be scanned")
		}
	}
	if err := p.runOnce(context.Background()); err != nil {
		t.Fatalf("runOnce over an idle estate must no-op cleanly: %v", err)
	}
}
