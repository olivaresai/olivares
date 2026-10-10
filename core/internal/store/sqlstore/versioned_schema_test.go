// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestSchemaReconciliationIsVersioned(t *testing.T) {
	for _, engine := range []store.Engine{store.EngineSQLite, store.EnginePostgres} {
		t.Run(string(engine), func(t *testing.T) {
			cfg := store.Config{Engine: engine, DSN: filepath.Join(t.TempDir(), "upgrade.db")}
			if engine == store.EnginePostgres {
				cfg.DSN = isolatedPG(t).App
			}
			ctx := context.Background()
			st, err := Open(ctx, cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.Close() })
			db := st.(*sqlStore).db
			var n int
			if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations_core WHERE version > 20").Scan(&n); err != nil {
				t.Fatal(err)
			}
			if n != 7 { // v21..v26 plus v27 user-group placement
				t.Fatalf("boot reconciliations recorded = %d, want 7", n)
			}
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
			st, err = Open(ctx, cfg, nil)
			if err != nil {
				t.Fatalf("reopen: %v", err)
			}
			defer st.Close()
		})
	}
}

func TestModuleSchemaTrackingSupportsMaximumTableNames(t *testing.T) {
	for _, engine := range []store.Engine{store.EngineSQLite, store.EnginePostgres} {
		t.Run(string(engine), func(t *testing.T) {
			cfg := store.Config{Engine: engine, DSN: filepath.Join(t.TempDir(), "names.db")}
			if engine == store.EnginePostgres {
				cfg.DSN = isolatedPG(t).App
			}
			prefix := "test_" + strings.Repeat("x", 34)
			st, err := Open(context.Background(), cfg, func(reg store.ExtensionRegistry) error {
				for _, suffix := range []string{"a", "b"} {
					if err := reg.Register(model.EntityDescriptor{Kind: model.Kind("test." + suffix), Table: prefix + suffix}); err != nil {
						return err
					}
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			for _, suffix := range []string{"a", "b"} {
				var n int
				if err := st.(*sqlStore).db.QueryRow("SELECT COUNT(*) FROM schema_migrations_tbl_" + prefix + suffix).Scan(&n); err != nil {
					t.Fatal(err)
				}
				if n != 1 {
					t.Fatalf("tracker %s has %d records", suffix, n)
				}
			}
		})
	}
}

func TestModuleSchemaAdoptsLegacyTableThroughRunner(t *testing.T) {
	for _, engine := range store.SupportedEngines() {
		t.Run(string(engine), func(t *testing.T) {
			ctx := context.Background()
			var db *sql.DB
			var dia dialect.Dialect
			if engine == store.EnginePostgres {
				_, db, dia = accessEvidencePGStore(t)
			} else {
				_, db, dia = accessEvidenceSQLiteStore(t)
			}
			for _, stmt := range []string{
				"CREATE TABLE applied_module_tables (table_name TEXT PRIMARY KEY, applied_at TEXT NOT NULL)",
				"INSERT INTO applied_module_tables VALUES ('test_items', 'legacy')",
				"CREATE TABLE test_items (id TEXT PRIMARY KEY, tenant_id TEXT NOT NULL, label TEXT)",
				"INSERT INTO test_items VALUES ('kept', 'tenant', 'existing')",
			} {
				if _, err := db.ExecContext(ctx, stmt); err != nil {
					t.Fatal(err)
				}
			}
			desc := model.EntityDescriptor{Kind: "test.item", Table: "test_items", Fields: []model.FieldSpec{
				{Name: "label", Kind: model.KindText, Nullable: true},
				{Name: "note", Kind: model.KindText, Nullable: true},
			}}
			if err := applyModuleTables(ctx, db, dia, []model.EntityDescriptor{desc}); err != nil {
				t.Fatal(err)
			}
			var label string
			var note sql.NullString
			if err := db.QueryRowContext(ctx, "SELECT label, note FROM test_items WHERE id = 'kept'").Scan(&label, &note); err != nil {
				t.Fatal(err)
			}
			if label != "existing" || note.Valid {
				t.Fatalf("legacy row changed: %q, %v", label, note)
			}
			var applied string
			if err := db.QueryRowContext(ctx, "SELECT applied_at FROM applied_module_tables WHERE table_name = 'test_items'").Scan(&applied); err != nil {
				t.Fatal(err)
			}
			if applied != "legacy" {
				t.Fatal("legacy tracking record changed")
			}
			if err := db.QueryRowContext(ctx, "SELECT applied_at FROM schema_migrations_tbl_test_items WHERE version = 1").Scan(&applied); err != nil {
				t.Fatal(err)
			}
			if err := applyModuleTables(ctx, db, dia, []model.EntityDescriptor{desc}); err != nil {
				t.Fatal(err)
			}
			var again string
			if err := db.QueryRowContext(ctx, "SELECT applied_at FROM schema_migrations_tbl_test_items WHERE version = 1").Scan(&again); err != nil {
				t.Fatal(err)
			}
			if again != applied {
				t.Fatal("module schema migration replayed")
			}
		})
	}
}

func TestModuleSchemaMigrationRollsBackPartialDDL(t *testing.T) {
	for _, engine := range store.SupportedEngines() {
		t.Run(string(engine), func(t *testing.T) {
			ctx := context.Background()
			var db *sql.DB
			var dia dialect.Dialect
			if engine == store.EnginePostgres {
				_, db, dia = accessEvidencePGStore(t)
			} else {
				_, db, dia = accessEvidenceSQLiteStore(t)
			}
			if _, err := db.ExecContext(ctx, "CREATE TABLE test_atomic (id TEXT PRIMARY KEY, tenant_id TEXT NOT NULL)"); err != nil {
				t.Fatal(err)
			}
			if _, err := db.ExecContext(ctx, "INSERT INTO test_atomic VALUES ('kept', 'tenant')"); err != nil {
				t.Fatal(err)
			}
			desc := model.EntityDescriptor{Kind: "test.atomic", Table: "test_atomic", Fields: []model.FieldSpec{
				{Name: "note", Kind: model.KindText, Nullable: true},
				{Name: "required_value", Kind: model.KindText},
			}}
			if err := applyModuleTables(ctx, db, dia, []model.EntityDescriptor{desc}); err == nil {
				t.Fatal("required column without backfill was accepted")
			}
			columns, err := dia.TableColumns(ctx, db, desc.Table)
			if err != nil {
				t.Fatal(err)
			}
			if columns["note"] {
				t.Fatal("partial DDL survived rollback")
			}
			var count int
			if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations_tbl_test_atomic").Scan(&count); err != nil || count != 0 {
				t.Fatalf("failed version recorded: %d, %v", count, err)
			}
			if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM test_atomic WHERE id = 'kept' AND tenant_id = 'tenant'").Scan(&count); err != nil || count != 1 {
				t.Fatalf("existing row changed: %d, %v", count, err)
			}
		})
	}
}

func TestVersionedBootCheckpointAdmission(t *testing.T) {
	for _, engine := range store.SupportedEngines() {
		for _, damage := range []string{"", "missing rollout table", "reverted record", "future version", "other tracker collision"} {
			t.Run(string(engine)+"/"+damage, func(t *testing.T) {
				ctx := context.Background()
				var cfg store.Config
				var db *sql.DB
				var dia dialect.Dialect
				if engine == store.EnginePostgres {
					cfg, db, dia = accessEvidencePGStore(t)
				} else {
					cfg, db, dia = accessEvidenceSQLiteStore(t)
				}
				reg := freshBootstrapRegistry(t, registerFreshBootstrapModule)
				if err := classifyRolloutControls(ctx, db, dia, reg.rolloutControls(), &freshBootstrapAdmission{}); err != nil {
					t.Fatal(err)
				}
				var statement string
				switch damage {
				case "missing rollout table":
					statement = "DROP TABLE control_rollout_state"
				case "reverted record":
					statement = "UPDATE schema_migrations_rollout SET reverted_at = 'changed'"
				case "future version":
					statement = "UPDATE schema_migrations_rollout SET version = 2"
				case "other tracker collision":
					statement = "CREATE TABLE schema_migrations_tbl_rrw_widget (foreign_value TEXT)"
				}
				if statement != "" {
					if _, err := db.ExecContext(ctx, statement); err != nil {
						t.Fatal(err)
					}
				}
				st, err := Open(ctx, cfg, registerFreshBootstrapModule)
				if damage == "" {
					if err != nil {
						t.Fatal(err)
					}
					defer st.Close()
				} else {
					if err == nil {
						st.Close()
						t.Fatal("damaged checkpoint admitted")
					}
					tracked, checkErr := coreTrackingRelationExists(ctx, db, dia)
					if checkErr != nil || tracked {
						t.Fatalf("refused boot changed core tracking: present=%v err=%v", tracked, checkErr)
					}
				}
			})
		}
	}
}
