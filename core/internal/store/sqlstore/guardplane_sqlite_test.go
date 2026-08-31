// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/store"
)

// guardplane_sqlite_test.go proves the v6 control plane on the engine that needs no server.
//
// SQLite is not a consolation prize here. buildCoreMigrations runs on BOTH engines, so a v6
// carrying PostgreSQL SQL would break every SQLite boot — and that failure would arrive at a
// user's first start rather than in CI. Running the REAL Open against SQLite exercises the
// whole bootstrap: the DDL parses, the guards install, the inventory activations and the
// bootstrap receipts commit with the tracking row, and every chain verifies.

// openSQLiteStoreForGuards opens a store on a temporary file and returns a raw handle to the
// same database.
//
// A file rather than :memory: because the raw handle must see the same database, and each
// :memory: connection is its own.
func openSQLiteStoreForGuards(t *testing.T) (*sql.DB, dialect.Dialect) {
	t.Helper()
	dsn := t.TempDir() + "/guards.db"
	st, err := Open(context.Background(), store.Config{Engine: store.EngineSQLite, DSN: dsn}, nil)
	if err != nil {
		t.Fatalf("open the SQLite store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	raw, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open a raw handle: %v", err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	dia, ok := dialect.New(store.EngineSQLite)
	if !ok {
		t.Fatal("no SQLite dialect")
	}
	return raw, dia
}

// TestSQLiteBootCreatesTheGuardControlPlane is the end-to-end bootstrap on SQLite.
//
// It asserts what the v6 migration promises AS A UNIT: the three relations, their
// append-only trigger pairs, one inventory activation per manifest entry, the three metadata
// bootstrap receipts plus the universal v7 completion witness, and a tracking row.
//
// WHAT IT CANNOT SEE, said here because an earlier version of this comment cited a test that
// did not exist: on a CLEAN RUN these counts are identical whether or not the rows shared the
// migration's transaction. The atomicity is therefore pinned where a failure can be injected —
// core/migrate/exec_atomic_test.go, which fails an Exec after its statements have run and
// requires the table, the rows and the tracking row to disappear together.
func TestSQLiteBootCreatesTheGuardControlPlane(t *testing.T) {
	t.Parallel()
	raw, dia := openSQLiteStoreForGuards(t)
	ctx := context.Background()

	for _, table := range dialect.GuardControlPlaneTables() {
		cols, err := dia.TableColumns(ctx, raw, table)
		if err != nil {
			t.Fatalf("introspect %s: %v", table, err)
		}
		if len(cols) == 0 {
			t.Fatalf("the boot did not create %s", table)
		}
		// The append-only trigger PAIR, by name. SQLite has no enable state, so the pair IS
		// the guarantee 'A' buys on PostgreSQL: it applies to every connection.
		for _, suffix := range []string{"_no_update", "_no_delete"} {
			var n int
			if err := raw.QueryRowContext(ctx,
				"SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND name=?", table+suffix).Scan(&n); err != nil {
				t.Fatalf("probe trigger %s%s: %v", table, suffix, err)
			}
			if n != 1 {
				t.Errorf("%s%s: found %d triggers, want 1", table, suffix, n)
			}
		}
	}

	// One activation per manifest entry, and the manifest is derived from the registry rather
	// than from a constant — so this compares two derivations of the same census instead of
	// freezing a number.
	m, err := buildGuardManifest(newRegistryForTest(t).appendOnlyTables())
	if err != nil {
		t.Fatalf("build the manifest: %v", err)
	}
	var activations int
	if err := raw.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM "+dialect.GuardInventoryEventsTable+" WHERE kind='activate'").Scan(&activations); err != nil {
		t.Fatalf("count activations: %v", err)
	}
	if activations != len(m.Specs) {
		t.Errorf("the inventory holds %d activations for a manifest of %d entries", activations, len(m.Specs))
	}
	t.Logf("GUARD_BOOTSTRAP_SQLITE|entries=%d|activations=%d", len(m.Specs), activations)

	var bootstrapReceipts int
	if err := raw.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM "+dialect.GuardReceiptsTable+" WHERE receipt_kind='bootstrap'").Scan(&bootstrapReceipts); err != nil {
		t.Fatalf("count bootstrap receipts: %v", err)
	}
	if want := len(dialect.GuardControlPlaneTables()) + 1; bootstrapReceipts != want {
		t.Errorf("found %d bootstrap receipts, want one per control-plane relation plus v7 completion (%d)", bootstrapReceipts, want)
	}
	history, err := verifyGuardEditionHistory(ctx, raw, dia, m)
	if err != nil {
		t.Fatalf("verify completed fresh v7 history: %v", err)
	}
	if history.Kind != guardEditionHistoryCurrentCompleted {
		t.Fatalf("fresh SQLite history = %s, want %s", history.Kind, guardEditionHistoryCurrentCompleted)
	}

	var version int
	var name, phase string
	if err := raw.QueryRowContext(ctx,
		"SELECT version, name, phase FROM "+coreTrackingTable+" WHERE version = ?", guardControlPlaneVersion).
		Scan(&version, &name, &phase); err != nil {
		t.Fatalf("read the v%d tracking row: %v", guardControlPlaneVersion, err)
	}
	if name != guardControlPlaneName || phase != "expand" {
		t.Errorf("v%d is tracked as %q/%q, want %q/expand", version, name, phase, guardControlPlaneName)
	}

	// Every chain verifies. This is the assertion that a stored digest is not decoration: the
	// fold recomputes each one from the row's own contents.
	revision, retained, err := verifyInventoryChain(ctx, raw, dia, m)
	if err != nil {
		t.Fatalf("the inventory chain does not verify: %v", err)
	}
	empty, err := emptyRetainedDigest()
	if err != nil {
		t.Fatal(err)
	}
	if revision != 0 || retained != empty {
		t.Errorf("after activations only, the retained pair is (%d,%s); activating a DECLARED entry must not advance the retained history",
			revision, hexDigest(retained))
	}
	rolloutID, err := guardRolloutID(m.Format, m.CodeEpoch, m.CodeSHA256, 0, empty)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := guardRolloutReceipts(ctx, raw, dia, rolloutID); err != nil {
		t.Fatalf("the receipt chain does not verify: %v", err)
	}
}

// TestSQLiteBootDoesNotOpenARollout pins the deviation from the design, so that it is a
// decision somebody can find rather than a silent difference.
//
// The v6 migration deliberately writes NO pending-opened: at that point in Open the module
// tables the units target do not exist yet, so the expected unit set is not derivable. On
// SQLite the coordinator returns early — the runner is PostgreSQL-only — so after a SQLite
// boot the gate log is EMPTY, and that is correct rather than incomplete.
func TestSQLiteBootDoesNotOpenARollout(t *testing.T) {
	t.Parallel()
	raw, _ := openSQLiteStoreForGuards(t)
	var events int
	if err := raw.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM "+dialect.GuardGateEventsTable).Scan(&events); err != nil {
		t.Fatalf("count gate events: %v", err)
	}
	if events != 0 {
		t.Errorf("a SQLite boot wrote %d gate events; the runner is PostgreSQL-only and the rollout is opened by the coordinator, which returns early here", events)
	}
}

// TestSQLiteBootIsIdempotent pins that a second Open changes nothing.
//
// migrate.Apply skips a recorded version, so the bootstrap must not run twice — and if it
// did, the inventory would gain a second set of activations and the receipts would either
// conflict or duplicate. Counting before and after is what makes "skipped" a measurement.
func TestSQLiteBootIsIdempotent(t *testing.T) {
	t.Parallel()
	dsn := t.TempDir() + "/guards.db"
	ctx := context.Background()

	count := func() (int, int) {
		raw, err := sql.Open("sqlite", dsn)
		if err != nil {
			t.Fatalf("raw open: %v", err)
		}
		defer func() { _ = raw.Close() }()
		var inv, rec int
		if err := raw.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+dialect.GuardInventoryEventsTable).Scan(&inv); err != nil {
			t.Fatalf("count inventory: %v", err)
		}
		if err := raw.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+dialect.GuardReceiptsTable).Scan(&rec); err != nil {
			t.Fatalf("count receipts: %v", err)
		}
		return inv, rec
	}

	st1, err := Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: dsn}, nil)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	inv1, rec1 := count()
	if err := st1.Close(); err != nil {
		t.Fatalf("close the first store: %v", err)
	}

	st2, err := Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: dsn}, nil)
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	inv2, rec2 := count()
	if err := st2.Close(); err != nil {
		t.Fatalf("close the second store: %v", err)
	}

	if inv1 != inv2 || rec1 != rec2 {
		t.Errorf("a second boot changed the control plane: inventory %d -> %d, receipts %d -> %d",
			inv1, inv2, rec1, rec2)
	}
	t.Logf("GUARD_SECOND_BOOT_SQLITE|inventory=%d|receipts=%d", inv2, rec2)
}

// TestSQLiteGuardStatementsCarryNoPostgresTokens is regression 9 of the wiring design.
//
// The SQLite implementation must be a real one rather than PostgreSQL SQL that happens to
// parse. The tokens are not all fatal in the same way, and pretending they are would be a false
// claim: pg_catalog does not exist, a DO block does not parse, REVOKE has nothing to revoke
// from, and octet_length is not a SQLite function — but BYTEA would PARSE, because SQLite
// accepts arbitrary declared type names and applies its own affinity rules. It is listed
// anyway, as a marker that PostgreSQL DDL was copied across rather than as a parse failure.
//
// The comparison is case-INSENSITIVE. SQLite treats keywords without regard to case, so a
// lower-case `revoke ...` would be exactly as broken and a case-sensitive scan would have let
// it through.
func TestSQLiteGuardStatementsCarryNoPostgresTokens(t *testing.T) {
	t.Parallel()
	dia, ok := dialect.New(store.EngineSQLite)
	if !ok {
		t.Fatal("no SQLite dialect")
	}
	stmts := dia.GuardControlPlaneStmts()
	if len(stmts) == 0 {
		t.Fatal("the SQLite control plane renders no statements")
	}
	for _, forbidden := range []string{"pg_catalog", "bytea", "do $", "revoke", "enable always", "octet_length"} {
		for i, stmt := range stmts {
			if strings.Contains(strings.ToLower(stmt), forbidden) {
				t.Errorf("SQLite statement %d contains %q, which SQLite cannot execute (or is PostgreSQL DDL copied across):\n%s", i, forbidden, stmt)
			}
		}
	}
	if got := dia.GuardMetadataACLStmts(); got != nil {
		t.Errorf("SQLite renders %d metadata ACL statements; it has no role layer and no TRUNCATE", len(got))
	}
}

// newRegistryForTest builds the same closed registry Open builds, with no modules.
//
// It exists so a test can derive the census the SAME way production does rather than reading
// it back from the database it is checking — comparing a value against itself would pass
// whatever the value was.
func newRegistryForTest(t *testing.T) *registry {
	t.Helper()
	reg := newRegistry()
	for _, d := range coreDescriptors() {
		if err := reg.registerCore(d); err != nil {
			t.Fatalf("register core descriptor: %v", err)
		}
	}
	reg.closed = true
	return reg
}
