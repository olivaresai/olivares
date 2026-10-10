// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestFinOpsCustodyRegistrationFreshAndRestartSQLite(t *testing.T) {
	cfg := store.Config{Engine: store.EngineSQLite, DSN: filepath.Join(t.TempDir(), "custody-registration.db")}
	testFinOpsCustodyRegistrationFreshAndRestart(t, cfg)
}

func TestFinOpsCustodyRegistrationFreshAndRestartPostgres(t *testing.T) {
	dsns := isolatedPGSplit(t)
	testFinOpsCustodyRegistrationFreshAndRestart(t, store.Config{Engine: store.EnginePostgres, DSN: dsns.App, OwnerDSN: dsns.Owner, AdminDSN: dsns.Admin})
}

func testFinOpsCustodyRegistrationFreshAndRestart(t *testing.T, cfg store.Config) {
	t.Helper()
	ctx := context.Background()
	st, err := Open(ctx, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ss := st.(*sqlStore)
	owner := finOpsCustodyRegistrationOwner(t, cfg)
	var name, phase string
	if err := owner.QueryRowContext(ctx, "SELECT name, phase FROM "+coreTrackingRelation(ss.dia)+" WHERE version = 19").Scan(&name, &phase); err != nil {
		t.Fatalf("registered custody migration missing: %v", err)
	}
	if name != "finops_custody_control_v1" || phase != "expand" {
		t.Fatalf("custody tracking = %q %q", name, phase)
	}
	for _, table := range dialect.FinOpsCustodyControlTables() {
		columns, err := ss.dia.TableColumns(ctx, ss.db, table)
		if err != nil || len(columns) == 0 {
			t.Fatalf("custody relation %s missing: %v", table, err)
		}
		if err := st.View(ctx, model.NewTenantID(), func(sc store.Scope) error {
			if _, err := sc.Ext(model.Kind(table)); err == nil {
				t.Fatalf("tenant repository exposes raw custody relation %s", table)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = Open(ctx, cfg, nil)
	if err != nil {
		t.Fatalf("restart refused valid custody: %v", err)
	}
	statement := "DROP TRIGGER control_custody_transition_no_delete"
	if cfg.Engine == store.EnginePostgres {
		statement = "DROP TRIGGER control_custody_transition_immutable ON public.control_custody_transition"
	}
	if _, err := owner.ExecContext(ctx, statement); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	refused, err := Open(ctx, cfg, nil)
	if refused != nil {
		_ = refused.Close()
	}
	if err == nil {
		t.Fatal("restart accepted missing custody retention guard")
	}
	if !strings.Contains(err.Error(), "custody") {
		t.Fatalf("restart refused for an unrelated reason: %v", err)
	}
}

func finOpsCustodyRegistrationOwner(t *testing.T, cfg store.Config) *sql.DB {
	t.Helper()
	driver, dsn := "sqlite", cfg.DSN
	if cfg.Engine == store.EnginePostgres {
		driver, dsn = "pgx", cfg.OwnerDSN
	}
	db, err := sql.Open(driver, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestFinOpsCustodyRegistrationUpgradePreservesExistingUsers(t *testing.T) {
	for _, engine := range store.SupportedEngines() {
		t.Run(string(engine), func(t *testing.T) {
			ctx := context.Background()
			st, cfg, users, _ := f2aFreshTarget(t, engine)
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
			owner := finOpsCustodyRegistrationOwner(t, cfg)
			// Reconstruct only the predecessor's custody absence in this disposable
			// fixture. Existing identities and their authority remain populated.
			if err := rewindFinOpsCustodyForHistoricalFixture(ctx, owner, st.dia); err != nil {
				t.Fatal(err)
			}
			var predecessor int
			if err := owner.QueryRowContext(ctx, "SELECT MAX(version) FROM "+coreTrackingRelation(st.dia)).Scan(&predecessor); err != nil || predecessor != 18 {
				t.Fatalf("predecessor = %d, want 18: %v", predecessor, err)
			}
			raw, err := Open(ctx, cfg, nil)
			if err != nil {
				t.Fatalf("upgrade refused: %v", err)
			}
			t.Cleanup(func() { _ = raw.Close() })
			if err := raw.AuthView(ctx, func(scope store.AuthScope) error {
				for _, want := range users {
					got, err := scope.Users().Get(ctx, want.ID)
					if err != nil {
						return err
					}
					if !reflect.DeepEqual(got, want) {
						t.Fatal("custody upgrade changed an existing user")
					}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			var count int
			if err := owner.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+coreTrackingRelation(st.dia)+" WHERE version = 19 AND name = 'finops_custody_control_v1' AND phase = 'expand'").Scan(&count); err != nil || count != 1 {
				t.Fatalf("upgrade custody tracking count = %d, want 1: %v", count, err)
			}
		})
	}
}

func TestFinOpsCustodyRegistrationRefusesTrackedDriftBeforeRepair(t *testing.T) {
	for _, engine := range store.SupportedEngines() {
		for _, change := range []string{"tracking_name", "tracking_phase", "tracking_reverted", "missing_guard", "extra_column", "missing_unique_index", "public_select", "app_column_update"} {
			if engine == store.EngineSQLite && (change == "public_select" || change == "app_column_update") {
				continue
			}
			t.Run(string(engine)+"/"+change, func(t *testing.T) {
				ctx := context.Background()
				cfg := store.Config{Engine: engine, DSN: filepath.Join(t.TempDir(), "custody-drift.db")}
				if engine == store.EnginePostgres {
					dsns := isolatedPGSplit(t)
					cfg.DSN, cfg.OwnerDSN, cfg.AdminDSN = dsns.App, dsns.Owner, dsns.Admin
				}
				st, err := Open(ctx, cfg, nil)
				if err != nil {
					t.Fatal(err)
				}
				dia := st.(*sqlStore).dia
				if err := st.Close(); err != nil {
					t.Fatal(err)
				}
				owner := finOpsCustodyRegistrationOwner(t, cfg)
				tracker := coreTrackingRelation(dia)
				var tracked int
				if err := owner.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+tracker+" WHERE version = 19").Scan(&tracked); err != nil || tracked != 1 {
					t.Fatalf("custody is not registered: count=%d err=%v", tracked, err)
				}
				var mutation string
				switch change {
				case "tracking_name":
					mutation = "UPDATE " + tracker + " SET name = 'foreign_custody' WHERE version = 19"
				case "tracking_phase":
					mutation = "UPDATE " + tracker + " SET phase = 'contract' WHERE version = 19"
				case "tracking_reverted":
					mutation = "UPDATE " + tracker + " SET reverted_at = '2026-10-02T00:00:00Z' WHERE version = 19"
				case "missing_guard":
					mutation = "DROP TRIGGER control_custody_transition_no_delete"
					if engine == store.EnginePostgres {
						mutation = "DROP TRIGGER control_custody_transition_immutable ON public.control_custody_transition"
					}
				case "extra_column":
					mutation = "ALTER TABLE " + directoryWriterRelation(dia, dialect.ControlCustodyProofTable) + " ADD COLUMN foreign_payload TEXT"
				case "missing_unique_index":
					mutation = "DROP INDEX control_custody_enrollment_one_live"
				case "public_select":
					mutation = "GRANT SELECT ON public.control_custody_proof TO PUBLIC"
				case "app_column_update":
					app := openCustodyPGPool(t, cfg.DSN)
					mutation = "GRANT UPDATE (actor) ON public.control_custody_transition TO " + quoteIdent(currentCustodyRole(t, app))
				}
				if _, err := owner.ExecContext(ctx, mutation); err != nil {
					t.Fatal(err)
				}
				before := finOpsCustodyDurableCatalog(t, owner, engine)
				refused, err := Open(ctx, cfg, nil)
				if refused != nil {
					_ = refused.Close()
				}
				if err == nil || !strings.Contains(err.Error(), "custody") {
					t.Fatalf("custody drift refusal = %v", err)
				}
				if after := finOpsCustodyDurableCatalog(t, owner, engine); after != before {
					t.Fatal("refused custody boot changed the durable catalog")
				}
			})
		}
	}
}

// Read the durable catalog independently of the verifier's projection. Temporary
// reference objects must never change any public/main object or its privileges.
func finOpsCustodyDurableCatalog(t *testing.T, db *sql.DB, engine store.Engine) string {
	t.Helper()
	queries := []string{`SELECT COALESCE(group_concat(row, char(10)), '') FROM (SELECT type || ':' || name || ':' || COALESCE(sql, '') AS row FROM main.sqlite_master ORDER BY type, name)`}
	if engine == store.EnginePostgres {
		queries = []string{
			`SELECT COALESCE(jsonb_agg(to_jsonb(c) ORDER BY c.oid)::text, '') FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = 'public'`,
			`SELECT COALESCE(jsonb_agg(to_jsonb(a) ORDER BY a.attrelid, a.attnum)::text, '') FROM pg_catalog.pg_attribute a JOIN pg_catalog.pg_class c ON c.oid = a.attrelid JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = 'public'`,
			`SELECT COALESCE(jsonb_agg(to_jsonb(c) ORDER BY c.oid)::text, '') FROM pg_catalog.pg_constraint c JOIN pg_catalog.pg_namespace n ON n.oid = c.connamespace WHERE n.nspname = 'public'`,
			`SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY t.oid)::text, '') FROM pg_catalog.pg_trigger t JOIN pg_catalog.pg_class c ON c.oid = t.tgrelid JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = 'public'`,
			`SELECT COALESCE(jsonb_agg(to_jsonb(p) ORDER BY p.oid)::text, '') FROM pg_catalog.pg_proc p JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace WHERE n.nspname = 'public'`,
			`SELECT COALESCE(jsonb_agg(to_jsonb(i) ORDER BY i.indexrelid)::text, '') FROM pg_catalog.pg_index i JOIN pg_catalog.pg_class c ON c.oid = i.indrelid JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = 'public'`,
		}
	}
	var out strings.Builder
	for _, query := range queries {
		var value string
		if err := db.QueryRowContext(context.Background(), query).Scan(&value); err != nil {
			t.Fatal(err)
		}
		out.WriteString(value)
		out.WriteByte('\n')
	}
	return out.String()
}
