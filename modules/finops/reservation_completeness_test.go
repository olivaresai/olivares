// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"context"
	"errors"
	"math"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// -----------------------------------------------------------------------------
// D02-A: a reservation ledger that could not be fully enumerated, or whose total
// is not representable, is an INABILITY TO ESTABLISH THE CEILING — never a
// smaller total and never new headroom. These cases pin that distinction at the
// seams that decide admission (ReserveBudget, CheckBudget), the seams that report
// it (budgetStatus) and the seams that settle it (Commit/Release, Sweep).
//
// The enumeration fixtures are scripted pagers over the REAL store, not a million
// rows on disk: the defect is in how the pager's own signals (HasMore, Cursor) are
// read, so staging those signals is the causal reproduction. The arithmetic cases
// use real rows with real int64 amounts, because that is where the money lives.
// -----------------------------------------------------------------------------

// forcedReservationData replaces ONLY the reservation repository's List, on both
// the read and the write path, so an enumeration that cannot be completed can be
// staged without writing maxScanPages × listCap rows. Create/Update still reach
// the real repository, so a settlement that DOES transition a row transitions it
// for real (which is what makes the "committed only a prefix" reproduction
// honest, rather than an error from a fabricated record).
type forcedReservationData struct {
	api.ModuleData
	pager *scriptedPager
}

func (d forcedReservationData) View(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	return d.ModuleData.View(ctx, tenant, func(sc store.Scope) error {
		return fn(forcedReservationScope{Scope: sc, pager: d.pager})
	})
}

func (d forcedReservationData) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	return d.ModuleData.Mutate(ctx, tenant, func(sc store.Scope) error {
		return fn(forcedReservationScope{Scope: sc, pager: d.pager})
	})
}

type forcedReservationScope struct {
	store.Scope
	pager *scriptedPager
}

// LockTransaction forwards the REAL scope's optional capability: embedding
// store.Scope alone hides it, and the alert writer fails closed without it. A
// fixture must not be able to make a case pass by pretending it acquired a lock, so
// this delegates to the scope the store actually produced.
func (s forcedReservationScope) LockTransaction(ctx context.Context, key string) error {
	locker, ok := s.Scope.(store.TransactionLocker)
	if !ok {
		return errors.New("finops-test: wrapped scope provides no transaction lock")
	}
	return locker.LockTransaction(ctx, key)
}

// TransactionNow forwards the REAL database clock, for the same reason
// LockTransaction forwards the real lock: a decorator that hid the capability
// would make the D02 lifecycle operations fail closed on the FIXTURE rather than
// on anything the case is about. Forwarding is additive — no existing case's
// behavior changes, because none of them read the transaction clock.
func (s forcedReservationScope) TransactionNow(ctx context.Context) (model.Timestamp, error) {
	clock, ok := s.Scope.(store.TransactionClock)
	if !ok {
		return model.Timestamp{}, errors.New("finops-test: wrapped scope provides no transaction clock")
	}
	return clock.TransactionNow(ctx)
}

func (s forcedReservationScope) Ext(kind model.Kind) (store.GenericRepo, error) {
	repo, err := s.Scope.Ext(kind)
	if err != nil || kind != budgetReservationKind {
		return repo, err
	}
	return &forcedReservationRepo{GenericRepo: repo, pager: s.pager}, nil
}

type forcedReservationRepo struct {
	store.GenericRepo
	pager *scriptedPager
}

func (r *forcedReservationRepo) List(ctx context.Context, q model.Query) ([]model.Record, model.Page, error) {
	// The seq probe is a sorted, single-row query; leave it on the real repo so a
	// paging fixture never has to impersonate the serialization token as well. With
	// neither script the enumeration is the real one, which is what a case about the
	// TRANSITIONS (rather than the reading) needs.
	if len(q.Sort) > 0 || (r.pager.script == nil && r.pager.listErr == nil) {
		return r.GenericRepo.List(ctx, q)
	}
	return r.pager.next(q)
}

func (r *forcedReservationRepo) Update(ctx context.Context, rec model.Record) (model.Record, error) {
	if err := r.pager.countUpdate(); err != nil {
		return nil, err
	}
	return r.GenericRepo.Update(ctx, rec)
}

// scriptedPager answers each unsorted List with the page the case means to stage:
// a cap reached with rows still to come, a page that claims more without handing
// back a cursor, or a cursor that never advances. It also counts the Update calls
// a settlement makes, so "committed only a prefix" is measured, not inferred.
type scriptedPager struct {
	mu      sync.Mutex
	calls   int
	updates int
	script  func(call int, q model.Query) ([]model.Record, model.Page)
	// listErr, when set, is consulted BEFORE the script on every unsorted List and
	// turns that read into a store failure. It is how a case stages an ordinary
	// outage (or a conflict) arriving AFTER the ledger has already been read once —
	// the order in which a decided refusal can be overwritten by an error.
	listErr func(call int) error
	// updateErrAfter, when positive, fails every Update past the nth: a store that
	// breaks PART WAY through a sweep, which is the other way a partial count can
	// be reported.
	updateErrAfter int
}

func (p *scriptedPager) next(q model.Query) ([]model.Record, model.Page, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	call := p.calls
	p.calls++
	if p.listErr != nil {
		if err := p.listErr(call); err != nil {
			return nil, model.Page{}, err
		}
	}
	if p.script == nil {
		// Only an error script was installed and this call is not one of its
		// failures: answer an empty, COMPLETE page rather than invent rows.
		return nil, model.Page{}, nil
	}
	rows, page := p.script(call, q)
	return rows, page, nil
}

func (p *scriptedPager) countUpdate() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.updates++
	if p.updateErrAfter > 0 && p.updates > p.updateErrAfter {
		return errors.New("store: update failed")
	}
	return nil
}

func (p *scriptedPager) updateCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.updates
}

func (p *scriptedPager) listCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

// forcePager installs the scripted reservation pager on the module's data handle.
func forcePager(m *Module, script func(call int, q model.Query) ([]model.Record, model.Page)) *scriptedPager {
	p := &scriptedPager{script: script}
	m.UseData(forcedReservationData{ModuleData: m.data, pager: p})
	return p
}

// forcePagerWithErrors installs a scripted pager whose reads can also FAIL: script
// answers the reads that succeed, listErr decides which call number breaks and how.
func forcePagerWithErrors(m *Module, script func(call int, q model.Query) ([]model.Record, model.Page), listErr func(call int) error) *scriptedPager {
	p := &scriptedPager{script: script, listErr: listErr}
	m.UseData(forcedReservationData{ModuleData: m.data, pager: p})
	return p
}

// forceUpdateFailureAfter keeps the REAL enumeration and breaks the nth+1 write,
// so a case can ask what a half-applied batch reports.
func forceUpdateFailureAfter(m *Module, n int) *scriptedPager {
	p := &scriptedPager{updateErrAfter: n}
	m.UseData(forcedReservationData{ModuleData: m.data, pager: p})
	return p
}

// alwaysMorePager reports another page forever with an advancing cursor: the scan
// stops at the page cap with rows still unread.
func alwaysMorePager(rows []model.Record) func(int, model.Query) ([]model.Record, model.Page) {
	return func(call int, _ model.Query) ([]model.Record, model.Page) {
		return rows, model.Page{Cursor: "cursor-" + strconv.Itoa(call+1), HasMore: true}
	}
}

// noCursorPager claims more rows without handing back a cursor: the scan cannot
// advance, so the enumeration is over before it finished.
func noCursorPager(rows []model.Record) func(int, model.Query) ([]model.Record, model.Page) {
	return func(_ int, _ model.Query) ([]model.Record, model.Page) {
		// The same answer every time: with no cursor there is nothing to resume
		// from, so the query re-issued is the query already asked.
		return rows, model.Page{HasMore: true}
	}
}

// stuckCursorPager hands back the SAME cursor: the next page is the page just
// read, so the scan makes no progress and would re-count the same rows.
func stuckCursorPager(rows []model.Record) func(int, model.Query) ([]model.Record, model.Page) {
	return func(_ int, _ model.Query) ([]model.Record, model.Page) {
		return rows, model.Page{Cursor: "stuck", HasMore: true}
	}
}

// exactCapPager fills EXACTLY maxScanPages pages and then says there is nothing
// more. This is a complete enumeration that merely used the whole budget, and it
// must stay valid — the cap is not the defect, dropping HasMore is.
func exactCapPager(rows []model.Record) func(int, model.Query) ([]model.Record, model.Page) {
	return func(call int, _ model.Query) ([]model.Record, model.Page) {
		if call+1 >= maxScanPages {
			return rows, model.Page{HasMore: false}
		}
		return rows, model.Page{Cursor: "cursor-" + strconv.Itoa(call+1), HasMore: true}
	}
}

// reservationRow builds an ACTIVE reservation row for a policy's current bucket.
func reservationRow(t testing.TB, policyID model.ID, scopeKey, period string, seq, amount int64, now time.Time, state string) model.Record {
	t.Helper()
	pStart, _ := periodStart(period, now)
	return model.Record{
		colResvPolicyRef:   policyID.String(),
		colResvPolicyKind:  policyKindBudget,
		colResvDimension:   "global",
		colResvScopeKey:    scopeKey,
		colResvPeriod:      period,
		colResvPeriodStart: model.NewTimestamp(pStart).String(),
		colResvSeq:         seq,
		colResvAmount:      amount,
		colResvActual:      int64(0),
		colResvState:       state,
		colResvHandle:      model.NewID().String(),
		colResvExpiresAt:   model.NewTimestamp(now.Add(time.Hour)).String(),
	}
}

// seedReservation writes one reservation row directly, so a case can stage a
// ledger state (a malformed amount, an exhausted seq) that the reserve path
// itself would never produce.
func seedReservation(t testing.TB, st store.Store, tenant model.TenantID, rec model.Record) model.Record {
	t.Helper()
	var out model.Record
	if err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(budgetReservationKind)
		if err != nil {
			return err
		}
		out, err = repo.Create(context.Background(), rec)
		return err
	}); err != nil {
		t.Fatalf("seed reservation: %v", err)
	}
	return out
}

// countReservations returns every reservation row of the tenant.
func countReservations(t testing.TB, st store.Store, tenant model.TenantID) []model.Record {
	t.Helper()
	var out []model.Record
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(budgetReservationKind)
		if err != nil {
			return err
		}
		out, _, err = repo.List(context.Background(), model.Query{Limit: listCap})
		return err
	}); err != nil {
		t.Fatalf("list reservations: %v", err)
	}
	return out
}

// -----------------------------------------------------------------------------
// Enumeration
// -----------------------------------------------------------------------------

// TestReserveDeniesAnIncompleteReservationEnumeration is the core D02-A case: the
// reserved sum stopped at the page cap with rows still to come, so it is a LOWER
// bound. Admitting on a lower bound hands out headroom that may not exist, and
// returning an error hands the decision to a caller documented to fail open. The
// only answer that establishes nothing false is an explicit denial.
func TestReserveDeniesAnIncompleteReservationEnumeration(t *testing.T) {
	cases := map[string]func([]model.Record) func(int, model.Query) ([]model.Record, model.Page){
		"page cap reached with more rows": alwaysMorePager,
		"more rows but no cursor":         noCursorPager,
		"cursor does not advance":         stuckCursorPager,
	}
	for name, pager := range cases {
		t.Run(name, func(t *testing.T) {
			m, st, tenant, _ := newFin(t)
			bid := createBudget(t, st, tenant, "global-block", budgetSpec{
				Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD, Action: "block",
			})
			m.clock = &fakeClock{t: baseTime}
			forcePager(m, pager([]model.Record{budgetReservationRow(tenant, bid, baseTime, 0)}))

			res, err := m.ReserveBudget(context.Background(), tenant, SpendDims{}, oneUSD)
			if err != nil {
				t.Fatalf("an incomplete enumeration must be a normal denial, not an error the seam fails open on: %v", err)
			}
			if res.Allowed {
				t.Fatalf("reservation admitted on a ledger sum that is only a lower bound: %+v", res)
			}
			if res.Action == "" || res.Reason == "" {
				t.Errorf("denial must name its action and reason, like the truncated budget census does: %+v", res)
			}
		})
	}
}

// TestCheckBudgetDeniesAnIncompleteReservationEnumeration pins the same fact on
// the read-only admission seam. CheckBudget fails OPEN on errors by contract, so
// an incomplete reservation ledger has to arrive as a DENIAL — the same shape it
// already uses for a truncated budget census.
func TestCheckBudgetDeniesAnIncompleteReservationEnumeration(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	bid := createBudget(t, st, tenant, "global-block", budgetSpec{
		Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD, Action: "block",
	})
	m.clock = &fakeClock{t: baseTime}
	forcePager(m, alwaysMorePager([]model.Record{budgetReservationRow(tenant, bid, baseTime, 0)}))

	chk, err := m.CheckBudget(context.Background(), tenant, SpendDims{})
	if err != nil {
		t.Fatalf("an incomplete enumeration must be a normal denial, not an error: %v", err)
	}
	if chk.Allowed {
		t.Fatalf("CheckBudget admitted with an unestablished reservation total: %+v", chk)
	}
	if chk.Action != "block" || !strings.Contains(chk.Reason, "fail-closed") {
		t.Errorf("deny = %+v, want an explicit block naming the fail-closed posture", chk)
	}
}

// TestReserveStillAdmitsWhenTheScanUsesTheWholeBudgetAndFinishes is the boundary
// control the fix must NOT break: exactly maxScanPages pages, the last one saying
// there is nothing more, is a COMPLETE enumeration. If this case denied, the fix
// would be refusing service on a legitimate estate.
func TestReserveStillAdmitsWhenTheScanUsesTheWholeBudgetAndFinishes(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	bid := createBudget(t, st, tenant, "global-block", budgetSpec{
		Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD, Action: "block",
	})
	m.clock = &fakeClock{t: baseTime}
	p := forcePager(m, exactCapPager([]model.Record{budgetReservationRow(tenant, bid, baseTime, 0)}))

	res, err := m.ReserveBudget(context.Background(), tenant, SpendDims{}, oneUSD)
	if err != nil {
		t.Fatalf("ReserveBudget: %v", err)
	}
	if !res.Allowed {
		t.Fatalf("a complete enumeration that used the whole page budget was refused: %+v", res)
	}
	if p.listCount() < maxScanPages {
		t.Errorf("the fixture drained %d pages, want the full %d — the case would not prove the boundary", p.listCount(), maxScanPages)
	}
}

// -----------------------------------------------------------------------------
// Arithmetic
// -----------------------------------------------------------------------------

// TestReserveDeniesWhenTheReservedSumIsNotRepresentable stages two live rows whose
// amounts do not fit in an int64 together. The unchecked sum WRAPS NEGATIVE, and a
// negative reserved total reads as headroom nobody has: the ledger says the bucket
// is full and the reservation is admitted anyway.
func TestReserveDeniesWhenTheReservedSumIsNotRepresentable(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	now := time.Now().UTC()
	bid := createBudget(t, st, tenant, "global-block", budgetSpec{
		Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD, Action: "block",
	})
	seedReservation(t, st, tenant, reservationRow(t, bid, "", "monthly", 1, math.MaxInt64, now, resvStateActive))
	seedReservation(t, st, tenant, reservationRow(t, bid, "", "monthly", 2, math.MaxInt64, now, resvStateActive))

	res, err := m.ReserveBudget(context.Background(), tenant, SpendDims{}, oneUSD)
	if err != nil {
		t.Fatalf("an unrepresentable total must be a normal denial, not an error: %v", err)
	}
	if res.Allowed {
		t.Fatalf("reservation admitted on a wrapped (negative) reserved total: %+v", res)
	}
}

// TestReserveDeniesANegativeReservationAmount stages a malformed row: a reservation
// holds prospective spend, so its amount is never negative. Summed unchecked, that
// one row MANUFACTURES headroom — a 50 USD estimate is admitted against a 10 USD
// budget. The check is confined to the reservation ledger: signed cost samples (a
// credit, a refund) keep working, and TestSignedCostAdjustmentsStillAggregate is
// the control that says so.
func TestReserveDeniesANegativeReservationAmount(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	now := time.Now().UTC()
	bid := createBudget(t, st, tenant, "global-block", budgetSpec{
		Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD, Action: "block",
	})
	seedReservation(t, st, tenant, reservationRow(t, bid, "", "monthly", 1, -100*oneUSD, now, resvStateActive))

	res, err := m.ReserveBudget(context.Background(), tenant, SpendDims{}, 50*oneUSD)
	if err != nil {
		t.Fatalf("a malformed row must be a normal denial, not an error: %v", err)
	}
	if res.Allowed {
		t.Fatalf("a negative reservation row bought 50 USD of headroom in a 10 USD budget: %+v", res)
	}
}

// TestSignedCostAdjustmentsStillAggregate is the anti-overreach control for the
// case above: a NEGATIVE cost sample is legitimate accounting (a credit), and the
// spend aggregate must keep honoring it. A blanket ban on negative money would
// have made a credited tenant look over budget.
func TestSignedCostAdjustmentsStillAggregate(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	ctx := context.Background()
	now := time.Now().UTC()
	createBudget(t, st, tenant, "global-block", budgetSpec{
		Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD, Action: "block",
	})
	// 12 USD spent, then a 6 USD credit: effective 6 < 10, so the budget admits.
	m.ingest(t, tenant, mkCost("openai", "gpt-x", "s-1", 10, 10, 12*oneUSD, now))
	m.ingest(t, tenant, mkCost("openai", "gpt-x", "s-1", 0, 0, -6*oneUSD, now.Add(time.Minute)))

	chk, err := m.CheckBudget(ctx, tenant, SpendDims{})
	if err != nil {
		t.Fatalf("CheckBudget: %v", err)
	}
	if !chk.Allowed {
		t.Fatalf("a 6 USD credit against 12 USD of spend left a 10 USD budget denying: %+v", chk)
	}
}

// TestReserveDeniesAnEstimateThatWouldNotBeRepresentable is the estimate-side wrap:
// the reserved total is exact, the estimate is exact, and their SUM is not. Unchecked,
// effective+estimate wraps negative and compares below the ceiling, so the biggest
// possible request is the one that gets in.
func TestReserveDeniesAnEstimateThatWouldNotBeRepresentable(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	now := time.Now().UTC()
	bid := createBudget(t, st, tenant, "global-block", budgetSpec{
		Dimension: "global", Period: "monthly", LimitMicroUSD: 1_000_000_000_000_000, Action: "block",
	})
	seedReservation(t, st, tenant, reservationRow(t, bid, "", "monthly", 1, math.MaxInt64-10, now, resvStateActive))

	res, err := m.ReserveBudget(context.Background(), tenant, SpendDims{}, 20)
	if err != nil {
		t.Fatalf("an unrepresentable comparison must be a normal denial, not an error: %v", err)
	}
	if res.Allowed {
		t.Fatalf("an estimate whose ceiling comparison wraps was admitted: %+v", res)
	}
}

// TestReserveAdmitsExactlyUpToTheCeiling is the arithmetic boundary control: a
// total that lands EXACTLY on the ceiling is representable and within budget, and
// one micro-USD more is not. This is what keeps the overflow guard from becoming a
// blanket refusal near large-but-valid numbers.
func TestReserveAdmitsExactlyUpToTheCeiling(t *testing.T) {
	const limit = int64(1_000_000_000_000_000)
	for _, tc := range []struct {
		name     string
		estimate int64
		want     bool
	}{
		{"exactly at the ceiling", 5, true},
		{"one micro-USD over", 6, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, st, tenant, _ := newFin(t)
			now := time.Now().UTC()
			bid := createBudget(t, st, tenant, "global-block", budgetSpec{
				Dimension: "global", Period: "monthly", LimitMicroUSD: limit, Action: "block",
			})
			seedReservation(t, st, tenant, reservationRow(t, bid, "", "monthly", 1, limit-5, now, resvStateActive))

			res, err := m.ReserveBudget(context.Background(), tenant, SpendDims{}, tc.estimate)
			if err != nil {
				t.Fatalf("ReserveBudget: %v", err)
			}
			if res.Allowed != tc.want {
				t.Fatalf("allowed = %v, want %v (reserved %d + estimate %d against ceiling %d)", res.Allowed, tc.want, limit-5, tc.estimate, limit)
			}
		})
	}
}

// TestReserveDeniesWhenTheSequenceSpaceIsExhausted stages a bucket whose highest
// seq is math.MaxInt64. maxSeq+1 WRAPS to math.MinInt64, and the insert succeeds:
// the UNIQUE index that serializes concurrent reservers stops being monotonic, so
// the token that proves "no reserver committed under me" is no longer a proof.
// Nothing about that is a smaller ceiling — it is an inability to reserve.
func TestReserveDeniesWhenTheSequenceSpaceIsExhausted(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	now := time.Now().UTC()
	bid := createBudget(t, st, tenant, "global-block", budgetSpec{
		Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD, Action: "block",
	})
	// Settled, so it holds no headroom: the ONLY thing under test is the seq.
	seedReservation(t, st, tenant, reservationRow(t, bid, "", "monthly", math.MaxInt64, oneUSD, now, resvStateReleased))

	res, err := m.ReserveBudget(context.Background(), tenant, SpendDims{}, oneUSD)
	if err != nil {
		t.Fatalf("an exhausted seq must be a normal denial, not an error: %v", err)
	}
	if res.Allowed {
		t.Fatalf("reservation admitted with a wrapped sequence number: %+v", res)
	}
	for _, r := range countReservations(t, st, tenant) {
		if seq := r.Int(colResvSeq); seq < 0 {
			t.Fatalf("a row was written with seq %d: the monotonic serialization token wrapped", seq)
		}
	}
}

// TestReserveLeavesNoRowsWhenOneOfSeveralBudgetsCannotBeEstablished keeps the
// all-or-none property honest for the NEW denial: two budgets scope the request,
// the second cannot be established (its seq space is exhausted), and the first
// must not be left holding a row. A partial reservation is a leak of headroom no
// settlement will ever return.
func TestReserveLeavesNoRowsWhenOneOfSeveralBudgetsCannotBeEstablished(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	now := time.Now().UTC()
	createBudget(t, st, tenant, "roomy", budgetSpec{
		Dimension: "global", Period: "monthly", LimitMicroUSD: 1000 * oneUSD, Action: "block",
	})
	second := createBudget(t, st, tenant, "seq-exhausted", budgetSpec{
		Dimension: "provider", Key: "openai", Period: "monthly", LimitMicroUSD: 1000 * oneUSD, Action: "block",
	})
	seeded := seedReservation(t, st, tenant, reservationRow(t, second, "openai", "monthly", math.MaxInt64, 0, now, resvStateReleased))

	res, err := m.ReserveBudget(context.Background(), tenant, SpendDims{ProviderRef: "openai"}, oneUSD)
	if err != nil {
		t.Fatalf("ReserveBudget: %v", err)
	}
	if res.Allowed {
		t.Fatalf("the request was admitted although one budget could not be established: %+v", res)
	}
	rows := countReservations(t, st, tenant)
	if len(rows) != 1 || rows[0].String(model.ColID) != seeded.String(model.ColID) {
		t.Fatalf("the denied reservation left %d rows behind, want only the seeded one: a partial reservation leaks headroom", len(rows))
	}
}

// -----------------------------------------------------------------------------
// Reporting
// -----------------------------------------------------------------------------

// TestBudgetStatusDoesNotPresentAnUnestablishedTotalAsComplete: status is what an
// operator and the console read. With the reservation ledger unreadable past the
// cap, the reported figure is a lower bound — so the DTO must say it is truncated
// and must not publish REMAINING headroom derived from it.
func TestBudgetStatusDoesNotPresentAnUnestablishedTotalAsComplete(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	ctx := context.Background()
	bid := createBudget(t, st, tenant, "global-block", budgetSpec{
		Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD, Action: "block",
	})
	m.clock = &fakeClock{t: baseTime}
	forcePager(m, alwaysMorePager([]model.Record{budgetReservationRow(tenant, bid, baseTime, oneUSD)}))

	var dto budgetStatusDTO
	if err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		p, e := sc.Policies().Get(ctx, bid)
		if e != nil {
			return e
		}
		var serr error
		dto, serr = budgetStatus(ctx, sc, p, time.Now().UTC())
		return serr
	}); err != nil {
		t.Fatalf("status view: %v", err)
	}
	if !dto.Truncated {
		t.Errorf("status reported a partial reservation total as complete (truncated=false)")
	}
	if dto.RemainingMicroUSD != 0 {
		t.Errorf("status published %d µUSD of remaining headroom from a total it could not establish", dto.RemainingMicroUSD)
	}
}

// TestBudgetStatusFlagsAnUnrepresentableRemaining is R3 of the independent review:
// remaining headroom is limit - effective, and with a large enough signed CREDIT that
// subtraction leaves the int64 range. Declining to publish the figure was right;
// leaving the DTO's default zero behind while still claiming the status is complete
// was not — zero is a number an operator acts on, and it is not the amount.
//
// The credit is deliberately a legitimate signed cost sample, not a malformed row:
// the ordinary control in the same table proves a -6 USD credit still yields an exact
// remaining of 16 USD against a 10 USD limit. Nothing is saturated, and no negative
// accounting value is rejected.
func TestBudgetStatusFlagsAnUnrepresentableRemaining(t *testing.T) {
	for _, tc := range []struct {
		name          string
		credit        int64
		wantTruncated bool
		wantRemaining int64
	}{
		{name: "an ordinary credit still gives an exact remaining", credit: -6 * oneUSD, wantRemaining: 16 * oneUSD},
		{name: "a credit that puts remaining past the int64 range", credit: -math.MaxInt64, wantTruncated: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, st, tenant, _ := newFin(t)
			ctx := context.Background()
			bid := createBudget(t, st, tenant, "credited", budgetSpec{
				Dimension: "global", Period: "total", LimitMicroUSD: 10 * oneUSD, Action: "block",
			})
			// A COMPLETE aggregate carrying the credit; the "total" period keeps the
			// forecast path out of the case.
			forceAggregateResult(m, tc.credit, false)

			var dto budgetStatusDTO
			if err := m.data.View(ctx, tenant, func(sc store.Scope) error {
				p, e := sc.Policies().Get(ctx, bid)
				if e != nil {
					return e
				}
				var serr error
				dto, serr = budgetStatus(ctx, sc, p, time.Now().UTC())
				return serr
			}); err != nil {
				t.Fatalf("status view: %v", err)
			}
			t.Logf("credit=%d status=%+v", tc.credit, dto)

			// The R3 fact this case exists for is the REMAINING figure, and it is
			// unchanged. The truncated flag is asserted only in the direction R3 fixed:
			// A4.2 additionally sets it as the conservative signal for an unestablished
			// strict evaluation, and this fixture's forced cost rows carry no tenant
			// cell, so the strict read cannot attribute them and the flag is set in
			// both cases. The authoritative section below is the stronger control.
			if tc.wantTruncated && !dto.Truncated {
				t.Fatalf("truncated = false, want true: an amount the ledger cannot represent is not a complete result")
			}
			if !tc.wantTruncated {
				if dto.Amount == nil {
					t.Fatalf("the authoritative amount section is missing")
				}
				if dto.Amount.Class != string(amountUnknown) {
					t.Fatalf("class = %s: the forced fixture's rows are unattributable, so the strict evaluation must say unknown", dto.Amount.Class)
				}
			}
			if dto.RemainingMicroUSD != tc.wantRemaining {
				t.Fatalf("remaining = %d, want %d", dto.RemainingMicroUSD, tc.wantRemaining)
			}
			if dto.SpendMicroUSD != tc.credit {
				t.Errorf("spend = %d, want the signed aggregate %d: credits stay legitimate", dto.SpendMicroUSD, tc.credit)
			}
		})
	}
}

// -----------------------------------------------------------------------------
// Settlement
// -----------------------------------------------------------------------------

// TestCommitReservationRefusesToSettleOnlyAPrefix: settlement enumerates the rows
// of a handle and transitions them. If the enumeration stopped early, committing
// what was read settles PART of a multi-budget reservation and reports success —
// the untouched rows keep holding headroom nobody will ever return, and the caller
// is told the actuation was accounted for. Refusing before the first Update is the
// only outcome that leaves the ledger consistent.
func TestCommitReservationRefusesToSettleOnlyAPrefix(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	ctx := context.Background()
	createBudget(t, st, tenant, "roomy", budgetSpec{
		Dimension: "global", Period: "monthly", LimitMicroUSD: 1000 * oneUSD, Action: "block",
	})
	createBudget(t, st, tenant, "roomy-provider", budgetSpec{
		Dimension: "provider", Key: "openai", Period: "monthly", LimitMicroUSD: 1000 * oneUSD, Action: "block",
	})
	res, err := m.ReserveBudget(ctx, tenant, SpendDims{ProviderRef: "openai"}, oneUSD)
	if err != nil || !res.Allowed {
		t.Fatalf("reserve: allowed=%v err=%v", res.Allowed, err)
	}
	rows := countReservations(t, st, tenant)
	if len(rows) != 2 {
		t.Fatalf("fixture reserved %d rows, want 2 (the case needs a settlement that CAN be cut in half)", len(rows))
	}

	// Page one holds the first row and claims more forever: the scan ends at the cap
	// with the second row never read.
	first := []model.Record{rows[0]}
	p := forcePager(m, func(call int, _ model.Query) ([]model.Record, model.Page) {
		if call == 0 {
			return first, model.Page{Cursor: "cursor-1", HasMore: true}
		}
		return nil, model.Page{Cursor: "cursor-" + strconv.Itoa(call+1), HasMore: true}
	})

	if err := m.CommitReservation(ctx, tenant, res.Handle, oneUSD); err == nil {
		t.Fatalf("commit reported success after settling only a prefix of the handle's rows")
	}
	if got := p.updateCount(); got != 0 {
		t.Fatalf("commit transitioned %d rows before refusing; a partial settlement must not happen at all", got)
	}
	for _, r := range countReservations(t, st, tenant) {
		if r.String(colResvState) != resvStateActive {
			t.Fatalf("row %s left in state %q: the refused settlement still moved the ledger", r.String(model.ColID), r.String(colResvState))
		}
	}
}

// TestSweepDoesNotReportPartialSuccessAfterAnIncompleteScan: the sweep's return
// value is a count operators and future reconciliation read. A count produced from
// a scan that stopped early describes a job that did not happen.
func TestSweepDoesNotReportPartialSuccessAfterAnIncompleteScan(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	ctx := context.Background()
	fc := &fakeClock{t: time.Now().UTC()}
	m.clock = fc
	defer func(prev time.Duration) { reservationTTL = prev }(reservationTTL)
	reservationTTL = 30 * time.Second

	createBudget(t, st, tenant, "roomy", budgetSpec{
		Dimension: "global", Period: "monthly", LimitMicroUSD: 1000 * oneUSD, Action: "block",
	})
	res, err := m.ReserveBudget(ctx, tenant, SpendDims{}, oneUSD)
	if err != nil || !res.Allowed {
		t.Fatalf("reserve: allowed=%v err=%v", res.Allowed, err)
	}
	fc.advance(time.Minute) // the reservation has lapsed; a complete sweep would mark it

	rows := countReservations(t, st, tenant)
	p := forcePager(m, func(call int, _ model.Query) ([]model.Record, model.Page) {
		if call == 0 {
			return rows, model.Page{Cursor: "cursor-1", HasMore: true}
		}
		return nil, model.Page{Cursor: "cursor-" + strconv.Itoa(call+1), HasMore: true}
	})

	swept, err := m.SweepExpiredReservations(ctx, tenant)
	if err == nil {
		t.Fatalf("sweep reported success (%d swept) on an enumeration that never finished", swept)
	}
	if swept != 0 {
		t.Fatalf("sweep reported %d rows swept while refusing: a partial count is a false report", swept)
	}
	if got := p.updateCount(); got != 0 {
		t.Fatalf("sweep transitioned %d rows before refusing", got)
	}
}

// TestOrdinaryReservationTrafficIsUnchanged is the compatibility control: small,
// positive, complete traffic — reserve, see the headroom held, commit, see it
// returned — must behave exactly as before the completeness work.
func TestOrdinaryReservationTrafficIsUnchanged(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	ctx := context.Background()
	createBudget(t, st, tenant, "global-block", budgetSpec{
		Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD, Action: "block",
	})

	first, err := m.ReserveBudget(ctx, tenant, SpendDims{}, 6*oneUSD)
	if err != nil || !first.Allowed {
		t.Fatalf("reserve 6 of 10 USD: allowed=%v err=%v", first.Allowed, err)
	}
	if res, err := m.ReserveBudget(ctx, tenant, SpendDims{}, 6*oneUSD); err != nil || res.Allowed {
		t.Fatalf("a second 6 USD reservation must be denied while the first holds: allowed=%v err=%v", res.Allowed, err)
	}
	if err := m.CommitReservation(ctx, tenant, first.Handle, 6*oneUSD); err != nil {
		t.Fatalf("commit: %v", err)
	}
	after, err := m.ReserveBudget(ctx, tenant, SpendDims{}, 6*oneUSD)
	if err != nil || !after.Allowed {
		t.Fatalf("after the commit the headroom must be free again: allowed=%v err=%v", after.Allowed, err)
	}
}

// -----------------------------------------------------------------------------
// The scan and the sum, at the function they live in
// -----------------------------------------------------------------------------

// pagedRepo answers List from a script and implements nothing else: scanReservations
// calls List and only List, so this IS the whole fixture — no store, no disk, and
// no million rows to make a page cap happen.
type pagedRepo struct {
	store.GenericRepo
	replies []pagedReply
	calls   int
	cursors []string // the cursor each call was asked to resume from
}

type pagedReply struct {
	rows []model.Record
	page model.Page
}

func (r *pagedRepo) List(_ context.Context, q model.Query) ([]model.Record, model.Page, error) {
	r.cursors = append(r.cursors, q.Cursor)
	if r.calls < len(r.replies) {
		reply := r.replies[r.calls]
		r.calls++
		return reply.rows, reply.page, nil
	}
	r.calls++
	// Past the script, REPEAT the last reply: that is what a store does when the
	// same query is re-issued with the same (or no) cursor, and it is what makes a
	// scan without a progress check re-count the same rows page after page.
	last := r.replies[len(r.replies)-1]
	return last.rows, last.page, nil
}

// fixedReservationTenant/fixedReservationPolicy/fixedReservationNow are the identity
// these fixture rows are attributed to. A4.2's producer validates that an observed
// row evidences the tenant/policy/scope/period/state/validity the read asked for, so
// a bare amount cell is no longer a reservation row — it is an unattributable one,
// which is a different case with its own test below. The TENANT joined that list in
// the A4.2 correction.
var (
	fixedReservationTenant = model.TenantID(model.NewID().String())
	fixedReservationPolicy = model.NewID()
	fixedReservationNow    = baseTime
)

func fixedReservationPeriodStart() time.Time {
	start, _ := periodStart("monthly", fixedReservationNow)
	return start
}

func amountRows(amounts ...int64) []model.Record {
	out := make([]model.Record, 0, len(amounts))
	for _, a := range amounts {
		out = append(out, attributedReservationRow(a))
	}
	return out
}

// attributedReservationRow is one live reservation row carrying every cell the
// producer checks before it will sum the amount — including the tenant cell added by
// the A4.2 correction.
func attributedReservationRow(amount int64) model.Record {
	return model.Record{
		model.ColTenantID:  fixedReservationTenant.String(),
		colResvPolicyRef:   fixedReservationPolicy.String(),
		colResvScopeKey:    "",
		colResvPeriodStart: model.NewTimestamp(fixedReservationPeriodStart()).String(),
		colResvState:       resvStateActive,
		colResvExpiresAt:   model.NewTimestamp(fixedReservationNow.Add(time.Hour)).String(),
		colResvAmount:      amount,
	}
}

// budgetReservationRow is the same shape for a budget the test created, so a module
// level fixture can be attributed to that budget's own policy and period.
func budgetReservationRow(tenant model.TenantID, policy model.ID, at time.Time, amount int64) model.Record {
	start, _ := periodStart("monthly", at)
	return model.Record{
		model.ColTenantID:  tenant.String(),
		colResvPolicyRef:   policy.String(),
		colResvScopeKey:    "",
		colResvPeriodStart: model.NewTimestamp(start).String(),
		colResvState:       resvStateActive,
		colResvExpiresAt:   model.NewTimestamp(at.Add(time.Hour)).String(),
		colResvAmount:      amount,
	}
}

// TestScanReservationsSeparatesAFinishedScanFromAStoppedOne is the enumeration
// contract in one table: only a page that says there is nothing more ends a scan
// completely. Reaching the cap, being handed no cursor, or being handed the same
// cursor twice all end it EARLY, and each says so in its own words.
func TestScanReservationsSeparatesAFinishedScanFromAStoppedOne(t *testing.T) {
	rows := amountRows(oneUSD)
	for _, tc := range []struct {
		name           string
		replies        []pagedReply
		wantIncomplete bool
		wantRows       int
	}{
		{
			name:     "a single page that says it is the last",
			replies:  []pagedReply{{rows: rows, page: model.Page{}}},
			wantRows: 1,
		},
		{
			name: "exactly the page cap, and the last page says it is the last",
			replies: func() []pagedReply {
				out := make([]pagedReply, maxScanPages)
				for i := range out {
					out[i] = pagedReply{rows: rows, page: model.Page{Cursor: "cursor-" + strconv.Itoa(i+1), HasMore: true}}
				}
				out[maxScanPages-1].page = model.Page{}
				return out
			}(),
			wantRows: maxScanPages,
		},
		{
			name: "the page cap with rows still to come",
			replies: func() []pagedReply {
				out := make([]pagedReply, maxScanPages)
				for i := range out {
					out[i] = pagedReply{rows: rows, page: model.Page{Cursor: "cursor-" + strconv.Itoa(i+1), HasMore: true}}
				}
				return out
			}(),
			wantIncomplete: true,
			wantRows:       maxScanPages,
		},
		{
			name:           "more rows promised without a cursor",
			replies:        []pagedReply{{rows: rows, page: model.Page{HasMore: true}}},
			wantIncomplete: true,
			wantRows:       1,
		},
		{
			// On a LATER page the missing cursor is not the same fact as a cursor
			// that repeats: resuming from "" would silently restart at page one and
			// re-count it, so the scan has to stop here rather than one page on.
			name: "a later page promises more without a cursor",
			replies: []pagedReply{
				{rows: rows, page: model.Page{Cursor: "cursor-1", HasMore: true}},
				{rows: rows, page: model.Page{HasMore: true}},
			},
			wantIncomplete: true,
			wantRows:       2,
		},
		{
			name: "the cursor does not advance",
			replies: []pagedReply{
				{rows: rows, page: model.Page{Cursor: "stuck", HasMore: true}},
				{rows: rows, page: model.Page{Cursor: "stuck", HasMore: true}},
			},
			wantIncomplete: true,
			wantRows:       2,
		},
		{
			// R1 of the independent review's paging finding: the cycle need not be
			// adjacent. A -> B -> A returns to a page already read, so the rows after
			// it are duplicates of rows already summed — and comparing only against
			// the PREVIOUS cursor cannot see it. A cycle that later says "no more"
			// would otherwise be reported as a complete enumeration.
			name: "a continuation cursor cycles back to one already used",
			replies: []pagedReply{
				{rows: rows, page: model.Page{Cursor: "A", HasMore: true}},
				{rows: rows, page: model.Page{Cursor: "B", HasMore: true}},
				{rows: rows, page: model.Page{Cursor: "A", HasMore: true}},
				{rows: rows, page: model.Page{}},
			},
			wantIncomplete: true,
			wantRows:       3,
		},
		{
			// The paired boundary the cycle check must NOT break: the LAST page is
			// allowed to carry anything in its cursor — empty, stale or a repeat —
			// because nobody is going to resume from it.
			name: "the final page repeats a cursor nobody will use",
			replies: []pagedReply{
				{rows: rows, page: model.Page{Cursor: "A", HasMore: true}},
				{rows: rows, page: model.Page{Cursor: "A", HasMore: false}},
			},
			wantRows: 2,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &pagedRepo{replies: tc.replies}
			got, incomplete, err := scanReservations(context.Background(), repo, nil)
			if err != nil {
				t.Fatalf("scanReservations: %v", err)
			}
			if (incomplete != "") != tc.wantIncomplete {
				t.Fatalf("incomplete = %q, want incomplete=%v", incomplete, tc.wantIncomplete)
			}
			if len(got) != tc.wantRows {
				t.Fatalf("read %d rows, want %d", len(got), tc.wantRows)
			}
			if repo.calls > maxScanPages {
				t.Fatalf("the scan issued %d List calls, past the %d-page cap", repo.calls, maxScanPages)
			}
		})
	}
}

// TestActiveReservedTotalIsExactOrNotAtAll: the sum either IS the held headroom —
// including at the very top of the int64 range — or it is not established. There is
// no third answer, and in particular no smaller-but-usable one.
func TestActiveReservedTotalIsExactOrNotAtAll(t *testing.T) {
	for _, tc := range []struct {
		name            string
		replies         []pagedReply
		want            int64
		wantEstablished bool
	}{
		{
			name:            "ordinary amounts",
			replies:         []pagedReply{{rows: amountRows(3*oneUSD, 4*oneUSD), page: model.Page{}}},
			want:            7 * oneUSD,
			wantEstablished: true,
		},
		{
			name:            "a total that lands exactly on the largest representable amount",
			replies:         []pagedReply{{rows: amountRows(math.MaxInt64-1, 1), page: model.Page{}}},
			want:            math.MaxInt64,
			wantEstablished: true,
		},
		{
			name:    "one micro-USD past it",
			replies: []pagedReply{{rows: amountRows(math.MaxInt64, 1), page: model.Page{}}},
		},
		{
			name:    "a malformed negative row",
			replies: []pagedReply{{rows: amountRows(5*oneUSD, -oneUSD), page: model.Page{}}},
		},
		{
			name:    "rows still to come",
			replies: []pagedReply{{rows: amountRows(oneUSD), page: model.Page{HasMore: true}}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &pagedRepo{replies: tc.replies}
			got, err := activeReservedMicroUSD(context.Background(), repo, fixedReservationTenant,
				fixedReservationPolicy, "", fixedReservationPeriodStart(), fixedReservationNow)
			if err != nil {
				t.Fatalf("activeReservedMicroUSD: %v", err)
			}
			if got.established() != tc.wantEstablished {
				t.Fatalf("established = %v (%q), want %v", got.established(), got.Incomplete, tc.wantEstablished)
			}
			if tc.wantEstablished && got.MicroUSD != tc.want {
				t.Fatalf("total = %d, want %d", got.MicroUSD, tc.want)
			}
			if !tc.wantEstablished && got.MicroUSD != 0 {
				t.Fatalf("an unestablished total carried the figure %d; a caller must not be able to spend it", got.MicroUSD)
			}
		})
	}
}

// TestMaxReservationSeqRejectsABucketWhoseTokenIsNegative: the seq is a
// serialization token, and a negative one can only come from a wrap or a hand-written
// row. Reading it as a maximum would issue max+1 in a range where nothing collides.
func TestMaxReservationSeqRejectsABucketWhoseTokenIsNegative(t *testing.T) {
	for _, tc := range []struct {
		name      string
		rows      []model.Record
		want      int64
		wantIssue bool
	}{
		{name: "an empty bucket starts at zero", rows: nil},
		{name: "an ordinary maximum", rows: []model.Record{{colResvSeq: int64(41)}}, want: 41},
		{name: "the largest representable seq is still a maximum", rows: []model.Record{{colResvSeq: int64(math.MaxInt64)}}, want: math.MaxInt64},
		{name: "a negative seq is not a maximum", rows: []model.Record{{colResvSeq: int64(-1)}}, wantIssue: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &pagedRepo{replies: []pagedReply{{rows: tc.rows, page: model.Page{}}}}
			got, issue, err := maxReservationSeq(context.Background(), repo, model.NewID(), "", time.Now().UTC())
			if err != nil {
				t.Fatalf("maxReservationSeq: %v", err)
			}
			if (issue != "") != tc.wantIssue {
				t.Fatalf("issue = %q, want issue=%v", issue, tc.wantIssue)
			}
			if !tc.wantIssue && got != tc.want {
				t.Fatalf("max seq = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestSweepReportsNoCountWhenATransitionFails is the other half of the sweep's
// honesty: the enumeration finishes, the rows are real and expired, and the STORE
// breaks part way through the batch. The transaction rolls back, so nothing was
// swept — and the number the caller is handed has to say that, not "1".
func TestSweepReportsNoCountWhenATransitionFails(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	ctx := context.Background()
	fc := &fakeClock{t: time.Now().UTC()}
	m.clock = fc
	defer func(prev time.Duration) { reservationTTL = prev }(reservationTTL)
	reservationTTL = 30 * time.Second

	createBudget(t, st, tenant, "roomy", budgetSpec{
		Dimension: "global", Period: "monthly", LimitMicroUSD: 1000 * oneUSD, Action: "block",
	})
	createBudget(t, st, tenant, "roomy-provider", budgetSpec{
		Dimension: "provider", Key: "openai", Period: "monthly", LimitMicroUSD: 1000 * oneUSD, Action: "block",
	})
	res, err := m.ReserveBudget(ctx, tenant, SpendDims{ProviderRef: "openai"}, oneUSD)
	if err != nil || !res.Allowed {
		t.Fatalf("reserve: allowed=%v err=%v", res.Allowed, err)
	}
	if rows := countReservations(t, st, tenant); len(rows) != 2 {
		t.Fatalf("fixture reserved %d rows, want 2 (the case needs a batch that CAN break in the middle)", len(rows))
	}
	fc.advance(time.Minute) // both rows have lapsed

	forceUpdateFailureAfter(m, 1) // the first transition lands, the second fails
	swept, err := m.SweepExpiredReservations(ctx, tenant)
	if err == nil {
		t.Fatalf("sweep reported success (%d swept) although a transition failed", swept)
	}
	if swept != 0 {
		t.Fatalf("sweep reported %d rows swept after a rolled-back transaction", swept)
	}
	for _, r := range countReservations(t, st, tenant) {
		if r.String(colResvState) != resvStateActive {
			t.Fatalf("row %s is in state %q: the rolled-back sweep left a transition behind", r.String(model.ColID), r.String(colResvState))
		}
	}
}

// TestRealStorePagingSumsEveryPage is the control the scripted pagers cannot give:
// the rows, the cursor and the paging are the STORE's, not a fixture's. One row
// past listCap forces a second page, so a sum that dropped the continuation would
// come back short — and the whole completeness argument rests on that sum being
// every page, not the first.
func TestRealStorePagingSumsEveryPage(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	ctx := context.Background()
	now := time.Now().UTC()
	bid := createBudget(t, st, tenant, "global-block", budgetSpec{
		Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD, Action: "block",
	})
	const rows = listCap + 1
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(budgetReservationKind)
		if err != nil {
			return err
		}
		for i := 0; i < rows; i++ {
			if _, err := repo.Create(ctx, reservationRow(t, bid, "", "monthly", int64(i+1), 1, now, resvStateActive)); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed %d reservations: %v", rows, err)
	}

	var total reservedTotal
	pStart, _ := periodStart("monthly", now)
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		var derr error
		total, derr = dynamicReservedMicroUSD(ctx, sc, bid, "", pStart, now)
		return derr
	}); err != nil {
		t.Fatalf("dynamicReservedMicroUSD: %v", err)
	}
	if !total.established() {
		t.Fatalf("a two-page enumeration the store CAN finish was reported incomplete: %q", total.Incomplete)
	}
	if total.MicroUSD != rows {
		t.Fatalf("reserved total = %d µUSD over %d rows: the second page was dropped", total.MicroUSD, rows)
	}
	_ = m
}
