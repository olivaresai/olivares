// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/migrate"
	"github.com/olivaresai/olivares/core/store"
)

// freshBootstrapModuleFS is a module migration filesystem in the shapes the repository's real
// modules ship: a helper index, a trigger, and a data backfill that creates nothing. It exists
// so the inventory is exercised against a module that registers FILE migrations — the one part
// of the managed set that cannot be derived from descriptors.
var freshBootstrapModuleFS = fstest.MapFS{
	"sqlite/0001_widget_label_unique.sql": &fstest.MapFile{Data: []byte(
		"-- a helper index, the shape modules/eventing/migrations/sqlite/0001 ships\n" +
			"CREATE UNIQUE INDEX rrw_widget_label_uniq ON rrw_widget(tenant_id, label)")},
	"sqlite/0002_widget_guard.sql": &fstest.MapFile{Data: []byte(
		"CREATE TRIGGER IF NOT EXISTS rrw_widget_probe_guard BEFORE DELETE ON rrw_widget\n" +
			"BEGIN SELECT RAISE(ABORT,'probe'); END")},
	"sqlite/0003_widget_backfill.sql": &fstest.MapFile{Data: []byte(
		"UPDATE rrw_widget SET count = 0 WHERE count IS NULL")},
	"postgres/0001_widget_label_unique.sql": &fstest.MapFile{Data: []byte(
		"CREATE UNIQUE INDEX rrw_widget_label_uniq ON rrw_widget(tenant_id, label)")},
	"postgres/0002_widget_backfill.sql": &fstest.MapFile{Data: []byte(
		"UPDATE rrw_widget SET count = 0 WHERE count IS NULL")},
}

// registerFreshBootstrapModule is the widest module profile this package can register: an
// entity, a staged rollout control witnessed by it, and a migration filesystem.
func registerFreshBootstrapModule(reg store.ExtensionRegistry) error {
	if err := reg.Register(widgetDescriptor); err != nil {
		return err
	}
	if err := reg.RolloutControl(testRolloutControl); err != nil {
		return err
	}
	return reg.Migrations("rrw", freshBootstrapModuleFS)
}

// freshBootstrapRegistry closes a registry the way Open does, so a test builds the inventory
// from the same closed declaration the boot uses.
func freshBootstrapRegistry(t *testing.T, register func(store.ExtensionRegistry) error) *registry {
	t.Helper()
	reg := newRegistry()
	for _, d := range coreDescriptors() {
		if err := reg.registerCore(d); err != nil {
			t.Fatal(err)
		}
	}
	// Open registers the core schema invariants here too (store.go). Without them the
	// registry is not the one the boot closes, and the inventory it builds cannot name
	// core v10's retention trigger.
	if err := reg.registerCoreUserAuthorityInvariants(); err != nil {
		t.Fatal(err)
	}
	if register != nil {
		if err := register(reg); err != nil {
			t.Fatal(err)
		}
	}
	reg.closed = true
	if err := reg.validateRolloutControls(); err != nil {
		t.Fatal(err)
	}
	return reg
}

func freshBootstrapPlans(t *testing.T, dia dialect.Dialect, reg *registry) []moduleFileMigrationPlan {
	t.Helper()
	plans, err := prepareModuleFileMigrations(dia, dia.Name(), reg)
	if err != nil {
		t.Fatal(err)
	}
	return plans
}

// sqliteCatalogObjects lists every object of the managed SQLite namespace, excluding the
// engine's own `sqlite_` bookkeeping.
func sqliteCatalogObjects(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()
	rows, err := db.QueryContext(context.Background(),
		`SELECT type, name FROM main.sqlite_master WHERE name NOT LIKE 'sqlite\_%' ESCAPE '\' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var kind, name string
		if err := rows.Scan(&kind, &name); err != nil {
			t.Fatal(err)
		}
		out[name] = kind
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestFreshBootstrapInventoryCoversTheInstalledProfile proves the managed object set is
// COMPLETE for the profile it installs, and it is deliberately not a re-reading of the file that
// declares it.
//
// The inventory is a claim about which identities this build administers, and the whole admission
// rests on it: a name it fails to list is an object a foreign schema could carry into a "fresh"
// install without anybody noticing. Comparing it against the constructors it was built from would
// be circular. So the census comes from the CATALOG of a real installation: open a store, let
// every migration, reconciler and module file migration run, and require that every object left
// behind is one the inventory names.
//
// TWO SCOPE LIMITS, stated rather than implied.
//
//   - The profiles are core-only and ONE SYNTHETIC module. `core` cannot import `modules` — the
//     module packages import core — so no test in this package can install the product's own
//     registration callback. TestFreshBootstrapInventoryReadsTheRepositoryModuleMigrations reads
//     the ACTUAL on-disk module migration files instead, and is a static prepared-plan control
//     rather than a catalog one.
//   - Objects created by a module statement whose effect this build cannot determine are
//     OUTSIDE the inventory by construction (see addModuleStatements). This control passes
//     because the synthetic module's undetermined statement is a row backfill that creates
//     nothing; it is not evidence that such a statement's objects would be covered.
//
// The converse is deliberately NOT asserted. The set legitimately contains identities a fresh
// install never creates — the historical index core v4 drops is the measured example — so it is
// a superset of a fresh census by construction.
func TestFreshBootstrapInventoryCoversTheInstalledProfile(t *testing.T) {
	for _, tc := range []struct {
		name     string
		register func(store.ExtensionRegistry) error
	}{
		{"core-only registrar", nil},
		{"synthetic module registrar with file migrations", registerFreshBootstrapModule},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			cfg, db, dia := accessEvidenceSQLiteStore(t)
			st, err := Open(ctx, cfg, tc.register)
			if err != nil {
				t.Fatalf("fresh Open: %v", err)
			}
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
			reg := freshBootstrapRegistry(t, tc.register)
			set, err := buildManagedObjectSet(dia, coreDescriptors(), reg,
				freshBootstrapPlans(t, dia, reg))
			if err != nil {
				t.Fatalf("build the managed object set: %v", err)
			}
			var missing []string
			for name, kind := range sqliteCatalogObjects(t, db) {
				// The CATALOG's own class decides the namespace, exactly as the production
				// census does: on SQLite a trigger does not share a namespace with a table.
				class := managedClassRelation
				if kind == "trigger" {
					class = managedClassTrigger
				}
				if _, ok := set.lookup(class, name, ""); !ok {
					missing = append(missing, kind+" "+name)
				}
			}
			if len(missing) > 0 {
				sort.Strings(missing)
				t.Fatalf("a real installation of this profile left %d object(s) the managed inventory does not name: %s",
					len(missing), strings.Join(missing, ", "))
			}
		})
	}
}

// ---------------------------------------------------------------------------------------
// Shared fixtures: the logical snapshot, the real pre-v1 checkpoint producers, and the
// assertions every negative case repeats.
// ---------------------------------------------------------------------------------------

// sqliteLogicalSnapshot is the state a refusal must leave untouched: the complete stored DDL of
// every object plus every value of every table, in canonical order.
//
// It is a LOGICAL snapshot on purpose. Page images, the WAL and file permissions are not
// compared, because SQLite applies its own file policy at open and this correction promises
// nothing about them. What it promises is that a refused boot changes no object and no row.
func sqliteLogicalSnapshot(t *testing.T, db *sql.DB) string {
	t.Helper()
	ctx := context.Background()
	var b strings.Builder
	rows, err := db.QueryContext(ctx,
		`SELECT type, name, tbl_name, COALESCE(sql,'') FROM main.sqlite_master ORDER BY type, name`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var kind, name, table, text string
		if err := rows.Scan(&kind, &name, &table, &text); err != nil {
			_ = rows.Close()
			t.Fatal(err)
		}
		fmt.Fprintf(&b, "OBJECT\t%s\t%s\t%s\t%s\n", kind, name, table, text)
		if kind == "table" && !strings.HasPrefix(name, "sqlite_") {
			tables = append(tables, name)
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		t.Fatal(err)
	}
	_ = rows.Close()
	sort.Strings(tables)
	for _, table := range tables {
		trows, err := db.QueryContext(ctx, "SELECT * FROM main."+table)
		if err != nil {
			t.Fatalf("read %s: %v", table, err)
		}
		cols, err := trows.Columns()
		if err != nil {
			_ = trows.Close()
			t.Fatal(err)
		}
		var values []string
		for trows.Next() {
			cells := make([]any, len(cols))
			holders := make([]sql.NullString, len(cols))
			for i := range cells {
				cells[i] = &holders[i]
			}
			if err := trows.Scan(cells...); err != nil {
				_ = trows.Close()
				t.Fatal(err)
			}
			var row []string
			for i := range holders {
				row = append(row, fmt.Sprintf("%s=%q/%t", cols[i], holders[i].String, holders[i].Valid))
			}
			values = append(values, strings.Join(row, "|"))
		}
		if err := trows.Err(); err != nil {
			_ = trows.Close()
			t.Fatalf("read %s: %v", table, err)
		}
		_ = trows.Close()
		sort.Strings(values)
		for _, v := range values {
			fmt.Fprintf(&b, "ROW\t%s\t%s\n", table, v)
		}
	}
	return b.String()
}

// seedStatements runs a fixture's DDL through the existing single-statement helper.
func seedStatements(t *testing.T, db *sql.DB, stmts ...string) {
	t.Helper()
	for _, stmt := range stmts {
		mustExec(t, db, stmt)
	}
}

func sqliteObjectExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var count int
	if err := db.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM main.sqlite_master WHERE name = ?", name).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count > 0
}

// realRolloutCheckpoint commits R through the PRODUCT'S OWN transaction rather than by hand.
//
// This is what makes the §4 table a statement about reachable checkpoints instead of about
// fabricated ones: classifyRolloutControls creates the three relations and seeds every control
// in the single transaction Open uses, so what it leaves behind IS the state a crash between
// that commit and ensureTracking produces.
func realRolloutCheckpoint(t *testing.T, db *sql.DB, dia dialect.Dialect, controls []store.RolloutControl) {
	t.Helper()
	if err := classifyRolloutControls(context.Background(), db, dia, controls, nil); err != nil {
		t.Fatalf("produce the real rollout checkpoint: %v", err)
	}
}

// realTrackerCheckpoint commits T5 through migrate.Apply's own ensureTracking with an empty
// plan: the second preliminary transaction, with no migration applied after it.
func realTrackerCheckpoint(t *testing.T, db *sql.DB, dia dialect.Dialect) {
	t.Helper()
	if err := migrate.Apply(context.Background(), db, dia, coreTrackingTable, nil); err != nil {
		t.Fatalf("produce the real tracker checkpoint: %v", err)
	}
}

// syntheticThreeColumnTracker builds the historical tracker shape.
//
// It is labeled SYNTHETIC deliberately and the label is part of the evidence: this repository
// has not demonstrated a release that leaves a three-column tracker with no history. What the
// ratified frontier admits is the STRUCTURE and its expansion path through the real
// ensureTracking — not a provenance nobody has shown.
func syntheticThreeColumnTracker(t *testing.T, db *sql.DB) {
	t.Helper()
	mustExec(t, db, "CREATE TABLE "+coreTrackingTable+
		" (version INTEGER PRIMARY KEY, name TEXT NOT NULL, applied_at TEXT NOT NULL)")
}

func trackerColumnCount(t *testing.T, db *sql.DB) int {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), "PRAGMA main.table_xinfo("+coreTrackingTable+")")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		count++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return count
}

func coreVersionCount(t *testing.T, db *sql.DB) int {
	t.Helper()
	if !sqliteObjectExists(t, db, coreTrackingTable) {
		return -1
	}
	var n int
	if err := db.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM "+coreTrackingTable).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// exactOrgsDDL renders the orgs table from the descriptor the engine itself uses, so the
// "precreated but EXACTLY right" case is exact rather than approximately so.
func exactOrgsDDL(t *testing.T, dia dialect.Dialect) []string {
	t.Helper()
	for _, d := range coreDescriptors() {
		if d.Table == "orgs" {
			return dia.CreateTableStmts(d)
		}
	}
	t.Fatal("no orgs descriptor")
	return nil
}

// ---------------------------------------------------------------------------------------
// The refusals.
// ---------------------------------------------------------------------------------------

// TestFreshBootstrapRefusesAPrecreatedManagedObject is the reproduction the independent review
// of cb1b5577 recorded, plus the family it belongs to.
//
// The measured defect: an empty tracker beside `orgs(review TEXT NOT NULL)` holding a row was
// classified `fresh-empty`, and so were a deliberately malformed empty `orgs` and an exactly
// precreated one. `maxVersion == 0` plus three absent families says nothing about the rest of
// the schema, and the boot went on from there.
//
// Each case asserts four separate things, because "it failed" is not the property:
//
//   - the refusal names ErrGuardManifestNoEdge rather than surfacing as a later DDL conflict;
//   - the three rollout relations were NOT created, so the first product commit never happened;
//   - the tracker is exactly as it was found — no ALTER, no new column, no version row;
//   - the complete logical snapshot is byte-identical, so the foreign object and its rows are
//     preserved rather than adopted, renamed or dropped.
func TestFreshBootstrapRefusesAPrecreatedManagedObject(t *testing.T) {
	for _, tc := range []struct {
		name     string
		register func(store.ExtensionRegistry) error
		seed     func(*testing.T, *sql.DB, dialect.Dialect)
	}{
		{
			name: "orgs in a foreign shape holding a row, beside an empty tracker",
			seed: func(t *testing.T, db *sql.DB, dia dialect.Dialect) {
				realTrackerCheckpoint(t, db, dia)
				seedStatements(t, db,
					"CREATE TABLE orgs (review TEXT NOT NULL)",
					"INSERT INTO orgs (review) VALUES ('preserve-me')")
			},
		},
		{
			name: "orgs in a foreign shape, empty",
			seed: func(t *testing.T, db *sql.DB, dia dialect.Dialect) {
				seedStatements(t, db, "CREATE TABLE orgs (review TEXT NOT NULL)")
			},
		},
		{
			// NOT called corruption. It may be a deliberate hand provision — and that is
			// exactly why it is refused: a table this build did not create is not a proven
			// prefix of this Open, however right its DDL looks.
			name: "orgs precreated with this build's exact contract",
			seed: func(t *testing.T, db *sql.DB, dia dialect.Dialect) {
				seedStatements(t, db, exactOrgsDDL(t, dia)...)
			},
		},
		{
			name: "the SQLite tenancy pin without core v1",
			seed: func(t *testing.T, db *sql.DB, dia dialect.Dialect) {
				seedStatements(t, db, dia.TenancyStmts()...)
			},
		},
		{
			name: "the module tracking table without core history",
			seed: func(t *testing.T, db *sql.DB, dia dialect.Dialect) {
				seedStatements(t, db, "CREATE TABLE "+moduleTablesTracking+
					" (table_name TEXT PRIMARY KEY, applied_at TEXT NOT NULL)")
			},
		},
		{
			name:     "a declared module's migration tracker without core history",
			register: registerFreshBootstrapModule,
			seed: func(t *testing.T, db *sql.DB, dia dialect.Dialect) {
				seedStatements(t, db, "CREATE TABLE schema_migrations_mod_rrw"+
					" (version INTEGER PRIMARY KEY, name TEXT NOT NULL, applied_at TEXT NOT NULL)")
			},
		},
		{
			name: "the audit ledger without core v3",
			seed: func(t *testing.T, db *sql.DB, dia dialect.Dialect) {
				seedStatements(t, db, "CREATE TABLE "+dialect.AuditEventsTable+" (probe TEXT)")
			},
		},
		{
			// A VIEW is refused for the same reason a table is: the name is the collision,
			// and `CREATE TABLE orgs` fails against either.
			name: "a view wearing a managed name",
			seed: func(t *testing.T, db *sql.DB, dia dialect.Dialect) {
				seedStatements(t, db, "CREATE VIEW orgs AS SELECT 1 AS review")
			},
		},
		{
			// SQLite resolves identifiers case-insensitively over ASCII, so ORGS IS orgs
			// to this engine. A census that compared bytes would have called this foreign.
			name: "a managed name in a different case, which SQLite resolves as the same object",
			seed: func(t *testing.T, db *sql.DB, dia dialect.Dialect) {
				seedStatements(t, db, "CREATE TABLE ORGS (review TEXT NOT NULL)")
			},
		},
		{
			// The index core v4 drops UNQUALIFIED. A foreign table may carry that name today
			// and a v1..v9 run would destroy it, so admitting this bootstrap would make the
			// preservation promise false.
			name: "a foreign index wearing the name core v4 drops",
			seed: func(t *testing.T, db *sql.DB, dia dialect.Dialect) {
				seedStatements(t, db,
					"CREATE TABLE review_unrelated (value TEXT NOT NULL)",
					"INSERT INTO review_unrelated (value) VALUES ('preserve-me')",
					"CREATE INDEX federation_configs_scope_uniq ON review_unrelated(value)")
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			cfg, db, dia := accessEvidenceSQLiteStore(t)
			tc.seed(t, db, dia)
			before := sqliteLogicalSnapshot(t, db)
			trackerColumnsBefore := -1
			if sqliteObjectExists(t, db, coreTrackingTable) {
				trackerColumnsBefore = trackerColumnCount(t, db)
			}

			st, err := Open(ctx, cfg, tc.register)
			if st != nil {
				_ = st.Close()
			}
			t.Logf("REFUSAL|%v", err)
			if err == nil {
				t.Fatal("Open admitted a managed object that predates every core migration")
			}
			if !errors.Is(err, ErrGuardManifestNoEdge) &&
				!errors.Is(err, ErrGuardControlPlaneBootstrapInconsistent) {
				t.Fatalf("Open error = %v, want the boot classification's own refusal", err)
			}
			for _, table := range []string{
				dialect.ControlRolloutStateTable,
				dialect.ControlRolloutTransitionTable,
				dialect.ControlRolloutClassificationTable,
			} {
				if sqliteObjectExists(t, db, table) {
					t.Fatalf("the refusal created %s: the first product commit happened anyway", table)
				}
			}
			if trackerColumnsBefore >= 0 && trackerColumnCount(t, db) != trackerColumnsBefore {
				t.Fatalf("the refusal altered %s: columns %d -> %d",
					coreTrackingTable, trackerColumnsBefore, trackerColumnCount(t, db))
			}
			if got := sqliteLogicalSnapshot(t, db); got != before {
				t.Fatalf("the refusal changed durable state.\n--- before ---\n%s\n--- after ---\n%s", before, got)
			}
		})
	}
}

// TestFreshBootstrapPreservesUnrelatedObjects is the other half of the contract, and it is the
// half a stricter check would silently break.
//
// A foreign table with rows must not block an install, on a virgin database or beside an admitted
// tracker checkpoint, and reopening must leave it alone. A name that merely shares a prefix with
// this build's own is not evidence of anything.
func TestFreshBootstrapPreservesUnrelatedObjects(t *testing.T) {
	for _, tc := range []struct {
		name string
		seed func(*testing.T, *sql.DB, dialect.Dialect)
	}{
		{
			name: "a virgin namespace holding one foreign table",
			seed: func(t *testing.T, db *sql.DB, dia dialect.Dialect) {},
		},
		{
			name: "the same, beside the tracker checkpoint ensureTracking really produces",
			seed: func(t *testing.T, db *sql.DB, dia dialect.Dialect) {
				realTrackerCheckpoint(t, db, dia)
			},
		},
		{
			name: "a foreign table whose name merely shares this build's prefix",
			seed: func(t *testing.T, db *sql.DB, dia dialect.Dialect) {
				seedStatements(t, db,
					"CREATE TABLE olivares_review_unrelated (value TEXT NOT NULL)",
					"INSERT INTO olivares_review_unrelated (value) VALUES ('prefix-is-not-membership')")
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			cfg, db, dia := accessEvidenceSQLiteStore(t)
			seedStatements(t, db,
				"CREATE TABLE review_unrelated (value TEXT NOT NULL)",
				"INSERT INTO review_unrelated (value) VALUES ('preserve-me')")
			tc.seed(t, db, dia)
			foreignDDL := sqliteObjectDDL(t, db, "review_unrelated")

			st, err := Open(ctx, cfg, nil)
			if err != nil {
				t.Fatalf("a foreign object blocked an install it has no claim on: %v", err)
			}
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
			assertForeignTablePreserved(t, db, foreignDDL)

			reopened, err := Open(ctx, cfg, nil)
			if err != nil {
				t.Fatalf("second Open: %v", err)
			}
			if err := reopened.Close(); err != nil {
				t.Fatal(err)
			}
			assertForeignTablePreserved(t, db, foreignDDL)
			// The complete plan is 1..11 then 13 (v12 reserved), so count the compiled plan.
			if want := len(compiledCoreMigrationVersions(loginCapabilitySQLiteDialect(t))); coreVersionCount(t, db) != want {
				t.Fatalf("core versions = %d, want the complete %d", coreVersionCount(t, db), want)
			}
		})
	}
}

func sqliteObjectDDL(t *testing.T, db *sql.DB, name string) string {
	t.Helper()
	var text sql.NullString
	if err := db.QueryRowContext(context.Background(),
		"SELECT sql FROM main.sqlite_master WHERE name = ?", name).Scan(&text); err != nil {
		t.Fatalf("read the stored DDL of %q: %v", name, err)
	}
	return text.String
}

func assertForeignTablePreserved(t *testing.T, db *sql.DB, wantDDL string) {
	t.Helper()
	if got := sqliteObjectDDL(t, db, "review_unrelated"); got != wantDDL {
		t.Fatalf("the foreign table's schema changed:\n got %q\nwant %q", got, wantDDL)
	}
	var value string
	if err := db.QueryRowContext(context.Background(),
		"SELECT value FROM review_unrelated").Scan(&value); err != nil {
		t.Fatalf("read the foreign row: %v", err)
	}
	if value != "preserve-me" {
		t.Fatalf("the foreign row = %q, want %q", value, "preserve-me")
	}
}

// ---------------------------------------------------------------------------------------
// The admitted checkpoints.
// ---------------------------------------------------------------------------------------

// TestFreshBootstrapAdmitsTheRatifiedPreV1Checkpoints walks the six positive rows of the
// ratified table, and five of the six are produced by running the PRODUCT'S OWN transactions in
// the order Open runs them rather than by fabricating tables.
//
// That distinction is the point of the row labels. `classifyRolloutControls` commits the three
// relations and every control's seed together, so stopping after it IS the state a crash between
// that commit and ensureTracking leaves. `migrate.Apply(…, nil)` runs the real ensureTracking and
// commits the five-column tracker with no migration after it. The one exception is the historical
// three-column tracker, which is built by hand and SAID to be synthetic: no release of this
// repository has been shown to leave one, and the ratification admits its structure, not a
// provenance nobody has demonstrated.
//
// Each row asserts the receipt suffix the sequence really produces. `virgin` and
// `pre-tracker-core` are ordered evidence about whether the tracker existed when the receipt was
// written, so normalizing them would erase the only fact they carry.
func TestFreshBootstrapAdmitsTheRatifiedPreV1Checkpoints(t *testing.T) {
	for _, tc := range []struct {
		name       string
		register   func(store.ExtensionRegistry) error
		controls   []store.RolloutControl
		build      func(*testing.T, *sql.DB, dialect.Dialect)
		wantSuffix string
		// wantTrackerColumns is named at every row rather than defaulted, because "the
		// three-column tracker was expanded exactly once, by the real ensureTracking" is one
		// of the things under test. A row that leaves it zero fails below.
		wantTrackerColumns int
	}{
		{
			name:               "F0: nothing at all",
			build:              func(t *testing.T, db *sql.DB, dia dialect.Dialect) {},
			wantTrackerColumns: 5,
		},
		{
			name:     "R complete, no tracker: interrupted after the classification commit",
			register: registerWidgetStaged,
			controls: []store.RolloutControl{testRolloutControl},
			build: func(t *testing.T, db *sql.DB, dia dialect.Dialect) {
				realRolloutCheckpoint(t, db, dia, []store.RolloutControl{testRolloutControl})
			},
			wantSuffix:         freshWitnessSuffixVirgin,
			wantTrackerColumns: 5,
		},
		{
			name: "R complete with zero controls: three empty relations are still the whole set",
			build: func(t *testing.T, db *sql.DB, dia dialect.Dialect) {
				realRolloutCheckpoint(t, db, dia, nil)
			},
			wantTrackerColumns: 5,
		},
		{
			name:     "R complete then T5: interrupted after ensureTracking, or v1 reverted",
			register: registerWidgetStaged,
			controls: []store.RolloutControl{testRolloutControl},
			build: func(t *testing.T, db *sql.DB, dia dialect.Dialect) {
				realRolloutCheckpoint(t, db, dia, []store.RolloutControl{testRolloutControl})
				realTrackerCheckpoint(t, db, dia)
			},
			// The receipt keeps what it observed. The tracker appearing afterwards does not
			// re-derive it, which is why the suffix is still `virgin`.
			wantSuffix:         freshWitnessSuffixVirgin,
			wantTrackerColumns: 5,
		},
		{
			name: "T5 alone: ensureTracking in isolation, which the normal order never produces from F0",
			build: func(t *testing.T, db *sql.DB, dia dialect.Dialect) {
				realTrackerCheckpoint(t, db, dia)
			},
			wantTrackerColumns: 5,
		},
		{
			name: "T3 alone: the historical structure, synthetic, expanded once by the real ensureTracking",
			build: func(t *testing.T, db *sql.DB, dia dialect.Dialect) {
				syntheticThreeColumnTracker(t, db)
			},
			wantTrackerColumns: 5,
		},
		{
			name:     "T3 then R: classified while the tracker existed, so the receipt says pre-tracker-core",
			register: registerWidgetStaged,
			controls: []store.RolloutControl{testRolloutControl},
			build: func(t *testing.T, db *sql.DB, dia dialect.Dialect) {
				syntheticThreeColumnTracker(t, db)
				realRolloutCheckpoint(t, db, dia, []store.RolloutControl{testRolloutControl})
			},
			wantSuffix:         freshWitnessSuffixPreTrackerCore,
			wantTrackerColumns: 5,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			cfg, db, dia := accessEvidenceSQLiteStore(t)
			tc.build(t, db, dia)

			st, err := Open(ctx, cfg, tc.register)
			if err != nil {
				t.Fatalf("a ratified pre-v1 checkpoint was refused: %v", err)
			}
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
			// The complete plan is 1..11 then 13 (v12 reserved), so count the compiled plan.
			if want := len(compiledCoreMigrationVersions(loginCapabilitySQLiteDialect(t))); coreVersionCount(t, db) != want {
				t.Fatalf("core versions = %d, want the complete %d", coreVersionCount(t, db), want)
			}
			if tc.wantTrackerColumns == 0 {
				t.Fatal("this row does not declare the tracker width it expects")
			}
			if got := trackerColumnCount(t, db); got != tc.wantTrackerColumns {
				t.Fatalf("tracker columns = %d, want %d", got, tc.wantTrackerColumns)
			}
			if tc.wantSuffix != "" {
				want := "table:" + testRolloutControl.WitnessTable + ":absent" + tc.wantSuffix
				assertReceiptDetail(t, db, testControlKey, want)
			}
			// A second Open is a verification, not a replay: the receipt written by the
			// interrupted boot survives the completed one.
			reopened, rerr := Open(ctx, cfg, tc.register)
			if rerr != nil {
				t.Fatalf("second Open: %v", rerr)
			}
			if err := reopened.Close(); err != nil {
				t.Fatal(err)
			}
			if tc.wantSuffix != "" {
				want := "table:" + testRolloutControl.WitnessTable + ":absent" + tc.wantSuffix
				assertReceiptDetail(t, db, testControlKey, want)
			}
		})
	}
}

func assertReceiptDetail(t *testing.T, db *sql.DB, key, want string) {
	t.Helper()
	var detail string
	if err := db.QueryRowContext(context.Background(),
		"SELECT witness_detail FROM "+dialect.ControlRolloutClassificationTable+" WHERE control_key = ?",
		key).Scan(&detail); err != nil {
		t.Fatalf("read the classification receipt for %q: %v", key, err)
	}
	if detail != want {
		t.Fatalf("receipt detail = %q, want %q: the suffix is ordered evidence about whether the tracker existed when the receipt was written, and must not be normalized",
			detail, want)
	}
}

// TestFreshBootstrapRefusesToExpandAThreeColumnTrackerItIsRejecting is the ordering half of the
// tracker rule: expansion happens only after the WHOLE admission has passed.
//
// The failure it forbids is subtle and permanent: expanding first and refusing afterwards leaves
// a database this build has already modified while telling the operator it did nothing.
func TestFreshBootstrapRefusesToExpandAThreeColumnTrackerItIsRejecting(t *testing.T) {
	ctx := context.Background()
	cfg, db, dia := accessEvidenceSQLiteStore(t)
	syntheticThreeColumnTracker(t, db)
	seedStatements(t, db, "CREATE TABLE orgs (review TEXT NOT NULL)")
	_ = dia
	before := sqliteLogicalSnapshot(t, db)

	st, err := Open(ctx, cfg, nil)
	if st != nil {
		_ = st.Close()
	}
	if err == nil {
		t.Fatal("Open admitted a precreated managed object beside a three-column tracker")
	}
	if got := trackerColumnCount(t, db); got != 3 {
		t.Fatalf("the refused boot expanded the tracker to %d columns", got)
	}
	if got := sqliteLogicalSnapshot(t, db); got != before {
		t.Fatalf("the refused boot changed durable state.\n--- before ---\n%s\n--- after ---\n%s", before, got)
	}
}

// TestFreshBootstrapDoesNotClaimAConfirmedV1 keeps the frontier on the right side of core v1.
//
// After v1 commits, its tracking row is the fact — so a boot whose acknowledgement was lost must
// resume as the v1 class and NOT re-enter the max0 admission, which would then refuse the
// tenancy objects v1 itself created.
func TestFreshBootstrapDoesNotClaimAConfirmedV1(t *testing.T) {
	ctx := context.Background()
	cfg, db, dia := accessEvidenceSQLiteStore(t)
	graph, gerr := guardEditionGraphFor(coreOnlyAccessEvidenceManifest(t))
	if gerr != nil {
		t.Fatal(gerr)
	}

	// v1 through the product's own plan and its own transaction, and nothing after it.
	plan := buildCoreMigrations(dia, coreDescriptors(), nil, nil)
	if plan[0].Version != 1 {
		t.Fatalf("the core plan does not start at v1: %d", plan[0].Version)
	}
	if err := migrate.Apply(ctx, db, dia, coreTrackingTable, plan[:1]); err != nil {
		t.Fatalf("apply the real core v1: %v", err)
	}

	got, err := classifyAccessEvidenceBoot(ctx, db, dia, graph, coreDescriptors(),
		coreOnlyRegistry(t), nil, guardEventFenceFacts{})
	if err != nil {
		t.Fatalf("classify a committed v1: %v", err)
	}
	if got.Class != accessEvidenceStartFreshPreV2 {
		t.Fatalf("class = %s, want %s: v1 is committed, so this is no longer the max0 frontier",
			got.Class, accessEvidenceStartFreshPreV2)
	}
	if got.FreshBootstrap != nil {
		t.Fatal("the max0 admission ran on a database whose v1 is recorded")
	}
	st, err := Open(ctx, cfg, nil)
	if err != nil {
		t.Fatalf("resume from a committed v1: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestFreshBootstrapRefusesAnInadmissibleTracker covers the half of T that the column comparison
// alone cannot decide.
//
// verifyCoreTrackingRelationShape already refuses type drift, a lost primary key, a partial
// phase/reverted_at pair and extra columns, and coreTrackingRelationExists already refuses a
// view and a trigger. What the ratified frontier adds here is everything attached to the relation
// that changes what an INSERT means — a CHECK, a foreign key, a uniqueness constraint — because a
// tracker admitted now and unable to record v1 fails the boot AFTER ensureTracking has committed.
func TestFreshBootstrapRefusesAnInadmissibleTracker(t *testing.T) {
	for _, tc := range []struct {
		name string
		ddl  []string
	}{
		{
			name: "version stored as TEXT",
			ddl: []string{"CREATE TABLE " + coreTrackingTable +
				" (version TEXT PRIMARY KEY, name TEXT NOT NULL, applied_at TEXT NOT NULL)"},
		},
		{
			name: "the primary key dropped",
			ddl: []string{"CREATE TABLE " + coreTrackingTable +
				" (version INTEGER NOT NULL, name TEXT NOT NULL, applied_at TEXT NOT NULL)"},
		},
		{
			name: "a CHECK that refuses the very row this boot would write",
			ddl: []string{"CREATE TABLE " + coreTrackingTable +
				" (version INTEGER PRIMARY KEY CHECK (version > 100), name TEXT NOT NULL, applied_at TEXT NOT NULL)"},
		},
		{
			name: "the phase column without reverted_at",
			ddl: []string{"CREATE TABLE " + coreTrackingTable +
				" (version INTEGER PRIMARY KEY, name TEXT NOT NULL, applied_at TEXT NOT NULL," +
				" phase TEXT NOT NULL DEFAULT 'expand')"},
		},
		{
			name: "a column this build never declares",
			ddl: []string{"CREATE TABLE " + coreTrackingTable +
				" (version INTEGER PRIMARY KEY, name TEXT NOT NULL, applied_at TEXT NOT NULL, review TEXT)"},
		},
		{
			name: "a trigger on the tracker",
			ddl: []string{
				"CREATE TABLE " + coreTrackingTable +
					" (version INTEGER PRIMARY KEY, name TEXT NOT NULL, applied_at TEXT NOT NULL)",
				"CREATE TRIGGER review_tracker_guard BEFORE INSERT ON " + coreTrackingTable +
					" BEGIN SELECT RAISE(ABORT,'review'); END",
			},
		},
		{
			name: "a view wearing the tracker's name",
			ddl: []string{"CREATE VIEW " + coreTrackingTable +
				" AS SELECT 1 AS version, 'x' AS name, 'y' AS applied_at"},
		},
		{
			name: "a uniqueness object this build never declares",
			ddl: []string{
				"CREATE TABLE " + coreTrackingTable +
					" (version INTEGER PRIMARY KEY, name TEXT NOT NULL, applied_at TEXT NOT NULL)",
				"CREATE UNIQUE INDEX review_tracker_name_uniq ON " + coreTrackingTable + "(name)",
			},
		},
		{
			name: "a foreign key that makes the tracker depend on another relation",
			ddl: []string{
				"CREATE TABLE review_parent (name TEXT PRIMARY KEY)",
				"CREATE TABLE " + coreTrackingTable +
					" (version INTEGER PRIMARY KEY, name TEXT NOT NULL REFERENCES review_parent(name), applied_at TEXT NOT NULL)",
			},
		},
		{
			name: "a non-canonical version, which is not an empty history",
			ddl: []string{
				"CREATE TABLE " + coreTrackingTable +
					" (version INTEGER PRIMARY KEY, name TEXT NOT NULL, applied_at TEXT NOT NULL)",
				"INSERT INTO " + coreTrackingTable + " (version, name, applied_at) VALUES (0, 'x', 'y')",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			cfg, db, _ := accessEvidenceSQLiteStore(t)
			seedStatements(t, db, tc.ddl...)
			before := sqliteLogicalSnapshot(t, db)

			st, err := Open(ctx, cfg, nil)
			if st != nil {
				_ = st.Close()
			}
			t.Logf("REFUSAL|%v", err)
			if err == nil {
				t.Fatal("Open admitted a tracker this build cannot have written")
			}
			if sqliteObjectExists(t, db, dialect.ControlRolloutStateTable) {
				t.Fatal("the refusal created the rollout relations")
			}
			if got := sqliteLogicalSnapshot(t, db); got != before {
				t.Fatalf("the refusal changed durable state.\n--- before ---\n%s\n--- after ---\n%s", before, got)
			}
		})
	}
}

// TestFreshBootstrapRefusesAnIncoherentRolloutCheckpoint is the R half.
//
// Every case starts from a checkpoint the PRODUCT really committed and then damages exactly one
// thing, so what is under test is the correlation and not the ability to reject nonsense. Two of
// them are the reason the correlation is established BEFORE the classifier's transaction rather
// than by calling it: classifyRolloutControls validates a state row, but ensureClassificationReceipt
// returns early when a receipt exists without comparing its fields, so a receipt that disagrees
// with its state row would have survived the call that appears to check it.
func TestFreshBootstrapRefusesAnIncoherentRolloutCheckpoint(t *testing.T) {
	rolloutTables := []string{
		dialect.ControlRolloutStateTable,
		dialect.ControlRolloutTransitionTable,
		dialect.ControlRolloutClassificationTable,
	}
	// The six non-empty PROPER subsets of the three relations. Each is produced by committing
	// the real checkpoint and dropping the complement, so the shapes and rows of whatever
	// survives are genuine.
	var subsets []struct {
		name string
		drop []string
	}
	for mask := 1; mask < 7; mask++ {
		var keep, drop []string
		for i, table := range rolloutTables {
			if mask&(1<<i) != 0 {
				keep = append(keep, table)
			} else {
				drop = append(drop, table)
			}
		}
		if len(drop) == 0 {
			continue
		}
		subsets = append(subsets, struct {
			name string
			drop []string
		}{"partial rollout set: only " + strings.Join(keep, "+"), drop})
	}

	// Every case reopens with the SAME registration profile that produced the checkpoint,
	// except the one whose subject IS a profile change. Getting that wrong would make each row
	// pass for the wrong reason — the profile mismatch would refuse the boot before the damage
	// under test was ever reached, and the mutation controls in this assessment are what
	// measured it.
	cases := []struct {
		name             string
		damage           func(*testing.T, *sql.DB)
		differentProfile bool
	}{
		{
			name: "the state row lost, its receipt surviving",
			damage: func(t *testing.T, db *sql.DB) {
				mustExec(t, db, "DELETE FROM "+dialect.ControlRolloutStateTable)
			},
		},
		{
			name: "the receipt lost, its state row surviving",
			damage: func(t *testing.T, db *sql.DB) {
				mustExec(t, db, "DELETE FROM "+dialect.ControlRolloutClassificationTable)
			},
		},
		{
			name: "a key this build does not declare",
			damage: func(t *testing.T, db *sql.DB) {
				mustExec(t, db, "INSERT INTO "+dialect.ControlRolloutStateTable+
					" SELECT 'rrw.unknown.v1', classified_mode, current_mode, enforcement_committed,"+
					" generation, classified_at, witness_kind, witness_detail, decided_at, decided_by,"+
					" decided_reason FROM "+dialect.ControlRolloutStateTable)
				mustExec(t, db, "INSERT INTO "+dialect.ControlRolloutClassificationTable+
					" SELECT 'rrw.unknown.v1', classified_mode, classified_at, witness_kind, witness_detail"+
					" FROM "+dialect.ControlRolloutClassificationTable)
			},
		},
		{
			name: "the receipt disagreeing with its state row on one field",
			damage: func(t *testing.T, db *sql.DB) {
				mustExec(t, db, "UPDATE "+dialect.ControlRolloutClassificationTable+
					" SET classified_at = '2020-01-01T00:00:00Z'")
			},
		},
		{
			name: "a classification into the compatibility mode, which needs a witness that cannot exist here",
			damage: func(t *testing.T, db *sql.DB) {
				mustExec(t, db, "UPDATE "+dialect.ControlRolloutStateTable+
					" SET classified_mode = 'legacy_compat', current_mode = 'legacy_compat'")
				mustExec(t, db, "UPDATE "+dialect.ControlRolloutClassificationTable+
					" SET classified_mode = 'legacy_compat'")
			},
		},
		{
			name: "a generation beyond the seed",
			damage: func(t *testing.T, db *sql.DB) {
				mustExec(t, db, "UPDATE "+dialect.ControlRolloutStateTable+" SET generation = 2")
			},
		},
		{
			name: "an enforcement commitment nobody could have decided yet",
			damage: func(t *testing.T, db *sql.DB) {
				mustExec(t, db, "UPDATE "+dialect.ControlRolloutStateTable+" SET enforcement_committed = 1")
			},
		},
		{
			name: "a recorded decision before the first complete Open",
			damage: func(t *testing.T, db *sql.DB) {
				mustExec(t, db, "UPDATE "+dialect.ControlRolloutStateTable+
					" SET decided_at = '2020-01-01T00:00:00Z', decided_by = 'review', decided_reason = 'review'")
				mustExec(t, db, "UPDATE "+dialect.ControlRolloutClassificationTable+
					" SET classified_at = classified_at")
			},
		},
		{
			name: "a transition recorded before any core migration",
			damage: func(t *testing.T, db *sql.DB) {
				mustExec(t, db, "INSERT INTO "+dialect.ControlRolloutTransitionTable+
					" (control_key, generation, from_mode, to_mode, committed, decided_at, decided_by,"+
					" decided_reason, evidence) VALUES ('"+testControlKey+
					"', 1, 'enforced', 'policy_optional', 1, '2020-01-01T00:00:00Z', 'review', 'review', 'review')")
			},
		},
		{
			name: "a witness detail that claims the witness table was there",
			damage: func(t *testing.T, db *sql.DB) {
				detail := "table:" + testRolloutControl.WitnessTable + ":present:tracked"
				mustExec(t, db, "UPDATE "+dialect.ControlRolloutStateTable+
					" SET witness_detail = '"+detail+"'")
				mustExec(t, db, "UPDATE "+dialect.ControlRolloutClassificationTable+
					" SET witness_detail = '"+detail+"'")
			},
		},
		{
			name: "a pre-tracker-core receipt with no tracker to have preceded it",
			damage: func(t *testing.T, db *sql.DB) {
				detail := "table:" + testRolloutControl.WitnessTable + ":absent:pre-tracker-core"
				mustExec(t, db, "UPDATE "+dialect.ControlRolloutStateTable+
					" SET witness_detail = '"+detail+"'")
				mustExec(t, db, "UPDATE "+dialect.ControlRolloutClassificationTable+
					" SET witness_detail = '"+detail+"'")
			},
		},
		{
			name: "a relation whose ordinary SELECTs still work and whose contract changed",
			damage: func(t *testing.T, db *sql.DB) {
				mustExec(t, db, "ALTER TABLE "+dialect.ControlRolloutStateTable+" ADD COLUMN review TEXT")
			},
		},
		{
			// A perfectly coherent checkpoint of ANOTHER registration profile. It is not
			// declared damage and it is not guessed at: the state is preserved and the boot
			// says it does not recognize this checkpoint for the profile it carries.
			name:             "a coherent checkpoint belonging to a different registration profile",
			damage:           func(t *testing.T, db *sql.DB) {},
			differentProfile: true,
		},
	}

	for _, subset := range subsets {
		drop := subset.drop
		cases = append(cases, struct {
			name             string
			damage           func(*testing.T, *sql.DB)
			differentProfile bool
		}{
			name: subset.name,
			damage: func(t *testing.T, db *sql.DB) {
				for _, table := range drop {
					mustExec(t, db, "DROP TABLE "+table)
				}
			},
		})
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			cfg, db, dia := accessEvidenceSQLiteStore(t)
			realRolloutCheckpoint(t, db, dia, []store.RolloutControl{testRolloutControl})
			tc.damage(t, db)
			before := sqliteLogicalSnapshot(t, db)

			register := registerWidgetStaged
			if tc.differentProfile {
				register = nil
			}
			st, err := Open(ctx, cfg, register)
			if st != nil {
				_ = st.Close()
			}
			t.Logf("REFUSAL|%v", err)
			if err == nil {
				t.Fatal("Open admitted a rollout checkpoint this build could not have committed")
			}
			if got := coreVersionCount(t, db); got > 0 {
				t.Fatalf("the refusal recorded %d core migration(s)", got)
			}
			if got := sqliteLogicalSnapshot(t, db); got != before {
				t.Fatalf("the refusal changed durable state.\n--- before ---\n%s\n--- after ---\n%s", before, got)
			}
		})
	}
}

// TestManagedObjectInventoryReadsEveryStatementThisBuildRenders is the deny-closed half of the
// inventory, and it needs no server.
//
// managedStatementObjects refuses a statement form it cannot name exactly, and that refusal
// travels: buildManagedObjectSet returns the error and the boot classification fails. That is the
// right default — an unnamed object is an object nobody knows is managed — but it means a new
// statement form in any constructor breaks the boot rather than quietly narrowing the census. This
// test is where that breakage is supposed to be found.
func TestManagedObjectInventoryReadsEveryStatementThisBuildRenders(t *testing.T) {
	for _, engine := range store.SupportedEngines() {
		t.Run(string(engine), func(t *testing.T) {
			dia, ok := dialect.New(engine)
			if !ok {
				t.Fatalf("no %s dialect", engine)
			}
			for _, profile := range []struct {
				name     string
				register func(store.ExtensionRegistry) error
			}{
				{"core-only registrar", nil},
				{"module registrar with file migrations", registerFreshBootstrapModule},
			} {
				t.Run(profile.name, func(t *testing.T) {
					reg := freshBootstrapRegistry(t, profile.register)
					set, err := buildManagedObjectSet(dia, coreDescriptors(), reg,
						freshBootstrapPlans(t, dia, reg))
					if err != nil {
						t.Fatalf("the inventory could not name a statement this build renders: %v", err)
					}
					// A spot check that the two identities with no descriptor and no
					// rendered statement of their own are there, because they are the two
					// this inventory would most plausibly lose.
					if _, ok := set.lookup(managedClassIndex, "federation_configs_scope_uniq", ""); !ok {
						t.Fatal("the historical index core v4 drops is not in the managed set")
					}
					if _, ok := set.lookup(managedClassRelation, coreTrackingTable, ""); !ok {
						t.Fatal("the core tracker is not in the managed set")
					}
				})
			}
		})
	}
}

// TestManagedStatementObjectsRefusesWhatItCannotName pins the deny-closed behavior itself, so a
// future relaxation of the extractor into "skip what you do not understand" fails here.
func TestManagedStatementObjectsRefusesWhatItCannotName(t *testing.T) {
	for _, tc := range []struct {
		stmt  string
		want  managedObject
		named bool
	}{
		{stmt: "CREATE TABLE IF NOT EXISTS main.orgs (id TEXT)", want: managedObject{class: managedClassRelation, name: "orgs"}, named: true},
		{stmt: "CREATE UNIQUE INDEX rrw_widget_label_uniq ON rrw_widget(label)", want: managedObject{class: managedClassIndex, name: "rrw_widget_label_uniq"}, named: true},
		{stmt: "DROP INDEX IF EXISTS federation_configs_scope_uniq", want: managedObject{class: managedClassIndex, name: "federation_configs_scope_uniq"}, named: true},
		{stmt: "CREATE OR REPLACE FUNCTION public.olivares_block_mutation() RETURNS trigger AS $$ $$", want: managedObject{class: managedClassRoutine, name: "olivares_block_mutation"}, named: true},
		{stmt: "CREATE EVENT TRIGGER olivares_guard_fence_ddl_end ON ddl_command_end", want: managedObject{class: managedClassEventTrigger, name: "olivares_guard_fence_ddl_end"}, named: true},
		{stmt: "-- a comment\nGRANT SELECT ON orgs TO olivares_app"},
		{stmt: "CREATE POLICY tenant_isolation ON orgs USING (true)"},
		{stmt: "CREATE MATERIALIZED VIEW review AS SELECT 1", want: managedObject{class: managedClassRelation, name: "review"}, named: true},
	} {
		t.Run(tc.stmt[:min(len(tc.stmt), 46)], func(t *testing.T) {
			got, err := managedStatementObjects(tc.stmt)
			if err != nil {
				t.Fatalf("statement refused: %v", err)
			}
			if !tc.named {
				if len(got) != 0 {
					t.Fatalf("statement named %v, want no durable identity of its own", got)
				}
				return
			}
			if len(got) != 1 || got[0].class != tc.want.class || got[0].name != tc.want.name {
				t.Fatalf("statement named %v, want %s", got, tc.want)
			}
		})
	}
	for _, stmt := range []string{
		"CREATE DOMAIN review AS text",
		"CREATE TABLE other_schema.orgs (id TEXT)",
		"REINDEX TABLE orgs",
	} {
		t.Run("refused: "+stmt[:min(len(stmt), 40)], func(t *testing.T) {
			if _, err := managedStatementObjects(stmt); !errors.Is(err, errManagedStatementUnreadable) {
				t.Fatalf("error = %v, want the deny-closed refusal: skipping a form nobody read is how an object stops being managed", err)
			}
		})
	}
}

// ---------------------------------------------------------------------------------------
// The R2 corrections: intended outcomes for the six findings the independent review measured.
// The reviewer's own probes, retained unchanged in
// an internal design note (not shipped), assert the
// DEFECT and therefore go red against this tree. These are their positive counterparts.
// ---------------------------------------------------------------------------------------

// TestFreshBootstrapTrackerContractMatchesEnsureTracking is the anti-drift control for the two
// structural constants, and it is what lets them be constants at all.
//
// They describe migrate.ensureTracking's DDL, which lives in another package. If that statement
// ever changes, this fails here rather than at somebody's boot — and the second half proves the
// measured claim the contract rests on: SQLite REWRITES the stored statement on ALTER TABLE ADD
// COLUMN, so a three-column tracker expanded by the real ensureTracking normalizes to exactly the
// same text as a freshly created five-column one.
func TestFreshBootstrapTrackerContractMatchesEnsureTracking(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name  string
		build func(*testing.T, *sql.DB, dialect.Dialect)
	}{
		{
			name: "created by ensureTracking on an empty database",
			build: func(t *testing.T, db *sql.DB, dia dialect.Dialect) {
				realTrackerCheckpoint(t, db, dia)
			},
		},
		{
			name: "expanded by ensureTracking from the historical three-column shape",
			build: func(t *testing.T, db *sql.DB, dia dialect.Dialect) {
				syntheticThreeColumnTracker(t, db)
				if got := normalizeSQLiteTableDDL(sqliteObjectDDL(t, db, coreTrackingTable),
					coreTrackingTable); got != freshTrackerContractT3 {
					t.Fatalf("the three-column contract does not describe the historical shape:\n got %q\nwant %q",
						got, freshTrackerContractT3)
				}
				realTrackerCheckpoint(t, db, dia)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, db, dia := accessEvidenceSQLiteStore(t)
			tc.build(t, db, dia)
			got := normalizeSQLiteTableDDL(sqliteObjectDDL(t, db, coreTrackingTable), coreTrackingTable)
			if got != freshTrackerContractT5 {
				t.Fatalf("the five-column contract no longer describes what ensureTracking creates:\n got %q\nwant %q",
					got, freshTrackerContractT5)
			}
			_ = ctx
		})
	}
}

// TestFreshBootstrapPreservesAnUnrelatedTriggerName is FB-2's SQLite half, and it is a
// PRESERVATION contract: the reviewed candidate refused an installation over it.
//
// MEASURED on the engine this build links, not inferred from sqlite_master holding everything in
// one table: `CREATE TRIGGER orgs …` and `CREATE TABLE orgs …` both succeed, while a table and an
// index of one name do not. So a trigger named like one of this build's tables is not that
// table's identity and is nobody's collision. The reviewer's probe measured the parent preserving
// it and the candidate refusing.
func TestFreshBootstrapPreservesAnUnrelatedTriggerName(t *testing.T) {
	ctx := context.Background()
	cfg, db, _ := accessEvidenceSQLiteStore(t)
	seedStatements(t, db,
		"CREATE TABLE review_unrelated (value TEXT NOT NULL)",
		"INSERT INTO review_unrelated (value) VALUES ('preserve-me')",
		"CREATE TRIGGER orgs AFTER INSERT ON review_unrelated BEGIN SELECT 1; END")
	triggerDDL := sqliteObjectDDL(t, db, "orgs")

	st, err := Open(ctx, cfg, nil)
	if err != nil {
		t.Fatalf("a trigger merely NAMED like a managed table refused the install: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, rerr := Open(ctx, cfg, nil)
	if rerr != nil {
		t.Fatalf("second Open: %v", rerr)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	// The complete plan is 1..11 then 13 (v12 reserved), so count the compiled plan.
	if want := len(compiledCoreMigrationVersions(loginCapabilitySQLiteDialect(t))); coreVersionCount(t, db) != want {
		t.Fatalf("core versions = %d, want the complete %d", coreVersionCount(t, db), want)
	}
	// The foreign trigger survived, and so did the managed TABLE of the same name, which is a
	// different object in a different namespace.
	if got := sqliteObjectDDL(t, db, "orgs"); got != triggerDDL {
		t.Fatalf("the foreign trigger changed:\n got %q\nwant %q", got, triggerDDL)
	}
	assertForeignTablePreserved(t, db, sqliteObjectDDL(t, db, "review_unrelated"))
	var tables int
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM main.sqlite_master WHERE type='table' AND name='orgs'").Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if tables != 1 {
		t.Fatalf("managed tables named orgs = %d, want the one this build creates", tables)
	}
}

// TestFreshBootstrapRefusesAManagedTriggerName is the other side of FB-2's SQLite half: the
// trigger namespace is separate, not unguarded.
//
// A foreign trigger wearing a name this build's own CREATE TRIGGER would take still collides, and
// it can only live on a foreign table — because the managed table it belongs to must be absent.
func TestFreshBootstrapRefusesAManagedTriggerName(t *testing.T) {
	ctx := context.Background()
	cfg, db, _ := accessEvidenceSQLiteStore(t)
	seedStatements(t, db,
		"CREATE TABLE review_unrelated (value TEXT NOT NULL)",
		"CREATE TRIGGER orgs_scope_ins AFTER INSERT ON review_unrelated BEGIN SELECT 1; END")
	before := sqliteLogicalSnapshot(t, db)

	st, err := Open(ctx, cfg, nil)
	if st != nil {
		_ = st.Close()
	}
	if err == nil {
		t.Fatal("Open admitted a trigger name this build's own CREATE TRIGGER would take")
	}
	if sqliteObjectExists(t, db, dialect.ControlRolloutStateTable) {
		t.Fatal("the refusal created the rollout relations")
	}
	if got := sqliteLogicalSnapshot(t, db); got != before {
		t.Fatalf("the refusal changed durable state.\n--- before ---\n%s\n--- after ---\n%s", before, got)
	}
}

// TestFreshBootstrapRefusesATrackerWithUndeclaredStructure is FB-3.
//
// The reviewed candidate proved the tracker "works" with ONE sample insert (`version=1`), which a
// `CHECK(version < 2)` passes. It then committed three rollout relations and core v1 and failed
// recording v2. A different predicate passes any finite set of samples, so the contract is the
// stored statement itself — which on SQLite is the only place the engine keeps a CHECK at all.
func TestFreshBootstrapRefusesATrackerWithUndeclaredStructure(t *testing.T) {
	for _, tc := range []struct {
		name string
		ddl  string
	}{
		{
			// The reviewer's exact witness.
			name: "a CHECK that admits the probe's sample and refuses the second migration",
			ddl: "CREATE TABLE " + coreTrackingTable + " (version INTEGER PRIMARY KEY CHECK(version < 2)," +
				" name TEXT NOT NULL, applied_at TEXT NOT NULL, phase TEXT NOT NULL DEFAULT 'expand', reverted_at TEXT)",
		},
		{
			name: "a CHECK on the three-column shape",
			ddl: "CREATE TABLE " + coreTrackingTable + " (version INTEGER PRIMARY KEY CHECK(version <> 4)," +
				" name TEXT NOT NULL, applied_at TEXT NOT NULL)",
		},
		{
			name: "a collation nobody declared, which changes what a stored name compares equal to",
			ddl: "CREATE TABLE " + coreTrackingTable + " (version INTEGER PRIMARY KEY," +
				" name TEXT NOT NULL COLLATE NOCASE, applied_at TEXT NOT NULL)",
		},
		{
			name: "an inline uniqueness rule",
			ddl: "CREATE TABLE " + coreTrackingTable + " (version INTEGER PRIMARY KEY," +
				" name TEXT NOT NULL UNIQUE, applied_at TEXT NOT NULL)",
		},
		{
			name: "a WITHOUT ROWID table, whose INTEGER PRIMARY KEY is not the rowid alias this build writes",
			ddl: "CREATE TABLE " + coreTrackingTable + " (version INTEGER PRIMARY KEY," +
				" name TEXT NOT NULL, applied_at TEXT NOT NULL) WITHOUT ROWID",
		},
		{
			name: "a DEFAULT this build does not write on the phase column",
			ddl: "CREATE TABLE " + coreTrackingTable + " (version INTEGER PRIMARY KEY," +
				" name TEXT NOT NULL, applied_at TEXT NOT NULL, phase TEXT NOT NULL DEFAULT 'contract', reverted_at TEXT)",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			cfg, db, _ := accessEvidenceSQLiteStore(t)
			seedStatements(t, db, tc.ddl)
			before := sqliteLogicalSnapshot(t, db)

			st, err := Open(ctx, cfg, nil)
			if st != nil {
				_ = st.Close()
			}
			t.Logf("REFUSAL|%v", err)
			if err == nil {
				t.Fatal("Open admitted a tracker whose statement declares something this build never writes")
			}
			if got := coreVersionCount(t, db); got > 0 {
				t.Fatalf("the refusal recorded %d core migration(s)", got)
			}
			if sqliteObjectExists(t, db, dialect.ControlRolloutStateTable) {
				t.Fatal("the refusal created the rollout relations")
			}
			if got := trackerColumnCount(t, db); got != strings.Count(tc.ddl, ",")+1 {
				t.Fatalf("the refusal changed the tracker width to %d", got)
			}
			if got := sqliteLogicalSnapshot(t, db); got != before {
				t.Fatalf("the refusal changed durable state.\n--- before ---\n%s\n--- after ---\n%s", before, got)
			}
		})
	}
}

// TestFreshBootstrapRefusesNoncanonicalRolloutFacts is FB-4.
//
// Every case starts from R that the PRODUCT's own transaction committed and changes only the
// stated field, so what is under test is the canonicality of the first classification's facts and
// not the ability to reject a malformed table. All three were measured admitting a full Open.
func TestFreshBootstrapRefusesNoncanonicalRolloutFacts(t *testing.T) {
	second := testRolloutControl
	second.Key = "rrw.second.v1"
	twoControls := func(reg store.ExtensionRegistry) error {
		if err := registerWidgetStaged(reg); err != nil {
			return err
		}
		return reg.RolloutControl(second)
	}
	for _, tc := range []struct {
		name     string
		controls []store.RolloutControl
		register func(store.ExtensionRegistry) error
		damage   func(*testing.T, *sql.DB)
	}{
		{
			// SQL NULL and the empty string are different facts. The table's own CHECK does
			// not catch this one: it pairs decided_at with decided_by and says nothing about
			// the reason.
			name: "an empty decision reason, which is a value and not the absence of one",
			damage: func(t *testing.T, db *sql.DB) {
				mustExec(t, db, "UPDATE "+dialect.ControlRolloutStateTable+" SET decided_reason = ''")
			},
		},
		{
			name: "a timestamp that parses and is not the representation the writer stores",
			damage: func(t *testing.T, db *sql.DB) {
				for _, table := range []string{
					dialect.ControlRolloutStateTable,
					dialect.ControlRolloutClassificationTable,
				} {
					mustExec(t, db, "UPDATE "+table+" SET classified_at = '2026-01-01T01:00:00+01:00'")
				}
			},
		},
		{
			name:     "two controls of one first classification carrying two instants",
			controls: []store.RolloutControl{testRolloutControl, second},
			register: twoControls,
			damage: func(t *testing.T, db *sql.DB) {
				for _, table := range []string{
					dialect.ControlRolloutStateTable,
					dialect.ControlRolloutClassificationTable,
				} {
					mustExec(t, db, "UPDATE "+table+
						" SET classified_at = '2020-01-01T00:00:00Z' WHERE control_key = '"+second.Key+"'")
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			cfg, db, dia := accessEvidenceSQLiteStore(t)
			controls := tc.controls
			if controls == nil {
				controls = []store.RolloutControl{testRolloutControl}
			}
			register := tc.register
			if register == nil {
				register = registerWidgetStaged
			}
			realRolloutCheckpoint(t, db, dia, controls)
			tc.damage(t, db)
			before := sqliteLogicalSnapshot(t, db)

			st, err := Open(ctx, cfg, register)
			if st != nil {
				_ = st.Close()
			}
			t.Logf("REFUSAL|%v", err)
			if err == nil {
				t.Fatal("Open admitted a first-classification fact this build's writer cannot produce")
			}
			if got := coreVersionCount(t, db); got > 0 {
				t.Fatalf("the refusal recorded %d core migration(s)", got)
			}
			if got := sqliteLogicalSnapshot(t, db); got != before {
				t.Fatalf("the refusal changed durable state; a witness must never be normalized to be admissible.\n--- before ---\n%s\n--- after ---\n%s",
					before, got)
			}
		})
	}
}

// TestFreshBootstrapAdmitsAMultiControlProductCheckpoint is the positive control FB-4 needs: the
// canonicality rules must not refuse what the writer really writes.
//
// Two controls seeded by ONE real classification share one timestamp and carry NULL decisions, so
// the checkpoint is admitted and the install completes.
func TestFreshBootstrapAdmitsAMultiControlProductCheckpoint(t *testing.T) {
	ctx := context.Background()
	second := testRolloutControl
	second.Key = "rrw.second.v1"
	register := func(reg store.ExtensionRegistry) error {
		if err := registerWidgetStaged(reg); err != nil {
			return err
		}
		return reg.RolloutControl(second)
	}
	cfg, db, dia := accessEvidenceSQLiteStore(t)
	realRolloutCheckpoint(t, db, dia, []store.RolloutControl{testRolloutControl, second})

	st, err := Open(ctx, cfg, register)
	if err != nil {
		t.Fatalf("a real two-control checkpoint was refused: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	// The complete plan is 1..11 then 13 (v12 reserved), so count the compiled plan.
	if want := len(compiledCoreMigrationVersions(loginCapabilitySQLiteDialect(t))); coreVersionCount(t, db) != want {
		t.Fatalf("core versions = %d, want %d", coreVersionCount(t, db), want)
	}
	for _, key := range []string{testControlKey, second.Key} {
		assertReceiptDetail(t, db, key,
			"table:"+testRolloutControl.WitnessTable+":absent"+freshWitnessSuffixVirgin)
	}
}

// TestModuleStatementsWithUndeterminedEffectsBecomeResiduals is FB-6.
//
// THE MEASURED HOLE: a supported module statement
//
//	DO $$ BEGIN EXECUTE 'CREATE TABLE review_managed_hidden(v text)'; END $$
//
// produced ZERO named identities AND ZERO residuals. The inventory treated a leading DO, ALTER,
// WITH or SELECT as effect-free — which is a fact about statements THIS repository renders, and
// not a fact about SQL a module registered. An effect that is neither named nor declared unknown
// is the worst of the three outcomes, because it is indistinguishable from an effect that does
// not exist.
//
// Now only CREATE and DROP, whose object is decided by the statement's own form, yield an
// identity from module SQL; everything else is an UNPROVEN EFFECT. Nothing is rejected: a module
// supported today keeps working, and the residual is a declared limit of this inventory.
func TestModuleStatementsWithUndeterminedEffectsBecomeResiduals(t *testing.T) {
	for _, tc := range []struct {
		name         string
		stmt         string
		wantResidual bool
		wantForm     string
	}{
		{
			name:         "the reviewer's DO block, which really does create a relation",
			stmt:         "DO $$ BEGIN EXECUTE 'CREATE TABLE review_managed_hidden(v text)'; END $$",
			wantResidual: true, wantForm: "DO",
		},
		{
			name:         "an ALTER, which may add a constraint or rename the relation",
			stmt:         "ALTER TABLE rrw_widget ADD COLUMN review text",
			wantResidual: true, wantForm: "ALTER",
		},
		{
			name:         "a backfill, which may call a function that creates something",
			stmt:         "UPDATE rrw_widget SET count = 0 WHERE count IS NULL",
			wantResidual: true, wantForm: "UPDATE",
		},
		{
			name:         "a CTE, for the same reason",
			stmt:         "WITH x AS (SELECT 1) SELECT * FROM x",
			wantResidual: true, wantForm: "WITH",
		},
		{
			name:     "a CREATE, whose object its own form decides",
			stmt:     "CREATE UNIQUE INDEX rrw_widget_label_uniq ON rrw_widget(tenant_id, label)",
			wantForm: "CREATE INDEX",
		},
		{
			name:     "a DROP, likewise",
			stmt:     "DROP INDEX rrw_widget_label_uniq",
			wantForm: "DROP INDEX",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			set := newManagedObjectSet(store.EnginePostgres)
			set.addModuleStatements("external", "0001_probe.sql", []string{tc.stmt})
			if got := len(set.unnamedModuleEffects) > 0; got != tc.wantResidual {
				t.Fatalf("residual recorded = %t, want %t (identities=%v)",
					got, tc.wantResidual, set.byClass)
			}
			if got := moduleStatementForm(tc.stmt); got != tc.wantForm {
				t.Fatalf("form = %q, want %q", got, tc.wantForm)
			}
			if !tc.wantResidual {
				if len(set.byClass) == 0 {
					t.Fatal("a determined statement named no identity")
				}
				return
			}
			// THE DIAGNOSTIC MUST NOT CARRY THE SQL. The statement's own text, its
			// identifiers and its literals are the operator's, and an earlier revision of
			// this record kept a 60-byte excerpt of them.
			effect := set.unnamedModuleEffects[0]
			for _, secret := range []string{"review_managed_hidden", "EXECUTE", "SELECT", "count", "$$"} {
				if strings.Contains(effect.String(), secret) {
					t.Fatalf("the residual record leaks statement content: %q contains %q",
						effect.String(), secret)
				}
			}
			if effect.namespace != "external" || effect.migration != "0001_probe.sql" {
				t.Fatalf("residual = %+v, want it located by namespace and migration file", effect)
			}
		})
	}
}

// TestModuleStatementFormNeverEchoesUnknownSQL pins the closed vocabulary.
//
// A form label is a classification, not content. A statement whose leading keyword this build
// does not recognize must be reported as "unrecognized" rather than by echoing whatever word it
// began with — otherwise an identifier could reach a boot log through the diagnostic.
func TestModuleStatementFormNeverEchoesUnknownSQL(t *testing.T) {
	for _, stmt := range []string{
		"REINDEX secret_table_name",
		"CALL review_secret_procedure()",
		"\\copy review FROM 'secret.csv'",
		"MERGE INTO review_secret USING x ON true",
	} {
		if got := moduleStatementForm(stmt); got != "unrecognized" {
			t.Fatalf("form of %.20q = %q, want %q: an unknown leading word must not be echoed",
				stmt, got, "unrecognized")
		}
	}
	if got := moduleStatementForm("   \n-- only a comment\n"); got != "empty" {
		t.Fatalf("form of a comment-only statement = %q, want %q", got, "empty")
	}
}

// TestFreshBootstrapInventoryReadsTheRepositoryModuleMigrations is the bounded control over the
// ACTUAL module migration files this repository ships, and its scope is exactly what it says.
//
// WHAT IT IS: the real files of the real modules, loaded through the real plan preparation, on
// both engines. It measures how much of them this inventory can name and how much it declares
// unknown, and it requires that nothing is rejected and that no residual carries SQL.
//
// WHAT IT IS NOT: a product-callback catalog control. `core` cannot import `modules`, so the
// registration callback those modules provide cannot run here. This reads their migration
// filesystems; it does not install them and does not compare a catalog.
func TestFreshBootstrapInventoryReadsTheRepositoryModuleMigrations(t *testing.T) {
	root := repositoryRootForModuleFiles(t)
	if root == "" {
		t.Skip("the modules/ tree is not present beside this package (curated export); this control needs the real migration files")
	}
	for _, engine := range store.SupportedEngines() {
		t.Run(string(engine), func(t *testing.T) {
			dia, ok := dialect.New(engine)
			if !ok {
				t.Fatalf("no %s dialect", engine)
			}
			reg := freshBootstrapRegistry(t, func(r store.ExtensionRegistry) error {
				for _, ns := range []string{"sessions", "eventing"} {
					if err := r.Migrations(ns, os.DirFS(filepath.Join(root, "modules", ns, "migrations"))); err != nil {
						return err
					}
				}
				return nil
			})
			plans := freshBootstrapPlans(t, dia, reg)
			set, err := buildManagedObjectSet(dia, coreDescriptors(), reg, plans)
			if err != nil {
				t.Fatalf("the real module migration files were rejected: %v", err)
			}
			statements := 0
			for _, p := range plans {
				statements += len(p.migrations)
			}
			if statements == 0 {
				t.Fatal("no module migration was loaded; this control measured nothing")
			}
			forms := map[string]int{}
			for _, e := range set.unnamedModuleEffects {
				forms[e.form]++
				for _, leak := range []string{"'", "$$", "("} {
					if strings.Contains(e.String(), leak) {
						t.Fatalf("a residual over real module SQL leaks content: %q", e.String())
					}
				}
			}
			t.Logf("REAL_MODULE_FILES|engine=%s|plans=%d|statements=%d|undetermined=%d|forms=%v",
				engine, len(plans), statements, len(set.unnamedModuleEffects), forms)
		})
	}
}

// repositoryRootForModuleFiles walks up from this package to the repository root, and returns ""
// when the modules tree is not there. It is located from the SOURCE path rather than the working
// directory so the control does not depend on how the test was invoked.
func repositoryRootForModuleFiles(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate this source file")
	}
	dir := filepath.Dir(file)
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, "modules", "eventing", "migrations")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}
