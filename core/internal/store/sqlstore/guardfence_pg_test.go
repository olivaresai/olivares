// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/store"
)

// guardfence_pg_test.go pins the two fences F-06 asked for, and is explicit about which claim
// each test carries.
//
// The finding was that the lock plan names only RELATIONS, while a unit's authorisation also
// rests on the shared trigger function — an object no LOCK TABLE can reach — and that the
// final bulk projection and the `ready` event lived in different transactions with no lock in
// between. Both are windows in which a receipt or a closing event is FALSE at the instant it
// commits, and both are reversible afterwards, leaving no other trace.
//
// WHAT IS PROVEN HERE, and it is deliberately split into mechanism and wiring:
//
//   - The mechanism: the statements this engine issues really do make a concurrent change
//     wait. Each mechanism test carries its own CONTROL — the same concurrent change without
//     the fence — because a test that only shows a failure proves nothing about which
//     statement caused it.
//   - The wiring: the runner calls the fence inside the attempt's transaction and fails closed
//     when it cannot be taken, and the close refuses when a target cannot be stabilized.
//
// WHAT IS NOT PROVEN, said rather than implied: no test here schedules a concurrent replace
// into the exact microsecond window of a production unit. That race is not reproducible on
// demand, which is precisely why the answer is a lock held to commit rather than a re-read.

// holdSharedFunctionFence opens a transaction on its own connection, takes the fence, and
// returns a release function. The transaction is left open until release is called.
// commitSharedFunctionFence runs the fence and COMMITS it, which is the only shape in which
// "does the fence preserve this field?" is a question at all.
//
// THE PREVIOUS PRESERVATION TEST WAS VACUOUS AND IT WAS MINE. It planted a drift, took the fence
// with holdSharedFunctionFence — which ROLLS BACK — and read the field from another connection
// while that transaction was still open. Under READ COMMITTED that reader sees the pre-fence
// row whatever the fence did, so `ALTER FUNCTION ... RESET ALL` would have passed it too: the
// assertion could not fail. In production the fence commits with the unit's work, so committing
// is what the test has to do.
func commitSharedFunctionFence(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	fn := canonicalGuardDefinition().Function
	if _, err := db.ExecContext(ctx, fenceSharedFunctionStatement(fn.Schema, fn.Name)); err != nil {
		t.Fatalf("take and commit the fence: %v", err)
	}
}

func holdSharedFunctionFence(t *testing.T, db *sql.DB) func() {
	t.Helper()
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("check out a connection: %v", err)
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	fn := canonicalGuardDefinition().Function
	if _, err := tx.ExecContext(ctx, fenceSharedFunctionStatement(fn.Schema, fn.Name)); err != nil {
		t.Fatalf("take the fence: %v", err)
	}
	released := false
	release := func() {
		if released {
			return
		}
		released = true
		_ = tx.Rollback()
		_ = conn.Close()
	}
	t.Cleanup(release)
	return release
}

// replaceSharedFunction attempts the concurrent change the fence exists to stop, with a short
// lock timeout so a blocked attempt fails instead of hanging the test.
func replaceSharedFunction(t *testing.T, db *sql.DB, body string) error {
	t.Helper()
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("check out a connection: %v", err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, "SET lock_timeout = '750ms'"); err != nil {
		t.Fatalf("arm the lock timeout: %v", err)
	}
	fn := canonicalGuardDefinition().Function
	_, err = conn.ExecContext(ctx, `CREATE OR REPLACE FUNCTION `+
		quoteIdent(fn.Schema)+`.`+quoteIdent(fn.Name)+`() RETURNS trigger AS $$
BEGIN
  RAISE EXCEPTION '`+body+`';
END;
$$ LANGUAGE plpgsql`)
	return err
}

// sharedFunctionBody reads the live source of the shared trigger function.
func sharedFunctionBody(t *testing.T, db *sql.DB) string {
	t.Helper()
	fn := canonicalGuardDefinition().Function
	var src string
	if err := db.QueryRowContext(context.Background(), `SELECT p.prosrc
FROM pg_catalog.pg_proc p
JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace
WHERE n.nspname = $1 AND p.proname = $2 AND p.pronargs = 0`, fn.Schema, fn.Name).Scan(&src); err != nil {
		t.Fatalf("read the shared function: %v", err)
	}
	return src
}

// TestPostgresTheSharedFunctionFenceStopsAConcurrentReplace is the MECHANISM, with its control.
//
// The control is the half that makes this a proof rather than an observation: without the
// fence held, the very same replace succeeds at once. So the failure in the fenced case is
// attributable to the fence and to nothing else about the environment.
func TestPostgresTheSharedFunctionFenceStopsAConcurrentReplace(t *testing.T) {
	dsns := isolatedPG(t)
	ctx := context.Background()
	st, err := Open(ctx, store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 4}, registerWidget)
	if err != nil {
		t.Fatalf("open the store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	probe := guardPGProbe(t, dsns.App)

	canonical := sharedFunctionBody(t, probe)
	if !strings.Contains(canonical, "append-only") {
		t.Fatalf("the shared function does not look like the canonical one: %q", canonical)
	}

	// FENCED: the replace must not land.
	release := holdSharedFunctionFence(t, probe)
	start := time.Now()
	err = replaceSharedFunction(t, probe, "REPLACED THROUGH THE FENCE")
	waited := time.Since(start)
	if err == nil {
		t.Fatal("a concurrent CREATE OR REPLACE FUNCTION committed while the fence was held: a unit's receipt can attest a definition that is no longer there")
	}
	if !strings.Contains(err.Error(), "lock timeout") {
		t.Errorf("the replace failed with %v, which is not the lock timeout — so it may have failed for a reason unrelated to the fence", err)
	}
	if waited < 500*time.Millisecond {
		t.Errorf("the replace failed after only %s, which is shorter than the lock timeout it should have spent WAITING", waited)
	}
	if got := sharedFunctionBody(t, probe); got != canonical {
		t.Errorf("the shared function changed despite the fence:\n%s", got)
	}
	t.Logf("GUARD_FUNCTION_FENCE|blocked_for=%s|err=%v", waited, err)

	// AND THE FENCE PRESERVES THE DRIFT IT EXISTS TO EXPOSE. This is the property the first
	// mechanism failed: ALTER FUNCTION ... RESET ALL fenced correctly and ERASED proconfig,
	// which the canonical form makes load-bearing as NULL — so the fence repaired the drift the
	// protected projection was supposed to reject, and left no durable evidence that it had.
	// A fence that launders is worse than no fence, and only a test that plants a drift can
	// tell the two apart.
	release()
	if _, err := probe.ExecContext(ctx, `ALTER FUNCTION `+
		quoteIdent(canonicalGuardDefinition().Function.Schema)+`.`+
		quoteIdent(canonicalGuardDefinition().Function.Name)+`() SET search_path = 'planted'`); err != nil {
		t.Fatalf("plant the drift: %v", err)
	}
	commitSharedFunctionFence(t, probe)
	var config sql.NullString
	if err := probe.QueryRowContext(ctx, `SELECT p.proconfig::text
FROM pg_catalog.pg_proc p
JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace
WHERE n.nspname = $1 AND p.proname = $2 AND p.pronargs = 0`,
		canonicalGuardDefinition().Function.Schema,
		canonicalGuardDefinition().Function.Name).Scan(&config); err != nil {
		t.Fatalf("read proconfig after the fence: %v", err)
	}
	if !config.Valid || !strings.Contains(config.String, "planted") {
		t.Fatalf("taking the fence ERASED the planted proconfig (now %v); the fence repairs the drift the protected projection must reject, and leaves no evidence it did",
			config)
	}
	t.Logf("GUARD_FUNCTION_FENCE_PRESERVES|proconfig=%s", config.String)
	if _, err := probe.ExecContext(ctx, `ALTER FUNCTION `+
		quoteIdent(canonicalGuardDefinition().Function.Schema)+`.`+
		quoteIdent(canonicalGuardDefinition().Function.Name)+`() RESET ALL`); err != nil {
		t.Fatalf("clear the planted drift: %v", err)
	}

	// AND THE SAME PROPERTY FOR EVERY OTHER FIELD THE STATEMENT COULD HAVE NORMALISED.
	//
	// Testing one field was how the second mechanism survived: `CALLED ON NULL INPUT` preserves
	// proconfig perfectly and writes proisstrict, so a test that plants only search_path is
	// green while a concurrent ALTER FUNCTION ... STRICT in the projection->fence window is
	// silently reverted. Round three found that; this table is what would have.
	//
	// Each case plants a drift a non-superuser OWNER can plant, takes the fence, and requires the
	// drift to still be there afterwards.
	//
	// THERE IS NO LONGER AN EXCEPTION, and procost is the case that used to be one. The fence
	// wrote the CANONICAL cost, so a drift arriving in the projection->fence window was erased by
	// the fence itself and the protected projection then read a value the fence had fabricated.
	// Round four called that laundering whatever the field's importance, and it was right. The
	// fence now reads the current value and writes it BACK, which moves xmin exactly the same and
	// rewrites nothing — so this table has one rule and no footnote.
	fnSchema := quoteIdent(canonicalGuardDefinition().Function.Schema)
	fnName := quoteIdent(canonicalGuardDefinition().Function.Name)
	alter := func(t *testing.T, clause string) {
		t.Helper()
		if _, err := probe.ExecContext(ctx, `ALTER FUNCTION `+fnSchema+`.`+fnName+`() `+clause); err != nil {
			t.Fatalf("ALTER FUNCTION ... %s: %v", clause, err)
		}
	}
	readProc := func(t *testing.T, column string) string {
		t.Helper()
		var got sql.NullString
		if err := probe.QueryRowContext(ctx, `SELECT p.`+column+`::text
FROM pg_catalog.pg_proc p
JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace
WHERE n.nspname = $1 AND p.proname = $2 AND p.pronargs = 0`,
			canonicalGuardDefinition().Function.Schema,
			canonicalGuardDefinition().Function.Name).Scan(&got); err != nil {
			t.Fatalf("read %s: %v", column, err)
		}
		return got.String
	}
	for _, tc := range []struct {
		name    string
		column  string
		plant   string
		restore string
		want    string
	}{
		{"a drift to STRICT", "proisstrict", "STRICT", "CALLED ON NULL INPUT", "true"},
		{"a drift to IMMUTABLE", "provolatile", "IMMUTABLE", "VOLATILE", "i"},
		{"a drift to SECURITY DEFINER", "prosecdef", "SECURITY DEFINER", "SECURITY INVOKER", "true"},
		{"a drift to PARALLEL SAFE", "proparallel", "PARALLEL SAFE", "PARALLEL UNSAFE", "s"},
		// THE CASE THAT USED TO BE THE EXCEPTION. It is now the same rule as the rest.
		{"a drift in the cost estimate", "procost", "COST 12345", "COST 100", "12345"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			alter(t, tc.plant)
			t.Cleanup(func() { alter(t, tc.restore) })
			commitSharedFunctionFence(t, probe)
			got := readProc(t, tc.column)
			if got != tc.want {
				t.Errorf("taking the fence ERASED the planted %s (now %q, want %q); the fence repairs the drift the protected projection must reject",
					tc.column, got, tc.want)
			}
			// The log reports what was MEASURED, not what the case hoped for. An earlier
			// revision wrote `preserved=true` unconditionally, so the run that caught the
			// normalising fence emitted `procost=100|preserved=true` on the very line that
			// had just failed — a structured log asserting more than the assertion above it
			// is the same defect as a comment that does, and it reaches a log reader who
			// never sees the failure.
			t.Logf("GUARD_FUNCTION_FENCE_FIELD|%s=%s|preserved=%t", tc.column, got, got == tc.want)
		})
	}

	// CONTROL: with nothing held, the same statement succeeds immediately. Without this the
	// fenced case above would pass on a server where CREATE OR REPLACE FUNCTION failed for any
	// reason at all.
	if err := replaceSharedFunction(t, probe, "REPLACED WITH NO FENCE HELD"); err != nil {
		t.Fatalf("with the fence released the replace still failed, so the fenced case proves nothing: %v", err)
	}
	if got := sharedFunctionBody(t, probe); got == canonical {
		t.Error("the control replace reported success and changed nothing, so it was not exercising the same operation")
	}
}

// TestPostgresTheCloseFenceStabilisesTheTargets is the MECHANISM for the second half.
//
// ROW EXCLUSIVE is the strongest mode an explicit LOCK TABLE can take on an append-only
// relation, and the claim is that it is nevertheless ENOUGH: its conflict set contains the
// modes every trigger-changing statement needs. The control is the same ALTER with no lock
// held.
func TestPostgresTheCloseFenceStabilisesTheTargets(t *testing.T) {
	dsns := isolatedPG(t)
	ctx := context.Background()
	st, err := Open(ctx, store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 4}, registerWidget)
	if err != nil {
		t.Fatalf("open the store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	probe := guardPGProbe(t, dsns.App)

	target := dialect.GuardGateEventsTable
	disable := "ALTER TABLE " + target + " DISABLE TRIGGER " + target + guardTriggerSuffix

	hold, err := probe.Conn(ctx)
	if err != nil {
		t.Fatalf("check out a connection: %v", err)
	}
	defer func() { _ = hold.Close() }()
	tx, err := hold.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	// ENDED UNCONDITIONALLY, and this is not tidiness. database/sql releases a *sql.Conn's
	// driver connection only when its transaction ends, so a Fatalf here would leave a live
	// session on the isolated database — and pgtest's teardown DROP DATABASE then waits on it
	// forever. Measured: the run hung until the test binary was killed.
	t.Cleanup(func() { _ = tx.Rollback() })
	for _, l := range guardCloseLocks([]guardKey{{Schema: guardSchema, Relation: target}}) {
		if _, err := tx.ExecContext(ctx, l.lockStatement()); err != nil {
			t.Fatalf("%s: %v", l.lockStatement(), err)
		}
	}

	attacker, err := probe.Conn(ctx)
	if err != nil {
		t.Fatalf("check out a connection: %v", err)
	}
	defer func() { _ = attacker.Close() }()
	if _, err := attacker.ExecContext(ctx, "SET lock_timeout = '750ms'"); err != nil {
		t.Fatalf("arm the lock timeout: %v", err)
	}
	start := time.Now()
	_, err = attacker.ExecContext(ctx, disable)
	waited := time.Since(start)
	if err == nil {
		t.Fatal("a guard was disabled while the close fence held ROW EXCLUSIVE on its relation: `ready` could be written over a snapshot that no longer describes the database")
	}
	if !strings.Contains(err.Error(), "lock timeout") {
		t.Errorf("the ALTER failed with %v, which is not the lock timeout", err)
	}
	if waited < 500*time.Millisecond {
		t.Errorf("the ALTER failed after only %s, shorter than the timeout it should have spent waiting", waited)
	}
	t.Logf("GUARD_CLOSE_FENCE|blocked_for=%s|err=%v", waited, err)

	// CONTROL.
	_ = tx.Rollback()
	if _, err := attacker.ExecContext(ctx, disable); err != nil {
		t.Fatalf("with the lock released the ALTER still failed, so the fenced case proves nothing: %v", err)
	}
}

// TestPostgresTheCloseRefusesWhenATargetCannotBeStabilised is the WIRING of the close fence.
//
// It calls the closing function directly with a target held at ACCESS EXCLUSIVE from another
// session. The lock acquisition is the FIRST thing the close does, so a refusal naming that
// relation proves the close takes the lock at all — which no assertion about a successful boot
// can, because a successful boot looks identical whether the lock was taken or not.
func TestPostgresTheCloseRefusesWhenATargetCannotBeStabilised(t *testing.T) {
	dsns := isolatedPG(t)
	ctx := context.Background()
	st, err := Open(ctx, store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 6}, registerWidget)
	if err != nil {
		t.Fatalf("open the store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	probe := guardPGProbe(t, dsns.App)

	dia, ok := dialect.New(store.EnginePostgres)
	if !ok {
		t.Fatal("no PostgreSQL dialect")
	}
	m, err := buildGuardManifest(registryWithWidget(t).appendOnlyTables())
	if err != nil {
		t.Fatalf("build the manifest: %v", err)
	}
	empty, err := emptyRetainedDigest()
	if err != nil {
		t.Fatal(err)
	}
	rolloutID, err := guardRolloutID(m.Format, m.CodeEpoch, m.CodeSHA256, 0, empty)
	if err != nil {
		t.Fatal(err)
	}
	rollout := guardRolloutContext{
		RolloutID: rolloutID, Format: m.Format, CodeEpoch: m.CodeEpoch,
		CodeSHA256: m.CodeSHA256, RetainedRevision: 0, RetainedSHA256: empty,
	}
	keys := make([]guardKey, 0, len(m.Specs))
	for _, spec := range m.Specs {
		keys = append(keys, spec.Key)
	}

	// Shortened so the refusal arrives in the test's lifetime rather than the operator's.
	restore := guardCloseLockTimeout
	guardCloseLockTimeout = 400 * time.Millisecond
	t.Cleanup(func() { guardCloseLockTimeout = restore })

	// THE BLOCKED TARGET IS THE **LAST** ONE THE CLOSE WOULD LOCK, not the first.
	//
	// Blocking keys[0] was a mutant this test could not see: truncating guardCloseLocks to its
	// first element leaves the other N-1 targets unfenced and this test still goes red on the
	// one it does lock. Taking the last of the close's own ordered plan is what makes the
	// assertion "every target is fenced" instead of "at least one is".
	closePlan := guardCloseLocks(keys)
	last := closePlan[len(closePlan)-1]
	var blocked guardKey
	for _, k := range keys {
		if k.Schema == last.Schema && k.Relation == last.Name {
			blocked = k
			break
		}
	}
	if blocked.Relation == "" {
		t.Fatalf("the close's lock plan names %s.%s, which is not one of the %d targets", last.Schema, last.Name, len(keys))
	}
	// THE BLOCKER IS THE SUPERUSER, and the reason is a property worth naming rather than a
	// convenience. LOCK TABLE checks privileges PER MODE and ownership grants no exemption, so
	// on an append-only relation — UPDATE, DELETE and TRUNCATE revoked — the application role
	// cannot take ANY mode above ROW EXCLUSIVE. Measured: "permission denied for table
	// audit_events (SQLSTATE 42501)". What is being modeled here is a DDL statement or an
	// operator, both of which act with rights the application role does not have.
	super := guardPGProbe(t, dsns.Superuser)
	hold, err := super.Conn(ctx)
	if err != nil {
		t.Fatalf("check out a connection: %v", err)
	}
	tx, err := hold.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	// Ended unconditionally: a *sql.Conn's driver connection is released only when its
	// transaction ends, and a live session makes pgtest's teardown DROP DATABASE wait forever.
	t.Cleanup(func() { _ = tx.Rollback(); _ = hold.Close() })
	// ACCESS EXCLUSIVE conflicts with everything, so the close cannot take even ROW EXCLUSIVE.
	if _, err := tx.ExecContext(ctx,
		"LOCK TABLE ONLY "+quoteIdent(blocked.Schema)+"."+quoteIdent(blocked.Relation)+" IN ACCESS EXCLUSIVE MODE"); err != nil {
		t.Fatalf("hold the target: %v", err)
	}

	conn, err := probe.Conn(ctx)
	if err != nil {
		t.Fatalf("check out a connection: %v", err)
	}
	defer func() { _ = conn.Close() }()
	summary := guardRolloutSummary{Executed: map[unitIntent]int{}, Outcomes: map[reconcileOutcome]int{}}
	_, cerr := closeGuardRolloutUnderFence(ctx, conn, dia, m, rollout, nil, keys, false, &summary)
	if cerr == nil {
		t.Fatal("the close committed `ready` while one of its targets was held at ACCESS EXCLUSIVE, so it took no lock on it")
	}
	if !strings.Contains(cerr.Error(), "stabilize") || !strings.Contains(cerr.Error(), blocked.Relation) {
		t.Fatalf("the refusal does not name the relation it could not stabilize (%s): %v", blocked.Relation, cerr)
	}
	t.Logf("GUARD_CLOSE_WIRING|blocked_on=%s|err=%v", blocked.Relation, cerr)
}

// TestTheFenceRunsInsideTheAttemptAndFailsClosed is the WIRING of the per-unit hook.
//
// Three claims, each with its own failure mode:
//
//  1. a fence that cannot be taken FAILS the unit — nothing is executed and no receipt is
//     written, which is the only safe answer when the thing a receipt would attest could not
//     be held still;
//  2. the fence runs INSIDE the attempt's transaction, so its effects roll back with a failed
//     attempt; and
//  3. on success it commits with the work.
func TestTheFenceRunsInsideTheAttemptAndFailsClosed(t *testing.T) {
	dsns := isolatedPG(t)
	ctx := context.Background()
	db, err := sql.Open("pgx", dsns.App)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS olv_fence_marks(note text)"); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	marks := func() int {
		var n int
		if err := db.QueryRowContext(ctx, "SELECT count(*) FROM olv_fence_marks").Scan(&n); err != nil {
			t.Fatalf("count marks: %v", err)
		}
		return n
	}
	receipts := func() int {
		var n int
		if err := db.QueryRowContext(ctx, "SELECT count(*) FROM olv_ru_receipts").Scan(&n); err != nil {
			t.Fatalf("count receipts: %v", err)
		}
		return n
	}
	budget := func() *lockBudget {
		return newLockBudget(5*time.Second, time.Now, sleepCtx, jitterFloat)
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("check out a connection: %v", err)
	}
	defer func() { _ = conn.Close() }()

	// 1. A FENCE THAT FAILS FAILS THE UNIT.
	boom := errors.New("the fence could not be taken")
	u := retryUnitFixture(t, ctx, db)
	u.Fence = func(context.Context, *sql.Tx) error { return boom }
	before := receipts()
	if err := u.run(ctx, conn, budget()); err == nil {
		t.Fatal("the unit succeeded although its fence could not be taken")
	} else if !strings.Contains(err.Error(), boom.Error()) {
		t.Errorf("the failure does not carry the fence's own error: %v", err)
	}
	if got := receipts(); got != before {
		t.Errorf("a receipt was written for a unit whose fence failed: %d -> %d", before, got)
	}

	// 2. THE FENCE IS INSIDE THE TRANSACTION: its effect disappears with a failed attempt.
	//
	// The marks table is DECLARED in the plan, and finding that out was itself a result: the
	// first version of this test left it undeclared and the runner refused the unit with
	// "locked a relation outside its declared plan". So the footprint check covers whatever a
	// fence touches too — which is why the production fence had to be a statement that takes
	// nothing outside pg_proc.
	u = retryUnitFixture(t, ctx, db)
	u.Plan.Metadata = withFenceMarks(u.Plan.Metadata)
	u.Fence = func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO olv_fence_marks VALUES ('fenced')")
		return err
	}
	u.Execute = func(context.Context, *sql.Tx, prestate) error {
		return errors.New("permanent: this attempt fails after the fence")
	}
	if err := u.run(ctx, conn, budget()); err == nil {
		t.Fatal("the unit succeeded although Execute failed")
	}
	if got := marks(); got != 0 {
		t.Errorf("the fence's effect survived a failed attempt (%d rows), so it did not share the attempt's transaction", got)
	}

	// 3. ON SUCCESS IT COMMITS WITH THE WORK.
	u = retryUnitFixture(t, ctx, db)
	u.Plan.Metadata = withFenceMarks(u.Plan.Metadata)
	u.Fence = func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO olv_fence_marks VALUES ('fenced')")
		return err
	}
	before = receipts()
	if err := u.run(ctx, conn, budget()); err != nil {
		t.Fatalf("the unit failed with a fence that succeeds: %v", err)
	}
	if got := marks(); got != 1 {
		t.Errorf("the fence ran %d times on the committed attempt, want 1", got)
	}
	if got := receipts(); got != before+1 {
		t.Errorf("receipts %d -> %d, want one more", before, got)
	}
}

// withFenceMarks declares the fixture's marks table in the plan, in the sorted order
// lockPlan.validate requires of the common metadata prefix.
func withFenceMarks(meta []plannedLock) []plannedLock {
	out := append([]plannedLock(nil), meta...)
	out = append(out, plannedLock{Schema: "public", Name: "olv_fence_marks", Mode: lockModeRowExclusive})
	sortPlannedLocks(out)
	return out
}
