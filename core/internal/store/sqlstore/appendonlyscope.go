// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

package sqlstore

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/store"
)

// THE MEMBERSHIP OF THE APPEND-ONLY SCOPE STOPS BEING DERIVED FROM THE OBJECT IT PROTECTS.
//
// appendOnlyACLScope used to answer "which tables must deny UPDATE/DELETE/TRUNCATE to the
// application role?" with `registry ∪ (tables carrying the immutability trigger)`. The
// union exists for a good reason — a module dropped from a build leaves its tables behind
// on purpose, because they hold retained evidence, and a set taken from the registry alone
// would stop protecting exactly the rows nobody writes any more. But the second half of
// that union derives membership FROM THE TRIGGER, so a single `DROP TRIGGER` removed the
// table from the set of tables whose protection is checked. Circular by construction:
// destroying the guard destroys the obligation to have one.
//
// MEASURED (PostgreSQL 17.10, single-role topology, the application role owning its tables,
// which is the default the harness provisions): with `retired_evidence` guarded and revoked
// (privileges f|f|f), `DROP TRIGGER retired_evidence_immutable` removes it from the scope
// query; the completeness guard of verifyAppendOnlyACL then computes `seen == len(tables)`
// over the ALREADY SHRUNK set and passes; a subsequent `GRANT TRUNCATE` — or a restore that
// replays an older ACL — leaves the application role able to TRUNCATE a retained evidence
// ledger, and the next boot is green. Two of the three steps are ordinary DBA operations.
//
// THE FIX IS TO CHANGE THE SOURCE OF MEMBERSHIP, not to add another check over the same
// source. The catalog becomes a source of ADMISSION only: a table that carries the guard
// joins the scope and is recorded durably. It is never a source of REMOVAL. A table in the
// inventory that still exists and no longer carries the guard is not out of scope — it is a
// DIVERGENCE, named and refused, which is what the guard control plane's own inventory
// chain already does for its three relations (verifyInventoryChain: "an activation this
// edition does not declare refuses the boot").
//
// WHAT THIS DOES NOT CLOSE, said plainly: a `DROP TABLE` still removes the relation, and
// this file treats an absent relation as not its business — the migration step owns
// creating tables and a REVOKE on a missing one is a hard error that would mask that
// failure. That limit was already declared in appendonly_acl.go and it is unchanged. What
// changes is that DROP TRIGGER is no longer a quiet way to reach the same outcome.
//
// POSTGRES ONLY, and that is the engine the defect lives on: SQLite's AppendOnlyACLTables
// returns nothing at all (it has no role layer, so its triggers ARE the boundary and they
// apply to every connection including the engine's own), and both callers of this scope
// already return early for it.

// appendOnlyScopeDDL creates the durable inventory of everything that has ever been in the
// append-only ACL scope.
//
// One row per table, PRIMARY KEY on the name: re-admitting a table it already knows is a
// no-op the shape enforces rather than the code remembering to. It carries the same
// append-only guard the relations it tracks do — see reconcileRolloutEvidenceGuards — for
// the obvious reason that an inventory of what must not be erased is itself something that
// must not be erased.
const appendOnlyScopeDDL = "CREATE TABLE IF NOT EXISTS " + dialect.ControlAppendOnlyScopeTable + ` (
	table_name TEXT PRIMARY KEY,
	first_seen_at TEXT NOT NULL
)`

// ensureAppendOnlyScopeTable creates the inventory. It runs on the OWNER pool, inside the
// migration lock, before anything reads the scope.
func ensureAppendOnlyScopeTable(ctx context.Context, ownerDB dialect.Execer, dia dialect.Dialect) error {
	if dia.Name() != store.EnginePostgres {
		return nil
	}
	if _, err := ownerDB.ExecContext(ctx, appendOnlyScopeDDL); err != nil {
		return fmt.Errorf("sqlstore: append-only scope inventory: %w", err)
	}
	return nil
}

// readAppendOnlyScopeInventory returns every table the inventory records.
//
// A MISSING INVENTORY IS AN ERROR, not an empty set, and that is deliberate. This relation
// is created inside the migration lock on every boot, so by the time either caller reads it
// it exists — unless something dropped it. Reading "absent" as "nothing was ever in scope"
// would restore the exact fail-open this file exists to remove, one level up: erase the
// memory and the obligations it holds evaporate with it.
func readAppendOnlyScopeInventory(ctx context.Context, db dialect.Querier, dia dialect.Dialect) ([]string, error) {
	present, err := existingTables(ctx, db, []string{dialect.ControlAppendOnlyScopeTable})
	if err != nil {
		return nil, fmt.Errorf("sqlstore: locate the append-only scope inventory: %w", err)
	}
	if !present[dialect.ControlAppendOnlyScopeTable] {
		return nil, fmt.Errorf("%w: the append-only scope inventory %q does not exist. It is created inside the migration lock on every boot, so its absence means it was dropped after this boot created it — and reading that as \"nothing is in scope\" is precisely the fail-open the inventory replaced. Restore it, or drop the whole schema deliberately if this database is being decommissioned",
			store.ErrAppendOnlyACLOpen, dialect.ControlAppendOnlyScopeTable)
	}
	rows, err := db.QueryContext(ctx, "SELECT table_name FROM "+dialect.ControlAppendOnlyScopeTable)
	if err != nil {
		return nil, fmt.Errorf("sqlstore: read the append-only scope inventory: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, fmt.Errorf("sqlstore: read the append-only scope inventory: %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlstore: read the append-only scope inventory: %w", err)
	}
	sort.Strings(out)
	return out, nil
}

// admitToAppendOnlyScope records every discovered table the inventory does not already
// hold. Admission only: nothing here ever removes a row.
//
// It runs on the OWNER pool because it writes, and inside the migration lock because that
// is the only place a boot may.
func admitToAppendOnlyScope(ctx context.Context, ownerDB dialect.Execer, dia dialect.Dialect, discovered []string) error {
	if dia.Name() != store.EnginePostgres {
		return nil
	}
	known, err := readAppendOnlyScopeInventory(ctx, ownerDB, dia)
	if err != nil {
		return err
	}
	have := make(map[string]bool, len(known))
	for _, t := range known {
		have[t] = true
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	ins := dia.Rebind("INSERT INTO " + dialect.ControlAppendOnlyScopeTable +
		" (table_name, first_seen_at) VALUES (?, ?)")
	for _, t := range discovered {
		if have[t] {
			continue
		}
		if _, err := ownerDB.ExecContext(ctx, ins, t, now); err != nil {
			return fmt.Errorf("sqlstore: admit %q to the append-only scope inventory: %w", t, err)
		}
		have[t] = true
	}
	return nil
}

// appendOnlyScopeDivergence names every table the inventory says must be guarded, that
// still EXISTS, and that the live schema no longer presents as guarded or registered.
//
// The "still exists" condition is what keeps this from firing on a DROP TABLE, which is a
// declared limit of this file and a different failure with a different remedy.
func appendOnlyScopeDivergence(inventory []string, discovered map[string]bool, present map[string]bool) []string {
	var gone []string
	for _, t := range inventory {
		if discovered[t] || !present[t] {
			continue
		}
		gone = append(gone, t)
	}
	sort.Strings(gone)
	return gone
}

// errAppendOnlyScopeShrank renders the refusal. It names the tables rather than reporting a
// count, because the operator's next action is a statement about ONE relation.
func errAppendOnlyScopeShrank(gone []string) error {
	return fmt.Errorf("%w: %v still exist and no longer carry the append-only immutability guard, although this database's scope inventory records that they had it. The guard is what puts a table in the set whose privileges are re-asserted and verified every boot, so dropping it USED to remove the table from that set silently — a shrinking scope reported as a complete one. It is now a refusal. Either restore the guard (a boot with the relation registered will re-emit it), or, if these tables are genuinely no longer evidence, remove them from %s deliberately",
		store.ErrAppendOnlyACLOpen, gone, dialect.ControlAppendOnlyScopeTable)
}
