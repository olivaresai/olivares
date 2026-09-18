// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"context"
	"errors"
	"math"
	"strconv"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// -----------------------------------------------------------------------------
// A4.2 groups 1, 5, 6 and 10: the typed dynamic producer, the exact decision, the
// status projection and the R1–R3 controls that cross the changed reservation
// signature. Fixtures are finite: scripted pages over the bare repository fake and
// small real-SQLite ledgers, never fabricated volume.
// -----------------------------------------------------------------------------

// reservationRepoStub answers the reservation repository's List from a script, so a
// paging outcome can be staged exactly.
type reservationRepoStub struct {
	store.GenericRepo
	replies []pagedReply
	// script, when set, answers every call instead of the reply list. It is what a
	// case that needs MANY pages (the page cap) uses, so the fixture stays finite.
	script func(call int) ([]model.Record, model.Page)
	err    error
	calls  int
}

func (r *reservationRepoStub) List(_ context.Context, q model.Query) ([]model.Record, model.Page, error) {
	r.calls++
	if r.err != nil {
		return nil, model.Page{}, r.err
	}
	if r.script != nil {
		rows, page := r.script(r.calls - 1)
		return rows, page, nil
	}
	if len(q.Sort) > 0 || r.calls > len(r.replies) {
		last := r.replies[len(r.replies)-1]
		return last.rows, last.page, nil
	}
	reply := r.replies[r.calls-1]
	return reply.rows, reply.page, nil
}

// forcedCostData replaces the COST repository's List on both the read and the write
// path. The alert evaluation runs inside the ingestion's Mutate, so a fixture that
// only wraps View (the older forceAggregateResult) never reaches it — and the rows
// it serves must carry the tenant, window and provenance cells the strict reader
// checks, or the staging would only prove that a malformed row is rejected.
type forcedCostData struct {
	api.ModuleData
	script func(call int, q model.Query) ([]model.Record, model.Page)
	calls  *int
}

func (d forcedCostData) View(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	return d.ModuleData.View(ctx, tenant, func(sc store.Scope) error {
		return fn(forcedCostScope{Scope: sc, data: d})
	})
}

func (d forcedCostData) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	return d.ModuleData.Mutate(ctx, tenant, func(sc store.Scope) error {
		return fn(forcedCostScope{Scope: sc, data: d})
	})
}

type forcedCostScope struct {
	store.Scope
	data forcedCostData
}

// LockTransaction forwards the REAL scope's optional capability. Embedding only
// store.Scope hides the optional methods a scope carries, so a fixture that wraps a
// scope silently removes them — and the alert writer fails closed when its lock is
// missing, which would turn every ingestion in this file into a refusal that proves
// nothing about the case under test. Forwarding is the honest wrapper: the lock a
// case observes is the one the real store took, not a pretend one.
func (s forcedCostScope) LockTransaction(ctx context.Context, key string) error {
	locker, ok := s.Scope.(store.TransactionLocker)
	if !ok {
		return errors.New("finops-test: wrapped scope provides no transaction lock")
	}
	return locker.LockTransaction(ctx, key)
}

func (s forcedCostScope) Ext(kind model.Kind) (store.GenericRepo, error) {
	repo, err := s.Scope.Ext(kind)
	if err != nil || kind != costSampleKind {
		return repo, err
	}
	return forcedCostRepo{GenericRepo: repo, data: s.data}, nil
}

type forcedCostRepo struct {
	store.GenericRepo
	data forcedCostData
}

func (r forcedCostRepo) List(ctx context.Context, q model.Query) ([]model.Record, model.Page, error) {
	// The ingest path also LOOKS UP a sample by its natural key before writing; that
	// query is a single-row dedup probe and must reach the real repository, or the
	// fixture would silently change what the ingestion does.
	for _, f := range q.Filters {
		if f.Column == colSampleKey {
			return r.GenericRepo.List(ctx, q)
		}
	}
	call := *r.data.calls
	*r.data.calls++
	rows, page := r.data.script(call, q)
	return rows, page, nil
}

// forceCostPages installs that fixture and returns the call counter.
func forceCostPages(m *Module, script func(call int, q model.Query) ([]model.Record, model.Page)) *int {
	calls := new(int)
	m.UseData(forcedCostData{ModuleData: m.data, script: script, calls: calls})
	return calls
}

// costSampleRow is one cost row with every cell the strict reader validates.
func costSampleRow(tenant model.TenantID, at time.Time, micro int64) model.Record {
	return model.Record{
		model.ColTenantID: tenant.String(),
		colOccurredAt:     model.NewTimestamp(at).String(),
		colCostMicroUSD:   micro,
		colProvenance:     provenanceEstimated,
	}
}

// liveReservationRow is one ACTIVE, in-scope, non-negative reservation row as the
// ledger's own reader expects to see it — INCLUDING its tenant cell, which the rows
// in this file used to omit. That omission is what let the full-page case be
// classified `known`: the producer summed rows carrying no tenant evidence at all.
func liveReservationRow(tenant model.TenantID, policy model.ID, scopeKey string, pStart, now time.Time, amount int64) model.Record {
	return model.Record{
		model.ColTenantID:  tenant.String(),
		colResvPolicyRef:   policy.String(),
		colResvScopeKey:    scopeKey,
		colResvPeriodStart: model.NewTimestamp(pStart).String(),
		colResvState:       resvStateActive,
		colResvExpiresAt:   model.NewTimestamp(now.Add(time.Hour)).String(),
		colResvAmount:      amount,
	}
}

// TestTypedDynamicProducerClassifiesWhatItObserved is A4.2 group 1. The producer's
// three answers are decided by what the rows show, and the paging outcome alone
// never grants the non-negative invariant: a prefix carrying a negative or
// malformed amount is indeterminate even though its enumeration merely stopped.
func TestTypedDynamicProducerClassifiesWhatItObserved(t *testing.T) {
	policy := model.NewID()
	tenant := model.TenantID(model.NewID().String())
	other := model.TenantID(model.NewID().String())
	now := baseTime
	pStart, _ := periodStart("monthly", now)
	valid := liveReservationRow(tenant, policy, "", pStart, now, 3*oneUSD)

	corrupt := func(mutate func(model.Record)) model.Record {
		r := liveReservationRow(tenant, policy, "", pStart, now, oneUSD)
		mutate(r)
		return r
	}
	// A page-cap script: every page promises more and hands back a fresh cursor, so
	// the scan runs to the bounded page budget instead of ending. It is the one paging
	// outcome the delivered table did not carry, and it is finite by construction —
	// maxScanPages pages of ONE row, not a fabricated ledger.
	pageCapScript := func(call int) ([]model.Record, model.Page) {
		return []model.Record{valid}, model.Page{Cursor: "page-" + strconv.Itoa(call+1), HasMore: true}
	}

	for _, tc := range []struct {
		name    string
		replies []pagedReply
		script  func(call int) ([]model.Record, model.Page)
		want    reservedState
		scan    reservationScanState
		fault   reservationFault
		micro   int64
	}{
		{
			name:    "a legitimate final page is exactly known",
			replies: []pagedReply{{rows: []model.Record{valid, valid}, page: model.Page{}}},
			want:    reservedKnown, scan: resvScanComplete, micro: 6 * oneUSD,
		},
		{
			name:    "more rows promised without a cursor, prefix valid",
			replies: []pagedReply{{rows: []model.Record{valid}, page: model.Page{HasMore: true}}},
			want:    reservedNonNegativeUnknown, scan: resvScanCursorMissing,
		},
		{
			name: "a cursor that does not advance, prefix valid",
			replies: []pagedReply{
				{rows: []model.Record{valid}, page: model.Page{Cursor: "stuck", HasMore: true}},
				{rows: []model.Record{valid}, page: model.Page{Cursor: "stuck", HasMore: true}},
			},
			want: reservedNonNegativeUnknown, scan: resvScanCursorStalled,
		},
		{
			name: "a cursor that returns to a page already read",
			replies: []pagedReply{
				{rows: []model.Record{valid}, page: model.Page{Cursor: "A", HasMore: true}},
				{rows: []model.Record{valid}, page: model.Page{Cursor: "B", HasMore: true}},
				{rows: []model.Record{valid}, page: model.Page{Cursor: "A", HasMore: true}},
			},
			want: reservedNonNegativeUnknown, scan: resvScanCursorCycle,
		},
		{
			// The order that matters: the enumeration stopped AND a row contradicts the
			// domain. The contradiction wins, because the invariant is what a bound
			// would rest on.
			name:    "a negative amount inside an incomplete prefix",
			replies: []pagedReply{{rows: []model.Record{corrupt(func(r model.Record) { r[colResvAmount] = int64(-1) })}, page: model.Page{HasMore: true}}},
			want:    reservedIndeterminate, scan: resvScanCursorMissing, fault: resvFaultRowAmountNegative,
		},
		{
			name:    "a malformed amount inside an incomplete prefix",
			replies: []pagedReply{{rows: []model.Record{corrupt(func(r model.Record) { r[colResvAmount] = "12" })}, page: model.Page{HasMore: true}}},
			want:    reservedIndeterminate, scan: resvScanCursorMissing, fault: resvFaultRowAmountMalformed,
		},
		{
			name:    "a row that does not evidence the requested scope",
			replies: []pagedReply{{rows: []model.Record{corrupt(func(r model.Record) { r[colResvScopeKey] = "somebody-else" })}, page: model.Page{}}},
			want:    reservedIndeterminate, scan: resvScanComplete, fault: resvFaultRowOutOfScope,
		},
		{
			name:    "a row whose validity cannot be read",
			replies: []pagedReply{{rows: []model.Record{corrupt(func(r model.Record) { r[colResvExpiresAt] = "not-a-timestamp" })}, page: model.Page{}}},
			want:    reservedIndeterminate, scan: resvScanComplete, fault: resvFaultRowOutOfScope,
		},
		{
			name: "a sum that does not fit",
			replies: []pagedReply{{rows: []model.Record{
				liveReservationRow(tenant, policy, "", pStart, now, math.MaxInt64),
				liveReservationRow(tenant, policy, "", pStart, now, math.MaxInt64),
			}, page: model.Page{}}},
			want: reservedIndeterminate, scan: resvScanComplete, fault: resvFaultSumUnrepresentable,
		},
		{
			// The paging outcome the delivered table omitted: the cap is REACHED with
			// rows still promised. Every observed row is valid, so the invariant — and
			// only the invariant — survives.
			name:   "the page cap is reached with rows still to come",
			script: pageCapScript,
			want:   reservedNonNegativeUnknown, scan: resvScanPageCap,
		},
		// The four tenant cases. The row's own tenant evidence is checked before its
		// amount, in both a final page (which would otherwise publish an exact total)
		// and an incomplete prefix (which would otherwise publish the invariant).
		{
			name: "a row carrying no tenant cell, final page",
			replies: []pagedReply{{rows: []model.Record{corrupt(func(r model.Record) {
				delete(r, model.ColTenantID)
			})}, page: model.Page{}}},
			want: reservedIndeterminate, scan: resvScanComplete, fault: resvFaultRowTenantMalformed,
		},
		{
			name: "a row whose tenant cell is null, incomplete prefix",
			replies: []pagedReply{{rows: []model.Record{corrupt(func(r model.Record) {
				r[model.ColTenantID] = nil
			})}, page: model.Page{HasMore: true}}},
			want: reservedIndeterminate, scan: resvScanCursorMissing, fault: resvFaultRowTenantMalformed,
		},
		{
			name: "a row whose tenant cell is not text",
			replies: []pagedReply{{rows: []model.Record{corrupt(func(r model.Record) {
				r[model.ColTenantID] = int64(7)
			})}, page: model.Page{}}},
			want: reservedIndeterminate, scan: resvScanComplete, fault: resvFaultRowTenantMalformed,
		},
		{
			name: "a row belonging to another tenant",
			replies: []pagedReply{{rows: []model.Record{corrupt(func(r model.Record) {
				r[model.ColTenantID] = other.String()
			})}, page: model.Page{}}},
			want: reservedIndeterminate, scan: resvScanComplete, fault: resvFaultRowOtherTenant,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &reservationRepoStub{replies: tc.replies, script: tc.script}
			got, err := activeReservedMicroUSD(context.Background(), repo, tenant, policy, "", pStart, now)
			if err != nil {
				t.Fatalf("a classification must not be an error: %v", err)
			}
			t.Logf("state=%s scan=%s fault=%s micro=%d incomplete=%q", got.State, got.Scan, got.Fault, got.MicroUSD, got.Incomplete)

			if got.State != tc.want || got.Scan != tc.scan || got.Fault != tc.fault {
				t.Fatalf("state=%s scan=%s fault=%s, want %s/%s/%s", got.State, got.Scan, got.Fault, tc.want, tc.scan, tc.fault)
			}
			if got.MicroUSD != tc.micro {
				t.Fatalf("micro = %d, want %d (a prefix sum is never published)", got.MicroUSD, tc.micro)
			}
			if (got.State == reservedKnown) != got.established() {
				t.Fatalf("established() = %v disagrees with state %s: admission's contract moved", got.established(), got.State)
			}
			// And the classification the financial evaluation receives.
			dyn := dynamicFromReservedTotal(got)
			switch tc.want {
			case reservedKnown:
				if !dyn.known() || dyn.MicroUSD != tc.micro {
					t.Fatalf("dynamic = %+v, want a known %d", dyn, tc.micro)
				}
			case reservedNonNegativeUnknown:
				if dyn.State != dynamicNonNegativeUnknown || !dyn.carriesNonNegativeInvariant() {
					t.Fatalf("dynamic = %+v, want a non-negative unknown that can carry a bound", dyn)
				}
			default:
				if dyn.State != dynamicIndeterminate || dyn.carriesNonNegativeInvariant() {
					t.Fatalf("dynamic = %+v, want an indeterminate that cannot carry a bound", dyn)
				}
			}
		})
	}
}

// TestTypedDynamicProducerKeepsARealErrorAnError: an outage is not one of the three
// classifications. It stays an error so the caller's transaction can abort.
func TestTypedDynamicProducerKeepsARealErrorAnError(t *testing.T) {
	boom := errors.New("finops-test: reservation read I/O failure")
	repo := &reservationRepoStub{err: boom}
	got, err := activeReservedMicroUSD(context.Background(), repo, model.TenantID(model.NewID().String()), model.NewID(), "", baseTime, baseTime)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the underlying read failure", err)
	}
	if got.State != "" || got.MicroUSD != 0 {
		t.Fatalf("a failed read produced a classification: %+v", got)
	}
}

// TestSharedEvaluationDecidesExactlyAndNeverFromAPrefix is A4.2 group 5 with the
// group 3 credit control beside it. The decision runs on the rational target, not
// on a float64 product, and a signed prefix cannot become a total.
func TestSharedEvaluationDecidesExactlyAndNeverFromAPrefix(t *testing.T) {
	t.Run("a fractional threshold decides on the exact rational", func(t *testing.T) {
		m, st, tenant, _ := newFin(t)
		m.clock = &fakeClock{t: baseTime}
		// 0.07 x 10 USD is exactly 700000; in float64 the same product is
		// 700000.0000000001, which would call an amount landing exactly on the
		// configured threshold "not crossed".
		if !(0.07*float64(10*oneUSD) > 700_000) {
			t.Fatalf("the float64 control did not misbehave: the case would prove nothing")
		}
		id := createBudget(t, st, tenant, "fractional", budgetSpec{
			Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD,
			Action: "block", Thresholds: []float64{0.07},
		})
		m.ingest(t, tenant, mkCost("openai", "gpt-x", "s-1", 1, 1, 700_000, baseTime))

		eval := evaluateOne(t, m, tenant, id, baseTime)
		t.Logf("class=%s amount=%s decisions=%+v", eval.Amount.Class, eval.Amount.Decimal(), eval.Thresholds)
		if eval.Amount.Class != amountExact || eval.Amount.Decimal() != "700000" {
			t.Fatalf("amount = %s/%s, want an exact 700000", eval.Amount.Class, eval.Amount.Decimal())
		}
		if len(eval.Thresholds) != 1 || eval.Thresholds[0].Decision.Result != crossingProven {
			t.Fatalf("decision = %+v, want proven on the configured decimal", eval.Thresholds)
		}
		if eval.Thresholds[0].Decision.TargetDecimal != "700000" {
			t.Fatalf("target = %q, want the exact 700000", eval.Thresholds[0].Decision.TargetDecimal)
		}
	})

	t.Run("a credit that cancels an intermediate keeps the wide exact total", func(t *testing.T) {
		m, st, tenant, _ := newFin(t)
		m.clock = &fakeClock{t: baseTime}
		id := createBudget(t, st, tenant, "credited", budgetSpec{
			Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD, Action: "block",
		})
		// The checked int64 accumulator refuses this order; the wide sum does not.
		if refused := sumInt64(math.MaxInt64, math.MaxInt64, -math.MaxInt64); refused.OK {
			t.Fatalf("the checked control accepted the sequence: the case would prove nothing")
		}
		m.ingest(t, tenant, mkCost("openai", "gpt-x", "a", 1, 1, math.MaxInt64, baseTime))
		m.ingest(t, tenant, mkCost("openai", "gpt-x", "b", 1, 1, math.MaxInt64, baseTime.Add(time.Minute)))
		m.ingest(t, tenant, mkCost("openai", "gpt-x", "c", 1, 1, -math.MaxInt64, baseTime.Add(2*time.Minute)))

		eval := evaluateOne(t, m, tenant, id, baseTime)
		t.Logf("class=%s amount=%s", eval.Amount.Class, eval.Amount.Decimal())
		if eval.Amount.Class != amountExact || eval.Amount.Decimal() != "9223372036854775807" {
			t.Fatalf("amount = %s/%s, want the exact wide total", eval.Amount.Class, eval.Amount.Decimal())
		}
		if _, ok := eval.Amount.RepresentableInt64(); !ok {
			t.Fatalf("MaxInt64 must still be representable")
		}
	})

	t.Run("a signed cost prefix proves nothing", func(t *testing.T) {
		m, st, tenant, _ := newFin(t)
		m.clock = &fakeClock{t: baseTime}
		id := createBudget(t, st, tenant, "prefix", budgetSpec{
			Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD, Action: "block",
		})
		// The cost read stops early: the unread page could carry the credit that
		// cancels the prefix, so no crossing may be formed from it.
		forceCostPages(m, func(int, model.Query) ([]model.Record, model.Page) {
			return []model.Record{costSampleRow(tenant, baseTime, 12*oneUSD)}, model.Page{HasMore: true}
		})

		eval := evaluateOne(t, m, tenant, id, baseTime)
		t.Logf("class=%s causes=%v decisions=%+v", eval.Amount.Class, eval.Amount.Causes, eval.Thresholds)
		if eval.Amount.Class != amountUnknown || eval.Amount.Value != nil {
			t.Fatalf("amount = %s/%v, want unknown with no value", eval.Amount.Class, eval.Amount.Value)
		}
		for _, te := range eval.Thresholds {
			if te.Decision.Result != crossingUnproven {
				t.Fatalf("threshold %v = %s, want unproven", te.Configured, te.Decision.Result)
			}
		}
	})
}

// TestSharedEvaluationRefusesAMalformedPolicyField: the policy is an input too. A
// present but unusable money cell must not become an exact zero through the silent
// default, and a negative static reserve is malformed configuration.
func TestSharedEvaluationRefusesAMalformedPolicyField(t *testing.T) {
	for _, tc := range []struct {
		name  string
		spec  map[string]any
		fault budgetConfigFault
	}{
		{
			name:  "a reserved capacity that is not a number",
			spec:  map[string]any{"dimension": "global", "period": "monthly", "limit_micro_usd": int64(10 * oneUSD), "reserved_micro_usd": "twelve"},
			fault: configFaultStaticMalformed,
		},
		{
			name:  "a fractional reserved capacity",
			spec:  map[string]any{"dimension": "global", "period": "monthly", "limit_micro_usd": int64(10 * oneUSD), "reserved_micro_usd": 1.5},
			fault: configFaultStaticMalformed,
		},
		{
			name:  "a negative reserved capacity",
			spec:  map[string]any{"dimension": "global", "period": "monthly", "limit_micro_usd": int64(10 * oneUSD), "reserved_micro_usd": int64(-1)},
			fault: configFaultStaticNegative,
		},
		{
			name:  "a limit that is not a number",
			spec:  map[string]any{"dimension": "global", "period": "monthly", "limit_micro_usd": "ten"},
			fault: configFaultLimitMalformed,
		},
		// The two NULL cases are the A4.2 correction. A stored `null` used to land in
		// the ABSENT branch, so `reserved_micro_usd: null` became a known, exact
		// reserve of zero and an alert could be recorded resting on it. Absence is the
		// documented default of an optional field; a present null is a representation
		// this evaluation cannot read a number from, and it has its own fault.
		{
			name:  "a reserved capacity stored as null",
			spec:  map[string]any{"dimension": "global", "period": "monthly", "limit_micro_usd": int64(10 * oneUSD), "reserved_micro_usd": nil},
			fault: configFaultStaticNull,
		},
		{
			name:  "a limit stored as null",
			spec:  map[string]any{"dimension": "global", "period": "monthly", "limit_micro_usd": nil},
			fault: configFaultLimitNull,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, st, tenant, _ := newFin(t)
			m.clock = &fakeClock{t: baseTime}
			var id model.ID
			if err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
				p, err := sc.Policies().Create(context.Background(), model.Policy{
					Name: "malformed", Kind: policyKindBudget, Enabled: true, Spec: tc.spec,
				})
				id = p.ID
				return err
			}); err != nil {
				t.Fatalf("create policy: %v", err)
			}

			eval := evaluateOne(t, m, tenant, id, baseTime)
			t.Logf("config=%s class=%s static=%+v", eval.Config, eval.Amount.Class, eval.Static)
			if eval.Config != tc.fault {
				t.Fatalf("config fault = %q, want %q", eval.Config, tc.fault)
			}
			if tc.fault != configFaultLimitMalformed && tc.fault != configFaultLimitNull {
				if eval.Static.Known || eval.Amount.Class != amountUnknown {
					t.Fatalf("an unusable reserve became usable: static=%+v class=%s", eval.Static, eval.Amount.Class)
				}
				if eval.Static.MicroUSD != 0 {
					t.Fatalf("an unusable reserve carried the figure %d", eval.Static.MicroUSD)
				}
			}
			// Whatever the fault, no threshold may be proven from it.
			for _, te := range eval.Thresholds {
				if te.Decision.Result == crossingProven {
					t.Fatalf("a crossing was proven on unusable configuration: %+v", te.Decision)
				}
			}
		})
	}

	// The other half of the same distinction, and the reason it is a correction and
	// not a tightening: the field that is simply NOT THERE keeps its documented
	// default, so an ordinary budget without a reserve still evaluates exactly.
	t.Run("an absent optional reserve still takes its documented default", func(t *testing.T) {
		m, st, tenant, _ := newFin(t)
		m.clock = &fakeClock{t: baseTime}
		var id model.ID
		if err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
			p, err := sc.Policies().Create(context.Background(), model.Policy{
				Name: "absent-reserve", Kind: policyKindBudget, Enabled: true,
				Spec: map[string]any{"dimension": "global", "period": "monthly", "limit_micro_usd": int64(10 * oneUSD)},
			})
			id = p.ID
			return err
		}); err != nil {
			t.Fatalf("create policy: %v", err)
		}
		m.ingest(t, tenant, mkCost("openai", "gpt-x", "s-1", 1, 1, 4*oneUSD, baseTime))

		eval := evaluateOne(t, m, tenant, id, baseTime)
		t.Logf("config=%s static=%+v class=%s amount=%s", eval.Config, eval.Static, eval.Amount.Class, eval.Amount.Decimal())
		if eval.Config != configFaultNone || !eval.Static.Known || eval.Static.MicroUSD != 0 {
			t.Fatalf("an absent optional field stopped defaulting: config=%q static=%+v", eval.Config, eval.Static)
		}
		if eval.Amount.Class != amountExact || eval.Amount.Decimal() != "4000000" {
			t.Fatalf("amount = %s/%s, want an exact 4000000", eval.Amount.Class, eval.Amount.Decimal())
		}
	})
}

// evaluateOne runs the shared evaluation for one budget through the module's own
// data handle.
func evaluateOne(t *testing.T, m *Module, tenant model.TenantID, id model.ID, at time.Time) budgetEvaluation {
	t.Helper()
	var eval budgetEvaluation
	if err := m.data.View(context.Background(), tenant, func(sc store.Scope) error {
		p, err := sc.Policies().Get(context.Background(), id)
		if err != nil {
			return err
		}
		spec := parseBudgetSpec(p.Spec)
		spec.fillDefaults()
		eval, err = evaluateBudgetAmount(context.Background(), sc, p, spec, at, at)
		return err
	}); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	return eval
}

// TestStatusPublishesUnknownAsUnknown is A4.2 group 6: status must be able to say
// "not established" without a zero remaining, a false "not over" or an invented
// projection — and its forecast is explicitly not certified by this increment.
func TestStatusPublishesUnknownAsUnknown(t *testing.T) {
	t.Run("an unknown amount publishes no remaining and no crossing", func(t *testing.T) {
		m, st, tenant, _ := newFin(t)
		m.clock = &fakeClock{t: baseTime}
		id := createBudget(t, st, tenant, "unknown-status", budgetSpec{
			Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD, Action: "block",
		})
		forceCostPages(m, func(int, model.Query) ([]model.Record, model.Page) {
			return []model.Record{costSampleRow(tenant, baseTime, 12*oneUSD)}, model.Page{HasMore: true}
		})

		dto := statusOf(t, m, tenant, id)
		t.Logf("status.amount=%+v truncated=%v", dto.Amount, dto.Truncated)
		if dto.Amount == nil || dto.Amount.Class != string(amountUnknown) || dto.Amount.State != "incomplete" {
			t.Fatalf("amount section = %+v, want an incomplete unknown", dto.Amount)
		}
		if dto.Amount.EffectiveMicroUSD != nil || dto.Amount.RemainingMicroUSD != nil {
			t.Fatalf("an unknown amount published figures: %+v", dto.Amount)
		}
		if dto.Amount.LegacyProjection != legacyValueUnavailable {
			t.Fatalf("legacy projection = %q, want unavailable", dto.Amount.LegacyProjection)
		}
		for _, td := range dto.Amount.Thresholds {
			if td.Result != string(crossingUnproven) {
				t.Fatalf("threshold %s = %s, want unproven", td.Threshold, td.Result)
			}
		}
		if dto.Amount.ForecastCertified {
			t.Fatalf("A4.2 does not certify the forecast")
		}
		if !dto.Truncated {
			t.Errorf("the conservative legacy signal must stay set for an old client")
		}
	})

	t.Run("a lower bound below the target is unproven, above it is proven", func(t *testing.T) {
		m, st, tenant, _ := newFin(t)
		m.clock = &fakeClock{t: baseTime}
		// 12 USD of static reserve, no cost, and a dynamic ledger that stopped early
		// with valid rows: the floor is 12 USD.
		id := createBudget(t, st, tenant, "bounded", budgetSpec{
			Dimension: "global", Period: "monthly", LimitMicroUSD: 20 * oneUSD,
			ReservedMicroUSD: 12 * oneUSD, Action: "block", Thresholds: []float64{0.5, 1},
		})
		forcePager(m, func(int, model.Query) ([]model.Record, model.Page) {
			return nil, model.Page{HasMore: true}
		})

		dto := statusOf(t, m, tenant, id)
		t.Logf("status.amount=%+v", dto.Amount)
		if dto.Amount == nil || dto.Amount.Class != string(amountLowerBound) {
			t.Fatalf("amount = %+v, want a lower bound", dto.Amount)
		}
		if dto.Amount.EffectiveMicroUSD == nil || *dto.Amount.EffectiveMicroUSD != "12000000" {
			t.Fatalf("bound = %v, want 12000000", dto.Amount.EffectiveMicroUSD)
		}
		if dto.Amount.RemainingMicroUSD != nil {
			t.Fatalf("a bound published an exact remaining: %v", *dto.Amount.RemainingMicroUSD)
		}
		var atHalf, atLimit string
		for _, td := range dto.Amount.Thresholds {
			switch td.Threshold {
			case "0.5":
				atHalf = td.Result
			case "1":
				atLimit = td.Result
			}
		}
		// 12 USD reaches 50% of 20 USD (10 USD) and does not reach the limit.
		if atHalf != string(crossingProven) || atLimit != string(crossingUnproven) {
			t.Fatalf("decisions: 0.5=%s 1=%s, want proven/unproven", atHalf, atLimit)
		}
	})

	t.Run("an exact credit whose remaining leaves int64 keeps the wide decimal", func(t *testing.T) {
		m, st, tenant, _ := newFin(t)
		m.clock = &fakeClock{t: baseTime}
		id := createBudget(t, st, tenant, "wide-credit", budgetSpec{
			Dimension: "global", Period: "total", LimitMicroUSD: 10 * oneUSD, Action: "block",
		})
		// A COMPLETE signed read of -MaxInt64: remaining is limit + MaxInt64, which is
		// outside int64.
		forceCostPages(m, func(int, model.Query) ([]model.Record, model.Page) {
			return []model.Record{costSampleRow(tenant, baseTime, -math.MaxInt64)}, model.Page{}
		})

		dto := statusOf(t, m, tenant, id)
		t.Logf("status.amount=%+v remaining_legacy=%d", dto.Amount, dto.RemainingMicroUSD)
		if dto.Amount == nil || dto.Amount.Class != string(amountExact) {
			t.Fatalf("amount = %+v, want exact", dto.Amount)
		}
		if dto.Amount.RemainingMicroUSD == nil || *dto.Amount.RemainingMicroUSD != "9223372036864775807" {
			t.Fatalf("remaining = %v, want the exact wide decimal 9223372036864775807", dto.Amount.RemainingMicroUSD)
		}
		if dto.RemainingMicroUSD != 0 {
			t.Errorf("the legacy field invented a projection: %d", dto.RemainingMicroUSD)
		}
		// A4.2 CORRECTION, on the case the independent review measured. The amount is
		// exact, so the whole legacy block used to be labelled `exact` — including a
		// remaining_micro_usd that was never published, because limit − effective does
		// not fit int64. The authoritative decimal is the amount; the older field is
		// simply not a projection of it.
		if dto.Amount.LegacyProjection != legacyValueUnavailable {
			t.Fatalf("legacy projection = %q, want unavailable: remaining=0 was never published", dto.Amount.LegacyProjection)
		}
		if dto.Amount.LegacyFields.RemainingMicroUSD != legacyValueUnavailable {
			t.Fatalf("legacy remaining = %q, want unavailable", dto.Amount.LegacyFields.RemainingMicroUSD)
		}
		// The fields that ARE projections of this evaluation keep saying so: the label
		// is per field, not one verdict for the block.
		if dto.Amount.LegacyFields.SpendMicroUSD != legacyValueExact {
			t.Fatalf("legacy spend = %q, want exact: the strict cost read re-derives it", dto.Amount.LegacyFields.SpendMicroUSD)
		}
		if dto.Amount.LegacyFields.ProjectedMicroUSD != legacyValueUnavailable || dto.Amount.ForecastCertified {
			t.Fatalf("the forecast acquired certainty: %+v", dto.Amount.LegacyFields)
		}
	})

	// A4.2 CORRECTION: the limit itself gets an authoritative decision whether or not
	// the operator configured a 1.0 threshold. Before it, a budget with thresholds
	// {0.5} had no authoritative statement about being over its limit at all, and the
	// only "over" a reader could find was the legacy boolean.
	t.Run("the limit is decided authoritatively without a configured 1.0 threshold", func(t *testing.T) {
		for _, tc := range []struct {
			name    string
			cost    int64
			want    crossingResult
			over    bool
			legacy  string
			unknown bool
		}{
			{name: "over the limit", cost: 12 * oneUSD, want: crossingProven, over: true, legacy: legacyValueExact},
			{name: "under the limit", cost: 4 * oneUSD, want: crossingNotReached, over: false, legacy: legacyValueExact},
			{name: "an unknown amount decides nothing", want: crossingUnproven, legacy: legacyValueUnavailable, unknown: true},
		} {
			t.Run(tc.name, func(t *testing.T) {
				m, st, tenant, _ := newFin(t)
				m.clock = &fakeClock{t: baseTime}
				id := createBudget(t, st, tenant, "no-limit-threshold", budgetSpec{
					Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD,
					Action: "block", Thresholds: []float64{0.5},
				})
				if tc.unknown {
					// A cost read that stops early: nothing is established, and "over" must
					// not become false by default.
					forceCostPages(m, func(int, model.Query) ([]model.Record, model.Page) {
						return []model.Record{costSampleRow(tenant, baseTime, 12*oneUSD)}, model.Page{HasMore: true}
					})
				} else {
					m.ingest(t, tenant, mkCost("openai", "gpt-x", "s-1", 1, 1, tc.cost, baseTime))
				}

				dto := statusOf(t, m, tenant, id)
				t.Logf("over_limit=%+v legacy_over=%v legacy_fields=%+v", dto.Amount.OverLimit, dto.Over, dto.Amount.LegacyFields)
				if dto.Amount == nil {
					t.Fatalf("no authoritative section")
				}
				if dto.Amount.OverLimit.Result != string(tc.want) {
					t.Fatalf("over_limit = %q, want %q", dto.Amount.OverLimit.Result, tc.want)
				}
				if dto.Amount.OverLimit.Threshold != "1" {
					t.Fatalf("over_limit threshold = %q, want the limit itself", dto.Amount.OverLimit.Threshold)
				}
				// The configured list is untouched: 1.0 is not injected into it.
				for _, td := range dto.Amount.Thresholds {
					if td.Threshold == "1" {
						t.Fatalf("the authoritative limit decision was injected into the configured thresholds: %+v", dto.Amount.Thresholds)
					}
				}
				if dto.Amount.LegacyFields.Over != string(tc.want) {
					t.Fatalf("legacy over classification = %q, want %q", dto.Amount.LegacyFields.Over, tc.want)
				}
				if dto.Amount.LegacyProjection != tc.legacy {
					t.Fatalf("legacy projection = %q, want %q", dto.Amount.LegacyProjection, tc.legacy)
				}
				if !tc.unknown && dto.Over != tc.over {
					t.Fatalf("the legacy boolean moved: %v", dto.Over)
				}
			})
		}
	})

	// A4.2 CORRECTION: status keeps TWO traversals — the older aggregates and this
	// strict evaluation — and under READ COMMITTED they may not see the same rows. The
	// authoritative section may not certify numbers it did not re-derive, so a
	// divergence is reported as unavailable rather than labelled exact.
	t.Run("two traversals that disagree do not certify each other", func(t *testing.T) {
		m, st, tenant, _ := newFin(t)
		m.clock = &fakeClock{t: baseTime}
		id := createBudget(t, st, tenant, "divergent", budgetSpec{
			Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD, Action: "block",
		})
		m.ingest(t, tenant, mkCost("openai", "gpt-x", "s-1", 1, 1, 4*oneUSD, baseTime))
		// Both traversals agree first: the label is earned.
		before := statusOf(t, m, tenant, id)
		if before.Amount.LegacyProjection != legacyValueExact {
			t.Fatalf("agreeing traversals = %q, want exact", before.Amount.LegacyProjection)
		}
		// Now the two traversals observe different ledgers. budgetStatus reads the old
		// aggregate FIRST and the strict evaluation second, so serving the first call
		// four dollars and every later call five stages exactly what READ COMMITTED
		// permits: a charge that landed between the two reads. Neither figure is wrong;
		// what would be wrong is certifying the first from the second.
		forceCostPages(m, func(call int, _ model.Query) ([]model.Record, model.Page) {
			if call == 0 {
				return []model.Record{costSampleRow(tenant, baseTime, 4*oneUSD)}, model.Page{}
			}
			return []model.Record{
				costSampleRow(tenant, baseTime, 4*oneUSD),
				costSampleRow(tenant, baseTime, oneUSD),
			}, model.Page{}
		})
		after := statusOf(t, m, tenant, id)
		t.Logf("legacy_spend=%d authoritative=%v fields=%+v", after.SpendMicroUSD, *after.Amount.EffectiveMicroUSD, after.Amount.LegacyFields)
		if after.SpendMicroUSD != 4*oneUSD {
			t.Fatalf("the first traversal did not see its own ledger: %d", after.SpendMicroUSD)
		}
		if after.Amount.EffectiveMicroUSD == nil || *after.Amount.EffectiveMicroUSD != "5000000" {
			t.Fatalf("the authoritative read did not diverge: %v", after.Amount.EffectiveMicroUSD)
		}
		if after.Amount.LegacyFields.SpendMicroUSD != legacyValueUnavailable ||
			after.Amount.LegacyFields.RemainingMicroUSD != legacyValueUnavailable ||
			after.Amount.LegacyProjection != legacyValueUnavailable {
			t.Fatalf("a diverging traversal was certified: %+v", after.Amount.LegacyFields)
		}
	})
}

// statusOf reads one budget's status DTO through the module's data handle.
func statusOf(t *testing.T, m *Module, tenant model.TenantID, id model.ID) budgetStatusDTO {
	t.Helper()
	var dto budgetStatusDTO
	if err := m.data.View(context.Background(), tenant, func(sc store.Scope) error {
		p, err := sc.Policies().Get(context.Background(), id)
		if err != nil {
			return err
		}
		dto, err = budgetStatus(context.Background(), sc, p, m.clock.Now().Time())
		return err
	}); err != nil {
		t.Fatalf("status: %v", err)
	}
	return dto
}

// TestAdmissionKeepsItsDecidedRefusalsAcrossTheNewSignature is A4.2 group 10: the
// R1–R3 controls that actually traverse the changed reservation reader. A decided
// refusal still survives a later I/O failure and a conflict, the outage posture with
// nothing decided is untouched, and a refused reservation still leaves no rows.
func TestAdmissionKeepsItsDecidedRefusalsAcrossTheNewSignature(t *testing.T) {
	readFailure := errors.New("finops-test: reservation read I/O failure")

	for _, api := range []string{"ReserveBudget", "CheckBudget"} {
		t.Run("a decided refusal survives a later failure/"+api, func(t *testing.T) {
			m, st, tenant, _ := newFin(t)
			for _, name := range []string{"first", "second"} {
				createBudget(t, st, tenant, "b-"+name, budgetSpec{
					Dimension: "global", Period: "total", LimitMicroUSD: 10 * oneUSD, Action: "block",
				})
			}
			// AN INDEPENDENT REVIEW RE-AIMED THE INJECTION, AND ONLY THE INJECTION. The staging is
			// call-indexed, and the hold reader now issues TWO reservation reads per
			// target even when the first is already unestablished: the legacy branch
			// and the disjoint v1 branch. It reads both because a CONTRADICTION in
			// the v1 prefix must be inspected rather than hidden behind a clean
			// legacy one — the correction the independent return required. So the
			// first budget consumes calls 0 AND 1, and the previous predicate broke
			// the FIRST budget before anything had been decided, turning this case
			// into the outage posture it is not about (measured: allowed=true with
			// nothing decided). Every assertion below is unchanged.
			forcePagerWithErrors(m, incompletePage, func(call int) error {
				if call < readsPerTarget {
					return nil
				}
				return readFailure
			})
			got := callAdmission(t, m, tenant, api)
			t.Logf("%s: allowed=%v action=%q err=%v", api, got.Allowed, got.Action, got.Err)
			if got.Allowed || got.Err != nil {
				t.Fatalf("the established refusal did not survive: %+v", got)
			}
			if rows := countReservations(t, st, tenant); len(rows) != 0 {
				t.Errorf("%d reservation rows survived", len(rows))
			}
		})

		t.Run("an outage with nothing decided keeps the declared posture/"+api, func(t *testing.T) {
			m, st, tenant, _ := newFin(t)
			createBudget(t, st, tenant, "only", budgetSpec{
				Dimension: "global", Period: "total", LimitMicroUSD: 10 * oneUSD, Action: "block",
			})
			forcePagerWithErrors(m, incompletePage, func(int) error { return readFailure })
			got := callAdmission(t, m, tenant, api)
			t.Logf("%s: allowed=%v err=%v", api, got.Allowed, got.Err)
			if !got.Allowed || !errors.Is(got.Err, readFailure) {
				t.Fatalf("outage posture changed: %+v", got)
			}
		})
	}
}
