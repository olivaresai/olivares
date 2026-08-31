// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

package sqlstore

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/store"
)

// C4-15: DROPPING THE GUARD USED TO DROP THE OBLIGATION TO HAVE ONE.
//
// The append-only ACL scope was `registry ∪ (tables carrying the immutability trigger)`.
// The second half is there on purpose — a module dropped from a build leaves its tables
// behind because they hold retained evidence, and those are precisely the tables nobody
// writes any more and everybody still relies on. But deriving membership from the trigger
// makes the set self-erasing: DROP TRIGGER removes the table from the set whose privileges
// are re-asserted and verified, the completeness guard then computes `seen == len(tables)`
// over the ALREADY SHRUNK set and passes, and a later GRANT TRUNCATE (or a restore that
// replays an older ACL) leaves the application role able to erase a retained ledger. Every
// boot after that is green.
//
// THE FIXTURE IS THE RETIRED MODULE, deliberately, and not one of the engine's own
// relations. control_rollout_transitions would be re-guarded by
// reconcileRolloutEvidenceGuards before the scope is ever computed — which is correct
// behavior and makes it useless as a probe. The three guard control-plane relations are
// re-verified by verifyGuardControlPlaneObjects and fail closed there already (measured on
// 17.10: "the control plane's own guard ... is not the declared object"). What is left, and
// what this reproduces, is the table of a module that is no longer in the build: nothing
// re-emits its guard, and before this fix nothing noticed it was gone.
func TestPostgresDroppingAGuardDoesNotShrinkTheAppendOnlyScope(t *testing.T) {
	t.Parallel()
	dsns := isolatedPG(t)
	ctx := context.Background()

	cfg := store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 4}

	// Boot 1: the schema, the guard function and the scope inventory come into existence.
	st, err := Open(ctx, cfg, registerWidget)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	_ = st.Close()

	raw, err := sql.Open("pgx", dsns.App)
	if err != nil {
		t.Fatalf("raw pool: %v", err)
	}
	defer func() { _ = raw.Close() }()

	// The retired module's table, guarded exactly as the engine guards its own: this is what
	// a build that once carried the module left behind.
	const retired = "olv_retired_evidence"
	for _, stmt := range []string{
		"CREATE TABLE IF NOT EXISTS " + retired + " (id bigint PRIMARY KEY, payload text)",
		"CREATE OR REPLACE TRIGGER " + retired + "_immutable BEFORE UPDATE OR DELETE ON " + retired +
			" FOR EACH ROW EXECUTE FUNCTION " + dialect.BlockMutationFn + "()",
	} {
		if _, err := raw.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("plant the retired module's table (%s): %v", stmt, err)
		}
	}

	// Boot 2: the catalog admits it to the scope, and the inventory records that admission.
	st2, err := Open(ctx, cfg, registerWidget)
	if err != nil {
		t.Fatalf("open with the retired table present: %v", err)
	}
	_ = st2.Close()

	var inInventory int
	if err := raw.QueryRowContext(ctx,
		"SELECT count(*) FROM "+dialect.ControlAppendOnlyScopeTable+" WHERE table_name = $1",
		retired).Scan(&inInventory); err != nil {
		t.Fatalf("read the scope inventory: %v", err)
	}
	if inInventory != 1 {
		t.Fatalf("the retired module's guarded table was not admitted to %s; the inventory cannot "+
			"detect a loss it never recorded", dialect.ControlAppendOnlyScopeTable)
	}
	// And it is protected: this is the state the drop is about to attack.
	assertNoWriteMutations(t, ctx, raw, retired)

	// THE ATTACK, and it is one statement by the role that owns the table — which in the
	// default single-role topology is the application role itself.
	if _, err := raw.ExecContext(ctx, "DROP TRIGGER "+retired+"_immutable ON "+retired); err != nil {
		t.Fatalf("drop the guard: %v", err)
	}

	// Boot 3 must refuse. Before the fix it was green, and the table had silently left the
	// set whose privileges anything checks.
	_, err = Open(ctx, cfg, registerWidget)
	if err == nil {
		t.Fatalf("boot was GREEN after the append-only guard was dropped from %q. That table has left "+
			"the scope every later boot re-asserts and verifies, so a GRANT TRUNCATE — or a restore "+
			"replaying an older ACL — now lets the application role erase a retained evidence ledger "+
			"with nothing reporting it", retired)
	}
	if !strings.Contains(err.Error(), retired) {
		t.Fatalf("the refusal must NAME the relation whose guard went missing, because the operator's "+
			"next action is a statement about one table; got %v", err)
	}
}

// TestPostgresTheScopeInventoryCannotBeQuietlyForgotten covers the level above: the
// inventory is the memory, so erasing the memory must not erase the obligations.
//
// Reading a missing inventory as "nothing was ever in scope" would reproduce the same
// fail-open one layer up — which is exactly the shape of the defect being fixed, so it is
// worth a test of its own rather than a comment.
func TestPostgresTheScopeInventoryCannotBeQuietlyForgotten(t *testing.T) {
	t.Parallel()
	dsns := isolatedPG(t)
	ctx := context.Background()
	cfg := store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 4}

	st, err := Open(ctx, cfg, registerWidget)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	_ = st.Close()

	raw, err := sql.Open("pgx", dsns.App)
	if err != nil {
		t.Fatalf("raw pool: %v", err)
	}
	defer func() { _ = raw.Close() }()

	// A read-only consumer must refuse a missing inventory. The boot's reconcile leg creates
	// it — deliberately the only leg that may — so this asserts the VERIFY side's rule
	// directly rather than through a boot that would just re-create it.
	if _, err := raw.ExecContext(ctx, "DROP TABLE "+dialect.ControlAppendOnlyScopeTable); err != nil {
		t.Fatalf("drop the inventory: %v", err)
	}
	// pgtest.Isolate provisions the app role as dialect.DefaultAppRole, which it refuses to
	// vary; the role only decides which name the revoke targets and this test issues none.
	dia, ok := dialect.NewForAppRole(store.EnginePostgres, dialect.DefaultAppRole)
	if !ok {
		t.Fatalf("dialect for %q", dialect.DefaultAppRole)
	}
	if _, err := readAppendOnlyScopeInventory(ctx, raw, dia); err == nil {
		t.Fatal("a missing scope inventory was read as an EMPTY one: erasing the record of what must " +
			"be protected would then erase the protection, which is the same fail-open one level up")
	}
}

// assertNoWriteMutations fails unless the connecting role is denied UPDATE, DELETE and
// TRUNCATE on the table — the boundary the append-only ACL exists to hold.
func assertNoWriteMutations(t *testing.T, ctx context.Context, db *sql.DB, table string) {
	t.Helper()
	for _, priv := range []string{"UPDATE", "DELETE", "TRUNCATE"} {
		var held bool
		if err := db.QueryRowContext(ctx,
			"SELECT pg_catalog.has_table_privilege($1, $2)", table, priv).Scan(&held); err != nil {
			t.Fatalf("probe %s on %s: %v", priv, table, err)
		}
		if held {
			t.Fatalf("%s still holds %s on %s before the drop; the fixture is not in the state this "+
				"test is about", "the application role", priv, table)
		}
	}
}
