// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/store"
)

// TestLockFootprintRefusesWhatThePlanDidNotDeclare is what turns the lock plan from a
// promise into a measurement.
//
// lockPlan declares relations and modes, but the statements are opaque SQL. A unit
// can take locks nobody wrote down — through a foreign key, a partition, a trigger's
// own table, an index rebuild — and the declaration still reads as correct. The only
// way to know what a transaction actually locked is to ask the catalog while it still
// holds them, which is before the commit: relation locks are released at commit, so
// afterwards there is nothing left to read.
//
// Mutation that must turn this red: return nil from verifyLockFootprint.
func TestLockFootprintRefusesWhatThePlanDidNotDeclare(t *testing.T) {
	dsns := isolatedPG(t)
	ctx := context.Background()

	db, err := openPostgres(store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 3})
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	defer db.Close() //nolint:errcheck // test teardown
	for _, ddl := range []string{
		`CREATE TABLE public.olv_fp_declared (id bigint PRIMARY KEY)`,
		`CREATE TABLE public.olv_fp_undeclared (id bigint PRIMARY KEY)`,
	} {
		if _, err := db.ExecContext(ctx, ddl); err != nil {
			t.Fatalf("setup %q: %v", ddl, err)
		}
	}

	plan := lockPlan{
		Target:          plannedLock{Schema: "public", Name: "olv_fp_declared", Mode: lockModeRowExclusive},
		TargetStatement: `LOCK TABLE ONLY "public"."olv_fp_declared" IN ROW EXCLUSIVE MODE`,
	}
	if err := plan.validate(); err != nil {
		t.Fatalf("the fixture plan is itself invalid: %v", err)
	}

	t.Run("a declared footprint passes", func(t *testing.T) {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer tx.Rollback() //nolint:errcheck // test teardown
		if _, err := tx.ExecContext(ctx, plan.TargetStatement); err != nil {
			t.Fatalf("take the declared lock: %v", err)
		}
		if err := verifyLockFootprint(ctx, tx, plan); err != nil {
			t.Errorf("a transaction that locked exactly what it declared was refused: %v", err)
		}
	})

	t.Run("an undeclared relation is refused", func(t *testing.T) {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer tx.Rollback() //nolint:errcheck // test teardown
		if _, err := tx.ExecContext(ctx, plan.TargetStatement); err != nil {
			t.Fatalf("take the declared lock: %v", err)
		}
		// This is the shape the check exists for: work that looks unrelated to the
		// plan and takes a lock the plan never mentioned.
		if _, err := tx.ExecContext(ctx, `SELECT count(*) FROM public.olv_fp_undeclared`); err != nil {
			t.Fatalf("touch the undeclared relation: %v", err)
		}
		err = verifyLockFootprint(ctx, tx, plan)
		if !errors.Is(err, ErrMigrationLockFootprint) {
			t.Fatalf("verify = %v, want ErrMigrationLockFootprint: the unit locked a relation outside its plan and was allowed to commit", err)
		}
		if !strings.Contains(err.Error(), "olv_fp_undeclared") {
			t.Errorf("the refusal does not name the relation it refused over: %v", err)
		}
	})

	t.Run("a stronger mode than declared is refused", func(t *testing.T) {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer tx.Rollback() //nolint:errcheck // test teardown
		// The plan authorizes ROW EXCLUSIVE; take ACCESS EXCLUSIVE instead. This is
		// what an unnoticed DROP TRIGGER inside Execute does.
		if _, err := tx.ExecContext(ctx,
			`LOCK TABLE ONLY "public"."olv_fp_declared" IN ACCESS EXCLUSIVE MODE`); err != nil {
			t.Fatalf("escalate: %v", err)
		}
		err = verifyLockFootprint(ctx, tx, plan)
		if !errors.Is(err, ErrMigrationLockFootprint) {
			t.Fatalf("verify = %v, want a refusal: escalating past the declared mode is exactly the deadlock recipe the plan orders locks to avoid", err)
		}
		if !strings.Contains(err.Error(), "ACCESS EXCLUSIVE") {
			t.Errorf("the refusal does not name the mode actually taken: %v", err)
		}
	})

	t.Run("catalog locks are not counted", func(t *testing.T) {
		// Every statement locks pg_class and friends while planning. Flagging those
		// would make the check noise nobody could act on, and a check nobody can act
		// on is a check that gets turned off.
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer tx.Rollback() //nolint:errcheck // test teardown
		if _, err := tx.ExecContext(ctx, plan.TargetStatement); err != nil {
			t.Fatalf("take the declared lock: %v", err)
		}
		if _, err := tx.ExecContext(ctx,
			`SELECT count(*) FROM pg_catalog.pg_class`); err != nil {
			t.Fatalf("read the catalog: %v", err)
		}
		if err := verifyLockFootprint(ctx, tx, plan); err != nil {
			t.Errorf("reading the system catalogs was counted as an undeclared footprint: %v", err)
		}
	})
}

// TestLockFootprintFailsClosedWhenItCannotLook pins that an unverifiable footprint is
// not an empty one.
//
// The check runs at the end of a transaction that may already be in trouble. If a
// failed read were treated as "nothing undeclared", the guarantee would evaporate at
// precisely the moment the database is behaving oddly enough to be worth checking.
func TestLockFootprintFailsClosedWhenItCannotLook(t *testing.T) {
	dsns := isolatedPG(t)
	ctx := context.Background()

	db, err := openPostgres(store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 2})
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	defer db.Close() //nolint:errcheck // test teardown

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck // test teardown

	// Poison the transaction so the footprint query cannot run: after a failed
	// statement PostgreSQL refuses everything until rollback (25P02). That is a real
	// state this check can meet, not a contrived one.
	if _, err := tx.ExecContext(ctx, `SELECT 1/0`); err == nil {
		t.Fatal("the poisoning statement unexpectedly succeeded")
	}

	err = verifyLockFootprint(ctx, tx, lockPlan{
		Target:          plannedLock{Schema: "public", Name: "whatever", Mode: lockModeRowExclusive},
		TargetStatement: `LOCK TABLE ONLY "public"."whatever" IN ROW EXCLUSIVE MODE`,
	})
	if err == nil {
		t.Fatal("an unverifiable footprint was reported as a clean one; the check would pass exactly when the database is least trustworthy")
	}
	if !strings.Contains(err.Error(), "could not verify") {
		t.Errorf("the failure does not say that verification itself failed, so it reads as a footprint violation: %v", err)
	}
}

// TestLockFootprintMatchesARelationThatNeedsQuoting is the regression for a defect
// found by reading my own code rather than by running it.
//
// plannedLock originally held ONE string, documented as "already quoted for
// interpolation", and the footprint check compared that same string against
// pg_class.relname — which the catalog stores UNQUOTED. For every ordinary
// lower-case identifier the two forms are identical and nothing shows. For a legal
// identifier that requires quoting they are not, and the consequences are opposite
// and both bad: unquoted, the LOCK TABLE fails to parse; quoted, the comparison never
// matches, so the check reports the unit's own declared relation as UNDECLARED and
// refuses a unit that did exactly what it said it would.
//
// This is not a hypothetical class of name. The repository already carries a
// regression for a role whose name contains a dollar-quote tag, because a legal
// identifier that needed quoting stopped an ordinary deployment from booting.
//
// Mutation that must turn this red: make relation() return the quoted form, or
// lockStatement() the unquoted one.
func TestLockFootprintMatchesARelationThatNeedsQuoting(t *testing.T) {
	dsns := isolatedPG(t)
	ctx := context.Background()

	db, err := openPostgres(store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 2})
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	defer db.Close() //nolint:errcheck // test teardown

	// Mixed case AND a space: PostgreSQL folds unquoted identifiers to lower case, so
	// this name can only ever be referred to quoted, while the catalog stores it raw.
	const schema, name = "public", `Olv Footprint "Odd"`
	if _, err := db.ExecContext(ctx,
		`CREATE TABLE `+quoteIdent(schema)+`.`+quoteIdent(name)+` (id bigint)`); err != nil {
		t.Fatalf("create a table whose name requires quoting: %v", err)
	}

	target := plannedLock{Schema: schema, Name: name, Mode: lockModeRowExclusive}
	plan := lockPlan{Target: target, TargetStatement: target.lockStatement()}
	if err := plan.validate(); err != nil {
		t.Fatalf("the plan is invalid: %v", err)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck // test teardown

	// If the two forms were one string, this is where an unquoted name fails to parse.
	if _, err := tx.ExecContext(ctx, plan.TargetStatement); err != nil {
		t.Fatalf("the generated LOCK TABLE does not parse for a name that requires quoting: %v", err)
	}
	// And this is where a quoted name fails to match the catalog.
	if err := verifyLockFootprint(ctx, tx, plan); err != nil {
		t.Errorf("the footprint check refused the unit's OWN declared relation: %v", err)
	}
}

// TestLockFootprintDoesNotRejectOrdinaryIndexAndSequenceLocks is the false-POSITIVE
// regression, and it matters as much as the false negatives.
//
// An ordinary INSERT takes RowExclusiveLock on the table AND on every one of its
// indexes, plus on the sequence behind an identity column. Counting those as
// undeclared relations rejects a unit that did exactly what it declared — and a check
// that blocks correct work is a check somebody switches off, which costs every
// guarantee it was providing.
//
// Mutation that must turn this red: drop the index/sequence attribution and count
// their locks as relations in their own right.
func TestLockFootprintDoesNotRejectOrdinaryIndexAndSequenceLocks(t *testing.T) {
	dsns := isolatedPG(t)
	ctx := context.Background()

	db, err := openPostgres(store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 2})
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	defer db.Close() //nolint:errcheck // test teardown

	// A table shaped like the engine's own: an identity column (so a sequence), a
	// primary key and a secondary index. All three are locked by a plain INSERT.
	if _, err := db.ExecContext(ctx, `
CREATE TABLE public.olv_fp_idx (
    id   bigint GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY,
    note text
)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	if _, err := db.ExecContext(ctx, `CREATE INDEX olv_fp_idx_note ON public.olv_fp_idx (note)`); err != nil {
		t.Fatalf("create index: %v", err)
	}

	target := plannedLock{Schema: "public", Name: "olv_fp_idx", Mode: lockModeRowExclusive}
	plan := lockPlan{Target: target, TargetStatement: target.lockStatement()}
	if err := plan.validate(); err != nil {
		t.Fatalf("plan invalid: %v", err)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck // test teardown
	if _, err := tx.ExecContext(ctx, plan.TargetStatement); err != nil {
		t.Fatalf("take the declared lock: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO public.olv_fp_idx (note) VALUES ('x')`); err != nil {
		t.Fatalf("insert: %v", err)
	}

	// Establish the premise: the transaction really is holding more than one relation
	// lock, or this test proves nothing.
	held, err := heldRelationLocks(ctx, tx)
	if err != nil {
		t.Fatalf("read held locks: %v", err)
	}
	if len(held) < 2 {
		t.Fatalf("only %d relation lock(s) held, so the index/sequence attribution is not being exercised: %v", len(held), held)
	}

	if err := verifyLockFootprint(ctx, tx, plan); err != nil {
		t.Errorf("an ordinary INSERT on the DECLARED table was rejected: %v", err)
	}
}

// TestLockFootprintStillSeesAnEscalationOnAnIndex pins the other half of that
// decision: index locks are ATTRIBUTED to their table, not discarded.
//
// REINDEX takes ACCESS EXCLUSIVE on the index while holding only SHARE on the table,
// so simply excluding index locks would have hidden it — trading a false positive for
// a false negative on the exact operation most worth catching.
func TestLockFootprintStillSeesAnEscalationOnAnIndex(t *testing.T) {
	dsns := isolatedPG(t)
	ctx := context.Background()

	db, err := openPostgres(store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 2})
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	defer db.Close() //nolint:errcheck // test teardown
	if _, err := db.ExecContext(ctx, `CREATE TABLE public.olv_fp_reidx (id bigint PRIMARY KEY)`); err != nil {
		t.Fatalf("create table: %v", err)
	}

	// The declared mode is SHARE ROW EXCLUSIVE, and that choice is the whole test.
	//
	// Measured on 15.18, `REINDEX INDEX` takes three locks:
	//
	//   public.olv_probe_reidx_pkey  kind=i  AccessExclusiveLock
	//   public.olv_probe_reidx       kind=r  RowExclusiveLock
	//   public.olv_probe_reidx       kind=r  ShareLock
	//
	// Declaring ROW EXCLUSIVE — the obvious choice, and the one this test used first —
	// makes the TABLE's ShareLock uncovered, so the refusal fires on the table and the
	// index is never consulted. The test then passes with index attribution removed
	// entirely, which is precisely the mutation it exists to catch.
	//
	// SHARE ROW EXCLUSIVE covers both of the table's locks and covers neither
	// AccessExclusive, so the only remaining violation is the one on the INDEX.
	target := plannedLock{Schema: "public", Name: "olv_fp_reidx", Mode: lockModeShareRowExclusive}
	plan := lockPlan{Target: target, TargetStatement: target.lockStatement()}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck // test teardown
	if _, err := tx.ExecContext(ctx, plan.TargetStatement); err != nil {
		t.Fatalf("take the declared lock: %v", err)
	}

	// Premise: with only the declared lock held, the footprint must be clean. If it
	// is not, the refusal below would be about the table and this test would be
	// asserting nothing about indexes.
	if err := verifyLockFootprint(ctx, tx, plan); err != nil {
		t.Fatalf("the declared SHARE ROW EXCLUSIVE lock alone was already refused, so the assertion below cannot be about the index: %v", err)
	}

	if _, err := tx.ExecContext(ctx, `REINDEX INDEX public.olv_fp_reidx_pkey`); err != nil {
		t.Fatalf("reindex: %v", err)
	}
	// And the table's own locks must STILL be covered, so nothing but the index can
	// account for a refusal.
	for _, m := range []lockMode{lockModeRowExclusive, lockModeShare} {
		if !target.Mode.covers(m) {
			t.Fatalf("%s does not cover %s, so REINDEX's table locks are uncovered and this test is again about the table", target.Mode, m)
		}
	}

	err = verifyLockFootprint(ctx, tx, plan)
	if !errors.Is(err, ErrMigrationLockFootprint) {
		t.Fatalf("verify = %v, want a refusal: REINDEX took ACCESS EXCLUSIVE on an INDEX of a table whose declared mode covers everything the table itself was locked at, and excluding index locks instead of attributing them would hide exactly that", err)
	}
}

// TestLockFootprintRequiresEveryDeclaredLockToBeTaken pins the direction that was
// never checked.
//
// Verifying only observed-is-subset-of-declared lets a plan declare ACCESS EXCLUSIVE,
// run `SELECT 1`, and pass — the declaration then documents a lock nobody took, while
// the real work happened under whatever the statement happened to need. A plan is a
// claim about what WILL be held, and half of it went unverified.
//
// Mutation that must turn this red: stop iterating the plan in reverse.
func TestLockFootprintRequiresEveryDeclaredLockToBeTaken(t *testing.T) {
	dsns := isolatedPG(t)
	ctx := context.Background()

	db, err := openPostgres(store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 2})
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	defer db.Close() //nolint:errcheck // test teardown
	for _, ddl := range []string{
		`CREATE TABLE public.olv_fp_decl_a (id bigint)`,
		`CREATE TABLE public.olv_fp_decl_b (id bigint)`,
	} {
		if _, err := db.ExecContext(ctx, ddl); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}

	plan := lockPlan{
		Metadata:        []plannedLock{{Schema: "public", Name: "olv_fp_decl_a", Mode: lockModeRowExclusive}},
		Target:          plannedLock{Schema: "public", Name: "olv_fp_decl_b", Mode: lockModeRowExclusive},
		TargetStatement: `LOCK TABLE ONLY "public"."olv_fp_decl_b" IN ROW EXCLUSIVE MODE`,
	}
	if err := plan.validate(); err != nil {
		t.Fatalf("plan invalid: %v", err)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck // test teardown
	// Take the target only. The metadata lock is declared and never acquired.
	if _, err := tx.ExecContext(ctx, plan.TargetStatement); err != nil {
		t.Fatalf("take the target lock: %v", err)
	}

	err = verifyLockFootprint(ctx, tx, plan)
	if !errors.Is(err, ErrMigrationLockFootprint) {
		t.Fatalf("verify = %v, want a refusal: a declared lock was never taken, so the plan documents protection the unit did not actually have", err)
	}
	if !strings.Contains(err.Error(), "never took declared locks") {
		t.Errorf("the refusal does not name the direction it failed in: %v", err)
	}
}

// TestLockFootprintSeesALockOnARelationTheUnitDropped pins the LEFT JOIN.
//
// pg_locks.relation is an OID. A transaction that locks a relation and then DROPs it
// still holds that lock at commit, while its pg_class row is already gone — so an
// INNER JOIN silently discarded the single most suspicious footprint there is: an
// undeclared lock on something the unit then destroyed.
//
// Mutation that must turn this red: make the pg_class join INNER again.
func TestLockFootprintSeesALockOnARelationTheUnitDropped(t *testing.T) {
	dsns := isolatedPG(t)
	ctx := context.Background()

	db, err := openPostgres(store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 2})
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	defer db.Close() //nolint:errcheck // test teardown
	for _, ddl := range []string{
		`CREATE TABLE public.olv_fp_keep (id bigint)`,
		`CREATE TABLE public.olv_fp_doomed (id bigint)`,
	} {
		if _, err := db.ExecContext(ctx, ddl); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}

	target := plannedLock{Schema: "public", Name: "olv_fp_keep", Mode: lockModeRowExclusive}
	plan := lockPlan{Target: target, TargetStatement: target.lockStatement()}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck // test teardown
	if _, err := tx.ExecContext(ctx, plan.TargetStatement); err != nil {
		t.Fatalf("take the declared lock: %v", err)
	}
	// Lock an undeclared relation and then destroy it inside the same transaction.
	if _, err := tx.ExecContext(ctx, `DROP TABLE public.olv_fp_doomed`); err != nil {
		t.Fatalf("drop: %v", err)
	}

	err = verifyLockFootprint(ctx, tx, plan)
	if !errors.Is(err, ErrMigrationLockFootprint) {
		t.Fatalf("verify = %v, want a refusal: the unit took an ACCESS EXCLUSIVE lock on an undeclared relation and dropped it, and the catalog row being gone must not make the lock invisible", err)
	}
}

// TestLockFootprintRefusesAWeakerModeThanDeclared closes the half of "both
// directions" that was only checking presence.
//
// The reverse pass kept a boolean per relation, so a plan declaring ACCESS EXCLUSIVE
// while the transaction only ever took ACCESS SHARE satisfied everything: the observed
// mode is covered by the declared one, and the relation was seen. The plan asserted
// total exclusion and nothing checked that it was obtained — which is a plan
// documenting protection the unit never had.
//
// Mutation that must turn this red: go back to a presence-only map.
func TestLockFootprintRefusesAWeakerModeThanDeclared(t *testing.T) {
	dsns := isolatedPG(t)
	ctx := context.Background()

	db, err := openPostgres(store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 2})
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	defer db.Close() //nolint:errcheck // test teardown
	if _, err := db.ExecContext(ctx, `CREATE TABLE public.olv_fp_weak (id bigint)`); err != nil {
		t.Fatalf("create table: %v", err)
	}

	plan := lockPlan{
		Target:          plannedLock{Schema: "public", Name: "olv_fp_weak", Mode: lockModeAccessExclusive},
		TargetStatement: `LOCK TABLE ONLY "public"."olv_fp_weak" IN ACCESS EXCLUSIVE MODE`,
	}
	if err := plan.validate(); err != nil {
		t.Fatalf("plan invalid: %v", err)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck // test teardown
	// Take a READ instead of the declared exclusive lock.
	if _, err := tx.ExecContext(ctx, `SELECT count(*) FROM public.olv_fp_weak`); err != nil {
		t.Fatalf("read: %v", err)
	}

	err = verifyLockFootprint(ctx, tx, plan)
	if !errors.Is(err, ErrMigrationLockFootprint) {
		t.Fatalf("verify = %v, want a refusal: the plan declared ACCESS EXCLUSIVE and the transaction only took a read, so the protection it claims to have run under was never obtained", err)
	}
	if !strings.Contains(err.Error(), "weaker locks than declared") {
		t.Errorf("the refusal does not name this direction: %v", err)
	}
}

// TestLockFootprintDoesNotCollapseAPartitionOntoItsParent pins the restriction that
// makes the sequence attribution correct.
//
// The dependency branch followed any pg_class -> pg_class dependency of type 'a' or
// 'i', and a LEAF PARTITION has exactly that dependency on its parent. So a lock on an
// undeclared partition was attributed to the parent and vanished — and the same
// collapse reported "never took declared locks" for a leaf that was declared and
// genuinely acquired. A false negative and a false positive from one line.
//
// Mutation that must turn this red: drop the relkind='S' restriction from the lateral.
func TestLockFootprintDoesNotCollapseAPartitionOntoItsParent(t *testing.T) {
	dsns := isolatedPG(t)
	ctx := context.Background()

	db, err := openPostgres(store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 2})
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	defer db.Close() //nolint:errcheck // test teardown
	for _, ddl := range []string{
		`CREATE TABLE public.olv_fp_parent (id bigint) PARTITION BY RANGE (id)`,
		`CREATE TABLE public.olv_fp_leaf PARTITION OF public.olv_fp_parent FOR VALUES FROM (0) TO (100)`,
	} {
		if _, err := db.ExecContext(ctx, ddl); err != nil {
			t.Fatalf("setup %q: %v", ddl, err)
		}
	}

	t.Run("a lock on an undeclared leaf is not hidden by the parent", func(t *testing.T) {
		parent := plannedLock{Schema: "public", Name: "olv_fp_parent", Mode: lockModeRowExclusive}
		plan := lockPlan{Target: parent, TargetStatement: parent.lockStatement()}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer tx.Rollback() //nolint:errcheck // test teardown
		if _, err := tx.ExecContext(ctx, plan.TargetStatement); err != nil {
			t.Fatalf("lock parent: %v", err)
		}
		// Reach the LEAF, which the plan never mentions.
		if _, err := tx.ExecContext(ctx,
			`LOCK TABLE ONLY "public"."olv_fp_leaf" IN ACCESS EXCLUSIVE MODE`); err != nil {
			t.Fatalf("lock leaf: %v", err)
		}
		err = verifyLockFootprint(ctx, tx, plan)
		if !errors.Is(err, ErrMigrationLockFootprint) {
			t.Fatalf("verify = %v, want a refusal: the leaf is a relation of its own and the plan never declared it", err)
		}
		if !strings.Contains(err.Error(), "olv_fp_leaf") {
			t.Errorf("the refusal names the parent instead of the leaf that was actually locked: %v", err)
		}
	})

	t.Run("a declared leaf that WAS acquired is not erased", func(t *testing.T) {
		leaf := plannedLock{Schema: "public", Name: "olv_fp_leaf", Mode: lockModeRowExclusive}
		plan := lockPlan{Target: leaf, TargetStatement: leaf.lockStatement()}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer tx.Rollback() //nolint:errcheck // test teardown
		if _, err := tx.ExecContext(ctx, plan.TargetStatement); err != nil {
			t.Fatalf("lock leaf: %v", err)
		}
		if err := verifyLockFootprint(ctx, tx, plan); err != nil {
			t.Errorf("a declared leaf that was genuinely acquired was reported as never taken: %v", err)
		}
	})
}
