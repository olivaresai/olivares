// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"context"
	"math"
	"math/big"
	"strconv"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// D02-A4, INCREMENT 1 — the strict cost reader and the amount/completeness algebra
// a later increment will evaluate budget alerts with.
//
// NOTHING HERE IS WIRED. No production path calls it, no schema, DTO, SDK, event,
// API or UI changes with it, and no classification it produces is persisted or
// published. `evaluateBudgets`, `aggregatePeriod`, `scanSamples` and the reservation
// ledger keep the behavior root already accepted; this file neither certifies them
// nor is called by them. R4 stays open, and so does D02-A.
//
// The one fact this file exists to establish is the one the alert history cannot
// state today: WHICH KIND OF NUMBER a consumption figure is.
//
//   - exact       — every component was established and their sum is valid.
//   - lower_bound — a monetary bound is provable, the total is not. A crossing the
//                   bound reaches is a REAL crossing; one it does not reach is not
//                   evidence of anything.
//   - unknown     — neither. Not zero, not "within budget", not a smaller total.
//
// Two rules do most of the work, and both come from the adjudicated contract:
//
//   - A COST PREFIX IS NOT A BOUND. Cost samples are signed (credits and refunds are
//     legitimate), so an unread page can carry a −6 USD credit that cancels the 12 USD
//     already read. A partial signed enumeration is unknown even when its prefix is
//     positive and large.
//   - A DYNAMIC RESERVATION THAT CANNOT BE ENUMERATED IS NOT ZERO — but the obligations
//     it represents are non-negative by domain invariant, so an exact cost plus a known
//     static reserve IS a lower bound of effective consumption. That is the R4 case:
//     0 cost + 12 USD static + unknown dynamic proves ≥ 12 USD, which crosses a 10 USD
//     limit. The bound comes from the invariant, never from "the prefix looked positive".
//
// SCOPE IS PART OF THE AMOUNT. A budget's subject must be expressible as a predicate
// over the cost rows before any total means anything: a global budget is the whole
// tenant and needs no predicate, while a group budget's subject lives in the directory
// and is not expressed by "no predicate" at all. Treating the second like the first
// attributes unrelated tenant spend to a group, so an unresolved or unsupported scope
// is unknown here, with its own cause. That is a refusal, not a narrowing of the
// product: group accounting stays later work over the existing group primitives.
//
// SNAPSHOT CONSISTENCY IS NOT CLAIMED, and cannot be from here. The reader pages
// inside whatever Scope the caller supplies. Under PostgreSQL READ COMMITTED a
// multi-statement enumeration can observe rows committed between pages, so Complete
// means "every page the store offered was read and the paging was self-consistent",
// not "an instantaneous global snapshot". A caller that needs the stronger property
// must supply a scope that provides it; this file records what it observed and says
// so, which is exactly what the contract asks the envelope to carry later.

// amountClass is how a consumption figure may be used.
type amountClass string

const (
	amountExact      amountClass = "exact"
	amountLowerBound amountClass = "lower_bound"
	amountUnknown    amountClass = "unknown"
)

// amountCause is the CLOSED vocabulary of reasons a figure is not exact. Closed on
// purpose: a later increment puts these in a persisted envelope, and a free-form
// string there would let a storage error text become part of a financial record.
type amountCause string

const (
	causeScopeTenantMismatch       amountCause = "scope_tenant_mismatch"
	causeScopeGroupUnresolved      amountCause = "scope_group_unresolved"
	causeScopeDimensionUnsupported amountCause = "scope_dimension_unsupported"
	causeScopePredicateUnsupported amountCause = "scope_predicate_unsupported"
	causeScopeUnresolved           amountCause = "scope_unresolved"
	causeWindowInvalid             amountCause = "window_invalid"
	causeCostReadFailed            amountCause = "cost_read_failed"
	causeCostScanTruncated         amountCause = "cost_scan_truncated"
	causeCostCursorMissing         amountCause = "cost_cursor_missing"
	causeCostCursorStalled         amountCause = "cost_cursor_not_advancing"
	causeCostCursorCycle           amountCause = "cost_cursor_cycle"
	causeCostRowMalformed          amountCause = "cost_row_malformed"
	causeCostRowOutOfWindow        amountCause = "cost_row_outside_requested_window"
	causeCostRowOtherTenant        amountCause = "cost_row_other_tenant"
	causeCostRowTenantMalformed    amountCause = "cost_row_tenant_malformed"
	causeCostRowOutOfScope         amountCause = "cost_row_outside_requested_scope"
	causeCostRowDimensionMalformed amountCause = "cost_row_dimension_malformed"
	causeCostRowProvenance         amountCause = "cost_row_outside_requested_provenance"
	causeStaticUnknown             amountCause = "static_reservation_unknown"
	causeStaticNegative            amountCause = "static_reservation_negative"
	causeDynamicUnknown            amountCause = "dynamic_reservation_unknown"
	causeDynamicScanIncomplete     amountCause = "dynamic_reservation_scan_incomplete"
	causeDynamicUnverified         amountCause = "dynamic_reservation_unverified"
	causeDynamicNegative           amountCause = "dynamic_reservation_negative"
	// causeDynamicStateUnclassified is the A4.2-correction cause: the reserved total
	// carries a state this adapter does not recognise (including the zero value of an
	// uninitialised struct). An unclassified state proves nothing, so it is named
	// rather than folded into "unverified", which describes an OBSERVED corruption.
	causeDynamicStateUnclassified amountCause = "dynamic_reservation_state_unclassified"
	causeThresholdNotFinite       amountCause = "threshold_not_finite"
	causeThresholdOutOfRange      amountCause = "threshold_out_of_range"
	causeLimitNotPositive         amountCause = "limit_not_positive"
	causeAmountNotDecidable       amountCause = "amount_not_decidable"
	// A4.2 batch-level and identity-level facts. The first is a diagnostic about the
	// budget CENSUS, not about one budget's money; the second says a proven crossing
	// cannot be given the historical alert identity.
	causeBudgetCensusTruncated            amountCause = "budget_census_truncated"
	causeThresholdIdentityUnrepresentable amountCause = "threshold_identity_unrepresentable"
)

// maxStrictCostRows is the number of rows the strict reader can possibly visit: the
// existing page cap times the existing page size. It is what BOUNDS the wide
// accumulator — every term is one int64 column of one row, and there are at most this
// many rows, so |total| < 2^63 × 10^6 < 2^83. The big.Int here is therefore a bounded
// domain widening of int64 arithmetic, NOT an arbitrary-precision parser: no value
// enters it from a string, a wire format or a caller.
const maxStrictCostRows = maxScanPages * listCap

// maxExactDecimalDigits bounds the fractional expansion this file is willing to PRINT
// for a rational. A threshold is a finite decimal and a limit is an integer, so their
// product terminates; a pathological denormal threshold could still terminate only
// after hundreds of digits, and an unbounded expansion is not something a financial
// record should contain.
//
// Past the bound the DECIMAL is withheld rather than rounded — a rounded money figure
// presented as exact is the defect this increment exists to prevent. The COMPARISON is
// unaffected: the target is kept as an exact rational and the crossing is still decided
// from it, so an empty TargetDecimal means "no exact decimal short enough to print",
// never "undecidable". (The earlier comment here said the value became undecidable,
// which the implementation never did; the independent review named it and it is fixed.)
const maxExactDecimalDigits = 40

// strictCostWindow is what a strict cost read is FOR: one tenant, one half-open
// window and one budget's dimension filters. The tenant is carried explicitly so the
// reader can refuse a scope bound to a different one instead of silently reading
// somebody else's ledger.
type strictCostWindow struct {
	Tenant model.TenantID
	// Filters are the equality predicates that express the requested scope over the
	// cost read model. Exactly one supported dimension column, or NONE for a verified
	// global scope — and the difference between those two is why ScopeResolved exists.
	Filters []model.Filter
	// ScopeResolved states that Filters express the requested scope EXACTLY. It is not
	// a formality: budgetSpec.sampleFilters returns nil both for a global budget (where
	// no predicate is the right predicate) and for a group or unsupported dimension
	// (where the scope was simply not expressed at all). Reading with nil filters in the
	// second case sums the whole tenant and calls it the group's, which is precisely the
	// false exactness this increment exists to prevent. The zero value is therefore
	// UNRESOLVED: a caller must state that the scope was established, and a window built
	// by hand without saying so is refused rather than silently widened.
	ScopeResolved bool
	// ScopeCause names why the scope could not be expressed, when it could not.
	ScopeCause amountCause
	// Start/HasStart and End/Bounded mirror aggregatePeriod's half-open window
	// exactly — [Start, End) when bounded, [Start, +inf) otherwise, and unbounded on
	// both ends for a "total" period.
	Start    time.Time
	HasStart bool
	End      time.Time
	Bounded  bool
}

// strictCostWindowForBudget builds the window for one budget's current period from
// the SAME primitives the evaluator uses (periodStart/periodEnd and the dimension
// column mapping), so a later increment cannot drift into scoping alerts differently
// from the budgets they belong to — and it says whether that scope could be expressed
// at all.
func strictCostWindowForBudget(tenant model.TenantID, spec budgetSpec, now time.Time) strictCostWindow {
	pStart, hasLower := periodStart(spec.Period, now)
	filters, resolved, cause := strictCostScope(spec)
	return strictCostWindow{
		Tenant:        tenant,
		Filters:       filters,
		ScopeResolved: resolved,
		ScopeCause:    cause,
		Start:         pStart,
		HasStart:      hasLower,
		End:           periodEnd(spec.Period, pStart),
		Bounded:       hasLower,
	}
}

// supportedScopeDimensions are the budget dimensions whose scope IS one equality over
// a cost-sample column. The list is closed and the mapping is not re-implemented: it
// runs through the existing dimensionColumn, which stays the single source of truth.
// A dimension added there and not here is refused rather than silently broadened —
// deny-closed is the only safe direction for a scope predicate.
//
// The group dimensions are absent ON PURPOSE, and their absence is a refusal, not a
// removal from scope: user_group and agent_group have no column of their own, their
// spend is a fan-out over the directory members that aggregateGroupPeriod resolves,
// and resolving them here would mean a second membership subsystem. Full group
// accounting stays later work with the existing group primitives.
var supportedScopeDimensions = []string{
	"provider", "model", "agent", "session", "team", "project", "workspace",
	"api_key", "actor", "service_tier", "context_window", "inference_geo",
	"gateway", "cost_type", "identity", "cost_center",
}

// strictCostScope expresses a budget's scope as cost-row predicates and reports when
// it CANNOT. nil filters mean "the whole tenant" only for a verified global budget.
func strictCostScope(spec budgetSpec) ([]model.Filter, bool, amountCause) {
	switch {
	case spec.Dimension == "global":
		// A verified global scope: the tenant is the subject, so no predicate is the
		// correct predicate. This is the ONLY case where nil filters mean resolved.
		return nil, true, ""
	case isGroupDimension(spec.Dimension):
		return nil, false, causeScopeGroupUnresolved
	}
	col := dimensionColumn(spec.Dimension)
	if col == "" || !supportedScopeColumn(col) {
		return nil, false, causeScopeDimensionUnsupported
	}
	// The key is used exactly as the evaluator uses it (budgetSpec.sampleFilters and
	// spec.matches compare the same string), including an empty key, which scopes the
	// budget to samples whose dimension is empty. Same subject, same predicate.
	return []model.Filter{eq(col, spec.Key)}, true, ""
}

// supportedScopeColumn reports whether a column is the cost-sample column of a
// supported budget dimension.
func supportedScopeColumn(col string) bool {
	if col == "" {
		return false
	}
	for _, dim := range supportedScopeDimensions {
		if dimensionColumn(dim) == col {
			return true
		}
	}
	return false
}

// strictCostTotal is what a strict cost read established. Total is meaningful ONLY
// when Complete: an incomplete read carries the rows it happened to see for
// diagnostics, and calling that a total is the mistake this type exists to prevent.
type strictCostTotal struct {
	Complete bool
	// Total is the exact signed sum over the window when Complete, nil otherwise. It
	// is never a prefix presented as a total.
	Total *big.Int
	// Rows and Pages are diagnostics about the read itself, never money.
	Rows  int
	Pages int
	// Causes name why the read is not complete, in the order they were detected.
	Causes []amountCause
	// Err is the underlying read failure, kept so the caller can distinguish an
	// outage from a well-formed incomplete enumeration. It is NOT part of the closed
	// cause vocabulary and must not reach a published record.
	Err error
}

// readStrictCostTotal sums the cost samples of one window with the completeness rules
// the reservation ledger already applies to its own enumeration: a finite page budget,
// a cursor that must exist and must make progress, and a final page that may carry
// anything because nobody resumes from it.
//
// It reuses the existing filters, page size, page cap, provenance exclusion and
// half-open window semantics. It deliberately does NOT touch scanSamples or
// aggregatePeriod: those keep serving the analytics they already serve, and nothing
// here certifies them.
//
// It enforces what it was asked for rather than trusting the repository to have done
// it: a scope bound to another tenant is refused before any read, and a row that
// arrives from another tenant or from outside the requested window makes the read
// incomplete instead of joining the sum.
func readStrictCostTotal(ctx context.Context, sc store.Scope, w strictCostWindow) strictCostTotal {
	out := strictCostTotal{Total: new(big.Int)}
	if sc == nil || sc.Tenant() != w.Tenant {
		return incompleteCost(out, causeScopeTenantMismatch, nil)
	}
	if !w.ScopeResolved {
		// The requested subject was never expressed as a predicate — an unresolved
		// group, an unsupported dimension, or a window built without establishing its
		// scope. Reading anyway would sum the whole tenant and attribute it to that
		// subject. The amount is unknown and the cause says which.
		cause := w.ScopeCause
		if cause == "" {
			cause = causeScopeUnresolved
		}
		return incompleteCost(out, cause, nil)
	}
	predicates, cause := strictCostRowPredicates(w.Filters)
	if cause != "" {
		// A predicate this reader cannot also CHECK on the returned rows is one it will
		// not send: an unverifiable filter would widen the ledger silently if the store
		// ignored it.
		return incompleteCost(out, cause, nil)
	}
	if w.HasStart && w.Bounded && !w.End.After(w.Start) {
		// An empty or inverted window would read zero rows and look like a complete
		// zero. It is a caller error, and zero is an amount somebody would act on.
		return incompleteCost(out, causeWindowInvalid, nil)
	}
	repo, err := sc.Ext(costSampleKind)
	if err != nil {
		return incompleteCost(out, causeCostReadFailed, err)
	}

	// The estimated stream only, exactly like every budget aggregate: the billed
	// cost_report stream is a separate reconciliation and summing both double-counts.
	filters := append([]model.Filter{estimatedFilter()}, w.Filters...)
	if w.HasStart {
		filters = append(filters, model.Filter{Column: colOccurredAt, Op: model.OpGte, Value: model.NewTimestamp(w.Start).String()})
	}
	if w.Bounded {
		filters = append(filters, model.Filter{Column: colOccurredAt, Op: model.OpLt, Value: model.NewTimestamp(w.End).String()})
	}

	q := model.Query{Filters: filters, Limit: listCap}
	used := make(map[string]bool)
	total := new(big.Int)
	for pages := 0; ; pages++ {
		recs, page, err := repo.List(ctx, q)
		if err != nil {
			return incompleteCost(out, causeCostReadFailed, err)
		}
		out.Pages = pages + 1
		for _, r := range recs {
			out.Rows++
			if out.Rows > maxStrictCostRows {
				// The page budget already bounds this; the counter is the second lock,
				// because the accumulator's magnitude bound is stated in terms of it.
				return incompleteCost(out, causeCostScanTruncated, nil)
			}
			if cause, ok := strictCostRowFault(r, w, predicates); !ok {
				return incompleteCost(out, cause, nil)
			}
			cell, _ := int64Cell(r, colCostMicroUSD)
			total.Add(total, big.NewInt(cell))
		}
		if !page.HasMore {
			out.Complete = true
			out.Total = total
			return out
		}
		if page.Cursor == "" {
			return incompleteCost(out, causeCostCursorMissing, nil)
		}
		if page.Cursor == q.Cursor {
			return incompleteCost(out, causeCostCursorStalled, nil)
		}
		if used[page.Cursor] {
			return incompleteCost(out, causeCostCursorCycle, nil)
		}
		if pages+1 >= maxScanPages {
			return incompleteCost(out, causeCostScanTruncated, nil)
		}
		used[page.Cursor] = true
		q.Cursor = page.Cursor
	}
}

// strictCostRowPredicates re-reads the window's own filters as the CLOSED set this
// reader can verify: one equality per supported dimension column. Anything else — a
// different operator, an unknown column, a non-string value, two predicates on the
// same column — is refused rather than sent, because a predicate that cannot be
// checked on the way back is a predicate the reader cannot enforce. This is not a
// filter engine: it recognizes the shape strictCostScope produces, and nothing else.
func strictCostRowPredicates(filters []model.Filter) ([]model.Filter, amountCause) {
	if len(filters) == 0 {
		return nil, ""
	}
	seen := make(map[string]bool, len(filters))
	out := make([]model.Filter, 0, len(filters))
	for _, f := range filters {
		if f.Op != model.OpEq || !supportedScopeColumn(f.Column) || seen[f.Column] {
			return nil, causeScopePredicateUnsupported
		}
		if _, ok := f.Value.(string); !ok {
			return nil, causeScopePredicateUnsupported
		}
		seen[f.Column] = true
		out = append(out, f)
	}
	return out, ""
}

// strictCostRowFault checks the row really belongs to what was asked for. A store
// that filters correctly makes this inert; a fixture, a future filter bug or a
// mis-scoped repository makes it the difference between an honest "unknown" and a
// silent sum over the wrong ledger. It checks every predicate the read was scoped
// with — tenant, dimension, window AND provenance stream — not only the ones that
// are cheap to check.
func strictCostRowFault(r model.Record, w strictCostWindow, predicates []model.Filter) (amountCause, bool) {
	if _, ok := int64Cell(r, colCostMicroUSD); !ok {
		return causeCostRowMalformed, false
	}
	if cause, ok := tenantCellSatisfies(r, w.Tenant); !ok {
		return cause, false
	}
	for _, p := range predicates {
		if cause, ok := dimensionCellSatisfies(r, p); !ok {
			return cause, false
		}
	}
	// The estimated stream, checked the way estimatedFilter defines it: BILLED rows are
	// excluded and everything else is kept. That preserves the existing legacy
	// semantics on purpose — a row whose provenance is absent or empty is not billed,
	// so it stays in the aggregate exactly as the SQL predicate leaves it, and a
	// legitimate signed credit is unaffected.
	if r.String(colProvenance) == provenanceBilled {
		return causeCostRowProvenance, false
	}
	if !w.HasStart && !w.Bounded {
		return "", true
	}
	at, err := model.ParseTimestamp(r.String(colOccurredAt))
	if err != nil {
		return causeCostRowMalformed, false
	}
	when := at.Time()
	if w.HasStart && when.Before(w.Start) {
		return causeCostRowOutOfWindow, false
	}
	if w.Bounded && !when.Before(w.End) {
		// The upper bound is EXCLUSIVE: a sample at exactly End belongs to the next
		// period, and counting it in both is how a period total silently inflates.
		return causeCostRowOutOfWindow, false
	}
	return "", true
}

// dimensionCellSatisfies compares ONE returned dimension cell against the equality the
// read was scoped with, and checks the cell's TYPE before its value.
//
// The type check is the whole point. Record.String returns "" for a cell that is
// absent, null OR of the wrong type, and the scope builder deliberately admits an
// empty dimension key — so a present int64(7) in provider_ref read as "" and SATISFIED
// a requested provider_ref == "". A cell that is not the kind of value the predicate
// compares cannot establish that equality in either direction, so the row is malformed
// for this read and the total is not established.
//
// The three outcomes are explicit on purpose:
//
//   - a present string — compared exactly, so a legitimately empty dimension and an
//     ordinary one both behave as the operator configured, and a signed credit on such
//     a row is counted like any other cost.
//   - a present value of another type — malformed. Text columns arrive as strings
//     through this engine (Record.String and every existing reader assume that), so a
//     different type is a fault in what came back, not a value to coerce.
//   - an ABSENT or NULL cell — out of scope, not malformed. It is a legitimate storage
//     state and this reader does not invent a SQL NULL contract for it: it simply
//     cannot show that the row satisfies the requested equality, so it does not count
//     it. A real store would not have returned such a row for this predicate anyway
//     (SQL equality is not true for NULL, including against the empty string), which
//     is why this is a defensive check and not a change of query semantics.
func dimensionCellSatisfies(r model.Record, p model.Filter) (amountCause, bool) {
	want, ok := p.Value.(string)
	if !ok {
		// strictCostRowPredicates already refused any non-string predicate before the
		// read; reaching here would mean the read was scoped with something it could
		// not verify, which is exactly what must not be summed.
		return causeScopePredicateUnsupported, false
	}
	cell, present := r[p.Column]
	if !present || cell == nil {
		return causeCostRowOutOfScope, false
	}
	text, isText := cell.(string)
	if !isText {
		return causeCostRowDimensionMalformed, false
	}
	if text != want {
		return causeCostRowOutOfScope, false
	}
	return "", true
}

// tenantCellSatisfies checks the row's OWN tenant evidence, the same way the dimension
// check reads its cell: the entry first, its type second, its value last.
//
// The line this replaces read `r.String(model.ColTenantID)` and skipped the comparison
// whenever that came back empty — which Record.String does for a missing, null OR
// wrongly typed cell, and for a genuinely empty one. A row with an otherwise valid
// amount, window, dimension and provenance therefore reached the accumulator carrying
// no usable tenant evidence at all, and on a final page the reader could publish a
// complete exact sum over it.
//
// An empty base tenant is NOT the same thing as an empty dimension key. A dimension key
// may legitimately be empty and is compared as configured; tenant_id is the engine's
// injected base column, declared TEXT NOT NULL in both dialects, always projected and
// always bound in the query, so absent, null, non-string or empty is not a storage state
// this reader can attribute to a real row. It refuses instead, with its own closed
// cause, and — the part that matters — it never fills, inherits, stringifies or coerces
// the SCOPE's tenant into a row that did not carry it.
//
// This is a defensive integrity check at the helper boundary, for a malformed or faked
// repository and for a future regression. It is NOT evidence that SQL ever leaked a row
// across tenants: the repository is tenant-pinned and appends its own tenant predicate,
// and the Scope/tenant mismatch refusal before any read remains the first barrier. This
// check exists because the helper promises to verify what came back before calling an
// amount exact.
func tenantCellSatisfies(r model.Record, tenant model.TenantID) (amountCause, bool) {
	switch tenantCellOf(r, tenant) {
	case tenantCellUnusable:
		return causeCostRowTenantMalformed, false
	case tenantCellOtherTenant:
		// A different, well-formed identity: the row is another tenant's, which is the
		// long-standing cause and a different fact from unusable evidence.
		return causeCostRowOtherTenant, false
	}
	return "", true
}

// tenantCellVerdict is the three-step check above, kept as ONE implementation so a
// second ledger cannot grow a second (and eventually different) idea of what counts
// as a row's own tenant evidence. A4.2's independent review found exactly that gap:
// the strict cost reader checked the cell and the reservation reader did not, so a
// reservation row carrying no tenant evidence at all was summed as if it did. The two
// readers report in their own closed vocabularies; what they must not do is disagree
// about the FACT.
type tenantCellVerdict int

const (
	// tenantCellMatches: the cell is present, is a non-empty string, and it is this
	// tenant's identity.
	tenantCellMatches tenantCellVerdict = iota
	// tenantCellUnusable: absent, null, not a string, or empty — no usable evidence.
	// It is never filled in from the scope that asked for the read.
	tenantCellUnusable
	// tenantCellOtherTenant: well-formed and someone else's.
	tenantCellOtherTenant
)

func tenantCellOf(r model.Record, tenant model.TenantID) tenantCellVerdict {
	cell, present := r[model.ColTenantID]
	if !present || cell == nil {
		return tenantCellUnusable
	}
	text, isText := cell.(string)
	if !isText || text == "" {
		return tenantCellUnusable
	}
	if text != tenant.String() {
		return tenantCellOtherTenant
	}
	return tenantCellMatches
}

// incompleteCost records a cause and guarantees the caller cannot mistake the read
// for a total: Complete stays false and Total is dropped.
func incompleteCost(out strictCostTotal, cause amountCause, err error) strictCostTotal {
	out.Complete = false
	out.Total = nil
	out.Causes = append(out.Causes, cause)
	if err != nil {
		out.Err = err
	}
	return out
}

// int64Cell reads a column that the schema declares as KindInt. Record.Int returns 0
// for an absent, null or wrongly typed cell, which is indistinguishable from a real
// zero — fine for a display aggregate, not for deciding whether a number exists.
func int64Cell(r model.Record, col string) (int64, bool) {
	switch v := r[col].(type) {
	case int64:
		return v, true
	case int:
		return int64(v), true
	case int32:
		return int64(v), true
	}
	return 0, false
}

// dynamicState is the THREE-valued state of the dynamic reservation obligation, and
// the third value is the whole point of A4-R2. "Not known" is not one condition but
// two, and only one of them may carry a bound:
//
//   - dynamicKnown — enumerated exactly. Non-negative; a negative here is corruption.
//   - dynamicNonNegativeUnknown — NOT observed, and nothing observed contradicts the
//     domain invariant that obligations are ≥ 0. A lower bound may rest on it: that is
//     the R4 case, where 0 cost + 12 USD static proves ≥ 12 USD against a 10 USD limit.
//   - dynamicIndeterminate — something WAS observed that the invariant cannot absorb
//     (a negative stored reserve, a total outside the representable range), or the
//     producer cannot say which of the two states it is in. No bound may rest on it.
//
// Collapsing the last two is exactly how an observed corruption became a proven bound:
// a −6 USD reservation row makes the ledger reader return an unestablished total, and
// treating that as "merely unobserved" asserted the very invariant the data violated.
type dynamicState string

const (
	dynamicKnown              dynamicState = "known"
	dynamicNonNegativeUnknown dynamicState = "nonnegative_unknown"
	dynamicIndeterminate      dynamicState = "indeterminate"
)

// dynamicComponent is the prospective reservation obligation for one budget period.
// The zero value is INDETERMINATE on purpose: a component nobody classified must not
// buy a bound.
type dynamicComponent struct {
	State    dynamicState
	MicroUSD int64
	Cause    amountCause
}

// known reports the exact-value state.
func (d dynamicComponent) known() bool { return d.State == dynamicKnown }

// carriesNonNegativeInvariant reports that a bound may rest on this component: either
// it is exact, or it is unobserved with the invariant intact.
func (d dynamicComponent) carriesNonNegativeInvariant() bool {
	return d.State == dynamicKnown || d.State == dynamicNonNegativeUnknown
}

// dynamicFromReservedTotal adapts the reservation ledger's EXISTING result without
// changing that reader, and it is deliberately conservative.
//
// An established total is known. An unestablished one is INDETERMINATE — not
// "unobserved" — because reservedTotal carries a single free-text Incomplete string
// that covers both an enumeration that stopped early (obligations still non-negative)
// and an invariant violation it detected (a negative row, an unrepresentable sum), and
// this adapter will not tell them apart by parsing that text: a financial classification
// keyed on error prose is a contract that breaks the next time the prose is edited.
//
// A4.2 supplied that typed reason at the producer: activeReservedMicroUSD now
// validates every observed row BEFORE classifying, so a paging enumeration whose
// observed rows are all valid, in scope and non-negative arrives as
// reservedNonNegativeUnknown and recovers the floor, while an observed negative,
// malformed or out-of-scope row arrives as reservedIndeterminate and still proves
// nothing. The free text is never parsed to tell them apart.
func dynamicFromReservedTotal(rt reservedTotal) dynamicComponent {
	switch rt.State {
	case reservedKnown:
		return dynamicComponent{State: dynamicKnown, MicroUSD: rt.MicroUSD}
	case reservedNonNegativeUnknown:
		// A4.2: the producer now states that the enumeration stopped for a PAGING
		// reason and that every row it did observe was valid, in scope and
		// non-negative. Nothing observed contradicts the invariant, so a bound may
		// rest on it — and the prefix sum is not carried, only the invariant.
		return dynamicComponent{State: dynamicNonNegativeUnknown, Cause: causeDynamicScanIncomplete}
	case reservedIndeterminate:
		return dynamicComponent{State: dynamicIndeterminate, Cause: causeDynamicUnverified}
	}
	// An UNCLASSIFIED result: a zero value, a value built before the typed state
	// existed, or one a future producer sets to something this adapter does not know.
	// It is INDETERMINATE, and A4.2's independent review is why this line changed.
	//
	// It used to fall back to `rt.established()` — "Incomplete is empty means exact" —
	// which reads the ABSENCE of a diagnostic string as a positive finding. The zero
	// value `reservedTotal{}` has an empty Incomplete, so it arrived here as an exact
	// reserve of zero: an uninitialised struct granting certainty about money. The
	// contract is explicit that a new/unknown/uninitialised state never grants a bound
	// or a certainty, and an unrecognised state is exactly that. The typed producer
	// above always sets State, so nothing legitimate reaches this line today; when a
	// future producer adds a fourth state, this refuses instead of guessing.
	return dynamicComponent{State: dynamicIndeterminate, Cause: causeDynamicStateUnclassified}
}

// unknownDynamic is the component for an obligation that was NOT OBSERVED at all — no
// read was attempted, or a caller states it deliberately. The non-negative invariant is
// intact because nothing contradicted it, so a lower bound may rest on this state. It
// is not the state an unreadable ledger produces; that one is indeterminate.
func unknownDynamic() dynamicComponent {
	return dynamicComponent{State: dynamicNonNegativeUnknown, Cause: causeDynamicUnknown}
}

// indeterminateDynamic is the component for an obligation whose observation cannot
// support the invariant, with the closed cause naming what was seen.
func indeterminateDynamic(cause amountCause) dynamicComponent {
	if cause == "" {
		cause = causeDynamicUnverified
	}
	return dynamicComponent{State: dynamicIndeterminate, Cause: cause}
}

// effectiveAmountInputs are the three components of effective consumption:
// accounted cost, the budget's static reserved capacity and the dynamic reservation
// obligations.
type effectiveAmountInputs struct {
	Cost strictCostTotal
	// StaticKnown/StaticMicroUSD is the budget's configured reserved capacity. It is
	// configuration, so it is known whenever the policy was read; a negative value is
	// malformed configuration, not a credit.
	StaticKnown    bool
	StaticMicroUSD int64
	Dynamic        dynamicComponent
}

// effectiveAmount is a classified consumption figure. Value carries the exact total
// (exact) or the proven bound (lower_bound) as a bounded wide integer, and is nil for
// unknown. It is never saturated and never rounded: a figure that does not fit an
// int64 stays a wide value and a decimal string, and the caller is told it does not
// fit rather than handed a clamped number.
type effectiveAmount struct {
	Class  amountClass
	Value  *big.Int
	Causes []amountCause
}

// Decimal returns the canonical decimal string of the figure, or "" when there is
// none. Bounded by construction: at most ~26 digits given maxStrictCostRows.
func (a effectiveAmount) Decimal() string {
	if a.Value == nil {
		return ""
	}
	return a.Value.String()
}

// RepresentableInt64 reports whether the figure fits the ledger's int64 money column.
// It answers a REPRESENTABILITY question for a later increment's legacy projection;
// it is not a publication, and a false here must never become a clamped amount.
func (a effectiveAmount) RepresentableInt64() (int64, bool) {
	if a.Value == nil || !a.Value.IsInt64() {
		return 0, false
	}
	return a.Value.Int64(), true
}

// classifyEffectiveAmount is the algebra. Every combination is decided by what was
// ESTABLISHED, never by how the numbers happen to look:
//
//	cost exact + static known + dynamic known             → exact (their sum)
//	cost exact + static known + dynamic NON-NEGATIVE
//	  unknown (unobserved, invariant intact)              → lower_bound (cost + static)
//	cost partial (signed prefix) | any malformed          → unknown
//	static unknown or negative                            → unknown
//	dynamic negative (corrupt row)                        → unknown
//	dynamic INDETERMINATE (observation the invariant
//	  cannot absorb, or an unclassified component)        → unknown
//
// The sum is accumulated WIDE, and the reason is precise rather than folkloric:
// two's-complement addition does return the right answer whenever the true total is
// representable, so an unchecked int64 accumulator is not wrong there. What it cannot
// do is the other two cases. A CHECKED int64 accumulator (the one this package already
// uses for the reservation ceiling) must refuse an intermediate that leaves the range,
// and whether it refuses depends on the ORDER the credits arrive in — the same terms
// summed the other way through. And when the true total is genuinely outside int64,
// the unchecked sum silently returns an in-range number that is not the total, while a
// saturating one returns a clamped number presented as an amount. Wide arithmetic is
// what makes the answer independent of both.
func classifyEffectiveAmount(in effectiveAmountInputs) effectiveAmount {
	var causes []amountCause
	if !in.Cost.Complete || in.Cost.Total == nil {
		// A signed prefix proves nothing in either direction: the pages not read can
		// hold credits that cancel it, or charges that dwarf it.
		causes = append(causes, in.Cost.Causes...)
		if len(causes) == 0 {
			causes = append(causes, causeAmountNotDecidable)
		}
		return effectiveAmount{Class: amountUnknown, Causes: causes}
	}
	if !in.StaticKnown {
		return effectiveAmount{Class: amountUnknown, Causes: []amountCause{causeStaticUnknown}}
	}
	if in.StaticMicroUSD < 0 {
		return effectiveAmount{Class: amountUnknown, Causes: []amountCause{causeStaticNegative}}
	}
	if in.Dynamic.known() && in.Dynamic.MicroUSD < 0 {
		return effectiveAmount{Class: amountUnknown, Causes: []amountCause{causeDynamicNegative}}
	}
	if !in.Dynamic.carriesNonNegativeInvariant() {
		// Indeterminate, or a state nobody set. The invariant that would justify a floor
		// is exactly what is in doubt, so there is no floor to publish.
		cause := in.Dynamic.Cause
		if cause == "" {
			cause = causeDynamicUnverified
		}
		return effectiveAmount{Class: amountUnknown, Causes: []amountCause{cause}}
	}

	value := new(big.Int).Set(in.Cost.Total)
	value.Add(value, big.NewInt(in.StaticMicroUSD))
	if !in.Dynamic.known() {
		cause := in.Dynamic.Cause
		if cause == "" {
			cause = causeDynamicUnknown
		}
		// cost + static is a genuine lower bound of cost + static + dynamic BECAUSE
		// dynamic is non-negative — the domain invariant, held by a state that says so,
		// never inferred from the mere absence of a value.
		return effectiveAmount{Class: amountLowerBound, Value: value, Causes: []amountCause{cause}}
	}
	value.Add(value, big.NewInt(in.Dynamic.MicroUSD))
	return effectiveAmount{Class: amountExact, Value: value}
}

// crossingResult is the decision about ONE configured threshold. It is separate from
// the amount class on purpose: a lower_bound that reaches the threshold proves a real
// crossing, and an unknown amount is not "within budget".
type crossingResult string

const (
	// crossingProven: the threshold IS crossed, and the evidence shows why.
	crossingProven crossingResult = "proven"
	// crossingNotReached: the threshold is NOT crossed, and that is established. Only
	// an exact amount can say this.
	crossingNotReached crossingResult = "not_reached"
	// crossingUnproven: nothing is established either way. Not a crossing, and not a
	// statement that the budget is within its limit.
	crossingUnproven crossingResult = "unproven"
)

// thresholdDecision is one evaluated threshold: the normalized threshold actually
// configured, the target it implies against the limit, and what was proven.
type thresholdDecision struct {
	Result crossingResult
	// Threshold is the round-trip decimal of the configured float64 — the value an
	// operator wrote, not the binary expansion underneath it.
	Threshold string
	// Target is limit × threshold as an exact rational, kept wide for a later
	// increment; TargetDecimal is its exact decimal when it terminates within the
	// bound, "" otherwise. Neither is ever rounded to look exact.
	Target        *big.Rat
	TargetDecimal string
	Causes        []amountCause
}

// normalizedThreshold turns a configured float64 into the exact rational of the
// DECIMAL the operator configured, via its shortest round-trip representation. 0.8
// becomes 4/5, not the binary 0.8000000000000000444…, so a comparison near the limit
// does not turn on an artefact of float64 storage. A non-finite or out-of-range
// threshold yields an explicit cause and no invented percentage.
func normalizedThreshold(t float64) (*big.Rat, string, amountCause) {
	if math.IsNaN(t) || math.IsInf(t, 0) {
		return nil, "", causeThresholdNotFinite
	}
	if t <= 0 {
		// budgetSpec.fillDefaults already drops non-positive thresholds; a value that
		// reaches here anyway is configuration this file will not guess at.
		return nil, "", causeThresholdOutOfRange
	}
	text := strconv.FormatFloat(t, 'g', -1, 64)
	rat, ok := new(big.Rat).SetString(text)
	if !ok || rat.Sign() <= 0 {
		return nil, "", causeThresholdNotFinite
	}
	return rat, text, ""
}

// evaluateThresholdCrossing decides one threshold against one classified amount.
//
//	exact       → proven when amount ≥ target, not_reached otherwise. Both are facts.
//	lower_bound → proven when the bound ≥ target. Below it, UNPROVEN: the bound says
//	              nothing about the part that was never established.
//	unknown     → unproven, always. Never a crossing, never a "within limit".
func evaluateThresholdCrossing(amount effectiveAmount, limitMicroUSD int64, threshold float64) thresholdDecision {
	rat, text, cause := normalizedThreshold(threshold)
	if cause != "" {
		return thresholdDecision{Result: crossingUnproven, Causes: []amountCause{cause}}
	}
	out := thresholdDecision{Result: crossingUnproven, Threshold: text}
	if limitMicroUSD <= 0 {
		out.Causes = append(out.Causes, causeLimitNotPositive)
		return out
	}
	target := new(big.Rat).Mul(rat, new(big.Rat).SetInt64(limitMicroUSD))
	out.Target = target
	if decimal, ok := ratExactDecimal(target, maxExactDecimalDigits); ok {
		out.TargetDecimal = decimal
	}
	if amount.Value == nil || amount.Class == amountUnknown {
		out.Causes = append(out.Causes, amount.Causes...)
		if len(out.Causes) == 0 {
			out.Causes = append(out.Causes, causeAmountNotDecidable)
		}
		return out
	}
	reached := new(big.Rat).SetInt(amount.Value).Cmp(target) >= 0
	switch {
	case reached:
		out.Result = crossingProven
	case amount.Class == amountExact:
		out.Result = crossingNotReached
	default:
		// A lower bound below the target is not evidence that the target was not
		// reached: the unestablished remainder can be anything non-negative.
		out.Causes = append(out.Causes, amount.Causes...)
	}
	return out
}

// ratExactDecimal renders a rational as an exact decimal string, or reports that it
// does not terminate within maxDigits. It never rounds: a caller that cannot have the
// exact decimal is told so, because a rounded money figure that reads as exact is the
// defect this increment exists to prevent.
func ratExactDecimal(r *big.Rat, maxDigits int) (string, bool) {
	if r == nil {
		return "", false
	}
	if r.IsInt() {
		return r.Num().String(), true
	}
	for digits := 1; digits <= maxDigits; digits++ {
		text := r.FloatString(digits)
		if back, ok := new(big.Rat).SetString(text); ok && back.Cmp(r) == 0 {
			return text, true
		}
	}
	return "", false
}
