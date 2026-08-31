// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

// reservation_group_test.go closes the last dimension the reserve ledger left open.
//
// The reserve ledger made admission atomic for every enforcing budget EXCEPT the group
// dimensions, which stayed on the read-only preventive path: reservation.go declared
// "Group-dimension budgets are not reserved here" and skipped them by
// `isGroupDimension(spec.Dimension)`. A skipped dimension is not a smaller race, it is the SAME
// race — N concurrent requests all read a spend read-model that none of them has written yet,
// and all N pass a cap that affords M-1.
//
// The first test below is the RED CHARACTERIZATION: it is written to pass against the
// pre-fix behavior, documenting the over-admit as a measured fact rather than an assertion in
// a commit message. The second is the fix.
//
// MUTATIONS THAT MUST TURN THIS RED:
//
//  1. Put `isGroupDimension(spec.Dimension)` back in ReserveBudget's skip condition. Red in
//     `a group budget admits exactly what it can pay for`, and the over-admit test goes back to
//     describing the live behavior.
//  2. Give the group target the non-group `spend` closure (spec.sampleFilters()). Red in
//     `a group budget counts its MEMBERS' spend and nobody else's`. That case is written the
//     way it is because the obvious version of it caught nothing: dimensionColumn("user_group")
//     is "", so sampleFilters() returns nil and aggregatePeriod then sums the WHOLE TENANT —
//     which, in a fixture whose only spend is the group's, equals the fan-out exactly. The
//     discriminating fixture needs spend belonging to the tenant and not to the group.
//
// NOT COVERED HERE, said rather than implied: the truncated-aggregate deny. It lives in the
// shared reserve loop (reservation.go:279-283), applies to every target regardless of
// dimension, and the group target reaches it through the same path — so it is covered by the
// existing non-group cases, and this file adds no group-specific claim about it.

// groupBudgetFixture builds a tenant with one user group, two members, and one enforcing
// user_group budget whose cap affords exactly `affords` requests of oneUSD.
type groupBudgetFixture struct {
	m       *Module
	tenant  model.TenantID
	groupID string
	members []model.ID
}

func newGroupBudgetFixture(t *testing.T, affords int) groupBudgetFixture {
	t.Helper()
	m, st, tenant, _ := newFin(t)
	u1 := createCanonicalUser(t, st, "reservation group member one").ID
	u2 := createCanonicalUser(t, st, "reservation group member two").ID
	group := createUserGroup(t, st, tenant, "engineering", u1, u2)
	createBudget(t, st, tenant, "user-group-cap", budgetSpec{
		Dimension:     "user_group",
		Key:           group.ID.String(),
		Period:        "monthly",
		LimitMicroUSD: int64(affords) * oneUSD,
		Action:        "block",
	})
	return groupBudgetFixture{m: m, tenant: tenant, groupID: group.ID.String(), members: []model.ID{u1, u2}}
}

func (f groupBudgetFixture) dims() SpendDims {
	return SpendDims{UserGroupRefs: []string{f.groupID}}
}

// TestGroupBudgetReserveAdmitsExactlyWhatItCanPayFor is the fix.
//
// M goroutines race to reserve against a group budget that affords M-1. Exactly M-1 must be
// admitted: the group dimension now consumes headroom the instant the reservation commits,
// like every other enforcing dimension.
func TestGroupBudgetReserveAdmitsExactlyWhatItCanPayFor(t *testing.T) {
	t.Run("a group budget admits exactly what it can pay for", func(t *testing.T) {
		const M = 8
		f := newGroupBudgetFixture(t, M-1)

		var wg sync.WaitGroup
		start := make(chan struct{})
		var allowed, denied int64
		wg.Add(M)
		for i := 0; i < M; i++ {
			go func() {
				defer wg.Done()
				<-start
				res, err := f.m.ReserveBudget(context.Background(), f.tenant, f.dims(), oneUSD)
				if err != nil {
					t.Errorf("ReserveBudget: %v", err)
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

		if allowed != int64(M-1) || denied != 1 {
			t.Fatalf("a group budget that affords %d admitted %d and denied %d; before the fix all %d were admitted, which is the over-admit this closes",
				M-1, allowed, denied, M)
		}
	})

	t.Run("a group budget counts its MEMBERS' spend and nobody else's", func(t *testing.T) {
		// THIS CASE EXISTS BECAUSE THE OBVIOUS VERSION OF IT DISCRIMINATED NOTHING.
		//
		// The first version spent the whole cap through one member and asserted the
		// reservation denied. It passed — and it also passed when the group target was
		// handed the NON-group reader, which is the mutation it was written to catch.
		// The reason is measured: dimensionColumn("user_group") is "", so
		// sampleFilters() returns nil and aggregatePeriod with nil filters sums the
		// WHOLE TENANT for the period. In a fixture whose only spend belongs to the
		// group, the wrong aggregate and the right one are the same number.
		//
		// So the discriminating fixture needs spend that belongs to the tenant and NOT
		// to the group. Fan-out admits (the members are under their cap); the unfiltered
		// sum denies, binding the group to a stranger's spending.
		f := newGroupBudgetFixture(t, 3)
		now := f.m.clock.Now().Time()

		member := mkCost("anthropic", "claude-opus-4-8", "", 10, 5, oneUSD, now)
		member.Actor = f.members[0].String()
		f.m.ingest(t, f.tenant, member)

		outsider := mkCost("anthropic", "claude-opus-4-8", "", 10, 5, 5*oneUSD, now.Add(time.Minute))
		outsider.Actor = model.NewID().String() // in the tenant, not in the group
		f.m.ingest(t, f.tenant, outsider)

		res, err := f.m.ReserveBudget(context.Background(), f.tenant, f.dims(), oneUSD)
		if err != nil {
			t.Fatalf("ReserveBudget: %v", err)
		}
		if !res.Allowed {
			t.Fatalf("the group has spent 1 of a cap of 3 and the reservation denied: it is counting spend that belongs to somebody outside the group (%+v)", res)
		}

		// And the fan-out really is what binds it: pushing the MEMBERS over the cap denies.
		over := mkCost("anthropic", "claude-opus-4-8", "", 10, 5, 3*oneUSD, now.Add(2*time.Minute))
		over.Actor = f.members[1].String()
		f.m.ingest(t, f.tenant, over)
		denied, err := f.m.ReserveBudget(context.Background(), f.tenant, f.dims(), oneUSD)
		if err != nil {
			t.Fatalf("ReserveBudget: %v", err)
		}
		if denied.Allowed {
			t.Fatalf("the members are over the cap and the reservation admitted: %+v", denied)
		}
		if denied.SpendMicroUSD != 4*oneUSD {
			t.Errorf("the deny reports spend=%d, want %d (the two members' 1+3): an operator reading a number that includes strangers cannot tell which budget bound the request",
				denied.SpendMicroUSD, 4*oneUSD)
		}
	})

	t.Run("a member outside the group is not bound by its budget", func(t *testing.T) {
		f := newGroupBudgetFixture(t, 1)
		now := f.m.clock.Now().Time()
		c := mkCost("anthropic", "claude-opus-4-8", "", 10, 5, 5*oneUSD, now)
		c.Actor = f.members[0].String()
		f.m.ingest(t, f.tenant, c)

		res, err := f.m.ReserveBudget(context.Background(), f.tenant, SpendDims{}, oneUSD)
		if err != nil {
			t.Fatalf("ReserveBudget: %v", err)
		}
		if !res.Allowed {
			t.Fatalf("a request that belongs to no group must not be bound by a group budget: %+v", res)
		}
	})

	t.Run("a released reservation returns the group's headroom", func(t *testing.T) {
		const M = 4
		f := newGroupBudgetFixture(t, M-1)
		handles := make([]string, 0, M-1)
		for i := 0; i < M-1; i++ {
			res, err := f.m.ReserveBudget(context.Background(), f.tenant, f.dims(), oneUSD)
			if err != nil || !res.Allowed {
				t.Fatalf("reservation %d must be admitted: %+v err=%v", i, res, err)
			}
			handles = append(handles, res.Handle)
		}
		full, err := f.m.ReserveBudget(context.Background(), f.tenant, f.dims(), oneUSD)
		if err != nil {
			t.Fatalf("ReserveBudget: %v", err)
		}
		if full.Allowed {
			t.Fatal("the group budget is fully reserved and admitted one more")
		}
		if err := f.m.ReleaseReservation(context.Background(), f.tenant, handles[0]); err != nil {
			t.Fatalf("ReleaseReservation: %v", err)
		}
		again, err := f.m.ReserveBudget(context.Background(), f.tenant, f.dims(), oneUSD)
		if err != nil {
			t.Fatalf("ReserveBudget after release: %v", err)
		}
		if !again.Allowed {
			t.Fatalf("releasing a reservation must return the group's headroom: %+v", again)
		}
	})
}

// TestGroupBudgetPreventiveCheckStillEnforces keeps the older path honest. Reserving is the
// atomic admission, but CheckBudget remains the preventive one for callers that do not reserve,
// and a change that moved enforcement into the reservation must not have taken it out of here.
func TestGroupBudgetPreventiveCheckStillEnforces(t *testing.T) {
	f := newGroupBudgetFixture(t, 1)
	now := f.m.clock.Now().Time()
	c := mkCost("anthropic", "claude-opus-4-8", "", 10, 5, 2*oneUSD, now.Add(time.Minute))
	c.Actor = f.members[1].String()
	f.m.ingest(t, f.tenant, c)

	chk, err := f.m.CheckBudget(context.Background(), f.tenant, f.dims())
	if err != nil {
		t.Fatalf("CheckBudget: %v", err)
	}
	if chk.Allowed || chk.Action != "block" {
		t.Fatalf("the preventive group check must still block once the members are over the cap: %+v", chk)
	}
}

// TestReserveBudgetBindsTheSameBudgetsAsCheckBudget closes the two asymmetries the contrast
// measured on this PR, and both existed because a claim was made without a test behind it.
//
// The PR's own commit message said the two admission paths "match the same budgets". They did
// not, in two different ways, and the regressions below are the shape that claim should have
// had from the start.
func TestReserveBudgetBindsTheSameBudgetsAsCheckBudget(t *testing.T) {
	t.Run("a fail-closed group that cannot resolve DENIES instead of erroring", func(t *testing.T) {
		// CheckBudget turns an unresolvable group into a normal deny (budgets.go, and
		// TestCheckBudgetGroupFailClosed pins it). The reservation path used to return an
		// ALLOWED result carrying the error, which the seam's documented fail-open handling
		// then converts into an admission — the exact opposite of what the operator asked
		// for by writing fail_closed.
		m, st, tenant, _ := newFin(t)
		missing := model.NewID().String()
		createBudget(t, st, tenant, "broken-group-cap", budgetSpec{
			Dimension: "user_group", Key: missing, Period: "monthly", LimitMicroUSD: 1000,
			Action: "block", FailClosed: true,
		})

		res, err := m.ReserveBudget(context.Background(), tenant,
			SpendDims{UserGroupRefs: []string{missing}}, oneUSD)
		if err != nil {
			t.Fatalf("a fail-closed group must produce a refusal, not an error the caller fails open on: %v", err)
		}
		if res.Allowed {
			t.Fatalf("a fail-closed group whose members cannot be resolved admitted the request: %+v", res)
		}
		if res.Reason != "group budget check failed (fail-closed)" {
			t.Errorf("reason = %q, want the same words CheckBudget uses, so an operator reading either path sees one vocabulary", res.Reason)
		}
	})

	t.Run("an enforcing budget beyond the first page still binds the reservation", func(t *testing.T) {
		// THE FIRST VERSION OF THIS CASE DISCRIMINATED NOTHING, and the reason is worth
		// keeping: it created 1,001 IDENTICAL enforcing budgets, so page one already held
		// one that denied and both paths agreed whatever the enumeration did. It also
		// asserted against truncation, which needs maxScanPages*listCap budgets, not
		// listCap+1 — so the flag was never set either.
		//
		// The real asymmetry is about SEEING the budget: the reservation read one page and
		// dropped HasMore. So the fixture puts a thousand budgets that never bind ahead of
		// ONE enforcing block budget created last, which the id keyset therefore sorts onto
		// the second page.
		m, st, tenant, _ := newFin(t)
		for i := 0; i < listCap; i++ {
			createBudget(t, st, tenant, fmt.Sprintf("noise-%04d", i), budgetSpec{
				Dimension: "global", Period: "monthly", LimitMicroUSD: 1_000_000 * oneUSD, Action: "alert",
			})
		}
		createBudget(t, st, tenant, "the-one-that-binds", budgetSpec{
			Dimension: "global", Period: "monthly", LimitMicroUSD: 0, Action: "block",
		})

		res, err := m.ReserveBudget(context.Background(), tenant, SpendDims{}, oneUSD)
		if err != nil {
			t.Fatalf("ReserveBudget: %v", err)
		}
		if res.Allowed {
			t.Fatalf("an enforcing block budget on the second page did not bind the reservation: %+v — one unread page is enough for a cap to be invisible to the admission that is supposed to be the atomic one", res)
		}
	})
}
