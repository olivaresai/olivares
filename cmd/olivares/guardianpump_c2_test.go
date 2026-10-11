// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"log/slog"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/governance"
)

// --- C2 item 4: the guardian sweep runs where rules exist ----------------------

type fakeGov struct {
	rules  bool
	sweeps int
}

func (f *fakeGov) HasGuardianRules(context.Context, model.TenantID) (bool, error) {
	return f.rules, nil
}

func (f *fakeGov) GuardianSweep(context.Context, model.TenantID) (governance.GuardianSweepResult, error) {
	f.sweeps++
	return governance.GuardianSweepResult{}, nil
}

// SR2C P1 (2026-10-03): there is no cached "no rules" decision. A rule created
// (and approved) right after an all-empty tick is swept on the VERY NEXT tick —
// the pump's 30-second operator cadence — never after a minutes-long window.
func TestGuardianPumpSweepsOnlyRuleTenantsAndRechecksEveryTick(t *testing.T) {
	gov := &fakeGov{rules: false}
	p := &guardianPump{st: fakeStoreLeader(true), gov: gov, log: slog.Default(),
		tenants: func(context.Context) ([]model.TenantID, error) { return []model.TenantID{"t1"}, nil }}
	// An all-empty tick: the rule-less tenant is probed and skipped, never swept.
	if err := p.runOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if gov.sweeps != 0 {
		t.Fatalf("a rule-free tenant is never swept, sweeps = %d", gov.sweeps)
	}
	// The next tick rechecks (no window): still empty, still skipped.
	if err := p.runOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if gov.sweeps != 0 {
		t.Fatalf("a still rule-free tenant is still skipped, sweeps = %d", gov.sweeps)
	}
	// A rule is created and approved between ticks: the very next tick sweeps it.
	gov.rules = true
	if err := p.runOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if gov.sweeps != 1 {
		t.Fatalf("a rule created between ticks is swept on the next tick, sweeps = %d", gov.sweeps)
	}
	// And it keeps the operator cadence while rules exist.
	if err := p.runOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if gov.sweeps != 2 {
		t.Fatalf("with rules the pump sweeps every tick, sweeps = %d", gov.sweeps)
	}
}

// A standby never sweeps, rules or not (the leader gate precedes everything).
func TestGuardianPumpStandbyNeverSweeps(t *testing.T) {
	gov := &fakeGov{rules: true}
	p := &guardianPump{st: fakeStoreLeader(false), gov: gov, log: slog.Default(),
		tenants: func(context.Context) ([]model.TenantID, error) { return []model.TenantID{"t1"}, nil }}
	if err := p.runOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if gov.sweeps != 0 {
		t.Fatalf("a standby must not sweep, sweeps = %d", gov.sweeps)
	}
}
