// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"log/slog"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/runtime"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/finops"
	"github.com/olivaresai/olivares/modules/sessions"
)

// budgetReservationKind is the module's reservation ext kind, spelled here because
// a test outside modules/finops cannot import the unexported constant. Staging a
// row through it is how a hold is made to have lapsed without spending the TTL.
const budgetReservationKind model.Kind = "finops.budget_reservation"

// openFinOpsEngine opens a real SQLite store with the FinOps schema, provisions
// one active business org and wires the module's data handle: the same shape boot
// builds, so the gates under test talk to the real admission ledger.
func openFinOpsEngine(t *testing.T) (*finops.Module, store.Store, model.TenantID) {
	t.Helper()
	ctx := context.Background()
	fin := finops.New()
	st, err := coreengine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true}, fin.RegisterSchema)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	var tenant model.TenantID
	if err := st.System(ctx, func(sys store.SystemScope) error {
		if _, err := sys.EnsureSystemTenant(ctx); err != nil {
			return err
		}
		org, err := sys.CreateOrg(ctx, model.Org{Name: "admission-recon", Slug: "admission-recon", Status: model.StatusActive})
		tenant = org.TenantID
		return err
	}); err != nil {
		t.Fatalf("provision tenant: %v", err)
	}
	fin.UseData(api.NewModuleData(st))
	return fin, st, tenant
}

// TestSessionLaunchGate_LaunchesLeaveNoHoldToSettle is the end-to-end of the
// decision this gate makes about amounts. A launch never learns what the session
// spent, so it can neither Commit nor Release — and a hold nobody can settle is
// drift the engine produces itself, reported to the operator as if a caller had
// misbehaved. Every enforcing budget is still evaluated; nothing is held.
//
// It drives the real module, so the assertion is over the real ledger and not
// over a recorded call.
func TestSessionLaunchGate_LaunchesLeaveNoHoldToSettle(t *testing.T) {
	fin, st, tenant := openFinOpsEngine(t)
	createBudgetPolicy(t, st, tenant, "launch-cap", map[string]any{
		"dimension": "global", "period": "monthly",
		"limit_micro_usd": int64(5_000_000), "action": "block",
	})
	g := &sessionLaunchGate{
		fin:             fin,
		budgetPosture:   availabilityFailClosed,
		recordAvailable: true,
		log:             slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
	}

	const launches = 4
	for i := 0; i < launches; i++ {
		dec, err := g.Authorize(context.Background(), tenant, sessions.LaunchIntent{
			PermissionMode: "default", RunRef: "run-" + strconv.Itoa(i), AgentRef: "agent-1",
		})
		if err != nil {
			t.Fatalf("launch %d: %v", i, err)
		}
		if !dec.Allowed {
			t.Fatalf("launch %d was denied under a cap with headroom: %+v", i, dec)
		}
	}

	report, err := fin.InspectReservations(context.Background(), tenant)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if report.Active != 0 {
		t.Fatalf("after %d launches the ledger holds %d active reservation(s) nobody can settle", launches, report.Active)
	}
	if report.Drift {
		t.Fatalf("launching produced drift the engine made itself: %+v", report)
	}
}

// TestAdmissionReconciler_SweepsALapsedHold pins the loop the design assumed and
// the engine never had. ReconcileReservations had a route, a CLI verb and no
// in-process caller at all, so a hold left by a caller that died mid-call sat in
// the ledger until its TTL and the console's drift read had nobody to act on it.
//
// The lapsed row is staged rather than waited for: the module's clock is not a
// seam the composition root can move, and five real minutes is not a test.
func TestAdmissionReconciler_SweepsALapsedHold(t *testing.T) {
	fin, st, tenant := openFinOpsEngine(t)
	stageLapsedHold(t, st, tenant)

	var buf bytes.Buffer
	l := newAdmissionReconciler(st, fin, slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	if l == nil {
		t.Fatal("the reconciler must be built when FinOps is wired")
	}
	if l.interval != admissionReconcileInterval {
		t.Fatalf("interval = %v, want the named constant %v", l.interval, admissionReconcileInterval)
	}

	before, err := fin.InspectReservations(context.Background(), tenant)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if before.ActiveLapsed != 1 {
		t.Fatalf("the fixture did not stage a lapsed hold: %+v", before)
	}

	if err := l.runOnce(context.Background()); err != nil {
		t.Fatalf("runOnce: %v", err)
	}

	after, err := fin.InspectReservations(context.Background(), tenant)
	if err != nil {
		t.Fatalf("inspect after: %v", err)
	}
	if after.ActiveLapsed != 0 || after.ExpiredUnsettled != 1 {
		t.Fatalf("the lapsed hold was not swept: %+v", after)
	}
	if !strings.Contains(buf.String(), "swept_expired=1") {
		t.Fatalf("the pass did not report what it swept:\n%s", buf.String())
	}
}

// TestAdmissionReconciler_RegistersOnTheRuntimeScheduler pins the ownership of the
// goroutine: the runtime starts it and the runtime's shutdown stops it, so there
// is no timer of ours to leak. The test starts and stops a real runtime, which is
// also the negative control for a job the scheduler would refuse (an empty name,
// a non-positive interval, a nil func).
func TestAdmissionReconciler_RegistersOnTheRuntimeScheduler(t *testing.T) {
	fin, st, _ := openFinOpsEngine(t)
	l := newAdmissionReconciler(st, fin, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	rt := runtime.New(runtime.Options{})
	if err := l.register(rt); err != nil {
		t.Fatalf("register: %v", err)
	}
	ctx := context.Background()
	if err := rt.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := rt.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}
}

// TestAdmissionReconciler_NilWithoutFinOps keeps the loop off an install that has
// no ledger to reconcile: a tick that enumerates tenants to call nothing is pure
// cost.
func TestAdmissionReconciler_NilWithoutFinOps(t *testing.T) {
	_, st, _ := openFinOpsEngine(t)
	if l := newAdmissionReconciler(st, nil, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))); l != nil {
		t.Fatal("no FinOps module means no reservation ledger and no loop")
	}
}

// stageLapsedHold writes one active reservation whose TTL has already passed —
// the durable trace of a caller that took headroom and died before settling it.
func stageLapsedHold(t *testing.T, st store.Store, tenant model.TenantID) {
	t.Helper()
	now := time.Now().UTC()
	if err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(budgetReservationKind)
		if err != nil {
			return err
		}
		_, err = repo.Create(context.Background(), model.Record{
			"policy_ref":       model.NewID().String(),
			"policy_kind":      "budget",
			"dimension":        "global",
			"dim_key":          "",
			"period":           "monthly",
			"period_start":     model.NewTimestamp(now.AddDate(0, 0, -1)).String(),
			"seq":              int64(1),
			"amount_micro_usd": int64(1_000_000),
			"actual_micro_usd": int64(0),
			"state":            "active",
			"handle":           model.NewID().String(),
			"expires_at":       model.NewTimestamp(now.Add(-time.Hour)).String(),
		})
		return err
	}); err != nil {
		t.Fatalf("stage a lapsed hold: %v", err)
	}
}

// countingElector answers Active() true for a fixed number of asks and false
// after that, so leadership can move BETWEEN two tenants without a seam inside
// the loop and without a real election. The loop asks once before enumerating the
// estate and once before each tenant, so activeFor = 1 + n lets exactly n tenants
// run. It wraps the store's real elector, so every other method still answers.
type countingElector struct {
	store.LeaderElector
	activeFor int64
	asked     atomic.Int64
}

func (e *countingElector) Active() bool { return e.asked.Add(1) <= e.activeFor }

// TestAdmissionReconciler_ChecksLeadershipBeforeEveryTenant pins the gate the
// loop's own comment claims and nothing held: a standby reconciles nothing, a
// node demoted mid-pass stops where it is and records why, and a leader finishes.
//
// The middle arm is the one that matters. A pass over a large estate outlives an
// election, so a gate taken once per tick lets a demoted node keep sweeping and
// filing drift findings for every remaining tenant while the new leader does the
// same — two writers on one ledger, which is what the gate exists to prevent.
func TestAdmissionReconciler_ChecksLeadershipBeforeEveryTenant(t *testing.T) {
	fin, raw, tenantA := openFinOpsEngine(t)
	tenantB := addFinOpsOrg(t, raw, "admission-recon-b")
	stageLapsedHold(t, raw, tenantA)
	stageLapsedHold(t, raw, tenantB)
	ctx := context.Background()

	lapsed := func() int {
		t.Helper()
		total := 0
		for _, tenant := range []model.TenantID{tenantA, tenantB} {
			report, err := fin.InspectReservations(ctx, tenant)
			if err != nil {
				t.Fatalf("inspect %s: %v", tenant, err)
			}
			total += report.ActiveLapsed
		}
		return total
	}
	pass := func(activeFor int64) string {
		t.Helper()
		var buf bytes.Buffer
		le := &countingElector{LeaderElector: raw.Leader(), activeFor: activeFor}
		l := newAdmissionReconciler(electedStore{Store: raw, le: le}, fin,
			slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
		if err := l.runOnce(ctx); err != nil {
			t.Fatalf("runOnce: %v", err)
		}
		return buf.String()
	}

	if lapsed() != 2 {
		t.Fatalf("the fixture did not stage a lapsed hold in each of the two tenants")
	}

	standby := pass(0)
	if got := lapsed(); got != 2 {
		t.Fatalf("a standby reconciled %d of the 2 tenants; a node that is not the active writer sweeps none", 2-got)
	}
	if !strings.Contains(standby, "standby") {
		t.Fatalf("a standby pass did not record why it did nothing:\n%s", standby)
	}

	demoted := pass(2) // the tick itself, then the first tenant
	if got := lapsed(); got != 1 {
		t.Fatalf("a node demoted after its first tenant reconciled %d of the 2; the pass must stop where leadership did", 2-got)
	}
	if !strings.Contains(demoted, "stopped=leadership") {
		t.Fatalf("the demotion was not reported as a leadership stop:\n%s", demoted)
	}

	if leader := pass(64); !strings.Contains(leader, "swept_expired=1") {
		t.Fatalf("a promoted node did not complete the pass:\n%s", leader)
	}
	if got := lapsed(); got != 0 {
		t.Fatalf("%d lapsed hold(s) survived a full pass by the active writer", got)
	}
}

// addFinOpsOrg provisions one more active business org on an open store, so a
// pass has more than one tenant to be interrupted between.
func addFinOpsOrg(t *testing.T, st store.Store, slug string) model.TenantID {
	t.Helper()
	var tenant model.TenantID
	if err := st.System(context.Background(), func(sys store.SystemScope) error {
		org, err := sys.CreateOrg(context.Background(), model.Org{
			Name: slug, Slug: slug, Status: model.StatusActive,
		})
		tenant = org.TenantID
		return err
	}); err != nil {
		t.Fatalf("provision %s: %v", slug, err)
	}
	return tenant
}
