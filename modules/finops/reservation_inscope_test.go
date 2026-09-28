// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// -----------------------------------------------------------------------------
// RESERVING INSIDE A CALLER'S TRANSACTION. An admission creates both components of one
// hold in the same transaction that proves it still owns its key, so the ledger's
// reserve has to run inside a transaction it does not own: the caller's rollback is
// its rollback, the caller's instant is its instant, the caller's identity is the
// identity of every row, and a lost seq race comes back typed, so the caller can tell
// it from a lost key.
//
// The fixture: budget B, global, monthly, block, 20 000 000 µUSD; spend limit S, daily,
// actor "a", 10 000 000 µUSD. B sums every sample; S sums actor a's.
// -----------------------------------------------------------------------------

// seedBudgetAndSeatLimit creates B and S and returns their ids.
func seedBudgetAndSeatLimit(t *testing.T, st store.Store, tenant model.TenantID) (budget, limit model.ID) {
	t.Helper()
	budget = createBudget(t, st, tenant, "B", budgetSpec{
		Dimension: "global", Period: "monthly", LimitMicroUSD: 20 * oneUSD, Action: "block",
	})
	if err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		p, err := sc.Policies().Create(context.Background(), model.Policy{
			Name: "S", Kind: policyKindSpendLimit, Enabled: true,
			Spec: map[string]any{
				"scope_type": "user", "scope_key": "a",
				"amount_micro_usd": int64(10 * oneUSD), "period": "daily",
			},
		})
		limit = p.ID
		return err
	}); err != nil {
		t.Fatalf("create the spend limit: %v", err)
	}
	return budget, limit
}

// admissionPhases returns the budget phase and the spend phase of a request by actor,
// as the module builds them before a create transaction opens.
func admissionPhases(t *testing.T, m *Module, tenant model.TenantID, actor string, now time.Time) (budgets, seats []reservationTarget) {
	t.Helper()
	ctx := context.Background()
	budgets, truncated, err := m.budgetTargets(ctx, tenant, SpendDims{}, now)
	if err != nil || truncated {
		t.Fatalf("budget targets: truncated=%v err=%v", truncated, err)
	}
	seats, err = m.spendLimitTargets(ctx, tenant, actor, nil, now)
	if err != nil {
		t.Fatalf("spend-limit targets: %v", err)
	}
	return budgets, seats
}

// inCallerTx runs fn in one transaction of m's data handle the way every caller of
// reserveInScope does: the writer lock, then the activation frontier, then fn.
func inCallerTx(ctx context.Context, m *Module, tenant model.TenantID, fn func(store.Scope) error) error {
	return m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		if err := lockFinOpsWriter(ctx, sc); err != nil {
			return err
		}
		if err := guardLegacyReservationMutation(ctx, sc); err != nil {
			return err
		}
		return fn(sc)
	})
}

// errCallerStepFailed stands for the caller's own write failing after its reserve
// succeeded — the one admission-row update of a create whose fence has moved.
var errCallerStepFailed = errors.New("finops-test: the caller's own write failed")

// TestReserveInScopeRollsBackWithCaller is what the ledger can promise a refused
// admission: a denial creates nothing. The spend phase refuses AFTER the budget phase
// inserted its row in the same transaction; the caller rolls back, and the row goes
// with everything else. A caller whose own later step fails rolls the holds back the
// same way. The control commits, and its row stays.
func TestReserveInScopeRollsBackWithCaller(t *testing.T) {
	forEachAdmissionEngine(t, runReserveInScopeRollsBackWithCaller)
}

func runReserveInScopeRollsBackWithCaller(t *testing.T, cfg store.Config) {
	m, st, tenant, _ := openFinCfg(t, cfg)
	m.clock = &fakeClock{t: baseTime}
	ctx := context.Background()
	now := baseTime
	_, limit := seedBudgetAndSeatLimit(t, st, tenant)
	spent := mkCost("anthropic", "model", "s1", 1, 1, 10*oneUSD, now)
	spent.Actor = "a"
	m.ingest(t, tenant, spent)
	budgets, seats := admissionPhases(t, m, tenant, "a", now)
	if len(budgets) != 1 || len(seats) != 1 {
		t.Fatalf("the fixture built %d budget and %d spend-limit targets, want one of each", len(budgets), len(seats))
	}

	// 1. B admits (12 M of 20 M) and inserts; S refuses (12 M of 10 M). Rolled back.
	h := newHoldID()
	var budgetPhase, seatPhase reserveOutcome
	inside := -1
	err := inCallerTx(ctx, m, tenant, func(sc store.Scope) error {
		var perr error
		if budgetPhase, perr = reserveInScope(ctx, sc, budgets, 2*oneUSD, now, h); perr != nil {
			return perr
		}
		rows, rerr := rowsUnderHold(ctx, sc, h)
		if rerr != nil {
			return rerr
		}
		inside = len(rows)
		seatPhase, perr = reserveInScope(ctx, sc, seats, 2*oneUSD, now, h)
		return perr
	})
	if !errors.Is(err, errReservationDenied) {
		t.Fatalf("the spend phase's refusal did not come back as the denial the caller rolls back on: %v", err)
	}
	if !budgetPhase.result.Allowed || budgetPhase.inserted != 1 || inside != 1 {
		t.Fatalf("fixture: the budget phase must admit and insert its row under the hold first: %+v inside=%d", budgetPhase, inside)
	}
	if seatPhase.result.Allowed || !seatPhase.decided || seatPhase.result.Action != "block" ||
		seatPhase.result.BudgetID != limit.String() {
		t.Fatalf("the spend phase did not refuse on S: %+v", seatPhase)
	}
	if rows := ledgerRowsUnder(t, st, tenant, h); len(rows) != 0 {
		t.Fatalf("a denied create left %d row(s) under its hold: the budget phase's row survived the rollback", len(rows))
	}

	// 2. Both phases would admit for this actor, and the caller's own step fails.
	h2 := newHoldID()
	budgetsC, seatsC := admissionPhases(t, m, tenant, "c", now)
	err = inCallerTx(ctx, m, tenant, func(sc store.Scope) error {
		for _, phase := range [][]reservationTarget{budgetsC, seatsC} {
			if _, perr := reserveInScope(ctx, sc, phase, 2*oneUSD, now, h2); perr != nil {
				return perr
			}
		}
		return errCallerStepFailed
	})
	if !errors.Is(err, errCallerStepFailed) {
		t.Fatalf("the caller's failure did not come back: %v", err)
	}
	if rows := ledgerRowsUnder(t, st, tenant, h2); len(rows) != 0 {
		t.Fatalf("a create whose caller failed left %d row(s) under its hold", len(rows))
	}
	if got := ledgerCounts(t, st, tenant, now); got != (ledgerCount{}) {
		t.Fatalf("two rolled-back creates left a ledger: %+v", got)
	}

	// 3. Control: actor c has no spend limit, the caller commits, and B's row stays.
	h3 := newHoldID()
	var control reserveOutcome
	if err := inCallerTx(ctx, m, tenant, func(sc store.Scope) error {
		var perr error
		control, perr = reserveInScope(ctx, sc, budgetsC, 2*oneUSD, now, h3)
		if perr != nil {
			return perr
		}
		_, perr = reserveInScope(ctx, sc, seatsC, 2*oneUSD, now, h3)
		return perr
	}); err != nil {
		t.Fatalf("control: %v", err)
	}
	rows := activeRowsUnder(t, st, tenant, h3)
	if len(rows) != 1 || rows[0].String(colResvPolicyKind) != policyKindBudget || rows[0].Int(colResvAmount) != 2*oneUSD {
		t.Fatalf("control: a committed create must leave exactly B's row under its hold, got %d", len(rows))
	}
	if control.result.Handle != h3.String() {
		t.Fatalf("control: the create answered handle %q, want its caller's identity %q", control.result.Handle, h3)
	}
}

// TestZeroReserveInsertsNoRow is the zero-hold rule. A hold of zero withholds nothing
// from anyone, so it inserts no row and issues no handle — and it is still a real
// question: every target is evaluated, and a cap already past its limit refuses it
// (here: B with a 5 000 000 µUSD limit, no spend limit, no actor, estimate 0).
func TestZeroReserveInsertsNoRow(t *testing.T) {
	forEachAdmissionEngine(t, runZeroReserveInsertsNoRow)
}

func runZeroReserveInsertsNoRow(t *testing.T, cfg store.Config) {
	m, st, tenant, _ := openFinCfg(t, cfg)
	m.clock = &fakeClock{t: baseTime}
	ctx := context.Background()
	now := baseTime
	budget := createBudget(t, st, tenant, "B", budgetSpec{
		Dimension: "global", Period: "monthly", LimitMicroUSD: 5 * oneUSD, Action: "block",
	})
	evaluate := func(t *testing.T, estimate int64) (reserveOutcome, holdID, error) {
		t.Helper()
		targets, truncated, err := m.budgetTargets(ctx, tenant, SpendDims{}, now)
		if err != nil || truncated || len(targets) != 1 {
			t.Fatalf("targets: %d truncated=%v err=%v", len(targets), truncated, err)
		}
		h := newHoldID()
		var out reserveOutcome
		err = inCallerTx(ctx, m, tenant, func(sc store.Scope) error {
			var perr error
			out, perr = reserveInScope(ctx, sc, targets, estimate, now, h)
			return perr
		})
		return out, h, err
	}
	holdsNothing := func(t *testing.T, what string, out reserveOutcome, h holdID) {
		t.Helper()
		if out.inserted != 0 || out.result.Handle != "" {
			t.Fatalf("%s: a hold of zero inserted %d row(s) and was handed %q", what, out.inserted, out.result.Handle)
		}
		if rows := ledgerRowsUnder(t, st, tenant, h); len(rows) != 0 {
			t.Fatalf("%s: %d row(s) under a hold of zero", what, len(rows))
		}
	}

	// 1. Under headroom: allowed, and nothing held.
	out, h, err := evaluate(t, 0)
	if err != nil || !out.result.Allowed {
		t.Fatalf("a zero reserve under headroom: %+v err=%v", out, err)
	}
	holdsNothing(t, "under headroom", out, h)

	// 2. Spend 1 M of 5 M, asked again: allowed on the ledger's answer, nothing held.
	m.ingest(t, tenant, mkCost("anthropic", "model", "s1", 1, 1, oneUSD, now))
	out, h, err = evaluate(t, 0)
	if err != nil || !out.result.Allowed {
		t.Fatalf("a zero reserve with 4 M of 5 M left was refused: %+v err=%v", out, err)
	}
	holdsNothing(t, "with headroom left", out, h)
	// The ledger's own reserve keeps the same rule.
	if res, err := m.ReserveBudget(ctx, tenant, SpendDims{}, 0); err != nil || !res.Allowed || res.Handle != "" {
		t.Fatalf("ReserveBudget of zero: %+v err=%v; want allowed with no handle", res, err)
	}
	if n := len(countReservations(t, st, tenant)); n != 0 {
		t.Fatalf("ReserveBudget of zero left %d row(s)", n)
	}

	// 3. Control: an amount is held, under the caller's identity.
	held, hh, err := evaluate(t, oneUSD)
	if err != nil || !held.result.Allowed || held.inserted != 1 || held.result.Handle != hh.String() {
		t.Fatalf("control: a 1 M reserve under headroom: %+v err=%v", held, err)
	}
	if rows := activeRowsUnder(t, st, tenant, hh); len(rows) != 1 || rows[0].Int(colResvAmount) != oneUSD {
		t.Fatalf("control: %d active row(s) under the held identity, want one of 1 M", len(rows))
	}

	// 4. Spend 10 M of 5 M. The zero reserve is evaluated and refused, still holding
	//    nothing.
	m.ingest(t, tenant, mkCost("anthropic", "model", "s2", 1, 1, 9*oneUSD, now))
	out, h, err = evaluate(t, 0)
	if !errors.Is(err, errReservationDenied) {
		t.Fatalf("a zero reserve over a blown cap did not refuse: %+v err=%v", out, err)
	}
	if out.result.Allowed || !out.decided || out.result.Action != "block" || out.result.BudgetID != budget.String() {
		t.Fatalf("the refusal does not name B: %+v", out)
	}
	holdsNothing(t, "over a blown cap", out, h)
	if got := ledgerCounts(t, st, tenant, now); got != (ledgerCount{Active: 1}) {
		t.Fatalf("the ledger holds %+v, want only the control's row", got)
	}
}

// TestReserveInScopeTypesSeqRace: two creates reach the ledger, and k1's first insert
// loses the seq race; the failure comes back as errSeqRace and NOT as store.ErrConflict,
// which is what a moved fence looks like, so a caller cannot take a seq race for a lost
// key. k1's create is repeated at the same identity and k2 creates its own: two holds,
// each with both components.
func TestReserveInScopeTypesSeqRace(t *testing.T) {
	forEachAdmissionEngine(t, runReserveInScopeTypesSeqRace)
}

func runReserveInScopeTypesSeqRace(t *testing.T, cfg store.Config) {
	m, st, tenant, _ := openFinCfg(t, cfg)
	m.clock = &fakeClock{t: baseTime}
	ctx := context.Background()
	now := baseTime
	seedBudgetAndSeatLimit(t, st, tenant)
	budgets, seats := admissionPhases(t, m, tenant, "a", now)

	// An amount with no identity is refused, typed, before anything is inserted: its rows
	// would name no hold, and money no identity names cannot be settled.
	var anonymous reserveOutcome
	err := inCallerTx(ctx, m, tenant, func(sc store.Scope) error {
		var perr error
		anonymous, perr = reserveInScope(ctx, sc, budgets, 2*oneUSD, now, "")
		return perr
	})
	if !errors.Is(err, errHoldIdentityRequired) || anonymous.inserted != 0 {
		t.Fatalf("a hold with an amount and no identity: inserted %d err=%v; want errHoldIdentityRequired", anonymous.inserted, err)
	}
	if n := len(countReservations(t, st, tenant)); n != 0 {
		t.Fatalf("a refused anonymous hold left %d row(s)", n)
	}

	base := m.data
	m.data = conflictOnInsert(base, 1)

	create := func(h holdID) (int, error) {
		inserted := 0
		err := inCallerTx(ctx, m, tenant, func(sc store.Scope) error {
			for _, phase := range [][]reservationTarget{budgets, seats} {
				out, perr := reserveInScope(ctx, sc, phase, 2*oneUSD, now, h)
				inserted += out.inserted
				if perr != nil {
					return perr
				}
			}
			return nil
		})
		return inserted, err
	}

	k1, k2 := newHoldID(), newHoldID()
	_, err = create(k1)
	if !errors.Is(err, errSeqRace) {
		t.Fatalf("the injected collision came back as %v, want errSeqRace", err)
	}
	if errors.Is(err, store.ErrConflict) {
		t.Fatalf("a seq race also reads as store.ErrConflict, a moved fence: %v", err)
	}
	if rows := ledgerRowsUnder(t, st, tenant, k1); len(rows) != 0 {
		t.Fatalf("the collided create left %d row(s)", len(rows))
	}
	for _, h := range []holdID{k1, k2} {
		if n, err := create(h); err != nil || n != 2 {
			t.Fatalf("create under %s: inserted %d err=%v; want both components", h, n, err)
		}
		rows := activeRowsUnder(t, st, tenant, h)
		kinds := map[string]int{}
		for _, r := range rows {
			kinds[r.String(colResvPolicyKind)]++
		}
		if len(rows) != 2 || kinds[policyKindBudget] != 1 || kinds[policyKindSpendLimit] != 1 {
			t.Fatalf("hold %s has %d active row(s) %v, want one b and one s", h, len(rows), kinds)
		}
	}
	if got := ledgerCounts(t, st, tenant, now); got != (ledgerCount{Active: 4}) {
		t.Fatalf("two holds of two components each: the ledger holds %+v", got)
	}

	// The ledger's own reserve still retries a lost seq race and admits.
	m.data = conflictOnInsert(base, 1)
	res, err := m.ReserveBudget(ctx, tenant, SpendDims{}, oneUSD)
	if err != nil || !res.Allowed || res.Handle == "" {
		t.Fatalf("ReserveBudget after one lost seq race: %+v err=%v", res, err)
	}
	if rows := activeRowsUnder(t, st, tenant, holdID(res.Handle)); len(rows) != 1 {
		t.Fatalf("ReserveBudget's retry left %d row(s) under its handle, want 1", len(rows))
	}
}

// TestComponentsShareOneExpiry: the budget and spend-limit rows of one hold expire
// together. The in-scope reserve reads no clock: every row it inserts expires at the
// instant its caller hands it plus the TTL, so a caller that hands both phases one
// instant, as this one does, gets one expiry for the whole hold. The control hands the
// two phases of a second hold two instants and gets two expiries, which shows that the
// expiry comes from the caller's instant and from nothing else.
func TestComponentsShareOneExpiry(t *testing.T) {
	forEachAdmissionEngine(t, runComponentsShareOneExpiry)
}

func runComponentsShareOneExpiry(t *testing.T, cfg store.Config) {
	m, st, tenant, _ := openFinCfg(t, cfg)
	m.clock = &fakeClock{t: baseTime}
	ctx := context.Background()
	now := baseTime
	seedBudgetAndSeatLimit(t, st, tenant)
	budgets, seats := admissionPhases(t, m, tenant, "a", now)

	// create reserves the budget phase at budgetAt and the spend phase at seatAt, in one
	// transaction, and returns each component's expiry.
	create := func(h holdID, budgetAt, seatAt time.Time) map[string]string {
		t.Helper()
		if err := inCallerTx(ctx, m, tenant, func(sc store.Scope) error {
			if _, err := reserveInScope(ctx, sc, budgets, 2*oneUSD, budgetAt, h); err != nil {
				return err
			}
			_, err := reserveInScope(ctx, sc, seats, 2*oneUSD, seatAt, h)
			return err
		}); err != nil {
			t.Fatalf("create under %s: %v", h, err)
		}
		rows := activeRowsUnder(t, st, tenant, h)
		if len(rows) != 2 {
			t.Fatalf("%d active row(s) under %s, want b and s", len(rows), h)
		}
		expiries := make(map[string]string, len(rows))
		for _, r := range rows {
			expiries[r.String(colResvPolicyKind)] = r.String(colResvExpiresAt)
		}
		return expiries
	}
	expiry := func(at time.Time) string { return model.NewTimestamp(at.Add(reservationTTL)).String() }

	one := create(newHoldID(), now, now)
	for kind, got := range one {
		if got != expiry(now) {
			t.Errorf("the %s row expires at %s, want %s: one hold, one instant, one expiry", kind, got, expiry(now))
		}
	}

	later := now.Add(7 * time.Second)
	two := create(newHoldID(), now, later)
	if two[policyKindBudget] != expiry(now) || two[policyKindSpendLimit] != expiry(later) {
		t.Errorf("control: phases handed %s and %s expire at %v; want each at its own instant plus the TTL",
			now, later, two)
	}
}
