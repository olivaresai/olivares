// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

package sqlstore

import (
	"context"
	"database/sql"
	"testing"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// TestPostgresReconcileCoreDataReopensUnderForceRLS covers A0.1-A0.2 on the
// real secure PostgreSQL role.
//
// THE GATING WAS STALE AND IT FAILED LOUDLY (measured 2026-08-06). Both tests here read
// OLIVARES_TEST_POSTGRES_DSN and opened whatever DATABASE it named, on the reasoning —
// written in this comment — that "local runs skip without the CI DSN, and CI executes it".
// That stopped being true when scripts/pg-test-env.sh began synthesizing that variable
// locally: the skip no longer fired, and the pair failed with
//
//	FATAL: database "olivares" does not exist (SQLSTATE 3D000)
//
// on a host whose only non-template database is olv_hub_iso. A gating assumption that
// silently became false is the exact class this package's isolation exists to remove —
// see core/internal/pgtest's doc: sharing ONE database is unsound because parts of the
// schema are global rather than tenant-scoped, and these two wrote federation_configs
// rows into it and deleted them again by hand.
//
// So they take their own database like every other Postgres leg in the package. It costs
// nothing, it drops the hand-rolled cleanup's reason to exist, and it removes a
// cross-suite write nobody was tracking.
func TestPostgresReconcileCoreDataReopensUnderForceRLS(t *testing.T) {
	dsn := isolatedPG(t).App
	ctx := context.Background()
	st, err := Open(ctx, store.Config{Engine: store.EnginePostgres, DSN: dsn}, nil)
	if err != nil {
		t.Fatalf("initial open: %v", err)
	}
	target := model.NewTenantID()
	var config model.FederationConfig
	if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
		var createErr error
		config, createErr = as.FederationConfigs().Create(ctx, model.FederationConfig{
			TargetTenantID: target,
			Alias:          model.DefaultFederationAlias,
			Protocol:       "oidc",
			Status:         model.StatusActive,
		})
		return createErr
	}); err != nil {
		t.Fatalf("create legacy fixture: %v", err)
	}
	db := st.(*sqlStore).db
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx,
		"SELECT set_config('app.tenant_id', $1, true)", model.SystemTenantID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx,
		"UPDATE federation_configs SET alias = NULL WHERE id = $1", config.ID.String()); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	unbound, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	unbound.SetMaxOpenConns(1)
	if _, err := unbound.ExecContext(ctx,
		"UPDATE federation_configs SET alias = alias WHERE id = $1", config.ID.String()); err == nil {
		t.Fatal("raw unbound owner/app UPDATE unexpectedly bypassed FORCE RLS")
	}
	if err := unbound.Close(); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close initial store: %v", err)
	}

	reopened, err := Open(ctx, store.Config{Engine: store.EnginePostgres, DSN: dsn}, nil)
	if err != nil {
		t.Fatalf("reopen with legacy federation row: %v", err)
	}
	defer reopened.Close() //nolint:errcheck
	if err := reopened.AuthView(ctx, func(as store.AuthScope) error {
		got, getErr := as.FederationConfigs().Get(ctx, config.ID)
		if getErr != nil {
			return getErr
		}
		if got.Alias != model.DefaultFederationAlias {
			t.Fatalf("alias = %q, want default", got.Alias)
		}
		return nil
	}); err != nil {
		t.Fatalf("read normalized config: %v", err)
	}
	if err := reopened.AuthMutate(ctx, func(as store.AuthScope) error {
		return as.FederationConfigs().Delete(ctx, config.ID)
	}); err != nil {
		t.Logf("cleanup federation config %s: %v", config.ID, err)
	}
}

// TestPostgresReconcileCoreDataBindsOnlySystemTenant is A0.3. Two legitimate
// auth rows with different target_tenant_id values converge, while a
// deliberately malformed row owned by a business tenant remains invisible to
// the SystemTenant-bound reconcile.
//
// Isolated for the reason spelled out on the test above, and with one extra edge of its
// own: it asserts on what reconcileCoreData did to the rows it can SEE, so a leftover
// federation_configs row from any other suite sharing the database is a false verdict
// waiting to happen.
func TestPostgresReconcileCoreDataBindsOnlySystemTenant(t *testing.T) {
	dsn := isolatedPG(t).App
	ctx := context.Background()
	st, err := Open(ctx, store.Config{Engine: store.EnginePostgres, DSN: dsn}, nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close() //nolint:errcheck
	db := st.(*sqlStore).db
	dia, _ := dialect.New(store.EnginePostgres)
	systemIDs := []string{"a0-system-" + uniqueSuffix(), "a0-target-" + uniqueSuffix()}
	targets := []model.TenantID{model.SystemTenantID, model.NewTenantID()}

	insertRawFederation := func(owner model.TenantID, id string, target model.TenantID) {
		t.Helper()
		tx, beginErr := db.BeginTx(ctx, nil)
		if beginErr != nil {
			t.Fatal(beginErr)
		}
		defer tx.Rollback() //nolint:errcheck
		if bindErr := dia.BindTenant(ctx, tx, owner); bindErr != nil {
			t.Fatal(bindErr)
		}
		_, insertErr := tx.ExecContext(ctx, `INSERT INTO federation_configs
			(id, tenant_id, created_at, updated_at, version, target_tenant_id, alias, protocol, status)
			VALUES ($1,$2,$3,$3,1,$4,NULL,'oidc','active')`,
			id, owner.String(), "2026-07-24T12:00:00.000000000Z", target.String())
		if insertErr != nil {
			t.Fatal(insertErr)
		}
		if commitErr := tx.Commit(); commitErr != nil {
			t.Fatal(commitErr)
		}
	}
	for i := range systemIDs {
		insertRawFederation(model.SystemTenantID, systemIDs[i], targets[i])
	}
	businessOwner := model.NewTenantID()
	invalidID := "a0-invalid-" + uniqueSuffix()
	insertRawFederation(businessOwner, invalidID, businessOwner)

	if err := reconcileCoreData(ctx, db, dia); err != nil {
		t.Fatalf("bound reconcile: %v", err)
	}
	for _, id := range systemIDs {
		if alias := rawFederationAlias(t, ctx, db, dia, model.SystemTenantID, id); alias != "default" {
			t.Errorf("system-owned %s alias = %q, want default", id, alias)
		}
	}
	if alias := rawFederationAlias(t, ctx, db, dia, businessOwner, invalidID); alias != "" {
		t.Errorf("business-owned invalid row alias = %q, want NULL/empty", alias)
	}

	for owner, ids := range map[model.TenantID][]string{
		model.SystemTenantID: systemIDs,
		businessOwner:        {invalidID},
	} {
		tx, beginErr := db.BeginTx(ctx, nil)
		if beginErr != nil {
			t.Fatal(beginErr)
		}
		if bindErr := dia.BindTenant(ctx, tx, owner); bindErr != nil {
			t.Fatal(bindErr)
		}
		for _, id := range ids {
			if _, deleteErr := tx.ExecContext(ctx,
				"DELETE FROM federation_configs WHERE id = $1", id); deleteErr != nil {
				t.Logf("cleanup %s/%s: %v", owner, id, deleteErr)
			}
		}
		_ = tx.Commit()
	}
}

func rawFederationAlias(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	dia dialect.Dialect,
	owner model.TenantID,
	id string,
) string {
	t.Helper()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback() //nolint:errcheck
	if err := dia.BindTenant(ctx, tx, owner); err != nil {
		t.Fatal(err)
	}
	var alias sql.NullString
	if err := tx.QueryRowContext(ctx,
		"SELECT alias FROM federation_configs WHERE id = $1", id).Scan(&alias); err != nil {
		t.Fatal(err)
	}
	return alias.String
}
