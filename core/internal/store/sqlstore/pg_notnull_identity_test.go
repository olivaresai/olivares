// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/store"
)

// Rename only the cataloged NOT NULL constraints. Before PostgreSQL 18 this
// property lives in pg_attribute; there are no names to change.
func renamePostgresNotNullConstraints(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	rows, err := db.Query(`SELECT conname::pg_catalog.text FROM pg_catalog.pg_constraint
 WHERE conrelid = $1::pg_catalog.regclass AND contype = 'n' ORDER BY conname`, "public."+table)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			_ = rows.Close()
			t.Fatal(err)
		}
		names = append(names, name)
	}
	if err := errorsJoinRows(rows); err != nil {
		t.Fatal(err)
	}
	for i, name := range names {
		// #nosec G202 -- test-owned relation and catalog identifiers are quoted.
		stmt := "ALTER TABLE public." + quoteIdent(table) + " RENAME CONSTRAINT " + quoteIdent(name) + " TO " + quoteIdent(fmt.Sprintf("renamed_not_null_%d", i))
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	return len(names)
}

func TestPostgresLoginCapabilityNotNullIdentityAndDrift(t *testing.T) {
	for _, variant := range []string{"fresh_open", "schema_apply", "renamed_not_null", "missing_not_null", "renamed_primary_key"} {
		t.Run(variant, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			pg := isolatedPGSplit(t)
			cfg := store.Config{Engine: store.EnginePostgres, DSN: pg.App, OwnerDSN: pg.Owner, AdminDSN: pg.Admin, MaxConns: 4}
			if variant == "schema_apply" {
				if err := ApplyMigrations(ctx, cfg, nil); err != nil {
					t.Fatalf("real schema application: %v", err)
				}
			}
			st, err := Open(ctx, cfg, nil)
			if err != nil {
				t.Fatalf("real fresh Open: %v", err)
			}
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
			if variant == "fresh_open" || variant == "schema_apply" {
				return
			}
			owner := openCustodyPGPool(t, pg.Owner)
			var version int
			if err := owner.QueryRowContext(ctx, "SHOW server_version_num").Scan(&version); err != nil {
				t.Fatal(err)
			}
			renamed := renamePostgresNotNullConstraints(t, owner, dialect.LoginCapabilityObservationTable)
			want := 0
			if version >= 180000 {
				want = 5
			}
			if renamed != want {
				t.Fatalf("cataloged NOT NULL constraints=%d, want %d", renamed, want)
			}
			st, err = Open(ctx, cfg, nil)
			if err != nil {
				t.Fatalf("generated NOT NULL names changed no semantics: %v", err)
			}
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
			switch variant {
			case "renamed_not_null":
				t.Logf("NOT_NULL_IDENTITY|major=%d|renamed=%d", version/10000, renamed)
				return
			case "missing_not_null":
				_, err = owner.ExecContext(ctx, "ALTER TABLE public.login_capability_observation ALTER COLUMN last_artifact_version DROP NOT NULL")
			case "renamed_primary_key":
				_, err = owner.ExecContext(ctx, "ALTER TABLE public.login_capability_observation RENAME CONSTRAINT login_capability_observation_pkey TO unexpected_primary_key")
			}
			if err != nil {
				t.Fatal(err)
			}
			st, err = Open(ctx, cfg, nil)
			if st != nil {
				_ = st.Close()
			}
			if err == nil || !strings.Contains(err.Error(), "core v13 login capability relation drift") {
				t.Fatalf("real %s drift admitted or wrong refusal: %v", variant, err)
			}
			t.Logf("NOT_NULL_DRIFT_REFUSED|major=%d|mutation=%s|error=%s", version/10000, variant, err)
		})
	}
}

func TestEvidenceV11PostgresNotNullIdentityAndDrift(t *testing.T) {
	p := newV11Postgres(t, false)
	p.build(t, v11Check(evidenceOpStateWords7))
	verify := func() error {
		inv, err := readPostgresEvidenceInventory(context.Background(), p.owner, p.dia, p.cal)
		if err != nil {
			return err
		}
		_, err = inv.classify(p.cal)
		return err
	}
	if err := verify(); err != nil {
		t.Fatalf("unchanged v11 inventory: %v", err)
	}
	renamed := renamePostgresNotNullConstraints(t, p.owner, evidenceOpDescriptor.Table)
	if err := verify(); err != nil {
		t.Fatalf("generated v11 NOT NULL names changed no semantics: %v", err)
	}
	t.Logf("V11_NOT_NULL_IDENTITY|renamed=%d", renamed)
	if _, err := p.owner.Exec("ALTER TABLE public.evidence_operations ALTER COLUMN created_at DROP NOT NULL"); err != nil {
		t.Fatal(err)
	}
	if err := verify(); err == nil {
		t.Fatal("real v11 NOT NULL drift was admitted")
	} else {
		t.Logf("V11_NOT_NULL_DRIFT_REFUSED|error=%s", err)
	}
}
