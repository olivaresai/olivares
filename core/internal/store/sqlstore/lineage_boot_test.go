// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"github.com/olivaresai/olivares/core/store"
	"path/filepath"
	"testing"
)

func TestLineageBootAttestationSQLite(t *testing.T) {
	for name, ddl := range map[string]string{
		"epoch":         "DROP TABLE main.core_sessions_lineage_epoch",
		"guard":         "DROP TRIGGER main.sessions_lineage_update",
		"extra_trigger": "CREATE TRIGGER unapproved_session_effect AFTER UPDATE ON sessions BEGIN SELECT 1; END",
		"seeded":        "DROP TABLE main.core_lineage_seeded",
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			cfg := store.Config{Engine: store.EngineSQLite, DSN: filepath.Join(t.TempDir(), "lineage.db")}
			st, e := Open(ctx, cfg, nil)
			if e != nil {
				t.Fatal(e)
			}
			provisionTenant(t, st, "restart")
			if e = st.Close(); e != nil {
				t.Fatal(e)
			}
			st, e = Open(ctx, cfg, nil)
			if e != nil {
				t.Fatalf("unchanged restart: %v", e)
			}
			_ = st.Close()
			db, e := sql.Open("sqlite", cfg.DSN)
			if e != nil {
				t.Fatal(e)
			}
			defer db.Close()
			if _, e = db.ExecContext(ctx, ddl); e != nil {
				t.Fatal(e)
			}
			st, e = Open(ctx, cfg, nil)
			if e == nil {
				st.Close()
				t.Fatal("altered authority boot accepted")
			}
			if name == "epoch" {
				var n int
				if e = db.QueryRowContext(ctx, "SELECT count(*) FROM main.sqlite_master WHERE name='core_sessions_lineage_epoch'").Scan(&n); e != nil {
					t.Fatal(e)
				}
				if n != 0 {
					t.Fatal("preflight repaired missing authority")
				}
			}
		})
	}
}

func TestLineageBootAttestationPostgres(t *testing.T) {
	for name, ddl := range map[string]string{
		"epoch":         "DROP TABLE public.core_sessions_lineage_epoch",
		"guard":         "ALTER TABLE public.sessions DISABLE TRIGGER sessions_lineage_guard",
		"rls":           "ALTER TABLE public.sessions DISABLE ROW LEVEL SECURITY",
		"policy":        "CREATE POLICY invisible_lineage ON public.sessions AS RESTRICTIVE FOR SELECT USING(false)",
		"extra_trigger": "CREATE TRIGGER unapproved_session_effect BEFORE UPDATE ON public.sessions FOR EACH ROW EXECUTE FUNCTION public.olivares_sessions_lineage_guard()",
		"seeded":        "DROP TABLE public.core_lineage_seeded",
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			pg := isolatedPGSplit(t)
			cfg := store.Config{Engine: store.EnginePostgres, DSN: pg.App, OwnerDSN: pg.Owner, AdminDSN: pg.Admin, MaxConns: 4}
			st, e := Open(ctx, cfg, nil)
			if e != nil {
				t.Fatal(e)
			}
			provisionTenant(t, st, "restart")
			if e = st.Close(); e != nil {
				t.Fatal(e)
			}
			st, e = Open(ctx, cfg, nil)
			if e != nil {
				t.Fatalf("unchanged restart: %v", e)
			}
			_ = st.Close()
			db, e := sql.Open("pgx", pg.Owner)
			if e != nil {
				t.Fatal(e)
			}
			defer db.Close()
			if _, e = db.ExecContext(ctx, ddl); e != nil {
				t.Fatal(e)
			}
			st, e = Open(ctx, cfg, nil)
			if e == nil {
				st.Close()
				t.Fatal("altered authority boot accepted")
			}
			if name == "epoch" {
				var n int
				if e = db.QueryRowContext(ctx, "SELECT count(*) FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relname='core_sessions_lineage_epoch'").Scan(&n); e != nil {
					t.Fatal(e)
				}
				if n != 0 {
					t.Fatal("preflight repaired missing authority")
				}
			}
		})
	}
}
