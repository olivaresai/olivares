// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

package sqlstore

import (
	"context"
	"database/sql"
	"testing"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/store"
)

// THE PROVISIONING REPAIR HAD THE SAME CIRCULARITY AND NO TEST THAT EXECUTED IT.
//
// AppendOnlyCatalogRevokeStmt is what `db init` runs to re-assert the append-only ACL from
// the catalog. It derived its membership from the immutability trigger too — so a relation
// whose guard had been dropped was skipped by the REPAIR as well, and an operator running
// provisioning precisely to fix an ACL would be told nothing was wrong. The sweep that
// found C4-15 did not mention this second site.
//
// It also had NO test that ran it against a server: the only existing coverage asserts the
// rendered SQL as a string (dialect/appendonly_acl_test.go). A rendered statement that no
// server ever parses is a statement whose syntax is an assumption — and this one is a
// PL/pgSQL DO block that now contains a conditional and a dynamic query, which is exactly
// the shape where a string test says nothing. Both halves are exercised here.
func TestPostgresTheProvisioningRevokeReachesATableWhoseGuardIsGone(t *testing.T) {
	t.Parallel()
	dsns := isolatedPG(t)
	ctx := context.Background()

	st, err := Open(ctx, store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 4}, registerWidget)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	_ = st.Close()

	raw, err := sql.Open("pgx", dsns.App)
	if err != nil {
		t.Fatalf("raw pool: %v", err)
	}
	defer func() { _ = raw.Close() }()

	// A retired module's table: guarded, recorded, and then stripped of its guard — the
	// state an operator would run provisioning to repair.
	const retired = "olv_prov_retired"
	for _, stmt := range []string{
		"CREATE TABLE IF NOT EXISTS " + retired + " (id bigint PRIMARY KEY)",
		"CREATE OR REPLACE TRIGGER " + retired + "_immutable BEFORE UPDATE OR DELETE ON " + retired +
			" FOR EACH ROW EXECUTE FUNCTION " + dialect.BlockMutationFn + "()",
		// Recorded in the inventory the way a boot would record it.
		"INSERT INTO " + dialect.ControlAppendOnlyScopeTable +
			" (table_name, first_seen_at) VALUES ('" + retired + "', 'seeded') ON CONFLICT DO NOTHING",
		// The guard goes; the row in the inventory stays. This is the whole point.
		"DROP TRIGGER " + retired + "_immutable ON " + retired,
		// And the privileges come back, as a restore replaying an older ACL would leave them.
		"GRANT UPDATE, DELETE, TRUNCATE ON " + retired + " TO " + dialect.DefaultAppRole,
	} {
		if _, err := raw.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("fixture %q: %v", stmt, err)
		}
	}

	// THE STATEMENT UNDER TEST, executed rather than compared to a string.
	if _, err := raw.ExecContext(ctx, dialect.AppendOnlyCatalogRevokeStmt(dialect.DefaultAppRole, "public")); err != nil {
		t.Fatalf("the provisioning revoke failed to execute: %v", err)
	}

	for _, priv := range []string{"UPDATE", "DELETE", "TRUNCATE"} {
		var held bool
		if err := raw.QueryRowContext(ctx,
			"SELECT pg_catalog.has_table_privilege($1, $2, $3)",
			dialect.DefaultAppRole, retired, priv).Scan(&held); err != nil {
			t.Fatalf("probe %s: %v", priv, err)
		}
		if held {
			t.Fatalf("the provisioning repair skipped %q because its immutability trigger was gone — "+
				"which is the state an operator runs the repair to FIX. %s is still held, so the "+
				"repair reports success having reached everything except the one relation that needed it",
				retired, priv)
		}
	}
}

// TestPostgresTheProvisioningRevokeRunsBeforeTheInventoryExists covers the ordering that
// makes the fix above safe: `db init` legitimately runs against a database the engine has
// never booted, so the inventory relation may not exist yet.
//
// This is not a hypothetical about ordering — it is about PL/pgSQL. A reference to a
// missing relation inside a PLANNED query fails when the block runs, whatever the WHERE
// clause says, because the whole statement is planned before any predicate is evaluated.
// The first version of this fix had exactly that bug; it is why the second arm is a
// to_regclass-gated block around a DYNAMIC query rather than a UNION.
func TestPostgresTheProvisioningRevokeRunsBeforeTheInventoryExists(t *testing.T) {
	t.Parallel()
	dsns := isolatedPG(t)
	ctx := context.Background()

	raw, err := sql.Open("pgx", dsns.App)
	if err != nil {
		t.Fatalf("raw pool: %v", err)
	}
	defer func() { _ = raw.Close() }()

	// A database the engine has never opened: no trigger function, no inventory, nothing.
	var exists bool
	if err := raw.QueryRowContext(ctx,
		"SELECT pg_catalog.to_regclass($1) IS NOT NULL", dialect.ControlAppendOnlyScopeTable).Scan(&exists); err != nil {
		t.Fatalf("probe the inventory: %v", err)
	}
	if exists {
		t.Fatalf("this test needs a database where %s does NOT exist; the fixture is not virgin",
			dialect.ControlAppendOnlyScopeTable)
	}

	if _, err := raw.ExecContext(ctx, dialect.AppendOnlyCatalogRevokeStmt(dialect.DefaultAppRole, "public")); err != nil {
		t.Fatalf("the provisioning revoke must run on a database the engine has never booted — "+
			"`db init` is precisely what runs there. Got: %v", err)
	}
}
