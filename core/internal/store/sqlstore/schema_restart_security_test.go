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

	"github.com/olivaresai/olivares/core/store"
)

func TestRolloutEvidenceGuardsRepairOnRestart(t *testing.T) {
	for _, table := range []string{"control_rollout_transitions", "control_rollout_classifications"} {
		for _, mutation := range []string{"update", "delete"} {
			t.Run(table+"/"+mutation, func(t *testing.T) {
				ctx := context.Background()
				cfg := store.Config{Engine: store.EngineSQLite, DSN: filepath.Join(t.TempDir(), "restart.db")}
				st, err := Open(ctx, cfg, registerWidgetStaged)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if st != nil {
						_ = st.Close()
					}
				})
				before := rolloutStateOf(t, st, testControlKey)
				_, err = st.(store.RolloutStater).SetRolloutMode(ctx, store.RolloutTransition{
					Key: testControlKey, Mode: store.RolloutPolicyOptional, Actor: "operator", Reason: "local use", ExpectGeneration: before.Generation,
				})
				if err != nil {
					t.Fatal(err)
				}
				db := st.(*sqlStore).db
				var applied string
				if err := db.QueryRow("SELECT applied_at FROM schema_migrations_rollout_guards WHERE version = 1").Scan(&applied); err != nil {
					t.Fatal(err)
				}
				if err := st.Close(); err != nil {
					t.Fatal(err)
				}
				raw, err := sql.Open("sqlite", cfg.DSN)
				if err != nil {
					t.Fatal(err)
				}
				defer raw.Close()
				if _, err := raw.Exec("DROP TRIGGER " + table + "_no_" + mutation); err != nil {
					t.Fatal(err)
				}
				st, err = Open(ctx, cfg, registerWidgetStaged)
				if err != nil {
					t.Fatalf("restart must repair the missing guard: %v", err)
				}
				db = st.(*sqlStore).db
				var again string
				if err := db.QueryRow("SELECT applied_at FROM schema_migrations_rollout_guards WHERE version = 1").Scan(&again); err != nil {
					t.Fatal(err)
				}
				if again != applied {
					t.Fatal("restart rewrote migration receipt")
				}
				var count int
				if err := db.QueryRow("SELECT COUNT(*) FROM "+table+" WHERE control_key = ?", testControlKey).Scan(&count); err != nil || count != 1 {
					t.Fatalf("evidence not preserved: rows=%d err=%v", count, err)
				}
				statement := "UPDATE " + table + " SET control_key = 'tampered' WHERE control_key = ?"
				if mutation == "delete" {
					statement = "DELETE FROM " + table + " WHERE control_key = ?"
				}
				if _, err := db.Exec(statement, testControlKey); err == nil || !strings.Contains(err.Error(), "append-only") {
					t.Fatalf("restart failed to protect evidence from %s: %v", mutation, err)
				}
			})
		}
	}
}

func TestOSAccountReservationIndexesRepairOnRestart(t *testing.T) {
	for _, engine := range store.SupportedEngines() {
		for _, key := range []string{"uid", "account", "subject"} {
			t.Run(string(engine)+"/"+key, func(t *testing.T) {
				ctx := context.Background()
				cfg := store.Config{Engine: engine, DSN: filepath.Join(t.TempDir(), "restart.db")}
				if engine == store.EnginePostgres {
					cfg.DSN = isolatedPG(t).App
				}
				st, err := Open(ctx, cfg, nil)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if st != nil {
						_ = st.Close()
					}
				})
				if err := st.Close(); err != nil {
					t.Fatal(err)
				}
				driver := "sqlite"
				if engine == store.EnginePostgres {
					driver = "pgx"
				}
				raw, err := sql.Open(driver, cfg.DSN)
				if err != nil {
					t.Fatal(err)
				}
				defer raw.Close()
				index := "core_credential_bindings_os_" + key + "_uniq"
				if _, err := raw.Exec("DROP INDEX " + index); err != nil {
					t.Fatal(err)
				}
				st, err = Open(ctx, cfg, nil)
				if err != nil {
					t.Fatalf("restart must repair the missing reservation index: %v", err)
				}
				query := "SELECT sql FROM sqlite_master WHERE type = 'index' AND name = ?"
				if engine == store.EnginePostgres {
					query = "SELECT indexdef FROM pg_indexes WHERE schemaname = current_schema() AND indexname = $1"
				}
				var definition string
				if err := raw.QueryRow(query, index).Scan(&definition); err != nil {
					t.Fatalf("reservation index missing after restart: %v", err)
				}
				if !strings.Contains(definition, "UNIQUE INDEX") || !strings.Contains(definition, "os_account") || !strings.Contains(definition, "subject_generation") {
					t.Fatalf("reservation index lost its enforcement: %s", definition)
				}
			})
		}
	}
}
