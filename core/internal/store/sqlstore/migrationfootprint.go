// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ErrMigrationLockFootprint reports a unit that locked something it never declared.
var ErrMigrationLockFootprint = errors.New("sqlstore: migration unit locked a relation outside its declared plan")

// heldLock is one relation lock this transaction actually holds.
type heldLock struct {
	// Relation is the NUL-joined comparison key; never shown to a human.
	Relation string
	// Display is the quoted, readable form — and it survives a relation that was
	// dropped inside this transaction, where it falls back to the OID.
	Display string
	Mode    lockMode
	Raw     string
}

// verifyLockFootprint compares what the transaction ACTUALLY locked against what the
// plan declared, and refuses the difference.
//
// This is the answer to the one hole the type system cannot close. lockPlan declares
// relations and modes, but EXECUTE AND RECEIPT are opaque SQL: a statement can take locks
// on relations nobody wrote down — through a foreign key, a partition, a trigger's own
// table, an index rebuild — and the declaration would still look perfectly correct on the
// page. Reading the answer out of pg_locks replaces a promise about the SQL with a
// measurement of it.
//
// TargetStatement is NOT among them any more, and that changes what this check is for.
// validate() requires it to be exactly the generated LOCK TABLE, so the acquisition's
// footprint is a property of the plan rather than of a string. What remains measured here
// is what the unit's own callbacks do.
//
// It runs INSIDE the transaction and BEFORE the commit, which is the only window
// where the question can be answered at all: relation locks are released at commit,
// so afterwards there is nothing left to read, and the whole point is to refuse
// rather than to report.
//
// Catalog relations are excluded. Every statement touches pg_class and friends while
// planning, those locks are the engine's business rather than the unit's, and a
// footprint check that flagged them would be noise nobody could act on and would
// therefore be turned off.
func verifyLockFootprint(ctx context.Context, tx *sql.Tx, plan lockPlan) error {
	held, err := heldRelationLocks(ctx, tx)
	if err != nil {
		// FAIL CLOSED. An unverifiable footprint is not an empty one, and treating a
		// failed check as a pass would make the whole guarantee evaporate exactly when
		// the database is behaving oddly enough to be worth checking.
		return fmt.Errorf("sqlstore: could not verify the lock footprint for %s: %w", plan.Target.displayRelation(), err)
	}

	var undeclared, escalated, missing, understated []string
	// The STRONGEST mode observed per relation, not merely "was it seen". A boolean
	// could not express the case that mattered: a plan declaring ACCESS EXCLUSIVE
	// while the transaction only ever took ACCESS SHARE satisfied both directions —
	// the observed mode is covered by the declared one, and the relation is present —
	// so the plan asserted total exclusion and nothing checked that it was obtained.
	seen := make(map[string]lockMode, len(held))
	for _, h := range held {
		if cur, ok := seen[h.Relation]; !ok || h.Mode.covers(cur) {
			seen[h.Relation] = h.Mode
		}
		want, ok := plan.declared(h.Relation)
		if !ok {
			undeclared = append(undeclared, h.Display+" at "+h.Mode.String())
			continue
		}
		// Containment of conflict sets, not an ordinal comparison. Two modes can be
		// incomparable — SHARE UPDATE EXCLUSIVE and SHARE conflict with different
		// things and neither covers the other — so `held > want` authorized taking a
		// mode the plan does not cover, on the strength of an ordering PostgreSQL does
		// not define.
		if !want.covers(h.Mode) {
			escalated = append(escalated,
				fmt.Sprintf("%s at %s, declared %s", h.Display, h.Mode, want))
		}
	}

	// AND THE OTHER DIRECTION. Checking only observed-is-subset-of-declared means a
	// plan can declare ACCESS EXCLUSIVE, run `SELECT 1`, and pass — the declaration
	// then documents a lock nobody took, while the real work happened under whatever
	// the statement happened to need. A plan is a claim about what WILL be held, and
	// half of it went unverified.
	for _, want := range append(append([]plannedLock{}, plan.Metadata...), plan.Target) {
		got, ok := seen[want.relation()]
		if !ok {
			missing = append(missing, want.String())
			continue
		}
		// Declared ACCESS EXCLUSIVE and took ACCESS SHARE is not a satisfied plan. The
		// declaration is a claim about the protection the unit ran under, so the mode
		// actually obtained has to cover the mode declared.
		if !got.covers(want.Mode) {
			understated = append(understated,
				fmt.Sprintf("%s declared %s, only took %s", want.displayRelation(), want.Mode, got))
		}
	}

	sort.Strings(undeclared)
	sort.Strings(escalated)
	sort.Strings(missing)
	sort.Strings(understated)

	var problems []string
	if len(undeclared) > 0 {
		problems = append(problems, fmt.Sprintf("took undeclared locks %v", undeclared))
	}
	if len(escalated) > 0 {
		problems = append(problems, fmt.Sprintf("took modes its plan does not cover: %v", escalated))
	}
	if len(missing) > 0 {
		problems = append(problems, fmt.Sprintf("never took declared locks %v", missing))
	}
	if len(understated) > 0 {
		problems = append(problems, fmt.Sprintf("took weaker locks than declared: %v", understated))
	}
	if len(problems) > 0 {
		return fmt.Errorf("%w: %s %s",
			ErrMigrationLockFootprint, plan.Target.displayRelation(), strings.Join(problems, "; "))
	}
	return nil
}

// heldIdentity builds the comparison key and the readable form for one held lock.
//
// A relation the transaction DROPPED has no catalog row left, so it falls back to the
// OID. That key is prefixed with NUL, which no identifier can start with, so the OID
// space cannot collide with a real schema called "oid".
func heldIdentity(schema, name sql.NullString, oid string) (key, display string) {
	if !schema.Valid || !name.Valid {
		return "\x00oid\x00" + oid, "oid:" + oid
	}
	return schema.String + "\x00" + name.String,
		quoteIdent(schema.String) + "." + quoteIdent(name.String)
}

// heldRelationLocks reads this backend's granted relation locks on user relations.
//
// Scoped to the current database and to this backend's own PID: pg_locks is
// cluster-wide, and a check that swept in another session's locks would refuse units
// for things they did not do.
//
// Every catalog reference is schema-qualified. This runs on the migration connection,
// whose search_path an untrusted role could otherwise influence, and a shadowed
// pg_locks would let an attacker choose the answer this refusal is based on — which
// is worth more to them than any single lock.
func heldRelationLocks(ctx context.Context, tx *sql.Tx) ([]heldLock, error) {
	// THREE THINGS THIS QUERY GETS RIGHT, each of which was wrong at some point.
	//
	// LEFT JOIN, not INNER. pg_locks.relation is an OID, and a transaction that takes
	// a lock on a relation and then DROPs it still holds that lock at commit while its
	// pg_class row is already gone. An inner join silently dropped exactly the case
	// worth catching: an undeclared lock on something the unit then destroyed.
	//
	// The identity is assembled in GO, not in SQL, and joined with NUL because it is
	// the one byte a PostgreSQL identifier cannot contain. A dot is not injective —
	// "a.b"."c" and "a"."b.c" collide — and this string is what authorizes a lock.
	//
	// It cannot be built server-side: PostgreSQL refuses NUL inside a text value
	// outright. Measured — `chr(0)` in the projection returned
	// `ERROR: null character not permitted (SQLSTATE 54000)` and took the whole check
	// down with it. So the query returns the two catalog parts and the flattening is a
	// decision of this package, which is where it belonged anyway.
	//
	// INDEXES AND OWNED SEQUENCES ARE ATTRIBUTED TO THEIR TABLE. This is the
	// difference between a check that works and one that gets switched off: an
	// ordinary INSERT takes RowExclusiveLock on the table AND on every one of its
	// indexes, and on the sequence behind an identity column. Counting those as
	// undeclared relations rejects a unit that did exactly what it declared.
	//
	// Attributing rather than EXCLUDING them is what keeps the guarantee. Dropping
	// index locks entirely would hide a REINDEX, which takes ACCESS EXCLUSIVE on the
	// index while holding only SHARE on the table — so the mode is still compared,
	// against the owning table's declaration, and an escalation on an index is refused
	// like any other.
	//
	// THE SEQUENCE BRANCH IS RESTRICTED TO relkind='S', and that restriction is the
	// whole correctness of it. An unrestricted pg_class -> pg_class dependency of type
	// 'a' or 'i' also matches a PARTITION's dependency on its parent, so a leaf
	// partition was being attributed to the parent table. Measured: pg_locks showed
	// parent and leaf, this query showed the parent twice, and the check returned nil
	// — a lock on an undeclared partition made invisible. The same collapse produced
	// the opposite error too, reporting "never took declared locks" for a leaf that
	// was declared and genuinely acquired.
	const q = `
WITH held AS (
    SELECT l.relation AS oid, l.mode
    FROM pg_catalog.pg_locks l
    WHERE l.pid = pg_catalog.pg_backend_pid()
      AND l.locktype = 'relation'
      AND l.granted
      AND l.database = (SELECT d.oid FROM pg_catalog.pg_database d
                        WHERE d.datname = pg_catalog.current_database())
),
attributed AS (
    SELECT h.mode,
           COALESCE(i.indrelid, dep.refobjid, h.oid) AS rel_oid
    FROM held h
    LEFT JOIN pg_catalog.pg_index i ON i.indexrelid = h.oid
    LEFT JOIN LATERAL (
        SELECT d.refobjid
        FROM pg_catalog.pg_depend d
        JOIN pg_catalog.pg_class dc
          ON dc.oid = d.objid
         AND dc.relkind = 'S'
        WHERE d.classid = 'pg_catalog.pg_class'::pg_catalog.regclass
          AND d.objid = h.oid
          AND d.refclassid = 'pg_catalog.pg_class'::pg_catalog.regclass
          AND d.deptype IN ('a', 'i')
        LIMIT 1
    ) dep ON true
)
SELECT n.nspname, c.relname, a.rel_oid::text AS rel_oid, a.mode
FROM attributed a
LEFT JOIN pg_catalog.pg_class c ON c.oid = a.rel_oid
LEFT JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
WHERE (n.nspname IS NULL
       OR (n.nspname NOT IN ('pg_catalog', 'information_schema')
           AND n.nspname NOT LIKE 'pg_toast%'))
ORDER BY 1, 2, 4`

	rows, err := tx.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // error surfaced via rows.Err below

	var out []heldLock
	for rows.Next() {
		var schema, name sql.NullString
		var oid, raw string
		if err := rows.Scan(&schema, &name, &oid, &raw); err != nil {
			return nil, err
		}
		rel, display := heldIdentity(schema, name, oid)
		mode, ok := lockModeFromCatalog[raw]
		if !ok {
			// An unknown mode is not a mode to ignore. PostgreSQL could add one, and
			// silently skipping it would turn this check into a filter that passes
			// precisely what it does not understand.
			return nil, fmt.Errorf("sqlstore: pg_locks reported the unknown lock mode %q on %s", raw, display)
		}
		out = append(out, heldLock{Relation: rel, Display: display, Mode: mode, Raw: raw})
	}
	return out, rows.Err()
}
