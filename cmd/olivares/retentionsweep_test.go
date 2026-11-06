// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/compliance"
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

	if l := newRetentionSweepLoop(getenv, nil, &compliance.Module{}, discardLog()); l == nil || l.interval != defaultRetentionSweepInterval {
		t.Fatalf("unset env must yield the default-cadence loop, got %+v", l)
	}
	env[retentionSweepIntervalEnv] = "0"
	if l := newRetentionSweepLoop(getenv, nil, &compliance.Module{}, discardLog()); l != nil {
		t.Fatal("\"0\" must disable the loop (nil)")
	}
	env[retentionSweepIntervalEnv] = "2h"
	if l := newRetentionSweepLoop(getenv, nil, &compliance.Module{}, discardLog()); l == nil || l.interval != 2*time.Hour {
		t.Fatalf("explicit interval not honored: %+v", l)
	}
}

// noAdminRole is a database that refuses to list every tenant (PostgreSQL without the tenant
// inventory or the administration role, a documented setup) but still lists the visible ones.
type noAdminRole struct{ store.Store }

func (s noAdminRole) System(ctx context.Context, fn func(store.SystemScope) error) error {
	return s.Store.System(ctx, func(sys store.SystemScope) error { return fn(refusingListOrgs{sys}) })
}

type refusingListOrgs struct{ store.SystemScope }

func (refusingListOrgs) ListOrgs(context.Context) ([]model.Org, error) {
	return nil, store.ErrEnumerationNotAuthoritative
}

// A Community boot warns about a stored enabled purge schedule it does not apply on the documented
// PostgreSQL setup too, not only where every tenant can be listed.
func TestCommunityWarnsAboutStoredPurgeSchedulesWithoutTheAdminRole(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if err := h.st.Mutate(ctx, model.TenantID(h.tenantA), func(sc store.Scope) error {
		repo, err := sc.Ext("compliance.retention_policy")
		if err != nil {
			return err
		}
		_, err = repo.Create(ctx, model.Record{"data_class": "agent.memory", "retention_days": int64(30), "disposition": "purge", "enabled": true})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	log, buf := captureLog()
	warnStoredPurgeSchedules(ctx, noAdminRole{h.st}, h.set.compliance, log)
	if !strings.Contains(buf.String(), "level=WARN") || !strings.Contains(buf.String(), "schedules=1") {
		t.Fatalf("boot log = %q; want one warning naming the stored schedule", buf.String())
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
