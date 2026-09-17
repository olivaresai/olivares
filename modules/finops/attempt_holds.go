// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

// D02 attempt lifecycle — the ONE version-aware hold reader.
//
// heldReservedForWindow answers the question every ceiling evaluation asks: how
// much of this policy+scope's headroom is currently HELD by live obligations. It
// replaces the single-branch reader with two DISJOINT branches over the same
// ledger:
//
//   - LEGACY, unchanged: active rows, not yet expired, in the original period
//     bucket. The TTL predicate stays here and only here.
//   - V1: the children of a HELD attempt parent, cross-checked against that
//     parent's manifest. NO TTL — an imported hold whose legacy expires_at has long
//     passed is still money the tenant is holding, and letting the TTL drop it
//     would silently manufacture headroom. NO rollover either: an obligation is
//     counted ONCE, in every window whose end is after the instant it is accounted
//     to, not copied into each period it spans.
//
// The temporal rule for v1, stated exactly because it is the part that is easy to
// get subtly wrong:
//
//   - KNOWN accounting instant: the hold carries into any window whose end is
//     after it. A window that closes before the obligation was accounted for does
//     not carry it.
//   - UNKNOWN accounting instant (an import whose history does not establish one):
//     the hold is retained for CURRENT AND FUTURE admission with NO temporal
//     exclusion. It is not zero and it is not given a fabricated date. What it
//     cannot do is answer a HISTORICAL question — see the note on windowedHold.
//
// Money comes from the CHILD rows, never from the parent: the parent is metadata
// and is never summed. The manifest is what makes deleting the only child fail
// loudly instead of reading as zero.

import (
	"context"
	"reflect"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// heldReservedForWindow returns the held reserve for one policy+scope over an
// explicit window.
//
// start/end/hasBounds are the CALLER's real window — the policy period it is
// evaluating — and never inferred from now. hasBounds false means the unbounded
// "total" period, which excludes nothing temporally.
//
// The returned reservedTotal keeps the EXISTING vocabulary. No new wire cause is
// introduced by this cut: a v1 shape fault, a missing expected child or an orphan
// is reservedIndeterminate with the existing out-of-scope cause, and a clean
// bounded paging incompleteness is reservedNonNegativeUnknown with its existing
// scan cause.
func heldReservedForWindow(
	ctx context.Context,
	sc store.Scope,
	policyID model.ID,
	scopeKey string,
	start, end time.Time,
	hasBounds bool,
	now time.Time,
) (reservedTotal, error) {
	repo, err := sc.Ext(budgetReservationKind)
	if err != nil {
		return reservedTotal{}, err
	}
	tenant := sc.Tenant()

	// The legacy branch first, exactly as it has always read.
	legacy, err := activeReservedMicroUSD(ctx, repo, tenant, policyID, scopeKey, start, now)
	if err != nil {
		return reservedTotal{}, err
	}
	// BOTH BRANCHES ARE ALWAYS READ (R6). The first cut returned the legacy result
	// as soon as it was not established, which short-circuited the v1 inspection
	// entirely: a v1 prefix containing a negative amount, a foreign-tenant row or an
	// unreadable parent was never looked at, and the clean non-negative class the
	// legacy branch had produced traveled on as though nothing had been observed.
	// A contradiction is a fact about the ledger wherever it is seen.
	v1, err := v1HeldForWindow(ctx, sc, repo, policyID, scopeKey, end, hasBounds, now)
	if err != nil {
		return reservedTotal{}, err
	}
	// Precedence: an observed contradiction outranks an unobserved remainder, which
	// outranks an exact figure. Anything else would let a clean half certify a
	// corrupt one.
	if legacy.State == reservedIndeterminate {
		return legacy, nil
	}
	if v1.State == reservedIndeterminate {
		return v1, nil
	}
	if !legacy.established() {
		return legacy, nil
	}
	if !v1.established() {
		return v1, nil
	}
	total := addInt64(legacy.MicroUSD, v1.MicroUSD)
	if !total.OK {
		return indeterminateReserved(resvScanComplete, resvFaultSumUnrepresentable), nil
	}
	return reservedTotal{MicroUSD: total.Value, State: reservedKnown, Scan: resvScanComplete}, nil
}

// v1HeldForWindow sums the v1 obligations of one policy+scope.
//
// The shape of the read, and why it is this shape: it starts from the PARENTS, not
// from the child rows. Starting from the children and finding none would conclude
// zero, which is exactly the answer a deleted child produces — so the manifest of
// every held parent states which children MUST exist, and their absence is a fault
// rather than a smaller number. The child scan is then checked in both directions:
// every expected child must be present, and every present in-scope v1 child must be
// claimed by its parent WITH THE SAME ATTRIBUTION.
//
// CONTRADICTIONS ARE INSPECTED BEFORE THE CLASS IS ASSIGNED (R6). The first cut
// returned the non-negative class the moment either enumeration was incomplete —
// before it had looked at a single row — so a prefix containing an unreadable
// parent, a foreign-tenant child or a negative amount was indistinguishable from a
// clean one, and A4.2's evidence algebra rested a published lower bound on it. Every
// observed row is validated first; only a prefix whose observations are all clean
// keeps the invariant.
//
// Both complete tenant parent and child censuses are re-read per target and
// bounded by the existing page cap. This is a private correctness cut, with no
// scaling claim or manifest index/schema change. A large tenant can hit that
// explicit bound; it cannot be admitted on a truncated exact total.
func v1HeldForWindow(
	ctx context.Context,
	sc store.Scope,
	repo store.GenericRepo,
	policyID model.ID,
	scopeKey string,
	end time.Time,
	hasBounds bool,
	now time.Time,
) (reservedTotal, error) {
	tenant := sc.Tenant()
	attemptRepo, err := sc.Ext(attemptKind)
	if err != nil {
		return reservedTotal{}, err
	}
	parentRows, parentScan, err := scanReservationsTyped(ctx, attemptRepo, nil)
	if err != nil {
		return reservedTotal{}, err
	}

	parents := map[AttemptRef]AttemptView{}
	byHandle := map[string]AttemptRef{}
	for _, row := range parentRows {
		view, err := decodeAttemptRow(tenant, row)
		if err != nil {
			return indeterminateReserved(parentScan, resvFaultRowOutOfScope), nil
		}
		if _, duplicate := parents[view.AttemptRef]; duplicate {
			return indeterminateReserved(parentScan, resvFaultRowOutOfScope), nil
		}
		if _, duplicate := byHandle[view.Handle.String()]; duplicate {
			return indeterminateReserved(parentScan, resvFaultRowOutOfScope), nil
		}
		parents[view.AttemptRef] = view
		byHandle[view.Handle.String()] = view.AttemptRef
	}

	// Scan once, without a policy/state subset: the commitment covers ALL children,
	// including an unlimited witness and rows rekeyed away from this query. Legacy
	// rows remain owned by the unchanged legacy reader unless mixed into a parent.
	childRows, childScan, err := scanReservationsTyped(ctx, repo, nil)
	if err != nil {
		return reservedTotal{}, err
	}
	groups := map[AttemptRef][]model.Record{}
	seen := map[string]model.Record{}
	for _, row := range childRows {
		link, ref := linkageOf(row)
		if link == linkageLegacy {
			if _, mixed := byHandle[row.String(colResvHandle)]; mixed {
				return indeterminateReserved(childScan, resvFaultRowOutOfScope), nil
			}
			continue
		}
		if tenantCellOf(row, tenant) != tenantCellMatches {
			return indeterminateReserved(childScan, resvFaultRowTenantMalformed), nil
		}
		if link == linkageMalformed {
			return indeterminateReserved(childScan, resvFaultRowOutOfScope), nil
		}
		amount, ok := int64Cell(row, colResvAmount)
		if !ok {
			return indeterminateReserved(childScan, resvFaultRowAmountMalformed), nil
		}
		if amount < 0 {
			return indeterminateReserved(childScan, resvFaultRowAmountNegative), nil
		}
		id, err := model.ParseID(row.String(model.ColID))
		if err != nil {
			return indeterminateReserved(childScan, resvFaultRowOutOfScope), nil
		}
		if previous, duplicate := seen[id.String()]; duplicate {
			// A stalled enumeration can repeat an identical observation. It is still
			// incomplete, without a contradiction or any publishable exact sum.
			if !childScan.complete() && reflect.DeepEqual(previous, row) {
				continue
			}
			return indeterminateReserved(childScan, resvFaultRowOutOfScope), nil
		}
		seen[id.String()] = row
		if owner, found := byHandle[row.String(colResvHandle)]; found && owner != ref {
			return indeterminateReserved(childScan, resvFaultRowOutOfScope), nil
		}
		if _, found := parents[ref]; !found {
			if parentScan.complete() {
				return indeterminateReserved(childScan, resvFaultRowOutOfScope), nil
			}
			continue // an unobserved parent is not a proven orphan
		}
		groups[ref] = append(groups[ref], row)
	}

	total := checkedSum{OK: true}
	unallocatable := false
	for ref, parent := range parents {
		rows := groups[ref]
		if err := validateAttemptGroup(tenant, parent, rows, childScan.complete()); err != nil {
			return indeterminateReserved(childScan, resvFaultRowOutOfScope), nil
		}
		// Money comes only from validated children. A missing child was a fault if
		// its census completed; a clean prefix is classified below, never summed as
		// a complete total.
		for _, row := range rows {
			if !textCellEquals(row, colResvPolicyRef, policyID.String()) || !textCellEquals(row, colResvScopeKey, scopeKey) {
				continue
			}
			switch windowVerdict(parent, end, hasBounds, now) {
			case holdInWindow:
				amount, _ := int64Cell(row, colResvAmount)
				total = addInt64(total.Value, amount)
				if !total.OK {
					return indeterminateReserved(childScan, resvFaultSumUnrepresentable), nil
				}
			case holdUnallocatable:
				unallocatable = true
			case holdOutOfWindow:
			}
		}
	}
	if !parentScan.complete() {
		return reservedTotal{Incomplete: parentScan.text(), State: reservedNonNegativeUnknown, Scan: parentScan}, nil
	}
	if !childScan.complete() {
		return reservedTotal{Incomplete: childScan.text(), State: reservedNonNegativeUnknown, Scan: childScan}, nil
	}
	if unallocatable {
		return reservedTotal{Incomplete: "a held obligation has no established accounting instant for this closed window",
			State: reservedNonNegativeUnknown, Scan: resvScanComplete, UnallocatedHistorical: true}, nil
	}
	return reservedTotal{MicroUSD: total.Value, State: reservedKnown, Scan: resvScanComplete}, nil
}

// holdWindowVerdict is what a held obligation contributes to ONE query window.
type holdWindowVerdict int

const (
	// holdInWindow: the obligation counts toward this window.
	holdInWindow holdWindowVerdict = iota
	// holdOutOfWindow: it is established that the obligation belongs elsewhere.
	holdOutOfWindow
	// holdUnallocatable: the obligation is real, and WHETHER it belongs to this
	// window cannot be established from what history recorded.
	holdUnallocatable
)

// windowVerdict decides what one held parent contributes to a query window.
//
// KNOWN accounting instant: the hold carries into any window that ends after it.
// The window's START is deliberately not applied — a hold accounted earlier is
// money still held now, and excluding it would report headroom that is not there.
//
// UNKNOWN accounting instant, and this is the R5 correction: it depends on the
// window, because the two questions a caller can ask are different facts.
//
//   - A window that is unbounded, or that has not closed yet, is an ADMISSION
//     question: how much headroom is there now, or will there be. The obligation is
//     retained IN FULL and conservatively — refusing to count it would report
//     headroom the tenant does not have, and it is not given a fabricated date.
//   - A window that CLOSED BEFORE NOW is a HISTORICAL ALLOCATION question, and
//     history does not establish that this obligation belongs to that month. The
//     first cut answered it in full anyway, and the caller that exposed it is real:
//     ingestion evaluates budgets for the period of the sample's occurred_at, so a
//     cost arriving late is evaluated against a closed window — 4 of real cost plus
//     7 of undated hold produced an exact 11 against a limit of 10. Neither zero
//     nor an invented date is the answer; the answer is that the allocation is not
//     established.
func windowVerdict(view AttemptView, end time.Time, hasBounds bool, now time.Time) holdWindowVerdict {
	if view.AccountingAt != nil {
		if !hasBounds || view.AccountingAt.Time().Before(end) {
			return holdInWindow
		}
		return holdOutOfWindow
	}
	if !hasBounds || end.After(now) {
		return holdInWindow
	}
	return holdUnallocatable
}
