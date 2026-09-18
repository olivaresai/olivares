// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// -----------------------------------------------------------------------------
// From an independent review of the completeness rule: AN ERROR THAT ARRIVES AFTER A
// DECISION DOES NOT ERASE THE DECISION.
//
// Both admission paths keep evaluating the remaining budgets after one of them has
// refused — that is how block outranks throttle. So a read that fails LATER used to
// overwrite an established refusal with the outage posture (`Allowed:true` plus the
// error), which is exactly the shape the actuation seams turn into an admission.
// The refusal and the outage are different facts: the first is decided, the second
// is not, and only the second may fail open.
//
// The staging is the reviewer's: two real SQLite budgets, the first reservation read
// incomplete (HasMore with no cursor) so a refusal is decided, then the next read
// fails. The controls in the same table are what keep the fix from becoming "deny on
// every outage": with no refusal established, the ordinary allow-plus-error posture
// must survive untouched.
// -----------------------------------------------------------------------------

// admissionOutcome is the part of either admission API's answer this file judges, so
// ReserveBudget and CheckBudget can be driven through one table.
type admissionOutcome struct {
	Allowed bool
	Action  string
	Reason  string
	Err     error
}

func callAdmission(t *testing.T, m *Module, tenant model.TenantID, api string) admissionOutcome {
	t.Helper()
	switch api {
	case "ReserveBudget":
		res, err := m.ReserveBudget(context.Background(), tenant, SpendDims{}, oneUSD)
		return admissionOutcome{res.Allowed, res.Action, res.Reason, err}
	case "CheckBudget":
		chk, err := m.CheckBudget(context.Background(), tenant, SpendDims{})
		return admissionOutcome{chk.Allowed, chk.Action, chk.Reason, err}
	}
	t.Fatalf("unknown admission api %q", api)
	return admissionOutcome{}
}

// incompletePage is a read that finishes early: it promises more rows and hands back
// no cursor, so the reserved total cannot be established and a refusal is decided.
func incompletePage(int, model.Query) ([]model.Record, model.Page) {
	return nil, model.Page{HasMore: true}
}

// readsPerTarget is how many RESERVATION-repository reads one admission target
// costs, and it is a fixture fact rather than a rule: the staged failures below are
// call-indexed, so a case that means "break the SECOND budget" has to know where
// the first one ends.
//
// It is two because the version-aware hold reader reads both of its disjoint
// branches — the legacy one and the v1 one — and reads the second EVEN WHEN the
// first is already unestablished, so that a contradiction observed in a v1 prefix
// cannot hide behind a clean legacy prefix, as an independent review found. The monotonic seq probe is a
// sorted single-row query and the paging fixture deliberately leaves it on the real
// repository, so it is not one of these.
const readsPerTarget = 2

func TestDecidedDenialSurvivesALaterReadFailure(t *testing.T) {
	readFailure := errors.New("finops-test: reservation read I/O failure")
	conflict := fmt.Errorf("finops-test: lost the write race: %w", store.ErrConflict)

	for _, tc := range []struct {
		name string
		// failOn returns the error for the nth unsorted reservation read (nil = read
		// succeeds and the incomplete page is served).
		failOn func(call int) error
		// wantDenial: the answer must be a refusal with a nil error. Otherwise the
		// declared outage posture (allow + the error) must be intact.
		wantDenial bool
		wantErrIs  error
	}{
		{
			name:       "refusal only, no failure at all",
			failOn:     func(int) error { return nil },
			wantDenial: true,
		},
		{
			name:       "the read fails before anything was decided",
			failOn:     func(int) error { return readFailure },
			wantErrIs:  readFailure,
			wantDenial: false,
		},
		{
			name:       "every read conflicts before anything was decided",
			failOn:     func(int) error { return conflict },
			wantErrIs:  store.ErrConflict,
			wantDenial: false,
		},
		{
			// See readsPerTarget below: the first budget's reads are calls 0 and 1.
			name: "a refusal is decided and THEN the read fails",
			failOn: func(call int) error {
				if call < readsPerTarget {
					return nil
				}
				return readFailure
			},
			wantDenial: true,
		},
		{
			name: "a refusal is decided and THEN the read conflicts",
			failOn: func(call int) error {
				if call < readsPerTarget {
					return nil
				}
				return conflict
			},
			wantDenial: true,
		},
	} {
		for _, api := range []string{"ReserveBudget", "CheckBudget"} {
			t.Run(tc.name+"/"+api, func(t *testing.T) {
				m, st, tenant, _ := newFin(t)
				// Two enforcing budgets: the first can decide, the second is where the
				// failure lands. Without a second budget the case could not tell "decided
				// then broke" from "broke".
				for _, name := range []string{"first", "second"} {
					createBudget(t, st, tenant, "review-budget-"+name, budgetSpec{
						Dimension: "global", Period: "total", LimitMicroUSD: 10 * oneUSD, Action: "block",
					})
				}
				forcePagerWithErrors(m, incompletePage, tc.failOn)

				got := callAdmission(t, m, tenant, api)
				t.Logf("%s: allowed=%v action=%q reason=%q err=%v", api, got.Allowed, got.Action, got.Reason, got.Err)

				if rows := countReservations(t, st, tenant); len(rows) != 0 {
					t.Errorf("%d reservation rows survived: the transaction did not roll back", len(rows))
				}
				if tc.wantDenial {
					if got.Allowed {
						t.Fatalf("a refusal already decided was reported as an admission: %+v", got)
					}
					if got.Err != nil {
						t.Fatalf("a decided refusal left as an ERROR (%v): a caller that fails open on errors admits it", got.Err)
					}
					if got.Reason == "" || got.Action == "" {
						t.Errorf("the surviving refusal lost its action/reason: %+v", got)
					}
					return
				}
				if !got.Allowed {
					t.Fatalf("an outage with NOTHING decided was turned into a denial: %+v", got)
				}
				if !errors.Is(got.Err, tc.wantErrIs) {
					t.Fatalf("outage posture changed: err = %v, want one wrapping %v", got.Err, tc.wantErrIs)
				}
			})
		}
	}
}

// TestDecidedDenialKeepsBlockOverThrottleWhenALaterReadFails: the reason both paths
// keep going after a refusal is precedence. If surviving the failure meant keeping
// the FIRST refusal, a throttle would outrank a block whenever the read after them
// broke — so the surviving answer has to be the most restrictive one established,
// not the earliest.
func TestDecidedDenialKeepsBlockOverThrottleWhenALaterReadFails(t *testing.T) {
	for _, api := range []string{"ReserveBudget", "CheckBudget"} {
		t.Run(api, func(t *testing.T) {
			m, st, tenant, _ := newFin(t)
			// Created in this order, so the id keyset visits them in this order: the
			// throttle refusal is decided first and the block must outrank it before
			// the third read fails.
			createBudget(t, st, tenant, "a-throttle", budgetSpec{
				Dimension: "global", Period: "total", LimitMicroUSD: 10 * oneUSD, Action: "throttle",
			})
			createBudget(t, st, tenant, "b-block", budgetSpec{
				Dimension: "global", Period: "total", LimitMicroUSD: 10 * oneUSD, Action: "block",
			})
			createBudget(t, st, tenant, "c-breaks", budgetSpec{
				Dimension: "global", Period: "total", LimitMicroUSD: 10 * oneUSD, Action: "block",
			})
			// The first TWO budgets must get through, so the block can outrank the
			// throttle before the third breaks: two targets at readsPerTarget each.
			forcePagerWithErrors(m, incompletePage, func(call int) error {
				if call < 2*readsPerTarget {
					return nil
				}
				return errors.New("finops-test: reservation read I/O failure")
			})

			got := callAdmission(t, m, tenant, api)
			t.Logf("%s: allowed=%v action=%q reason=%q err=%v", api, got.Allowed, got.Action, got.Reason, got.Err)
			if got.Allowed || got.Err != nil {
				t.Fatalf("the established refusal did not survive: %+v", got)
			}
			if got.Action != "block" {
				t.Fatalf("action = %q, want block: the most restrictive refusal established must be the one returned", got.Action)
			}
			if rows := countReservations(t, st, tenant); len(rows) != 0 {
				t.Errorf("%d reservation rows survived a refused reservation", len(rows))
			}
		})
	}
}

// -----------------------------------------------------------------------------
// R1, remaining branch (second independent review): A GROUP'S FAIL-CLOSED REFUSAL
// IS A REFUSAL LIKE ANY OTHER — it does not get to overwrite a stronger one, and
// it does not get to end the evaluation early.
//
// The fail-closed group branch assigned `result` directly and returned the denial
// sentinel at once. Two consequences, both of them precedence: an already decided
// BLOCK was replaced by this target's throttle, and every later target — where a
// stronger block may be waiting — was never evaluated. CheckBudget's equivalent
// branch already recorded through its precedence guard and carried on, so the two
// admission paths disagreed about the same three policies.
//
// The four-case table below is the reviewer's own fixture, adapted: real SQLite,
// real policies, real group aggregation (the group is genuinely missing), the
// public APIs, and no repository replaced.
// -----------------------------------------------------------------------------

// missingGroupBudget creates a fail-closed user_group budget whose group does not
// exist, so the REAL aggregation fails and the fail-closed branch is reached.
func missingGroupBudget(t *testing.T, st store.Store, tenant model.TenantID, name, action string) string {
	t.Helper()
	missing := model.NewID().String()
	createBudget(t, st, tenant, name, budgetSpec{
		Dimension: "user_group", Key: missing, Period: "total",
		LimitMicroUSD: 100 * oneUSD, Action: action, FailClosed: true,
	})
	return missing
}

func TestGroupFailClosedRefusalKeepsTheStrongerRefusal(t *testing.T) {
	for _, withBlock := range []bool{false, true} {
		for _, api := range []string{"ReserveBudget", "CheckBudget"} {
			name := "group refusal alone/" + api
			want := "throttle"
			if withBlock {
				name = "a block is decided before the group fails/" + api
				want = "block"
			}
			t.Run(name, func(t *testing.T) {
				m, st, tenant, _ := newFin(t)
				// This one has room, so ReserveBudget inserts its row inside the
				// transaction: the later refusal must roll it back.
				createBudget(t, st, tenant, "a-has-room", budgetSpec{
					Dimension: "global", Period: "total", LimitMicroUSD: 100 * oneUSD, Action: "block",
				})
				if withBlock {
					createBudget(t, st, tenant, "b-already-blocked", budgetSpec{
						Dimension: "global", Period: "total", LimitMicroUSD: 10 * oneUSD,
						ReservedMicroUSD: 11 * oneUSD, Action: "block",
					})
				}
				missing := missingGroupBudget(t, st, tenant, "c-missing-group-throttle", "throttle")

				var got admissionOutcome
				if api == "ReserveBudget" {
					res, err := m.ReserveBudget(context.Background(), tenant, SpendDims{UserGroupRefs: []string{missing}}, oneUSD)
					got = admissionOutcome{res.Allowed, res.Action, res.Reason, err}
				} else {
					chk, err := m.CheckBudget(context.Background(), tenant, SpendDims{UserGroupRefs: []string{missing}})
					got = admissionOutcome{chk.Allowed, chk.Action, chk.Reason, err}
				}
				rows := countReservations(t, st, tenant)
				t.Logf("%s: allowed=%v action=%q reason=%q err=%v rows=%d want=%s", api, got.Allowed, got.Action, got.Reason, got.Err, len(rows), want)

				if len(rows) != 0 {
					t.Errorf("%d reservation rows survived the refusal: the roll-back is incomplete", len(rows))
				}
				if got.Allowed || got.Err != nil {
					t.Fatalf("the refusal changed shape: allowed=%v err=%v (a fail-closed group is a normal denial)", got.Allowed, got.Err)
				}
				if got.Action != want {
					t.Fatalf("action = %q, want %q: a later group refusal must not replace the strongest refusal already established", got.Action, want)
				}
			})
		}
	}
}

// TestGroupThrottleIsOutrankedByALaterBlock is the other half of the same rule and
// the reason the branch must not return early: the group refuses FIRST, so the only
// way a stronger block can be found is to keep evaluating. CheckBudget already did
// and is carried here as the parity control that says what the answer should be.
func TestGroupThrottleIsOutrankedByALaterBlock(t *testing.T) {
	for _, api := range []string{"ReserveBudget", "CheckBudget"} {
		t.Run(api, func(t *testing.T) {
			m, st, tenant, _ := newFin(t)
			createBudget(t, st, tenant, "a-has-room", budgetSpec{
				Dimension: "global", Period: "total", LimitMicroUSD: 100 * oneUSD, Action: "block",
			})
			missing := missingGroupBudget(t, st, tenant, "b-missing-group-throttle", "throttle")
			// Created last, so the id keyset visits it AFTER the group: 11 USD reserved
			// against a 10 USD limit leaves no headroom and it blocks.
			createBudget(t, st, tenant, "c-blocked", budgetSpec{
				Dimension: "global", Period: "total", LimitMicroUSD: 10 * oneUSD,
				ReservedMicroUSD: 11 * oneUSD, Action: "block",
			})

			var got admissionOutcome
			if api == "ReserveBudget" {
				res, err := m.ReserveBudget(context.Background(), tenant, SpendDims{UserGroupRefs: []string{missing}}, oneUSD)
				got = admissionOutcome{res.Allowed, res.Action, res.Reason, err}
			} else {
				chk, err := m.CheckBudget(context.Background(), tenant, SpendDims{UserGroupRefs: []string{missing}})
				got = admissionOutcome{chk.Allowed, chk.Action, chk.Reason, err}
			}
			rows := countReservations(t, st, tenant)
			t.Logf("%s: allowed=%v action=%q reason=%q err=%v rows=%d", api, got.Allowed, got.Action, got.Reason, got.Err, len(rows))

			if len(rows) != 0 {
				t.Errorf("%d reservation rows survived the refusal", len(rows))
			}
			if got.Allowed || got.Err != nil {
				t.Fatalf("the refusal changed shape: allowed=%v err=%v", got.Allowed, got.Err)
			}
			if got.Action != "block" {
				t.Fatalf("action = %q, want block: a group refusal that ends the evaluation hides every stronger cap behind it", got.Action)
			}
		})
	}
}

// TestGroupRefusalSurvivesALaterReadFailure joins this branch to the R1 rule already
// in force: once the group refusal is decided, an ordinary outage or a lost write
// race afterwards is still answered with the refusal and a nil error, not with the
// fail-open outage posture.
//
// Note what changed underneath it: while the branch returned immediately, the later
// read was never issued, so this held vacuously. With the evaluation continuing, the
// failure is really reached — the reservation read of the third budget — and the
// `decided` rule is what keeps the answer a refusal.
func TestGroupRefusalSurvivesALaterReadFailure(t *testing.T) {
	readFailure := errors.New("finops-test: reservation read I/O failure")
	conflict := fmt.Errorf("finops-test: lost the write race: %w", store.ErrConflict)

	for _, failure := range []struct {
		name string
		err  error
	}{
		{name: "ordinary I/O failure", err: readFailure},
		{name: "lost write race", err: conflict},
	} {
		for _, api := range []string{"ReserveBudget", "CheckBudget"} {
			t.Run(failure.name+"/"+api, func(t *testing.T) {
				m, st, tenant, _ := newFin(t)
				createBudget(t, st, tenant, "a-has-room", budgetSpec{
					Dimension: "global", Period: "total", LimitMicroUSD: 100 * oneUSD, Action: "block",
				})
				missing := missingGroupBudget(t, st, tenant, "b-missing-group-throttle", "throttle")
				createBudget(t, st, tenant, "c-reads-break", budgetSpec{
					Dimension: "global", Period: "total", LimitMicroUSD: 100 * oneUSD, Action: "block",
				})
				// The first budget's reservation reads succeed; the group budget never
				// reaches one (its aggregation fails first); the third one breaks.
				//
				// THE INJECTION WAS RE-AIMED, AND ONLY THE INJECTION. The staging is
				// call-indexed, and the version-aware hold reader issues TWO reservation
				// reads per target where the single-branch reader issued one: the legacy
				// branch (active, unexpired, original bucket, NULL lifecycle linkage) and
				// the disjoint v1 branch (the children of held attempt parents). So the
				// first budget now consumes calls 0 AND 1, and `call == 0` — the previous
				// predicate — broke the FIRST budget instead of the third, before the
				// group refusal had been decided at all. Measured: the case reported
				// allowed=true with reservation-reads=2, i.e. it was no longer exercising
				// the rule it exists for.
				//
				// Every assertion below is unchanged. What moved is where the staged
				// failure lands, back to the budget this comment always said it lands on.
				pager := forcePagerWithErrors(m, nil, func(call int) error {
					if call < readsPerTarget {
						return nil
					}
					return failure.err
				})

				var got admissionOutcome
				if api == "ReserveBudget" {
					res, err := m.ReserveBudget(context.Background(), tenant, SpendDims{UserGroupRefs: []string{missing}}, oneUSD)
					got = admissionOutcome{res.Allowed, res.Action, res.Reason, err}
				} else {
					chk, err := m.CheckBudget(context.Background(), tenant, SpendDims{UserGroupRefs: []string{missing}})
					got = admissionOutcome{chk.Allowed, chk.Action, chk.Reason, err}
				}
				rows := countReservations(t, st, tenant)
				t.Logf("%s: allowed=%v action=%q reason=%q err=%v rows=%d reservation-reads=%d",
					api, got.Allowed, got.Action, got.Reason, got.Err, len(rows), pager.listCount())

				if len(rows) != 0 {
					t.Errorf("%d reservation rows survived the refusal", len(rows))
				}
				if got.Allowed {
					t.Fatalf("a decided group refusal was reported as an admission: %+v", got)
				}
				if got.Err != nil {
					t.Fatalf("a decided group refusal left as an ERROR (%v): a caller that fails open on errors admits it", got.Err)
				}
				if got.Action != "throttle" || got.Reason == "" {
					t.Fatalf("the surviving refusal is not the group's: action=%q reason=%q", got.Action, got.Reason)
				}
			})
		}
	}
}

// TestReserveDefaultsAnEmptyTargetActionToBlock pins the default this branch has
// always applied. It goes through reserve() directly and says why: a BUDGET can no
// longer carry an empty action by the time it becomes a target — fillDefaults maps
// "" to alert, and alert-only budgets are filtered out before the loop — so the
// default is defensive, and a test through the public API could not reach it. What
// it must do is refuse as a block in every respect, including outranking a throttle
// already recorded, rather than as an action nobody can read.
func TestReserveDefaultsAnEmptyTargetActionToBlock(t *testing.T) {
	failing := func(context.Context, store.Scope) (aggResult, error) {
		return aggResult{}, errors.New("finops-test: group members unresolvable")
	}
	target := func(name, action string) reservationTarget {
		return reservationTarget{
			policyID: model.NewID(), policyKind: policyKindBudget, name: name, action: action,
			dimension: "user_group", scopeKey: "g-" + name, period: "total",
			ceiling: 100 * oneUSD, spend: failing, failClosed: true,
		}
	}
	for _, tc := range []struct {
		name    string
		targets []reservationTarget
	}{
		{name: "alone", targets: []reservationTarget{target("no-action", "")}},
		{name: "after a throttle was recorded", targets: []reservationTarget{target("throttling", "throttle"), target("no-action", "")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, st, tenant, _ := newFin(t)
			res, err := m.reserve(context.Background(), tenant, tc.targets, oneUSD, time.Now().UTC())
			t.Logf("allowed=%v action=%q reason=%q err=%v", res.Allowed, res.Action, res.Reason, err)
			if err != nil {
				t.Fatalf("a fail-closed refusal must be a normal denial, not an error: %v", err)
			}
			if res.Allowed || res.Action != "block" {
				t.Fatalf("allowed=%v action=%q, want a refusal with the default action block", res.Allowed, res.Action)
			}
			if rows := countReservations(t, st, tenant); len(rows) != 0 {
				t.Errorf("%d reservation rows survived", len(rows))
			}
		})
	}
}
