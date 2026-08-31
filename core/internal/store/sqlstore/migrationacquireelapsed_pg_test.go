// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// C4-09: THE TWO CALL SITES THAT LIED TO THE JUDGE.
//
// classifyCancel decides who canceled a 57014, and its rule is right: a statement killed
// by OUR OWN timeout necessarily ran for the timeout's duration, so `Elapsed == 0` cannot
// be our own timeout and must fall to unknown. Its contract is pinned by tests and this fix
// does not touch it.
//
// The defect was in the two acquisition-phase call sites, which passed a LITERAL 0 for
// Elapsed while passing a real `armed > 0` — `verifyLockFootprint` and the re-projection of
// the precondition under its lock. So a 57014 raised by the statement_timeout the runner
// itself armed thirty lines earlier was classified `cancelUnknown`, which in phaseAcquire
// maps to `retryNever` and to the operator-facing text "statement canceled from outside
// this runner … not a timeout this unit armed". The rollout was then written to the ledger
// as permanently blocked with UnblockPolicy=operator — and this edition ships no repair CLI,
// so that boot never starts again — while the message sent the operator to hunt a
// pg_cancel_backend that never happened. The same call, in pre-commit, has always measured.
//
// The second half of the fix is in armAcquisition: it armed lock_timeout and
// statement_timeout at the SAME value, which the repository had already measured one layer
// up as making the statement ceiling always win, so a LOCK WAIT arrived as 57014 instead of
// the retryable 55P03. Both halves are needed and neither subsumes the other.
//
// THIS TEST MEASURES THE CLASSIFICATION, not the number: a test that asserted "Elapsed is
// nonzero" would pass against a call site that passed any constant. What must change is
// what the runner DOES with a cancellation it caused itself.
// NOT parallel: it shortens the package-level unitLockTimeout, and a parallel sibling
// running a real acquisition under a 700ms ceiling would fail for a reason neither test is
// about.
func TestPostgresAnAcquisitionTimeoutIsNotBlamedOnAnOutsider(t *testing.T) {
	dsns := isolatedPG(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	db, err := sql.Open("pgx", dsns.App)
	if err != nil {
		t.Fatalf("open app pool: %v", err)
	}
	db.SetMaxOpenConns(4)
	t.Cleanup(func() { _ = db.Close() })

	// THE SERVER'S CEILING HAS TO BE THE SMALLER ONE, or this test measures the wrong clock.
	// b.context() bounds every roundtrip by the WHOLE remaining budget, so the server-side
	// statement_timeout (the per-acquisition ceiling plus its slack) only fires first when
	// that ceiling is well under the budget — which is the production pair (60s under 10min)
	// and was NOT the first shape of this test. Measured: with budget == ceiling the client
	// deadline wins, the failure never reaches classifyCancel, and the test passed with the
	// defect restored.
	prevCeiling := unitLockTimeout
	unitLockTimeout = 700 * time.Millisecond
	t.Cleanup(func() { unitLockTimeout = prevCeiling })

	unit := retryUnitFixture(t, ctx, db)

	// The re-projection UNDER THE LOCK is the one that must overrun. It is told apart from
	// the pre-lock projection by the type of the querier it is handed: the pre-lock one runs
	// on the pooled connection, the under-lock one on the acquisition transaction. That is a
	// property of the runner's own shape, not of this fixture's bookkeeping, so it stays true
	// across retries without a counter that could drift.
	base := unit.Project
	unit.Project = func(pctx context.Context, dbx rowQuerier) (prestate, error) {
		if _, underLock := dbx.(*sql.Tx); underLock {
			// Longer than any ceiling the budget below can arm, so the statement_timeout the
			// runner armed is what ends it — a 57014 this runner caused.
			if _, err := dbx.QueryContext(pctx, `SELECT pg_sleep(30)`); err != nil {
				return prestate{}, err
			}
		}
		return base(pctx, dbx)
	}

	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("unit conn: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	// Comfortably LARGER than the per-acquisition ceiling above, so the statement_timeout the
	// runner arms is what ends the sleep, and the retries it authorizes are what eventually
	// spend the budget.
	const budget = 8 * time.Second
	b := newLockBudget(budget, time.Now, sleepCtx, func() float64 { return 1 })

	done := make(chan error, 1)
	go func() { done <- unit.run(ctx, conn, b) }()

	var runErr error
	select {
	case runErr = <-done:
	case <-time.After(120 * time.Second):
		t.Fatal("the unit never returned; the fixture is not reaching the under-lock re-projection")
	}

	if runErr == nil {
		t.Fatal("the unit reported success although its own timeout killed the re-projection")
	}
	// THE ASSERTION. A cancellation this runner armed must never be attributed to a third
	// party, because that attribution is what makes the failure PERMANENT and unrepairable.
	if strings.Contains(runErr.Error(), "from outside this runner") {
		t.Fatalf("a 57014 raised by the statement_timeout THIS unit armed was blamed on an external "+
			"cancellation, which classifies retryNever and writes the rollout blocked with "+
			"UnblockPolicy=operator — permanently, since this edition ships no repair CLI. Error: %v", runErr)
	}
	// And it must be reported as what it is: a deadline that ran out.
	if !errors.Is(runErr, ErrMigrationLockBudgetExceeded) {
		t.Fatalf("a self-inflicted acquisition timeout must surface as a spent budget once the retries "+
			"are exhausted, got %v", runErr)
	}
}

// TestPostgresArmAcquisitionDoesNotArmTheTwoCeilingsEqual pins the second half by ASKING
// THE SERVER what the runner armed.
//
// THE EARLIER SHAPE OF THIS TEST DID NOT CALL PRODUCTION. It asserted that the constant is
// positive and that `d + constant > d`, which is arithmetic — the contrast round restored
// `setLocalTimeouts(actx, tx, d, d)` in armAcquisition and this test stayed green, because
// nothing in it ever reached the function whose behavior it claimed to pin. A test that
// re-derives the intended value instead of observing the produced one cannot fail when the
// producer changes.
//
// So it now runs armAcquisition against a real server and reads back the two GUCs it set.
func TestPostgresArmAcquisitionDoesNotArmTheTwoCeilingsEqual(t *testing.T) {
	t.Parallel()
	dsns := isolatedPG(t)
	ctx := context.Background()

	db, err := sql.Open("pgx", dsns.App)
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	db.SetMaxOpenConns(2)
	t.Cleanup(func() { _ = db.Close() })

	u := retryUnitFixture(t, ctx, db)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback() }()

	b := newLockBudget(5*time.Minute, time.Now, sleepCtx, func() float64 { return 1 })
	armed, err := u.armAcquisition(ctx, tx, b)
	if err != nil {
		t.Fatalf("armAcquisition: %v", err)
	}
	if armed <= 0 {
		t.Fatalf("armAcquisition reported a non-positive ceiling %v", armed)
	}

	// SET LOCAL, so both are read inside the same transaction that armed them.
	var lockText, stmtText string
	if err := tx.QueryRowContext(ctx, "SHOW lock_timeout").Scan(&lockText); err != nil {
		t.Fatalf("read lock_timeout: %v", err)
	}
	if err := tx.QueryRowContext(ctx, "SHOW statement_timeout").Scan(&stmtText); err != nil {
		t.Fatalf("read statement_timeout: %v", err)
	}
	lockMS, stmtMS := pgIntervalMillis(t, lockText), pgIntervalMillis(t, stmtText)
	t.Logf("ARMED_CEILINGS|armed=%v|lock_timeout=%s(%dms)|statement_timeout=%s(%dms)",
		armed, lockText, lockMS, stmtText, stmtMS)

	if lockMS <= 0 || stmtMS <= 0 {
		t.Fatalf("PostgreSQL reads 0 as UNLIMITED: lock_timeout=%dms statement_timeout=%dms", lockMS, stmtMS)
	}
	// THE PROPERTY. statement_timeout runs from the start of the statement and lock_timeout
	// from the moment it begins WAITING, microseconds later. Armed equal, the statement
	// ceiling always fires first and a lock wait arrives as a terminal 57014 instead of the
	// retryable 55P03 — the defect this repository already measured one layer up.
	if stmtMS <= lockMS {
		t.Fatalf("the two ceilings were armed with statement_timeout (%dms) NOT above lock_timeout "+
			"(%dms). A lock wait then ends in 57014 rather than 55P03, which the retry classifier "+
			"treats as permanent — the rollout blocks with no repair path in this edition", stmtMS, lockMS)
	}
}

// pgIntervalMillis parses what SHOW returns for a timeout GUC ("2s", "250ms", "1min", "0").
func pgIntervalMillis(t *testing.T, s string) int64 {
	t.Helper()
	s = strings.TrimSpace(s)
	if s == "0" {
		return 0
	}
	for _, u := range []struct {
		suffix string
		mul    int64
	}{{"ms", 1}, {"min", 60000}, {"s", 1000}, {"h", 3600000}, {"d", 86400000}} {
		if strings.HasSuffix(s, u.suffix) {
			n, err := strconv.ParseInt(strings.TrimSuffix(s, u.suffix), 10, 64)
			if err != nil {
				t.Fatalf("parse %q: %v", s, err)
			}
			return n * u.mul
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return n
}

// TestNeitherAcquisitionFailureReportsALiteralZeroElapsed is the structural half, and it is
// labeled as one rather than dressed up.
//
// The behavioral test above can only make ONE of the two sites overrun — the re-projection,
// which goes through the injectable Project callback. verifyLockFootprint reads pg_locks
// through no seam at all, so restoring its literal 0 alone leaves that test green (measured
// by the contrast round). What this asserts instead is the shape both call sites must have:
// an acquisition-phase failure never reports Elapsed as a literal 0 while claiming a
// positive Armed, because classifyCancel reads exactly that pair as "not my own timeout".
func TestNeitherAcquisitionFailureReportsALiteralZeroElapsed(t *testing.T) {
	t.Parallel()

	src, err := os.ReadFile("migrationretry.go")
	if err != nil {
		t.Fatalf("read migrationretry.go: %v", err)
	}
	// THE ANCHOR WAS TOO NARROW AND IS NOW THE WHOLE PHASE, and the reason is a correction to
	// this session's own reasoning rather than a change of taste.
	//
	// An earlier version restricted the rule to failures wrapping a round trip through
	// budgetFailure, on the argument that the other five literal zeros could not carry a
	// cancellation: three return an error from armAcquisition, which "normalises before it
	// gets here", and two are local Go logic. THE SECOND HALF IS RIGHT AND THE FIRST IS
	// WRONG. `SET LOCAL` lives until the end of the transaction — this runner depends on that
	// and says so — so from the second metadata lock onward there is ALREADY a
	// statement_timeout armed by this unit. armAcquisition's own two set_config round trips
	// run under it, and when the global budget still has room budgetFailure returns the
	// PgError untouched. So a 57014 raised by THIS runner's previously-armed ceiling reaches
	// classifyCancel with Elapsed=0 through those three sites too, and lands on retryNever.
	//
	// The two that stay legitimate are prestate.validate and expectedEnableState: local Go
	// over an already-fetched projection, which cannot raise a SQLSTATE at all. They are
	// excluded by NAME rather than by shape, so a new zero cannot hide behind their exemption.
	// The window is 240 bytes, not the rest of the LINE: both exempt sites put their
	// identifying error on the FOLLOWING line, so a line-anchored match sees only
	// `fail(phaseAcquire, armed, 0,` and cannot tell them apart. A grep that stops at a
	// newline cannot read a statement that does not.
	body := string(src)
	var bad []string
	for _, loc := range regexp.MustCompile(`fail\(phaseAcquire,\s*(armed|armedOrInForce\([^)]*\)),\s*0\s*,`).FindAllStringIndex(body, -1) {
		end := loc[1] + 240
		if end > len(body) {
			end = len(body)
		}
		window := body[loc[0]:end]
		if strings.Contains(window, "ErrMigrationUnauthorised") || strings.Contains(window, "re-projected for") {
			continue
		}
		bad = append(bad, strings.SplitN(window, "\n", 2)[0])
	}
	if len(bad) > 0 {
		t.Fatalf("%d acquisition-phase failure(s) still report a literal Elapsed=0 with a positive "+
			"Armed. classifyCancel reads that pair as cancelUnknown, which in phaseAcquire maps to "+
			"retryNever and writes the rollout permanently blocked with UnblockPolicy=operator — over "+
			"a 57014 this runner's own statement_timeout caused: %v", len(bad), bad)
	}
	// AND THE THREE RE-ARM SITES MUST USE armedOrInForce, not bare `armed`.
	//
	// The unit test above protects the helper's body; nothing protected its WIRING. Measured by
	// the contrast: reverting all three callers to `armed` compiles and leaves the whole package
	// green, because setLocalTimeouts' zero only appears at runtime. `armStarted` is the shape
	// that marks a re-arm failure — it exists only there — so a re-arm reporting bare `armed` is
	// the exact regression.
	rearmBare := regexp.MustCompile(`fail\(phaseAcquire,\s*armed,\s*b\.now\(\)\.Sub\(armStarted\)`).FindAllString(body, -1)
	if len(rearmBare) > 0 {
		t.Fatalf("%d failed re-arm(s) report bare `armed`, which setLocalTimeouts sets to ZERO on "+
			"failure. classifyCancel needs Armed > 0, so a 57014 raised by the ceiling ALREADY IN "+
			"FORCE goes to cancelUnknown and then to retryNever — a permanent block over this "+
			"runner's own timeout. They must report armedOrInForce(armed, inForce)", len(rearmBare))
	}
	if n := len(regexp.MustCompile(`armedOrInForce\(armed, inForce\)`).FindAllString(body, -1)); n < 3 {
		t.Fatalf("expected the three re-arm failures to carry the ceiling in force, found %d", n)
	}

	// Not vacuous: the five sites that can carry a cancellation DO pass a measured duration.
	// Both shapes count: the two that report `armed` directly and the three that report
	// armedOrInForce(armed, inForce) — a failed re-arm has to name the ceiling still in force,
	// not the zero setLocalTimeouts returns.
	measured := regexp.MustCompile(`fail\(phaseAcquire,\s*(armed|armedOrInForce\([^)]*\)),\s*b\.now\(\)\.Sub\(`).FindAllString(string(src), -1)
	if len(measured) < 5 {
		t.Fatalf("expected the five acquisition-phase failures that can carry a SQLSTATE to pass a "+
			"measured elapsed, found %d. If a site was removed, lower this deliberately and say why",
			len(measured))
	}
}

// R3 MEDIA-1: MEASURING Elapsed WAS NECESSARY AND NOT SUFFICIENT.
//
// The three acquisition sites now report a real Elapsed, and a 57014 raised by the ceiling
// ALREADY IN FORCE was still blamed on an outsider — because armAcquisition reports what
// setLocalTimeouts returns, and that is ZERO when either of its round trips fails.
// classifyCancel needs Armed > 0 AND Elapsed > 0, so Armed=0 sent it to cancelUnknown and
// then to retryNever: a permanent block over a timeout this runner set. Measured by the
// contrast with a probe over the production constructors:
//
//	unitFailure{Phase: acquire, Armed: 0, Elapsed: 1m, Err: PgError(57014)} -> retryNever
//
// This pins the rule that closes it: after the first successful arm there IS a ceiling in
// force for the rest of the transaction, and that is the number a failed re-arm must report.
// Before the first arm there is none, and zero is then correct.
func TestArmedOrInForceReportsTheCeilingThatWouldHaveFired(t *testing.T) {
	t.Parallel()

	if got := armedOrInForce(0, 60*time.Second); got != 60*time.Second {
		t.Fatalf("a failed re-arm must report the ceiling still in force, got %v. Reporting zero is "+
			"what makes classifyCancel call this runner's own 57014 an external cancellation, which "+
			"maps to retryNever and blocks the rollout permanently", got)
	}
	if got := armedOrInForce(30*time.Second, 60*time.Second); got != 30*time.Second {
		t.Fatalf("a SUCCESSFUL arm must report its own value, not the previous one, got %v", got)
	}
	// Nothing armed yet: zero is the honest answer and must survive.
	if got := armedOrInForce(0, 0); got != 0 {
		t.Fatalf("before the first arm there is no ceiling; reporting one would claim a timeout this "+
			"transaction never set, got %v", got)
	}
}
