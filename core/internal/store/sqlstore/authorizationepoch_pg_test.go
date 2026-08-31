// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestAuthorizationEpochPostgresSplitOwnerBackfillOCCAndLock(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pg := isolatedPGSplit(t)
	cfg := store.Config{
		Engine: store.EnginePostgres, DSN: pg.App, OwnerDSN: pg.Owner,
		AdminDSN: pg.Admin, MaxConns: 4,
	}
	st, err := Open(ctx, cfg, nil)
	if err != nil {
		t.Fatalf("open split-owner authorization store: %v", err)
	}
	tenant := provisionTenant(t, st, "authorization-epoch-pg")
	initial := authorizationEpochTestRead(t, st, tenant)
	if initial.Kind != model.AuthorizationEpochKind || initial.ID != model.ID(tenant) ||
		initial.Version != 1 {
		t.Fatalf("seeded PostgreSQL authorization epoch = %+v", initial)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close seed store: %v", err)
	}

	app, err := sql.Open("pgx", pg.App)
	if err != nil {
		t.Fatalf("open raw app pool: %v", err)
	}
	defer app.Close() //nolint:errcheck
	authorizationEpochTestDeletePostgresRow(t, app, tenant)

	noAdmin := cfg
	noAdmin.AdminDSN = ""
	incomplete, err := Open(ctx, noAdmin, nil)
	if err != nil {
		t.Fatalf("open no-admin split store: %v", err)
	}
	err = incomplete.View(ctx, tenant, func(sc store.Scope) error {
		_, err := sc.(store.AuthorizationEpochReader).ReadAuthorizationEpoch(ctx)
		return err
	})
	if !errors.Is(err, store.ErrAuthorizationEpochUnavailable) {
		t.Fatalf("no-admin missing epoch read = %v, want unavailable", err)
	}
	if err := incomplete.Close(); err != nil {
		t.Fatalf("close no-admin store: %v", err)
	}
	if _, found := authorizationEpochTestReadPostgresRow(t, app, tenant); found {
		t.Fatal("no-admin boot fabricated an authorization epoch")
	}

	healed, err := Open(ctx, cfg, nil)
	if err != nil {
		t.Fatalf("open authoritative healing store: %v", err)
	}
	t.Cleanup(func() { _ = healed.Close() })
	healedFact := authorizationEpochTestRead(t, healed, tenant)
	if healedFact != initial {
		t.Fatalf("healed fact = %+v, want fresh generation %+v", healedFact, initial)
	}

	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results <- healed.Mutate(ctx, tenant, func(sc store.Scope) error {
				_, err := sc.(store.AuthorizationEpochBumper).
					BumpAuthorizationEpoch(ctx, healedFact)
				return err
			})
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	var succeeded, unavailable int
	for err := range results {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, store.ErrAuthorizationEpochUnavailable):
			unavailable++
		default:
			t.Fatalf("concurrent bump error = %v", err)
		}
	}
	if succeeded != 1 || unavailable != 1 {
		t.Fatalf("concurrent bumps: success=%d unavailable=%d", succeeded, unavailable)
	}
	current := authorizationEpochTestRead(t, healed, tenant)
	if current.Version != 2 {
		t.Fatalf("post-OCC generation = %d, want 2", current.Version)
	}
	err = healed.Mutate(ctx, tenant, func(sc store.Scope) error {
		locker := sc.(store.AuthoritySnapshotLocker)
		if err := locker.LockAuthoritySnapshot(ctx, []store.AuthorizationFactRef{current}); err != nil {
			return err
		}
		return locker.LockAuthoritySnapshot(ctx, []store.AuthorizationFactRef{healedFact})
	})
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("stale PostgreSQL authority lock = %v, want ErrConflict", err)
	}
}

func TestEpochReconcilePostgresForeignAdminRollsBackDirectoryBeforeAuthorization(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	primary := isolatedPGSplit(t)
	foreign := isolatedPGSplit(t)
	primaryCfg := store.Config{
		Engine: store.EnginePostgres, DSN: primary.App, OwnerDSN: primary.Owner,
		AdminDSN: primary.Admin, MaxConns: 4,
	}
	primaryStore, err := Open(ctx, primaryCfg, nil)
	if err != nil {
		t.Fatalf("open primary epoch store: %v", err)
	}
	if err := primaryStore.Close(); err != nil {
		t.Fatalf("close primary epoch store: %v", err)
	}
	foreignStore, err := Open(ctx, store.Config{
		Engine: store.EnginePostgres, DSN: foreign.App, OwnerDSN: foreign.Owner,
		AdminDSN: foreign.Admin, MaxConns: 4,
	}, nil)
	if err != nil {
		t.Fatalf("open foreign epoch store: %v", err)
	}
	foreignTenant := provisionTenant(t, foreignStore, "epoch-foreign-open")
	if err := foreignStore.Close(); err != nil {
		t.Fatalf("close foreign epoch store: %v", err)
	}

	mixed := primaryCfg
	mixed.AdminDSN = foreign.Admin
	bad, openErr := Open(ctx, mixed, nil)
	if bad != nil {
		_ = bad.Close()
	}
	if !errors.Is(openErr, store.ErrEnumerationNotAuthoritative) ||
		!strings.Contains(openErr.Error(), "does not address the owner database") {
		t.Fatalf("foreign AdminDSN Open error = %v, want typed identity refusal", openErr)
	}

	app, err := openPGPinnedToEngineSchema(primary.App, 2)
	if err != nil {
		t.Fatalf("open primary application witness: %v", err)
	}
	defer app.Close() //nolint:errcheck
	if _, found := directoryEpochTestReadPostgresRow(t, app, foreignTenant); found {
		t.Fatal("foreign AdminDSN left a directory epoch in the primary database")
	}
	if _, found := authorizationEpochTestReadPostgresRow(t, app, foreignTenant); found {
		t.Fatal("foreign AdminDSN left an authorization epoch in the primary database")
	}
}

func TestAuthorizationEpochReconcilePostgresRejectsForeignAdminDirectly(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	primary := isolatedPGSplit(t)
	foreign := isolatedPGSplit(t)
	primaryStore, err := Open(ctx, store.Config{
		Engine: store.EnginePostgres, DSN: primary.App, OwnerDSN: primary.Owner,
		AdminDSN: primary.Admin, MaxConns: 4,
	}, nil)
	if err != nil {
		t.Fatalf("open primary epoch store: %v", err)
	}
	if err := primaryStore.Close(); err != nil {
		t.Fatalf("close primary epoch store: %v", err)
	}
	foreignStore, err := Open(ctx, store.Config{
		Engine: store.EnginePostgres, DSN: foreign.App, OwnerDSN: foreign.Owner,
		AdminDSN: foreign.Admin, MaxConns: 4,
	}, nil)
	if err != nil {
		t.Fatalf("reopen foreign epoch store: %v", err)
	}
	foreignTenant := provisionTenant(t, foreignStore, "epoch-foreign-direct")
	if err := foreignStore.Close(); err != nil {
		t.Fatalf("close foreign epoch store: %v", err)
	}

	app, err := openPGPinnedToEngineSchema(primary.App, 2)
	if err != nil {
		t.Fatalf("open primary application pool: %v", err)
	}
	defer app.Close() //nolint:errcheck
	admin, err := openPGPinnedToEngineSchema(foreign.Admin, 1)
	if err != nil {
		t.Fatalf("open foreign administrative pool: %v", err)
	}
	defer admin.Close() //nolint:errcheck
	dia, _ := dialect.New(store.EnginePostgres)
	complete, reconcileErr := reconcileAuthorizationEpochs(
		ctx, app, admin, dia,
		guardRoleFact{Role: foreign.Result.AdminPosture.Role, Known: true},
	)
	if complete || !errors.Is(reconcileErr, store.ErrEnumerationNotAuthoritative) ||
		!strings.Contains(reconcileErr.Error(), "does not address the owner database") {
		t.Fatalf("direct foreign authorization reconcile complete=%t error=%v, want typed identity refusal",
			complete, reconcileErr)
	}
	if _, found := authorizationEpochTestReadPostgresRow(t, app, foreignTenant); found {
		t.Fatal("direct foreign authorization reconciliation left a primary epoch row")
	}
}

func TestAuthorizationEpochReconcilePostgresEnumeratesThroughPinnedTransaction(t *testing.T) {
	pg := isolatedPGSplit(t)
	// The old 10-second context covered BOTH boot/migrations and the reconcile this test names.
	// Under the loaded race runner it expired in Open's append-only inventory before the subject
	// ran (run 33254801927). Give setup its own bound so a setup failure remains a setup failure.
	setupCtx, setupCancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer setupCancel()
	cfg := store.Config{
		Engine: store.EnginePostgres, DSN: pg.App, OwnerDSN: pg.Owner,
		AdminDSN: pg.Admin, MaxConns: 4,
	}
	st, err := Open(setupCtx, cfg, nil)
	if err != nil {
		t.Fatalf("open epoch store: %v", err)
	}
	tenant := provisionTenant(t, st, "epoch-pinned-admin-tx")
	if err := st.Close(); err != nil {
		t.Fatalf("close epoch store: %v", err)
	}

	app, err := openPGPinnedToEngineSchema(pg.App, 2)
	if err != nil {
		t.Fatalf("open application pool: %v", err)
	}
	defer app.Close() //nolint:errcheck
	authorizationEpochTestDeletePostgresRow(t, app, tenant)
	admin, err := openPGPinnedToEngineSchema(pg.Admin, 1)
	if err != nil {
		t.Fatalf("open single-connection administrative pool: %v", err)
	}
	defer admin.Close() //nolint:errcheck
	dia, _ := dialect.New(store.EnginePostgres)
	reconcileCtx, reconcileCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer reconcileCancel()
	complete, err := reconcileAuthorizationEpochs(
		reconcileCtx, app, admin, dia,
		guardRoleFact{Role: pg.Result.AdminPosture.Role, Known: true},
	)
	if err != nil || !complete {
		t.Fatalf("pinned-transaction reconcile complete=%t error=%v", complete, err)
	}
	if fact, found := authorizationEpochTestReadPostgresRow(t, app, tenant); !found || fact.ID != model.ID(tenant) || fact.Version != 1 {
		t.Fatalf("pinned-transaction backfill = %+v found=%t", fact, found)
	}
}

func TestAuthorizationEpochReconcilePostgresRejectsLiveAdminPostureDrift(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pg := isolatedPGSplit(t)
	cfg := store.Config{
		Engine: store.EnginePostgres, DSN: pg.App, OwnerDSN: pg.Owner,
		AdminDSN: pg.Admin, MaxConns: 4,
	}
	st, err := Open(ctx, cfg, nil)
	if err != nil {
		t.Fatalf("open epoch store: %v", err)
	}
	tenant := provisionTenant(t, st, "epoch-live-admin-posture")
	provisionTenant(t, st, "epoch-live-admin-hidden")
	if err := st.Close(); err != nil {
		t.Fatalf("close epoch store: %v", err)
	}

	app, err := openPGPinnedToEngineSchema(pg.App, 2)
	if err != nil {
		t.Fatalf("open application pool: %v", err)
	}
	defer app.Close() //nolint:errcheck
	authorizationEpochTestDeletePostgresRow(t, app, tenant)
	super, err := sql.Open("pgx", pg.Superuser)
	if err != nil {
		t.Fatalf("open superuser posture authority: %v", err)
	}
	defer super.Close() //nolint:errcheck
	adminRole := pg.Result.AdminPosture.Role
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if _, cleanupErr := super.ExecContext(
			cleanupCtx, "ALTER ROLE "+quoteIdent(adminRole)+" BYPASSRLS",
		); cleanupErr != nil {
			t.Errorf("restore AdminDSN BYPASSRLS: %v", cleanupErr)
		}
		if _, cleanupErr := super.ExecContext(
			cleanupCtx,
			"ALTER ROLE "+quoteIdent(adminRole)+" IN DATABASE "+
				quoteIdent(pg.Database)+" RESET app.tenant_id",
		); cleanupErr != nil {
			t.Errorf("reset AdminDSN tenant preset: %v", cleanupErr)
		}
	}()
	if _, err := super.ExecContext(
		ctx,
		"ALTER ROLE "+quoteIdent(adminRole)+" IN DATABASE "+quoteIdent(pg.Database)+
			" SET app.tenant_id TO "+systemAdminTestQuoteLiteral(tenant.String()),
	); err != nil {
		t.Fatalf("preset AdminDSN tenant identity: %v", err)
	}
	if _, err := super.ExecContext(
		ctx, "ALTER ROLE "+quoteIdent(adminRole)+" NOBYPASSRLS",
	); err != nil {
		t.Fatalf("revoke AdminDSN BYPASSRLS: %v", err)
	}
	hostileAdmin, err := openPGPinnedToEngineSchema(pg.Admin, 1)
	if err != nil {
		t.Fatalf("open drifted administrative pool: %v", err)
	}
	defer hostileAdmin.Close() //nolint:errcheck
	dia, _ := dialect.New(store.EnginePostgres)
	complete, reconcileErr := reconcileAuthorizationEpochs(
		ctx, app, hostileAdmin, dia,
		guardRoleFact{Role: adminRole, Known: true},
	)
	if complete || !errors.Is(reconcileErr, store.ErrEnumerationNotAuthoritative) ||
		!strings.Contains(reconcileErr.Error(), "live AdminDSN must be exact boot-pinned") {
		t.Fatalf("drifted AdminDSN reconcile complete=%t error=%v, want live posture refusal",
			complete, reconcileErr)
	}
	if _, found := authorizationEpochTestReadPostgresRow(t, app, tenant); found {
		t.Fatal("drifted AdminDSN left a partial-visibility authorization epoch")
	}
}

func authorizationEpochTestReadPostgresRow(
	t *testing.T,
	db *sql.DB,
	tenant model.TenantID,
) (store.AuthorizationFactRef, bool) {
	t.Helper()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin PostgreSQL authorization epoch read: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck
	dia, _ := dialect.New(store.EnginePostgres)
	if err := bindDirectoryTenant(context.Background(), tx, dia, tenant); err != nil {
		t.Fatalf("bind PostgreSQL authorization epoch read: %v", err)
	}
	var id string
	var version int64
	err = tx.QueryRowContext(context.Background(), `SELECT id, version
FROM public.core_authorization_epoch WHERE tenant_id = $1`, tenant.String()).Scan(&id, &version)
	if errors.Is(err, sql.ErrNoRows) {
		return store.AuthorizationFactRef{}, false
	}
	if err != nil {
		t.Fatalf("read PostgreSQL authorization epoch: %v", err)
	}
	return store.AuthorizationFactRef{
		Kind: model.AuthorizationEpochKind, ID: model.ID(id), Version: version,
	}, true
}

func authorizationEpochTestDeletePostgresRow(
	t *testing.T,
	db *sql.DB,
	tenant model.TenantID,
) {
	t.Helper()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin PostgreSQL authorization epoch delete: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck
	dia, _ := dialect.New(store.EnginePostgres)
	if err := bindDirectoryTenant(context.Background(), tx, dia, tenant); err != nil {
		t.Fatalf("bind PostgreSQL authorization epoch delete: %v", err)
	}
	result, err := tx.ExecContext(context.Background(),
		"DELETE FROM public.core_authorization_epoch WHERE tenant_id = $1", tenant.String())
	if err != nil {
		t.Fatalf("delete PostgreSQL authorization epoch: %v", err)
	}
	if rows, _ := result.RowsAffected(); rows != 1 {
		t.Fatalf("delete PostgreSQL authorization epoch affected %d rows, want one", rows)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit PostgreSQL authorization epoch delete: %v", err)
	}
}
