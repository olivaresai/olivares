// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"context"
	"errors"
	"math"
	"math/big"
	"strconv"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// -----------------------------------------------------------------------------
// The financial evidence, increment 1. These cases pin the ONE thing the alert history cannot say
// today: which kind of number a consumption figure is. Nothing here is wired, so
// every case drives the helper directly — the point is the algebra and the reading,
// not a path through the evaluator that increment 2 has not built yet.
//
// The fixtures are finite: scripted pages over the existing bare GenericRepo fake,
// and one real SQLite control that proves the reader's filters and columns match the
// actual read model rather than a fixture's idea of it.
// -----------------------------------------------------------------------------

// costScopeStub is the smallest Scope the strict reader needs: the tenant it is bound
// to and the cost-sample repository. Nothing else is implemented, so a reader that
// reached for anything else would panic rather than pass quietly.
type costScopeStub struct {
	store.Scope
	tenant model.TenantID
	repo   store.GenericRepo
	extErr error
}

func (s costScopeStub) Tenant() model.TenantID { return s.tenant }

func (s costScopeStub) Ext(kind model.Kind) (store.GenericRepo, error) {
	if s.extErr != nil {
		return nil, s.extErr
	}
	if kind != costSampleKind {
		return nil, errors.New("finops-test: the strict cost reader asked for the wrong kind: " + string(kind))
	}
	return s.repo, nil
}

// erroringCostRepo fails every List, so a read outage can be staged without a store.
type erroringCostRepo struct {
	store.GenericRepo
	err   error
	calls int
}

func (r *erroringCostRepo) List(context.Context, model.Query) ([]model.Record, model.Page, error) {
	r.calls++
	return nil, model.Page{}, r.err
}

// testWindow is the June 2026 month over a VERIFIED GLOBAL scope — the one case where
// no predicate is the correct predicate — using the same period arithmetic the
// evaluator uses.
func testWindow(tenant model.TenantID) strictCostWindow {
	start, hasLower := periodStart("monthly", baseTime)
	return strictCostWindow{
		Tenant: tenant, ScopeResolved: true, Start: start, HasStart: hasLower,
		End: periodEnd("monthly", start), Bounded: hasLower,
	}
}

// knownDynamic is an exactly enumerated obligation.
func knownDynamic(micro int64) dynamicComponent {
	return dynamicComponent{State: dynamicKnown, MicroUSD: micro}
}

// costRow builds one cost-sample row inside that window unless told otherwise.
func costRow(tenant model.TenantID, at time.Time, micro int64) model.Record {
	return model.Record{
		model.ColTenantID: tenant.String(),
		colOccurredAt:     model.NewTimestamp(at).String(),
		colCostMicroUSD:   micro,
	}
}

func costRows(tenant model.TenantID, at time.Time, micros ...int64) []model.Record {
	out := make([]model.Record, 0, len(micros))
	for _, m := range micros {
		out = append(out, costRow(tenant, at, m))
	}
	return out
}

func finalPage(rows []model.Record) pagedReply {
	return pagedReply{rows: rows, page: model.Page{}}
}

// -----------------------------------------------------------------------------
// The reader
// -----------------------------------------------------------------------------

func TestStrictCostReaderSeparatesACompleteReadFromEverythingElse(t *testing.T) {
	tenant := model.TenantID(model.NewID())
	at := baseTime
	one := costRows(tenant, at, 1)

	for _, tc := range []struct {
		name string
		// window overrides the June window when set.
		window   *strictCostWindow
		replies  []pagedReply
		complete bool
		total    string
		cause    amountCause
	}{
		{
			name:     "one final page with a credit in it",
			replies:  []pagedReply{finalPage(costRows(tenant, at, 12*oneUSD, -6*oneUSD))},
			complete: true, total: "6000000",
		},
		{
			// The final page is allowed to carry anything in its cursor: nobody
			// resumes from it.
			name: "the final page repeats a cursor nobody will use",
			replies: []pagedReply{
				{rows: one, page: model.Page{Cursor: "A", HasMore: true}},
				{rows: one, page: model.Page{Cursor: "A", HasMore: false}},
			},
			complete: true, total: "2",
		},
		{
			name: "exactly the page cap, and the last page says it is the last",
			replies: func() []pagedReply {
				out := make([]pagedReply, maxScanPages)
				for i := range out {
					out[i] = pagedReply{rows: one, page: model.Page{Cursor: "c" + strconv.Itoa(i+1), HasMore: true}}
				}
				out[maxScanPages-1].page = model.Page{}
				return out
			}(),
			complete: true, total: strconv.Itoa(maxScanPages),
		},
		{
			name: "the page cap with rows still to come",
			replies: func() []pagedReply {
				out := make([]pagedReply, maxScanPages)
				for i := range out {
					out[i] = pagedReply{rows: one, page: model.Page{Cursor: "c" + strconv.Itoa(i+1), HasMore: true}}
				}
				return out
			}(),
			cause: causeCostScanTruncated,
		},
		{
			name:    "more rows promised without a cursor",
			replies: []pagedReply{{rows: one, page: model.Page{HasMore: true}}},
			cause:   causeCostCursorMissing,
		},
		{
			name: "the cursor does not advance",
			replies: []pagedReply{
				{rows: one, page: model.Page{Cursor: "stuck", HasMore: true}},
				{rows: one, page: model.Page{Cursor: "stuck", HasMore: true}},
			},
			cause: causeCostCursorStalled,
		},
		{
			name: "a continuation cursor cycles back to one already used",
			replies: []pagedReply{
				{rows: one, page: model.Page{Cursor: "A", HasMore: true}},
				{rows: one, page: model.Page{Cursor: "B", HasMore: true}},
				{rows: one, page: model.Page{Cursor: "A", HasMore: true}},
				finalPage(one),
			},
			cause: causeCostCursorCycle,
		},
		{
			name:    "a row whose cost cell is not an integer",
			replies: []pagedReply{finalPage([]model.Record{{model.ColTenantID: tenant.String(), colOccurredAt: model.NewTimestamp(at).String(), colCostMicroUSD: "12"}})},
			cause:   causeCostRowMalformed,
		},
		{
			name:    "a row with no cost cell at all",
			replies: []pagedReply{finalPage([]model.Record{{model.ColTenantID: tenant.String(), colOccurredAt: model.NewTimestamp(at).String()}})},
			cause:   causeCostRowMalformed,
		},
		{
			name:    "a row whose timestamp cannot be parsed",
			replies: []pagedReply{finalPage([]model.Record{{model.ColTenantID: tenant.String(), colOccurredAt: "not-a-timestamp", colCostMicroUSD: int64(1)}})},
			cause:   causeCostRowMalformed,
		},
		{
			name:    "a row belonging to another tenant",
			replies: []pagedReply{finalPage([]model.Record{costRow(model.TenantID(model.NewID()), at, 1)})},
			cause:   causeCostRowOtherTenant,
		},
		{
			name:    "a row before the requested window",
			replies: []pagedReply{finalPage([]model.Record{costRow(tenant, baseTime.AddDate(0, -1, 0), 1)})},
			cause:   causeCostRowOutOfWindow,
		},
		{
			// The upper bound is exclusive: a sample at exactly End is the next
			// period's, and counting it in both inflates two totals at once.
			name: "a row exactly at the exclusive end of the window",
			replies: []pagedReply{finalPage([]model.Record{
				costRow(tenant, periodEnd("monthly", mustPeriodStart("monthly", baseTime)), 1),
			})},
			cause: causeCostRowOutOfWindow,
		},
		{
			name: "an empty window",
			window: func() *strictCostWindow {
				w := testWindow(tenant)
				w.End = w.Start
				return &w
			}(),
			replies: []pagedReply{finalPage(one)},
			cause:   causeWindowInvalid,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := testWindow(tenant)
			if tc.window != nil {
				w = *tc.window
			}
			repo := &pagedRepo{replies: tc.replies}
			got := readStrictCostTotal(context.Background(), costScopeStub{tenant: tenant, repo: repo}, w)
			t.Logf("complete=%v total=%v rows=%d pages=%d causes=%v err=%v", got.Complete, got.Total, got.Rows, got.Pages, got.Causes, got.Err)

			if got.Complete != tc.complete {
				t.Fatalf("complete = %v, want %v (causes %v)", got.Complete, tc.complete, got.Causes)
			}
			if tc.complete {
				if got.Total == nil || got.Total.String() != tc.total {
					t.Fatalf("total = %v, want %s", got.Total, tc.total)
				}
				if len(got.Causes) != 0 {
					t.Errorf("a complete read carried causes: %v", got.Causes)
				}
				return
			}
			if got.Total != nil {
				t.Fatalf("an incomplete read handed back a total (%v): a prefix is not a sum", got.Total)
			}
			if len(got.Causes) != 1 || got.Causes[0] != tc.cause {
				t.Fatalf("causes = %v, want exactly [%s]", got.Causes, tc.cause)
			}
		})
	}
}

func mustPeriodStart(period string, at time.Time) time.Time {
	start, _ := periodStart(period, at)
	return start
}

// TestStrictCostReaderRefusesAScopeItWasNotAskedFor: the tenant is carried in the
// request, so a scope bound to a different one is refused BEFORE any read. A reader
// that trusted the repository to be scoped correctly would have summed another
// tenant's ledger and called it complete.
func TestStrictCostReaderRefusesAScopeItWasNotAskedFor(t *testing.T) {
	asked := model.TenantID(model.NewID())
	bound := model.TenantID(model.NewID())
	repo := &pagedRepo{replies: []pagedReply{finalPage(costRows(bound, baseTime, 99*oneUSD))}}

	got := readStrictCostTotal(context.Background(), costScopeStub{tenant: bound, repo: repo}, testWindow(asked))
	t.Logf("complete=%v causes=%v list-calls=%d", got.Complete, got.Causes, repo.calls)

	if got.Complete || got.Total != nil {
		t.Fatalf("a scope bound to another tenant produced a total: %+v", got)
	}
	if len(got.Causes) != 1 || got.Causes[0] != causeScopeTenantMismatch {
		t.Fatalf("causes = %v, want [%s]", got.Causes, causeScopeTenantMismatch)
	}
	if repo.calls != 0 {
		t.Fatalf("the reader issued %d reads against the wrong tenant's scope", repo.calls)
	}
}

// TestStrictCostReaderReportsAReadFailureAsUnknown: an outage is not a zero and not a
// partial total. The underlying error is kept for the caller, outside the closed
// cause vocabulary that a later increment persists.
func TestStrictCostReaderReportsAReadFailureAsUnknown(t *testing.T) {
	tenant := model.TenantID(model.NewID())
	boom := errors.New("finops-test: cost read I/O failure")
	repo := &erroringCostRepo{err: boom}

	got := readStrictCostTotal(context.Background(), costScopeStub{tenant: tenant, repo: repo}, testWindow(tenant))
	if got.Complete || got.Total != nil {
		t.Fatalf("a failed read produced a total: %+v", got)
	}
	if len(got.Causes) != 1 || got.Causes[0] != causeCostReadFailed {
		t.Fatalf("causes = %v, want [%s]", got.Causes, causeCostReadFailed)
	}
	if !errors.Is(got.Err, boom) {
		t.Fatalf("err = %v, want the underlying read failure", got.Err)
	}

	// The same posture when the repository itself cannot be opened.
	got = readStrictCostTotal(context.Background(), costScopeStub{tenant: tenant, extErr: boom}, testWindow(tenant))
	if got.Complete || len(got.Causes) != 1 || got.Causes[0] != causeCostReadFailed {
		t.Fatalf("an unopenable repository must be an unknown read: %+v", got)
	}
}

// TestStrictCostReaderSumsWideAndInAnyOrder is the arithmetic the evaluator cannot do
// today, and each case names exactly what goes wrong without it: a checked int64
// accumulator REFUSES an intermediate that leaves the range even when the true total
// is representable — and whether it refuses depends on the order the credits arrive
// in — while an unchecked one silently returns an in-range number when the true total
// is outside int64.
func TestStrictCostReaderSumsWideAndInAnyOrder(t *testing.T) {
	tenant := model.TenantID(model.NewID())
	at := baseTime

	t.Run("an intermediate outside int64 whose true total is representable", func(t *testing.T) {
		// MaxInt64 + MaxInt64 leaves the range; the third row brings the true total
		// back to exactly MaxInt64. The CHECKED int64 accumulator this package already
		// uses refuses that sequence — correctly, by its own conservative contract —
		// so an evaluator built on it could not report the exact total that exists.
		if refused := sumInt64(math.MaxInt64, math.MaxInt64, -math.MaxInt64); refused.OK {
			t.Fatalf("the checked int64 control accepted the sequence (%+v): the case would prove nothing", refused)
		}
		rows := costRows(tenant, at, math.MaxInt64, math.MaxInt64, -math.MaxInt64)
		got := readStrictCostTotal(context.Background(), costScopeStub{tenant: tenant, repo: &pagedRepo{replies: []pagedReply{finalPage(rows)}}}, testWindow(tenant))
		if !got.Complete || got.Total.String() != strconv.FormatInt(math.MaxInt64, 10) {
			t.Fatalf("total = %v, want %d", got.Total, int64(math.MaxInt64))
		}
	})

	t.Run("the checked int64 accumulator is order dependent on the same terms", func(t *testing.T) {
		// Same three amounts, two orders: one is refused and the other is not. That is
		// the loss the contract names — a result that depends on when the credits
		// happen to be read — and it is why the wide accumulator exists.
		refused := sumInt64(math.MaxInt64, math.MaxInt64, -math.MaxInt64)
		accepted := sumInt64(math.MaxInt64, -math.MaxInt64, math.MaxInt64)
		if refused.OK || !accepted.OK || accepted.Value != math.MaxInt64 {
			t.Fatalf("the order-dependence control did not hold: refused=%+v accepted=%+v", refused, accepted)
		}
		forward := readStrictCostTotal(context.Background(), costScopeStub{tenant: tenant, repo: &pagedRepo{replies: []pagedReply{finalPage(costRows(tenant, at, math.MaxInt64, math.MaxInt64, -math.MaxInt64))}}}, testWindow(tenant))
		reverse := readStrictCostTotal(context.Background(), costScopeStub{tenant: tenant, repo: &pagedRepo{replies: []pagedReply{finalPage(costRows(tenant, at, math.MaxInt64, -math.MaxInt64, math.MaxInt64))}}}, testWindow(tenant))
		if !forward.Complete || !reverse.Complete || forward.Total.Cmp(reverse.Total) != 0 {
			t.Fatalf("the wide reader was order dependent: %v vs %v", forward.Total, reverse.Total)
		}
	})

	t.Run("a total that is mathematically outside int64", func(t *testing.T) {
		rows := costRows(tenant, at, math.MaxInt64, math.MaxInt64)
		got := readStrictCostTotal(context.Background(), costScopeStub{tenant: tenant, repo: &pagedRepo{replies: []pagedReply{finalPage(rows)}}}, testWindow(tenant))
		want := new(big.Int).Mul(big.NewInt(math.MaxInt64), big.NewInt(2))
		var naive int64
		for _, v := range []int64{math.MaxInt64, math.MaxInt64} {
			naive += v // an unchecked int64 accumulator: silently -2
		}
		if big.NewInt(naive).Cmp(want) == 0 {
			t.Fatalf("the unchecked int64 control did not misbehave (%d): the case would prove nothing", naive)
		}
		if !got.Complete || got.Total.Cmp(want) != 0 {
			t.Fatalf("total = %v, want %v", got.Total, want)
		}
		amount := classifyEffectiveAmount(effectiveAmountInputs{Cost: got, StaticKnown: true, Dynamic: knownDynamic(0)})
		if _, ok := amount.RepresentableInt64(); ok {
			t.Fatalf("a total beyond int64 reported itself representable: %s", amount.Decimal())
		}
		if amount.Decimal() != want.String() {
			t.Fatalf("decimal = %q, want %q: nothing may be saturated on the way out", amount.Decimal(), want.String())
		}
	})

	t.Run("credits in any order give the same total", func(t *testing.T) {
		forward := costRows(tenant, at, 12*oneUSD, -6*oneUSD, math.MaxInt64, -math.MaxInt64)
		reverse := costRows(tenant, at, -math.MaxInt64, math.MaxInt64, -6*oneUSD, 12*oneUSD)
		a := readStrictCostTotal(context.Background(), costScopeStub{tenant: tenant, repo: &pagedRepo{replies: []pagedReply{finalPage(forward)}}}, testWindow(tenant))
		b := readStrictCostTotal(context.Background(), costScopeStub{tenant: tenant, repo: &pagedRepo{replies: []pagedReply{finalPage(reverse)}}}, testWindow(tenant))
		if !a.Complete || !b.Complete || a.Total.Cmp(b.Total) != 0 || a.Total.String() != "6000000" {
			t.Fatalf("order changed the total: %v vs %v", a.Total, b.Total)
		}
	})
}

// -----------------------------------------------------------------------------
// The algebra
// -----------------------------------------------------------------------------

func completeCost(micro int64) strictCostTotal {
	return strictCostTotal{Complete: true, Total: big.NewInt(micro), Rows: 1, Pages: 1}
}

func partialCost(prefix int64, cause amountCause) strictCostTotal {
	// A partial read never carries a total — the prefix is only in Rows/Pages
	// diagnostics — which is exactly why it cannot become a bound.
	return strictCostTotal{Complete: false, Rows: 1, Pages: 1, Causes: []amountCause{cause}}
}

func TestEffectiveAmountClassification(t *testing.T) {
	for _, tc := range []struct {
		name  string
		in    effectiveAmountInputs
		class amountClass
		value string
		cause amountCause
	}{
		{
			name:  "cost, static and dynamic all established",
			in:    effectiveAmountInputs{Cost: completeCost(4 * oneUSD), StaticKnown: true, StaticMicroUSD: 3 * oneUSD, Dynamic: knownDynamic(2 * oneUSD)},
			class: amountExact, value: "9000000",
		},
		{
			name:  "a complete cost may be negative: credits stay legitimate",
			in:    effectiveAmountInputs{Cost: completeCost(-6 * oneUSD), StaticKnown: true, Dynamic: knownDynamic(0)},
			class: amountExact, value: "-6000000",
		},
		{
			// The R4 case: nothing was spent, 12 USD is reserved statically, and the
			// dynamic obligation was never OBSERVED — unobserved non-negative
			// uncertainty, which is the state that holds the invariant, not the
			// unreadable-ledger state (that one is indeterminate and proves nothing).
			// Obligations are non-negative, so 12 USD is a genuine floor.
			name:  "an obligation that was never observed still leaves a floor",
			in:    effectiveAmountInputs{Cost: completeCost(0), StaticKnown: true, StaticMicroUSD: 12 * oneUSD, Dynamic: unknownDynamic()},
			class: amountLowerBound, value: "12000000", cause: causeDynamicUnknown,
		},
		{
			name:  "a partial signed cost is unknown even with a large positive prefix",
			in:    effectiveAmountInputs{Cost: partialCost(12*oneUSD, causeCostScanTruncated), StaticKnown: true, StaticMicroUSD: 12 * oneUSD, Dynamic: knownDynamic(0)},
			class: amountUnknown, cause: causeCostScanTruncated,
		},
		{
			name:  "an unknown static reserve is unknown, not zero",
			in:    effectiveAmountInputs{Cost: completeCost(oneUSD), Dynamic: knownDynamic(0)},
			class: amountUnknown, cause: causeStaticUnknown,
		},
		{
			name:  "a negative static reserve is malformed configuration",
			in:    effectiveAmountInputs{Cost: completeCost(oneUSD), StaticKnown: true, StaticMicroUSD: -1, Dynamic: knownDynamic(0)},
			class: amountUnknown, cause: causeStaticNegative,
		},
		{
			// A negative reservation row is corruption, never a credit that lowers the
			// floor: taking it as one would hand out headroom nobody reserved.
			name:  "a negative dynamic reserve is corruption, not a credit",
			in:    effectiveAmountInputs{Cost: completeCost(oneUSD), StaticKnown: true, Dynamic: knownDynamic(-1)},
			class: amountUnknown, cause: causeDynamicNegative,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyEffectiveAmount(tc.in)
			t.Logf("class=%s value=%s causes=%v", got.Class, got.Decimal(), got.Causes)
			if got.Class != tc.class {
				t.Fatalf("class = %s, want %s (causes %v)", got.Class, tc.class, got.Causes)
			}
			if tc.class == amountUnknown {
				if got.Value != nil {
					t.Fatalf("an unknown amount carried the value %s", got.Decimal())
				}
			} else if got.Decimal() != tc.value {
				t.Fatalf("value = %s, want %s", got.Decimal(), tc.value)
			}
			if tc.cause != "" && (len(got.Causes) == 0 || got.Causes[0] != tc.cause) {
				t.Fatalf("causes = %v, want them to start with %s", got.Causes, tc.cause)
			}
			if tc.cause == "" && len(got.Causes) != 0 {
				t.Fatalf("an established amount carried causes: %v", got.Causes)
			}
		})
	}
}

// TestDynamicComponentAdaptsTheReservationLedgerWithoutChangingIt: the adapter reads
// the ledger's existing result and nothing more — a TYPED known total becomes known,
// and any incompleteness becomes the closed cause, without the ledger's free text.
//
// A4.2 CORRECTION, and the case that moved is the first one. It used to assert that
// `reservedTotal{MicroUSD: 7 * oneUSD}` — a value with NO state at all — adapted to
// an exact known reserve, because the adapter fell back to "Incomplete is empty
// means established". That fallback reads the absence of a diagnostic string as a
// positive finding, and it is what let the zero value `reservedTotal{}` become an
// exact reserve of zero. The producer sets the state; a value that does not carry one
// is unclassified, and unclassified proves nothing. The assertion is inverted here
// deliberately: the old expectation was the defect.
func TestDynamicComponentAdaptsTheReservationLedgerWithoutChangingIt(t *testing.T) {
	known := dynamicFromReservedTotal(reservedTotal{MicroUSD: 7 * oneUSD, State: reservedKnown})
	if !known.known() || known.MicroUSD != 7*oneUSD {
		t.Fatalf("a known reserved total did not survive the adapter: %+v", known)
	}
	for _, tc := range []struct {
		name string
		in   reservedTotal
	}{
		{name: "the zero value", in: reservedTotal{}},
		{name: "a figure with no state", in: reservedTotal{MicroUSD: 7 * oneUSD}},
		{name: "a state this adapter does not know", in: reservedTotal{State: reservedState("future_state")}},
	} {
		got := dynamicFromReservedTotal(tc.in)
		if got.State != dynamicIndeterminate || got.Cause != causeDynamicStateUnclassified {
			t.Fatalf("%s became %+v, want an indeterminate component with the unclassified cause", tc.name, got)
		}
		if got.carriesNonNegativeInvariant() {
			t.Fatalf("%s would carry a bound: %+v", tc.name, got)
		}
		// And the algebra refuses to build any figure on it.
		amount := classifyEffectiveAmount(effectiveAmountInputs{
			Cost:        strictCostTotal{Complete: true, Total: big.NewInt(oneUSD)},
			StaticKnown: true, Dynamic: got,
		})
		if amount.Class != amountUnknown || amount.Value != nil {
			t.Fatalf("%s produced the amount %+v; an unclassified reserve is not a zero", tc.name, amount)
		}
	}
	// A4-R2: an unestablished total is INDETERMINATE, not "unobserved". The ledger's
	// single free-text reason covers both an enumeration that stopped early and an
	// invariant it caught being violated, and this adapter will not tell them apart by
	// reading that prose. These carry the typed indeterminate state the producer sets.
	for _, incomplete := range []string{
		"the reservation ledger reported more rows without a cursor",
		"a reservation row holds a negative amount",
		"the reserved total is not representable",
	} {
		got := dynamicFromReservedTotal(reservedTotal{Incomplete: incomplete, State: reservedIndeterminate})
		if got.State != dynamicIndeterminate || got.Cause != causeDynamicUnverified {
			t.Fatalf("unestablished %q became %+v, want an indeterminate component", incomplete, got)
		}
		if got.carriesNonNegativeInvariant() {
			t.Fatalf("unestablished %q would carry a bound: %+v", incomplete, got)
		}
	}
}

// -----------------------------------------------------------------------------
// The threshold
// -----------------------------------------------------------------------------

func TestThresholdCrossingIsProvenOrItIsNot(t *testing.T) {
	lowerBound12 := classifyEffectiveAmount(effectiveAmountInputs{
		Cost: completeCost(0), StaticKnown: true, StaticMicroUSD: 12 * oneUSD, Dynamic: unknownDynamic(),
	})
	unknownFromPrefix := classifyEffectiveAmount(effectiveAmountInputs{
		Cost: partialCost(12*oneUSD, causeCostScanTruncated), StaticKnown: true, Dynamic: knownDynamic(0),
	})
	exact6 := classifyEffectiveAmount(effectiveAmountInputs{
		Cost: completeCost(6 * oneUSD), StaticKnown: true, Dynamic: knownDynamic(0),
	})

	for _, tc := range []struct {
		name      string
		amount    effectiveAmount
		limit     int64
		threshold float64
		want      crossingResult
	}{
		{
			// R4: the floor of 12 USD already exceeds a 10 USD limit, so the cap is a
			// real crossing even though the total was never established.
			name:   "a lower bound that reaches the limit proves the cap",
			amount: lowerBound12, limit: 10 * oneUSD, threshold: 1, want: crossingProven,
		},
		{
			// The same floor against a 20 USD limit proves nothing — and "unproven" is
			// not "within budget".
			name:   "a lower bound below the target proves nothing either way",
			amount: lowerBound12, limit: 20 * oneUSD, threshold: 1, want: crossingUnproven,
		},
		{
			// The false cap: 12 USD of prefix with an unread −6 USD credit behind it.
			// The prefix is not a floor, so no crossing may be fabricated from it.
			name:   "a partial signed prefix cannot fabricate a cap",
			amount: unknownFromPrefix, limit: 10 * oneUSD, threshold: 1, want: crossingUnproven,
		},
		{
			// The same ledger read completely: 12 − 6 = 6 USD, exactly below the limit.
			name:   "the same ledger read completely is exactly below the limit",
			amount: exact6, limit: 10 * oneUSD, threshold: 1, want: crossingNotReached,
		},
		{
			name:   "an exact amount on the threshold crosses it",
			amount: classifyEffectiveAmount(effectiveAmountInputs{Cost: completeCost(8 * oneUSD), StaticKnown: true, Dynamic: knownDynamic(0)}),
			limit:  10 * oneUSD, threshold: 0.8, want: crossingProven,
		},
		{
			name:   "one micro-USD below it does not",
			amount: classifyEffectiveAmount(effectiveAmountInputs{Cost: completeCost(8*oneUSD - 1), StaticKnown: true, Dynamic: knownDynamic(0)}),
			limit:  10 * oneUSD, threshold: 0.8, want: crossingNotReached,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := evaluateThresholdCrossing(tc.amount, tc.limit, tc.threshold)
			t.Logf("result=%s threshold=%q target=%s causes=%v", got.Result, got.Threshold, got.TargetDecimal, got.Causes)
			if got.Result != tc.want {
				t.Fatalf("result = %s, want %s", got.Result, tc.want)
			}
			if tc.want == crossingUnproven && len(got.Causes) == 0 {
				t.Errorf("an unproven decision must say why")
			}
		})
	}
}

// TestThresholdComparisonUsesTheConfiguredDecimalNotItsBinaryProduct is the case that
// makes the rational comparison worth its cost. 0.07 is not 0.07 in binary, and
// 0.07 × 10_000_000 in float64 is strictly GREATER than the exact 700_000 — so an
// amount landing exactly on the configured threshold is judged not to have crossed it
// by float arithmetic, and to have crossed it by the decimal the operator configured.
// (The header used to cite 0.1 × 30_000_000, which the body stopped using when that
// premise turned out to be exact in float64; the independent review named the stale
// text and it is fixed.)
func TestThresholdComparisonUsesTheConfiguredDecimalNotItsBinaryProduct(t *testing.T) {
	const limit = int64(10 * oneUSD)
	const threshold = 0.07
	const exactTarget = int64(700_000)
	// 0.07 is not 0.07 in binary: 0.07 x 10_000_000 in float64 is 700000.0000000001,
	// strictly ABOVE the exact target. An amount landing exactly on the threshold the
	// operator configured is therefore judged NOT to have crossed it by float
	// arithmetic, and to have crossed it by the decimal itself.
	if !(threshold*float64(limit) > float64(exactTarget)) {
		t.Fatalf("the float64 control did not misbehave (%.20f): the case would prove nothing", threshold*float64(limit))
	}
	amount := classifyEffectiveAmount(effectiveAmountInputs{
		Cost: completeCost(exactTarget), StaticKnown: true, Dynamic: knownDynamic(0),
	})
	got := evaluateThresholdCrossing(amount, limit, threshold)
	t.Logf("result=%s threshold=%q target=%s float-product=%.20f", got.Result, got.Threshold, got.TargetDecimal, threshold*float64(limit))
	if got.Result != crossingProven {
		t.Fatalf("result = %s, want proven: an amount exactly on the configured decimal threshold has crossed it", got.Result)
	}
	if got.Threshold != "0.07" || got.TargetDecimal != "700000" {
		t.Fatalf("threshold/target = %q/%q, want the configured decimal 0.07 and its exact target 700000", got.Threshold, got.TargetDecimal)
	}
	// One micro-USD below the exact target is still not a crossing.
	if below := evaluateThresholdCrossing(classifyEffectiveAmount(effectiveAmountInputs{
		Cost: completeCost(exactTarget - 1), StaticKnown: true, Dynamic: knownDynamic(0),
	}), limit, threshold); below.Result != crossingNotReached {
		t.Fatalf("one micro-USD below the target: result = %s, want not_reached", below.Result)
	}
}

// TestThresholdBoundariesAndInvalidValues: a fractional target that is not an integer
// is still compared exactly, and a threshold that is not a finite positive number
// produces a named cause instead of an invented percentage.
func TestThresholdBoundariesAndInvalidValues(t *testing.T) {
	exactly := func(micro int64) effectiveAmount {
		return classifyEffectiveAmount(effectiveAmountInputs{Cost: completeCost(micro), StaticKnown: true, Dynamic: knownDynamic(0)})
	}
	t.Run("a fractional target compares exactly", func(t *testing.T) {
		// 0.85 × 7_000_001 = 5_950_000.85 — not an integer number of micro-USD.
		const limit = int64(7_000_001)
		if got := evaluateThresholdCrossing(exactly(5_950_000), limit, 0.85); got.Result != crossingNotReached {
			t.Fatalf("5950000 vs 5950000.85: result = %s, want not_reached (target %q)", got.Result, got.TargetDecimal)
		}
		got := evaluateThresholdCrossing(exactly(5_950_001), limit, 0.85)
		if got.Result != crossingProven {
			t.Fatalf("5950001 vs 5950000.85: result = %s, want proven", got.Result)
		}
		if got.TargetDecimal != "5950000.85" {
			t.Fatalf("target = %q, want the exact 5950000.85 — never a rounded figure presented as exact", got.TargetDecimal)
		}
	})
	for _, tc := range []struct {
		name      string
		threshold float64
		limit     int64
		cause     amountCause
	}{
		{name: "NaN", threshold: math.NaN(), limit: 10 * oneUSD, cause: causeThresholdNotFinite},
		{name: "positive infinity", threshold: math.Inf(1), limit: 10 * oneUSD, cause: causeThresholdNotFinite},
		{name: "negative infinity", threshold: math.Inf(-1), limit: 10 * oneUSD, cause: causeThresholdNotFinite},
		{name: "zero", threshold: 0, limit: 10 * oneUSD, cause: causeThresholdOutOfRange},
		{name: "negative", threshold: -0.5, limit: 10 * oneUSD, cause: causeThresholdOutOfRange},
		{name: "a limit that is not positive", threshold: 0.8, limit: 0, cause: causeLimitNotPositive},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := evaluateThresholdCrossing(exactly(100*oneUSD), tc.limit, tc.threshold)
			if got.Result != crossingUnproven {
				t.Fatalf("result = %s, want unproven", got.Result)
			}
			if len(got.Causes) != 1 || got.Causes[0] != tc.cause {
				t.Fatalf("causes = %v, want [%s]", got.Causes, tc.cause)
			}
			if got.TargetDecimal != "" {
				t.Errorf("an unusable threshold produced the target %q", got.TargetDecimal)
			}
		})
	}
}

// -----------------------------------------------------------------------------
// Against the real read model
// -----------------------------------------------------------------------------

// TestStrictCostReaderAgainstTheRealReadModel is the control the scripted pages
// cannot give: real SQLite, the real ingest path, the real columns and the real
// filters. If the strict reader's window or provenance handling drifted from the
// evaluator's, this is where it shows — the fixture spends 12 USD, credits 6 USD and
// puts one sample in the previous month, and the answer must be an exact 6 USD.
func TestStrictCostReaderAgainstTheRealReadModel(t *testing.T) {
	m, _, tenant, _ := newFin(t)
	ctx := context.Background()
	m.clock = &fakeClock{t: baseTime}

	m.ingest(t, tenant, mkCost("openai", "gpt-x", "s-1", 10, 10, 12*oneUSD, baseTime))
	m.ingest(t, tenant, mkCost("openai", "gpt-x", "s-1", 0, 0, -6*oneUSD, baseTime.Add(time.Minute)))
	m.ingest(t, tenant, mkCost("openai", "gpt-x", "s-2", 5, 5, 99*oneUSD, baseTime.AddDate(0, -1, 0)))

	spec := budgetSpec{Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD, Action: "block"}
	spec.fillDefaults()
	w := strictCostWindowForBudget(tenant, spec, baseTime)

	var got strictCostTotal
	if err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		got = readStrictCostTotal(ctx, sc, w)
		return nil
	}); err != nil {
		t.Fatalf("view: %v", err)
	}
	t.Logf("complete=%v total=%v rows=%d pages=%d causes=%v", got.Complete, got.Total, got.Rows, got.Pages, got.Causes)

	if !got.Complete {
		t.Fatalf("a two-row window over a real store was reported incomplete: %v", got.Causes)
	}
	if got.Total.String() != "6000000" {
		t.Fatalf("total = %v, want 6000000 (12 USD spent, 6 USD credited, the previous month excluded)", got.Total)
	}
	if got.Rows != 2 {
		t.Fatalf("rows = %d, want 2: the window or the provenance filter does not match the evaluator's", got.Rows)
	}

	// And the window really is the evaluator's own period arithmetic.
	start, hasLower := periodStart("monthly", baseTime)
	if !w.HasStart || !w.Bounded || !w.Start.Equal(start) || !w.End.Equal(periodEnd("monthly", start)) || !hasLower {
		t.Fatalf("the window is not the budget's period: %+v", w)
	}
}

// -----------------------------------------------------------------------------
// A4-R1/R2/R3 — the three defects the independent review returned, as permanent
// cases. The staging is the reviewer's finite probe (`review_a4_contract_test.go`,
// preserved unchanged beside their report), adapted here so the corrections carry
// their own regression proof. Each case keeps its healthy control beside it: the
// point is never "refuse more", it is "refuse exactly what was not established".
// -----------------------------------------------------------------------------

// TestScopeMustBeResolvedBeforeAnyTotal is A4-R1. budgetSpec.sampleFilters returns nil
// for a global budget AND for a group or unsupported dimension, so a window built from
// it alone cannot tell "the whole tenant is the subject" from "the subject was never
// expressed". Read with nil filters, the second case sums every cost row in the tenant
// and reports it as the group's exact spend.
func TestScopeMustBeResolvedBeforeAnyTotal(t *testing.T) {
	m, _, tenant, _ := newFin(t)
	ctx := context.Background()
	m.clock = &fakeClock{t: baseTime}
	m.ingest(t, tenant, mkCost("openai", "gpt-x", "s-1", 1, 1, 12*oneUSD, baseTime))

	for _, tc := range []struct {
		name      string
		dimension string
		key       string
		resolved  bool
		filters   int
		total     string
		cause     amountCause
	}{
		{name: "a verified global scope needs no predicate", dimension: "global", resolved: true, total: "12000000"},
		{name: "a supported dimension carries its own predicate", dimension: "provider", key: "missing-scope", resolved: true, filters: 1, total: "0"},
		{name: "a matching supported dimension still sums", dimension: "provider", key: "openai", resolved: true, filters: 1, total: "12000000"},
		{name: "a user group is not resolved by an absent predicate", dimension: "user_group", key: "missing-scope", cause: causeScopeGroupUnresolved},
		{name: "an agent group is not resolved by an absent predicate", dimension: "agent_group", key: "missing-scope", cause: causeScopeGroupUnresolved},
		{name: "a dimension with no cost column is unsupported", dimension: "not-a-dimension", key: "x", cause: causeScopeDimensionUnsupported},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := budgetSpec{Dimension: tc.dimension, Key: tc.key, Period: "monthly", LimitMicroUSD: 10 * oneUSD, Action: "block"}
			spec.fillDefaults()
			w := strictCostWindowForBudget(tenant, spec, baseTime)

			var got strictCostTotal
			if err := m.data.View(ctx, tenant, func(sc store.Scope) error {
				got = readStrictCostTotal(ctx, sc, w)
				return nil
			}); err != nil {
				t.Fatalf("view: %v", err)
			}
			t.Logf("dim=%s key=%q resolved=%v filters=%v complete=%v total=%v causes=%v",
				tc.dimension, tc.key, w.ScopeResolved, w.Filters, got.Complete, got.Total, got.Causes)

			if w.ScopeResolved != tc.resolved || len(w.Filters) != tc.filters {
				t.Fatalf("window scope = resolved:%v filters:%v, want resolved:%v filters:%d", w.ScopeResolved, w.Filters, tc.resolved, tc.filters)
			}
			if !tc.resolved {
				if got.Complete || got.Total != nil {
					t.Fatalf("an unresolved scope produced a tenant-wide amount: %+v", got)
				}
				if len(got.Causes) != 1 || got.Causes[0] != tc.cause {
					t.Fatalf("causes = %v, want [%s]", got.Causes, tc.cause)
				}
				return
			}
			if !got.Complete || got.Total == nil || got.Total.String() != tc.total {
				t.Fatalf("complete=%v total=%v, want an exact %s", got.Complete, got.Total, tc.total)
			}
		})
	}
}

// TestAWindowThatNeverStatedItsScopeIsRefused: the zero value is unresolved, so a
// window assembled by hand — by a future caller, or by a test — cannot read the whole
// tenant by simply omitting the question.
func TestAWindowThatNeverStatedItsScopeIsRefused(t *testing.T) {
	tenant := model.TenantID(model.NewID())
	w := testWindow(tenant)
	w.ScopeResolved = false
	repo := &pagedRepo{replies: []pagedReply{finalPage(costRows(tenant, baseTime, 12*oneUSD))}}

	got := readStrictCostTotal(context.Background(), costScopeStub{tenant: tenant, repo: repo}, w)
	t.Logf("complete=%v total=%v causes=%v list-calls=%d", got.Complete, got.Total, got.Causes, repo.calls)
	if got.Complete || got.Total != nil {
		t.Fatalf("an unstated scope produced a total: %+v", got)
	}
	if len(got.Causes) != 1 || got.Causes[0] != causeScopeUnresolved {
		t.Fatalf("causes = %v, want [%s]", got.Causes, causeScopeUnresolved)
	}
	if repo.calls != 0 {
		t.Fatalf("the reader issued %d reads for a scope it could not express", repo.calls)
	}
}

// TestObservedDynamicCorruptionCannotProveABound is A4-R2, staged the way the reviewer
// staged it: a real −6 USD reservation row in real SQLite, read by the ACTUAL
// reservation reader. That reader returns an unestablished total, and treating "not
// established" as "merely unobserved" asserted the very non-negativity the data had
// just violated — and then published a 12 USD floor that crossed a 10 USD cap.
//
// The control beside it is the one that must NOT change: an obligation nobody observed
// keeps the invariant intact and keeps proving the R4 crossing.
func TestObservedDynamicCorruptionCannotProveABound(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	ctx := context.Background()
	id := createBudget(t, st, tenant, "corrupt-reserve", budgetSpec{
		Dimension: "global", Period: "total", LimitMicroUSD: 10 * oneUSD,
		ReservedMicroUSD: 12 * oneUSD, Action: "block",
	})
	seedReservation(t, st, tenant, reservationRow(t, id, "", "total", 1, -6*oneUSD, baseTime, resvStateActive))

	var reserved reservedTotal
	if err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		start, _ := periodStart("total", baseTime)
		var e error
		reserved, e = dynamicReservedMicroUSD(ctx, sc, id, "", start, baseTime)
		return e
	}); err != nil {
		t.Fatalf("view: %v", err)
	}
	if reserved.established() {
		t.Fatalf("the fixture did not produce an unestablished reserve: %+v", reserved)
	}

	for _, tc := range []struct {
		name     string
		dynamic  dynamicComponent
		class    amountClass
		crossing crossingResult
	}{
		{
			name:    "an obligation nobody observed still proves the floor",
			dynamic: unknownDynamic(), class: amountLowerBound, crossing: crossingProven,
		},
		{
			name:    "an obligation the ledger could not establish proves nothing",
			dynamic: dynamicFromReservedTotal(reserved), class: amountUnknown, crossing: crossingUnproven,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			amount := classifyEffectiveAmount(effectiveAmountInputs{
				Cost: completeCost(0), StaticKnown: true, StaticMicroUSD: 12 * oneUSD, Dynamic: tc.dynamic,
			})
			decision := evaluateThresholdCrossing(amount, 10*oneUSD, 1)
			t.Logf("reserved=%+v dynamic=%+v class=%s amount=%s crossing=%s causes=%v",
				reserved, tc.dynamic, amount.Class, amount.Decimal(), decision.Result, amount.Causes)

			if amount.Class != tc.class {
				t.Fatalf("class = %s, want %s", amount.Class, tc.class)
			}
			if decision.Result != tc.crossing {
				t.Fatalf("crossing = %s, want %s", decision.Result, tc.crossing)
			}
			if tc.class == amountLowerBound && amount.Decimal() != "12000000" {
				t.Fatalf("the R4 floor changed: %s", amount.Decimal())
			}
			if tc.class == amountUnknown && amount.Value != nil {
				t.Fatalf("an indeterminate obligation still carried the figure %s", amount.Decimal())
			}
		})
	}
}

// TestReaderChecksTheScopeAndProvenanceOfEveryRowItGetsBack is A4-R3. The reader sends
// the dimension predicate and the estimated-stream filter; it must also CHECK them on
// what comes back, or a repository that ignores either widens the ledger silently. The
// adversarial repository here is a deliberately faulted fixture, not a claim about the
// real store — A4-R1's case shows a real-store scope defect without any fake.
func TestReaderChecksTheScopeAndProvenanceOfEveryRowItGetsBack(t *testing.T) {
	tenant := model.TenantID(model.NewID())
	for _, tc := range []struct {
		name       string
		provider   string
		provenance any
		complete   bool
		cause      amountCause
	}{
		{name: "the requested provider on the estimated stream", provider: "openai", provenance: provenanceEstimated, complete: true},
		{name: "a legacy row with no provenance is still estimated", provider: "openai", provenance: nil, complete: true},
		{name: "a legacy row with an empty provenance is still estimated", provider: "openai", provenance: "", complete: true},
		{name: "another provider's row", provider: "other", provenance: provenanceEstimated, cause: causeCostRowOutOfScope},
		{name: "a billed row from the reconciliation stream", provider: "openai", provenance: provenanceBilled, cause: causeCostRowProvenance},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := costRow(tenant, baseTime, 12*oneUSD)
			row[colProviderRef] = tc.provider
			if tc.provenance != nil {
				row[colProvenance] = tc.provenance
			}
			w := testWindow(tenant)
			w.Filters = []model.Filter{eq(colProviderRef, "openai")}

			got := readStrictCostTotal(context.Background(), costScopeStub{tenant: tenant, repo: &pagedRepo{replies: []pagedReply{finalPage([]model.Record{row})}}}, w)
			t.Logf("provider=%s provenance=%v complete=%v total=%v causes=%v", tc.provider, tc.provenance, got.Complete, got.Total, got.Causes)

			if got.Complete != tc.complete {
				t.Fatalf("complete = %v, want %v (causes %v)", got.Complete, tc.complete, got.Causes)
			}
			if tc.complete {
				if got.Total.String() != "12000000" {
					t.Fatalf("a legitimate row was dropped: total = %v", got.Total)
				}
				return
			}
			if got.Total != nil {
				t.Fatalf("a rejected row still produced a total: %v", got.Total)
			}
			if len(got.Causes) != 1 || got.Causes[0] != tc.cause {
				t.Fatalf("causes = %v, want [%s]", got.Causes, tc.cause)
			}
		})
	}
}

// TestReaderRefusesAPredicateItCannotVerify: the reader only sends predicates it can
// re-check on the returned rows. A filter shape outside that closed set — another
// operator, an unknown column, a non-string value, two predicates on one column — is
// refused before the read rather than trusted to the store.
func TestReaderRefusesAPredicateItCannotVerify(t *testing.T) {
	tenant := model.TenantID(model.NewID())
	for _, tc := range []struct {
		name    string
		filters []model.Filter
	}{
		{name: "an operator other than equality", filters: []model.Filter{{Column: colProviderRef, Op: model.OpNe, Value: "openai"}}},
		{name: "a column that is not a budget dimension", filters: []model.Filter{eq(colSampleKey, "k")}},
		{name: "a value that is not a string", filters: []model.Filter{{Column: colProviderRef, Op: model.OpEq, Value: 7}}},
		{name: "two predicates on the same column", filters: []model.Filter{eq(colProviderRef, "a"), eq(colProviderRef, "b")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := testWindow(tenant)
			w.Filters = tc.filters
			repo := &pagedRepo{replies: []pagedReply{finalPage(costRows(tenant, baseTime, 12*oneUSD))}}

			got := readStrictCostTotal(context.Background(), costScopeStub{tenant: tenant, repo: repo}, w)
			t.Logf("filters=%v complete=%v causes=%v list-calls=%d", tc.filters, got.Complete, got.Causes, repo.calls)
			if got.Complete || got.Total != nil {
				t.Fatalf("an unverifiable predicate produced a total: %+v", got)
			}
			if len(got.Causes) != 1 || got.Causes[0] != causeScopePredicateUnsupported {
				t.Fatalf("causes = %v, want [%s]", got.Causes, causeScopePredicateUnsupported)
			}
			if repo.calls != 0 {
				t.Fatalf("the reader issued %d reads with a predicate it could not check", repo.calls)
			}
		})
	}
}

// TestSupportedScopeColumnsFollowTheDimensionMapping keeps the closed dimension list
// honest against the existing mapping it routes through: every supported dimension
// must still resolve to a column, the group dimensions must not, and a column outside
// the mapping is not a scope column.
func TestSupportedScopeColumnsFollowTheDimensionMapping(t *testing.T) {
	for _, dim := range supportedScopeDimensions {
		col := dimensionColumn(dim)
		if col == "" || !supportedScopeColumn(col) {
			t.Errorf("supported dimension %q maps to %q, which is not a scope column", dim, col)
		}
	}
	for _, dim := range []string{"user_group", "agent_group"} {
		if dimensionColumn(dim) != "" {
			t.Errorf("group dimension %q now has a cost column: the refusal in strictCostScope needs revisiting", dim)
		}
	}
	for _, col := range []string{"", colSampleKey, colOccurredAt, colCostMicroUSD, colProvenance} {
		if supportedScopeColumn(col) {
			t.Errorf("column %q is not a budget dimension column but was accepted as one", col)
		}
	}
}

// TestDimensionCellTypeIsCheckedBeforeItsValue is the residual A4-R3 case. Record.String
// returns "" for a cell that is absent, null OR of the wrong type, and the scope builder
// deliberately admits an empty dimension key — so a present int64(7) in provider_ref read
// as "" and SATISFIED a requested provider_ref == "", producing a complete exact total
// from a row that could not establish the equality at all. The staging is the reviewer's
// finite probe (`review_empty_dimension_test.go`, preserved unchanged beside their
// report), extended here with the non-empty, absent and null boundaries.
func TestDimensionCellTypeIsCheckedBeforeItsValue(t *testing.T) {
	tenant := model.TenantID(model.NewID())
	const absent = "\x00absent\x00" // a sentinel this table uses to omit the cell entirely

	for _, tc := range []struct {
		name string
		// key is the dimension value the read is scoped with.
		key string
		// cell is what the repository hands back for that column; absent omits it and
		// nil is a null cell.
		cell     any
		micro    int64
		complete bool
		cause    amountCause
	}{
		{name: "an empty dimension key met by an empty string", key: "", cell: "", micro: 12 * oneUSD, complete: true},
		{name: "the same row carrying a signed credit", key: "", cell: "", micro: -6 * oneUSD, complete: true},
		{name: "an ordinary dimension value", key: "openai", cell: "openai", micro: 12 * oneUSD, complete: true},
		{
			// The defect: not a string, so it cannot BE the empty string.
			name: "a present integer cell is not the empty string",
			key:  "", cell: int64(7), micro: 12 * oneUSD, cause: causeCostRowDimensionMalformed,
		},
		{
			name: "a present integer cell is not an ordinary value either",
			key:  "openai", cell: int64(7), micro: 12 * oneUSD, cause: causeCostRowDimensionMalformed,
		},
		{
			name: "a present cell of another text-ish type is still malformed",
			key:  "openai", cell: []byte("openai"), micro: 12 * oneUSD, cause: causeCostRowDimensionMalformed,
		},
		{
			// Absent and null are legitimate storage states, not corruption: the row
			// simply cannot be shown to satisfy the equality, so it is out of scope. A
			// real store would not have returned it for this predicate either.
			name: "an absent cell cannot satisfy an empty key",
			key:  "", cell: absent, micro: 12 * oneUSD, cause: causeCostRowOutOfScope,
		},
		{
			name: "a null cell cannot satisfy an empty key",
			key:  "", cell: nil, micro: 12 * oneUSD, cause: causeCostRowOutOfScope,
		},
		{
			name: "a string that simply does not match is out of scope, not malformed",
			key:  "openai", cell: "other", micro: 12 * oneUSD, cause: causeCostRowOutOfScope,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := costRow(tenant, baseTime, tc.micro)
			row[colProvenance] = provenanceEstimated
			if s, ok := tc.cell.(string); !ok || s != absent {
				row[colProviderRef] = tc.cell
			}
			w := testWindow(tenant)
			w.Filters = []model.Filter{eq(colProviderRef, tc.key)}
			repo := &pagedRepo{replies: []pagedReply{finalPage([]model.Record{row})}}

			got := readStrictCostTotal(context.Background(), costScopeStub{tenant: tenant, repo: repo}, w)
			t.Logf("key=%q cell=%v (%T) micro=%d complete=%v total=%v causes=%v",
				tc.key, tc.cell, tc.cell, tc.micro, got.Complete, got.Total, got.Causes)

			if got.Complete != tc.complete {
				t.Fatalf("complete = %v, want %v (causes %v)", got.Complete, tc.complete, got.Causes)
			}
			if tc.complete {
				if got.Total == nil || got.Total.Int64() != tc.micro {
					t.Fatalf("total = %v, want the exact signed %d", got.Total, tc.micro)
				}
				if len(got.Causes) != 0 {
					t.Errorf("a complete read carried causes: %v", got.Causes)
				}
				return
			}
			if got.Total != nil {
				t.Fatalf("a row that could not satisfy the predicate still produced a total: %v", got.Total)
			}
			if len(got.Causes) != 1 || got.Causes[0] != tc.cause {
				t.Fatalf("causes = %v, want [%s]", got.Causes, tc.cause)
			}
		})
	}
}

// TestTenantCellIsCheckedAsEvidenceNotCoerced is the last same-class case. The tenant
// guard used to read `r.String(model.ColTenantID)` and skip the comparison whenever that
// came back empty — which Record.String does for a missing, null or wrongly typed cell,
// and for a genuinely empty one. A row with valid amount, window, dimension and
// provenance therefore reached the accumulator with no usable tenant evidence, and on a
// final page the reader published a complete exact sum over it.
//
// The scope's tenant is never filled into such a row. An empty base tenant is not the
// permitted empty DIMENSION key: tenant_id is the engine's injected base column, always
// projected and always bound in the query, so absent/null/non-string/empty is a fault in
// what came back. This is a malformed-repository integrity contract at the helper
// boundary, not evidence that SQL ever returned another tenant's row.
func TestTenantCellIsCheckedAsEvidenceNotCoerced(t *testing.T) {
	tenant := model.TenantID(model.NewID())
	other := model.TenantID(model.NewID())
	const absent = "\x00absent\x00" // this table's sentinel for omitting the cell

	for _, tc := range []struct {
		name     string
		cell     any
		micro    int64
		complete bool
		cause    amountCause
	}{
		{name: "the requested tenant, positive cost", cell: tenant.String(), micro: 12 * oneUSD, complete: true},
		{name: "the requested tenant, signed credit", cell: tenant.String(), micro: -6 * oneUSD, complete: true},
		{name: "another tenant's row", cell: other.String(), micro: 12 * oneUSD, cause: causeCostRowOtherTenant},
		{name: "no tenant cell at all", cell: absent, micro: 12 * oneUSD, cause: causeCostRowTenantMalformed},
		{name: "a null tenant cell", cell: nil, micro: 12 * oneUSD, cause: causeCostRowTenantMalformed},
		{name: "an integer where the tenant should be", cell: int64(7), micro: 12 * oneUSD, cause: causeCostRowTenantMalformed},
		{name: "an empty tenant string", cell: "", micro: 12 * oneUSD, cause: causeCostRowTenantMalformed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Everything except the tenant cell is valid: the window, the cost, the
			// estimated provenance and the requested dimension all match, so only the
			// tenant evidence can decide the outcome.
			row := costRow(tenant, baseTime, tc.micro)
			row[colProvenance] = provenanceEstimated
			row[colProviderRef] = "openai"
			delete(row, model.ColTenantID)
			if s, ok := tc.cell.(string); !ok || s != absent {
				row[model.ColTenantID] = tc.cell
			}
			w := testWindow(tenant)
			w.Filters = []model.Filter{eq(colProviderRef, "openai")}
			repo := &pagedRepo{replies: []pagedReply{finalPage([]model.Record{row})}}

			got := readStrictCostTotal(context.Background(), costScopeStub{tenant: tenant, repo: repo}, w)
			t.Logf("cell=%v (%T) micro=%d complete=%v total=%v causes=%v",
				tc.cell, tc.cell, tc.micro, got.Complete, got.Total, got.Causes)

			if got.Complete != tc.complete {
				t.Fatalf("complete = %v, want %v (causes %v)", got.Complete, tc.complete, got.Causes)
			}
			if tc.complete {
				if got.Total == nil || got.Total.Int64() != tc.micro {
					t.Fatalf("total = %v, want the exact signed %d", got.Total, tc.micro)
				}
				if len(got.Causes) != 0 {
					t.Errorf("a complete read carried causes: %v", got.Causes)
				}
				return
			}
			if got.Total != nil {
				t.Fatalf("a row without usable tenant evidence still produced a total: %v", got.Total)
			}
			if len(got.Causes) != 1 || got.Causes[0] != tc.cause {
				t.Fatalf("causes = %v, want [%s]", got.Causes, tc.cause)
			}
		})
	}
}
