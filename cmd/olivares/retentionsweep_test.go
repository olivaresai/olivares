// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"testing"
	"time"
)

// retentionsweep_test.go pins the loop's environment contract (default
// 24h; "0" disables, loudly; a typo never silently changes the cadence) and
// the business-tenant enumeration (the reserved system tenant is never swept).

func TestRetentionSweepIntervalEnv(t *testing.T) {
	log := discardLog()
	cases := []struct {
		raw  string
		want time.Duration
		ok   bool
	}{
		{"", defaultRetentionSweepInterval, true},
		{"36h", 36 * time.Hour, true},
		{" 15m ", 15 * time.Minute, true},
		// The explicit zero is the ONLY disable; warned loudly.
		{"0", 0, false},
		{"0s", 0, false},
		// A typo or a negative keeps the default rather than silently changing
		// destruction-side cadence.
		{"garbage", defaultRetentionSweepInterval, true},
		{"-5m", defaultRetentionSweepInterval, true},
	}
	for _, c := range cases {
		got, ok := retentionSweepInterval(c.raw, log)
		if ok != c.ok || (ok && got != c.want) {
			t.Fatalf("retentionSweepInterval(%q) = (%v, %v), want (%v, %v)", c.raw, got, ok, c.want, c.ok)
		}
	}
}

func TestNewRetentionSweepLoopDisableSemantics(t *testing.T) {
	env := map[string]string{}
	getenv := func(k string) string { return env[k] }

	if l := newRetentionSweepLoop(getenv, nil, nil, discardLog()); l == nil || l.interval != defaultRetentionSweepInterval {
		t.Fatalf("unset env must yield the default-cadence loop, got %+v", l)
	}
	env[retentionSweepIntervalEnv] = "0"
	if l := newRetentionSweepLoop(getenv, nil, nil, discardLog()); l != nil {
		t.Fatal("\"0\" must disable the loop (nil)")
	}
	env[retentionSweepIntervalEnv] = "2h"
	if l := newRetentionSweepLoop(getenv, nil, nil, discardLog()); l == nil || l.interval != 2*time.Hour {
		t.Fatalf("explicit interval not honored: %+v", l)
	}
}

func TestRetentionSweepBusinessTenantsSkipsSystem(t *testing.T) {
	h := newHarness(t)
	l := &retentionSweepLoop{st: h.st, log: discardLog()}
	tenants, err := l.businessTenants(context.Background())
	if err != nil {
		t.Fatalf("businessTenants: %v", err)
	}
	if len(tenants) < 2 {
		t.Fatalf("expected the harness's two business tenants, got %d", len(tenants))
	}
	foundA := false
	for _, tid := range tenants {
		if tid.IsSystem() {
			t.Fatal("the reserved system tenant must never be swept (no business retention policies there)")
		}
		if tid.String() == h.tenantA {
			foundA = true
		}
	}
	if !foundA {
		t.Fatalf("tenant A missing from the sweep set: %v", tenants)
	}
}
