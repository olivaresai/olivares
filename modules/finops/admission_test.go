// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

func TestParseUnreachablePosture(t *testing.T) {
	if ParseUnreachablePosture("") != UnreachableDeny {
		t.Fatal("empty must default to deny")
	}
	if ParseUnreachablePosture("allow") != UnreachableAllow {
		t.Fatal("allow must parse")
	}
	if ParseUnreachablePosture("typo") != UnreachableDeny {
		t.Fatal("unknown must deny, not weaken")
	}
}

func TestReserveRejectsInvalidRequest(t *testing.T) {
	m, _, tenant, _ := newFin(t)
	_, err := m.Reserve(context.Background(), tenant, AdmissionRequest{
		Scope: "nope", EstimateMicroUSD: 1, IdempotencyKey: "k1",
	})
	if !errors.Is(err, ErrInvalidAdmission) {
		t.Fatalf("unknown scope: %v", err)
	}
	_, err = m.Reserve(context.Background(), tenant, AdmissionRequest{
		Scope: AdmissionScopeModelGateway, EstimateMicroUSD: 1,
	})
	if !errors.Is(err, ErrInvalidAdmission) {
		t.Fatalf("missing key: %v", err)
	}
	_, err = m.Reserve(context.Background(), tenant, AdmissionRequest{
		Scope: AdmissionScopeModelGateway, EstimateMicroUSD: -1, IdempotencyKey: "k1",
	})
	if !errors.Is(err, ErrInvalidAdmission) {
		t.Fatalf("negative estimate: %v", err)
	}
}

func TestReserveFailClosedWhenStoreMissing(t *testing.T) {
	m := New()
	res, err := m.Reserve(context.Background(), "t1", AdmissionRequest{
		Scope: AdmissionScopeModelGateway, IdempotencyKey: "k-missing",
	})
	if err != nil {
		t.Fatalf("deny-closed must not return an error the caller fails open on: %v", err)
	}
	if res.Allowed || res.Action != "block" || res.Reason != ReasonStoreUnreachable {
		t.Fatalf("missing store must deny: %+v", res)
	}
}

func TestReserveFailClosedWhenStoreMissingCanAllow(t *testing.T) {
	m := New()
	res, err := m.Reserve(context.Background(), "t1", AdmissionRequest{
		Scope: AdmissionScopeModelGateway, IdempotencyKey: "k-missing",
		Unreachable: UnreachableAllow,
	})
	if err == nil || !res.Allowed {
		t.Fatalf("explicit allow must fail open: %+v err=%v", res, err)
	}
}

func TestReserveDeniesExhaustedBudgetAndAudits(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	createBudget(t, st, tenant, "global-block", budgetSpec{
		Dimension: "global", Period: "monthly", LimitMicroUSD: oneUSD, Action: "block",
	})
	first, err := m.Reserve(context.Background(), tenant, AdmissionRequest{
		Scope: AdmissionScopeModelGateway, EstimateMicroUSD: oneUSD, IdempotencyKey: "k-first",
	})
	if err != nil || !first.Allowed {
		t.Fatalf("first reserve: %+v err=%v", first, err)
	}
	second, err := m.Reserve(context.Background(), tenant, AdmissionRequest{
		Scope: AdmissionScopeModelGateway, EstimateMicroUSD: oneUSD, IdempotencyKey: "k-second",
	})
	if err != nil {
		t.Fatalf("second reserve err: %v", err)
	}
	if second.Allowed || second.Action != "block" {
		t.Fatalf("exhausted budget must deny: %+v", second)
	}
	if countAuditAction(t, st, tenant, "finops.admission.denied") < 1 {
		t.Fatal("every deny must write an audit row")
	}
}

func TestReserveIdempotentRetryDoesNotDoubleCount(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	createBudget(t, st, tenant, "global-block", budgetSpec{
		Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD, Action: "block",
	})
	req := AdmissionRequest{
		Scope: AdmissionScopeModelGateway, EstimateMicroUSD: oneUSD, IdempotencyKey: "same-key",
	}
	first, err := m.Reserve(context.Background(), tenant, req)
	if err != nil || !first.Allowed || first.Handle == "" {
		t.Fatalf("first: %+v err=%v", first, err)
	}
	second, err := m.Reserve(context.Background(), tenant, req)
	if err != nil || !second.Allowed || !second.Replayed {
		t.Fatalf("retry must replay: %+v err=%v", second, err)
	}
	if second.Handle != first.Handle {
		t.Fatalf("retry handle %q != %q", second.Handle, first.Handle)
	}
	conflict, err := m.Reserve(context.Background(), tenant, AdmissionRequest{
		Scope: AdmissionScopeModelGateway, EstimateMicroUSD: 2 * oneUSD, IdempotencyKey: "same-key",
	})
	if !errors.Is(err, ErrAdmissionConflict) {
		t.Fatalf("different payload must conflict, got %+v err=%v", conflict, err)
	}
}

func TestReserveCommitReleaseLifecycle(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	createBudget(t, st, tenant, "global-block", budgetSpec{
		Dimension: "global", Period: "monthly", LimitMicroUSD: 5 * oneUSD, Action: "block",
	})
	res, err := m.Reserve(context.Background(), tenant, AdmissionRequest{
		Scope: AdmissionScopeSessionLaunch, EstimateMicroUSD: oneUSD, IdempotencyKey: "life-1",
	})
	if err != nil || !res.Allowed {
		t.Fatalf("reserve: %+v err=%v", res, err)
	}
	if err := m.Commit(context.Background(), tenant, res.Handle, 500_000); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := m.Commit(context.Background(), tenant, res.Handle, 500_000); err != nil {
		t.Fatalf("commit must be idempotent: %v", err)
	}
	released, err := m.Reserve(context.Background(), tenant, AdmissionRequest{
		Scope: AdmissionScopeScheduledJob, EstimateMicroUSD: oneUSD, IdempotencyKey: "life-2",
	})
	if err != nil || !released.Allowed {
		t.Fatalf("second reserve: %+v err=%v", released, err)
	}
	if err := m.Release(context.Background(), tenant, released.Handle); err != nil {
		t.Fatalf("release: %v", err)
	}
	if err := m.Release(context.Background(), tenant, released.Handle); err != nil {
		t.Fatalf("release must be idempotent: %v", err)
	}
}

func TestAdmissionReserve_ConcurrentExactlyMminus1(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	const M = 100
	limit := int64(M-1) * oneUSD
	createBudget(t, st, tenant, "global-block", budgetSpec{
		Dimension: "global", Period: "monthly", LimitMicroUSD: limit, Action: "block",
	})

	var wg sync.WaitGroup
	start := make(chan struct{})
	var allowed, denied int64
	wg.Add(M)
	for i := 0; i < M; i++ {
		i := i
		go func() {
			defer wg.Done()
			<-start
			res, err := m.Reserve(context.Background(), tenant, AdmissionRequest{
				Scope: AdmissionScopeModelGateway, EstimateMicroUSD: oneUSD,
				IdempotencyKey: "par-" + strconv.Itoa(i),
			})
			if err != nil {
				t.Errorf("Reserve: %v", err)
				return
			}
			if res.Allowed {
				atomic.AddInt64(&allowed, 1)
			} else {
				atomic.AddInt64(&denied, 1)
			}
		}()
	}
	close(start)
	wg.Wait()
	if allowed != int64(M-1) {
		t.Fatalf("admitted %d, want exactly %d (over-admission)", allowed, M-1)
	}
	if denied != 1 {
		t.Fatalf("denied %d, want 1", denied)
	}
}

func TestAdmissionReserve_SameKeyParallelDoesNotDoubleCount(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	createBudget(t, st, tenant, "global-block", budgetSpec{
		Dimension: "global", Period: "monthly", LimitMicroUSD: 2 * oneUSD, Action: "block",
	})
	const N = 32
	var wg sync.WaitGroup
	start := make(chan struct{})
	var allowed int64
	handles := make([]string, N)
	wg.Add(N)
	for i := 0; i < N; i++ {
		i := i
		go func() {
			defer wg.Done()
			<-start
			res, err := m.Reserve(context.Background(), tenant, AdmissionRequest{
				Scope: AdmissionScopeModelGateway, EstimateMicroUSD: oneUSD,
				IdempotencyKey: "one-intent",
			})
			if err != nil {
				t.Errorf("Reserve: %v", err)
				return
			}
			if res.Allowed {
				atomic.AddInt64(&allowed, 1)
				handles[i] = res.Handle
			}
		}()
	}
	close(start)
	wg.Wait()
	if allowed != N {
		t.Fatalf("every retry of the same key must admit, got %d", allowed)
	}
	first := ""
	for _, h := range handles {
		if h == "" {
			continue
		}
		if first == "" {
			first = h
			continue
		}
		if h != first {
			t.Fatalf("same key produced two handles %q and %q", first, h)
		}
	}
	third, err := m.Reserve(context.Background(), tenant, AdmissionRequest{
		Scope: AdmissionScopeModelGateway, EstimateMicroUSD: oneUSD,
		IdempotencyKey: "other-intent",
	})
	if err != nil {
		t.Fatalf("other intent: %v", err)
	}
	if !third.Allowed {
		t.Fatal("a 2 USD budget must still have room for a second distinct intent of 1 USD")
	}
}

func TestReconcileReservationsReportsExpiredUnsettled(t *testing.T) {
	m, st, tenant, host := newFin(t)
	clk := &fakeClock{t: baseTime}
	m.clock = clk
	createBudget(t, st, tenant, "global-block", budgetSpec{
		Dimension: "global", Period: "monthly", LimitMicroUSD: 5 * oneUSD, Action: "block",
	})
	res, err := m.Reserve(context.Background(), tenant, AdmissionRequest{
		Scope: AdmissionScopeModelGateway, EstimateMicroUSD: oneUSD, IdempotencyKey: "drift-1",
	})
	if err != nil || !res.Allowed {
		t.Fatalf("reserve: %+v err=%v", res, err)
	}
	clk.advance(reservationTTL + 1)
	report, err := m.ReconcileReservations(context.Background(), tenant)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if !report.Drift || report.ExpiredUnsettled < 1 {
		t.Fatalf("expired unsettle must be drift: %+v", report)
	}
	found := false
	for _, f := range host.findings() {
		if f.Kind == findingKindReservationDrift {
			found = true
		}
	}
	if !found {
		t.Fatal("drift must emit a posture finding, not stay silent")
	}
}

// TestInspectReservationsReadsWithoutWriting pins the difference between the two
// reconciliation routes. GET /admission/reconciliation needs only budget READ, so
// what it calls must not sweep a hold or file a finding: a read permission that
// moves the ledger is a write wearing a read's name. The same ledger state still
// reports drift both ways — a lapsed hold the job has not swept is ActiveLapsed
// on the read and ExpiredUnsettled after the job.
func TestInspectReservationsReadsWithoutWriting(t *testing.T) {
	m, st, tenant, host := newFin(t)
	clk := &fakeClock{t: baseTime}
	m.clock = clk
	createBudget(t, st, tenant, "global-block", budgetSpec{
		Dimension: "global", Period: "monthly", LimitMicroUSD: 5 * oneUSD, Action: "block",
	})
	res, err := m.Reserve(context.Background(), tenant, AdmissionRequest{
		Scope: AdmissionScopeModelGateway, EstimateMicroUSD: oneUSD, IdempotencyKey: "read-only-1",
	})
	if err != nil || !res.Allowed {
		t.Fatalf("reserve: %+v err=%v", res, err)
	}
	clk.advance(reservationTTL + 1)

	before := len(host.findings())
	report, err := m.InspectReservations(context.Background(), tenant)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if !report.Drift || report.ActiveLapsed < 1 {
		t.Fatalf("a lapsed hold must read as drift: %+v", report)
	}
	if report.SweptExpired != 0 {
		t.Fatalf("the read swept %d hold(s): it must not sweep", report.SweptExpired)
	}
	if report.FindingRef != "" {
		t.Fatalf("the read filed %q: it must emit no finding", report.FindingRef)
	}
	if got := len(host.findings()); got != before {
		t.Fatalf("findings went from %d to %d across a READ", before, got)
	}

	// The second read sees the same ledger: nothing the first one did changed it.
	again, err := m.InspectReservations(context.Background(), tenant)
	if err != nil {
		t.Fatalf("second inspect: %v", err)
	}
	if again.ActiveLapsed != report.ActiveLapsed || again.Active != report.Active {
		t.Fatalf("a read changed the ledger: %+v then %+v", report, again)
	}

	// And the JOB, over that same state, does sweep and does file the finding.
	job, err := m.ReconcileReservations(context.Background(), tenant)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if job.SweptExpired < 1 {
		t.Fatalf("the job must sweep the lapsed hold: %+v", job)
	}
	if !job.Drift || job.FindingRef == "" {
		t.Fatalf("the job must report drift with its finding: %+v", job)
	}
	if got := len(host.findings()); got <= before {
		t.Fatalf("the job filed no finding: %d then %d", before, got)
	}
}

// TestAdmissionSpendLimitDenyIsMarked pins the SpendLimit flag on the way out of
// Reserve. Without it a per-seat cap and a pooled budget are the same deny to
// every caller, and the inference proxy publishes them as the same sentence —
// which is what happened when the gate moved onto Reserve: the apps-gateway
// contract's "spend limit reached" became "budget limit reached" on the wire.
func TestAdmissionSpendLimitDenyIsMarked(t *testing.T) {
	m, _, tenant, _ := newFin(t)
	ctx := context.Background()
	if _, _, err := m.SpendLimitUpsert(ctx, tenant, userLimit("user:seat", "1", "monthly"), "user:admin"); err != nil {
		t.Fatalf("spend limit upsert: %v", err)
	}
	res, err := m.Reserve(ctx, tenant, AdmissionRequest{
		Scope: AdmissionScopeModelGateway, ActorRef: "user:seat",
		EstimateMicroUSD: 10 * oneUSD, IdempotencyKey: "k-seat",
	})
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if res.Allowed {
		t.Fatalf("a seat over its limit must be denied: %+v", res)
	}
	if !res.SpendLimit {
		t.Fatalf("a spend-limit deny must be marked as one: %+v", res)
	}
}

// TestAdmissionBudgetDenyIsNotMarkedSpendLimit is the other side of the same
// flag: a pooled budget deny must NOT claim to be a seat limit, or the marker
// says nothing.
func TestAdmissionBudgetDenyIsNotMarkedSpendLimit(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	createBudget(t, st, tenant, "global-block", budgetSpec{
		Dimension: "global", Period: "monthly", LimitMicroUSD: oneUSD, Action: "block",
	})
	ctx := context.Background()
	if first, err := m.Reserve(ctx, tenant, AdmissionRequest{
		Scope: AdmissionScopeModelGateway, EstimateMicroUSD: oneUSD, IdempotencyKey: "k-b1",
	}); err != nil || !first.Allowed {
		t.Fatalf("first reserve: %+v err=%v", first, err)
	}
	res, err := m.Reserve(ctx, tenant, AdmissionRequest{
		Scope: AdmissionScopeModelGateway, EstimateMicroUSD: oneUSD, IdempotencyKey: "k-b2",
	})
	if err != nil || res.Allowed {
		t.Fatalf("exhausted budget must deny: %+v err=%v", res, err)
	}
	if res.SpendLimit {
		t.Fatalf("a budget deny must not be marked as a spend limit: %+v", res)
	}
}

func TestCommitEmptyHandleIsNoop(t *testing.T) {
	m, _, tenant, _ := newFin(t)
	if err := m.Commit(context.Background(), tenant, "", 1); err != nil {
		t.Fatalf("empty commit: %v", err)
	}
	if err := m.Release(context.Background(), tenant, ""); err != nil {
		t.Fatalf("empty release: %v", err)
	}
}

func TestReserveNoEnforcingBudgetAdmits(t *testing.T) {
	m, _, tenant, _ := newFin(t)
	res, err := m.Reserve(context.Background(), tenant, AdmissionRequest{
		Scope: AdmissionScopeScheduledJob, EstimateMicroUSD: oneUSD, IdempotencyKey: "no-budget",
	})
	if err != nil || !res.Allowed {
		t.Fatalf("no enforcing budget must admit: %+v err=%v", res, err)
	}
}

var _ interface {
	Reserve(context.Context, model.TenantID, AdmissionRequest) (Reservation, error)
	Commit(context.Context, model.TenantID, string, int64) error
	Release(context.Context, model.TenantID, string) error
	ReconcileReservations(context.Context, model.TenantID) (AdmissionReconciliation, error)
} = (*Module)(nil)
