// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// TestBudgetFailureNormalisation pins the decision function itself.
//
// The runner has nine roundtrips whose expiry must surface as the budget sentinel rather
// than as whatever the canceled operation returned, and all nine route through
// budgetFailure. This is that function's own truth table, so a change to the RULE is
// caught here even when a change to a CALL SITE is not.
//
// The two properties that matter and are easy to get backwards:
//
//   - the caller's own cancellation WINS. An operator who stops a boot must not have their
//     Ctrl-C relabelled as a deadline this runner set.
//   - the budget's CLOCK decides, not the error's shape. pgx converts an operation on an
//     already-expired context into driver.ErrBadConn, which carries no deadline at all —
//     so a rule that only matched context.DeadlineExceeded would miss the most common real
//     case.
func TestBudgetFailureNormalisation(t *testing.T) {
	t.Parallel()

	u := retryUnit{Plan: lockPlan{Target: plannedLock{Schema: "public", Name: "t", Mode: lockModeRowExclusive}}}
	opaque := errors.New("driver: bad connection")

	// spent is a budget with nothing left; alive has minutes.
	frozen := time.Now()
	spent := newLockBudget(0, func() time.Time { return frozen }, sleepCtx, jitterFloat)
	alive := newLockBudget(10*time.Minute, func() time.Time { return frozen }, sleepCtx, jitterFloat)

	canceled, cancel := context.WithCancel(context.Background())
	cancel()

	for _, tc := range []struct {
		name         string
		parent       context.Context
		b            *lockBudget
		err          error
		wantSentinel bool
		why          string
	}{
		{
			name: "spent budget with an opaque driver error", parent: context.Background(),
			b: spent, err: opaque, wantSentinel: true,
			why: "the clock decides; pgx turns an expired context into ErrBadConn, which names no deadline",
		},
		{
			name: "spent budget with a deadline error", parent: context.Background(),
			b: spent, err: context.DeadlineExceeded, wantSentinel: true,
			why: "the plain case",
		},
		{
			name: "live budget with a deadline error", parent: context.Background(),
			b: alive, err: context.DeadlineExceeded, wantSentinel: true,
			why: "a deadline fired under a live budget is still a deadline this runner set on the operation",
		},
		{
			name: "live budget with an ordinary server error", parent: context.Background(),
			b: alive, err: &pgconn.PgError{Code: sqlStateInsufficientPriv}, wantSentinel: false,
			why: "42501 is a privilege problem and must keep its own diagnosis",
		},
		{
			name: "CALLER CANCELED, spent budget, deadline error", parent: canceled,
			b: spent, err: context.DeadlineExceeded, wantSentinel: false,
			why: "the operator's cancellation wins; relabelling it as our deadline hides who stopped the boot",
		},
		{
			name: "CALLER CANCELED with an opaque error", parent: canceled,
			b: spent, err: opaque, wantSentinel: false,
			why: "same rule, other error shape",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := u.budgetFailure(tc.parent, tc.b, "doing something", tc.err)
			isSentinel := errors.Is(got, ErrMigrationLockBudgetExceeded)
			if isSentinel != tc.wantSentinel {
				t.Errorf("budgetFailure sentinel = %v, want %v: %s", isSentinel, tc.wantSentinel, tc.why)
			}
			// The original error must survive either way: a normalised error that swallows
			// its cause leaves an operator with a category and no evidence.
			if !errors.Is(got, tc.err) {
				t.Errorf("budgetFailure dropped its cause: %v does not wrap %v", got, tc.err)
			}
		})
	}
}

// TestEveryBudgetBoundedRoundtripNormalisesItsExpiry is a COVERAGE CANARY, and it is
// labeled as one rather than dressed up as a behavioral test.
//
// The contrast round demonstrated the gap it closes: eight of the nine budgetFailure call
// sites were removed together and the ENTIRE suite stayed green —
//
//	BUDGET_SIBLINGS_MUTANT|removed=8|pass=415|skip=0|fail=0|exit=0
//
// The implementation was not wrong; the point is that eight regressions could evaporate
// without the gate noticing, and a regression that can vanish silently is not one. Each of
// those sites would need its own seam to test behaviourally — BeginTx, three lock
// statements and two footprint reads have no injection point that does not change
// production code — so what is asserted instead is the structural fact: every operation
// issued under a budget-derived context normalises its expiry.
//
// It is deliberately crude and deliberately honest. It cannot prove the normalisation is
// CORRECT — TestBudgetFailureNormalisation does that for the rule, and
// TestRetryUnitReportsASpentBudgetAsASpentBudget does it end-to-end for the site where it
// mattered most. What it does prove is that the sites did not quietly disappear.
func TestEveryBudgetBoundedRoundtripNormalisesItsExpiry(t *testing.T) {
	t.Parallel()

	const path = "migrationretry.go"
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	body := string(src)

	// Count the operations that run under a budget-derived context, and the
	// normalisations. Both patterns are anchored on the helpers rather than on prose, so a
	// rename breaks the test loudly instead of hollowing it out.
	bounded := regexp.MustCompile(`b\.context\(ctx\)|b\.unitWorkContext\(ctx\)`).FindAllString(body, -1)
	normalised := strings.Count(body, "u.budgetFailure(")
	// unitWorkContext sites normalise inline with an explicit b.expired() check instead of
	// through the helper, because they must also distinguish a spent budget BEFORE the work
	// from one that expired during it.
	inline := strings.Count(body, "if b.expired() && ctx.Err() == nil {")

	t.Logf("BUDGET_NORMALISATION|bounded_ops=%d|via_helper=%d|inline=%d", len(bounded), normalised, inline)

	// The floors are the counts at the SHA that closed R9-03, minus nothing. Raising them
	// when a roundtrip is added is the intended maintenance burden; lowering them silently
	// is exactly what this exists to prevent.
	//
	// RAISED FOR C4-06, and the raise is the maintenance burden being paid rather than
	// dodged: BeforeAttempt now takes a budget-derived context and normalises through the
	// helper like every other roundtrip, so both counts go up by one.
	//
	// AND THE LIMIT OF THIS CANARY, said plainly, because C4-06 is the case that proves it:
	// it counts sites that are ALREADY bounded, so a callback invoked with the CALLER's raw
	// context is invisible to it — removing that fix does not lower any count below its
	// floor, because the fix was what put the count there. That gap is covered
	// behaviourally by TestPostgresTheStartOfAnAttemptCannotWaitForever, which fails on a
	// timeout against a real server. A structural canary cannot see an absence it was never
	// counting.
	const (
		wantBoundedAtLeast    = 10
		wantNormalisedAtLeast = 10
		wantInlineAtLeast     = 3
	)
	if len(bounded) < wantBoundedAtLeast {
		t.Errorf("found %d budget-derived contexts, expected at least %d; if a roundtrip was removed, lower the floor deliberately and say why",
			len(bounded), wantBoundedAtLeast)
	}
	if normalised < wantNormalisedAtLeast {
		t.Errorf("found %d budgetFailure call sites, expected at least %d: a roundtrip under a budget-derived context whose expiry is not normalised reports a spent deadline as whatever the canceled operation returned — and for a non-PgError before commit that is retryNewSession, which asks the caller to replace the session holding the cluster-wide coordination lock",
			normalised, wantNormalisedAtLeast)
	}
	if inline < wantInlineAtLeast {
		t.Errorf("found %d inline budget checks after a callback, expected at least %d: Execute, Receipt and the postcondition each need one",
			inline, wantInlineAtLeast)
	}
}
