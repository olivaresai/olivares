// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
package sqlstore

import (
	"context"
	"database/sql"
	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/store"
	"reflect"
	"testing"
)

// Reconstruct the exact v17 predecessor, then witness v18 on both engines.
func TestTOTPMigrationFromSeventeen(t *testing.T) {
	for _, engine := range store.SupportedEngines() {
		t.Run(string(engine), func(t *testing.T) {
			ctx := context.Background()
			s, cfg, users, _ := f2aFreshTarget(t, engine)
			tables := []string{"totp_credentials", "totp_recovery_codes", "auth_policy"}
			fresh := make([]map[string]bool, len(tables))
			for i, table := range tables {
				var err error
				fresh[i], err = s.dia.TableColumns(ctx, s.db, table)
				if err != nil || len(fresh[i]) == 0 {
					t.Fatalf("fresh %s schema unavailable: %v", table, err)
				}
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			driver, dsn := "sqlite", cfg.DSN
			if engine == store.EnginePostgres {
				driver, dsn = "pgx", cfg.OwnerDSN
			}
			db, err := sql.Open(driver, dsn)
			if err != nil {
				t.Fatal(err)
			}
			for _, table := range []string{"totp_recovery_codes", "totp_credentials", "auth_policy"} {
				if _, err = db.ExecContext(ctx, "DROP TABLE "+table); err != nil {
					t.Fatal(err)
				}
			}
			// v19 creates this relation with its version record. Remove both
			// halves when reconstructing the predecessor, not just the record.
			for _, table := range dialect.FinOpsCustodyControlTables() {
				if _, err := db.ExecContext(ctx, "DROP TABLE "+table); err != nil {
					t.Fatal(err)
				}
			}
			if engine == store.EnginePostgres {
				if _, err := db.ExecContext(ctx, "DROP FUNCTION public."+dialect.PostgresCustodyGuardFunction+"()"); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = db.ExecContext(ctx, s.dia.Rebind("DELETE FROM "+coreTrackingTable+" WHERE version > ?"), 17); err != nil {
				t.Fatal(err)
			}
			if err = db.Close(); err != nil {
				t.Fatal(err)
			}
			raw, err := Open(ctx, cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = raw.Close() })
			up := raw.(*sqlStore)
			for i, table := range tables {
				cols, err := up.dia.TableColumns(ctx, up.db, table)
				if err != nil || !reflect.DeepEqual(cols, fresh[i]) {
					t.Fatalf("upgraded %s differs: %v", table, err)
				}
			}
			var count int
			if err = up.db.QueryRowContext(ctx, up.dia.Rebind("SELECT count(*) FROM "+coreTrackingTable+" WHERE version = ?"), 18).Scan(&count); err != nil || count != 1 {
				t.Fatalf("v18 record count=%d err=%v", count, err)
			}
			if err = raw.AuthView(ctx, func(as store.AuthScope) error { _, err := as.Users().Get(ctx, users[0].ID); return err }); err != nil {
				t.Fatal("upgrade lost existing user")
			}
			testTOTPRoundTrip(t, raw)
		})
	}
}
