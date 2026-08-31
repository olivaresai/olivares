// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"testing"
	"time"
)

// orchworkflowpump_test.go pins the pump's environment contract (default
// 15s; "0" disables, loudly; a typo never silently changes the cadence), the
// workflow-limit env parsing (a typo keeps the module defaults) and the
// tenant-enumeration pass over the real composition root. The runner's
// semantics are pinned in modules/orchestration (the workflow_test suite).

func TestOrchWorkflowPumpIntervalEnv(t *testing.T) {
	log := discardLog()
	cases := []struct {
		raw  string
		want time.Duration
		ok   bool
	}{
		{"", defaultOrchWorkflowPumpInterval, true},
		{"30s", 30 * time.Second, true},
		{" 2m ", 2 * time.Minute, true},
		{"0", 0, false},
		{"0s", 0, false},
		{"garbage", defaultOrchWorkflowPumpInterval, true},
		{"-5s", defaultOrchWorkflowPumpInterval, true},
	}
	for _, c := range cases {
		got, ok := orchWorkflowPumpInterval(c.raw, log)
		if ok != c.ok || (ok && got != c.want) {
			t.Fatalf("orchWorkflowPumpInterval(%q) = (%v, %v), want (%v, %v)", c.raw, got, ok, c.want, c.ok)
		}
	}
}

func TestLoadWorkflowLimits(t *testing.T) {
	env := map[string]string{}
	getenv := func(k string) string { return env[k] }
	log := discardLog()

	if w, s := loadWorkflowLimits(getenv, log); w != 0 || s != 0 {
		t.Fatalf("unset env must keep the module defaults (0,0 passthrough), got (%d,%d)", w, s)
	}
	env[orchWorkflowMaxEnv] = "500"
	env[orchWorkflowStepsMaxEnv] = "80"
	if w, s := loadWorkflowLimits(getenv, log); w != 500 || s != 80 {
		t.Fatalf("explicit limits not honored: (%d,%d)", w, s)
	}
	env[orchWorkflowMaxEnv] = "banana"
	env[orchWorkflowStepsMaxEnv] = "-3"
	if w, s := loadWorkflowLimits(getenv, log); w != 0 || s != 0 {
		t.Fatalf("a typo must keep the defaults, got (%d,%d)", w, s)
	}
}

// One runOnce pass over the full composition root: enumerates the business
// tenants (never the system tenant), advances per tenant, and an idle estate
// is a clean no-op. Also the wire-proof that the composition root captures the
// orchestration module for THIS pump too.
func TestOrchWorkflowPumpRunOncePassesAllBusinessTenants(t *testing.T) {
	h := newHarness(t)
	if h.set.orchestration == nil {
		t.Fatal("moduleSet must capture the orchestration module for the workflow pump")
	}
	p := &orchWorkflowPump{st: h.st, orch: h.set.orchestration, interval: time.Second, log: discardLog()}

	tenants, err := p.businessTenants(context.Background())
	if err != nil {
		t.Fatalf("businessTenants: %v", err)
	}
	for _, tid := range tenants {
		if tid.IsSystem() || tid.IsZero() {
			t.Fatal("the reserved system tenant must never be advanced")
		}
	}
	if err := p.runOnce(context.Background()); err != nil {
		t.Fatalf("runOnce over an idle estate must no-op cleanly: %v", err)
	}
}
