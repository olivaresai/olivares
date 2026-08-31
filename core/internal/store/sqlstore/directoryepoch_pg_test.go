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

func TestDirectoryEpochPostgresSplitOwnerNoAdminStatusAndHeal(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pg := isolatedPGSplit(t)
	fixed := model.NewTimestamp(time.Date(2001, time.February, 3, 4, 5, 6, 0, time.UTC))
	full := store.Config{
		Engine: store.EnginePostgres, DSN: pg.App, OwnerDSN: pg.Owner,
		AdminDSN: pg.Admin, MaxConns: 1,
		Clock: transactionClockFixedAppClock{now: fixed},
	}
	st, err := Open(ctx, full, nil)
	if err != nil {
		t.Fatalf("open split-owner directory store: %v", err)
	}
	directoryEpochTestWantStatus(t, st, store.DirectoryStatus{
		EpochCoverageComplete: true,
		ControlMode:           store.DirectoryControlStaged,
		WriterPosture:         store.DirectoryWriterSplitOwner,
		ExpectedGeneration:    1,
	})
	beforeCreate := time.Now().UTC().Add(-time.Second)
	tenant := provisionTenant(t, st, "directory-pg-split")
	afterCreate := time.Now().UTC().Add(time.Second)
	if err := st.Close(); err != nil {
		t.Fatalf("close split-owner seed boot: %v", err)
	}

	app, err := sql.Open("pgx", pg.App)
	if err != nil {
		t.Fatalf("open raw app pool: %v", err)
	}
	defer app.Close() //nolint:errcheck
	seeded, found := directoryEpochTestReadPostgresRow(t, app, tenant)
	if !found || seeded.id != tenant.String() || seeded.rowTenant != tenant.String() ||
		seeded.version != 1 {
		t.Fatalf("seeded PostgreSQL epoch = %+v found=%t", seeded, found)
	}
	if seeded.createdAt == fixed.String() || seeded.updatedAt != seeded.createdAt {
		t.Fatalf("seeded PostgreSQL epoch timestamps = %s/%s, skew=%s",
			seeded.createdAt, seeded.updatedAt, fixed.String())
	}
	seededTime, err := model.ParseTimestamp(seeded.createdAt)
	if err != nil || seededTime.Time().Before(beforeCreate) || seededTime.Time().After(afterCreate) {
		t.Fatalf("seeded PostgreSQL DB time=%s err=%v outside [%s,%s]",
			seeded.createdAt, err, beforeCreate, afterCreate)
	}
	directoryEpochTestDeletePostgresRow(t, app, tenant)

	owner, err := sql.Open("pgx", pg.Owner)
	if err != nil {
		t.Fatalf("open raw owner pool: %v", err)
	}
	defer owner.Close() //nolint:errcheck
	if _, err := owner.ExecContext(ctx, `UPDATE public.directory_writer_control
SET mode = 'enforced', expected_generation = 7`); err != nil {
		t.Fatalf("activate test writer control: %v", err)
	}

	noAdmin := full
	noAdmin.AdminDSN = ""
	incomplete, err := Open(ctx, noAdmin, nil)
	if err != nil {
		t.Fatalf("no-admin split-owner boot: %v", err)
	}
	directoryEpochTestWantStatus(t, incomplete, store.DirectoryStatus{
		EpochCoverageComplete: false,
		ControlMode:           store.DirectoryControlEnforced,
		WriterPosture:         store.DirectoryWriterSplitOwner,
		ExpectedGeneration:    7,
	})
	if _, found := directoryEpochTestReadPostgresRow(t, app, tenant); found {
		t.Fatal("no-admin boot mutated missing epoch despite incomplete coverage")
	}
	if err := incomplete.Close(); err != nil {
		t.Fatalf("close incomplete boot: %v", err)
	}

	beforeHeal := time.Now().UTC().Add(-time.Second)
	healed, err := Open(ctx, full, nil)
	if err != nil {
		t.Fatalf("authoritative healing boot: %v", err)
	}
	afterHeal := time.Now().UTC().Add(time.Second)
	directoryEpochTestWantStatus(t, healed, store.DirectoryStatus{
		EpochCoverageComplete: true,
		ControlMode:           store.DirectoryControlEnforced,
		WriterPosture:         store.DirectoryWriterSplitOwner,
		ExpectedGeneration:    7,
	})
	if err := healed.Close(); err != nil {
		t.Fatalf("close healing boot: %v", err)
	}
	healedRow, found := directoryEpochTestReadPostgresRow(t, app, tenant)
	if !found || healedRow.version != 1 || healedRow.createdAt == fixed.String() {
		t.Fatalf("healed PostgreSQL epoch = %+v found=%t", healedRow, found)
	}
	healedTime, err := model.ParseTimestamp(healedRow.createdAt)
	if err != nil || healedTime.Time().Before(beforeHeal) || healedTime.Time().After(afterHeal) {
		t.Fatalf("healed PostgreSQL DB time=%s err=%v outside [%s,%s]",
			healedRow.createdAt, err, beforeHeal, afterHeal)
	}

	idempotent, err := Open(ctx, full, nil)
	if err != nil {
		t.Fatalf("second authoritative PostgreSQL boot: %v", err)
	}
	if err := idempotent.Close(); err != nil {
		t.Fatalf("close idempotent PostgreSQL boot: %v", err)
	}
	if got, found := directoryEpochTestReadPostgresRow(t, app, tenant); !found || got != healedRow {
		t.Fatalf("second PostgreSQL boot rewrote epoch: got=%+v found=%t want=%+v",
			got, found, healedRow)
	}
}

func TestDirectoryEpochPostgresMaxOneRejectsInheritedGeneration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pg := isolatedPG(t)
	st, err := Open(ctx, store.Config{
		Engine: store.EnginePostgres, DSN: pg.App, MaxConns: 1,
	}, nil)
	if err != nil {
		t.Fatalf("open single-role MaxConns=1 store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	directoryEpochTestWantStatus(t, st, store.DirectoryStatus{
		EpochCoverageComplete: false,
		ControlMode:           store.DirectoryControlStaged,
		WriterPosture:         store.DirectoryWriterSingleRoleCapability,
		ExpectedGeneration:    1,
	})
	tenant := provisionTenant(t, st, "directory-pg-guc")
	ss := st.(*sqlStore)
	if _, err := ss.db.ExecContext(ctx, `UPDATE public.directory_writer_control
SET mode = 'enforced', expected_generation = 1`); err != nil {
		t.Fatalf("enforce generation for contamination probe: %v", err)
	}
	contaminate := `SELECT
pg_catalog.set_config('app.tenant_id', $1, false),
pg_catalog.set_config('app.directory_writer_generation', '1', false)`
	if _, err := ss.db.ExecContext(ctx, contaminate, tenant.String()); err != nil {
		t.Fatalf("contaminate the only pooled session: %v", err)
	}

	called := false
	err = st.System(ctx, func(store.SystemScope) error {
		called = true
		return nil
	})
	if called || !errors.Is(err, errDirectoryWriterControlInvalid) {
		t.Fatalf("System inherited generation: called=%t err=%v", called, err)
	}
	var inherited string
	if err := ss.db.QueryRowContext(ctx,
		"SELECT COALESCE(pg_catalog.current_setting($1, true), '')",
		dialect.DirectoryWriterGenerationGUC).Scan(&inherited); err != nil {
		t.Fatalf("read inherited generation after refusal: %v", err)
	}
	if inherited != "1" {
		t.Fatalf("refusal unexpectedly hid session contaminant %q", inherited)
	}
	result, err := ss.db.ExecContext(ctx, `UPDATE public.orgs
SET status = 'suspended' WHERE id = $1 AND tenant_id = $2`,
		tenant.String(), tenant.String())
	if err != nil {
		t.Fatalf("single-role raw write did not demonstrate inherited capability: %v", err)
	}
	if rows, _ := result.RowsAffected(); rows != 1 {
		t.Fatalf("single-role raw write affected %d rows, want 1", rows)
	}
	if _, err := ss.db.ExecContext(ctx, `UPDATE public.orgs
SET status = 'active' WHERE id = $1 AND tenant_id = $2`,
		tenant.String(), tenant.String()); err != nil {
		t.Fatalf("restore raw org status: %v", err)
	}
	if _, err := ss.db.ExecContext(ctx, `SELECT
pg_catalog.set_config('app.tenant_id', '', false),
pg_catalog.set_config('app.directory_writer_generation', '', false)`); err != nil {
		t.Fatalf("clear session contamination: %v", err)
	}

	tx, err := ss.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin SYSTEM presentation probe: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck
	state, err := acquireDirectoryWriter(ctx, tx, ss.dia)
	if err != nil {
		t.Fatalf("acquire clean writer: %v", err)
	}
	if err := bindDirectoryTenant(ctx, tx, ss.dia, tenant); err != nil {
		t.Fatalf("bind real tenant: %v", err)
	}
	if err := armDirectoryWriter(ctx, tx, ss.dia, state); err != nil {
		t.Fatalf("arm generation: %v", err)
	}
	if err := restoreSystemDirectoryBaseline(ctx, tx, ss.dia); err != nil {
		t.Fatalf("restore SYSTEM presentation: %v", err)
	}
	var boundTenant, generation string
	if err := tx.QueryRowContext(ctx, `SELECT
COALESCE(pg_catalog.current_setting('app.tenant_id', true), ''),
COALESCE(pg_catalog.current_setting('app.directory_writer_generation', true), '')`).
		Scan(&boundTenant, &generation); err != nil {
		t.Fatalf("read restored presentation: %v", err)
	}
	if boundTenant != model.SystemTenantID.String() || generation != "" {
		t.Fatalf("restored presentation tenant=%q generation=%q", boundTenant, generation)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback presentation probe: %v", err)
	}
	if _, err := ss.db.ExecContext(ctx,
		"UPDATE public.directory_writer_control SET mode = 'staged'"); err != nil {
		t.Fatalf("restore staged control: %v", err)
	}
}

func TestDirectoryEpochPostgresLockOrderInterleavings(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pg := isolatedPG(t)
	st, err := Open(ctx, store.Config{
		Engine: store.EnginePostgres, DSN: pg.App, MaxConns: 4,
	}, nil)
	if err != nil {
		t.Fatalf("open lock-order store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	// The reader must lock epoch(5) before taking Identity's SHARE predicate
	// lock. Hold the epoch row as DropTenant would, start the reader, and prove
	// RowExclusive(identity) remains immediately available rather than cycling.
	tenant := provisionTenant(t, st, "directory-lock-epoch")
	var identity model.Identity
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		var err error
		identity, err = sc.Identities().Create(ctx, model.Identity{
			Name: "epoch lock identity", Kind: "service", ExternalID: "lock:epoch",
		})
		return err
	}); err != nil {
		t.Fatalf("seed epoch lock identity: %v", err)
	}
	epoch := directoryWriterTestEpoch(t, st, tenant)
	raw, err := sql.Open("pgx", pg.App)
	if err != nil {
		t.Fatalf("open raw lock-order pool: %v", err)
	}
	defer raw.Close() //nolint:errcheck
	holder, err := raw.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin epoch holder: %v", err)
	}
	pgDia, _ := dialect.New(store.EnginePostgres)
	if err := bindDirectoryTenant(ctx, holder, pgDia, tenant); err != nil {
		t.Fatalf("bind epoch holder: %v", err)
	}
	if _, err := holder.ExecContext(ctx,
		"DELETE FROM public.core_directory_epoch WHERE tenant_id = $1", tenant.String()); err != nil {
		t.Fatalf("hold epoch delete: %v", err)
	}
	readerStarted := make(chan struct{})
	readerDone := make(chan error, 1)
	go func() {
		readerDone <- st.Mutate(ctx, tenant, func(sc store.Scope) error {
			close(readerStarted)
			return sc.(store.AuthoritySnapshotLocker).LockAuthoritySnapshot(ctx,
				[]store.AuthorizationFactRef{
					{Kind: model.DirectoryEpochKind, ID: model.ID(tenant), Version: epoch.Version},
					{Kind: identityDescriptor.Kind, ID: identity.ID, Version: identity.Version},
				})
		})
	}()
	<-readerStarted
	select {
	case err := <-readerDone:
		t.Fatalf("epoch reader did not wait on held order-5 row: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	if _, err := holder.ExecContext(ctx, "SET LOCAL lock_timeout = '500ms'"); err != nil {
		t.Fatalf("set identity table lock timeout: %v", err)
	}
	if _, err := holder.ExecContext(ctx,
		"LOCK TABLE ONLY public.identities IN ROW EXCLUSIVE MODE"); err != nil {
		t.Fatalf("reader took Identity SHARE before epoch: %v", err)
	}
	if err := holder.Rollback(); err != nil {
		t.Fatalf("rollback epoch holder: %v", err)
	}
	if err := <-readerDone; err != nil {
		t.Fatalf("epoch-first authority reader: %v", err)
	}

	// The K2-only Identity→Agent snapshot interleaves with the real DropTenant.
	// Pause Drop after Identity(10); the snapshot must wait at SHARE and may not
	// acquire Agent(20) in the opposite direction.
	k2Tenant := provisionTenant(t, st, "directory-lock-k2")
	var k2Identity model.Identity
	var k2Agent model.Agent
	if err := st.Mutate(ctx, k2Tenant, func(sc store.Scope) error {
		var err error
		k2Identity, err = sc.Identities().Create(ctx, model.Identity{
			Name: "k2 identity", Kind: "service", ExternalID: "lock:k2",
		})
		if err != nil {
			return err
		}
		k2Agent, err = sc.Agents().Create(ctx, model.Agent{
			Name: "k2 agent", Kind: "test", IdentityID: k2Identity.ID,
			Status: model.StatusActive,
		})
		return err
	}); err != nil {
		t.Fatalf("seed K2 lock facts: %v", err)
	}
	identityDeleted := make(chan struct{})
	releaseDrop := make(chan struct{})
	var identityOnce sync.Once
	tenantDropAfterAuthorizationFactTestHook = func(kind model.Kind) error {
		if kind == identityDescriptor.Kind {
			identityOnce.Do(func() { close(identityDeleted) })
			select {
			case <-releaseDrop:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	}
	t.Cleanup(func() { tenantDropAfterAuthorizationFactTestHook = nil })
	dropDone := make(chan error, 1)
	go func() {
		dropDone <- st.System(ctx, func(sys store.SystemScope) error {
			return sys.DropTenant(ctx, k2Tenant)
		})
	}()
	select {
	case <-identityDeleted:
	case err := <-dropDone:
		t.Fatalf("DropTenant ended before Identity checkpoint: %v", err)
	case <-ctx.Done():
		t.Fatalf("DropTenant did not reach Identity checkpoint: %v", ctx.Err())
	}
	k2Started := make(chan struct{})
	k2Done := make(chan error, 1)
	go func() {
		k2Done <- st.Mutate(ctx, k2Tenant, func(sc store.Scope) error {
			close(k2Started)
			return sc.(store.AuthoritySnapshotLocker).LockAuthoritySnapshot(ctx,
				[]store.AuthorizationFactRef{
					{Kind: identityDescriptor.Kind, ID: k2Identity.ID, Version: k2Identity.Version},
					{Kind: agentDescriptor.Kind, ID: k2Agent.ID, Version: k2Agent.Version},
				})
		})
	}()
	<-k2Started
	select {
	case err := <-k2Done:
		t.Fatalf("K2 snapshot crossed Drop's Identity lock: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	close(releaseDrop)
	if err := <-dropDone; err != nil {
		t.Fatalf("ordered DropTenant: %v", err)
	}
	if err := <-k2Done; !errors.Is(err, store.ErrNotFound) ||
		strings.Contains(strings.ToLower(err.Error()), "deadlock") {
		t.Fatalf("K2 snapshot after ordered Drop err = %v, want non-deadlock ErrNotFound", err)
	}
	tenantDropAfterAuthorizationFactTestHook = nil
}

func TestDirectoryEpochPostgresDropPurgesClosedAuthEstate(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pg := isolatedPGSplit(t)
	st, err := Open(ctx, store.Config{
		Engine: store.EnginePostgres, DSN: pg.App, OwnerDSN: pg.Owner,
		AdminDSN: pg.Admin, MaxConns: 1,
	}, nil)
	if err != nil {
		t.Fatalf("open PostgreSQL auth-estate store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	directoryEpochTestExerciseAuthEstateDrop(t, st)
}

func TestDirectoryEpochPostgresLifecyclePathsShareGlobalWriterLock(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pg := isolatedPGSplit(t)
	cfg := store.Config{
		Engine: store.EnginePostgres, DSN: pg.App, OwnerDSN: pg.Owner,
		AdminDSN: pg.Admin, MaxConns: 4,
	}
	st, err := Open(ctx, cfg, nil)
	if err != nil {
		t.Fatalf("open lifecycle-lock store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	missing := provisionTenant(t, st, "directory-global-lock-backfill")
	raw, err := sql.Open("pgx", pg.App)
	if err != nil {
		t.Fatalf("open raw lifecycle-lock pool: %v", err)
	}
	defer raw.Close() //nolint:errcheck
	directoryEpochTestDeletePostgresRow(t, raw, missing)

	backfillPaused := make(chan struct{})
	releaseBackfill := make(chan struct{})
	var backfillOnce sync.Once
	directoryEpochBeforeInsertTestHook = func(tenant model.TenantID) error {
		if tenant != missing {
			return nil
		}
		backfillOnce.Do(func() { close(backfillPaused) })
		select {
		case <-releaseBackfill:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	t.Cleanup(func() { directoryEpochBeforeInsertTestHook = nil })
	type openResult struct {
		store store.Store
		err   error
	}
	backfillDone := make(chan openResult, 1)
	go func() {
		reopened, err := Open(ctx, cfg, nil)
		backfillDone <- openResult{store: reopened, err: err}
	}()
	select {
	case <-backfillPaused:
	case result := <-backfillDone:
		if result.store != nil {
			_ = result.store.Close()
		}
		t.Fatalf("authoritative backfill ended before pause: %v", result.err)
	case <-ctx.Done():
		t.Fatalf("authoritative backfill did not reach pause: %v", ctx.Err())
	}

	createDuringBackfill := make(chan error, 1)
	go func() {
		createDuringBackfill <- st.System(ctx, func(sys store.SystemScope) error {
			_, err := sys.CreateOrg(ctx, model.Org{
				Name: "created after backfill lock", Slug: "directory-lock-after-backfill",
				Status: model.StatusActive,
			})
			return err
		})
	}()
	select {
	case err := <-createDuringBackfill:
		t.Fatalf("CreateOrg crossed held backfill writer lock: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	close(releaseBackfill)
	result := <-backfillDone
	if result.err != nil {
		t.Fatalf("release authoritative backfill: %v", result.err)
	}
	if result.store == nil {
		t.Fatal("authoritative backfill returned nil store")
	}
	if err := result.store.Close(); err != nil {
		t.Fatalf("close authoritative backfill store: %v", err)
	}
	if err := <-createDuringBackfill; err != nil {
		t.Fatalf("CreateOrg after backfill commit: %v", err)
	}
	directoryEpochBeforeInsertTestHook = nil

	victim := provisionTenant(t, st, "directory-global-lock-drop")
	if err := st.Mutate(ctx, victim, func(sc store.Scope) error {
		_, err := sc.Identities().Create(ctx, model.Identity{
			Name: "global lock identity", Kind: "service", ExternalID: "global:lock",
		})
		return err
	}); err != nil {
		t.Fatalf("seed lifecycle-lock victim: %v", err)
	}
	dropPaused := make(chan struct{})
	releaseDrop := make(chan struct{})
	var dropOnce sync.Once
	tenantDropAfterAuthorizationFactTestHook = func(kind model.Kind) error {
		if kind != identityDescriptor.Kind {
			return nil
		}
		dropOnce.Do(func() { close(dropPaused) })
		select {
		case <-releaseDrop:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	t.Cleanup(func() { tenantDropAfterAuthorizationFactTestHook = nil })
	dropDone := make(chan error, 1)
	go func() {
		dropDone <- st.System(ctx, func(sys store.SystemScope) error {
			return sys.DropTenant(ctx, victim)
		})
	}()
	select {
	case <-dropPaused:
	case err := <-dropDone:
		t.Fatalf("DropTenant ended before writer-lock pause: %v", err)
	case <-ctx.Done():
		t.Fatalf("DropTenant did not reach writer-lock pause: %v", ctx.Err())
	}
	createDuringDrop := make(chan error, 1)
	go func() {
		createDuringDrop <- st.System(ctx, func(sys store.SystemScope) error {
			_, err := sys.CreateOrg(ctx, model.Org{
				Name: "created after drop lock", Slug: "directory-lock-after-drop",
				Status: model.StatusActive,
			})
			return err
		})
	}()
	select {
	case err := <-createDuringDrop:
		t.Fatalf("CreateOrg crossed held DropTenant writer lock: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	close(releaseDrop)
	if err := <-dropDone; err != nil {
		t.Fatalf("release DropTenant: %v", err)
	}
	if err := <-createDuringDrop; err != nil {
		t.Fatalf("CreateOrg after DropTenant commit: %v", err)
	}
	tenantDropAfterAuthorizationFactTestHook = nil
}

func directoryEpochTestReadPostgresRow(
	t *testing.T,
	db *sql.DB,
	tenant model.TenantID,
) (directoryEpochTestRow, bool) {
	t.Helper()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin PostgreSQL epoch read: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck
	dia, _ := dialect.New(store.EnginePostgres)
	if err := bindDirectoryTenant(context.Background(), tx, dia, tenant); err != nil {
		t.Fatalf("bind PostgreSQL epoch read: %v", err)
	}
	var row directoryEpochTestRow
	err = tx.QueryRowContext(context.Background(), `SELECT
id, tenant_id, created_at, updated_at, version
FROM public.core_directory_epoch WHERE tenant_id = $1`, tenant.String()).Scan(
		&row.id, &row.rowTenant, &row.createdAt, &row.updatedAt, &row.version,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return directoryEpochTestRow{}, false
	}
	if err != nil {
		t.Fatalf("read PostgreSQL directory epoch %s: %v", tenant, err)
	}
	return row, true
}

func directoryEpochTestDeletePostgresRow(t *testing.T, db *sql.DB, tenant model.TenantID) {
	t.Helper()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin PostgreSQL epoch delete: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck
	dia, _ := dialect.New(store.EnginePostgres)
	if err := bindDirectoryTenant(context.Background(), tx, dia, tenant); err != nil {
		t.Fatalf("bind PostgreSQL epoch delete: %v", err)
	}
	result, err := tx.ExecContext(context.Background(),
		"DELETE FROM public.core_directory_epoch WHERE tenant_id = $1", tenant.String())
	if err != nil {
		t.Fatalf("delete PostgreSQL epoch: %v", err)
	}
	if rows, _ := result.RowsAffected(); rows != 1 {
		t.Fatalf("delete PostgreSQL epoch affected %d rows, want 1", rows)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit PostgreSQL epoch delete: %v", err)
	}
}
