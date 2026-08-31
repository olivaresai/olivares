// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/store"
)

// appendonly_acl.go closes the ACL leg of append-only enforcement on PostgreSQL.
//
// The immutability trigger is BEFORE UPDATE OR DELETE ... FOR EACH ROW, and TRUNCATE
// is a statement-level operation that no row trigger can observe. So for TRUNCATE the
// revoked privilege is the only defense the schema has, and a revoke nobody verified
// is an intention, not a control. Two steps, in this order:
//
//  1. reconcileAppendOnlyACL re-asserts the revoke on every boot, because the DDL that
//     carries it runs exactly once per table and never again.
//  2. verifyAppendOnlyACL reads the EFFECTIVE privileges back and refuses to serve if
//     the application role can still mutate or wipe evidence — or cannot append to the
//     tables it is actually expected to write.
//
// Step 1 is what makes step 2 meaningful rather than vacuous: because the revoke is
// re-executed unconditionally just before, a clean verification means "it was applied
// and it held", not merely "this role never happened to be granted anything".
//
// Everything here addresses a FIXED schema (dialect.EngineSchema) and passes table
// names as bound parameters. Both are deliberate: resolving the schema from
// search_path let a connection answer about the wrong schema — measured, an app role
// with search_path=other,public produced an empty inventory and the verification
// passed while public.audit_events was wide open — and packing names into a delimited
// string silently dropped a legal table name containing that delimiter.

// appendOnlyACLScope resolves the tables the ACL work covers: everything the registry
// declares append-only (plus audit_events), UNION everything the live schema still guards
// with the immutability trigger, UNION the durable inventory of everything that has ever
// carried that guard — restricted to tables that exist.
//
// THE THIRD INPUT IS THE ONE THAT DECIDES, and this sentence used to omit it. The first two
// are sources of ADMISSION only; membership itself is the inventory's, because a scope
// derived from the live trigger let a DROP TRIGGER remove a table from the very set whose
// protection is checked — destroying the guard destroyed the obligation to have one. A
// reader who stopped at this summary would carry away the semantics that defect had. See
// appendonlyscope.go, and the divergence refusal below.
//
// The union of the first two is not belt-and-braces. A module dropped from a build leaves its tables
// behind on purpose — they hold retained evidence — so a set taken from the registry
// alone would quietly stop protecting the tables of every module ever removed, which
// are precisely the ones no longer written and most likely to be wiped unnoticed.
// Conversely the registry is needed too: a table registered but not yet guarded (a
// schema created by an older build) must still be reached by the reconcile.
//
// It returns the live scope AND the set that has been observed CARRYING the guard, because
// those are two different questions and only the second may be written to the durable
// inventory. Admitting a merely-REGISTERED table would record "this must be guarded" about
// a relation this deployment has never seen guarded, and if its module were later dropped
// from the build the divergence check would then refuse a boot over a table that never had
// a guard to lose. The inventory's row means one thing: this database HAS carried the guard
// on this relation.
//
// AND THAT NARROWING IS NOT OBSERVABLE TODAY, which is said here rather than defended with a
// test that cannot fail. Measured: replacing `guardedNow` with the whole live scope leaves
// every test in this package green, because by the time this runs on PostgreSQL every
// REGISTERED append-only table is already guarded — the rollout adopts them a few statements
// earlier, and a registered table whose guard is missing has already failed the boot at
// verifyGuardTerminals (measured separately: TERMINAL_DIVERGENT). So the two sets coincide on
// every path a boot can reach, and a regression written against the difference would pass
// with the narrowing removed. It is kept as precision about what the inventory MEANS, and a
// guard against a future change that lets a registered table reach here unguarded — not as a
// behavior anything currently exercises. A test claiming otherwise was written, measured
// useless by mutation, and deleted.
func appendOnlyACLScope(ctx context.Context, db dialect.Querier, dia dialect.Dialect, registered []string) (live, guardedNow []string, err error) {
	guarded, err := dia.AppendOnlyACLTables(ctx, db)
	if err != nil {
		return nil, nil, fmt.Errorf("list append-only guarded tables: %w", err)
	}
	// `discovered` is what the LIVE schema and this build say right now. It is the source
	// of ADMISSION to the scope — and, since C4-15, never the source of removal: see
	// appendonlyscope.go for the measurement that made the difference matter.
	discovered := make(map[string]bool, len(registered)+len(guarded))
	for _, t := range registered {
		discovered[t] = true
	}
	for _, t := range guarded {
		discovered[t] = true
	}
	inventory, err := readAppendOnlyScopeInventory(ctx, db, dia)
	if err != nil {
		return nil, nil, err
	}
	want := make(map[string]bool, len(discovered)+len(inventory))
	for t := range discovered {
		want[t] = true
	}
	for _, t := range inventory {
		want[t] = true
	}
	all := make([]string, 0, len(want))
	for t := range want {
		all = append(all, t)
	}
	sort.Strings(all)

	present, err := existingTables(ctx, db, all)
	if err != nil {
		return nil, nil, fmt.Errorf("list tables: %w", err)
	}
	// THE REFUSAL, BEFORE ANYTHING IS RECONCILED OR VERIFIED. A table the inventory holds,
	// that still exists, and that the live schema no longer presents as guarded, is not a
	// table that left the scope: it is a guard that was removed from a table that is still
	// in it.
	if gone := appendOnlyScopeDivergence(inventory, discovered, present); len(gone) > 0 {
		return nil, nil, errAppendOnlyScopeShrank(gone)
	}
	live = make([]string, 0, len(all))
	for _, t := range all {
		// A registered descriptor whose table is absent is not this file's business:
		// the migration step owns creating it, and a REVOKE on a missing relation is a
		// hard error that would mask that failure.
		if present[t] {
			live = append(live, t)
		}
	}
	guardedNow = make([]string, 0, len(guarded))
	for _, t := range guarded {
		if present[t] {
			guardedNow = append(guardedNow, t)
		}
	}
	sort.Strings(guardedNow)
	return live, guardedNow, nil
}

// reconcileAppendOnlyACL re-asserts the append-only revoke for every append-only table
// that exists, against the dialect's effective application role.
//
// It runs on the OWNER pool (which is the app pool itself outside the owner/app
// split): only a table's owner may administer its ACL. It is idempotent — revoking a
// privilege that is already absent is a no-op — and it is the path that converges an
// EXISTING database, where the creation DDL that first emitted the revoke will never
// run again (migrate.Apply skips applied versions, applyModuleTables skips tracked
// tables, reconcileColumns only creates a wholly absent table).
//
// A no-op on engines without a role layer (SQLite).
func reconcileAppendOnlyACL(ctx context.Context, ownerDB dialect.Execer, dia dialect.Dialect, registered []string) error {
	if dia.Name() != store.EnginePostgres {
		return nil
	}
	// The inventory has to EXIST before the scope is read, and this is the one call site
	// that may create it: it holds the migration lock and runs on the owner pool. The
	// verification later in boot deliberately cannot — a read-only check that provisions
	// its own memory whenever it finds none would pass on a database that lost it.
	if err := ensureAppendOnlyScopeTable(ctx, ownerDB, dia); err != nil {
		return err
	}
	live, guardedNow, err := appendOnlyACLScope(ctx, ownerDB, dia, registered)
	if err != nil {
		return fmt.Errorf("sqlstore: append-only ACL reconcile: %w", err)
	}
	// Admission happens AFTER the divergence check inside appendOnlyACLScope, so a boot
	// cannot launder a shrunken scope by recording the shrunken set as the new truth. And it
	// admits what this boot OBSERVED GUARDED, not the whole live scope: see appendOnlyACLScope
	// for why recording a merely-registered relation would arm the divergence check against a
	// table that never carried a guard.
	if err := admitToAppendOnlyScope(ctx, ownerDB, dia, guardedNow); err != nil {
		return err
	}
	if err := requireTableOwnership(ctx, ownerDB, live); err != nil {
		return err
	}
	stmts := dia.AppendOnlyACLStmts(live)
	if len(stmts) != len(live) {
		// The contract is one statement per table, in order — the index is what lets a
		// failure name its table. Assert it rather than risk reporting the wrong one
		// (or indexing out of range) if that contract ever changes.
		return fmt.Errorf("sqlstore: append-only ACL reconcile: dialect returned %d statements for %d tables",
			len(stmts), len(live))
	}
	for i, stmt := range stmts {
		if _, err := ownerDB.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("sqlstore: append-only ACL reconcile on %q: %w", live[i], err)
		}
	}
	return nil
}

// requireTableOwnership refuses unless the connection can actually administer the
// ACL of every table in scope — that is, it owns each one, or belongs to the role
// that does.
//
// This exists because a successful REVOKE proves nothing. PostgreSQL does NOT error
// when a role revokes privileges it did not grant: it emits
// "WARNING: no privileges could be revoked" and reports success. So a reconcile that
// inferred "applied" from a clean exit would silently do nothing whenever the owner
// pool is not the tables' owner — and then the verification's clean result would be
// read as "the boundary was re-asserted and held" when nothing was re-asserted at all.
//
// The realistic way to get there: a deployment that started single-role, so the tables
// are owned by the APP role, and later adopted --owner-dsn. Provisioning never
// reassigns table ownership, so the new owner pool owns nothing. Measured: the revoke
// no-ops, the app keeps TRUNCATE, and (when the owner holds no privileges at all) the
// statement is a hard error instead.
//
// Refusing here turns both variants into one boot failure that names ownership and its
// remedy, instead of an attestation nobody can rely on.
func requireTableOwnership(ctx context.Context, db dialect.Querier, tables []string) error {
	if len(tables) == 0 {
		return nil
	}
	list, args := tableParams([]any{dialect.EngineSchema}, tables)
	// #nosec G202 -- `list` is tableParams' output: ONLY "$2,$3,…" placeholders (appendonly_acl.go:188-197). The table names travel as bound args, the schema as $1
	q := `SELECT c.relname, pg_catalog.pg_get_userbyid(c.relowner), pg_catalog.pg_has_role(current_user, c.relowner, 'USAGE')
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1 AND c.relkind IN ('r','p') AND c.relname IN (` + list + `)`
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return fmt.Errorf("sqlstore: append-only ACL reconcile: ownership check: %w", err)
	}
	defer rows.Close()
	var foreign []string
	for rows.Next() {
		var table, owner string
		var canAdminister bool
		if err := rows.Scan(&table, &owner, &canAdminister); err != nil {
			return fmt.Errorf("sqlstore: append-only ACL reconcile: ownership check: %w", err)
		}
		if !canAdminister {
			foreign = append(foreign, fmt.Sprintf("%s (owned by %s)", table, owner))
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("sqlstore: append-only ACL reconcile: ownership check: %w", err)
	}
	if len(foreign) > 0 {
		sort.Strings(foreign)
		return fmt.Errorf(
			"sqlstore: refusing to start: %w — the DDL connection does not own these append-only tables, so it cannot administer their ACL and every REVOKE it issues would report success while changing nothing: %v. "+
				"This happens when a deployment created its schema single-role and later added --owner-dsn: provisioning does not reassign table ownership. "+
				"Fix it once with REASSIGN OWNED BY <old owner> TO <owner role> (or ALTER TABLE ... OWNER TO) and boot again",
			store.ErrAppendOnlyACLUnverifiable, foreign)
	}
	return nil
}

// tableParams renders "$2,$3,…" for len(tables) names and returns them as query
// arguments after leading. Table names come from the CATALOG, which permits any text
// at all — a comma, a quote, whatever — so they are BOUND, never packed into a
// delimited string. An earlier revision joined them with commas and split them
// server-side; a legal table named `retained,evidence` was silently dropped from the
// scope and kept its TRUNCATE.
func tableParams(leading []any, tables []string) (string, []any) {
	args := make([]any, 0, len(leading)+len(tables))
	args = append(args, leading...)
	placeholders := make([]string, len(tables))
	for i, t := range tables {
		placeholders[i] = "$" + strconv.Itoa(len(leading)+i+1)
		args = append(args, t)
	}
	return strings.Join(placeholders, ","), args
}

// existingTables returns which of tables exist in the engine's schema.
func existingTables(ctx context.Context, db dialect.Querier, tables []string) (map[string]bool, error) {
	if len(tables) == 0 {
		return map[string]bool{}, nil
	}
	list, args := tableParams([]any{dialect.EngineSchema}, tables)
	// #nosec G202 -- `list` is tableParams' output: ONLY "$2,$3,…" placeholders (appendonly_acl.go:188-197). The table names travel as bound args, the schema as $1
	q := `SELECT c.relname
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1 AND c.relkind IN ('r','p') AND c.relname IN (` + list + `)`
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]bool, len(tables))
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out[name] = true
	}
	return out, rows.Err()
}

// appendOnlyACLRow is one table's effective privilege state for the connected role.
type appendOnlyACLRow struct {
	table                             string
	canSelect, canInsert              bool
	canUpdate, canDelete, canTruncate bool
}

// verifyAppendOnlyACL refuses to start unless the APPLICATION role's effective
// privileges on the append-only tables are what evidence requires: it cannot update,
// delete or truncate ANY of them, and it can read and append to the ones this build
// actually writes.
//
// The two halves cover deliberately different sets. The NEGATIVE half spans the whole
// scope — registry plus whatever the live schema still guards — because a table left
// behind by a module that is no longer built still holds evidence and must not become
// wipeable. The POSITIVE half spans only the REGISTERED tables, because those are the
// ones with an active repository behind them; demanding INSERT on a retired module's
// retained table would refuse a perfectly legitimate deployment for being unable to
// write something nothing writes.
//
// It runs on the application pool, because the question is what THAT connection may
// do; has_table_privilege reports the effective answer, accounting for direct grants,
// role membership, PUBLIC and ownership alike. That coverage is why the check is not
// redundant with the reconcile: a name-targeted REVOKE does not strip a privilege held
// through a group or through PUBLIC, and only reading the effective privilege sees it.
// For the same reason it never demands a DIRECT ACL entry — holding the required DML
// through a group role is a legitimate least-privilege arrangement.
//
// Deliberate limits, stated in the refusal text rather than hidden:
//   - It is a point-in-time attestation. In the single-role topology the constrained
//     role owns the table and can re-grant itself the privilege a statement later;
//     boot then refuses at the NEXT start, which is detection, not prevention.
//   - It says nothing about DROP TABLE, ALTER TABLE ... DISABLE TRIGGER, or a
//     compromised owner DSN in the split. Those are not ACL questions.
func verifyAppendOnlyACL(ctx context.Context, db *sql.DB, dia dialect.Dialect, registered []string) error {
	if dia.Name() != store.EnginePostgres {
		return nil
	}
	tables, _, err := appendOnlyACLScope(ctx, db, dia, registered)
	if err != nil {
		return fmt.Errorf("sqlstore: append-only ACL verification: %w", err)
	}
	if len(tables) == 0 {
		return nil
	}
	active := make(map[string]bool, len(registered))
	for _, t := range registered {
		active[t] = true
	}

	// The privileges are probed by OID off a single catalog row per table, so a
	// concurrent search_path change cannot make the five probes answer about
	// different relations, and the schema is pinned by parameter rather than resolved.
	list, args := tableParams([]any{dialect.EngineSchema}, tables)
	// #nosec G202 -- `list` is tableParams' output: ONLY "$2,$3,…" placeholders (appendonly_acl.go:188-197). The table names travel as bound args, the schema as $1
	q := `SELECT c.relname,
       pg_catalog.has_table_privilege(c.oid, 'SELECT'),
       pg_catalog.has_table_privilege(c.oid, 'INSERT'),
       pg_catalog.has_table_privilege(c.oid, 'UPDATE'),
       pg_catalog.has_table_privilege(c.oid, 'DELETE'),
       pg_catalog.has_table_privilege(c.oid, 'TRUNCATE')
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1 AND c.relkind IN ('r','p') AND c.relname IN (` + list + `)`
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return fmt.Errorf("sqlstore: append-only ACL verification: %w", err)
	}
	defer rows.Close()

	var open, unusable []string
	var seen int
	for rows.Next() {
		var r appendOnlyACLRow
		if err := rows.Scan(&r.table, &r.canSelect, &r.canInsert,
			&r.canUpdate, &r.canDelete, &r.canTruncate); err != nil {
			return fmt.Errorf("sqlstore: append-only ACL verification: %w", err)
		}
		seen++
		var held []string
		for _, p := range []struct {
			name string
			ok   bool
		}{{"UPDATE", r.canUpdate}, {"DELETE", r.canDelete}, {"TRUNCATE", r.canTruncate}} {
			if p.ok {
				held = append(held, p.name)
			}
		}
		if len(held) > 0 {
			open = append(open, fmt.Sprintf("%s: %s", r.table, strings.Join(held, ",")))
		}
		if active[r.table] {
			var missing []string
			for _, p := range []struct {
				name string
				ok   bool
			}{{"SELECT", r.canSelect}, {"INSERT", r.canInsert}} {
				if !p.ok {
					missing = append(missing, p.name)
				}
			}
			if len(missing) > 0 {
				unusable = append(unusable, fmt.Sprintf("%s: %s", r.table, strings.Join(missing, ",")))
			}
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("sqlstore: append-only ACL verification: %w", err)
	}
	if seen != len(tables) {
		// The scope was computed from this same schema moments ago, so a table that
		// cannot be re-resolved now means the answer is incomplete — and an incomplete
		// answer about an evidence boundary is a refusal, never a pass.
		return fmt.Errorf("sqlstore: %w: resolved %d of %d append-only tables in schema %q",
			store.ErrAppendOnlyACLUnverifiable, seen, len(tables), dialect.EngineSchema)
	}
	if len(open) > 0 {
		sort.Strings(open)
		return fmt.Errorf(
			"sqlstore: refusing to start: %w — the application role can still mutate append-only evidence: %v. "+
				"The engine re-asserts this revoke on every boot, so a privilege that survives it was granted "+
				"outside it: to this role directly, to a group role it belongs to, or to PUBLIC. Find the grant "+
				"with: SELECT grantee::regrole, privilege_type FROM pg_class c, aclexplode(c.relacl) "+
				"WHERE c.relname = '<table>'",
			store.ErrAppendOnlyACLOpen, open)
	}
	if len(unusable) > 0 {
		sort.Strings(unusable)
		return fmt.Errorf(
			"sqlstore: refusing to start: %w — the application role cannot read or append evidence: %v. "+
				"Append-only tables need SELECT and INSERT (the engine removes only UPDATE/DELETE/TRUNCATE); "+
				"in an owner/app split grant them from the owner, directly or through a group role",
			store.ErrAppendOnlyGrantMissing, unusable)
	}
	return nil
}
