// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/store"
)

// guardcoordinator.go is the production caller: the function that turns the manifest, the
// ledger and the runner into a step of boot.
//
// Everything else in this campaign is library until this exists. A constructor that returns
// zero units, or a test that calls retryUnit.run directly, does not make the runner part of
// the product — so this file is the one whose absence would make "implemented" a false
// claim, and the regression that proves it works must go through the real Open.

// guardBulkProjectionSQL projects EVERY target in ONE roundtrip.
//
// The stable boot is the case that matters. Calling the three per-unit projectors for each
// entry would floor a steady-state boot at 3N statements — 138 for this edition, 276 if both
// edges of every lineage were walked — for a database in which nothing has to change. That
// cost is paid on every start, forever, so it is worth one wide query.
//
// ROWS FROM ... WITH ORDINALITY is what makes the answer checkable. PostgreSQL runs the
// functions of a ROWS FROM in parallel and PADS the shorter outputs with NULL, so unequal
// arrays would silently produce rows with missing components; the ordinal lets Go verify that
// the batch came back complete, in order, and with the cardinality it asked for. Go rejects
// unequal input lengths BEFORE the query for the same reason.
//
// It returns DATA, never a verdict: the same columns the per-unit projection returns, decoded
// by the same function, compared by the same comparator.
const guardBulkProjectionSQL = `WITH expected AS (
  SELECT u.relation_schema, u.relation_name, u.trigger_name, u.ordinal
  FROM ROWS FROM (
    pg_catalog.unnest($1::text[]),
    pg_catalog.unnest($2::text[]),
    pg_catalog.unnest($3::text[])
  ) WITH ORDINALITY AS u(relation_schema, relation_name, trigger_name, ordinal)
),
relations AS (
  SELECT e.ordinal,
         c.oid AS relation_oid,
         e.relation_schema,
         e.relation_name,
         e.trigger_name,
         c.relkind::text AS relkind,
         c.relpersistence::text AS relpersistence,
         c.relispartition,
         EXISTS (SELECT 1 FROM pg_catalog.pg_inherits i WHERE i.inhrelid = c.oid) AS has_parent,
         EXISTS (SELECT 1 FROM pg_catalog.pg_inherits i WHERE i.inhparent = c.oid) AS has_child
  FROM expected e
  LEFT JOIN pg_catalog.pg_namespace n ON n.nspname = e.relation_schema
  LEFT JOIN pg_catalog.pg_class c ON c.relnamespace = n.oid AND c.relname = e.relation_name
)
SELECT r.ordinal, ` + guardCatalogColumns + `
FROM relations r
` + guardCatalogJoins + `
ORDER BY r.ordinal`

// projectGuardCatalogBatch reads every key in one statement.
func projectGuardCatalogBatch(ctx context.Context, q rowQuerier, keys []guardKey) (map[guardKey]guardCatalogRow, error) {
	if len(keys) == 0 {
		return map[guardKey]guardCatalogRow{}, nil
	}
	schemas := make([]string, 0, len(keys))
	relations := make([]string, 0, len(keys))
	triggers := make([]string, 0, len(keys))
	seen := make(map[guardKey]bool, len(keys))
	for _, k := range keys {
		if seen[k] {
			return nil, fmt.Errorf("sqlstore: the guard batch names %s twice; a duplicated target would make the row count disagree with the plan", k)
		}
		seen[k] = true
		schemas = append(schemas, k.Schema)
		relations = append(relations, k.Relation)
		triggers = append(triggers, k.Trigger)
	}
	// ROWS FROM pads a shorter output with NULLs, so three arrays of different lengths would
	// come back as rows whose components silently did not belong together.
	//
	// As written the three cannot differ — one loop appends to all of them — so this check
	// CANNOT fire today, and saying so is better than implying it is catching something. It is
	// here for the edit that builds them separately, which is the shape that would reintroduce
	// the hazard.
	if len(schemas) != len(relations) || len(relations) != len(triggers) {
		return nil, fmt.Errorf("sqlstore: the guard batch arrays are %d/%d/%d long; ROWS FROM pads the shorter one with NULLs, so they must match exactly",
			len(schemas), len(relations), len(triggers))
	}

	rows, err := q.QueryContext(ctx, guardBulkProjectionSQL, schemas, relations, triggers)
	if err != nil {
		return nil, fmt.Errorf("sqlstore: project the guard catalog batch: %w", err)
	}
	defer rows.Close()
	read := make([]guardBatchRow, 0, len(keys))
	for rows.Next() {
		var ordinal int64
		row, serr := scanGuardCatalogRowWithOrdinal(rows.Scan, &ordinal)
		if serr != nil {
			return nil, fmt.Errorf("sqlstore: decode the guard catalog batch: %w", serr)
		}
		read = append(read, guardBatchRow{Ordinal: ordinal, Row: row})
	}
	if err := rows.Err(); err != nil {
		// A failed scan or a failed iteration invalidates the WHOLE batch. Keeping the rows
		// read so far would present a truncated reading as a complete one, and a target that
		// was never read would look like a target whose relation is absent.
		return nil, fmt.Errorf("sqlstore: read the guard catalog batch: %w", err)
	}
	return foldGuardCatalogBatch(read, keys)
}

// guardBatchRow is one row of the bulk projection, with the ordinal the query prefixes.
type guardBatchRow struct {
	Ordinal int64
	Row     guardCatalogRow
}

// foldGuardCatalogBatch turns the rows the bulk query returned into the map the plan is
// compared against, refusing anything that is not a complete, ordered, duplicate-free reading.
//
// IT IS A SEPARATE FUNCTION BECAUSE OF WHAT A TEST CAN REACH. These checks used to live inside
// the loop that consumes *sql.Rows, and the only test over them ran a real query whose SQL
// happens to return the right ordinals in the right order — so deleting the ordinal check
// entirely left that test green. A projection is only comparable with a plan if it is complete
// and in order; a property nothing can exercise is a property nothing is holding.
//
// The ordinal is what makes "in order" checkable at all: ROWS FROM preserves the input order,
// so a row arriving out of sequence means the reading no longer lines up with the arrays that
// asked for it — and the KEY of each row is decoded from the same catalog it came from, which
// is why the count alone cannot substitute for it.
func foldGuardCatalogBatch(read []guardBatchRow, keys []guardKey) (map[guardKey]guardCatalogRow, error) {
	out := make(map[guardKey]guardCatalogRow, len(keys))
	var expected int64 = 1
	for _, r := range read {
		if r.Ordinal != expected {
			return nil, fmt.Errorf("sqlstore: the guard catalog batch returned ordinal %d where %d was expected; a batch that is not complete and in order cannot be compared with the plan",
				r.Ordinal, expected)
		}
		expected++
		if _, dup := out[r.Row.Key]; dup {
			return nil, fmt.Errorf("sqlstore: the guard catalog batch returned %s twice", r.Row.Key)
		}
		out[r.Row.Key] = r.Row
	}
	if len(out) != len(keys) {
		return nil, fmt.Errorf("sqlstore: the guard catalog batch returned %d rows for %d targets", len(out), len(keys))
	}
	// AND EVERY KEY ASKED FOR IS PRESENT. Equal counts with a substituted key would otherwise
	// pass: the map would hold as many entries as there are targets, one of them for a relation
	// nobody asked about, and the target it replaced would read as absent.
	for _, k := range keys {
		if _, ok := out[k]; !ok {
			return nil, fmt.Errorf("sqlstore: the guard catalog batch holds no reading for %s although it returned %d rows for %d targets",
				k, len(read), len(keys))
		}
	}
	return out, nil
}

// scanGuardCatalogRowWithOrdinal reads the ordinal the bulk query prefixes, then the shared
// columns.
func scanGuardCatalogRowWithOrdinal(sc func(...any) error, ordinal *int64) (guardCatalogRow, error) {
	return scanGuardCatalogRow(func(dest ...any) error {
		return sc(append([]any{ordinal}, dest...)...)
	})
}

// guardLockPlan is the lock plan EVERY unit shares.
//
// The three control-plane relations are a COMMON PREFIX taken in one fixed order, and that
// is what stops two units forming a cycle with each other. They are taken at ROW EXCLUSIVE
// because that is the only mode available on a self-revoked append-only table: every mode
// above it requires UPDATE, DELETE or TRUNCATE and fails 42501 — ownership does not exempt —
// while INSERT authorizes ROW EXCLUSIVE, which is the mode the insert takes anyway.
//
// The target goes LAST and at SHARE ROW EXCLUSIVE: it is the relation with real concurrent
// writers, and it is taken at the strongest mode the whole unit needs, because escalating
// mode inside a transaction is a documented deadlock recipe. PostgreSQL 15 and 18 both
// document SHARE ROW EXCLUSIVE for ALTER TABLE ... ENABLE ALWAYS TRIGGER and for CREATE
// TRIGGER.
func guardLockPlan(target string, intent unitIntent) lockPlan {
	meta := make([]plannedLock, 0, 3)
	for _, t := range dialect.GuardControlPlaneTables() {
		meta = append(meta, plannedLock{Schema: guardSchema, Name: t, Mode: lockModeRowExclusive})
	}
	// Sorted by the comparison key, which is what lockPlan.validate requires. The declared
	// DDL order and the sorted order coincide today; sorting rather than relying on that is
	// what keeps a future rename from silently breaking the prefix property.
	sortPlannedLocks(meta)

	// THE DECLARED MODE IS PER INTENT, because the two edges genuinely need different things.
	//
	// Adoption performs NO DDL: it verifies under the lock and writes attribution. ROW
	// EXCLUSIVE is enough for that and it is not a compromise — its conflict set contains
	// SHARE ROW EXCLUSIVE and ACCESS EXCLUSIVE, so holding it blocks every statement that could
	// change a trigger (CREATE, ALTER ... ENABLE/DISABLE, DROP). It is also the strongest mode
	// an explicit LOCK TABLE can take on an append-only table at all.
	//
	// The transition runs ALTER TABLE ... ENABLE ALWAYS TRIGGER, which acquires SHARE ROW
	// EXCLUSIVE implicitly and by ownership rather than by privilege. So it DECLARES that mode
	// — the pre-commit footprint checks the real one — while its sentinel takes ROW EXCLUSIVE,
	// which is all PostgreSQL will grant an explicit statement here. See lockPlan.TargetAcquire.
	if intent == intentAdoptLegacy {
		tgt := plannedLock{Schema: guardSchema, Name: target, Mode: lockModeRowExclusive}
		return lockPlan{Metadata: meta, Target: tgt, TargetStatement: tgt.lockStatement()}
	}
	sentinel := lockModeRowExclusive
	tgt := plannedLock{Schema: guardSchema, Name: target, Mode: lockModeShareRowExclusive}
	plan := lockPlan{Metadata: meta, Target: tgt, TargetAcquire: &sentinel}
	plan.TargetStatement = plan.targetAcquireStatement()
	return plan
}

func sortPlannedLocks(ls []plannedLock) {
	for i := 1; i < len(ls); i++ {
		for j := i; j > 0 && ls[j].relation() < ls[j-1].relation(); j-- {
			ls[j], ls[j-1] = ls[j-1], ls[j]
		}
	}
}

// guardAttemptID is one attempt's identity, DERIVED rather than generated.
//
// Deterministic on purpose, and the determinism is what makes idempotence work: a boot that
// retries the same attempt over the same reading produces the same attempt id, so the receipt
// it would write is byte-identical to the one already there and the insert is idempotent
// instead of a conflict. A random id would make every retry a different claim about the same
// work.
func guardAttemptID(rolloutID, unitID string, attempt int, pre [32]byte) (string, error) {
	w := newCanonWriter(canonDomainEvent, guardManifestFormat)
	w.str("attempt")
	w.str(rolloutID)
	w.str(unitID)
	w.i64(int64(attempt))
	w.bytes32(pre)
	d, err := w.sum()
	if err != nil {
		return "", err
	}
	return hexDigest(d), nil
}

// guardUnitRunner holds the per-unit state the five callbacks and the two hooks share.
type guardUnitRunner struct {
	dia     dialect.Dialect
	conn    *sql.Conn
	rollout guardRolloutContext
	plan    guardUnitPlan
	// predecessor is the receipt this unit's lineage requires, already verified by the
	// coordinator. Nil for the first edge of a target.
	predecessor optDigest

	// lastProjected is the most recent reading Project produced.
	//
	// BeforeAttempt needs it and is not given it: the runner projects, validates, checks the
	// precondition and only then calls the hook, so the reading is available by the time the
	// hook runs. It is overwritten again by the re-projection UNDER the lock, which is
	// deliberate — that one is the judged reading, and Execute is handed it directly.
	lastProjected      prestate
	lastProjectedValid bool

	attempt   int
	attemptID string

	// session takes a DISTINCT checkout for the durable failure record.
	//
	// Not "a new physical connection", and not "a usable" one either: database/sql may hand back
	// an idle connection the pool already holds, and it promises a single pooled connection with
	// same-session queries — never that a later BeginTx on it will succeed. Both stronger
	// wordings have been written here and both were removed for claiming what nothing enforces.
	// What IS supported is the only thing this needs: a checkout distinct from the poisoned
	// handle the cancellation left behind. If that one also fails, afterFailure logs it.
	//
	// afterFailure cannot use r.conn and that is a measured fact, not a preference: the
	// failure this engine most needs to record is a CANCELLATION, and canceling an operation
	// in flight poisons the connection it was running on. context.WithoutCancel produces a
	// live context, never a live socket, so BeginTx on r.conn came back `driver: bad
	// connection` and the only diagnosis of the incident was dropped. Round seven built that
	// case and measured exactly it.
	//
	// It is the same argument ReconcileSession is built on, one call site over: reading — or
	// here writing — through the connection that just died asks the corpse whether it survived.
	session func(ctx context.Context) (rowQuerier, func(), error)
}

// project reads the prestate: the catalog for the object, the ledger for the receipt, and
// the rollout for the authorisation.
//
// The two readings are SEPARATE queries on purpose. Folding them into one join would make an
// unreadable ledger indistinguishable from an absent receipt, which is the single most
// dangerous simplification in this path: it turns a committed unit into one about to be
// applied twice.
// fenceSharedGuardFunction pins the shared trigger function's catalog row until commit.
//
// WHY A RELATION LOCK COULD NOT DO IT. The canonical projection includes the function's own
// source and attributes, and the guard on every target calls THAT function. But CREATE OR
// REPLACE FUNCTION needs no lock on the relation whose trigger points at it: it opens pg_proc
// in RowExclusiveLock, which is compatible with the AccessShareLock a projection takes. So the
// runner could project a canonical function, an owner could replace it, and the receipt would
// commit attesting bytes that were no longer there — reversible before the next projection,
// leaving no other trace.
//
// WHY THIS STATEMENT AND NOT ANOTHER, all four measured on PostgreSQL 15.18:
//
//   - SELECT ... FROM pg_proc ... FOR UPDATE does fence a concurrent replace (measured: the
//     replacer blocked for its whole lock_timeout and died "while updating tuple (17,18) in
//     relation pg_proc"), but it is REFUSED to a non-superuser — "permission denied for table
//     pg_proc" as olivares_app — and this engine refuses a superuser application role by
//     design. So it is unusable in the topology the product ships.
//   - An advisory lock keyed on the function's OID would fence nothing: an operator's CREATE
//     OR REPLACE FUNCTION takes no advisory lock, so it would coordinate this engine with
//     itself and call that a boundary.
//   - LOCK TABLE cannot name pg_proc's ROW, and the object has no LOCK statement of its own.
//   - ALTER FUNCTION ... RESET ALL fences, and it is REJECTED here: it ERASES proconfig, which
//     the canonical form makes load-bearing as NULL. Measured: with {search_path=evil} installed,
//     RESET ALL leaves proconfig NULL — so the fence repairs the very drift the protected
//     projection exists to reject, and the mutation leaves no durable evidence because the
//     mechanism undoes it. A fence that launders is worse than no fence.
//   - ALTER FUNCTION ... SET "olivares.fence" = '1' would preserve other settings, but a
//     non-superuser cannot: "permission denied to set parameter olivares.fence". Same wall as
//     FOR UPDATE.
//   - ALTER FUNCTION ... OWNER TO <the same owner> preserves everything and fences NOTHING:
//     PostgreSQL returns early when the owner is unchanged, so no tuple is updated and no lock
//     is taken. Measured: the concurrent replace completed in 2 ms and wiped proconfig.
//   - ALTER FUNCTION ... CALLED ON NULL INPUT fences and preserves proconfig, and it was used
//     until round three showed it moved the laundering rather than closing it: the statement
//     writes proisstrict, which the canonical form compares, so a concurrent
//     ALTER FUNCTION ... STRICT landing between the preliminary projection and the fence was
//     silently reverted — the same pattern RESET ALL was rejected for, on another load-bearing
//     field. Rejected for that reason.
//   - ALTER FUNCTION ... COST <the value it already has> is what is used. Measured on 15.18 with
//     the value UNCHANGED, which is the question that matters (OWNER TO returns early when
//     nothing changes, and this does not): the pg_proc tuple was rewritten — xmin moved
//     39758 -> 39759 — and a concurrent CREATE OR REPLACE blocked its whole lock_timeout and
//     died "while updating tuple (18,32) in relation pg_proc". And it preserves proisstrict:
//     with STRICT planted, the statement left proisstrict = true for the projection to reject.
//
// THIS FENCE NORMALISES NOTHING, and the sentence that used to stand here said the opposite.
// An earlier form wrote the CANONICAL cost, and round four called that laundering whatever the
// field's importance: a procost drift arriving between the preliminary projection and this
// statement was erased by the fence itself, and the protected projection then read a value the
// fence had fabricated. The statement now READS the current value inside a DO block and writes
// it back, which moves xmin exactly the same and rewrites nothing.
//
// The residual window is stated rather than hidden: a change committed between that SELECT and
// the ALTER inside the block would be overwritten by the value read a moment earlier. It is
// unbounded rather than tiny — a backend can be descheduled between two consecutive steps and
// nothing here says otherwise. What can be said is the comparison: it is narrower than the
// normalisation it replaced, which happened on EVERY fence unconditionally, against a field that
// is a planner estimate. Measured on 15, 16, 17 and 18 by
// TestPostgresTheSharedFunctionFenceStopsAConcurrentReplace: a planted COST 12345 survives, and
// reverting the statement to the canonical form reddens that case and only that case.
//
// WHAT IT COSTS, declared rather than discovered later: one pg_proc tuple update per unit per
// boot, PLUS one per attempt of the CLOSE, which takes this same fence in its acquisition order
// and takes it again on every retry — a call this paragraph used to omit, so it reported zero for
// a boot where the close still runs. On a first boot that is one per edge; on later boots every unit is already applied and
// skipped, so it is zero. Autovacuum reclaims them.
//
// WHAT IT REQUIRES: ownership of the function, which the migration role has because it created
// it. Where it does not, the ALTER fails and the unit fails with it — the rollout does not
// close, which is the correct answer: a receipt nothing could fence is a receipt nothing can
// stand behind.
// verifyGuardFenceCapability refuses BEFORE anything durable is written when this session
// cannot take the fence.
//
// THE BRICK IT REMOVES. The canonical form of the shared function deliberately excludes its
// OWNER, because a legitimate installation may have created it under any role — so
// verifyBootstrapFunction accepts a function owned by somebody else. ALTER FUNCTION does not:
// PostgreSQL requires ownership. On a database where an older edition created the function
// under a previous role, the preflight passed, v6 reused the function, the triggers called it,
// and then the FIRST unit's fence failed 42501. That failure is classified permanent, which
// appends `pending/blocked` — and a blocked rollout refuses to mutate on every later boot, so
// transferring ownership afterwards fixed nothing. This edition ships no repair CLI, so the
// deployment was bricked by a state the preflight had called acceptable.
//
// Checking here makes it recoverable: nothing has been written, the boot refuses with the two
// remedies named, and the next boot after either of them proceeds normally.
//
// THE PREDICATE IS THE SERVER'S OWN, measured on 15.18 rather than reasoned about:
//
//	as a role that is NOT a member of the owner:
//	  pg_has_role(p.proowner, 'USAGE') = f
//	  ALTER FUNCTION public.probe_fn() CALLED ON NULL INPUT
//	    -> ERROR: must be owner of function public.probe_fn
//	after GRANT owner TO this_role:
//	  pg_has_role(p.proowner, 'USAGE') = t
//	  ALTER FUNCTION ...                  -> ALTER FUNCTION
//
// USAGE and MEMBER are read separately because they are different remedies. USAGE is what
// ownership checks use — privileges inherited automatically. A role that is a MEMBER without
// USAGE (a NOINHERIT membership) would have to SET ROLE first, and the fence statement does
// not, so it genuinely cannot take it; saying which of the two situations it is in is the
// difference between a usable error and "permission denied".
func verifyGuardFenceCapability(ctx context.Context, q rowQuerier) error {
	fn := canonicalGuardDefinition().Function
	rows, err := q.QueryContext(ctx, `SELECT o.rolname,
       pg_catalog.pg_has_role(p.proowner, 'USAGE'),
       pg_catalog.pg_has_role(p.proowner, 'MEMBER')
FROM pg_catalog.pg_proc p
JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace
JOIN pg_catalog.pg_roles o ON o.oid = p.proowner
WHERE n.nspname = $1 AND p.proname = $2 AND p.pronargs = 0`, fn.Schema, fn.Name)
	if err != nil {
		return fmt.Errorf("sqlstore: read the owner of the shared guard function %s.%s(): %w", fn.Schema, fn.Name, err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return fmt.Errorf("sqlstore: read the owner of the shared guard function %s.%s(): %w", fn.Schema, fn.Name, err)
		}
		// Absent rather than unownable. It is a different failure with a different cause, and
		// the bootstrap verification says so in its own terms — but reporting it here as "cannot
		// fence" would send an operator after a permission that is not the problem.
		return fmt.Errorf("%w: the shared guard function %s.%s() does not exist, so there is nothing for the guards on the control plane to call",
			ErrGuardControlPlaneBootstrapInconsistent, fn.Schema, fn.Name)
	}
	var owner string
	var usage, member bool
	if err := rows.Scan(&owner, &usage, &member); err != nil {
		return fmt.Errorf("sqlstore: read the owner of the shared guard function %s.%s(): %w", fn.Schema, fn.Name, err)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("sqlstore: read the owner of the shared guard function %s.%s(): %w", fn.Schema, fn.Name, err)
	}
	if usage {
		return nil
	}
	remedy := fmt.Sprintf("transfer it (ALTER FUNCTION %s.%s() OWNER TO <the migration role>), run the migration as %q, or GRANT %q TO <the migration role>",
		fn.Schema, fn.Name, owner, owner)
	if member {
		remedy = fmt.Sprintf("the migration role is a member of %q but does not inherit its privileges, and the fence does not SET ROLE; make the membership inheritable (ALTER ROLE <the migration role> INHERIT, or re-grant it WITH INHERIT TRUE) or transfer the function (ALTER FUNCTION %s.%s() OWNER TO <the migration role>)",
			owner, fn.Schema, fn.Name)
	}
	return fmt.Errorf("%w: the shared guard function %s.%s() is owned by %q and this session cannot ALTER it, so no unit could hold it still while its receipt was written. Nothing has been recorded and no rollout has been opened — %s",
		ErrMigrationUnauthorised, fn.Schema, fn.Name, owner, remedy)
}

func (r *guardUnitRunner) fenceSharedGuardFunction(ctx context.Context, tx *sql.Tx) error {
	fn := canonicalGuardDefinition().Function
	if _, err := tx.ExecContext(ctx, fenceSharedFunctionStatement(fn.Schema, fn.Name)); err != nil {
		return fmt.Errorf("sqlstore: fence the shared guard function %s.%s(): %w", fn.Schema, fn.Name, err)
	}
	return nil
}

// fenceSharedFunctionStatement renders the fence for one zero-argument function.
//
// IT WRITES THE VALUE THE FUNCTION ALREADY HAS, and that is the whole point of the shape.
//
// The fence needs to move the row's xmin — that is what makes a concurrent CREATE OR REPLACE
// wait instead of landing between the projection and the receipt — and `ALTER FUNCTION ... COST`
// does that even when the value is unchanged (measured). The previous version wrote the CANONICAL
// cost, which achieved the same fence and, in doing so, ERASED any drift in procost that a
// concurrent owner had introduced: the protected projection then read a value the fence itself
// had just fabricated. Round four called that laundering and was right, whatever the field's
// importance.
//
// Reading the value inside the same statement and writing it back keeps both properties: the row
// is fenced and nothing is rewritten. The residual window is stated rather than hidden — a change
// committed between the SELECT and the ALTER inside this block would be overwritten by the value
// read a moment earlier. Nothing bounds that window: two consecutive steps can still be split by
// the scheduler, and no measurement here turns "usually tiny" into a ceiling. It is narrower than
// the normalisation it replaces, which happened on EVERY fence unconditionally.
//
// The identifiers are rendered rather than bound because ALTER FUNCTION takes no parameters, and
// they are both COMPILE-TIME constants of this package (guardSchema, guardBlockMutationFn) —
// never runtime data. Quoting them keeps the statement well formed if either ever becomes a name
// needing quotes; it is not what makes this safe.
func fenceSharedFunctionStatement(schema, name string) string {
	return `DO $guardfence$
DECLARE cost_now real;
BEGIN
  SELECT p.procost INTO STRICT cost_now
    FROM pg_catalog.pg_proc p
    JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace
   WHERE n.nspname = ` + quoteSQLLiteral(schema) + `
     AND p.proname = ` + quoteSQLLiteral(name) + `
     AND p.pronargs = 0;
  EXECUTE pg_catalog.format('ALTER FUNCTION %I.%I() COST %s',
                 ` + quoteSQLLiteral(schema) + `, ` + quoteSQLLiteral(name) + `, cost_now::text);
END $guardfence$`
}

// quoteSQLLiteral renders a Go string as a single-quoted SQL literal.
//
// Used only for the two compile-time constants above, where a bound parameter is not available
// because the statement is a DO block. Doubling the quote is the escape PostgreSQL defines for a
// standard-conforming string.
func quoteSQLLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

func (r *guardUnitRunner) project(ctx context.Context, db rowQuerier) (prestate, error) {
	row, err := projectGuardCatalogRow(ctx, db, r.plan.Spec.Key)
	if err != nil {
		return prestate{}, err
	}
	receiptPresent := false
	switch _, rerr := readGuardReceipt(ctx, dbQuerier{db}, r.dia, r.rollout.RolloutID, r.plan.UnitID, guardReceiptKindUnit); {
	case rerr == nil:
		receiptPresent = true
	case errors.Is(rerr, sql.ErrNoRows):
	default:
		// An unreadable ledger is NOT an absent receipt. Propagating it means the runner
		// refuses rather than proceeding on a reading it does not have.
		return prestate{}, rerr
	}
	pre := r.rollout.bind(prestateFromCatalog(row, r.plan.Spec, receiptPresent), r.plan.Spec)
	r.lastProjected, r.lastProjectedValid = pre, true
	return pre, nil
}

// dbQuerier adapts a rowQuerier to the dialect.Querier the ledger helpers take.
//
// The two interfaces differ by ExecContext, which a PROJECTION must not have: a projector
// that could execute is a projector that could mutate the thing it is describing. This
// adapter provides the read methods and a refusal for the write one.
type dbQuerier struct{ q rowQuerier }

func (d dbQuerier) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return d.q.QueryContext(ctx, query, args...)
}

func (d dbQuerier) ExecContext(context.Context, string, ...any) (sql.Result, error) {
	return nil, errors.New("sqlstore: a guard projection may not execute statements")
}

// beforeAttempt records, durably and OUTSIDE the unit's transaction, that an attempt began.
//
// THE ORDINAL THAT IDENTIFIES THE ATTEMPT COMES FROM THE LEDGER, NOT FROM THE ARGUMENT, and that
// is the whole of the C4-01/C4-02 fix. `attempt` is this RUN's counter: it restarts at 1 in every
// process, so using it as an identity input made a post-crash retry re-announce an id already on
// the record and left the rollout permanently unfoldable. See gateUnitAttemptOrdinal for the
// measurement and for why the count has to happen inside the transaction that appends.
//
// The argument is still taken and still stored, because backoff and the retry budget are genuinely
// properties of this run. It simply no longer decides who the attempt IS.
func (r *guardUnitRunner) beforeAttempt(ctx context.Context, attempt int) error {
	if !r.lastProjectedValid {
		return errors.New("sqlstore: an attempt was started before any prestate had been projected")
	}
	r.attempt = attempt
	digest, err := prestateDigest(r.lastProjected)
	if err != nil {
		return err
	}
	tx, err := r.conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	// Inside the transaction, and before the append that this count is about to be numbered by.
	ordinal, err := gateUnitAttemptOrdinal(ctx, tx, r.dia, r.rollout.RolloutID, r.plan.UnitID)
	if err != nil {
		return err
	}
	if r.attemptID, err = guardAttemptID(r.rollout.RolloutID, r.plan.UnitID, ordinal, digest); err != nil {
		return err
	}
	if _, err := appendGateEvent(ctx, tx, r.dia, r.gateEvent(gateEventAttemptStarted, r.lastProjected, digest,
		gatePhasePending, gateConditionClean, guardDiagnostic{})); err != nil {
		return err
	}
	return tx.Commit()
}

// execute inserts the judged reading and then performs the intent's DDL.
//
// THE JUDGED EVENT COMES FIRST, before any mutation of the target. That order is the whole
// contract: the event carries the reading that Authorized the change, and a reading written
// after the change would be a reading of the result. It shares this transaction with the
// work and the receipt, so all three commit together or none of them do.
func (r *guardUnitRunner) execute(ctx context.Context, tx *sql.Tx, pre prestate) error {
	digest, err := prestateDigest(pre)
	if err != nil {
		return err
	}
	if _, err := appendGateEvent(ctx, tx, r.dia, r.gateEvent(gateEventAttemptJudged, pre, digest,
		gatePhasePending, gateConditionClean, guardDiagnostic{})); err != nil {
		return err
	}
	switch r.plan.Intent {
	case intentAdoptLegacy:
		// Adoption alters NOTHING. It verifies under the lock and writes attribution; the
		// object it adopts is the object the table's own DDL created.
		return nil
	case intentTransitionLegacyOToA:
		// #nosec G202 -- a SQL IDENTIFIER cannot be a bind parameter, so gosec's remedy does not exist for DDL: `ALTER TABLE ONLY $1 ENABLE ALWAYS TRIGGER $2` is a syntax error on every major. The three parts are guardKey fields, which buildGuardManifest derives from the registry's compile-time table list and refuses when NUL-bearing (guardmanifest.go:477); none of them is ever request data. quoteIdent doubles embedded quotes, which is PostgreSQL's own identifier quoting
		stmt := "ALTER TABLE ONLY " + quoteIdent(r.plan.Spec.Key.Schema) + "." +
			quoteIdent(r.plan.Spec.Key.Relation) +
			" ENABLE ALWAYS TRIGGER " + quoteIdent(r.plan.Spec.Key.Trigger)
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("sqlstore: promote %s to ALWAYS: %w", r.plan.Spec.Key, err)
		}
		return nil
	default:
		// Every other intent is unreachable from this coordinator, and saying so is better
		// than a silent no-op: a no-op would let a unit report success for work it never did.
		return fmt.Errorf("%w: this coordinator does not execute intent %q", ErrMigrationUnauthorised, r.plan.Intent)
	}
}

// receipt writes the attribution, after checking it against the judged event.
//
// The check is the part that matters. The receipt claims to attribute work authorized by a
// particular reading; reading the judged event back IN THIS TRANSACTION and requiring the
// same attempt id and the same prestate digest is what makes that claim verifiable instead
// of asserted. If Execute ever wrote a different reading than the one it acted on, this is
// where it stops.
func (r *guardUnitRunner) receipt(ctx context.Context, tx *sql.Tx, pre prestate) error {
	digest, err := prestateDigest(pre)
	if err != nil {
		return err
	}
	judgedAttempt, judgedDigest, err := readJudgedEvent(ctx, tx, r.dia, r.rollout.RolloutID, r.plan.UnitID)
	if err != nil {
		return err
	}
	if judgedAttempt != r.attemptID {
		return fmt.Errorf("%w: the receipt for %s carries attempt %q but the judged event in this transaction carries %q",
			ErrMigrationUnauthorised, r.plan.Spec.Key, r.attemptID, judgedAttempt)
	}
	if !judgedDigest.Valid || judgedDigest.D != digest {
		return fmt.Errorf("%w: the receipt for %s attributes a reading hashing to %s but the judged event records %s",
			ErrMigrationUnauthorised, r.plan.Spec.Key, hexDigest(digest), judgedDigest)
	}

	var from optText
	if r.plan.Intent == intentTransitionLegacyOToA {
		// The transition records where it came FROM, which is the fact the catalog will no
		// longer show: after success the object reads 'A', and 'O' survives only here.
		from = someText(pre.GuardEnableState)
	}
	_, err = insertGuardReceipt(ctx, tx, r.dia, guardReceipt{
		RolloutID:            r.rollout.RolloutID,
		UnitID:               r.plan.UnitID,
		Kind:                 guardReceiptKindUnit,
		Intent:               r.plan.Intent,
		Key:                  r.plan.Spec.Key,
		Epoch:                r.rollout.CodeEpoch,
		Format:               r.rollout.Format,
		CodeSHA256:           r.rollout.CodeSHA256,
		RetainedRevision:     r.rollout.RetainedRevision,
		RetainedSHA256:       r.rollout.RetainedSHA256,
		SpecSHA256:           r.plan.Spec.SpecSHA256,
		DefinitionSHA256:     r.plan.Spec.DefinitionSHA256,
		PrestateSHA256:       digest,
		FromEnableState:      from,
		ToEnableState:        r.plan.CanonicalEnableState,
		PredecessorReceiptID: r.predecessor,
		AttemptID:            r.attemptID,
	})
	return err
}

// projectReceipt reads what the ledger says about this unit.
func (r *guardUnitRunner) projectReceipt(ctx context.Context, db rowQuerier) (receiptProjection, error) {
	got, err := readGuardReceipt(ctx, dbQuerier{db}, r.dia, r.rollout.RolloutID, r.plan.UnitID, guardReceiptKindUnit)
	switch {
	case err == nil:
		return receiptProjectionFrom(got), nil
	case errors.Is(err, sql.ErrNoRows):
		// Absent, and READABLE. The runner turns an error into Readable=false; an absence is
		// a fact, and the two must not be the same value.
		return receiptProjection{Readable: true}, nil
	default:
		return receiptProjection{}, err
	}
}

// projectObject reads what the object looks like now.
func (r *guardUnitRunner) projectObject(ctx context.Context, db rowQuerier) (objectProjection, error) {
	row, err := projectGuardCatalogRow(ctx, db, r.plan.Spec.Key)
	if err != nil {
		return objectProjection{}, err
	}
	return objectProjectionFrom(row, r.plan.Spec), nil
}

// failureWriter returns the connection the durable failure record is written on.
//
// A DISTINCT CHECKOUT WHEN THERE IS A PROVIDER, r.conn otherwise — distinct from the poisoned
// handle, which is what the pool supports, and neither "fresh" nor "usable", which it does not
// promise. The provider is the same one
// ReconcileSession uses and it exists for the same reason — a connection that has just had an
// operation canceled under it is not a session, it is a corpse — but falling back is deliberate
// rather than lazy: this whole path is best-effort diagnosis, and refusing to write anything
// because a second connection could not be opened would lose MORE than the bug it guards against.
func (r *guardUnitRunner) failureWriter(ctx context.Context) (*sql.Conn, func(), error) {
	if r.session == nil {
		return r.conn, func() {}, nil
	}
	q, release, err := r.session(ctx)
	if err != nil {
		return nil, nil, err
	}
	conn, ok := q.(*sql.Conn)
	if !ok {
		release()
		return r.conn, func() {}, nil
	}
	return conn, release, nil
}

// afterFailure records the failure durably, with its class.
func (r *guardUnitRunner) afterFailure(ctx context.Context, attempt int, f unitFailure, decision retryDecision, err error) {
	diag := guardDiagnostic{
		Code:          guardDiagnosticCode(f.Phase, decision),
		RetryClass:    guardRetryClassOf(decision),
		UnblockPolicy: guardUnblockPolicyOf(decision),
		SQLState:      sqlStateOf(err),
		Details:       guardRedactedDetail(err),
	}
	condition := gateConditionRetryable
	if diag.RetryClass != guardRetryClassRetryable {
		condition = gateConditionBlocked
	}
	digest, derr := prestateDigest(r.lastProjected)
	if derr != nil {
		slog.Warn("could not hash the prestate of a failed guard attempt; the failure is recorded without it",
			"target", r.plan.Spec.Key.String(), "err", derr)
		return
	}
	// A BOUNDED, INDEPENDENT CONTEXT **AND A DIFFERENT CONNECTION**, because the first half
	// alone does not work and that was measured. The failure this most needs to record is a
	// cancellation, and canceling an operation in flight leaves the connection it ran on
	// unusable: `context.WithoutCancel` hands back a live context on a dead socket, and BeginTx
	// returned `driver: bad connection` — so the run left an `attempt-started` and no
	// `attempt-failed` at all, losing the only diagnosis of the incident.
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), guardDiagnosticWriteTimeout)
	defer cancel()
	ctx = wctx
	writer, release, werr := r.failureWriter(ctx)
	if werr != nil {
		slog.Warn("could not open a session to record a failed guard attempt; the boot's own error is unchanged",
			"target", r.plan.Spec.Key.String(), "attempt", attempt, "err", werr)
		return
	}
	defer release()
	tx, terr := writer.BeginTx(ctx, nil)
	if terr != nil {
		// Logged and dropped, never substituted for the real error. The unit already has a
		// diagnosis; losing it to a bookkeeping failure would trade the answer for the note.
		slog.Warn("could not record a failed guard attempt durably; the boot's own error is unchanged",
			"target", r.plan.Spec.Key.String(), "attempt", attempt, "err", terr)
		return
	}
	defer func() { _ = tx.Rollback() }()
	if _, aerr := appendGateEvent(ctx, tx, r.dia, r.gateEvent(gateEventAttemptFailed, r.lastProjected, digest,
		gatePhasePending, condition, diag)); aerr != nil {
		slog.Warn("could not append the failure event for a guard attempt; the boot's own error is unchanged",
			"target", r.plan.Spec.Key.String(), "attempt", attempt, "err", aerr)
		return
	}
	if cerr := tx.Commit(); cerr != nil {
		slog.Warn("could not commit the failure event for a guard attempt; the boot's own error is unchanged",
			"target", r.plan.Spec.Key.String(), "attempt", attempt, "err", cerr)
	}
}

// gateEvent fills the fields every per-unit event shares.
func (r *guardUnitRunner) gateEvent(kind gateEventKind, pre prestate, digest [32]byte,
	phase gatePhase, condition gateCondition, diag guardDiagnostic) gateEvent {
	return gateEvent{
		RolloutID:        r.rollout.RolloutID,
		Kind:             kind,
		UnitID:           r.plan.UnitID,
		AttemptID:        r.attemptID,
		Intent:           r.plan.Intent,
		Key:              r.plan.Spec.Key,
		Format:           r.rollout.Format,
		CodeEpoch:        r.rollout.CodeEpoch,
		CodeSHA256:       r.rollout.CodeSHA256,
		RetainedRevision: r.rollout.RetainedRevision,
		RetainedSHA256:   r.rollout.RetainedSHA256,
		SpecSHA256:       someDigest(r.plan.Spec.SpecSHA256),
		DefinitionSHA256: someDigest(r.plan.Spec.DefinitionSHA256),
		PrestateSHA256:   someDigest(digest),
		PrestatePresent:  true,
		Prestate:         pre,
		PrestateBytes:    prestateRendering(pre),
		Phase:            phase,
		Condition:        condition,
		Diagnostic:       diag,
	}
}

// readJudgedEvent reads the newest attempt-judged for a unit, from within a transaction.
func readJudgedEvent(ctx context.Context, tx *sql.Tx, dia dialect.Dialect, rolloutID, unitID string) (string, optDigest, error) {
	rows, err := tx.QueryContext(ctx, dia.Rebind(
		"SELECT attempt_id, prestate_sha256 FROM "+guardOnly(dia)+guardGateEventsTable+
			" WHERE rollout_id = ? AND unit_id = ? AND kind = ? ORDER BY event_ordinal DESC LIMIT 1"),
		rolloutID, unitID, string(gateEventAttemptJudged))
	if err != nil {
		return "", optDigest{}, fmt.Errorf("sqlstore: read the judged event for unit %s: %w", unitID, err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return "", optDigest{}, fmt.Errorf("sqlstore: read the judged event for unit %s: %w", unitID, err)
		}
		return "", optDigest{}, fmt.Errorf("%w: unit %s has no judged event in this transaction, so its receipt would attribute a reading nothing recorded",
			ErrMigrationUnauthorised, unitID)
	}
	var attemptID sql.NullString
	var raw []byte
	if err := rows.Scan(&attemptID, &raw); err != nil {
		return "", optDigest{}, fmt.Errorf("sqlstore: read the judged event for unit %s: %w", unitID, err)
	}
	d, err := scanOptDigest(raw, "a judged prestate digest")
	if err != nil {
		return "", optDigest{}, err
	}
	return attemptID.String, d, rows.Err()
}

// guardDiagnosticCode names a failure in a form later boots route on.
func guardDiagnosticCode(phase unitPhase, decision retryDecision) string {
	return "UNIT_" + strings.ToUpper(string(phase)) + "_" + strings.ToUpper(guardRetryClassOf(decision))
}

// guardRetryClassOf maps the runner's decision onto the durable class.
//
// retryNewTransaction is the only retryable one. retryNewSession is NOT: this runner cannot
// replace the session that holds the coordination lock, so from the gate's point of view the
// rollout is stopped until another boot — which is a block, not a retry.
func guardRetryClassOf(decision retryDecision) string {
	switch decision {
	case retryNewTransaction:
		return guardRetryClassRetryable
	case retryAfterReconcile:
		return guardRetryClassUnknown
	default:
		return guardRetryClassPermanent
	}
}

// guardUnblockPolicyOf says what could move this failure out of blocked.
//
// An unknown outcome may be resolved by a later READ, so it records read_reconcile; a
// permanent failure needs a human, so it records operator. The difference must not depend on
// a message, which is why it is a column.
func guardUnblockPolicyOf(decision retryDecision) string {
	switch decision {
	case retryNewTransaction:
		return guardUnblockNone
	case retryAfterReconcile:
		return guardUnblockReadReconcile
	default:
		return guardUnblockOperator
	}
}

// sqlStateOf extracts a PostgreSQL condition code when the error carries one.
func sqlStateOf(err error) string {
	var pg interface{ SQLState() string }
	if errors.As(err, &pg) {
		return pg.SQLState()
	}
	return ""
}

// guardRedactedDetail is the human snapshot stored beside a failure.
//
// BEST EFFORT, and that word replaces a guarantee this cannot keep. An earlier version of this
// comment said the durable diagnostic would "never" become a second channel for secrets. It
// cannot promise that: it redacts by scanning FREE TEXT produced by arbitrary error values, and
// no amount of markers makes that exhaustive. What it does is drop the shapes that actually
// carry credentials here, case-insensitively, and bound the length.
//
// The structured fields are what decisions read. This is what an operator reads, and it is
// truncated and scrubbed on the way in rather than trusted.
func guardRedactedDetail(err error) string {
	if err == nil {
		return ""
	}
	const max = 1024
	s := err.Error()
	// Case-INSENSITIVE, because `POSTGRES://` and `Password =` are the same hazard as their
	// lower-case forms and a case-sensitive scan let both through. The comparison is done on a
	// folded copy while the CUT is applied to the original, so the stored text is not itself
	// case-folded.
	folded := strings.ToLower(s)
	for _, marker := range []string{
		"postgres://", "postgresql://",
		"password=", "password =", "password:",
		"secret=", "token=", "api_key=", "apikey=",
	} {
		if i := strings.Index(folded, marker); i >= 0 {
			s = s[:i] + "[redacted]"
			break
		}
	}
	if len(s) > max {
		s = s[:max] + "…[truncated]"
	}
	return s
}

// guardRolloutSummary is the observable result of one boot's guard work.
//
// It carries counts and identities, never bytes of prestate, never SQL with values and never
// a DSN. What it is for is an operator seeing, in one line, whether this boot changed
// anything and whether the gate closed.
type guardRolloutSummary struct {
	RolloutID    string
	Phase        gatePhase
	Condition    gateCondition
	Entries      int
	PlannedUnits int
	// FastPath is true when the boot did no DDL at all: one bulk projection, zero units.
	FastPath  bool
	Executed  map[unitIntent]int
	Outcomes  map[reconcileOutcome]int
	Refusals  []guardPlanRefusal
	Diagnosis string
}

// logValue renders the summary for slog without secrets.
func (s guardRolloutSummary) log() {
	attrs := []any{
		"rollout", s.RolloutID,
		"phase", string(s.Phase),
		"condition", string(s.Condition),
		"entries", s.Entries,
		"planned_units", s.PlannedUnits,
		"fast_path", s.FastPath,
		"refusals", len(s.Refusals),
	}
	for intent, n := range s.Executed {
		attrs = append(attrs, "executed_"+string(intent), n)
	}
	for outcome, n := range s.Outcomes {
		attrs = append(attrs, "outcome_"+string(outcome), n)
	}
	if s.Diagnosis != "" {
		attrs = append(attrs, "diagnosis", s.Diagnosis)
	}
	slog.Info("store: append-only guard rollout", attrs...)
}

// runAppendOnlyGuardUnits is THE production caller.
//
// It runs inside the callback of withMigrationLock, after applyModuleFileMigrations — so
// every relation it targets exists — and before reconcileAppendOnlyACL, which stays a
// separate leg because a row trigger cannot see TRUNCATE and the receipt of a trigger
// therefore cannot attest an ACL.
//
// On SQLite it returns immediately: the runner is PostgreSQL-only, and SQLite's append-only
// defense is its unconditional trigger pair.
func runAppendOnlyGuardUnits(
	ctx context.Context,
	mdb dialect.Execer,
	dia dialect.Dialect,
	registered []string,
	observerDSN string,
	hardened bool,
	session func(ctx context.Context) (rowQuerier, func(), error),
) (guardRolloutSummary, error) {
	summary := guardRolloutSummary{
		Executed: map[unitIntent]int{},
		Outcomes: map[reconcileOutcome]int{},
	}
	// THE MANIFEST IS BUILT ON BOTH ENGINES, before the engine gate, because the control
	// plane's own verification below needs it and because a manifest this binary cannot build
	// is a refusal that has nothing to do with which engine is underneath.
	m, err := buildGuardManifest(registered)
	if err != nil {
		return summary, err
	}
	if err := requireCompleteGuardCurrentEdition(m); err != nil {
		return summary, err
	}
	summary.Entries = len(m.Specs)
	// Receipt, inventory and gate histories select one edition state together. Validating
	// them independently would accept cross-products no writer can commit (for example an
	// epoch-1 bootstrap beside an epoch-2 inventory).
	history, err := verifyGuardEditionHistory(ctx, mdb, dia, m)
	if err != nil && m.CodeEpoch >= 3 {
		// A direct epoch-1 -> current boot performs 1->2 inside core v7. On
		// PostgreSQL each predecessor rollout may still be pending and may not be
		// treated as ready for the next edge. Finish the exact immediate predecessor
		// with the ordinary runner, under this same migration lock; recursion walks
		// every compiled edge in order. SQLite has no gate, but the same exact chain
		// keeps its inventory and receipts attributable.
		edge, ok, edgeErr := guardManifestEditionEdge(m)
		if edgeErr != nil {
			return summary, edgeErr
		}
		if ok {
			predecessorHistory, predecessorErr := verifyGuardEditionHistory(ctx, mdb, dia, edge.From)
			if predecessorErr == nil && guardEditionHistoryCompletesV7(predecessorHistory.Kind) {
				predecessorTables := make([]string, 0, len(edge.From.Specs))
				for _, spec := range edge.From.Specs {
					predecessorTables = append(predecessorTables, spec.Key.Relation)
				}
				if _, bridgeErr := runAppendOnlyGuardUnits(ctx, mdb, dia, predecessorTables,
					observerDSN, hardened, session); bridgeErr != nil {
					return summary, fmt.Errorf("sqlstore: complete epoch-%d predecessor before epoch-%d transition: %w",
						edge.From.CodeEpoch, m.CodeEpoch, bridgeErr)
				}
				history, err = verifyGuardEditionHistory(ctx, mdb, dia, m)
			}
		}
	}
	if err != nil {
		return summary, err
	}
	// Module editions are deliberately crossed here: core v7 runs before module tables,
	// while this seam runs after authored module migrations and before the ordinary unit
	// runner. A completed immediate predecessor is therefore evidence to begin exactly
	// one transition, not permission to skip missing module objects or an intermediate edge.
	if history.Kind == guardEditionHistoryPredecessorV7 {
		history, err = transitionGuardEditionAfterModules(ctx, mdb, dia, m, history)
		if err != nil {
			return summary, err
		}
	}

	if dia.Name() != store.EnginePostgres {
		// THE RUNNER is PostgreSQL-only — SQLite's append-only defense is its unconditional
		// trigger pair, and there is no tgenabled to adopt or transition.
		//
		// THE CONTROL PLANE IS NOT. SQLite has the same three relations and the same bootstrap
		// attribution: the metadata receipts plus the universal v7 completion (and a start on
		// direct upgrades), or the predecessor's metadata receipts plus the exact transition
		// seal. The joint history above re-reads that attribution, its inventory and its gate
		// before this boot can serve.
		if !guardEditionHistoryCompletesV7(history.Kind) {
			return summary, fmt.Errorf("%w: SQLite guard history %q has no completed core v7 witness",
				ErrGuardManifestNoEdge, history.Kind)
		}
		return summary, nil
	}
	if !guardEditionHistoryCompletesV7(history.Kind) {
		return summary, fmt.Errorf("%w: PostgreSQL guard history %q has no completed core v7 witness",
			ErrGuardManifestNoEdge, history.Kind)
	}
	// THE TYPED ASSERTION, before a single unit is built.
	//
	// retryUnit.run requires a physical *sql.Conn: everything it does depends on ONE session,
	// because the coordination lock, the session-scoped timeouts and the transaction all have
	// to be the same connection's. withMigrationLock hands the callback exactly that
	// connection, but its signature exposes it as dialect.Execer — which *sql.DB also
	// satisfies. Asserting here converts a silent pool-semantics bug into a refusal before
	// anything is attempted.
	conn, ok := mdb.(*sql.Conn)
	if !ok {
		return summary, fmt.Errorf(
			"sqlstore: append-only guard runner: the PostgreSQL migration executor is %T, want *sql.Conn; every guarantee this runner makes depends on one session holding the coordination lock", mdb)
	}
	if session == nil {
		return summary, errors.New("sqlstore: append-only guard runner: no reconcile-session provider, so a commit whose acknowledgement is lost could never be resolved")
	}
	// THE FENCE MUST BE TAKEABLE BEFORE ANYTHING DURABLE IS WRITTEN. See
	// verifyGuardFenceCapability: this is the check that turns a legacy function owner from a
	// permanent brick into a recoverable refusal.
	if err := verifyGuardFenceCapability(ctx, conn); err != nil {
		return summary, err
	}

	keys := make([]guardKey, 0, len(m.Specs))
	for _, spec := range m.Specs {
		keys = append(keys, spec.Key)
	}
	observed, err := projectGuardCatalogBatch(ctx, conn, keys)
	if err != nil {
		return summary, err
	}
	plans, refusals, err := buildGuardUnitPlans(m, observed)
	if err != nil {
		return summary, err
	}
	summary.PlannedUnits, summary.Refusals = len(plans), refusals

	// A PLAN WITH REFUSALS MAY NOT OPEN A ROLLOUT. The enumeration is the authorisation, so one
	// that omits a target the manifest declares would let every later boot skip it — see
	// openOrVerifyGuardRollout. Where a rollout already exists the refusal is still recorded
	// against it, below.
	rollout, gate, opened, err := openOrVerifyGuardRollout(ctx, mdb, dia, m, history, plans, len(refusals) == 0)
	if err != nil {
		return summary, err
	}
	summary.RolloutID, summary.Phase, summary.Condition = rollout.RolloutID, gate.Phase, gate.Condition

	// FROM HERE THE DURABLE ENUMERATION IS THE PLAN, not the fresh observation.
	//
	// The observation-derived plan above exists only to be recorded when a rollout is OPENED.
	// Once one exists, its enumeration is part of its authorisation: a later boot verifies the
	// units the rollout opened. Re-deriving them was a measured defect — on a second boot the
	// guards are at ALWAYS, so a fresh reading enumerates one adoption per target instead of the
	// adoption-plus-transition pair that actually ran, marks that adoption terminal, and the
	// matrix then declares a correct rollout divergent.
	//
	// Rebuilding UNCONDITIONALLY, including on the boot that just opened the rollout, is
	// deliberate: there the rebuild must reproduce the plan it recorded, so doing it always
	// turns the round trip into something the ordinary path exercises rather than something a
	// test has to remember to check.
	derived := plans
	plans, err = guardPlanFromEnumeration(m, gate.ExpectedUnits, gate.Units, observed)
	if err != nil {
		return summary, err
	}
	// THE ROUND TRIP IS COMPARED WHENEVER THIS BOOT IS THE ONE THAT OPENED THE ROLLOUT, and that
	// fact is now CARRIED rather than guessed at.
	//
	// The previous version inferred it from `len(derived) == len(plans)`, which inverted the
	// check exactly where it mattered: a durable enumeration one unit SHORT than the derived
	// plan is the divergence this comparison exists to catch, and unequal lengths were the
	// condition under which it did not run at all.
	if opened {
		if aerr := guardPlansAgree(derived, plans); aerr != nil {
			return summary, fmt.Errorf("sqlstore: the guard rollout's durable enumeration does not rebuild the plan it recorded: %w", aerr)
		}
	}
	summary.PlannedUnits = len(plans)

	// The control plane's OWN guards, verified from the catalog. This is what makes the
	// bootstrap receipt's constructed prestate checkable rather than self-certifying.
	if err := verifyGuardControlPlaneObjects(ctx, conn, dia, m); err != nil {
		return summary, err
	}

	receipts, err := guardRolloutReceipts(ctx, mdb, dia, rollout.RolloutID)
	if err != nil {
		return summary, err
	}

	// A rollout that already closed is VERIFY-ONLY. Re-creating an object observed to be
	// missing under `ready` would launder the sabotage instead of reporting it, so this path
	// authorizes zero DDL: it compares each terminal unit's receipt and object through the
	// same matrix and refuses if any answer is not "applied".
	if gate.Phase == gatePhaseReady {
		summary.FastPath = true
		// VERIFIED, not merely `ready`. Asking only about the phase left every other ready/*
		// combination taking this path, and the condition is the axis that says whether the
		// coordinator may act on what it sees. A rollout that closed and then recorded drift is
		// `ready/blocked`, and the refusal below is what stops it booting; anything else under
		// `ready` is a condition this engine does not produce, so it is refused rather than
		// interpreted.
		if gate.Condition != gateConditionVerified && gate.Condition != gateConditionBlocked {
			summary.Diagnosis = "READY_CONDITION_UNKNOWN"
			summary.log()
			return summary, fmt.Errorf("%w: rollout %s is closed with condition %q, which this engine never writes under %q",
				ErrGuardGateIllegalTransition, rollout.RolloutID, gate.Condition, gatePhaseReady)
		}
		// A CLOSED ROLLOUT THAT IS RECORDED AS BLOCKED REFUSES, and it refuses BEFORE the
		// objects are looked at again.
		//
		// The order is the whole point. An earlier version verified first and booted whenever
		// the objects happened to look right by now — which means a deployment whose ledger
		// records that its evidence guards were tampered with would start silently as soon as
		// somebody put the guard back. That is laundering with extra steps: the drift is the
		// event, and the object looking correct afterwards does not unmake it.
		//
		// Nothing clears this automatically. The durable event carries
		// `unblock_policy=operator` precisely so that the decision is a human's, and this
		// edition ships no repair CLI — so the operational cost is real and is stated in the
		// message rather than hidden: the deployment does not serve until somebody
		// acknowledges what the ledger recorded.
		// THE CHECKPOINT IS VERIFIED FIRST. Without it, the only thing a verified chain proved was
		// that the prefix which still existed had not been edited — so removing the LAST row of the
		// inventory or of the receipts left a shorter chain that verified perfectly. The closing
		// event recorded both heads and both counts, and comparing them is what gives those last
		// rows a successor.
		if err := verifyGuardCheckpoint(ctx, mdb, dia, rollout.RolloutID, gate); err != nil {
			summary.Diagnosis = "CHECKPOINT_MISMATCH"
			summary.log()
			return summary, err
		}
		if gate.Condition == gateConditionBlocked {
			summary.Diagnosis = gate.FirstBlocking.Code
			summary.log()
			return summary, fmt.Errorf("%w: rollout %s closed and then recorded drift (%s: %s). The objects are NOT re-verified before this refusal on purpose: the drift is the event, and a guard that looks correct again does not unmake it. Its durable diagnostic carries unblock_policy=operator, so clearing it is a deliberate human decision — inspect %s for this rollout",
				ErrGuardGateBlocked, rollout.RolloutID,
				gate.FirstBlocking.Code, gate.FirstBlocking.Details, guardGateEventsTable)
		}
		refusal, verr := verifyGuardTerminals(ctx, conn, dia, rollout, gate, plans, receipts, observed, &summary)
		if refusal != nil {
			// THE PHASE IS THE FOLDED ONE. This branch only runs on a rollout that is already
			// `ready`, so recording `pending` here wrote a row that contradicted the history it
			// was being appended to.
			if rerr := recordGuardVerificationFailure(ctx, conn, dia, rollout, refusal.UnitID, refusal.Refusal, gate.Phase); rerr != nil {
				slog.Warn("could not record a guard verification failure durably", "err", rerr)
			}
		}
		if verr != nil {
			summary.log()
			return summary, verr
		}
		summary.log()
		return summary, nil
	}

	if !gate.mayMutate() {
		summary.Diagnosis = gate.FirstBlocking.Code
		summary.log()
		return summary, fmt.Errorf("%w: rollout %s is %s/%s (%s: %s); this boot may read but not change anything",
			ErrGuardGateBlocked, rollout.RolloutID, gate.Phase, gate.Condition,
			gate.FirstBlocking.Code, gate.FirstBlocking.Details)
	}
	if len(refusals) > 0 {
		// A refusal is durable BEFORE the boot fails, so the next boot finds the same
		// diagnosis instead of re-deriving it — and so a permanent condition does not depend
		// on whoever reads the logs.
		// A refused target has no unit — it was rejected BEFORE one could be built — so the
		// adoption identity is used as the stable name for "this target, first edge". It is the
		// identity a later boot would compute for the same target, which is what makes the
		// diagnostic's deduplication key stable across restarts.
		refusedUnit, uerr := guardUnitID(m.Format, refusals[0].Key, intentAdoptLegacy)
		if uerr != nil {
			slog.Warn("could not name the unit for a guard plan refusal", "err", uerr)
		} else if rerr := recordGuardVerificationFailure(ctx, conn, dia, rollout, refusedUnit, refusals[0], gate.Phase); rerr != nil {
			slog.Warn("could not record a guard plan refusal durably", "err", rerr)
		}
		summary.Diagnosis = refusals[0].Code
		summary.log()
		return summary, fmt.Errorf("sqlstore: the append-only guard rollout cannot proceed: %d of %d targets are unusable; the first is %s",
			len(refusals), len(m.Specs), refusals[0])
	}

	// Execute, in plan order. A unit whose receipt is already durable is skipped here rather
	// than inside the runner: reaching the runner would cost a projection and a lock for a
	// question the ledger has already answered.
	for _, p := range plans {
		if done, have := receipts[receiptLookupKey(p.UnitID, guardReceiptKindUnit)]; have {
			// PRESENCE IS NOT ATTRIBUTION. Skipping on the strength of a row under the unique key
			// let a receipt whose intent, attempt or judged reading belonged to different work
			// stand in for this unit's — so the unit was never executed and the rollout closed
			// over work that never happened.
			if verr := validateGuardReceipt(p, rollout, gate.Units[p.UnitID], done); verr != nil {
				summary.Diagnosis = "RECEIPT_NOT_THIS_UNIT"
				summary.log()
				return summary, fmt.Errorf("sqlstore: refusing to skip unit %s for %s: %w",
					p.Intent, p.Spec.Key, verr)
			}
			summary.Outcomes[outcomeApplied]++
			continue
		}
		// A JUDGED UNIT WITH NO RECEIPT IS NOT A UNIT TO RUN. The judged event, the DDL and the
		// receipt share one transaction, so after any ordinary commit they exist together or not
		// at all; a judged reading standing alone is only reachable from a history somebody
		// truncated or fabricated. Executing it would REGENERATE the attribution that is missing
		// — the one outcome that turns a broken chain into a clean-looking one.
		if fold, ran := gate.Units[p.UnitID]; ran && fold.JudgedReadingValid {
			summary.Diagnosis = "JUDGED_WITHOUT_RECEIPT"
			summary.log()
			return summary, fmt.Errorf("%w: unit %s for %s has a judged reading and no receipt; those commit together, so this is a truncated history and re-running the unit would rebuild the attribution instead of reporting its absence",
				ErrGuardGateChainBroken, p.UnitID, p.Spec.Key)
		}
		predecessor := optDigest{}
		if p.Predecessor != "" {
			pred, ok := receipts[receiptLookupKey(p.Predecessor, guardReceiptKindUnit)]
			if !ok {
				return summary, fmt.Errorf("sqlstore: unit %s for %s requires the receipt of %s, which is not in the ledger; a lineage is verified, never assumed",
					p.UnitID, p.Spec.Key, p.Predecessor)
			}
			predecessor = someDigest(pred.ReceiptID)
		}
		r := &guardUnitRunner{dia: dia, conn: conn, rollout: rollout, plan: p, predecessor: predecessor, session: session}
		unit := retryUnit{
			Spec:             p.unitSpec(),
			Plan:             guardLockPlan(p.Spec.Key.Relation, p.Intent),
			ObserverDSN:      observerDSN,
			Fence:            r.fenceSharedGuardFunction,
			Project:          r.project,
			Execute:          r.execute,
			Receipt:          r.receipt,
			ProjectReceipt:   r.projectReceipt,
			ProjectObject:    r.projectObject,
			ReconcileSession: session,
			BeforeAttempt:    r.beforeAttempt,
			AfterFailure:     r.afterFailure,
		}
		budget := newMigrationUnitBudget()
		if err := unit.run(ctx, conn, budget); err != nil {
			return summary, fmt.Errorf("sqlstore: append-only guard unit %s on %s: %w", p.Intent, p.Spec.Key, err)
		}
		summary.Executed[p.Intent]++
		// The ledger is re-read so the next unit's lineage check sees this unit's receipt.
		// Carrying an in-memory belief forward instead would make the lineage a claim about
		// what this process did rather than about what the database holds.
		receipts, err = guardRolloutReceipts(ctx, mdb, dia, rollout.RolloutID)
		if err != nil {
			return summary, err
		}
	}

	// THE CLOSE HAPPENS UNDER A FENCE, in ONE transaction: the three logs held against INSERT,
	// the shared function pinned, the targets locked, EVERY authoritative reading taken after
	// that, the terminals judged, the checkpoint computed and `ready` appended, all committing
	// together. See closeGuardRolloutUnderFence for why re-reading without a lock that survives
	// to commit is not enough, and for the one topology where the strong mode is unavailable.
	refusal, err := closeGuardRolloutUnderFence(ctx, conn, dia, m, rollout, plans, keys, hardened, &summary)
	if refusal != nil {
		// Recorded AFTER the fence transaction has ended, never inside it. A durable diagnostic
		// written in the transaction that is about to roll back is a diagnostic that vanishes
		// with the thing it was diagnosing — and this connection cannot open a second
		// transaction while the first is open, so there is no "inside" that would work.
		if rerr := recordGuardVerificationFailure(ctx, conn, dia, rollout, refusal.UnitID, refusal.Refusal, gate.Phase); rerr != nil {
			slog.Warn("could not record a guard verification failure durably", "err", rerr)
		}
	}
	if err != nil {
		summary.log()
		return summary, err
	}
	summary.Phase, summary.Condition = gatePhaseReady, gateConditionVerified
	summary.log()
	return summary, nil
}

func transitionGuardEditionAfterModules(
	ctx context.Context,
	mdb dialect.Execer,
	dia dialect.Dialect,
	current guardManifest,
	history guardEditionHistory,
) (guardEditionHistory, error) {
	if current.CodeEpoch < 3 || history.Kind != guardEditionHistoryPredecessorV7 {
		return history, fmt.Errorf("%w: post-module transition needs an epoch-3-or-later target over a completed immediate predecessor, got epoch %d history %q",
			ErrGuardManifestNoEdge, current.CodeEpoch, history.Kind)
	}
	tx, err := mdb.BeginTx(ctx, nil)
	if err != nil {
		return history, fmt.Errorf("sqlstore: begin guard edition %d transition: %w", current.CodeEpoch, err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit

	var plans []guardUnitPlan
	openCurrent := dia.Name() == store.EnginePostgres
	if openCurrent {
		if err := verifyGuardFenceCapability(ctx, tx); err != nil {
			return history, err
		}
		keys := make([]guardKey, 0, len(current.Specs))
		for _, spec := range current.Specs {
			keys = append(keys, spec.Key)
		}
		observed, err := projectGuardCatalogBatch(ctx, tx, keys)
		if err != nil {
			return history, err
		}
		var refusals []guardPlanRefusal
		plans, refusals, err = buildGuardUnitPlans(current, observed)
		if err != nil {
			return history, err
		}
		if len(refusals) != 0 {
			return history, fmt.Errorf("sqlstore: guard edition post-module transition cannot open epoch %d: %d of %d targets have no authorized plan (first: %s)",
				current.CodeEpoch, len(refusals), len(current.Specs), refusals[0])
		}
	}
	if err := transitionGuardEditionInTx(ctx, tx, dia, current, history, plans, openCurrent); err != nil {
		return history, err
	}
	if err := tx.Commit(); err != nil {
		return history, fmt.Errorf("sqlstore: commit guard edition %d transition: %w", current.CodeEpoch, err)
	}
	post, err := verifyGuardEditionHistory(ctx, mdb, dia, current)
	if err != nil {
		return history, fmt.Errorf("sqlstore: verify committed guard edition %d transition: %w", current.CodeEpoch, err)
	}
	if post.Kind != guardEditionHistoryTransitioned {
		return history, fmt.Errorf("%w: committed epoch-%d transition produced history %q",
			ErrGuardManifestNoEdge, current.CodeEpoch, post.Kind)
	}
	return post, nil
}

// guardTerminalRefusal is a terminal unit the matrix refused, carried back to a caller that
// can make it durable.
//
// It is RETURNED rather than written where it is found, because the finding now happens inside
// the fence transaction — and a record written there would roll back with it.
type guardTerminalRefusal struct {
	UnitID  string
	Refusal guardPlanRefusal
}

// closeGuardRolloutUnderFence holds every object whose reading authorizes `ready` stable until
// the closing commit.
//
// THE WINDOW IT CLOSES, which the previous shape left wide open: the final bulk projection ran
// with no locks at all, the terminal verification judged that projection, and `ready` was then
// appended in a SEPARATE transaction. Any DDL landing in between — a DROP TRIGGER, an ALTER
// TABLE ... DISABLE TRIGGER, a CREATE OR REPLACE FUNCTION — meant the closing event attested a
// snapshot that had already stopped describing the database. Re-reading closer to the commit
// narrows that window; it never closes it, because nothing stops the change from landing
// inside whatever remains.
//
// So the fence is a real one, and each half was measured on PostgreSQL 15.18:
//
//   - ROW EXCLUSIVE on every target. It is the STRONGEST mode an explicit LOCK TABLE can take
//     on an append-only relation — the stronger modes need UPDATE/DELETE/TRUNCATE, which the
//     append-only ACL revokes, and ownership grants no exemption — and it is enough: its
//     conflict set contains SHARE ROW EXCLUSIVE and ACCESS EXCLUSIVE, so it blocks CREATE
//     TRIGGER, ALTER TABLE ... ENABLE/DISABLE TRIGGER, DROP TRIGGER and DROP TABLE alike.
//     Measured: a concurrent ALTER TABLE ... DISABLE TRIGGER blocked for its whole lock_timeout
//     and was canceled.
//   - The shared function, pinned by the same statement the units use. See
//     fenceSharedGuardFunction for the four mechanisms considered and why three of them fence
//     nothing that matters.
//
// The targets are locked in the plan's sorted order, which is the order every unit already
// takes them in, so this transaction cannot form a cycle with a unit of this same rollout.
func closeGuardRolloutUnderFence(
	ctx context.Context,
	conn *sql.Conn,
	dia dialect.Dialect,
	m guardManifest,
	rollout guardRolloutContext,
	plans []guardUnitPlan,
	keys []guardKey,
	hardened bool,
	summary *guardRolloutSummary,
) (*guardTerminalRefusal, error) {
	// TWO budgets, BOTH shared across retries — and "both" is the correction to a shape where
	// only the first one was.
	//
	// `lock_timeout` is documented to apply PER ACQUISITION and only while waiting for a lock, so
	// arming it once and taking N locks bounds nothing: with 49 relations to hold, a 15-second
	// ceiling is a worst case of over twelve minutes. The acquisition budget is what makes THAT
	// ceiling total, by recomputing the per-statement value from whatever is left before every
	// lock.
	//
	// THE WORK AFTER THE LOCKS HAS ITS OWN, because the two bound different things: acquisition
	// bounds WAITING for other sessions in the acquisition ORDER, where a lost race is expected.
	// It used to be created INSIDE the attempt, which handed each retry a fresh two minutes:
	// three attempts meant a work phase of six, and the retry loop consulted neither. Sharing it
	// is what makes the ceiling of a close its two budgets and not their product with the
	// attempt count — and the work phase CAN wait too, so that sharing is load-bearing rather
	// than defensive. See the workBudget call below.
	budgets := &guardCloseBudgets{
		acquire: newLockBudget(guardCloseAcquisitionBudget, time.Now, sleepCtx, jitterFloat),
	}
	if obs := guardCloseWorkSpentObserver; obs != nil {
		// ⛔ CAPTURADO EN LOCAL, NO LEIDO OTRA VEZ DENTRO DE LA CLAUSURA. La condicion y la
		// llamada ocurren en momentos distintos —el close entero pasa entre medias—, asi que
		// una clausura que releyera la global entregaria el gasto de ESTE close al observador
		// que hubiera puesto OTRO test, o haria panic si mientras tanto volvio a nil.
		//
		// Deferred so it reports on EVERY exit — the clean return, the refusal and each of the
		// error paths — instead of only the one a future edit remembers to instrument.
		defer func() { obs(budgets.workSpent()) }()
	}
	b := budgets.acquire
	for attempt := 1; ; attempt++ {
		refusal, err := attemptGuardClose(ctx, conn, dia, m, rollout, plans, keys, hardened, budgets, summary)
		if err == nil || refusal != nil {
			return refusal, err
		}
		// A LOCK-ORDER FAILURE IS RETRIED, BOUNDED; ANYTHING ELSE IS THE ANSWER.
		//
		// 40P01 and 55P03 mean the server broke a wait that this transaction was one side of —
		// an operator holding the shared function while waiting for a target forms exactly that
		// cycle, and no lock order this engine can choose prevents a participant that takes the
		// two in the opposite order. They are availability, not integrity: the transaction rolled
		// back whole, no `ready` exists, and the receipts of the units that ran are untouched.
		if attempt >= guardCloseMaxAttempts || !guardCloseRetryable(err) {
			return nil, err
		}
		slog.Warn("the guard rollout's close lost a lock race and will be retried",
			"rollout", rollout.RolloutID, "attempt", attempt, "sqlstate", sqlStateOf(err), "err", err)
		waited, berr := b.backoff(ctx, attempt, guardCloseBackoffBase, guardCloseBackoffMax)
		if berr != nil {
			return nil, berr
		}
		if !waited {
			// The budget is spent. Returning the ORIGINAL error rather than a budget message
			// keeps the diagnosis on what actually went wrong.
			return nil, err
		}
	}
}

// attemptGuardClose is one transaction of the close.
//
// THE ORDER IS THE ONE EVERY PARTICIPANT USES: the three control-plane relations first, in the
// exported order, then the shared trigger function, then the targets. It is the SAME order
// retryUnit.Acquire takes — metadata prefix, Fence, target — and that is the point: a global
// order is only a defense against cycles if the close obeys it too. The previous shape took
// targets, then the function, then metadata implicitly as it wrote, which is the exact inverse
// and could cycle with a unit of another node the moment the advisory lock was not the only
// serialiser (an operator, a repair script, a psql session).
func attemptGuardClose(
	ctx context.Context,
	conn *sql.Conn,
	dia dialect.Dialect,
	m guardManifest,
	rollout guardRolloutContext,
	plans []guardUnitPlan,
	keys []guardKey,
	hardened bool,
	budgets *guardCloseBudgets,
	summary *guardRolloutSummary,
) (_ *guardTerminalRefusal, err error) {
	b := budgets.acquire
	// THE TRANSACTION IS BORN UNDER A DEADLINE OF ITS OWN, and that is not the same statement as
	// "every statement is bounded". See guardCloseTxClock: database/sql binds a transaction's
	// LIFETIME — statements, rollback and commit — to the context handed to BeginTx, so one begun
	// under the caller's deadline-free boot context had a commit no client clock could reach. HOW
	// that binding stops a commit is a race between the watchdog's rollback and Commit seeing the
	// cancellation; this package depends on the outcome, not on which of the two wins.
	ctx, clock := newGuardCloseTxClock(ctx, b.remaining(), errGuardCloseAcquisitionDeadline)
	defer clock.stop()
	// Every failure below leaves through here, so a statement that died because THIS clock fired
	// says so instead of surfacing as an anonymous `context canceled` from whichever roundtrip
	// happened to notice first.
	defer func() { err = clock.attribute(ctx, err) }()
	tx, berr := conn.BeginTx(ctx, nil)
	if berr != nil {
		return nil, fmt.Errorf("sqlstore: close the guard rollout: begin: %w", berr)
	}
	defer func() { _ = tx.Rollback() }()

	acquire := func(what, stmt string) error {
		if aerr := armGuardCloseAcquisition(ctx, tx, b, what); aerr != nil {
			return aerr
		}
		if _, eerr := tx.ExecContext(ctx, stmt); eerr != nil {
			return fmt.Errorf("sqlstore: close the guard rollout: stabilize %s: %w", what, eerr)
		}
		return nil
	}

	// 1. THE THREE LOGS, AT A MODE THAT CONFLICTS WITH INSERT WHERE THAT IS POSSIBLE.
	for _, l := range guardCloseMetadataLocks(guardCloseMetadataMode(hardened)) {
		if err := acquire(l.displayRelation(), l.lockStatement()); err != nil {
			return nil, err
		}
	}
	// 2. THE SHARED FUNCTION.
	fn := canonicalGuardDefinition().Function
	if err := acquire(fn.Schema+"."+fn.Name+"()", fenceSharedFunctionStatement(fn.Schema, fn.Name)); err != nil {
		return nil, err
	}
	// 3. THE TARGETS.
	for _, l := range guardCloseLocks(keys) {
		if err := acquire(l.displayRelation(), l.lockStatement()); err != nil {
			return nil, err
		}
	}
	// THE WORK PHASE RE-ARMS THAT DEADLINE — a WALL-CLOCK one, not a per-statement one.
	//
	// `statement_timeout` is documented to apply PER STATEMENT
	// (https://www.postgresql.org/docs/16/runtime-config-client.html), so arming it once at 60s
	// and then issuing a projection, two folds, a receipt read, a checkpoint and an append
	// bounds each of them at 60s and their SUM at nothing. Worse for the last step: PostgreSQL
	// DISARMS statement_timeout before committing, so the commit is bounded by neither.
	//
	// Re-arming the TRANSACTION's clock, rather than deriving a second context for the statements,
	// is what closes that: the deadline now lives on the context BeginTx was given, which is the
	// context the transaction's LIFETIME is bound to. It does NOT interrupt a commit already in
	// flight — see guardCloseTxClock for what actually stops one. The budget is shared across
	// retries, so a lock race that sends this attempt back to the loop does not hand the next one
	// a fresh two minutes.
	//
	// BOTH PROPERTIES NOW HAVE A DISCRIMINATING REGRESSION, which round five was right that they
	// did not. TestPostgresTheCloseCommitIsBoundToTheClockItsTransactionBeganUnder reddens when
	// BeginTx is unbound from this clock, and TestPostgresTheAcquisitionBudgetIsTotalAcrossThe
	// CloseRetries reddens when each attempt is handed a fresh budget. The second one is what
	// found the 57014 that made this loop unreachable — see armGuardCloseAcquisition.
	//
	// THE WORK BUDGET'S SHARING IS MEASURED TOO, and the reason it was once called unmeasurable
	// was FALSE. That reason read: "everything after this point reads or writes relations this
	// transaction already holds locks on, so nothing waits". Two things are wrong with it.
	//
	// The table lock this close can take on the gate in the single-role topology is ROW
	// EXCLUSIVE — the stronger modes need privileges the append-only reconcile revokes — and ROW
	// EXCLUSIVE DOES NOT CONFLICT WITH ITSELF, so another session's INSERT is not excluded at
	// all. And holding a compatible table lock says nothing about row level: two inserts aiming
	// at the same key of a unique index wait on each other regardless.
	//
	// The `ready` append below is where that lands, and it is the LAST statement of the phase —
	// after the projection, both folds, the census and the checkpoint have each spent budget.
	// TestPostgresTheWorkBudgetIsTotalAcrossTheCloseRetries drives exactly that with an
	// uncommitted squatter on the ordinal: production spends 951ms of a 900ms budget over two
	// attempts, and a budget rebuilt per attempt spends 2.23s over three.
	work := budgets.workBudget()
	if work.expired() {
		return nil, fmt.Errorf("%w: the guard rollout's close ran out of work budget before it could read anything under the fence",
			ErrMigrationLockBudgetExceeded)
	}
	clock.rearm(work.remaining(), errGuardCloseWorkDeadline)
	if _, err := setLocalTimeouts(ctx, tx, guardCloseLockTimeout, guardCloseWorkTimeout); err != nil {
		return nil, fmt.Errorf("sqlstore: close the guard rollout: arm the work timeout: %w", err)
	}

	// THE CONTROL PLANE'S OWN OBJECTS, RE-VERIFIED UNDER THE LOCKS.
	//
	// It is checked once before the units run, and that reading is not enough on its own: an
	// owner can commit `ALTER TABLE … DISABLE TRIGGER` after it and before this transaction
	// takes its locks, and the close would then interpret three logs whose immutability guard is
	// gone — while its own comment claimed every authoritative reading was fenced. Repeating it
	// here, after the metadata prefix is held, is what makes that sentence true.
	if err := verifyGuardControlPlaneObjects(ctx, tx, dia, m); err != nil {
		return nil, err
	}
	// FROM HERE EVERY AUTHORITATIVE READING IS TAKEN UNDER THE FENCE — and "every" now means
	// every one, which is what the previous shape got wrong. It locked the targets and then
	// verified against a receipt map read BEFORE the transaction began, never re-folded the
	// inventory, and computed the checkpoint over whatever the last statement could see. Under
	// READ COMMITTED each statement sees commits that landed since the previous one, so a
	// receipt, an inventory event or a gate event inserted in any of those gaps could enter the
	// checkpoint while escaping the verification that was supposed to authorize it.
	fresh, err := projectGuardCatalogBatch(ctx, tx, keys)
	if err != nil {
		return nil, err
	}
	// THE INVENTORY IS RE-FOLDED, semantically, against the manifest, and its retained pair must
	// still be the one this rollout's identity was derived from. An activation appended after
	// the rollout opened would otherwise be attested by the checkpoint without ever having been
	// interpreted.
	revision, retained, err := verifyInventoryChain(ctx, tx, dia, m)
	if err != nil {
		return nil, err
	}
	if revision != rollout.RetainedRevision || retained != rollout.RetainedSHA256 {
		return nil, fmt.Errorf("%w: rollout %s was opened over inventory revision %d/%s and the inventory now folds to %d/%s",
			ErrGuardGateChainBroken, rollout.RolloutID, rollout.RetainedRevision,
			hexDigest(rollout.RetainedSHA256), revision, hexDigest(retained))
	}
	// AND THE GATE IS RE-FOLDED, which is not symmetry for its own sake. The judged readings
	// the matrix needs were appended by the units that just ran, so the projection captured
	// before they ran holds none of them — verifying against it reported every terminal as
	// having "a receipt but no judged reading", which is true of that stale fold and false of
	// the database.
	gate, err := foldGateEvents(ctx, tx, dia, rollout.RolloutID)
	if err != nil {
		return nil, err
	}
	if gate.Phase != gatePhasePending || !gate.mayMutate() {
		return nil, fmt.Errorf("%w: rollout %s folds to %s/%s under the closing fence, so the reading that authorized this close no longer holds",
			ErrGuardGateIllegalTransition, rollout.RolloutID, gate.Phase, gate.Condition)
	}
	// AND THE RECEIPTS ARE RE-READ, under the locks, as the exact set the plan allows.
	receipts, err := guardRolloutReceipts(ctx, tx, dia, rollout.RolloutID)
	if err != nil {
		return nil, err
	}
	if err := verifyGuardReceiptCensus(rollout, plans, receipts); err != nil {
		summary.Diagnosis = "RECEIPT_CENSUS"
		return nil, err
	}
	refusal, verr := verifyGuardTerminals(ctx, tx, dia, rollout, gate, plans, receipts, fresh, summary)
	if verr != nil {
		return refusal, verr
	}
	// The checkpoint is computed in this same transaction, so the heads it records are the
	// heads at the instant of closure — and now also under the fence, so nothing it attests can
	// have arrived after the fold that interpreted it.
	checkpoint, err := computeGuardCheckpoint(ctx, tx, dia, rollout.RolloutID)
	if err != nil {
		return nil, err
	}
	if _, err := appendGateEvent(ctx, tx, dia, gateEvent{
		RolloutID:         rollout.RolloutID,
		Kind:              gateEventReady,
		Format:            rollout.Format,
		CodeEpoch:         rollout.CodeEpoch,
		CodeSHA256:        rollout.CodeSHA256,
		RetainedRevision:  rollout.RetainedRevision,
		RetainedSHA256:    rollout.RetainedSHA256,
		Phase:             gatePhaseReady,
		Condition:         gateConditionVerified,
		ExpectedUnits:     guardPlanUnitIDs(plans),
		Checkpoint:        checkpoint,
		CheckpointPresent: true,
	}); err != nil {
		return nil, err
	}
	// THE ONE INSTANT AT WHICH THE TRANSACTION'S BINDING IS OBSERVABLE. See
	// guardCloseBeforeCommitTestHook for why the seam is here and nowhere else.
	if hook := guardCloseBeforeCommitTestHook; hook != nil {
		hook(ctx, clock.fire)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("sqlstore: close the guard rollout: commit: %w", err)
	}
	return nil, nil
}

// guardCloseBeforeCommitTestHook runs immediately before the close commits, with the
// transaction's own context and a function that expires its clock.
//
// WHY A SEAM EXISTS HERE AT ALL, because a test-only hook in production code needs a reason
// better than convenience. Every statement of this close is issued with `ctx` passed
// explicitly, so changing what BeginTx is given changes nothing any statement can observe:
// round five verified exactly that by replacing `conn.BeginTx(ctx, nil)` with
// `conn.BeginTx(context.WithoutCancel(ctx), nil)` and finding every budget test still green.
//
// Tx.Commit is the only operation in the transaction that takes no context of its own — it
// consults the one the transaction BEGAN under. So the binding this fix rests on is observable
// at precisely one instant: after the last statement, before the commit. A regression that
// cannot reach that instant cannot tell a bound transaction from an unbound one, and the
// property would ship on the strength of a code reading.
//
// It is nil in production, read once into a local so a concurrent write cannot make the check
// and the call disagree, and takes the expiry function rather than the clock so a test never
// depends on the clock's shape.
var guardCloseBeforeCommitTestHook func(ctx context.Context, expire func())

// armGuardCloseAcquisition recomputes the per-statement ceiling from what is left of the total
// budget, immediately before the statement it governs.
//
// It is armAcquisition's discipline for the close: `lock_timeout` restarts on every acquisition,
// so a value armed once is a per-statement ceiling and never a total. Re-arming from the
// remaining budget is what turns N per-statement ceilings back into one deadline.
//
// THE TWO CEILINGS ARE NOT THE SAME NUMBER, and making them the same is what made the close's
// whole retry loop unreachable. Measured, on a real close queued behind an external holder of the
// shared function:
//
//	setLocalTimeouts(actx, tx, d, d)  ->  ERROR: canceling statement due to statement timeout
//	                                      (SQLSTATE 57014), 0 retries
//
// `statement_timeout` runs from the START OF THE STATEMENT and `lock_timeout` from the moment it
// begins WAITING for the lock, which is microseconds later. Armed at the same value the statement
// ceiling therefore always wins, and 57014 is not in guardCloseRetryable — deliberately, because
// it means "this transaction is over budget", not "the server broke a wait". So every lock
// contention the loop exists to survive was arriving as a terminal error instead, and the
// comments promising a bounded retry described code that could not retry.
//
// The slack makes the lock ceiling the one that fires while leaving the statement ceiling as the
// runaway backstop it was meant to be. It may exceed the remaining budget, and that is harmless:
// the transaction's own clock is the total, and it is armed from the same budget.
func armGuardCloseAcquisition(ctx context.Context, tx *sql.Tx, b *lockBudget, what string) error {
	d, ok := b.clampPositive(guardCloseLockTimeout)
	if !ok {
		return fmt.Errorf("%w: the guard rollout's close ran out of acquisition budget before %s",
			ErrMigrationLockBudgetExceeded, what)
	}
	actx, cancel := b.context(ctx)
	_, err := setLocalTimeouts(actx, tx, d, d+guardCloseAcquisitionStatementSlack)
	cancel()
	if err != nil {
		return fmt.Errorf("sqlstore: close the guard rollout: arm the timeouts before %s: %w", what, err)
	}
	// Re-checked AFTER arming, because arming is itself two roundtrips: if they consumed the
	// remainder, the statement they were supposed to bound must not be issued at all.
	if b.expired() {
		return fmt.Errorf("%w: the guard rollout's close spent its budget arming the timeouts for %s",
			ErrMigrationLockBudgetExceeded, what)
	}
	return nil
}

// guardCloseBudgets are the two ceilings of one close, both SHARED ACROSS ITS RETRIES.
//
// The acquisition budget starts when the close does. The work budget is started LAZILY, by the
// first attempt that actually gets its locks, and that is deliberate rather than incidental: a
// budget created alongside the other one would be consumed by the waiting the other one exists
// to bound, and a close that spent two minutes queueing for locks would then have no time left
// to do the work it just acquired them for. Started at the first work phase and shared from
// there, the ceiling of the whole close is the SUM of the two, retries included.
type guardCloseBudgets struct {
	acquire *lockBudget
	work    *lockBudget
	// prev is the last work budget CREATED, and unlike `work` nothing ever clears it.
	//
	// ⛔ LA CONTABILIDAD NO PUEDE COLGAR DEL CAMPO QUE LA MUTACION TOCA. Quien cambie a un
	// presupuesto por intento escribe `budgets.work = nil` en el bucle y nada mas; si lo unico
	// que se lee es `work`, se lee el gasto del ULTIMO intento —siempre por debajo de uno— y el
	// techo deja de morder. Medido contra Postgres el 2026-08-24 con esa version:
	//
	//	mutante realista   work_spent=715ms   elapsed=2.415s   ->  PASS   (SOBREVIVIA)
	//	techo viejo (reloj de pared)          2,415 s > 1,8 s  ->  MUERTO
	//
	// O sea que el arreglo apagaba el unico control que cazaba el defecto. `prev` no forma parte
	// de conceder presupuesto, asi que esconder el gasto exigiria ADEMAS limpiarlo a proposito.
	prev *lockBudget
	// workFrozen is the consumption of every REPLACED work budget, snapshotted at the instant
	// it was replaced.
	//
	// ⛔ CONGELADO Y NO RECALCULADO, y esto tampoco lo vi hasta que lo tumbo su propio control:
	// `remaining()` compara contra el reloj VIVO, asi que un presupuesto abandonado sigue
	// ENVEJECIENDO despues de que su intento terminara. Sumar `remaining()` sobre todos daba
	// 3 min donde el gasto real eran 2 — un numero inflado, que es justo lo que acabo de
	// publicar mal una vez. Se congela donde se sustituye, que es el unico instante en que el
	// gasto de ese presupuesto es un hecho y no una lectura movil.
	workFrozen time.Duration
	// workCreated counts how many work budgets this close built. Uno en la implementacion
	// compartida; uno por intento en la que el techo existe para cazar.
	workCreated int
	now         func() time.Time
}

// clock is the source `workBudget` builds its budgets against: the injected one when a test
// supplies it, the real one otherwise.
func (g *guardCloseBudgets) clock() func() time.Time {
	if g.now != nil {
		return g.now
	}
	return time.Now
}

// workBudget returns the shared work budget, starting it on first use.
func (g *guardCloseBudgets) workBudget() *lockBudget {
	if g.work == nil {
		// La contabilidad se cierra DONDE SE CREA EL SIGUIENTE, que es el unico sitio por el que
		// no se puede pasar de largo: para tener un presupuesto por intento hay que venir por
		// aqui. Y es tambien el instante exacto en que el gasto del anterior deja de moverse.
		if g.prev != nil {
			g.workFrozen += guardCloseWorkBudget - g.prev.remaining()
		}
		g.work = newLockBudget(guardCloseWorkBudget, g.clock(), sleepCtx, jitterFloat)
		g.prev = g.work
		g.workCreated++
	}
	return g.work
}

// workSpent reports how much work budget this close consumed IN TOTAL — the frozen consumption
// of every replaced budget plus the live one's. Zero when the work phase was never reached,
// which is the honest answer rather than a made-up one.
//
// ⛔ SOBRE `prev` Y NO SOBRE `work`: son el mismo puntero mientras el presupuesto vive, y se
// separan exactamente en el caso que esta medida existe para ver — el intento que suelta el
// suyo y termina sin pedir otro.
//
// ⛔ Y CADA SUMANDO SATURA EN SU PROPIO PRESUPUESTO: `remaining()` (migrationunit.go) devuelve
// cero cuando el plazo ya paso, nunca un negativo. Un presupuesto agotado aporta exactamente
// `guardCloseWorkBudget` por muy cargada que este la caja, asi que la cifra la mueve el NUMERO
// de presupuestos y no la maquina. Medido: compartido 900 ms clavados en cinco corridas.
//
// It is a pure read: `remaining()` compares a stored deadline against the budget's own clock
// and returns a value; it mutates nothing and takes no lock.
func (g *guardCloseBudgets) workSpent() time.Duration {
	if g.prev == nil {
		return g.workFrozen
	}
	return g.workFrozen + (guardCloseWorkBudget - g.prev.remaining())
}

// guardCloseWorkSpentObserver, when non-nil, is handed the shared work budget's consumption
// once the close has finished. It exists for ONE assertion and the reason is worth writing
// down, because the assertion it replaces had looked correct since 2026-07-31 (c193fda04,
// 759435f3a) — twenty-four days, not the "months" I first wrote without measuring it.
//
// ⛔ EL TECHO DE ESE TEST MEDIA EL RELOJ DE PARED DE `Open`, NO EL PRESUPUESTO.
// `TestPostgresTheWorkBudgetIsTotalAcrossTheCloseRetries` cronometraba `Open(...)` entero
// —montaje del store, migraciones, adquisicion de locks Y el trabajo— y lo comparaba contra
// `2 * guardCloseWorkBudget`, que se deriva de UNO SOLO de los dos presupuestos. Medido el
// 2026-08-24 con Postgres: en caja tranquila gasta 1,101-1,124 s (sigma 20 ms) de un techo de
// 1,8 s, o sea 38 % de margen; en CI, con tres corridas sobre ocho runners, se paso dos veces
// en SHAs distintos — 1,844 s (+2,4 %) y 2,085 s (+15,8 %) — y `control-plane` es un contexto
// REQUERIDO. Lo que se comia el margen no era el sujeto: era todo lo demas.
//
// Con el gasto a la vista, la separacion que el techo tiene que resolver es la que el propio
// diseño documenta tres funciones mas arriba: 951 ms compartido contra 2,23 s por intento.
// Eso es 1,28 s de margen en vez de 0,69 s, y sin la carga de la caja dentro de la cuenta.
//
// ⛔ Y ES OBSERVACION PURA, que es la condicion bajo la que entra:
//
//	· nil por defecto, asi que en produccion ni siquiera se instala el defer;
//	· se llama DESPUES de que el close haya decidido su valor de retorno;
//	· solo lee, y lo que lee no muta nada.
//
// Un instrumento que altera lo que mide es la forma mas cara de equivocarse, y esta costura
// no puede hacerlo por construccion, no por promesa.
var guardCloseWorkSpentObserver func(time.Duration)

// guardCloseTxClock is the deadline the close's TRANSACTION lives under, re-armed when the
// close stops waiting for locks and starts doing work.
//
// It exists because of a property of database/sql that no amount of statement-level care
// substitutes for: a transaction — its statements, its COMMIT and its rollback — is governed by
// the context handed to BeginTx and by nothing else. The previous shape began the transaction
// under the caller's context, which on a boot has no deadline at all, and then bounded the
// statements after the locks with a SECOND context. That bounded every statement and left the
// commit under no client deadline whatsoever.
//
// The server does not cover it either, and that is MEASURED rather than assumed: PostgreSQL
// disarms statement_timeout before committing (finish_xact_command calls
// disable_statement_timeout before CommitTransactionCommand). A COMMIT with two seconds of
// deferred trigger work ran 2.002s under a 250ms statement_timeout and left a durable row.
//
// So the deadline has to be on the context BeginTx is given, and it has to CHANGE: the
// acquisition phase may legitimately wait minutes for other sessions, the work phase may not. A
// context's deadline cannot be moved, so the transaction gets a cancellable context and a timer
// that is re-armed at the phase change.
//
// HOW THE COMMIT IS ACTUALLY STOPPED, measured rather than inferred: database/sql runs a
// watchdog goroutine per transaction that rolls back the moment the BeginTx context is done. So
// a close whose phase expires before it commits may find the transaction ALREADY finished, and
// Commit then returns `sql: transaction has already been committed or rolled back`; it may also
// see the canceled context first and return that. WHICH of the two surfaces is a race, and this
// package depends on neither — measured both ways, and
// TestPostgresTheCloseCommitIsBoundToTheClockItsTransactionBeganUnder observed
// `commit: context canceled`. What holds either way is the OUTCOME: no commit, no durable row.
//
// WHAT IT DOES NOT DO, stated because the honest guarantee is narrower than "the commit is
// bounded": none of this interrupts a COMMIT already in flight. Once Tx.Commit has marked the
// transaction done and issued it, the watchdog's rollback finds nothing to do, and closing the
// socket underneath does not stop the server from finishing either (measured separately, on
// sql.Conn.Raw). What this guarantees is that the close cannot ENTER a commit after its phase
// expired, and that every statement before it is bounded by the same clock.
// THE CAUSE BELONGS TO EACH SCHEDULED CALLBACK, not to a slot both phases share, and that is a
// correction rather than a style. The previous shape kept one mutable `cause` field that fire()
// read WHEN IT GOT THE MUTEX, and re-armed by Reset-ing the same timer. `AfterFunc` having
// expired does not mean its callback has run: an acquisition callback could be waiting on the
// lock while rearm swapped the cause underneath it, and then cancel with the WORK cause. The
// round-five reproduction forced exactly that ordering and got the wrong attribution 10 times
// out of 10:
//
//	expired acquisition callback recorded cause=… exceeded its work budget
//
// It resurrects nothing and widens no budget — the deadline still fires when it should — but it
// files the incident under the wrong phase, and telling a slow server from an over-subscribed
// one is the only reason the two causes exist. Each timer now closes over the cause it was armed
// with, so a callback cannot observe a phase change that happened after it was scheduled.
type guardCloseTxClock struct {
	mu sync.Mutex
	// current is the cause of the phase in progress, for a caller that expires the clock by
	// hand. The TIMER never reads it.
	current error
	cancel  context.CancelCauseFunc
	timer   *time.Timer
}

// newGuardCloseTxClock derives the transaction's context and arms it for its first phase.
func newGuardCloseTxClock(parent context.Context, d time.Duration, cause error) (context.Context, *guardCloseTxClock) {
	ctx, cancel := context.WithCancelCause(parent)
	c := &guardCloseTxClock{cancel: cancel}
	c.arm(d, cause)
	return ctx, c
}

// arm replaces the timer with one that carries its own cause.
//
// Stopping the old one rather than Reset-ing it is the point: Reset reuses the callback that is
// already scheduled, which is how the cause could outlive the phase that set it.
func (c *guardCloseTxClock) arm(d time.Duration, cause error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.timer != nil {
		c.timer.Stop()
	}
	c.current = cause
	c.timer = time.AfterFunc(d, func() { c.cancel(cause) })
}

// fire expires the clock immediately, attributing it to the phase in progress.
func (c *guardCloseTxClock) fire() {
	c.mu.Lock()
	cause := c.current
	c.mu.Unlock()
	c.cancel(cause)
}

// rearm re-programs the deadline for the phase the close is entering.
//
// A phase that already ran out is NOT resurrected by this: context.WithCancelCause keeps the
// FIRST cause, so a context already canceled by the acquisition timer stays attributed to
// acquisition however many times a later timer cancels it again.
func (c *guardCloseTxClock) rearm(d time.Duration, cause error) { c.arm(d, cause) }

// stop releases the timer and the context once the transaction is finished either way.
func (c *guardCloseTxClock) stop() {
	c.mu.Lock()
	if c.timer != nil {
		c.timer.Stop()
	}
	c.mu.Unlock()
	c.cancel(context.Canceled)
}

// attribute names the phase that ran out, when that is what actually happened.
//
// WITHOUT IT THE DEADLINE IS INVISIBLE IN THE DIAGNOSIS, and that was measured on the first
// version of this fix: a close whose work phase expired failed with
// `read pg_constraint: context canceled`, which tells an operator that something canceled the
// boot and not that the close spent its own budget. Those are different incidents with
// different remedies — one is an operator or a shutdown, the other is a server too slow or a
// rollout too large — and a message that cannot tell them apart sends the reader looking for
// the wrong thing.
//
// A context canceled by the CALLER keeps the caller's error untouched: the cause is only
// substituted when it is one of this clock's own.
func (c *guardCloseTxClock) attribute(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	cause := context.Cause(ctx)
	if !errors.Is(cause, errGuardCloseAcquisitionDeadline) && !errors.Is(cause, errGuardCloseWorkDeadline) {
		return err
	}
	return fmt.Errorf("%w: %v", cause, err)
}

var (
	// errGuardCloseAcquisitionDeadline and errGuardCloseWorkDeadline are the CAUSES the
	// transaction's context carries, so a failure names the phase that ran out rather than
	// presenting as an opaque "context canceled" from whichever statement noticed first.
	errGuardCloseAcquisitionDeadline = errors.New("sqlstore: the guard rollout's close exceeded its lock-acquisition budget")
	errGuardCloseWorkDeadline        = errors.New("sqlstore: the guard rollout's close exceeded its work budget")
)

const (
	// guardCloseWorkTimeout bounds ONE statement of the work phase, as a runaway ceiling. It is
	// deliberately far above guardCloseLockTimeout, because the work phase CAN block on another
	// session — the `ready` insert queues behind an uncommitted row at the same ordinal — and the
	// lock ceiling has to be the one that fires there. Because `statement_timeout` is per
	// statement, neither of them is the ceiling of the PHASE; the shared work budget is.
	guardCloseWorkTimeout = 60 * time.Second
	// guardCloseMaxAttempts bounds the retries of a close that lost a lock race.
	guardCloseMaxAttempts  = 3
	guardCloseBackoffBase  = 50 * time.Millisecond
	guardCloseBackoffMax   = 2 * time.Second
	guardCloseDeadlock     = "40P01"
	guardCloseLockNotAvail = "55P03"
	guardCloseSerializable = "40001"
	// guardCloseAcquisitionStatementSlack is how much longer than the lock ceiling the statement
	// ceiling is armed for, so a wait ends in 55P03 rather than 57014. See
	// armGuardCloseAcquisition for where it is applied.
	//
	// IT IS APPLIED IN TWO PLACES, and for a while it was applied in one. retryUnit's
	// armAcquisition (migrationretry.go) armed the two ceilings EQUAL — the same defect this
	// constant exists to fix, on the path the close is only the last step of — and a header in
	// guardcallerwiring_pg_test.go asserted that the close was "the only place that armed the
	// two equal", which was false when written. Both sites use THIS constant rather than one
	// each: two constants that must agree with nothing checking that they do is how a fix ends
	// up landing on one of them.
	//
	// WHAT IT GUARANTEES IS NARROWER THAN "the lock ceiling always fires first", and the earlier
	// wording here claimed the wide version. A statement's clock starts before its lock wait
	// does, and the distance between them is whatever the statement does FIRST — parsing,
	// planning, and any earlier lock it takes in the same statement. The slack covers that gap
	// only while it stays under a quarter second.
	//
	// AND THAT IS AN ASSUMPTION ABOUT THESE STATEMENTS, NOT A MEASURED BOUND ON THEM. The
	// statements this close issues take one relation each and plan almost nothing, so the gap
	// SHOULD be small; nothing in the code bounds it, and a host that descheduled the backend
	// between statement start and lock wait would blow through a quarter second without any of
	// this being wrong. The earlier wording asserted the gap as a fact and it was never
	// measured — the honest reading is: chosen to cover the expected gap, with 57014 instead of
	// 55P03 as the visible consequence when it does not, which the retry classifier already
	// treats as permanent rather than silently retrying.
	//
	// The total is bounded regardless: the transaction's own clock is armed from the remaining
	// budget, so a slack that overshoots buys no extra time, only a different error.
	guardCloseAcquisitionStatementSlack = 250 * time.Millisecond
)

// guardCloseLockTimeout is the per-acquisition ceiling the close asks for; the budget clamps it
// down to whatever is actually left.
//
// It is a var only so a regression can shorten it and prove the lock is taken, instead of
// waiting out the production value to learn the same thing.
var guardCloseLockTimeout = 15 * time.Second

// guardCloseAcquisitionBudget bounds the WAITING of a whole close, retries included.
//
// guardCloseWorkBudget bounds everything after the last lock is held — projection, folds,
// receipt read, checkpoint, the `ready` append and the commit — also across retries.
//
// Both are vars for the same reason guardCloseLockTimeout is: a regression proves a deadline
// exists by making it expire, and waiting out three real minutes to learn that is not a test.
var (
	guardCloseAcquisitionBudget = 3 * time.Minute
	guardCloseWorkBudget        = 2 * time.Minute
)

// guardCloseRetryable reports whether the server broke a wait this transaction was part of.
func guardCloseRetryable(err error) bool {
	switch sqlStateOf(err) {
	case guardCloseDeadlock, guardCloseLockNotAvail, guardCloseSerializable:
		return true
	default:
		return false
	}
}

// guardCloseMetadataMode is the mode the close takes on the three logs, and it is decided by
// the TOPOLOGY because the topology decides what PostgreSQL will grant.
//
// Both halves were measured on 15.18:
//
//   - SPLIT (an owner role distinct from the application role): the owner has not revoked its
//     own UPDATE/DELETE/TRUNCATE, so `LOCK TABLE ... IN SHARE MODE` succeeds; the same
//     transaction can still INSERT its own `ready` — a transaction never conflicts with itself —
//     and a concurrent INSERT from the application role blocks until it is released. That is a
//     real fence over the three logs: nothing can be appended between the fold that interprets
//     them and the checkpoint that attests them.
//   - SINGLE ROLE: the application role owns these relations AND the append-only ACL revokes
//     UPDATE/DELETE/TRUNCATE from it, so SHARE is refused — measured, "permission denied for
//     table" — and ROW EXCLUSIVE is the strongest mode an explicit LOCK TABLE can take.
//     Ownership grants no exemption.
//
// SO THE LIMIT IS STATED RATHER THAN HIDDEN: under a single role this lock orders the close
// against DDL on the same relations, and it does NOT stop a concurrent INSERT. It cannot; there
// is no mode available that would. That is the same limit reconcileGuardMetadataACL already
// declares for that topology — the engine's writer and the application's are one role — and the
// remedy is the same one: --owner-dsn.
func guardCloseMetadataMode(hardened bool) lockMode {
	if hardened {
		return lockModeShare
	}
	return lockModeRowExclusive
}

// guardCloseMetadataLocks renders the three control-plane relations in the exported order.
func guardCloseMetadataLocks(mode lockMode) []plannedLock {
	out := make([]plannedLock, 0, 3)
	for _, t := range dialect.GuardControlPlaneTables() {
		out = append(out, plannedLock{Schema: guardSchema, Name: t, Mode: mode})
	}
	sortPlannedLocks(out)
	return out
}

// guardCloseLocks renders the target locks in the deterministic order the units use.
func guardCloseLocks(keys []guardKey) []plannedLock {
	out := make([]plannedLock, 0, len(keys))
	for _, k := range keys {
		out = append(out, plannedLock{Schema: k.Schema, Name: k.Relation, Mode: lockModeRowExclusive})
	}
	sortPlannedLocks(out)
	return out
}

// verifyGuardReceiptCensus refuses a rollout whose ledger holds a receipt the plan does not
// authorize, or is missing one it does.
//
// PRESENCE PER UNIT WAS NEVER THE WHOLE QUESTION. Every other check walks the PLAN and asks the
// ledger about each entry, so a receipt filed under this rollout for a unit the plan never
// enumerates was read by nothing, verified by nothing — and counted by the checkpoint, which is
// what made it permanent. The census walks the LEDGER instead and requires the two sets to be
// equal: any bootstrap evidence belonging to this rollout (the three metadata receipts and
// the exact v7 completion/start or edition-transition witness admitted by global history),
// and exactly one unit receipt per enumerated unit.
func verifyGuardReceiptCensus(rollout guardRolloutContext, plans []guardUnitPlan, receipts map[string]guardReceipt) error {
	allowed := make(map[string]bool, len(plans)+len(dialect.GuardControlPlaneTables()))
	for _, p := range plans {
		allowed[receiptLookupKey(p.UnitID, guardReceiptKindUnit)] = true
	}
	// THE METADATA BOOTSTRAP RECEIPTS BELONG HERE TOO, when the migration's derived rollout id and
	// this one coincide — which they do on the ordinary path, because both are computed from
	// (format, epoch, code digest, retained pair) and the bootstrap runs at revision 0 over the
	// empty retained digest. They are ALLOWED rather than REQUIRED: a rollout opened over a
	// later retained pair is a different id, and its ledger legitimately holds none of them.
	// verifyGuardControlPlaneObjects is what requires them, from the whole table and by content.
	metaSpecs, err := guardMetadataSpecs(rollout.Format)
	if err != nil {
		return err
	}
	if len(metaSpecs) == 0 {
		return fmt.Errorf("%w: guard metadata census is empty for format %d",
			ErrGuardGateChainBroken, rollout.Format)
	}
	bootstrapKeys := make(map[string]bool, len(metaSpecs))
	for _, spec := range metaSpecs {
		id, ierr := guardBootstrapUnitID(rollout.Format, spec.Key)
		if ierr != nil {
			return ierr
		}
		k := receiptLookupKey(id, guardReceiptKindBootstrap)
		allowed[k] = true
		bootstrapKeys[k] = true
	}
	// Core v7 adds one completion witness to every current bootstrap and, on a
	// direct <=v5 upgrade, one start witness before it. Their presence is decided
	// by the global exact-history verifier above; the rollout census must merely
	// recognize their distinct bootstrap unit identities instead of mistaking
	// valid v7 evidence for a plan unit somebody smuggled into the stream.
	for _, phase := range []guardV7SealPhase{guardV7SealStart, guardV7SealCompletion} {
		id, ierr := guardV7SealUnitID(rollout.Format, phase, metaSpecs[0].Key)
		if ierr != nil {
			return ierr
		}
		k := receiptLookupKey(id, guardReceiptKindBootstrap)
		allowed[k] = true
		bootstrapKeys[k] = true
	}
	for k, r := range receipts {
		if !allowed[k] {
			return fmt.Errorf("%w: rollout %s holds a %s receipt for unit %s on %s, which its plan does not enumerate",
				ErrGuardGateChainBroken, rollout.RolloutID, r.Kind, r.UnitID, r.Key)
		}
	}
	for k := range allowed {
		if bootstrapKeys[k] {
			continue
		}
		if _, ok := receipts[k]; !ok {
			return fmt.Errorf("%w: rollout %s is missing the receipt its plan requires under %s",
				ErrGuardGateChainBroken, rollout.RolloutID, k)
		}
	}
	return nil
}

// validateGuardReceipt refuses a receipt that does not attribute THIS unit's authorized work.
//
// It exists because presence under the unique key is not attribution. The key is
// (rollout, unit, kind) — three fields — while a receipt makes claims with twenty-one, and every
// one of the others was previously unchecked after the writing transaction ended. The write path
// does compare the judged event, but that comparison was not carried forward: on the NEXT boot
// the coordinator skipped any unit with a row under the key, and the terminal verification
// compared only the eight binding fields and the object.
//
// So a row whose intent, key, attempt or judged prestate belonged to different work was accepted
// as this unit's receipt — which is a receipt attesting something that did not happen, the one
// thing this ledger exists to make impossible.
//
// The judged event is the other half. A receipt says "I changed this"; the judged event says
// "this is the reading that authorized it". Requiring the same attempt_id and the same
// prestate_sha256 is what makes those one statement instead of two that can drift.
func validateGuardReceipt(p guardUnitPlan, rollout guardRolloutContext, fold unitGateFold, r guardReceipt) error {
	for _, f := range []struct {
		what string
		a, b string
	}{
		{"receipt_kind", r.Kind, guardReceiptKindUnit},
		{"intent", string(r.Intent), string(p.Intent)},
		{"relation_schema", r.Key.Schema, p.Spec.Key.Schema},
		{"relation_name", r.Key.Relation, p.Spec.Key.Relation},
		{"trigger_name", r.Key.Trigger, p.Spec.Key.Trigger},
		{"unit_id", r.UnitID, p.UnitID},
		{"rollout_id", r.RolloutID, rollout.RolloutID},
		{"epoch", fmt.Sprint(r.Epoch), fmt.Sprint(rollout.CodeEpoch)},
		{"manifest_format", fmt.Sprint(r.Format), fmt.Sprint(rollout.Format)},
		{"code_sha256", hexDigest(r.CodeSHA256), hexDigest(rollout.CodeSHA256)},
		{"retained_revision", fmt.Sprint(r.RetainedRevision), fmt.Sprint(rollout.RetainedRevision)},
		{"retained_sha256", hexDigest(r.RetainedSHA256), hexDigest(rollout.RetainedSHA256)},
		{"spec_sha256", hexDigest(r.SpecSHA256), hexDigest(p.Spec.SpecSHA256)},
		{"definition_sha256", hexDigest(r.DefinitionSHA256), hexDigest(p.Spec.DefinitionSHA256)},
		{"to_enable_state", r.ToEnableState, p.CanonicalEnableState},
	} {
		if f.a != f.b {
			return fmt.Errorf("the receipt for %s records %s=%q where the plan and rollout require %q",
				p.Spec.Key, f.what, f.a, f.b)
		}
	}
	// THE JUDGED EVENT, bound by identity AND by reading. Without the attempt a receipt could
	// belong to a different attempt of the same unit; without the prestate it could attribute
	// work authorized by a reading nobody recorded.
	if !fold.JudgedReadingValid || !fold.JudgedPrestate.Valid {
		return fmt.Errorf("the receipt for %s has no judged event to be bound to, so nothing records what authorized it", p.Spec.Key)
	}
	if r.AttemptID != fold.AttemptID {
		return fmt.Errorf("the receipt for %s carries attempt %q while its judged event carries %q",
			p.Spec.Key, r.AttemptID, fold.AttemptID)
	}
	if r.PrestateSHA256 != fold.JudgedPrestate.D {
		return fmt.Errorf("the receipt for %s attributes a reading hashing to %s while its judged event records %s",
			p.Spec.Key, hexDigest(r.PrestateSHA256), fold.JudgedPrestate)
	}
	// AND THE JUDGED EVENT'S OWN SUBJECT, which the attempt and the prestate digest do not pin.
	//
	// A prestate digest is computed from the READING — target exists, guard present, state,
	// canonicality, receipt present — and from nothing that says WHICH entry the reading belongs
	// to. So the two halves could each be internally consistent while describing different work:
	// a judged event naming one target's spec and definition, a receipt naming another's, bound
	// only by an attempt id and a hash they could both legitimately carry. Comparing the judged
	// event's own intent, key, spec and definition with the plan is what makes them one
	// statement instead of two that happen to share a number.
	if fold.Intent != p.Intent {
		return fmt.Errorf("the receipt for %s is bound to a judged event whose intent is %s, and the plan enumerates %s",
			p.Spec.Key, fold.Intent, p.Intent)
	}
	if fold.Key != p.Spec.Key {
		return fmt.Errorf("the receipt for %s is bound to a judged event about %s", p.Spec.Key, fold.Key)
	}
	if !fold.JudgedSpecSHA256.Valid || fold.JudgedSpecSHA256.D != p.Spec.SpecSHA256 {
		return fmt.Errorf("the receipt for %s is bound to a judged event recording entry %s, and the plan declares %s",
			p.Spec.Key, fold.JudgedSpecSHA256, hexDigest(p.Spec.SpecSHA256))
	}
	if !fold.JudgedDefinitionSHA256.Valid || fold.JudgedDefinitionSHA256.D != p.Spec.DefinitionSHA256 {
		return fmt.Errorf("the receipt for %s is bound to a judged event recording object %s, and the plan declares %s",
			p.Spec.Key, fold.JudgedDefinitionSHA256, hexDigest(p.Spec.DefinitionSHA256))
	}
	// THE PREDECESSOR IS EXACT IN BOTH DIRECTIONS. The check used to run only when the plan
	// named a predecessor, so a FIRST edge carrying an arbitrary predecessor receipt id was
	// accepted — a lineage pointing at something the plan says it cannot have.
	if p.Predecessor == "" && r.PredecessorReceiptID.Valid {
		return fmt.Errorf("the %s receipt for %s records predecessor %s, and it is the first edge of this target's lineage",
			p.Intent, p.Spec.Key, r.PredecessorReceiptID)
	}
	if p.Predecessor != "" && !r.PredecessorReceiptID.Valid {
		return fmt.Errorf("the %s receipt for %s records no predecessor, and its lineage requires the receipt of %s",
			p.Intent, p.Spec.Key, p.Predecessor)
	}
	// And the FROM state, which only the transition records: it is the fact the catalog no longer
	// shows, so a receipt that lost it could not be told from one that never had it.
	switch p.Intent {
	case intentTransitionLegacyOToA:
		if !r.FromEnableState.Valid || r.FromEnableState.V != fold.JudgedReading.GuardEnableState {
			return fmt.Errorf("the transition receipt for %s records from-state %s while its judged reading says %q",
				p.Spec.Key, r.FromEnableState, fold.JudgedReading.GuardEnableState)
		}
	default:
		if r.FromEnableState.Valid {
			return fmt.Errorf("the %s receipt for %s records a from-state (%s), which only a transition may carry",
				p.Intent, p.Spec.Key, r.FromEnableState)
		}
	}
	return nil
}

// verifyGuardTerminals runs the matrix once per terminal unit.
//
// The JUDGED reading is the authority, taken from the durable attempt-judged event and never
// from the catalog as it stands now. That is the whole point of recording it: after a
// successful O -> A the catalog says 'A', while the prestate that authorized the transition
// says 'O'. Substituting the live reading would make the matrix declare a correct receipt
// divergent before it even compared the object.
//
// Predecessor receipts are verified as LINEAGE, not run through the matrix: an adoption that
// a transition superseded is history, and holding it against the 'A' its successor was
// authorized to produce would report a correct chain as divergence.
func verifyGuardTerminals(
	ctx context.Context,
	q rowQuerier,
	dia dialect.Dialect,
	rollout guardRolloutContext,
	gate gateProjection,
	plans []guardUnitPlan,
	receipts map[string]guardReceipt,
	observed map[guardKey]guardCatalogRow,
	summary *guardRolloutSummary,
) (*guardTerminalRefusal, error) {
	for _, p := range plans {
		receipt, have := receipts[receiptLookupKey(p.UnitID, guardReceiptKindUnit)]
		if !have {
			// CONTINUITY FIRST, before the matrix. A missing receipt for an enumerated unit is
			// a broken chain, and the legacy adoption exception ("no receipt, but the object is
			// already in the poststate") would otherwise read it as a fresh start and re-adopt
			// — regenerating evidence instead of noticing its absence.
			summary.Diagnosis = "RECEIPT_MISSING"
			return guardRefusalFor(p, summary.Diagnosis, "the rollout enumerates this unit and the ledger holds no receipt for it"),
				fmt.Errorf("sqlstore: rollout %s enumerates unit %s for %s but the ledger holds no receipt for it",
					rollout.RolloutID, p.UnitID, p.Spec.Key)
		}
		// EVERY enumerated unit's receipt is validated in full, terminal or not. A superseded
		// adoption is history, but it is history this lineage rests on: the transition's
		// predecessor link points at its receipt id, so a predecessor receipt that attributed
		// different work would make the whole chain a chain of the wrong events.
		if verr := validateGuardReceipt(p, rollout, gate.Units[p.UnitID], receipt); verr != nil {
			summary.Diagnosis = "RECEIPT_NOT_THIS_UNIT"
			return guardRefusalFor(p, summary.Diagnosis, verr.Error()), fmt.Errorf("sqlstore: %w", verr)
		}
		if !p.IsTerminal {
			// History: verified by lineage above and by the predecessor link below.
			continue
		}
		if p.Predecessor != "" {
			pred, ok := receipts[receiptLookupKey(p.Predecessor, guardReceiptKindUnit)]
			if !ok {
				summary.Diagnosis = "LINEAGE_BROKEN"
				return guardRefusalFor(p, summary.Diagnosis, "the terminal unit links to a predecessor the ledger does not hold"),
					fmt.Errorf("sqlstore: the terminal unit for %s links to predecessor %s, which the ledger does not hold",
						p.Spec.Key, p.Predecessor)
			}
			if !receipt.PredecessorReceiptID.Valid || receipt.PredecessorReceiptID.D != pred.ReceiptID {
				summary.Diagnosis = "LINEAGE_MISMATCH"
				return guardRefusalFor(p, summary.Diagnosis, "the terminal receipt records a predecessor that is not its predecessor's receipt"),
					fmt.Errorf("sqlstore: the terminal receipt for %s records predecessor %s but its predecessor's receipt is %s",
						p.Spec.Key, receipt.PredecessorReceiptID, hexDigest(pred.ReceiptID))
			}
		}

		fold, ok := gate.Units[p.UnitID]
		if !ok || !fold.JudgedReadingValid {
			summary.Diagnosis = "JUDGED_READING_MISSING"
			return guardRefusalFor(p, summary.Diagnosis, "the terminal unit has a receipt and no judged reading, so nothing records what authorized it"),
				fmt.Errorf("sqlstore: the terminal unit for %s has a receipt but no judged reading in the gate, so nothing records what authorized it",
					p.Spec.Key)
		}
		// A TERMINAL ADOPTION MAY NOT LEAVE THE TARGET SHORT OF WHAT THE EDITION DECLARES.
		//
		// THE RACE THIS CLOSES is a time-of-check/time-of-use that requireGuardEnumerationCoversManifest
		// cannot see, because it runs too early. That rule says "an adoption that judged a state
		// other than the desired one must also enumerate its transition" — and it evaluates the
		// JUDGED reading, which for a unit that has not run yet does not exist. So: a durable
		// `pending-opened` enumerating only the adoption of a target sitting at 'O'; the rule
		// skips it for want of a judged event; this boot then RUNS that adoption, creating the
		// judged 'O' and its receipt; and the plan is never rebuilt afterwards, so the close
		// judges an adoption-only lineage against the 'O' it adopted and can write `ready` over a
		// target the manifest wants at 'A'. The next boot would refuse — after this one served.
		//
		// Asked here it cannot be raced: this runs under the closing fence, after every unit, on
		// the judged reading that is by then durable.
		if p.IsTerminal && p.Intent == intentAdoptLegacy &&
			fold.JudgedReading.GuardEnableState != p.Spec.DesiredEnableState {
			summary.Diagnosis = "LINEAGE_INCOMPLETE"
			return guardRefusalFor(p, summary.Diagnosis,
					fmt.Sprintf("the terminal adoption judged state %q and this edition declares %q, so the lineage needs its transition",
						fold.JudgedReading.GuardEnableState, p.Spec.DesiredEnableState)),
				fmt.Errorf("sqlstore: the terminal unit for %s is an adoption that judged state %q while this edition declares %q; the rollout enumerates no transition for it, so closing would attest a target short of what the manifest requires",
					p.Spec.Key, fold.JudgedReading.GuardEnableState, p.Spec.DesiredEnableState)
		}
		row, seen := observed[p.Spec.Key]
		if !seen {
			summary.Diagnosis = "PROJECTION_MISSING"
			return guardRefusalFor(p, summary.Diagnosis, "the catalog projection holds no reading for this target"),
				fmt.Errorf("sqlstore: the guard batch holds no reading for %s", p.Spec.Key)
		}
		outcome := reconcileOutcomeFor(p.unitSpec(), fold.JudgedReading,
			receiptProjectionFrom(receipt), objectProjectionFrom(row, p.Spec))
		summary.Outcomes[outcome]++
		if outcome != outcomeApplied {
			summary.Diagnosis = "TERMINAL_" + strings.ToUpper(string(outcome))
			// THE REFUSAL IS RETURNED, NOT WRITTEN. This now runs inside the closing fence's
			// transaction, and a durable diagnostic appended there would roll back with the
			// close it was diagnosing. The caller records it once the transaction has ended.
			return &guardTerminalRefusal{
					UnitID: p.UnitID,
					Refusal: guardPlanRefusal{
						Key:    p.Spec.Key,
						Code:   summary.Diagnosis,
						Detail: fmt.Sprintf("the terminal unit reconciles as %s", outcome),
					},
				}, fmt.Errorf("sqlstore: the terminal unit for %s reconciles as %s rather than applied",
					p.Spec.Key, outcome)
		}
	}
	return nil, nil
}

// guardRefusalFor names a terminal failure so the caller can make it durable.
//
// EVERY failure class returns one now. Before, only "the matrix answered something other than
// applied" did, and the five that did not — receipt missing, binding invalid, lineage broken or
// mismatched, judged reading missing, projection missing — returned an error with a nil refusal.
// The caller writes a durable diagnostic only when it is handed one, so those five failed the
// boot and left nothing in the ledger: the next boot re-derived the same condition from scratch
// and an operator reading the gate saw a rollout that had simply never closed.
func guardRefusalFor(p guardUnitPlan, code, detail string) *guardTerminalRefusal {
	return &guardTerminalRefusal{UnitID: p.UnitID, Refusal: guardPlanRefusal{Key: p.Spec.Key, Code: code, Detail: detail}}
}

// recordGuardVerificationFailure appends the durable verification-failed event.
//
// It needs a transaction, so it takes the connection rather than the projection handle. The
// idempotence comes from the ledger: the same failure produces the same diagnostic
// fingerprint, and the unique constraint on (rollout, unit, fingerprint) turns a second boot
// finding the same condition into a no-op rather than a second row.
func recordGuardVerificationFailure(ctx context.Context, q rowQuerier, dia dialect.Dialect, rollout guardRolloutContext, unitID string, refusal guardPlanRefusal, phase gatePhase) error {
	conn, ok := q.(*sql.Conn)
	if !ok {
		return fmt.Errorf("sqlstore: cannot record a guard verification failure through %T", q)
	}
	// THE UNIT IS THE CALLER'S TO NAME. An earlier version derived it here as the target's
	// ADOPTION identity whatever had actually failed, which put a transition's drift on the
	// adoption's row — and since (rollout, unit, fingerprint) is the deduplication key, two
	// genuinely different failures on one target could collapse onto one record.
	if strings.TrimSpace(unitID) == "" {
		return fmt.Errorf("sqlstore: a guard verification failure for %s names no unit", refusal.Key)
	}
	// AND SO IS THE PHASE. It used to be hard-coded to `pending`, so a drift found by the fast
	// path — which only runs on a rollout that is already `ready` — wrote a durable row claiming
	// the rollout was pending. The fold ignores an event's phase and preserves its own, so the
	// projection stayed correct and the ROW lied: an operator reading the ledger to decide what
	// happened was told the rollout had never closed.
	if phase != gatePhasePending && phase != gatePhaseReady {
		return fmt.Errorf("sqlstore: a guard verification failure for %s names phase %q, which is not one this gate records", refusal.Key, phase)
	}
	diag := guardDiagnostic{
		Code:          refusal.Code,
		RetryClass:    guardRetryClassPermanent,
		UnblockPolicy: guardUnblockOperator,
		Details:       refusal.Detail,
	}
	// A BOUNDED CONTEXT OF ITS OWN, because the caller's has often just failed.
	//
	// This is called after a rolled-back transaction and, in the close's case, after an error
	// that may itself have been a cancellation. Reusing that context meant the durable
	// diagnostic was dropped exactly when it was most needed, leaving only a log line on a node
	// that may be about to exit. WithoutCancel keeps the values (tracing, deadlines the caller
	// set for its own reasons are deliberately not inherited) and the explicit timeout keeps
	// this from becoming an unbounded write on a server that is not answering.
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), guardDiagnosticWriteTimeout)
	defer cancel()
	tx, err := conn.BeginTx(wctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	_, err = appendGateEvent(wctx, tx, dia, gateEvent{
		RolloutID:        rollout.RolloutID,
		Kind:             gateEventVerificationFailed,
		UnitID:           unitID,
		Key:              refusal.Key,
		Format:           rollout.Format,
		CodeEpoch:        rollout.CodeEpoch,
		CodeSHA256:       rollout.CodeSHA256,
		RetainedRevision: rollout.RetainedRevision,
		RetainedSHA256:   rollout.RetainedSHA256,
		Phase:            phase,
		Condition:        gateConditionBlocked,
		Diagnostic:       diag,
	})
	if err != nil {
		// A COLLISION ON (rollout, unit, fingerprint) IS IDEMPOTENCE — AND ONLY THAT ONE.
		//
		// Swallowing every 23505 also swallowed a collision on (rollout, event_ordinal) and on
		// the primary key, which are not repeats of this failure: they mean another writer took
		// the ordinal this transaction computed, so the diagnostic was never recorded and the
		// caller was told it had been. The row is re-read under its OWN deduplication key and
		// compared; anything that does not match is propagated.
		if !isUniqueViolation(err) {
			return err
		}
		// THE TRANSACTION IS ENDED BEFORE THE ROW IS RE-READ, and the previous version did not do
		// it. After a 23505 the transaction is ABORTED: PostgreSQL rejects every further command
		// in it until it is closed, and the *sql.Conn it was begun on is still owned by that
		// transaction — so a query issued on the connection could not have demonstrated
		// idempotence at all. Rolling back first is what makes the re-read a reading.
		if rberr := tx.Rollback(); rberr != nil && !errors.Is(rberr, sql.ErrTxDone) {
			return fmt.Errorf("sqlstore: a guard verification failure collided and its transaction could not be closed: %w (original: %v)", rberr, err)
		}
		same, cerr := guardDiagnosticAlreadyRecorded(wctx, conn, dia, rollout, unitID, diag)
		if cerr != nil {
			return fmt.Errorf("sqlstore: a guard verification failure collided and could not be re-read: %w (original: %v)", cerr, err)
		}
		if same {
			return nil
		}
		return fmt.Errorf("sqlstore: a guard verification failure for %s collided with a row that is not the same failure: %w", refusal.Key, err)
	}
	return tx.Commit()
}

// guardDiagnosticWriteTimeout bounds the durable write of a diagnostic taken after a failure.
const guardDiagnosticWriteTimeout = 15 * time.Second

// guardDiagnosticAlreadyRecorded reports whether THIS failure is already in the ledger.
//
// It re-reads by the deduplication key the unique constraint is built on — (rollout, unit,
// fingerprint) — and compares the diagnostic fields. Equal fingerprints with different contents
// are not possible for an honest writer, so comparing them anyway is what makes "idempotent"
// something this function checked rather than inferred from a SQLSTATE.
func guardDiagnosticAlreadyRecorded(ctx context.Context, q dialect.Querier, dia dialect.Dialect,
	rollout guardRolloutContext, unitID string, diag guardDiagnostic) (bool, error) {
	fp, err := diag.fingerprint(rollout.RolloutID, unitID, rollout.CodeEpoch, rollout.CodeSHA256, gateConditionBlocked)
	if err != nil {
		return false, err
	}
	rows, err := q.QueryContext(ctx, dia.Rebind(
		"SELECT kind, diagnostic_code, retry_class, unblock_policy, details FROM "+guardOnly(dia)+guardGateEventsTable+
			" WHERE rollout_id = ? AND unit_id = ? AND diagnostic_fingerprint = ?"),
		rollout.RolloutID, unitID, fp[:])
	if err != nil {
		return false, err
	}
	defer rows.Close()
	if !rows.Next() {
		return false, rows.Err()
	}
	var kind, code, class, policy, details string
	if err := rows.Scan(&kind, &code, &class, &policy, &details); err != nil {
		return false, err
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	return kind == string(gateEventVerificationFailed) &&
		code == diag.Code && class == guardRetryClassPermanent &&
		policy == guardUnblockOperator && details == diag.Details, nil
}

// isUniqueViolation reports whether err is PostgreSQL's 23505 or SQLite's uniqueness refusal.
//
// Two engines, two shapes, one meaning. It is matched on the CONDITION rather than on message
// text for PostgreSQL; SQLite has no SQLSTATE, so its message is the only signal available and
// that limit is stated rather than hidden.
func isUniqueViolation(err error) bool {
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		return pg.Code == "23505"
	}
	return strings.Contains(strings.ToLower(err.Error()), "unique constraint")
}

// newMigrationUnitBudget is one unit's acquisition deadline, on the production clock.
//
// A NEW budget per unit, and the same pointer across all of that unit's retries: the deadline
// bounds the unit, not the attempt, so restarting it on every retry would make it unbounded
// in exactly the situation it exists for. It is a different object from the coordination
// budget because waiting for another NODE and waiting for a long-running WRITER are different
// situations, and one value for both would be wrong for one of them.
func newMigrationUnitBudget() *lockBudget {
	return newLockBudget(unitAcquisitionBudget, time.Now, sleepCtx, jitterFloat)
}

// computeGuardCheckpoint reads the head and count of the inventory and the receipt streams.
//
// Both are read through the SAME handle the caller is closing the rollout on, so what is attested
// is what that transaction can see.
func computeGuardCheckpoint(ctx context.Context, q dialect.Querier, dia dialect.Dialect, rolloutID string) (guardCheckpoint, error) {
	var cp guardCheckpoint
	_, invHead, _, _, err := inventoryStreamHead(ctx, q, dia)
	if err != nil {
		return guardCheckpoint{}, err
	}
	if !invHead.Valid {
		// A rollout cannot be closed over an empty inventory: its own activations are what the
		// manifest was compared against, so an empty stream means the comparison had nothing to
		// compare.
		return guardCheckpoint{}, fmt.Errorf("sqlstore: refusing to close rollout %s over an empty inventory", rolloutID)
	}
	cp.InventoryHead = invHead.D
	if cp.InventoryCount, err = countStreamRows(ctx, q, dia, guardInventoryEventsTable, ""); err != nil {
		return guardCheckpoint{}, err
	}
	_, rcptHead, err := receiptStreamHead(ctx, q, dia, rolloutID)
	if err != nil {
		return guardCheckpoint{}, err
	}
	if !rcptHead.Valid {
		return guardCheckpoint{}, fmt.Errorf("sqlstore: refusing to close rollout %s with no receipts at all", rolloutID)
	}
	cp.ReceiptHead = rcptHead.D
	if cp.ReceiptCount, err = countStreamRows(ctx, q, dia, guardReceiptsTable, rolloutID); err != nil {
		return guardCheckpoint{}, err
	}
	return cp, nil
}

// countStreamRows counts a log's rows, scoped to a rollout when the table is per-rollout.
func countStreamRows(ctx context.Context, q dialect.Querier, dia dialect.Dialect, table, rolloutID string) (int64, error) {
	query := "SELECT COUNT(*) FROM " + guardOnly(dia) + table // #nosec G202 -- table is one of this package's own constants, and guardOnly returns one of two literals
	var args []any
	if rolloutID != "" {
		query += " WHERE rollout_id = ?"
		args = append(args, rolloutID)
	}
	rows, err := q.QueryContext(ctx, dia.Rebind(query), args...)
	if err != nil {
		return 0, fmt.Errorf("sqlstore: count %s: %w", table, err)
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		return 0, fmt.Errorf("sqlstore: count %s returned no row", table)
	}
	var n int64
	if err := rows.Scan(&n); err != nil {
		return 0, fmt.Errorf("sqlstore: count %s: %w", table, err)
	}
	return n, rows.Err()
}

// verifyGuardCheckpoint compares the two logs against what the closing event attested.
//
// WHAT IT CATCHES: a row removed from the tail of either log after closure, and a row added to
// either without re-closing. Both change a head or a count.
//
// WHAT IT DOES NOT CATCH, said plainly: the gate's own tail. A closing event cannot attest itself,
// so deleting it leaves a `pending` prefix that is indistinguishable from a boot which crashed
// before closing — and the consequence is re-attestation, not a silent downgrade: the next boot
// re-reads every object, re-validates every receipt and either closes again or refuses. Covering
// that needs an anchor outside this database.
func verifyGuardCheckpoint(ctx context.Context, q dialect.Querier, dia dialect.Dialect, rolloutID string, gate gateProjection) error {
	if gate.Phase != gatePhaseReady {
		return nil
	}
	if !gate.CheckpointPresent {
		return fmt.Errorf("%w: rollout %s is closed by an event that carries no checkpoint, so the other two logs are unattested",
			ErrGuardGateIllegalTransition, rolloutID)
	}
	_, invHead, _, _, err := inventoryStreamHead(ctx, q, dia)
	if err != nil {
		return err
	}
	invCount, err := countStreamRows(ctx, q, dia, guardInventoryEventsTable, "")
	if err != nil {
		return err
	}
	if !invHead.Valid || invHead.D != gate.Checkpoint.InventoryHead || invCount != gate.Checkpoint.InventoryCount {
		return fmt.Errorf("%w: rollout %s attested an inventory of %d events heading at %s, and it now holds %d heading at %s",
			ErrGuardGateChainBroken, rolloutID, gate.Checkpoint.InventoryCount,
			hexDigest(gate.Checkpoint.InventoryHead), invCount, invHead)
	}
	_, rcptHead, err := receiptStreamHead(ctx, q, dia, rolloutID)
	if err != nil {
		return err
	}
	rcptCount, err := countStreamRows(ctx, q, dia, guardReceiptsTable, rolloutID)
	if err != nil {
		return err
	}
	if !rcptHead.Valid || rcptHead.D != gate.Checkpoint.ReceiptHead || rcptCount != gate.Checkpoint.ReceiptCount {
		return fmt.Errorf("%w: rollout %s attested %d receipts heading at %s, and it now holds %d heading at %s",
			ErrGuardGateChainBroken, rolloutID, gate.Checkpoint.ReceiptCount,
			hexDigest(gate.Checkpoint.ReceiptHead), rcptCount, rcptHead)
	}
	return nil
}
