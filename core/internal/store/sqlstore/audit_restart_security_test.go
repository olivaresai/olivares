// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/store"
)

func TestAuditBlindGuardRepairsOnRestart(t *testing.T) {
	for _, malformedRow := range []bool{false, true} {
		name := "repair"
		if malformedRow {
			name = "refuse_invalid_existing_blind"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			cfg := store.Config{Engine: store.EnginePostgres, DSN: isolatedPG(t).App}
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
			raw, err := sql.Open("pgx", cfg.DSN)
			if err != nil {
				t.Fatal(err)
			}
			defer raw.Close()
			// Upgraded ledgers have the named repair constraint, but not the
			// inline CHECK emitted by the current fresh-schema CREATE TABLE.
			if _, err := raw.Exec("ALTER TABLE audit_events DROP CONSTRAINT audit_events_meta_blind_check, DROP CONSTRAINT audit_events_meta_blind_len"); err != nil {
				t.Fatal(err)
			}
			dia, _ := dialect.New(store.EnginePostgres)
			insert := func(id string, seq int, blind []byte) error {
				tx, err := raw.BeginTx(ctx, nil)
				if err != nil {
					return err
				}
				defer tx.Rollback()
				if err := dia.BindTenant(ctx, tx, "audit-restart"); err != nil {
					return err
				}
				_, err = tx.ExecContext(ctx, `INSERT INTO audit_events
					(id, tenant_id, seq, occurred_at, actor, actor_kind, action, target_kind, target_id, meta, meta_blind, prev_hash, hash)
					VALUES ($1, 'audit-restart', $2, '2026-10-05', 'operator', 'user', 'test', '', '', '{}', $3, '\x00', '\x00')`, id, seq, blind)
				if err != nil {
					return err
				}
				return tx.Commit()
			}
			for i, blind := range [][]byte{nil, make([]byte, 32)} {
				if err := insert([]string{"legacy", "blinded"}[i], i+1, blind); err != nil {
					t.Fatal(err)
				}
			}
			if malformedRow {
				if err := insert("invalid", 3, []byte{1}); err != nil {
					t.Fatal(err)
				}
			}
			snapshot := func() string {
				t.Helper()
				tx, err := raw.BeginTx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
				if err := dia.BindTenant(ctx, tx, "audit-restart"); err != nil {
					t.Fatal(err)
				}
				var value string
				if err := tx.QueryRow(`SELECT jsonb_build_array(
					(SELECT jsonb_agg(to_jsonb(a) ORDER BY seq) FROM audit_events a),
					(SELECT jsonb_agg(to_jsonb(m) ORDER BY version) FROM schema_migrations_core m),
					(SELECT jsonb_agg(to_jsonb(b)) FROM audit_meta_blinding b))::text`).Scan(&value); err != nil {
					t.Fatal(err)
				}
				return value
			}
			before := snapshot()
			st, err = Open(ctx, cfg, nil)
			if malformedRow {
				var pgerr *pgconn.PgError
				if !errors.As(err, &pgerr) || pgerr.Code != "23514" || pgerr.ConstraintName != "audit_events_meta_blind_len" {
					t.Fatalf("restart must refuse the invalid stored blind: %v", err)
				}
			} else {
				if err != nil {
					t.Fatalf("restart must repair the missing audit guard: %v", err)
				}
				var applied, again string
				const receipt = "SELECT applied_at FROM schema_migrations_audit_blind_guards WHERE version = 1"
				if err := raw.QueryRow(receipt).Scan(&applied); err != nil {
					t.Fatal(err)
				}
				if err := st.Close(); err != nil {
					t.Fatal(err)
				}
				if _, err := raw.Exec("ALTER TABLE audit_events DROP CONSTRAINT audit_events_meta_blind_len"); err != nil {
					t.Fatal(err)
				}
				st, err = Open(ctx, cfg, nil)
				if err != nil {
					t.Fatalf("second restart must repair even with a recorded version: %v", err)
				}
				if err := raw.QueryRow(receipt).Scan(&again); err != nil || applied != again {
					t.Fatalf("restart changed the repair receipt: %v", err)
				}
				for _, blind := range [][]byte{{}, {1}, make([]byte, 31), make([]byte, 33)} {
					err := insert("malformed", 3, blind)
					var pgerr *pgconn.PgError
					if !errors.As(err, &pgerr) || pgerr.Code != "23514" || pgerr.ConstraintName != "audit_events_meta_blind_len" {
						t.Fatalf("restart admitted a %d-byte blind or refused for the wrong reason: %v", len(blind), err)
					}
				}
			}
			if after := snapshot(); after != before {
				t.Fatal("restart changed audit rows, migration receipts, or blinding state")
			}
		})
	}
}
