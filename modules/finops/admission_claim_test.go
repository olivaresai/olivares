// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// -----------------------------------------------------------------------------
// THE CLAIM AND THE CREATE. A caller names its hold in the claim before any ledger row
// exists, and creates the hold's rows only in a transaction that finds the claim still
// its own; a create that finds the key moved writes nothing and is not tried again.
// -----------------------------------------------------------------------------

// stageClaim writes, through the real claim, the row a caller stages before it
// evaluates the budgets — owing the holds in owed — and stops there: what a crash
// between the two leaves behind. It returns the identity the claim names.
func stageClaim(t *testing.T, m *Module, tenant model.TenantID, req AdmissionRequest, owed owedHolds) holdID {
	t.Helper()
	h := newHoldID()
	row := admissionRow{
		key: req.IdempotencyKey, payloadHash: admissionPayloadHash(req), scope: req.Scope,
		estimate: req.EstimateMicroUSD, state: admStatePending, stateAt: m.clock.Now(), handle: h, owed: owed,
	}
	if _, w, err := m.claim(context.Background(), tenant, row); w != writeCommitted || err != nil {
		t.Fatalf("stage the claim: %v", err)
	}
	return h
}

// stagePendingClaim stages a claim that owes nothing.
func stagePendingClaim(t *testing.T, m *Module, tenant model.TenantID, req AdmissionRequest) holdID {
	t.Helper()
	return stageClaim(t, m, tenant, req, nil)
}

// admissionRowOf reads the row of key.
func admissionRowOf(t *testing.T, m *Module, tenant model.TenantID, key string) admissionRow {
	t.Helper()
	row, found, err := m.readRow(context.Background(), tenant, key)
	if err != nil || !found {
		t.Fatalf("the row of %q: found=%v err=%v", key, found, err)
	}
	return row
}

// admissionRecordFor builds a row for req's key as an earlier build left it: the
// request's payload hash, scope and estimate, and the given state, slots and date.
func admissionRecordFor(req AdmissionRequest, state string, handle, spend holdID, stateAt time.Time) model.Record {
	rec := admissionRecord(req.IdempotencyKey, state, handle, spend, stateAt)
	rec[colAdmPayloadHash] = admissionPayloadHash(req)
	rec[colAdmScope] = req.Scope
	rec[colAdmEstimate] = req.EstimateMicroUSD
	return rec
}

// setStoredCell overwrites one stored column of the row of key, as a corruption would.
func setStoredCell(t *testing.T, st store.Store, tenant model.TenantID, key, col string, value any) {
	t.Helper()
	if err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(admissionIdempotencyKind)
		if err != nil {
			return err
		}
		recs, _, err := repo.List(context.Background(), model.Query{Filters: []model.Filter{eq(colAdmKey, key)}, Limit: 2})
		if err != nil {
			return err
		}
		if len(recs) != 1 {
			t.Fatalf("fixture: %d rows for %q", len(recs), key)
		}
		recs[0][col] = value
		_, err = repo.Update(context.Background(), recs[0])
		return err
	}); err != nil {
		t.Fatalf("overwrite %s of %q: %v", col, key, err)
	}
}

// seatRequest is a request of actor "a" for 2 000 000 µUSD under key, which the budget
// and seat limit of seedBudgetAndSeatLimit hold as two components of one hold.
func seatRequest(key string) AdmissionRequest {
	return AdmissionRequest{Scope: AdmissionScopeModelGateway, ActorRef: "a", EstimateMicroUSD: 2 * oneUSD, IdempotencyKey: key}
}

// componentsOf counts the rows of rows by policy kind.
func componentsOf(rows []model.Record) map[string]int {
	out := map[string]int{}
	for _, r := range rows {
		out[r.String(colResvPolicyKind)]++
	}
	return out
}

// TestClaimNamesHoldBeforeLedgerRow: between the claim and the create, the row already
// names the hold and no ledger row exists; the create then puts both components under
// exactly that identity, and the caller is answered with it.
func TestClaimNamesHoldBeforeLedgerRow(t *testing.T) {
	forEachAdmissionEngine(t, runClaimNamesHoldBeforeLedgerRow)
}

func runClaimNamesHoldBeforeLedgerRow(t *testing.T, cfg store.Config) {
	m, st, tenant, _ := openFinCfg(t, cfg)
	m.clock = &fakeClock{t: baseTime}
	seedBudgetAndSeatLimit(t, st, tenant)
	req := seatRequest("model_gateway/claimed-first")
	d := &pausedMutateData{ModuleData: m.data, nth: 2, reached: make(chan struct{}), resume: make(chan struct{})}
	m.data = d
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	done := make(chan pausedReserveOutcome, 1)
	go func() {
		r, e := m.Reserve(pausedCtx(ctx), tenant, req)
		done <- pausedReserveOutcome{r, e}
	}()
	awaitPausedMutate(t, d)
	row := admissionRowOf(t, m, tenant, req.IdempotencyKey)
	intent := row.handle
	if row.state != admStatePending || intent.isZero() || len(row.owed) != 0 {
		close(d.resume)
		t.Fatalf("before the create the row must be a claim naming its hold: %+v", row)
	}
	if n := len(countReservations(t, st, tenant)); n != 0 {
		close(d.resume)
		t.Fatalf("%d ledger row(s) exist before the create", n)
	}
	close(d.resume)
	got := <-done
	if got.err != nil || !got.res.Allowed || got.res.Handle != intent.String() {
		t.Fatalf("the caller was answered %+v err=%v, want the hold its claim named (%s)", got.res, got.err, intent)
	}
	rows := activeRowsUnder(t, st, tenant, intent)
	kinds := componentsOf(rows)
	if len(rows) != 2 || kinds[policyKindBudget] != 1 || kinds[policyKindSpendLimit] != 1 {
		t.Fatalf("the create put %d row(s) %v under the claimed hold, want one budget and one spend-limit row", len(rows), kinds)
	}
	if n := len(countReservations(t, st, tenant)); n != 2 {
		t.Fatalf("%d ledger rows in all, want only the two under the claimed hold", n)
	}
}

// TestPausedClaimantCannotCreate: a claimant paused between its claim and its create
// loses the key to a caller that takes the stale claim over. Resumed, its create finds
// the key moved and inserts nothing; it is answered with the successor's hold.
func TestPausedClaimantCannotCreate(t *testing.T) {
	forEachAdmissionEngine(t, runPausedClaimantCannotCreate)
}

func runPausedClaimantCannotCreate(t *testing.T, cfg store.Config) {
	m, st, tenant, _ := openFinCfg(t, cfg)
	clk := &fakeClock{t: baseTime}
	m.clock = clk
	seedBudgetAndSeatLimit(t, st, tenant)
	req := seatRequest("model_gateway/paused-claimant")
	d := &pausedMutateData{ModuleData: m.data, nth: 2, reached: make(chan struct{}), resume: make(chan struct{})}
	m.data = d
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	done := make(chan pausedReserveOutcome, 1)
	go func() {
		r, e := m.Reserve(pausedCtx(ctx), tenant, req)
		done <- pausedReserveOutcome{r, e}
	}()
	awaitPausedMutate(t, d)
	intent := admissionRowOf(t, m, tenant, req.IdempotencyKey).handle
	clk.advance(admissionClaimTakeover + time.Second)
	successor, err := m.Reserve(ctx, tenant, req)
	close(d.resume)
	delayed := <-done

	if err != nil || !successor.Allowed || successor.Handle == "" || successor.Handle == intent.String() {
		t.Fatalf("the successor must be admitted under a hold of its own: %+v err=%v", successor, err)
	}
	if delayed.err != nil || !delayed.res.Allowed || !delayed.res.Replayed || delayed.res.Handle != successor.Handle {
		t.Fatalf("the paused claimant was answered %+v err=%v, want a replay of %s", delayed.res, delayed.err, successor.Handle)
	}
	row := admissionRowOf(t, m, tenant, req.IdempotencyKey)
	if row.state != admStateReserved || row.handle.String() != successor.Handle || len(row.owed) != 0 {
		t.Fatalf("the row is %+v, want the successor's admission owing nothing", row)
	}
	if rows := ledgerRowsUnder(t, st, tenant, intent); len(rows) != 0 {
		t.Fatalf("the paused claimant created %d row(s) under a claim it no longer held", len(rows))
	}
	if got := ledgerCounts(t, st, tenant, clk.t); got != (ledgerCount{Active: 2}) {
		t.Fatalf("the ledger holds %+v, want only the successor's two components", got)
	}
}

// TestFenceRefusalIsNotRetried separates the two failures a create can meet. A moved key
// is final: the claimant writes nothing more, and its create is not tried again. A lost
// seq race is not a moved key: the create is tried again under the same claim, and the
// key is published once, with both components.
func TestFenceRefusalIsNotRetried(t *testing.T) {
	forEachAdmissionEngine(t, runFenceRefusalIsNotRetried)
}

func runFenceRefusalIsNotRetried(t *testing.T, cfg store.Config) {
	t.Run("a moved key", func(t *testing.T) {
		m, st, tenant, _ := openFinCfg(t, cfg)
		clk := &fakeClock{t: baseTime}
		m.clock = clk
		seedBudgetAndSeatLimit(t, st, tenant)
		req := seatRequest("model_gateway/moved-key")
		d := &pausedMutateData{ModuleData: m.data, nth: 2, reached: make(chan struct{}), resume: make(chan struct{})}
		m.data = d
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		done := make(chan pausedReserveOutcome, 1)
		go func() {
			r, e := m.Reserve(pausedCtx(ctx), tenant, req)
			done <- pausedReserveOutcome{r, e}
		}()
		awaitPausedMutate(t, d)
		clk.advance(admissionClaimTakeover + time.Second)
		if successor, err := m.Reserve(ctx, tenant, req); err != nil || !successor.Allowed {
			close(d.resume)
			t.Fatalf("the successor: %+v err=%v", successor, err)
		}
		close(d.resume)
		if delayed := <-done; delayed.err != nil || !delayed.res.Replayed {
			t.Fatalf("the claimant whose key moved: %+v err=%v", delayed.res, delayed.err)
		}
		if n := d.writes.Load(); n != 2 {
			t.Fatalf("the claimant wrote %d times, want its claim and the one create the fence refused", n)
		}
	})

	t.Run("a lost seq race", func(t *testing.T) {
		m, st, tenant, _ := openFinCfg(t, cfg)
		m.clock = &fakeClock{t: baseTime}
		ctx := context.Background()
		seedBudgetAndSeatLimit(t, st, tenant)
		m.data = conflictOnInsert(m.data, 1)
		k1, err := m.Reserve(ctx, tenant, seatRequest("model_gateway/k1"))
		if err != nil || !k1.Allowed || k1.Handle == "" {
			t.Fatalf("k1, whose first insert lost the seq race: %+v err=%v", k1, err)
		}
		if row := admissionRowOf(t, m, tenant, "model_gateway/k1"); row.version != 3 || row.handle.String() != k1.Handle {
			t.Fatalf("k1's row is at version %d naming %q, want one claim, one create and one publication", row.version, row.handle)
		}
		k2, err := m.Reserve(ctx, tenant, seatRequest("model_gateway/k2"))
		if err != nil || !k2.Allowed || k2.Handle == "" || k2.Handle == k1.Handle {
			t.Fatalf("k2: %+v err=%v", k2, err)
		}
		for _, h := range []string{k1.Handle, k2.Handle} {
			rows := activeRowsUnder(t, st, tenant, holdID(h))
			if kinds := componentsOf(rows); len(rows) != 2 || kinds[policyKindBudget] != 1 || kinds[policyKindSpendLimit] != 1 {
				t.Fatalf("hold %s has %d row(s) %v, want one of each component", h, len(rows), kinds)
			}
		}
		if got := ledgerCounts(t, st, tenant, baseTime); got != (ledgerCount{Active: 4}) {
			t.Fatalf("two holds of two components each: the ledger holds %+v", got)
		}
	})
}

// twiceData runs every write callback twice in one transaction, as a store adapter may,
// and returns the second run's error.
type twiceData struct{ api.ModuleData }

func (d twiceData) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	return d.ModuleData.Mutate(ctx, tenant, func(sc store.Scope) error {
		if err := fn(sc); err != nil {
			return err
		}
		return fn(sc)
	})
}

// TestMutateClassifiedResetsDone pins how a write transaction's end is classified: a
// failure while the callback is unfinished changed nothing, and one after it finished may
// have committed. An adapter that runs the callback twice must not carry the first run's
// "finished" into a second run that fails.
func TestMutateClassifiedResetsDone(t *testing.T) {
	forEachAdmissionEngine(t, runMutateClassifiedResetsDone)
}

func runMutateClassifiedResetsDone(t *testing.T, cfg store.Config) {
	m, _, tenant, _ := openFinCfg(t, cfg)
	ctx := context.Background()
	base := m.data

	runs := 0
	secondFailed := errors.New("finops-test: the second run failed")
	m.data = twiceData{ModuleData: base}
	w, err := m.mutateClassified(ctx, tenant, func(store.Scope) error {
		runs++
		if runs == 2 {
			return secondFailed
		}
		return nil
	})
	if runs != 2 || w != writeRolledBack || !errors.Is(err, secondFailed) {
		t.Fatalf("a second run that failed unfinished: runs=%d outcome=%v err=%v; want a known rollback", runs, w, err)
	}

	for _, tc := range []struct {
		name string
		data api.ModuleData
		fn   func(store.Scope) error
		want writeOutcome
	}{
		{"the transaction never opens", &faultData{ModuleData: base, nth: 1, mode: faultBeforeCallback},
			func(store.Scope) error { return nil }, writeRolledBack},
		{"the callback finishes and the commit's acknowledgment is lost",
			&faultData{ModuleData: base, nth: 1, mode: faultAfterCommit},
			func(store.Scope) error { return nil }, writeUncertain},
		{"the callback finishes and the transaction rolls back",
			&faultData{ModuleData: base, nth: 1, mode: faultAfterRollback},
			func(store.Scope) error { return nil }, writeUncertain},
		{"the callback fails on its own", base, func(store.Scope) error { return errCallerStepFailed }, writeRolledBack},
		{"the transaction commits", base, func(store.Scope) error { return nil }, writeCommitted},
	} {
		m.data = tc.data
		if w, err := m.mutateClassified(pausedCtx(ctx), tenant, tc.fn); w != tc.want {
			t.Errorf("%s: outcome=%v err=%v, want %v", tc.name, w, err, tc.want)
		}
	}
}

// TestSpendDenialCreatesNoRow: the budget admits and the seat limit refuses, and the
// refusal leaves nothing behind — no row under the claim's hold, no row at all — because
// both components are created in one transaction that the refusal rolls back. The key is
// released and the refusal says it came from a spend limit. The control, an actor with no
// seat limit, gets its one budget row.
func TestSpendDenialCreatesNoRow(t *testing.T) {
	forEachAdmissionEngine(t, runSpendDenialCreatesNoRow)
}

func runSpendDenialCreatesNoRow(t *testing.T, cfg store.Config) {
	m, st, tenant, _ := openFinCfg(t, cfg)
	m.clock = &fakeClock{t: baseTime}
	ctx := context.Background()
	_, limit := seedBudgetAndSeatLimit(t, st, tenant)
	spent := mkCost("anthropic", "model", "s1", 1, 1, 10*oneUSD, baseTime)
	spent.Actor = "a"
	m.ingest(t, tenant, spent)

	req := seatRequest("model_gateway/seat-over")
	res, err := m.Reserve(ctx, tenant, req)
	if err != nil || res.Allowed || !res.SpendLimit || res.Action != "block" || res.BudgetID != limit.String() {
		t.Fatalf("the seat limit must refuse, marked as one: %+v err=%v", res, err)
	}
	row := admissionRowOf(t, m, tenant, req.IdempotencyKey)
	if row.state != admStateReleased || !row.handle.isZero() || len(row.owed) != 0 {
		t.Fatalf("the refused key's row is %+v, want released naming nothing", row)
	}
	if n := len(countReservations(t, st, tenant)); n != 0 {
		t.Fatalf("a refused create left %d ledger row(s)", n)
	}
	if n := countAuditAction(t, st, tenant, auditActionAdmissionDenied); n != 1 {
		t.Fatalf("%d refusal(s) recorded, want 1", n)
	}

	ctrl, err := m.Reserve(ctx, tenant, AdmissionRequest{
		Scope: AdmissionScopeModelGateway, ActorRef: "c", EstimateMicroUSD: 2 * oneUSD,
		IdempotencyKey: "model_gateway/actor-c",
	})
	if err != nil || !ctrl.Allowed || ctrl.Handle == "" || ctrl.SpendLimit {
		t.Fatalf("control: an actor with no seat limit must be admitted: %+v err=%v", ctrl, err)
	}
	if rows := activeRowsUnder(t, st, tenant, holdID(ctrl.Handle)); len(rows) != 1 || rows[0].String(colResvPolicyKind) != policyKindBudget {
		t.Fatalf("control: %d row(s) under the admitted hold, want its one budget row", len(rows))
	}
}

// TestOwedHoldsBoundedAtSixteen: a takeover that would owe a seventeenth hold is refused
// deny-closed and writes nothing — the row keeps its version, its claim and its list. The
// control owes fifteen: the takeover owes sixteen, the create settles them all, and the
// key is published owing nothing.
func TestOwedHoldsBoundedAtSixteen(t *testing.T) {
	forEachAdmissionEngine(t, runOwedHoldsBoundedAtSixteen)
}

func runOwedHoldsBoundedAtSixteen(t *testing.T, cfg store.Config) {
	m, st, tenant, _ := openFinCfg(t, cfg)
	clk := &fakeClock{t: baseTime}
	m.clock = clk
	ctx := context.Background()
	createBudget(t, st, tenant, "B", budgetSpec{Dimension: "global", Period: "monthly", LimitMicroUSD: 20 * oneUSD, Action: "block"})
	owing := func(n int) owedHolds {
		out := make(owedHolds, n)
		for i := range out {
			out[i] = newHoldID()
		}
		return out
	}
	full := AdmissionRequest{Scope: AdmissionScopeModelGateway, EstimateMicroUSD: oneUSD, IdempotencyKey: "model_gateway/owes-sixteen"}
	room := AdmissionRequest{Scope: AdmissionScopeModelGateway, EstimateMicroUSD: oneUSD, IdempotencyKey: "model_gateway/owes-fifteen"}
	stageClaim(t, m, tenant, full, owing(maxOwedHolds))
	stageClaim(t, m, tenant, room, owing(maxOwedHolds-1))
	clk.advance(admissionClaimTakeover + time.Second)

	before := admissionRowOf(t, m, tenant, full.IdempotencyKey)
	res, err := m.Reserve(ctx, tenant, full)
	if err != nil || res.Allowed || res.Reason != ReasonStoreUnreachable {
		t.Fatalf("a takeover past sixteen owed holds must refuse deny-closed: %+v err=%v", res, err)
	}
	after := admissionRowOf(t, m, tenant, full.IdempotencyKey)
	if after.version != before.version || after.handle != before.handle || len(after.owed) != maxOwedHolds {
		t.Fatalf("the refused takeover wrote the row: before %+v, after %+v", before, after)
	}
	if n := len(countReservations(t, st, tenant)); n != 0 {
		t.Fatalf("the refused takeover left %d ledger row(s)", n)
	}

	ok, err := m.Reserve(ctx, tenant, room)
	if err != nil || !ok.Allowed || ok.Handle == "" {
		t.Fatalf("control: a takeover owing sixteen must go through: %+v err=%v", ok, err)
	}
	if row := admissionRowOf(t, m, tenant, room.IdempotencyKey); row.state != admStateReserved || len(row.owed) != 0 {
		t.Fatalf("control: the row is %+v, want published owing nothing", row)
	}
}

// TestUndecodableRowRefusesKey: a row whose owed list does not decode is refused
// deny-closed whatever its state or age, and it is not written: its version and its
// stored text stay as they were.
func TestUndecodableRowRefusesKey(t *testing.T) {
	forEachAdmissionEngine(t, runUndecodableRowRefusesKey)
}

func runUndecodableRowRefusesKey(t *testing.T, cfg store.Config) {
	m, st, tenant, _ := openFinCfg(t, cfg)
	clk := &fakeClock{t: baseTime}
	m.clock = clk
	ctx := context.Background()
	createBudget(t, st, tenant, "B", budgetSpec{Dimension: "global", Period: "monthly", LimitMicroUSD: 20 * oneUSD, Action: "block"})
	const garbled = `["not an identity"`
	stale := AdmissionRequest{Scope: AdmissionScopeModelGateway, EstimateMicroUSD: oneUSD, IdempotencyKey: "model_gateway/garbled-stale"}
	young := AdmissionRequest{Scope: AdmissionScopeModelGateway, EstimateMicroUSD: oneUSD, IdempotencyKey: "model_gateway/garbled-young"}
	stagePendingClaim(t, m, tenant, stale)
	setStoredCell(t, st, tenant, stale.IdempotencyKey, colAdmOwedHandles, garbled)
	clk.advance(admissionClaimTakeover + time.Second)
	stagePendingClaim(t, m, tenant, young)
	setStoredCell(t, st, tenant, young.IdempotencyKey, colAdmOwedHandles, garbled)

	for _, req := range []AdmissionRequest{stale, young} {
		before := admissionRowOf(t, m, tenant, req.IdempotencyKey)
		res, err := m.Reserve(ctx, tenant, req)
		if err != nil || res.Allowed || res.Reason != ReasonStoreUnreachable {
			t.Errorf("%s: a row whose owed list does not decode must refuse deny-closed: %+v err=%v", req.IdempotencyKey, res, err)
		}
		after := admissionRowOf(t, m, tenant, req.IdempotencyKey)
		if after.version != before.version || after.owedRaw != garbled || !errors.Is(after.owedErr, errOwedUndecodable) {
			t.Errorf("%s: the row was written: version %d → %d, text %v", req.IdempotencyKey, before.version, after.version, after.owedRaw)
		}
	}
	if n := len(countReservations(t, st, tenant)); n != 0 {
		t.Fatalf("a refused key left %d ledger row(s)", n)
	}
}

// twoRowsPerKeyReads serves the marked caller's reads through a repository that returns
// each row of a key twice.
type twoRowsPerKeyReads struct{ api.ModuleData }

func (d twoRowsPerKeyReads) View(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	if !marked(ctx) {
		return d.ModuleData.View(ctx, tenant, fn)
	}
	return d.ModuleData.View(ctx, tenant, func(sc store.Scope) error {
		return fn(twoRowsPerKeyScope{Scope: sc})
	})
}

// TestCorruptRowRefusesKey: a key whose row is corrupt — a slot that is not a hold
// identity, a second row returned for the key, or a hold that another row names — is
// refused, in every unreachable posture: the store was read, and what it holds is not an
// admission. Nothing is written to either row or to the ledger, and the refusal takes no
// hold. A key beside them is admitted as usual.
func TestCorruptRowRefusesKey(t *testing.T) {
	forEachAdmissionEngine(t, runCorruptRowRefusesKey)
}

func runCorruptRowRefusesKey(t *testing.T, cfg store.Config) {
	m, st, tenant, _ := openFinCfg(t, cfg)
	clk := &fakeClock{t: baseTime}
	m.clock = clk
	ctx := context.Background()
	budget, limit := seedBudgetAndSeatLimit(t, st, tenant)
	seq := int64(0)
	held := func(h holdID) {
		seq++
		seedReservation(t, st, tenant, ledgerRow(budget, "b", h, seq, 2*oneUSD, baseTime, baseTime.Add(reservationTTL), resvStateActive))
		seedReservation(t, st, tenant, ledgerRow(limit, "s", h, seq, 2*oneUSD, baseTime, baseTime.Add(reservationTTL), resvStateActive))
	}

	// A reserved row whose hold is valid and withholds and whose spend slot is no identity.
	badSpend := seatRequest("model_gateway/bad-spend-slot")
	h := newHoldID()
	rec := admissionRecordFor(badSpend, admStateReserved, h, "", baseTime)
	rec[colAdmSpendHandle] = "x1"
	seedAdmission(t, st, tenant, rec)
	held(h)
	// A reserved row whose handle slot is spelled as no writer stores it.
	badHandle := seatRequest("model_gateway/bad-handle-slot")
	rec = admissionRecordFor(badHandle, admStateReserved, newHoldID(), "", baseTime)
	rec[colAdmHandle] = strings.ToUpper(rec.String(colAdmHandle))
	seedAdmission(t, st, tenant, rec)
	// A valid reserved row, asked through reads that return its row twice.
	twice := seatRequest("model_gateway/two-rows-for-one-key")
	h2 := newHoldID()
	seedAdmission(t, st, tenant, admissionRecordFor(twice, admStateReserved, h2, "", baseTime))
	held(h2)
	// A dead claimant's pending row whose hold another key's reserved row names too.
	dead := seatRequest("model_gateway/dead-claimant")
	other := seatRequest("model_gateway/names-the-same-hold")
	shared := newHoldID()
	seedAdmission(t, st, tenant, admissionRecordFor(dead, admStatePending, shared, "", baseTime))
	seedAdmission(t, st, tenant, admissionRecordFor(other, admStateReserved, shared, "", baseTime))
	held(shared)
	clk.advance(admissionClaimTakeover + time.Second)

	admissions := rowsByID(t, st, tenant, admissionIdempotencyKind)
	ledger := rowsByID(t, st, tenant, budgetReservationKind)
	base := m.data
	for _, tc := range []struct {
		name string
		req  AdmissionRequest
		data api.ModuleData
	}{
		{"a spend slot that is not an identity", badSpend, base},
		{"a handle slot spelled as no writer stores it", badHandle, base},
		{"two rows returned for the key", twice, twoRowsPerKeyReads{ModuleData: base}},
		{"a stale claim whose hold another row names", dead, base},
	} {
		for _, posture := range []UnreachablePosture{UnreachableDeny, UnreachableAllow} {
			req := tc.req
			req.Unreachable = posture
			m.data = tc.data
			res, err := m.Reserve(pausedCtx(ctx), tenant, req)
			m.data = base
			if err != nil || res.Allowed || res.Replayed || res.Handle != "" || res.Reason != ReasonAdmissionIntegrity {
				t.Errorf("%s, posture %s: %+v err=%v; want the integrity refusal", tc.name, posture, res, err)
			}
		}
	}
	assertRowsUnchanged(t, "the admission rows", admissions, rowsByID(t, st, tenant, admissionIdempotencyKind))
	assertRowsUnchanged(t, "the ledger", ledger, rowsByID(t, st, tenant, budgetReservationKind))

	beside, err := m.Reserve(ctx, tenant, seatRequest("model_gateway/beside"))
	if err != nil || !beside.Allowed || beside.Handle == "" {
		t.Fatalf("a key beside the corrupt ones must be admitted: %+v err=%v", beside, err)
	}
}

// TestNoTargetReEvaluatedAfterBudgetAppears: with no budget and no spend limit, an
// admission is allowed and holds nothing — its row publishes no hold — so the same
// request after a budget is created is evaluated against that budget and takes a hold.
func TestNoTargetReEvaluatedAfterBudgetAppears(t *testing.T) {
	forEachAdmissionEngine(t, runNoTargetReEvaluatedAfterBudgetAppears)
}

func runNoTargetReEvaluatedAfterBudgetAppears(t *testing.T, cfg store.Config) {
	m, st, tenant, _ := openFinCfg(t, cfg)
	m.clock = &fakeClock{t: baseTime}
	ctx := context.Background()
	req := seatRequest("model_gateway/no-target")

	first, err := m.Reserve(ctx, tenant, req)
	if err != nil || !first.Allowed || first.Handle != "" {
		t.Fatalf("no target: %+v err=%v; want allowed holding nothing", first, err)
	}
	if row := admissionRowOf(t, m, tenant, req.IdempotencyKey); row.state != admStateReserved || !row.handle.isZero() {
		t.Fatalf("the row publishes %q (%s); an admission that inserted no row publishes no hold", row.handle, row.state)
	}

	createBudget(t, st, tenant, "B", budgetSpec{Dimension: "global", Period: "monthly", LimitMicroUSD: 20 * oneUSD, Action: "block"})
	second, err := m.Reserve(ctx, tenant, req)
	if err != nil || !second.Allowed || second.Replayed || second.Handle == "" {
		t.Fatalf("after the budget appears: %+v err=%v; want a new evaluation that holds", second, err)
	}
	rows := activeRowsUnder(t, st, tenant, holdID(second.Handle))
	if len(rows) != 1 || rows[0].String(colResvPolicyKind) != policyKindBudget || rows[0].Int(colResvAmount) != 2*oneUSD {
		t.Fatalf("%d row(s) under the new hold, want its one budget row of 2 000 000", len(rows))
	}
}

// TestHeldRetryReplaysAfterNewSpend: a retry inside the window of a call that holds is
// handed that hold even after spend was recorded since, and the ledger holds once.
func TestHeldRetryReplaysAfterNewSpend(t *testing.T) {
	forEachAdmissionEngine(t, runHeldRetryReplaysAfterNewSpend)
}

func runHeldRetryReplaysAfterNewSpend(t *testing.T, cfg store.Config) {
	m, st, tenant, _ := openFinCfg(t, cfg)
	m.clock = &fakeClock{t: baseTime}
	ctx := context.Background()
	seedBudgetAndSeatLimit(t, st, tenant)
	req := seatRequest("model_gateway/held-retry")
	first, err := m.Reserve(ctx, tenant, req)
	if err != nil || !first.Allowed || first.Handle == "" {
		t.Fatalf("first: %+v err=%v", first, err)
	}
	spent := mkCost("anthropic", "model", "s1", 1, 1, oneUSD, baseTime)
	spent.Actor = "a"
	m.ingest(t, tenant, spent)
	retry, err := m.Reserve(ctx, tenant, req)
	if err != nil || !retry.Allowed || !retry.Replayed || retry.Handle != first.Handle {
		t.Fatalf("the retry: %+v err=%v; want a replay of %s", retry, err, first.Handle)
	}
	if n := len(countReservations(t, st, tenant)); n != 2 || len(activeRowsUnder(t, st, tenant, holdID(first.Handle))) != 2 {
		t.Fatalf("%d ledger rows, want only the hold's two components", n)
	}
}

// incompleteLedgerReads serves the marked caller's reads with a reservation ledger that
// ends on a page promising more rows and giving no cursor.
type incompleteLedgerReads struct{ api.ModuleData }

func (d incompleteLedgerReads) View(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	if !marked(ctx) {
		return d.ModuleData.View(ctx, tenant, fn)
	}
	return d.ModuleData.View(ctx, tenant, func(sc store.Scope) error {
		return fn(ledgerFaultScope{Scope: sc, incomplete: true})
	})
}

// TestUnreadableHoldRetryRefuses: a retry inside the window whose hold cannot be read
// completely is refused deny-closed and changes nothing — not the row and not the hold.
func TestUnreadableHoldRetryRefuses(t *testing.T) {
	forEachAdmissionEngine(t, runUnreadableHoldRetryRefuses)
}

func runUnreadableHoldRetryRefuses(t *testing.T, cfg store.Config) {
	m, st, tenant, _ := openFinCfg(t, cfg)
	m.clock = &fakeClock{t: baseTime}
	ctx := context.Background()
	seedBudgetAndSeatLimit(t, st, tenant)
	req := seatRequest("model_gateway/unreadable-retry")
	first, err := m.Reserve(ctx, tenant, req)
	if err != nil || !first.Allowed || first.Handle == "" {
		t.Fatalf("first: %+v err=%v", first, err)
	}
	before := admissionRowOf(t, m, tenant, req.IdempotencyKey)

	base := m.data
	m.data = incompleteLedgerReads{ModuleData: base}
	retry, err := m.Reserve(pausedCtx(ctx), tenant, req)
	m.data = base
	if err != nil || retry.Allowed || retry.Replayed || retry.Reason != ReasonStoreUnreachable {
		t.Fatalf("a retry whose hold cannot be read: %+v err=%v; want a deny-closed refusal", retry, err)
	}
	after := admissionRowOf(t, m, tenant, req.IdempotencyKey)
	if after.version != before.version || after.handle.String() != first.Handle || after.state != admStateReserved {
		t.Fatalf("the refused retry moved the row: before %+v, after %+v", before, after)
	}
	if rows := activeRowsUnder(t, st, tenant, holdID(first.Handle)); len(rows) != 2 {
		t.Fatalf("the refused retry moved the hold: %d active row(s), want 2", len(rows))
	}
}

// TestZeroLedgerHoldIsReEvaluated: a reserved row inside its window that names a hold
// under which a complete read finds no ledger row withholds nothing, so it is not
// replayed: the key is taken over and evaluated in full, and takes a hold of its own.
func TestZeroLedgerHoldIsReEvaluated(t *testing.T) {
	forEachAdmissionEngine(t, runZeroLedgerHoldIsReEvaluated)
}

func runZeroLedgerHoldIsReEvaluated(t *testing.T, cfg store.Config) {
	m, st, tenant, _ := openFinCfg(t, cfg)
	m.clock = &fakeClock{t: baseTime.Add(10 * time.Second)}
	seedBudgetAndSeatLimit(t, st, tenant)
	req := seatRequest("model_gateway/names-a-hold-without-rows")
	empty := newHoldID()
	seedAdmission(t, st, tenant, admissionRecordFor(req, admStateReserved, empty, "", baseTime))

	res, err := m.Reserve(context.Background(), tenant, req)
	if err != nil || !res.Allowed || res.Replayed || res.Handle == "" || res.Handle == empty.String() {
		t.Fatalf("a row naming a hold with no ledger row: %+v err=%v; want a new evaluation that holds", res, err)
	}
	row := admissionRowOf(t, m, tenant, req.IdempotencyKey)
	if row.state != admStateReserved || row.handle.String() != res.Handle || len(row.owed) != 0 {
		t.Fatalf("the row is %s naming %s owing %d, want reserved naming the new hold and owing nothing", row.state, row.handle, len(row.owed))
	}
	if rows := ledgerRowsUnder(t, st, tenant, empty); len(rows) != 0 {
		t.Fatalf("%d row(s) under the hold that had none", len(rows))
	}
	rows := activeRowsUnder(t, st, tenant, holdID(res.Handle))
	if kinds := componentsOf(rows); len(rows) != 2 || kinds[policyKindBudget] != 1 || kinds[policyKindSpendLimit] != 1 {
		t.Fatalf("the new hold has %d active row(s) %v, want one of each component", len(rows), kinds)
	}
}

// TestZeroLedgerReadErrorRefuses: the same row, when the read under its hold does not
// complete, is refused deny-closed: only a complete read may show that a hold has no
// rows. The row is not written and no hold is taken.
func TestZeroLedgerReadErrorRefuses(t *testing.T) {
	forEachAdmissionEngine(t, runZeroLedgerReadErrorRefuses)
}

func runZeroLedgerReadErrorRefuses(t *testing.T, cfg store.Config) {
	m, st, tenant, _ := openFinCfg(t, cfg)
	m.clock = &fakeClock{t: baseTime.Add(10 * time.Second)}
	seedBudgetAndSeatLimit(t, st, tenant)
	req := seatRequest("model_gateway/names-a-hold-without-rows")
	seedAdmission(t, st, tenant, admissionRecordFor(req, admStateReserved, newHoldID(), "", baseTime))
	admissions := rowsByID(t, st, tenant, admissionIdempotencyKind)

	base := m.data
	m.data = incompleteLedgerReads{ModuleData: base}
	res, err := m.Reserve(pausedCtx(context.Background()), tenant, req)
	m.data = base
	if err != nil || res.Allowed || res.Replayed || res.Handle != "" || res.Reason != ReasonStoreUnreachable {
		t.Fatalf("a row whose hold cannot be read completely: %+v err=%v; want a deny-closed refusal", res, err)
	}
	assertRowsUnchanged(t, "the admission rows", admissions, rowsByID(t, st, tenant, admissionIdempotencyKind))
	if n := len(countReservations(t, st, tenant)); n != 0 {
		t.Fatalf("the refusal left %d ledger row(s)", n)
	}
}

// steppingClock moves forward by step every time it is read, and remembers every
// instant it returned.
type steppingClock struct {
	mu    sync.Mutex
	t     time.Time
	step  time.Duration
	reads []time.Time
}

func (c *steppingClock) Now() model.Timestamp {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.t
	c.reads = append(c.reads, now)
	c.t = c.t.Add(c.step)
	return model.NewTimestamp(now)
}

// returned reports whether the clock returned at.
func (c *steppingClock) returned(at time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, r := range c.reads {
		if r.Equal(at) {
			return true
		}
	}
	return false
}

// TestCreateGivesBothPhasesOneInstant: the module clock moves on a second every time it
// is read, and both components of the hold still expire together, at one instant the
// clock returned before the publication plus the TTL — the create's one instant, used by
// its budget phase and its spend phase alike.
func TestCreateGivesBothPhasesOneInstant(t *testing.T) {
	forEachAdmissionEngine(t, runCreateGivesBothPhasesOneInstant)
}

func runCreateGivesBothPhasesOneInstant(t *testing.T, cfg store.Config) {
	m, st, tenant, _ := openFinCfg(t, cfg)
	clk := &steppingClock{t: baseTime, step: time.Second}
	m.clock = clk
	seedBudgetAndSeatLimit(t, st, tenant)
	req := seatRequest("model_gateway/one-instant")

	res, err := m.Reserve(context.Background(), tenant, req)
	if err != nil || !res.Allowed || res.Handle == "" {
		t.Fatalf("reserve: %+v err=%v", res, err)
	}
	rows := activeRowsUnder(t, st, tenant, holdID(res.Handle))
	if kinds := componentsOf(rows); len(rows) != 2 || kinds[policyKindBudget] != 1 || kinds[policyKindSpendLimit] != 1 {
		t.Fatalf("the hold has %d active row(s) %v, want one of each component", len(rows), kinds)
	}
	if b, s := rows[0].String(colResvExpiresAt), rows[1].String(colResvExpiresAt); b != s {
		t.Fatalf("the components expire at %s and %s: one hold, one instant, one expiry", b, s)
	}
	exp, err := model.ParseTimestamp(rows[0].String(colResvExpiresAt))
	if err != nil {
		t.Fatalf("the expiry does not parse: %v", err)
	}
	instant := exp.Time().Add(-reservationTTL)
	published := admissionRowOf(t, m, tenant, req.IdempotencyKey).stateAt.Time()
	if !clk.returned(instant) || !instant.Before(published) {
		t.Fatalf("the hold expires at %s: its instant %s is not one the clock returned before the publication at %s", exp, instant, published)
	}
}

// TestAStalePendingClaimIsTakenOverAndAnsweredFromTheLedger: a claim whose caller died
// between staging it and its verdict is taken over and answered from the ledger — yes
// under a cap with headroom, and no once the cap is blown — never refused as unreadable.
func TestAStalePendingClaimIsTakenOverAndAnsweredFromTheLedger(t *testing.T) {
	forEachAdmissionEngine(t, runAStalePendingClaimIsTakenOverAndAnsweredFromTheLedger)
}

func runAStalePendingClaimIsTakenOverAndAnsweredFromTheLedger(t *testing.T, cfg store.Config) {
	m, st, tenant, _ := openFinCfg(t, cfg)
	clk := &fakeClock{t: baseTime}
	m.clock = clk
	ctx := context.Background()
	createBudget(t, st, tenant, "global-block", budgetSpec{
		Dimension: "global", Period: "monthly", LimitMicroUSD: 5 * oneUSD, Action: "block",
	})

	withRoom := AdmissionRequest{Scope: AdmissionScopeSessionLaunch, IdempotencyKey: "session_launch/run-crashed"}
	stagePendingClaim(t, m, tenant, withRoom)
	clk.advance(admissionClaimTakeover + time.Second)

	res, err := m.Reserve(ctx, tenant, withRoom)
	if err != nil {
		t.Fatalf("reserve over a stale claim: %v", err)
	}
	if res.Reason == ReasonStoreUnreachable {
		t.Fatalf("a claim nobody is holding refused the key as an unreadable ledger: %+v", res)
	}
	if !res.Allowed {
		t.Fatalf("the recovered key was refused under a cap with headroom: %+v", res)
	}

	m.ingest(t, tenant, mkCost("anthropic", "model", "s1", 1, 1, 10*oneUSD, baseTime))
	overCap := AdmissionRequest{Scope: AdmissionScopeSessionLaunch, IdempotencyKey: "session_launch/run-crashed-2"}
	stagePendingClaim(t, m, tenant, overCap)
	clk.advance(admissionClaimTakeover + time.Second)

	denied, err := m.Reserve(ctx, tenant, overCap)
	if err != nil {
		t.Fatalf("reserve over a stale claim on a blown cap: %v", err)
	}
	if denied.Allowed {
		t.Fatalf("the takeover admitted a run over a cap 10 USD past its 5 USD limit: %+v", denied)
	}
	if denied.Reason == ReasonStoreUnreachable {
		t.Fatalf("the takeover refused with the deny-closed reason instead of the budget's: %+v", denied)
	}
	if denied.Action != "block" {
		t.Fatalf("Action = %q, want block", denied.Action)
	}
}

// TestAFreshPendingClaimIsWaitedForNotTakenOver: a claim staged a moment ago is another
// caller mid-evaluation, and taking it over would put two callers on one key. The waiter
// gets the claim-holder's hold, and the ledger holds once. The holder publishes only
// after the waiter completed the read that met the pending row, so the test cannot pass
// without the waiter having met the state it must wait for.
func TestAFreshPendingClaimIsWaitedForNotTakenOver(t *testing.T) {
	forEachAdmissionEngine(t, runAFreshPendingClaimIsWaitedForNotTakenOver)
}

func runAFreshPendingClaimIsWaitedForNotTakenOver(t *testing.T, cfg store.Config) {
	m, st, tenant, _ := openFinCfg(t, cfg)
	m.clock = &fakeClock{t: baseTime}
	ctx := context.Background()
	createBudget(t, st, tenant, "global-block", budgetSpec{
		Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD, Action: "block",
	})
	req := AdmissionRequest{
		Scope: AdmissionScopeModelGateway, EstimateMicroUSD: oneUSD,
		IdempotencyKey: "model_gateway/held-by-a-live-caller",
	}
	stagePendingClaim(t, m, tenant, req)

	d := &pausedReadData{ModuleData: m.data, nth: 1, reached: make(chan struct{}), resume: make(chan struct{})}
	m.data = d
	finished := publishAfterObservedPendingRead(t, m, tenant, req, d)

	res, err := m.Reserve(pausedCtx(ctx), tenant, req)
	claimed := <-finished
	if err != nil {
		t.Fatalf("the waiter errored: %v", err)
	}
	if claimed == "" {
		t.Fatal("the claim holder could not reach its verdict; the test proves nothing")
	}
	if !res.Allowed || !res.Replayed || res.Handle != claimed {
		t.Fatalf("the waiter came back with %+v, not the claim holder's hold %q: two callers evaluated one key", res, claimed)
	}
	if got := ledgerCounts(t, st, tenant, baseTime); got.Active != 1 {
		t.Fatalf("one key evaluated once left %+v, want exactly 1 active row", got)
	}
}

// TestThePauseBetweenClaimPollsIsRealAndCancellable: the wait for another caller's claim
// actually pauses between reads, and it ends the moment the caller stops waiting.
func TestThePauseBetweenClaimPollsIsRealAndCancellable(t *testing.T) {
	m := &Module{}

	start := time.Now()
	if err := m.pauseBetweenClaimPolls(context.Background()); err != nil {
		t.Fatalf("an uncanceled pause returned %v", err)
	}
	if elapsed := time.Since(start); elapsed < admissionClaimPoll {
		t.Fatalf("the pause took %v, less than the %v it is for: the wait is a spin", elapsed, admissionClaimPoll)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := m.pauseBetweenClaimPolls(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("a pause under a canceled context returned %v, want it to carry context.Canceled", err)
	}
}
