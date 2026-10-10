// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

package sqlstore

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// TestReconcileCoreDataRebindsPersistedSQLiteScope is the A0.4 regression for
// S5-A. A prior process may leave SQLite's persisted scope pin on a
// business tenant. Boot-time federation normalization must bind the system
// tenant in its own transaction before touching FORCE-guarded auth rows.
func TestReconcileCoreDataRebindsPersistedSQLiteScope(t *testing.T) {
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "reconcile-scope.db")
	cfg := store.Config{Engine: store.EngineSQLite, DSN: dsn}

	st, err := Open(ctx, cfg, nil)
	if err != nil {
		t.Fatalf("initial open: %v", err)
	}
	target := model.NewTenantID()
	var configID model.ID
	if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
		rec, createErr := as.FederationConfigs().Create(ctx, model.FederationConfig{
			TargetTenantID: target,
			Alias:          model.DefaultFederationAlias,
			Protocol:       "oidc",
			Status:         model.StatusActive,
		})
		configID = rec.ID
		return createErr
	}); err != nil {
		t.Fatalf("create federation config: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close initial store: %v", err)
	}

	raw, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open raw sqlite: %v", err)
	}
	defer raw.Close() //nolint:errcheck // closed before reopening below
	tx, err := raw.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin legacy fixture: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit
	if _, err := tx.ExecContext(ctx,
		"UPDATE federation_configs SET alias = NULL WHERE id = ?", configID.String()); err != nil {
		t.Fatalf("restore legacy NULL alias: %v", err)
	}
	untrackFederationDataMigration(t, ctx, tx)
	businessPin := model.NewTenantID()
	if _, err := tx.ExecContext(ctx,
		"UPDATE "+dialect.ScopeTenantTable+" SET tenant_id = ?", businessPin.String()); err != nil {
		t.Fatalf("leave business scope pin: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit legacy fixture: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw sqlite: %v", err)
	}

	reopened, err := Open(ctx, cfg, nil)
	if err != nil {
		t.Fatalf("reopen with business scope pin: %v", err)
	}
	defer reopened.Close() //nolint:errcheck
	dia, _ := dialect.New(store.EngineSQLite)
	if alias := rawFederationAlias(t, ctx, reopened.(*sqlStore).db, dia, model.SystemTenantID, configID.String()); alias != model.DefaultFederationAlias {
		t.Errorf("stored alias = %q, want default", alias)
	}
	if err := reopened.AuthView(ctx, func(as store.AuthScope) error {
		got, getErr := as.FederationConfigs().Get(ctx, configID)
		if getErr != nil {
			return getErr
		}
		if got.Alias != model.DefaultFederationAlias {
			t.Fatalf("normalized alias = %q, want %q", got.Alias, model.DefaultFederationAlias)
		}
		return nil
	}); err != nil {
		t.Fatalf("read normalized federation config: %v", err)
	}
}
