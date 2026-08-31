// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestDirectoryWritersBumpAtomicallyPostgresSplitOwner(t *testing.T) {
	ctx := context.Background()
	pg := isolatedPGSplit(t)
	st, err := Open(ctx, store.Config{
		Engine: store.EnginePostgres, DSN: pg.App, OwnerDSN: pg.Owner,
		AdminDSN: pg.Admin, MaxConns: 6,
	}, nil)
	if err != nil {
		t.Fatalf("open split-owner directory writer store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	directoryWriterTestEnforcePostgres(t, pg.Owner, 37)

	tenantA := provisionTenant(t, st, "pg-directory-a")
	tenantZ := provisionTenant(t, st, "pg-directory-z")

	// Enforced tenant-local writers present the generation, pre-bump once for a
	// multi-source transaction and return to an empty transaction-local GUC.
	beforeA := directoryWriterTestEpoch(t, st, tenantA).Version
	if err := st.Mutate(ctx, tenantA, func(scope store.Scope) error {
		identity, err := scope.Identities().Create(ctx, model.Identity{
			Name: "pg identity", Kind: "service", ExternalID: "pg-identity",
		})
		if err != nil {
			return err
		}
		_, err = scope.Agents().Create(ctx, model.Agent{
			Name: "pg agent", Kind: "assistant", Status: model.StatusActive,
			IdentityID: identity.ID,
		})
		return err
	}); err != nil {
		t.Fatalf("PostgreSQL tenant-local enforced writers: %v", err)
	}
	directoryWriterTestWantEpoch(t, st, tenantA, beforeA+1)
	directoryWriterTestWantPostgresGenerationBaseline(t, st.(*sqlStore).db)

	var (
		user       model.User
		membership model.Membership
		groupA     model.UserGroup
		groupZ     model.UserGroup
		member     model.UserGroupMember
	)
	if err := st.AuthMutate(ctx, func(auth store.AuthScope) error {
		var err error
		user, err = auth.Users().Create(ctx, model.User{
			Email: "pg-directory@example.test", Status: model.StatusActive,
		})
		return err
	}); err != nil {
		t.Fatalf("create PostgreSQL guarded user: %v", err)
	}
	directoryWriterTestAuthDeltas(t, st, []model.TenantID{tenantA}, func(auth store.AuthScope) error {
		var err error
		membership, err = auth.Memberships().Create(ctx, model.Membership{
			UserID: user.ID, TargetTenantID: tenantA, Role: "viewer",
		})
		return err
	})
	directoryWriterTestAuthDeltas(t, st, []model.TenantID{tenantA}, func(auth store.AuthScope) error {
		var err error
		groupA, err = auth.Groups().Create(ctx, model.UserGroup{
			TargetTenantID: tenantA, DisplayName: "PG A",
		})
		return err
	})
	directoryWriterTestAuthDeltas(t, st, []model.TenantID{tenantZ}, func(auth store.AuthScope) error {
		var err error
		groupZ, err = auth.Groups().Create(ctx, model.UserGroup{
			TargetTenantID: tenantZ, DisplayName: "PG Z",
		})
		return err
	})
	directoryWriterTestAuthDeltas(t, st, []model.TenantID{tenantA}, func(auth store.AuthScope) error {
		var err error
		member, err = auth.GroupMembers().Create(ctx, model.UserGroupMember{
			GroupID: groupA.ID, UserID: user.ID,
		})
		return err
	})

	// Old+new tenant sets are fenced for Membership and GroupMember moves.
	membership.TargetTenantID = tenantZ
	directoryWriterTestAuthDeltas(t, st, []model.TenantID{tenantA, tenantZ}, func(auth store.AuthScope) error {
		var err error
		membership, err = auth.Memberships().Update(ctx, membership)
		return err
	})
	member.GroupID = groupZ.ID
	directoryWriterTestAuthDeltas(t, st, []model.TenantID{tenantA, tenantZ}, func(auth store.AuthScope) error {
		var err error
		member, err = auth.GroupMembers().Update(ctx, member)
		return err
	})
	var movingGroup model.UserGroup
	directoryWriterTestAuthDeltas(t, st, []model.TenantID{tenantA}, func(auth store.AuthScope) error {
		var err error
		movingGroup, err = auth.Groups().Create(ctx, model.UserGroup{
			TargetTenantID: tenantA, DisplayName: "PG moving group",
		})
		return err
	})
	movingGroup.TargetTenantID = tenantZ
	directoryWriterTestAuthDeltas(t, st, []model.TenantID{tenantA, tenantZ}, func(auth store.AuthScope) error {
		var err error
		movingGroup, err = auth.Groups().Update(ctx, movingGroup)
		return err
	})
	directoryWriterTestAuthDeltas(t, st, []model.TenantID{tenantZ}, func(auth store.AuthScope) error {
		return auth.Groups().Delete(ctx, movingGroup.ID)
	})

	// Move both facts back to A so the User↔Membership race starts with a
	// deterministic User tenant set containing A.
	membership.TargetTenantID = tenantA
	directoryWriterTestAuthDeltas(t, st, []model.TenantID{tenantA, tenantZ}, func(auth store.AuthScope) error {
		var err error
		membership, err = auth.Memberships().Update(ctx, membership)
		return err
	})
	member.GroupID = groupA.ID
	directoryWriterTestAuthDeltas(t, st, []model.TenantID{tenantA, tenantZ}, func(auth store.AuthScope) error {
		var err error
		member, err = auth.GroupMembers().Update(ctx, member)
		return err
	})

	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	directoryWriterAfterLockTestHook = func() {
		once.Do(func() {
			close(entered)
			<-release
		})
	}
	t.Cleanup(func() { directoryWriterAfterLockTestHook = nil })
	userDone := make(chan error, 1)
	go func() {
		user.DisplayName = "raced update"
		userDone <- st.AuthMutate(ctx, func(auth store.AuthScope) error {
			_, err := auth.Users().Update(ctx, user)
			return err
		})
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("User.Update did not reach post-lock/pre-discovery boundary")
	}
	type membershipResult struct {
		membership model.Membership
		err        error
	}
	membershipDone := make(chan membershipResult, 1)
	go func() {
		moved := membership
		moved.TargetTenantID = tenantZ
		var updated model.Membership
		err := st.AuthMutate(ctx, func(auth store.AuthScope) error {
			var err error
			updated, err = auth.Memberships().Update(ctx, moved)
			return err
		})
		membershipDone <- membershipResult{membership: updated, err: err}
	}()
	select {
	case result := <-membershipDone:
		t.Fatalf("Membership.Update crossed User's global writer lock: %v", result.err)
	case <-time.After(200 * time.Millisecond):
	}
	beforeRaceA := directoryWriterTestEpoch(t, st, tenantA).Version
	beforeRaceZ := directoryWriterTestEpoch(t, st, tenantZ).Version
	close(release)
	if err := <-userDone; err != nil {
		t.Fatalf("release raced User.Update: %v", err)
	}
	result := <-membershipDone
	if result.err != nil {
		t.Fatalf("release raced Membership.Update: %v", result.err)
	}
	membership = result.membership
	directoryWriterAfterLockTestHook = nil
	// User fences A; the later Membership move fences old A and new Z.
	directoryWriterTestWantEpoch(t, st, tenantA, beforeRaceA+2)
	directoryWriterTestWantEpoch(t, st, tenantZ, beforeRaceZ+1)

	// Opposite A→Z/Z→A batches complete without a lock-order inversion. Each
	// operation touches both tenants, hence two exact advances per tenant.
	var userTwo model.User
	if err := st.AuthMutate(ctx, func(auth store.AuthScope) error {
		var err error
		userTwo, err = auth.Users().Create(ctx, model.User{
			Email: "pg-directory-two@example.test", Status: model.StatusActive,
		})
		return err
	}); err != nil {
		t.Fatalf("create second PostgreSQL user: %v", err)
	}
	var membershipTwo model.Membership
	directoryWriterTestAuthDeltas(t, st, []model.TenantID{tenantZ}, func(auth store.AuthScope) error {
		var err error
		membershipTwo, err = auth.Memberships().Create(ctx, model.Membership{
			UserID: userTwo.ID, TargetTenantID: tenantZ, Role: "viewer",
		})
		return err
	})
	membership.TargetTenantID = tenantA
	directoryWriterTestAuthDeltas(t, st, []model.TenantID{tenantA, tenantZ}, func(auth store.AuthScope) error {
		var err error
		membership, err = auth.Memberships().Update(ctx, membership)
		return err
	})
	beforeCrossA := directoryWriterTestEpoch(t, st, tenantA).Version
	beforeCrossZ := directoryWriterTestEpoch(t, st, tenantZ).Version
	crossDone := make(chan error, 2)
	go func() {
		moved := membership
		moved.TargetTenantID = tenantZ
		crossDone <- st.AuthMutate(ctx, func(auth store.AuthScope) error {
			_, err := auth.Memberships().Update(ctx, moved)
			return err
		})
	}()
	go func() {
		moved := membershipTwo
		moved.TargetTenantID = tenantA
		crossDone <- st.AuthMutate(ctx, func(auth store.AuthScope) error {
			_, err := auth.Memberships().Update(ctx, moved)
			return err
		})
	}()
	for range 2 {
		select {
		case err := <-crossDone:
			if err != nil {
				t.Fatalf("opposite membership batch: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("opposite membership batches deadlocked")
		}
	}
	directoryWriterTestWantEpoch(t, st, tenantA, beforeCrossA+2)
	directoryWriterTestWantEpoch(t, st, tenantZ, beforeCrossZ+2)

	beforeStatus := directoryWriterTestEpoch(t, st, tenantZ).Version
	if err := st.System(ctx, func(system store.SystemScope) error {
		_, err := system.SetOrgStatus(ctx, tenantZ, model.StatusSuspended)
		return err
	}); err != nil {
		t.Fatalf("PostgreSQL SetOrgStatus: %v", err)
	}
	directoryWriterTestWantEpoch(t, st, tenantZ, beforeStatus+1)
	if err := st.System(ctx, func(system store.SystemScope) error {
		_, err := system.SetOrgStatus(ctx, tenantZ, model.StatusSuspended)
		return err
	}); err != nil {
		t.Fatalf("PostgreSQL idempotent SetOrgStatus: %v", err)
	}
	directoryWriterTestWantEpoch(t, st, tenantZ, beforeStatus+1)
	directoryWriterTestWantPostgresGenerationBaseline(t, st.(*sqlStore).db)
}

func TestDirectoryWritersBumpAtomicallyPostgresReadCommittedAfterWait(t *testing.T) {
	ctx := context.Background()
	pg := isolatedPGSplit(t)
	rrApp := directoryWriterTestDefaultIsolationDSN(t, pg.App, "repeatable read")
	st, err := Open(ctx, store.Config{
		Engine: store.EnginePostgres, DSN: rrApp, OwnerDSN: pg.Owner,
		AdminDSN: pg.Admin, MaxConns: 6,
	}, nil)
	if err != nil {
		t.Fatalf("open split-owner store with REPEATABLE READ default: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ss := st.(*sqlStore)

	// Prove the adversarial session default is active. The production envelopes
	// below must override it; otherwise BindTenant fixes this stale snapshot
	// before a contended advisory lock is acquired.
	rawTx, err := ss.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin transaction inheriting server isolation: %v", err)
	}
	var inherited string
	if err := rawTx.QueryRowContext(ctx, "SHOW transaction_isolation").Scan(&inherited); err != nil {
		_ = rawTx.Rollback()
		t.Fatalf("read inherited transaction isolation: %v", err)
	}
	if err := rawTx.Rollback(); err != nil {
		t.Fatalf("rollback inherited-isolation probe: %v", err)
	}
	if inherited != "repeatable read" {
		t.Fatalf("inherited transaction isolation = %q, want repeatable read", inherited)
	}

	directoryWriterTestEnforcePostgres(t, pg.Owner, 47)
	tenantA := provisionTenant(t, st, "pg-directory-rr-a")
	tenantZ := provisionTenant(t, st, "pg-directory-rr-z")

	var authIsolation string
	if err := st.AuthMutate(ctx, func(auth store.AuthScope) error {
		return auth.(*authScope).ts.tx.QueryRowContext(
			ctx, "SHOW transaction_isolation",
		).Scan(&authIsolation)
	}); err != nil {
		t.Fatalf("probe AuthMutate transaction isolation: %v", err)
	}
	if authIsolation != "read committed" {
		t.Fatalf("AuthMutate isolation = %q, want read committed", authIsolation)
	}
	var systemIsolation string
	if err := st.System(ctx, func(system store.SystemScope) error {
		return system.(*systemScope).tx.QueryRowContext(
			ctx, "SHOW transaction_isolation",
		).Scan(&systemIsolation)
	}); err != nil {
		t.Fatalf("probe System transaction isolation: %v", err)
	}
	if systemIsolation != "read committed" {
		t.Fatalf("System isolation = %q, want read committed", systemIsolation)
	}

	var user model.User
	if err := st.AuthMutate(ctx, func(auth store.AuthScope) error {
		var err error
		user, err = auth.Users().Create(ctx, model.User{
			Email: "pg-directory-rr@example.test", Status: model.StatusActive,
		})
		return err
	}); err != nil {
		t.Fatalf("create REPEATABLE READ race user: %v", err)
	}
	var membership model.Membership
	directoryWriterTestAuthDeltas(t, st, []model.TenantID{tenantA}, func(auth store.AuthScope) error {
		var err error
		membership, err = auth.Memberships().Create(ctx, model.Membership{
			UserID: user.ID, TargetTenantID: tenantA, Role: "viewer",
		})
		return err
	})

	beforeA := directoryWriterTestEpoch(t, st, tenantA).Version
	beforeZ := directoryWriterTestEpoch(t, st, tenantZ).Version
	entered := make(chan struct{})
	release := make(chan struct{})
	var blockOnce, releaseOnce sync.Once
	directoryWriterAfterLockTestHook = func() {
		blockOnce.Do(func() {
			close(entered)
			<-release
		})
	}
	releaseHolder := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(func() {
		directoryWriterAfterLockTestHook = nil
		releaseHolder()
	})

	type moveResult struct {
		membership model.Membership
		err        error
	}
	moveDone := make(chan moveResult, 1)
	go func() {
		moved := membership
		moved.TargetTenantID = tenantZ
		var updated model.Membership
		err := st.AuthMutate(ctx, func(auth store.AuthScope) error {
			var err error
			updated, err = auth.Memberships().Update(ctx, moved)
			return err
		})
		moveDone <- moveResult{membership: updated, err: err}
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("Membership.Update did not reach post-lock boundary")
	}

	userDone := make(chan error, 1)
	go func() {
		updated := user
		updated.DisplayName = "post-wait snapshot"
		userDone <- st.AuthMutate(ctx, func(auth store.AuthScope) error {
			_, err := auth.Users().Update(ctx, updated)
			return err
		})
	}()

	owner, err := sql.Open("pgx", pg.Owner)
	if err != nil {
		t.Fatalf("open owner lock observer: %v", err)
	}
	defer owner.Close()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var waiters int
		if err := owner.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM pg_catalog.pg_locks
WHERE locktype = 'advisory'
  AND database = (SELECT oid FROM pg_catalog.pg_database WHERE datname = current_database())
  AND NOT granted`).Scan(&waiters); err != nil {
			t.Fatalf("observe waiting directory advisory lock: %v", err)
		}
		if waiters > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("User.Update did not wait on Membership's directory lock")
		}
		time.Sleep(10 * time.Millisecond)
	}

	releaseHolder()
	result := <-moveDone
	if result.err != nil {
		t.Fatalf("release REPEATABLE READ Membership.Update: %v", result.err)
	}
	if result.membership.TargetTenantID != tenantZ {
		t.Fatalf("moved membership tenant = %s, want %s",
			result.membership.TargetTenantID, tenantZ)
	}
	select {
	case err := <-userDone:
		if err != nil {
			t.Fatalf("User.Update after directory lock wait: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("User.Update stayed blocked after Membership commit")
	}
	directoryWriterAfterLockTestHook = nil

	// Membership fences old A and new Z. The waiting User must discover only the
	// committed Z association; a snapshot inherited from before the wait instead
	// produces A+2/Z+1 and fails these exact assertions.
	directoryWriterTestWantEpoch(t, st, tenantA, beforeA+1)
	directoryWriterTestWantEpoch(t, st, tenantZ, beforeZ+2)
	directoryWriterTestWantPostgresGenerationBaseline(t, ss.db)
}

func TestDirectoryWriterPostgresTempIdentityLockUsesPublic(t *testing.T) {
	ctx := context.Background()
	pg := isolatedPGSplit(t)
	st, err := Open(ctx, store.Config{
		Engine: store.EnginePostgres, DSN: pg.App, OwnerDSN: pg.Owner,
		AdminDSN: pg.Admin, MaxConns: 4,
	}, nil)
	if err != nil {
		t.Fatalf("open split-owner TEMP-lock store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	directoryWriterTestEnforcePostgres(t, pg.Owner, 41)
	tenant := provisionTenant(t, st, "pg-directory-temp-lock")

	var identity model.Identity
	if err := st.Mutate(ctx, tenant, func(scope store.Scope) error {
		var err error
		identity, err = scope.Identities().Create(ctx, model.Identity{
			Name: "lock identity", Kind: "service", ExternalID: "shared-external",
		})
		return err
	}); err != nil {
		t.Fatalf("seed identity for public table lock: %v", err)
	}

	ss := st.(*sqlStore)
	conn, err := ss.db.Conn(ctx)
	if err != nil {
		t.Fatalf("pin PostgreSQL TEMP-lock connection: %v", err)
	}
	defer conn.Close() //nolint:errcheck
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin PostgreSQL TEMP-lock transaction: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck
	if err := ss.dia.BindTenant(ctx, tx, tenant); err != nil {
		t.Fatalf("bind PostgreSQL TEMP-lock tenant: %v", err)
	}
	if _, err := tx.ExecContext(ctx,
		"CREATE TEMP TABLE identities AS SELECT * FROM public.identities WHERE false",
	); err != nil {
		t.Fatalf("create pg_temp.identities: %v", err)
	}
	scope := &tenantScope{s: ss, tx: tx, tenant: tenant}
	if err := scope.LockAuthoritySnapshot(ctx, []store.AuthorizationFactRef{{
		Kind: identityDescriptor.Kind, ID: identity.ID, Version: identity.Version,
	}}); err != nil {
		t.Fatalf("lock identity authority snapshot with TEMP shadow: %v", err)
	}

	writerDone := make(chan error, 1)
	go func() {
		writerDone <- st.Mutate(ctx, tenant, func(scope store.Scope) error {
			_, err := scope.Identities().Create(ctx, model.Identity{
				Name: "phantom", Kind: "service", ExternalID: "shared-external",
			})
			return err
		})
	}()
	select {
	case err := <-writerDone:
		t.Fatalf("writer crossed public identities SHARE lock via TEMP shadow: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("release public identities SHARE lock: %v", err)
	}
	select {
	case err := <-writerDone:
		if err != nil {
			t.Fatalf("writer after public identities lock release: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("writer stayed blocked after public identities lock release")
	}
}

func TestDirectoryWriterPostgresCreateRequiresExactSourceRow(t *testing.T) {
	ctx := context.Background()
	pg := isolatedPGSplit(t)
	st, err := Open(ctx, store.Config{
		Engine: store.EnginePostgres, DSN: pg.App, OwnerDSN: pg.Owner,
		AdminDSN: pg.Admin, MaxConns: 4,
	}, nil)
	if err != nil {
		t.Fatalf("open split-owner cardinality store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	directoryWriterTestEnforcePostgres(t, pg.Owner, 43)
	tenant := provisionTenant(t, st, "pg-directory-cardinality")

	owner, err := sql.Open("pgx", pg.Owner)
	if err != nil {
		t.Fatalf("open PostgreSQL owner for RETURN NULL trigger: %v", err)
	}
	defer owner.Close()
	if _, err := owner.ExecContext(ctx, `
CREATE FUNCTION public.directory_writer_test_ignore_identity()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  RETURN NULL;
END
$$;
CREATE TRIGGER aaa_directory_writer_test_ignore_identity
BEFORE INSERT ON public.identities
FOR EACH ROW EXECUTE FUNCTION public.directory_writer_test_ignore_identity()`); err != nil {
		t.Fatalf("install PostgreSQL RETURN NULL source trigger: %v", err)
	}

	before := directoryWriterTestEpoch(t, st, tenant).Version
	err = st.Mutate(ctx, tenant, func(scope store.Scope) error {
		_, err := scope.Identities().Create(ctx, model.Identity{
			Name: "ignored", Kind: "service", ExternalID: "ignored",
		})
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "want exactly one") {
		t.Fatalf("PostgreSQL RETURN NULL insert err = %v, want exact-cardinality refusal", err)
	}
	directoryWriterTestWantEpoch(t, st, tenant, before)
	directoryWriterTestWantPostgresGenerationBaseline(t, st.(*sqlStore).db)
}

func directoryWriterTestEnforcePostgres(t *testing.T, ownerDSN string, generation int64) {
	t.Helper()
	owner, err := sql.Open("pgx", ownerDSN)
	if err != nil {
		t.Fatalf("open PostgreSQL owner for directory activation: %v", err)
	}
	defer owner.Close()
	result, err := owner.ExecContext(context.Background(), `
UPDATE public.directory_writer_control
SET mode = 'enforced', expected_generation = $1
WHERE control_key = $2`, generation, directoryWriterLockKey)
	if err != nil {
		t.Fatalf("enforce PostgreSQL directory writer control: %v", err)
	}
	rows, err := result.RowsAffected()
	if err != nil || rows != 1 {
		t.Fatalf("enforce PostgreSQL control affected %d rows, err=%v", rows, err)
	}
}

func directoryWriterTestWantPostgresGenerationBaseline(t *testing.T, db *sql.DB) {
	t.Helper()
	var generation string
	if err := db.QueryRowContext(context.Background(),
		"SELECT COALESCE(pg_catalog.current_setting($1, true), '')",
		dialect.DirectoryWriterGenerationGUC,
	).Scan(&generation); err != nil {
		t.Fatalf("read PostgreSQL directory generation baseline: %v", err)
	}
	if generation != "" {
		t.Fatalf("PostgreSQL directory generation baseline = %q, want empty", generation)
	}
}

func directoryWriterTestDefaultIsolationDSN(
	t *testing.T,
	dsn string,
	isolation string,
) string {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse PostgreSQL isolation test DSN: %v", err)
	}
	query := u.Query()
	query.Set("default_transaction_isolation", isolation)
	u.RawQuery = query.Encode()
	return u.String()
}
