// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/migrate"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestCoreDirectoryV7SQLite(t *testing.T) {
	t.Parallel()
	dia, ok := dialect.New(store.EngineSQLite)
	if !ok {
		t.Fatal("SQLite dialect unavailable")
	}

	t.Run("fresh upgrade parity", func(t *testing.T) {
		db := openCoreDirectorySQLite(t)
		prepareCoreDirectoryPrerequisites(t, db, dia)
		assertCoreDirectoryFreshUpgradeParity(t, db, dia)
	})
	t.Run("partial and malformed refuse", func(t *testing.T) {
		db := openCoreDirectorySQLite(t)
		prepareCoreDirectoryPrerequisites(t, db, dia)
		assertCoreDirectoryPartialAndMalformedRefuse(t, db, dia)
	})
	t.Run("contract defects refuse", func(t *testing.T) {
		db := openCoreDirectorySQLite(t)
		prepareCoreDirectoryPrerequisites(t, db, dia)
		assertCoreDirectoryContractDefectsRefuse(t, db, dia)
	})
	t.Run("rollback and retry", func(t *testing.T) {
		db := openCoreDirectorySQLite(t)
		prepareCoreDirectoryPrerequisites(t, db, dia)
		assertCoreDirectoryRollbackAndRetry(t, db, dia)
	})
}

func TestCoreMigrationPlanEndsWithTransactionalDirectoryV7(t *testing.T) {
	t.Parallel()
	dia, ok := dialect.New(store.EngineSQLite)
	if !ok {
		t.Fatal("SQLite dialect unavailable")
	}
	migrations := buildCoreMigrations(dia, coreDescriptors(), nil, nil)
	if len(migrations) == 0 {
		t.Fatal("core migration plan is empty")
	}
	got := migrations[len(migrations)-1]
	if got.Version != coreDirectoryMigrationVersion || got.Name != coreDirectoryMigrationName {
		t.Fatalf("last core migration = v%d/%q, want v%d/%q",
			got.Version, got.Name, coreDirectoryMigrationVersion, coreDirectoryMigrationName)
	}
	if got.Exec == nil || got.NonTransactional || len(got.Stmts) != 0 {
		t.Fatalf("core v7 shape = Exec:%t NonTransactional:%t Stmts:%d, want Exec-only transactional",
			got.Exec != nil, got.NonTransactional, len(got.Stmts))
	}
}

func TestCoreDirectoryV7Postgres(t *testing.T) {
	pg := isolatedPG(t)
	dia, ok := dialect.New(store.EnginePostgres)
	if !ok {
		t.Fatal("PostgreSQL dialect unavailable")
	}
	db, err := sql.Open("pgx", pg.Owner)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	prepareCoreDirectoryPrerequisites(t, db, dia)

	t.Run("fresh upgrade parity", func(t *testing.T) {
		assertCoreDirectoryFreshUpgradeParity(t, db, dia)
	})
	resetCoreDirectoryV7TestState(t, db)
	t.Run("partial and malformed refuse", func(t *testing.T) {
		assertCoreDirectoryPartialAndMalformedRefuse(t, db, dia)
	})
	resetCoreDirectoryV7TestState(t, db)
	t.Run("contract defects refuse", func(t *testing.T) {
		assertCoreDirectoryContractDefectsRefuse(t, db, dia)
	})
	resetCoreDirectoryV7TestState(t, db)
	t.Run("rollback and retry", func(t *testing.T) {
		assertCoreDirectoryRollbackAndRetry(t, db, dia)
	})
}

// TestCoreDirectoryV7PostgresSurvivesTheInstalledEventFence is the K2 -> K3
// regression: every descriptor-rendered probe has an *_immutable trigger, so
// cleaning it up with DROP TABLE asks the K2 sql_drop fence to remove a guard
// and must be refused. v7 instead scopes each probe to a savepoint and rolls it
// back, which is not a DROP command and leaves neither probes nor tracking gaps.
func TestCoreDirectoryV7PostgresSurvivesTheInstalledEventFence(t *testing.T) {
	pg := isolatedPG(t)
	ctx := context.Background()
	cfg := store.Config{Engine: store.EnginePostgres, DSN: pg.App, MaxConns: 4}

	// Build a complete current database first, then turn only its durable
	// migration/guard state back into the exact K2 predecessor. This reuses the
	// production boot and the attested K2 fixture rather than inventing a weaker
	// approximation of either history.
	st, err := Open(ctx, cfg, registerWidget)
	if err != nil {
		t.Fatalf("create current fixture: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close current fixture: %v", err)
	}

	dia, ok := dialect.New(store.EnginePostgres)
	if !ok {
		t.Fatal("PostgreSQL dialect unavailable")
	}
	db, err := sql.Open("pgx", pg.Owner)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	current, err := buildGuardManifest(registryWithWidget(t).appendOnlyTables())
	if err != nil {
		t.Fatal(err)
	}
	edge, ok, err := guardManifestEditionEdge(current)
	if err != nil || !ok {
		t.Fatalf("derive K2 predecessor: ok=%t err=%v", ok, err)
	}

	// A K2 database has v2/v6 recorded, no directory relations, and a complete
	// predecessor rollout. Remove v7 and its three additions before installing
	// the event fence; after installation even their *_immutable trigger cascade
	// must be impossible to drop.
	dropDirectoryWriterGuardsForFixture(t, db, dia)
	dropCoreDirectoryTables(t, db)
	wipeGuardLogForFixture(t, db, guardGateEventsTable, "")
	wipeGuardLogForFixture(t, db, guardReceiptsTable, "")
	wipeGuardLogForFixture(t, db, guardInventoryEventsTable, "")
	seedGuardHistoricalEdition(t, db, dia, edge.From)
	seedGuardPredecessorReady(t, db, dia, edge.From, guardPredecessorComplete)
	if _, err := db.ExecContext(ctx,
		"DELETE FROM "+coreTrackingTable+" WHERE version = $1", coreDirectoryMigrationVersion); err != nil {
		t.Fatalf("restore K2 core tracking: %v", err)
	}
	installEventFenceThroughSuperuser(t, pg.Superuser)

	cfg.GuardEventFence = store.GuardEventFenceRequired
	upgraded, err := Open(ctx, cfg, registerWidget)
	if err != nil {
		t.Fatalf("open K2 -> K3 through the installed event fence: %v", err)
	}
	if err := upgraded.Close(); err != nil {
		t.Fatalf("close upgraded store: %v", err)
	}

	assertCoreDirectoryExistence(t, db, dia, map[string]bool{
		"core_directory_epoch":     true,
		"core_directory_tombstone": true,
		"core_user_tombstone":      true,
	})
	var tracked int
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM "+coreTrackingTable+" WHERE version IN (6, 7) AND reverted_at IS NULL").
		Scan(&tracked); err != nil {
		t.Fatalf("read K2 -> K3 tracking history: %v", err)
	}
	if tracked != 2 {
		t.Fatalf("K2 -> K3 tracking history has %d live rows, want 2", tracked)
	}
	for _, probe := range []string{"olv_k3p_epoch", "olv_k3p_dt", "olv_k3p_ut"} {
		var exists bool
		if err := db.QueryRowContext(ctx, `SELECT EXISTS (
SELECT 1 FROM pg_catalog.pg_class c
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1 AND c.relname = $2)`, dialect.EngineSchema, probe).Scan(&exists); err != nil {
			t.Fatalf("inspect contract probe %q: %v", probe, err)
		}
		if exists {
			t.Fatalf("contract probe %q survived its savepoint rollback", probe)
		}
	}
	var enabledFenceLegs int
	if err := db.QueryRowContext(ctx, `SELECT count(*)
FROM pg_catalog.pg_event_trigger
WHERE evtname IN ($1, $2) AND evtenabled = 'A'`,
		dialect.GuardEventFenceDropTrigger, dialect.GuardEventFenceEndTrigger).
		Scan(&enabledFenceLegs); err != nil {
		t.Fatalf("read event-fence state after v7: %v", err)
	}
	if enabledFenceLegs != 2 {
		t.Fatalf("v7 left %d enabled event-fence legs, want 2", enabledFenceLegs)
	}
	history, err := verifyGuardEditionHistory(ctx, db, dia, current)
	if err != nil {
		t.Fatalf("verify upgraded guard history: %v", err)
	}
	if history.Kind != guardEditionHistoryTransitioned {
		t.Fatalf("upgraded guard history = %s, want transitioned", history.Kind)
	}
	reopened, err := Open(ctx, cfg, registerWidget)
	if err != nil {
		t.Fatalf("reopen the K2 -> K3 database: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatalf("close reopened K2 -> K3 store: %v", err)
	}
}

func TestCoreDirectoryV7ProductionReopenSQLite(t *testing.T) {
	dia, ok := dialect.New(store.EngineSQLite)
	if !ok {
		t.Fatal("SQLite dialect unavailable")
	}
	ctx := context.Background()

	t.Run("fresh", func(t *testing.T) {
		cfg := store.Config{Engine: store.EngineSQLite, DSN: t.TempDir() + "/fresh-reopen.db"}
		for attempt := 1; attempt <= 2; attempt++ {
			st, err := Open(ctx, cfg, registerWidget)
			if err != nil {
				t.Fatalf("fresh Open attempt %d: %v", attempt, err)
			}
			if err := st.Close(); err != nil {
				t.Fatalf("fresh Close attempt %d: %v", attempt, err)
			}
		}
	})

	t.Run("K2 upgrade", func(t *testing.T) {
		cfg := store.Config{Engine: store.EngineSQLite, DSN: t.TempDir() + "/k2-reopen.db"}
		currentStore, err := Open(ctx, cfg, registerWidget)
		if err != nil {
			t.Fatalf("create current fixture: %v", err)
		}
		if err := currentStore.Close(); err != nil {
			t.Fatalf("close current fixture: %v", err)
		}
		db, err := sql.Open("sqlite", cfg.DSN)
		if err != nil {
			t.Fatal(err)
		}
		db.SetMaxOpenConns(1)
		t.Cleanup(func() { _ = db.Close() })
		current, err := buildGuardManifest(registryWithWidget(t).appendOnlyTables())
		if err != nil {
			t.Fatal(err)
		}
		edge, ok, err := guardManifestEditionEdge(current)
		if err != nil || !ok {
			t.Fatalf("derive K2 predecessor: ok=%t err=%v", ok, err)
		}
		dropDirectoryWriterGuardsForFixture(t, db, dia)
		dropCoreDirectoryTables(t, db)
		for _, table := range []string{
			guardGateEventsTable, guardReceiptsTable, guardInventoryEventsTable,
		} {
			wipeSQLiteGuardLogForFixture(t, db, table)
		}
		seedGuardHistoricalEdition(t, db, dia, edge.From)
		if _, err := db.ExecContext(ctx, dia.Rebind(
			"DELETE FROM "+coreTrackingRelation(dia)+" WHERE version = ?"),
			coreDirectoryMigrationVersion); err != nil {
			t.Fatalf("restore K2 core tracking: %v", err)
		}

		for attempt := 1; attempt <= 2; attempt++ {
			st, err := Open(ctx, cfg, registerWidget)
			if err != nil {
				t.Fatalf("K2 -> K3 Open attempt %d: %v", attempt, err)
			}
			if err := st.Close(); err != nil {
				t.Fatalf("K2 -> K3 Close attempt %d: %v", attempt, err)
			}
		}
		history, err := verifyGuardEditionHistory(ctx, db, dia, current)
		if err != nil {
			t.Fatalf("verify reopened SQLite K2 history: %v", err)
		}
		if history.Kind != guardEditionHistoryTransitioned {
			t.Fatalf("reopened SQLite K2 history = %s, want transitioned", history.Kind)
		}
	})
}

func TestCoreDirectoryV7ProductionReopenPostgresFresh(t *testing.T) {
	pg := isolatedPG(t)
	cfg := store.Config{Engine: store.EnginePostgres, DSN: pg.App, MaxConns: 4}
	for attempt := 1; attempt <= 2; attempt++ {
		st, err := Open(context.Background(), cfg, registerWidget)
		if err != nil {
			t.Fatalf("fresh PostgreSQL Open attempt %d: %v", attempt, err)
		}
		if err := st.Close(); err != nil {
			t.Fatalf("fresh PostgreSQL Close attempt %d: %v", attempt, err)
		}
	}
}

func TestCoreDirectoryV7TrackedContractTamperRefusesSQLite(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name   string
		tamper string
		probe  string
	}{
		{
			name:   "drop unique index",
			tamper: "DROP INDEX main.core_directory_tombstone_principal_uniq",
			probe:  "core_directory_tombstone_principal_uniq",
		},
		{
			name:   "drop append guard",
			tamper: "DROP TRIGGER main.core_directory_tombstone_no_delete",
			probe:  "core_directory_tombstone_no_delete",
		},
		{
			name: "replace append guard",
			tamper: `DROP TRIGGER main.core_directory_tombstone_no_delete;
CREATE TRIGGER core_directory_tombstone_no_delete
BEFORE INSERT ON core_directory_tombstone BEGIN SELECT 1; END`,
			probe: "core_directory_tombstone_no_delete",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := store.Config{Engine: store.EngineSQLite, DSN: t.TempDir() + "/tracked-tamper.db"}
			st, err := Open(ctx, cfg, registerWidget)
			if err != nil {
				t.Fatalf("create tracked v7 fixture: %v", err)
			}
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
			db, err := sql.Open("sqlite", cfg.DSN)
			if err != nil {
				t.Fatal(err)
			}
			db.SetMaxOpenConns(1)
			t.Cleanup(func() { _ = db.Close() })
			for _, stmt := range strings.Split(tc.tamper, ";\n") {
				if _, err := db.ExecContext(ctx, stmt); err != nil {
					t.Fatalf("tamper tracked v7 contract: %v", err)
				}
			}
			if got, err := Open(ctx, cfg, registerWidget); err == nil {
				_ = got.Close()
				t.Fatal("Open healed or accepted a tampered tracked-v7 directory contract")
			} else if !strings.Contains(err.Error(), "preflight core directory relations") {
				t.Fatalf("Open refused for the wrong layer: %v", err)
			}

			var definition sql.NullString
			if err := db.QueryRowContext(ctx,
				`SELECT sql FROM main.sqlite_master WHERE name=?`, tc.probe).Scan(&definition); err != nil &&
				!errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("inspect tamper after refused Open: %v", err)
			}
			switch tc.name {
			case "replace append guard":
				if !definition.Valid || !strings.Contains(definition.String, "BEFORE INSERT") {
					t.Fatalf("refused Open changed replacement trigger: %q", definition.String)
				}
			default:
				if definition.Valid {
					t.Fatalf("refused Open recreated %q as %q", tc.probe, definition.String)
				}
			}
		})
	}
}

func TestCoreDirectoryV7TrackedContractTamperRefusesPostgres(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name   string
		tamper []string
		probe  string
	}{
		{
			name:   "drop unique index",
			tamper: []string{"DROP INDEX public.core_directory_tombstone_principal_uniq"},
			probe:  "index",
		},
		{
			name:   "drop append guard",
			tamper: []string{"DROP TRIGGER core_directory_tombstone_immutable ON public.core_directory_tombstone"},
			probe:  "trigger-absent",
		},
		{
			name: "replace append guard",
			tamper: []string{
				"DROP TRIGGER core_directory_tombstone_immutable ON public.core_directory_tombstone",
				`CREATE TRIGGER core_directory_tombstone_immutable
BEFORE UPDATE ON public.core_directory_tombstone
FOR EACH ROW EXECUTE FUNCTION public.olivares_block_mutation()`,
				"ALTER TABLE ONLY public.core_directory_tombstone ENABLE ALWAYS TRIGGER core_directory_tombstone_immutable",
			},
			probe: "trigger-replaced",
		},
		{
			name: "add instead insert rule",
			tamper: []string{`CREATE RULE core_directory_tombstone_discard_insert
AS ON INSERT TO public.core_directory_tombstone DO INSTEAD NOTHING`},
			probe: "rule-replaced",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pg := isolatedPG(t)
			cfg := store.Config{Engine: store.EnginePostgres, DSN: pg.App, MaxConns: 4}
			st, err := Open(ctx, cfg, registerWidget)
			if err != nil {
				t.Fatalf("create tracked v7 fixture: %v", err)
			}
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
			db, err := sql.Open("pgx", pg.Owner)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			for _, stmt := range tc.tamper {
				if _, err := db.ExecContext(ctx, stmt); err != nil {
					t.Fatalf("tamper tracked v7 contract: %v", err)
				}
			}
			if got, err := Open(ctx, cfg, registerWidget); err == nil {
				_ = got.Close()
				t.Fatal("Open healed or accepted a tampered tracked-v7 directory contract")
			} else if !strings.Contains(err.Error(), "preflight core directory relations") {
				t.Fatalf("Open refused for the wrong layer: %v", err)
			}

			switch tc.probe {
			case "index":
				var exists bool
				if err := db.QueryRowContext(ctx,
					`SELECT pg_catalog.to_regclass('public.core_directory_tombstone_principal_uniq') IS NOT NULL`).
					Scan(&exists); err != nil {
					t.Fatal(err)
				}
				if exists {
					t.Fatal("refused Open recreated the dropped unique index")
				}
			case "trigger-absent":
				var count int
				if err := db.QueryRowContext(ctx, `SELECT count(*)
FROM pg_catalog.pg_trigger t
JOIN pg_catalog.pg_class c ON c.oid=t.tgrelid
JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
WHERE n.nspname='public' AND c.relname='core_directory_tombstone'
  AND t.tgname='core_directory_tombstone_immutable' AND NOT t.tgisinternal`).Scan(&count); err != nil {
					t.Fatal(err)
				}
				if count != 0 {
					t.Fatal("refused Open recreated the dropped append guard")
				}
			case "trigger-replaced":
				var definition string
				if err := db.QueryRowContext(ctx, `SELECT pg_catalog.pg_get_triggerdef(t.oid, false)
FROM pg_catalog.pg_trigger t
JOIN pg_catalog.pg_class c ON c.oid=t.tgrelid
JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
WHERE n.nspname='public' AND c.relname='core_directory_tombstone'
  AND t.tgname='core_directory_tombstone_immutable' AND NOT t.tgisinternal`).Scan(&definition); err != nil {
					t.Fatal(err)
				}
				if strings.Contains(definition, " OR DELETE ") || !strings.Contains(definition, "BEFORE UPDATE") {
					t.Fatalf("refused Open changed replacement trigger: %q", definition)
				}
			case "rule-replaced":
				var definition string
				if err := db.QueryRowContext(ctx, `SELECT pg_catalog.pg_get_ruledef(r.oid, false)
FROM pg_catalog.pg_rewrite r
JOIN pg_catalog.pg_class c ON c.oid=r.ev_class
JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
WHERE n.nspname='public' AND c.relname='core_directory_tombstone'
  AND r.rulename='core_directory_tombstone_discard_insert'`).Scan(&definition); err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(definition, "DO INSTEAD NOTHING") {
					t.Fatalf("refused Open changed replacement rule: %q", definition)
				}
			}
		})
	}
}

func openCoreDirectorySQLite(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", t.TempDir()+"/directory-v7.db")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func prepareCoreDirectoryPrerequisites(t *testing.T, db *sql.DB, dia dialect.Dialect) {
	t.Helper()
	for i, stmt := range dia.TenancyStmts() {
		if _, err := db.ExecContext(context.Background(), stmt); err != nil {
			t.Fatalf("apply tenancy prerequisite %d: %v", i+1, err)
		}
	}
}

func assertCoreDirectoryFreshUpgradeParity(t *testing.T, db *sql.DB, dia dialect.Dialect) {
	t.Helper()
	ctx := context.Background()
	createCoreDirectoryTables(t, db, dia, directoryDescriptors())
	fresh := readCoreDirectoryShapes(t, db, dia)
	identities := coreDirectoryRelationIdentities(t, db, dia)

	called := false
	var freshInitial coreDirectoryInitialDisposition
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = ensureCoreDirectoryRelations(ctx, tx, dia, directoryDescriptors(),
		func(_ context.Context, got *sql.Tx, initial coreDirectoryInitialDisposition) error {
			called = got == tx
			freshInitial = initial
			return nil
		})
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("fresh v7 verification: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit fresh v7 verification: %v", err)
	}
	if !called {
		t.Fatal("the v7 continuation did not receive the migration transaction")
	}
	if freshInitial != coreDirectoryInitiallyPresent {
		t.Fatalf("fresh v7 disposition = %s, want present", freshInitial)
	}
	if got := coreDirectoryRelationIdentities(t, db, dia); !reflect.DeepEqual(got, identities) {
		t.Fatalf("fresh v7 replaced a verified target relation: before=%v after=%v",
			identities, got)
	}
	if got := readCoreDirectoryShapes(t, db, dia); !reflect.DeepEqual(got, fresh) {
		t.Fatalf("fresh v7 changed relation shapes\nbefore=%+v\nafter=%+v", fresh, got)
	}

	dropCoreDirectoryTables(t, db)
	var upgradeInitial coreDirectoryInitialDisposition
	upgradeCalled := false
	if err := migrate.Apply(ctx, db, dia, coreTrackingTable, []migrate.Migration{
		coreDirectoryMigration(dia, directoryDescriptors(),
			func(_ context.Context, _ *sql.Tx, initial coreDirectoryInitialDisposition) error {
				upgradeCalled = true
				upgradeInitial = initial
				return nil
			}),
	}); err != nil {
		t.Fatalf("apply v7 to an upgraded schema: %v", err)
	}
	if !upgradeCalled || upgradeInitial != coreDirectoryInitiallyAbsent {
		t.Fatalf("upgrade v7 disposition = %s, want absent", upgradeInitial)
	}
	upgrade := readCoreDirectoryShapes(t, db, dia)
	if !reflect.DeepEqual(upgrade, fresh) {
		t.Fatalf("fresh and upgraded v7 shapes differ\nfresh=%+v\nupgrade=%+v", fresh, upgrade)
	}
	var name, phase string
	if err := db.QueryRowContext(ctx, dia.Rebind(
		"SELECT name, phase FROM "+coreTrackingTable+" WHERE version = ?"),
		coreDirectoryMigrationVersion).Scan(&name, &phase); err != nil {
		t.Fatalf("read core v7 tracking row: %v", err)
	}
	if name != coreDirectoryMigrationName || phase != migrate.Expand.String() {
		t.Fatalf("core v7 tracked as %q/%q, want %q/%q",
			name, phase, coreDirectoryMigrationName, migrate.Expand.String())
	}
}

func assertCoreDirectoryContractDefectsRefuse(t *testing.T, db *sql.DB, dia dialect.Dialect) {
	t.Helper()
	ctx := context.Background()
	tests := []struct {
		name       string
		noChecks   bool
		sqliteStmt string
		pgStmt     string
	}{
		{
			name:       "missing unique index",
			sqliteStmt: "DROP INDEX core_directory_tombstone_principal_uniq",
			pgStmt:     "DROP INDEX core_directory_tombstone_principal_uniq",
		},
		{
			name:     "missing check with identical columns",
			noChecks: true,
		},
		{
			name:       "missing append-only trigger",
			sqliteStmt: "DROP TRIGGER core_directory_tombstone_no_delete",
			pgStmt:     "DROP TRIGGER core_directory_tombstone_immutable ON core_directory_tombstone",
		},
		{
			name:       "missing tenant guard",
			sqliteStmt: "DROP TRIGGER core_directory_epoch_scope_ins",
			pgStmt:     "DROP POLICY tenant_isolation ON core_directory_epoch",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dropCoreDirectoryTables(t, db)
			descs := directoryDescriptors()
			if tc.noChecks {
				descs[0].Checks = nil
			}
			createCoreDirectoryTables(t, db, dia, descs)
			stmt := tc.sqliteStmt
			if dia.Name() == store.EnginePostgres {
				stmt = tc.pgStmt
			}
			if stmt != "" {
				if _, err := db.ExecContext(ctx, stmt); err != nil {
					t.Fatalf("create contract defect: %v", err)
				}
			}
			// The defect fixture deliberately retains the exact descriptor
			// columns; a column-only verifier would accept every case here.
			for i, desc := range directoryDescriptors() {
				tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
				if err != nil {
					t.Fatal(err)
				}
				shape, exists, err := inspectCoreDirectoryRelation(ctx, tx, dia, desc.Table)
				_ = tx.Rollback()
				if err != nil || !exists {
					t.Fatalf("inspect exact-column fixture %s: exists=%t err=%v", desc.Table, exists, err)
				}
				if err := verifyCoreDirectoryRelationShape(dia, directoryDescriptors()[i], shape); err != nil {
					t.Fatalf("fixture %s changed columns: %v", desc.Table, err)
				}
			}

			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			err = ensureCoreDirectoryRelations(ctx, tx, dia, directoryDescriptors(), nil)
			_ = tx.Rollback()
			if err == nil || !strings.Contains(err.Error(), "descriptor contract") {
				t.Fatalf("contract defect error = %v, want descriptor-contract refusal", err)
			}
		})
	}
}

func assertCoreDirectoryPartialAndMalformedRefuse(t *testing.T, db *sql.DB, dia dialect.Dialect) {
	t.Helper()
	ctx := context.Background()
	descs := directoryDescriptors()
	createCoreDirectoryTables(t, db, dia, descs[:1])

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = ensureCoreDirectoryRelations(ctx, tx, dia, descs, nil)
	_ = tx.Rollback()
	if err == nil || !strings.Contains(err.Error(), "partial relation set") {
		t.Fatalf("partial relation set error = %v, want explicit refusal", err)
	}
	assertCoreDirectoryExistence(t, db, dia, map[string]bool{
		"core_directory_epoch":     true,
		"core_directory_tombstone": false,
		"core_user_tombstone":      false,
	})

	dropCoreDirectoryTables(t, db)
	createCoreDirectoryTables(t, db, dia, descs)
	if _, err := db.ExecContext(ctx, "DROP TABLE core_directory_epoch"); err != nil {
		t.Fatalf("drop epoch for malformed fixture: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		"CREATE TABLE core_directory_epoch (id TEXT PRIMARY KEY)"); err != nil {
		t.Fatalf("create malformed epoch fixture: %v", err)
	}

	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = ensureCoreDirectoryRelations(ctx, tx, dia, descs, nil)
	_ = tx.Rollback()
	if err == nil || !strings.Contains(err.Error(), "is malformed") {
		t.Fatalf("malformed relation error = %v, want explicit refusal", err)
	}
	// Refusal is read-only: the malformed table is neither repaired nor replaced.
	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	shape, exists, err := inspectCoreDirectoryRelation(ctx, tx, dia, "core_directory_epoch")
	_ = tx.Rollback()
	if err != nil || !exists || len(shape.Columns) != 1 || shape.Columns[0].Name != "id" {
		t.Fatalf("malformed fixture was changed: exists=%t shape=%+v err=%v", exists, shape, err)
	}
}

func assertCoreDirectoryRollbackAndRetry(t *testing.T, db *sql.DB, dia dialect.Dialect) {
	t.Helper()
	ctx := context.Background()
	boom := errors.New("injected post-DDL failure")
	failedCalled := false
	failedInitial := coreDirectoryInitiallyPresent
	failing := coreDirectoryMigration(dia, directoryDescriptors(),
		func(callbackCtx context.Context, callbackTx *sql.Tx, initial coreDirectoryInitialDisposition) error {
			failedCalled = true
			failedInitial = initial
			state, err := verifyDirectoryWriterControl(callbackCtx, callbackTx, dia)
			if err != nil {
				return fmt.Errorf("writer raw state was not complete before continuation: %w", err)
			}
			if state.Mode != directoryWriterStaged || state.ExpectedGeneration != 1 {
				return fmt.Errorf("writer raw state before continuation = %+v, want staged/generation 1", state)
			}
			return boom
		})
	err := migrate.Apply(ctx, db, dia, coreTrackingTable, []migrate.Migration{failing})
	if !errors.Is(err, boom) {
		t.Fatalf("failed v7 error = %v, want injected failure", err)
	}
	if !failedCalled || failedInitial != coreDirectoryInitiallyAbsent {
		t.Fatalf("failed upgrade disposition = %s, want absent", failedInitial)
	}
	assertCoreDirectoryExistence(t, db, dia, map[string]bool{
		"core_directory_epoch":     false,
		"core_directory_tombstone": false,
		"core_user_tombstone":      false,
	})
	assertDirectoryWriterRawExistence(t, db, dia, false)
	var tracked int
	if err := db.QueryRowContext(ctx, dia.Rebind(
		"SELECT COUNT(*) FROM "+coreTrackingTable+" WHERE version = ?"),
		coreDirectoryMigrationVersion).Scan(&tracked); err != nil {
		t.Fatalf("count failed v7 tracking rows: %v", err)
	}
	if tracked != 0 {
		t.Fatalf("failed v7 left %d tracking rows, want zero", tracked)
	}

	retryCalled := false
	retryInitial := coreDirectoryInitiallyPresent
	if err := migrate.Apply(ctx, db, dia, coreTrackingTable, []migrate.Migration{
		coreDirectoryMigration(dia, directoryDescriptors(),
			func(_ context.Context, _ *sql.Tx, initial coreDirectoryInitialDisposition) error {
				retryCalled = true
				retryInitial = initial
				return nil
			}),
	}); err != nil {
		t.Fatalf("retry v7 after rollback: %v", err)
	}
	if !retryCalled || retryInitial != coreDirectoryInitiallyAbsent {
		t.Fatalf("retry disposition = %s, want absent after rollback", retryInitial)
	}
	assertCoreDirectoryExistence(t, db, dia, map[string]bool{
		"core_directory_epoch":     true,
		"core_directory_tombstone": true,
		"core_user_tombstone":      true,
	})
	assertDirectoryWriterRawExistence(t, db, dia, true)
	if err := db.QueryRowContext(ctx, dia.Rebind(
		"SELECT COUNT(*) FROM "+coreTrackingTable+" WHERE version = ?"),
		coreDirectoryMigrationVersion).Scan(&tracked); err != nil {
		t.Fatalf("count successful v7 tracking rows: %v", err)
	}
	if tracked != 1 {
		t.Fatalf("successful retry left %d tracking rows, want one", tracked)
	}
}

func createCoreDirectoryTables(
	t *testing.T,
	db *sql.DB,
	dia dialect.Dialect,
	descs []model.EntityDescriptor,
) {
	t.Helper()
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, desc := range descs {
		for i, stmt := range dia.CreateTableStmts(desc) {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				t.Fatalf("create %s statement %d: %v", desc.Table, i+1, err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit directory fixtures: %v", err)
	}
}

func readCoreDirectoryShapes(
	t *testing.T,
	db *sql.DB,
	dia dialect.Dialect,
) []coreDirectoryRelationShape {
	t.Helper()
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	shapes := make([]coreDirectoryRelationShape, 0, len(directoryDescriptors()))
	for _, desc := range directoryDescriptors() {
		shape, exists, err := inspectCoreDirectoryRelation(ctx, tx, dia, desc.Table)
		if err != nil {
			t.Fatalf("inspect %s: %v", desc.Table, err)
		}
		if !exists {
			t.Fatalf("relation %s is absent", desc.Table)
		}
		if err := verifyCoreDirectoryRelationShape(dia, desc, shape); err != nil {
			t.Fatalf("verify %s: %v", desc.Table, err)
		}
		shapes = append(shapes, shape)
	}
	return shapes
}

func assertCoreDirectoryExistence(
	t *testing.T,
	db *sql.DB,
	dia dialect.Dialect,
	want map[string]bool,
) {
	t.Helper()
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	for table, expected := range want {
		_, exists, err := inspectCoreDirectoryRelation(ctx, tx, dia, table)
		if err != nil {
			t.Fatalf("inspect %s: %v", table, err)
		}
		if exists != expected {
			t.Errorf("relation %s exists=%t, want %t", table, exists, expected)
		}
	}
}

func coreDirectoryRelationIdentities(
	t *testing.T,
	db *sql.DB,
	dia dialect.Dialect,
) []string {
	t.Helper()
	ctx := context.Background()
	identities := make([]string, 0, len(directoryDescriptors()))
	switch dia.Name() {
	case store.EngineSQLite:
		for _, desc := range directoryDescriptors() {
			var root string
			if err := db.QueryRowContext(ctx,
				"SELECT CAST(rootpage AS TEXT) FROM sqlite_master WHERE type='table' AND name=?",
				desc.Table).Scan(&root); err != nil {
				t.Fatal(err)
			}
			identities = append(identities, desc.Table+"="+root)
		}
	case store.EnginePostgres:
		for _, desc := range directoryDescriptors() {
			var oid string
			if err := db.QueryRowContext(ctx, `SELECT c.oid::text
FROM pg_catalog.pg_class c
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1 AND c.relname = $2`, dialect.EngineSchema, desc.Table).
				Scan(&oid); err != nil {
				t.Fatal(err)
			}
			identities = append(identities, desc.Table+"="+oid)
		}
	default:
		t.Fatalf("unsupported engine %q", dia.Name())
	}
	return identities
}

func dropCoreDirectoryTables(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, desc := range directoryDescriptors() {
		if _, err := db.ExecContext(context.Background(), "DROP TABLE IF EXISTS "+desc.Table); err != nil {
			t.Fatalf("drop %s: %v", desc.Table, err)
		}
	}
	// The raw control and SQLite marker are intrinsic v7 additions too. A test
	// that reconstructs K2 must remove the complete v7 state; leaving control
	// behind while deleting the tracking row is an impossible committed state
	// whose unconditional CREATE is supposed to refuse.
	for _, table := range []string{
		dialect.DirectoryWriterMarkerTable,
		dialect.DirectoryWriterControlTable,
	} {
		if _, err := db.ExecContext(context.Background(), "DROP TABLE IF EXISTS "+table); err != nil {
			t.Fatalf("drop %s: %v", table, err)
		}
	}
}

func assertDirectoryWriterRawExistence(t *testing.T, db *sql.DB, dia dialect.Dialect, expected bool) {
	t.Helper()
	ctx := context.Background()
	var controlExists bool
	switch dia.Name() {
	case store.EngineSQLite:
		if err := db.QueryRowContext(ctx,
			`SELECT EXISTS(SELECT 1 FROM main.sqlite_master WHERE type='table' AND name=?)`,
			dialect.DirectoryWriterControlTable).Scan(&controlExists); err != nil {
			t.Fatal(err)
		}
		var markerExists bool
		if err := db.QueryRowContext(ctx,
			`SELECT EXISTS(SELECT 1 FROM main.sqlite_master WHERE type='table' AND name=?)`,
			dialect.DirectoryWriterMarkerTable).Scan(&markerExists); err != nil {
			t.Fatal(err)
		}
		if markerExists != expected {
			t.Fatalf("SQLite writer marker exists=%t, want %t", markerExists, expected)
		}
	case store.EnginePostgres:
		if err := db.QueryRowContext(ctx, `SELECT EXISTS (
SELECT 1 FROM pg_catalog.pg_class c
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname=$1 AND c.relname=$2 AND c.relkind='r')`,
			dialect.EngineSchema, dialect.DirectoryWriterControlTable).Scan(&controlExists); err != nil {
			t.Fatal(err)
		}
		var markerExists bool
		if err := db.QueryRowContext(ctx, `SELECT EXISTS (
SELECT 1 FROM pg_catalog.pg_class c
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname=$1 AND c.relname=$2)`,
			dialect.EngineSchema, dialect.DirectoryWriterMarkerTable).Scan(&markerExists); err != nil {
			t.Fatal(err)
		}
		if markerExists {
			t.Fatal("PostgreSQL acquired the SQLite-only writer marker")
		}
	default:
		t.Fatalf("unsupported engine %q", dia.Name())
	}
	if controlExists != expected {
		t.Fatalf("writer control exists=%t, want %t", controlExists, expected)
	}
}

func dropDirectoryWriterGuardsForFixture(t *testing.T, db *sql.DB, dia dialect.Dialect) {
	t.Helper()
	ctx := context.Background()
	switch dia.Name() {
	case store.EngineSQLite:
		for _, spec := range sqliteDirectoryWriterGuardSpecs() {
			if _, err := db.ExecContext(ctx, "DROP TRIGGER IF EXISTS main."+spec.Name); err != nil {
				t.Fatalf("drop SQLite writer trigger %q: %v", spec.Name, err)
			}
		}
	case store.EnginePostgres:
		for _, table := range directoryWriterSourceTables {
			if _, err := db.ExecContext(ctx, fmt.Sprintf(
				"DROP TRIGGER IF EXISTS %s_directory_writer_guard ON public.%s", table, table)); err != nil {
				t.Fatalf("drop PostgreSQL writer trigger on %q: %v", table, err)
			}
		}
		if _, err := db.ExecContext(ctx,
			"DROP FUNCTION IF EXISTS public."+dialect.DirectoryWriterGuardFunction+"()"); err != nil {
			t.Fatalf("drop PostgreSQL writer function: %v", err)
		}
	default:
		t.Fatalf("unsupported engine %q", dia.Name())
	}
}

func wipeSQLiteGuardLogForFixture(t *testing.T, db *sql.DB, table string) {
	t.Helper()
	ctx := context.Background()
	rows, err := db.QueryContext(ctx, `SELECT name, sql
FROM main.sqlite_master
WHERE type='trigger' AND tbl_name=? AND sql IS NOT NULL
ORDER BY name`, table)
	if err != nil {
		t.Fatalf("read SQLite guard definitions on %q: %v", table, err)
	}
	type trigger struct{ name, definition string }
	var triggers []trigger
	for rows.Next() {
		var got trigger
		if err := rows.Scan(&got.name, &got.definition); err != nil {
			_ = rows.Close()
			t.Fatalf("read SQLite guard definitions on %q: %v", table, err)
		}
		triggers = append(triggers, got)
	}
	if err := closeCoreDirectoryRows(rows); err != nil {
		t.Fatalf("read SQLite guard definitions on %q: %v", table, err)
	}
	if len(triggers) == 0 {
		t.Fatalf("SQLite guard log %q has no triggers to preserve", table)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback() //nolint:errcheck
	for _, guard := range triggers {
		if _, err := tx.ExecContext(ctx, "DROP TRIGGER main."+guard.name); err != nil {
			t.Fatalf("drop SQLite guard %q: %v", guard.name, err)
		}
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM main."+table); err != nil {
		t.Fatalf("wipe SQLite guard log %q: %v", table, err)
	}
	for _, guard := range triggers {
		if _, err := tx.ExecContext(ctx, guard.definition); err != nil {
			t.Fatalf("restore SQLite guard %q: %v", guard.name, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit SQLite guard wipe %q: %v", table, err)
	}
}

func resetCoreDirectoryV7TestState(t *testing.T, db *sql.DB) {
	t.Helper()
	dropCoreDirectoryTables(t, db)
	if _, err := db.ExecContext(context.Background(), "DROP TABLE IF EXISTS "+coreTrackingTable); err != nil {
		t.Fatalf("drop core tracker: %v", err)
	}
}
