// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/store"
)

// These are catalog bytes from real engines, not hashes of migration strings.
// This also proves the permanent row cannot be deleted even by its owner.
func TestUserAuthorityRetentionCatalog(t *testing.T) {
	for _, engine := range store.SupportedEngines() {
		t.Run(string(engine), func(t *testing.T) {
			ctx := context.Background()
			var db *sql.DB
			var err error
			if engine == store.EngineSQLite {
				db, err = openSQLite(filepath.Join(t.TempDir(), "authority.db"))
			} else {
				pg := isolatedPGSplit(t)
				db, err = openPGPinnedToEngineSchema(pg.Owner, 2)
			}
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			dia, _ := dialect.New(engine)
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			for _, stmt := range append(dia.TenancyStmts(), dia.CreateTableStmts(userAuthorityDescriptor)...) {
				if _, err := tx.ExecContext(ctx, stmt); err != nil {
					t.Fatal(err)
				}
			}
			if engine == store.EnginePostgres {
				if _, err := tx.ExecContext(ctx, postgresUserAuthorityRetentionDDL); err != nil {
					t.Fatal(err)
				}
			}
			for _, stmt := range userAuthorityRetentionDDL(dia) {
				if _, err := tx.ExecContext(ctx, stmt); err != nil {
					t.Fatal(err)
				}
			}
			triggers, err := dia.SchemaTriggers(ctx, tx)
			if err != nil {
				t.Fatal(err)
			}
			schema := "main"
			if engine == store.EnginePostgres {
				schema = "public"
			}
			trigger, found := triggers[dialect.TriggerKey{Schema: schema, Table: userAuthorityDescriptor.Table, Name: "core_user_authority_no_delete"}]
			if !found {
				t.Fatal("retention guard missing")
			}
			digest := fmt.Sprintf("%x", sha256.Sum256([]byte(trigger.Definition)))
			t.Logf("retention catalog SHA256 %s", digest)
			if want := userAuthoritySchemaInvariants()[engine][0].DefinitionSHA256; digest != want {
				t.Fatalf("retention definition = %s, want %s", digest, want)
			}
			if err := dia.BindTenant(ctx, tx, "ffffffff-ffff-ffff-ffff-ffffffffffff"); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.ExecContext(ctx, "INSERT INTO core_user_authority(id,tenant_id,created_at,updated_at,version) VALUES ('01993926-1000-7000-8000-000000000001','ffffffff-ffff-ffff-ffff-ffffffffffff','2026-09-07T00:00:00Z','2026-09-07T00:00:00Z',1)"); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.ExecContext(ctx, "SAVEPOINT delete_probe"); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.ExecContext(ctx, "DELETE FROM core_user_authority"); err == nil {
				t.Fatal("permanent User authority row was deleted")
			}
			if _, err := tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT delete_probe"); err != nil {
				t.Fatal(err)
			}
			var count int
			if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM core_user_authority").Scan(&count); err != nil || count != 1 {
				t.Fatalf("retention count=%d err=%v", count, err)
			}
		})
	}
}
