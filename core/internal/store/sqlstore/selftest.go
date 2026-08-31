// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"sort"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/store"
)

// runSelfTest verifies, after migrations and before the store serves traffic,
// that every tenant table carries this engine's isolation guard (Postgres:
// FORCE row-level security plus a tenant policy; SQLite: the tripwire triggers).
// A missing guard would be a silent cross-tenant leak, so the store refuses to
// open instead. This converts "someone added a table without isolation" from a
// latent vulnerability into a startup failure.
func runSelfTest(ctx context.Context, db *sql.DB, dia dialect.Dialect, tenantTables []string) error {
	guarded, err := dia.GuardedTables(ctx, db)
	if err != nil {
		return fmt.Errorf("self-test: list guarded tables: %w", err)
	}
	var missing []string
	for _, t := range tenantTables {
		if !guarded[t] {
			missing = append(missing, t)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("self-test: tenant tables missing isolation guard: %v", missing)
	}
	return nil
}

// runSchemaInvariantSelfTest verifies module-declared trigger objects after
// migrations and, in an owner/app split, the app role's exact privileges at
// that trigger boundary. A tracked migration row is not proof that its trigger
// still exists, targets the intended table, or remains executable.
//
// It is deliberately thin: everything it does beyond READING the live catalog
// lives in schemaInvariantViolation, which is pure and therefore provable
// without a PostgreSQL server.
func runSchemaInvariantSelfTest(
	ctx context.Context,
	db *sql.DB,
	dia dialect.Dialect,
	posture dialect.RolePosture,
	invariants []registeredSchemaTrigger,
	boundaryTables []string,
	verifyAppPrivileges bool,
) error {
	if len(invariants) == 0 {
		return nil
	}
	schema, err := dia.SchemaName(ctx, db)
	if err != nil {
		return fmt.Errorf("self-test: resolve schema name: %w", err)
	}
	live, err := dia.SchemaTriggers(ctx, db)
	if err != nil {
		return fmt.Errorf("self-test: list schema triggers: %w", err)
	}
	if err := schemaInvariantViolation(
		schema, posture, live, invariants, boundaryTables, dia.Name(), verifyAppPrivileges,
	); err != nil {
		return err
	}
	if verifyAppPrivileges && dia.Name() == store.EnginePostgres {
		return checkInvariantTablePrivileges(ctx, db, boundaryTables)
	}
	return nil
}

// acceptAlwaysEnabledBoundaryTriggers records an OPEN product decision at the exact
// line where it bites, instead of letting it remain an accident of "it fires, so we
// accept it".
//
// 'A' (ENABLE ALWAYS) genuinely fires under every replication role — that is not in
// question, and the PostgreSQL regression proves it behaviourally. What is undecided
// is whether a deployment that has put a security-boundary trigger in that state
// should be ACCEPTED: an ALWAYS trigger also fires on a subscriber applying replicated
// rows, so a cutover guard there can re-materialize a fact the subscription is already
// delivering — which is exactly why the rollout migration leaves its own triggers at
// 'O'. Refusing it outright would equally be a decision, and a costly one: it would
// reject a database whose guards demonstrably run.
//
// Today: accepted. Owner of the decision: product, together with the
// logical-replication posture. Flipping this one
// constant changes the behavior AND the expectation of the regression that reads it,
// so the decision can be taken without editing a test to match.
const acceptAlwaysEnabledBoundaryTriggers = true

// schemaInvariantViolation is the trigger-boundary decision, expressed as a pure
// function of the live catalog and the declared invariants.
//
// It is separated from the catalog read for one reason: a decision left tangled
// with the query can only be exercised against a live server, and a pure function
// runs on every machine regardless. Here the whole matrix — a trigger absent,
// DISABLED, replica-only, moved to another table, or with its body swapped — is a
// table test that runs everywhere. (The dev container DOES carry a live
// PostgreSQL — see CONTRIBUTING.md — so the server-gated half runs locally too;
// an earlier revision of this comment claimed otherwise, wrongly.)
//
// That does NOT reduce the server-gated surface to one claim. What still needs a real
// server: that PostgreSQL emits the tgenabled characters this maps, that the dialect
// produces one catalog key per (schema, table, name), that this decision is still
// wired into Open, that the states mean what they say when a guard actually runs, and
// that the boundary grants hold in an owner/app split.
//
// The order of the checks is the order of severity, and every category is
// collected before any is reported, so an operator sees the whole boundary's
// state rather than fixing one trigger at a time.
func schemaInvariantViolation(
	schema string,
	posture dialect.RolePosture,
	live map[dialect.TriggerKey]dialect.TriggerInfo,
	invariants []registeredSchemaTrigger,
	boundaryTables []string,
	engine store.Engine,
	verifyAppPrivileges bool,
) error {
	if len(invariants) == 0 {
		return nil
	}
	// PRECONDITION for every firing verdict below. TriggerEnableState.Fires answers
	// "does this run" for an origin/local session; in a replica session the mapping
	// INVERTS ('R' fires, 'O' does not), so trusting it there would turn each verdict
	// into its opposite — and the inert-trigger case would be reported as healthy.
	// The boot guards already refuse a replica session (store.go:105, dbsetup.go:87), so
	// this branch is unreachable through Open today. It is here to make the function
	// TOTAL rather than conditional on a caller ordering a refactor could change — not
	// as new coverage. It also does NOT close the per-connection residual: the posture,
	// the schema name and the catalog are three queries on a POOL and may land on
	// different physical connections, and a sampled posture never proved every one of
	// them (see dialect.RolePosture.ReplicationRole).
	if posture.TriggersDisabled() {
		return fmt.Errorf(
			"self-test: %w: session_replication_role=%q inverts PostgreSQL's firing rules, so a DISABLED guard could be reported as healthy. This should already have been refused at boot",
			store.ErrSchemaBoundaryUnjudgeable, posture.ReplicationRole)
	}
	var missing, inert, tampered, unexecutable []string
	for _, required := range invariants {
		// Look the trigger up by its FULL identity. Keying by name alone would let
		// a same-named trigger on another table stand in for the required one.
		key := dialect.TriggerKey{Schema: schema, Table: required.Table, Name: required.Name}
		info, ok := live[key]
		if !ok {
			missing = append(missing, key.String())
			continue
		}
		// Present but inert is the failure mode a presence check cannot see: the
		// catalog still lists a DISABLED or replica-only trigger. Describe names the
		// exact state and the statement that undoes it — "disabled" alone sends an
		// operator hunting for a DROP that never happened.
		if !info.EnableState.Fires() {
			inert = append(inert, fmt.Sprintf("%s: %s", key, info.EnableState.Describe()))
			continue
		}
		// A firing guard is accepted, EXCEPT that ENABLE ALWAYS is governed by a
		// recorded, still-open product decision rather than by the firing rule alone.
		if info.EnableState == dialect.TriggerFiresAlways && !acceptAlwaysEnabledBoundaryTriggers {
			inert = append(inert, fmt.Sprintf(
				"%s: %s — refused by the recorded logical-replication posture, not by PostgreSQL",
				key, info.EnableState.Describe()))
			continue
		}
		// Present, firing, on the right table — and its body swapped for a no-op.
		// Only the catalog's own text distinguishes that from the real trigger.
		if required.DefinitionSHA256 != "" {
			got := sha256.Sum256([]byte(info.Definition))
			if hex.EncodeToString(got[:]) != required.DefinitionSHA256 {
				tampered = append(tampered, fmt.Sprintf(
					"%s (declared %s, live %s, %d bytes)",
					key, shortDigest(required.DefinitionSHA256),
					shortDigest(hex.EncodeToString(got[:])), len(info.Definition)))
			}
		}
		if verifyAppPrivileges && engine == store.EnginePostgres && !info.CanExecute {
			unexecutable = append(unexecutable,
				fmt.Sprintf("%s via %s", key, info.Function))
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("self-test: %w: %v", store.ErrSchemaTriggerMissing, missing)
	}
	if len(inert) > 0 {
		sort.Strings(inert)
		return fmt.Errorf(
			"self-test: %w — the invariant they enforce is inert: %v",
			store.ErrSchemaTriggerInert, inert)
	}
	if len(tampered) > 0 {
		sort.Strings(tampered)
		return fmt.Errorf(
			"self-test: %w — the object was replaced: %v",
			store.ErrSchemaTriggerTampered, tampered)
	}
	if len(unexecutable) > 0 {
		sort.Strings(unexecutable)
		return fmt.Errorf("self-test: %w: %v", store.ErrSchemaTriggerUnexecutable, unexecutable)
	}
	if verifyAppPrivileges && engine == store.EnginePostgres && len(boundaryTables) == 0 {
		return fmt.Errorf("self-test: %w", store.ErrSchemaBoundaryTableMissing)
	}
	return nil
}

func checkInvariantTablePrivileges(ctx context.Context, db *sql.DB, tables []string) error {
	const query = `SELECT
  pg_catalog.has_table_privilege(quote_ident(pg_catalog.current_schema())||'.'||quote_ident($1), 'SELECT'),
  pg_catalog.has_table_privilege(quote_ident(pg_catalog.current_schema())||'.'||quote_ident($1), 'INSERT')`
	for _, table := range tables {
		var canSelect, canInsert bool
		if err := db.QueryRowContext(ctx, query, table).Scan(&canSelect, &canInsert); err != nil {
			return fmt.Errorf("self-test: app-role invariant privilege check on %q: %w", table, err)
		}
		var missing []string
		if !canSelect {
			missing = append(missing, "SELECT")
		}
		if !canInsert {
			missing = append(missing, "INSERT")
		}
		if len(missing) > 0 {
			return fmt.Errorf(
				"self-test: %w: %v on %q — the guard would fire, but the application could not "+
					"write the fact it guards, so the boundary is only half installed",
				store.ErrSchemaBoundaryGrantMissing, missing, table)
		}
	}
	return nil
}

// shortDigest renders a digest for an error message: enough to correlate with the
// declaration, never the object's contents.
func shortDigest(hexDigest string) string {
	if len(hexDigest) <= 12 {
		return hexDigest
	}
	return hexDigest[:12] + "…"
}
