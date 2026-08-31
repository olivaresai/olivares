// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// TestDirectoryWritersBumpAtomically is M117's SQLite discriminator. It covers
// the closed decorated repository inventory, transaction-wide deduplication, auth
// old+new discovery, User's membership+group union, pre-bump ordering,
// generation presentation, rollback/poison and the final empty marker.
func TestDirectoryWritersBumpAtomically(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	ss := st.(*sqlStore)
	tenantA := provisionTenant(t, st, "directory-writer-a")
	tenantB := provisionTenant(t, st, "directory-writer-b")
	directoryWriterTestEnforceSQLite(t, ss, 17)

	// Even a User.Create with no business-tenant associations must take the
	// writer lock and arm the generation: users is itself a guarded source.
	var beforeSourceCalls int
	// MEASURED, not derived. Retiring the unconditional prelude removes one
	// observation per AuthMutate -- six of them -- and the surviving thirteen are
	// not the old table minus six rows, because the values themselves shift. This
	// table came from instrumenting the hook to PRINT what it saw and reading the
	// output; deriving it by hand is how a table like this starts certifying the
	// drift it exists to catch.
	preBumpVersions := [][2]int64{
		{1, 1},
		{2, 1}, {2, 2}, {2, 2},
		{3, 3}, {4, 3},
		{5, 4}, {6, 5}, {7, 5},
		{7, 5}, {7, 5}, {7, 5},
		{8, 5},
	}
	directoryWriterBeforeSourceTestHook = func(
		ctx context.Context,
		tracker *directoryWriteTracker,
	) error {
		if beforeSourceCalls >= len(preBumpVersions) {
			return fmt.Errorf("unexpected before-source observation %d", beforeSourceCalls+1)
		}
		want := preBumpVersions[beforeSourceCalls]
		beforeSourceCalls++
		if err := directoryWriterTestRequireSQLitePresentation(ctx, tracker, 17); err != nil {
			return err
		}
		for i, tenant := range []model.TenantID{tenantA, tenantB} {
			epoch, found, err := readDirectoryEpochRow(ctx, tracker.tx, tracker.dia, tenant)
			if err != nil || !found {
				return fmt.Errorf("read pre-source epoch for %s: found=%t err=%v",
					tenant, found, err)
			}
			if epoch.Version != want[i] {
				return fmt.Errorf("pre-source epoch for %s = %d, want %d",
					tenant, epoch.Version, want[i])
			}
		}
		return nil
	}
	t.Cleanup(func() {
		directoryWriterBeforeSourceTestHook = nil
		directoryWriterAfterLockTestHook = nil
	})

	// A callback that mutates NOTHING takes no directory writer lock at all. This is
	// the property the retired prelude denied: it reserved the global lock for every
	// auth transaction, so a read-only or no-op callback serialized against every
	// other one. Zero here is the whole point of the change, and it is asserted
	// BEFORE the create so a later bump cannot make it pass by accident.
	if err := st.AuthMutate(ctx, func(store.AuthScope) error { return nil }); err != nil {
		t.Fatalf("no-op AuthMutate: %v", err)
	}
	if beforeSourceCalls != 0 {
		t.Fatalf("no-op AuthMutate before-source observations = %d, want 0", beforeSourceCalls)
	}

	var user model.User
	if err := st.AuthMutate(ctx, func(auth store.AuthScope) error {
		var err error
		user, err = auth.Users().Create(ctx, model.User{
			Email: "directory-writer@example.test", Status: model.StatusActive,
		})
		return err
	}); err != nil {
		t.Fatalf("create guarded user with empty tenant set: %v", err)
	}
	if beforeSourceCalls != 1 {
		t.Fatalf("User.Create before-source observations = %d, want 1", beforeSourceCalls)
	}
	directoryEpochTestWantSQLiteBaseline(t, ss.db)
	directoryWriterTestWantEpoch(t, st, tenantA, 1)
	directoryWriterTestWantEpoch(t, st, tenantB, 1)

	// Membership(A), Group(B) and GroupMember(B) share one transaction. A and B
	// each bump exactly once even though B is affected by two source writes.
	var (
		membership model.Membership
		groupB     model.UserGroup
		member     model.UserGroupMember
	)
	if err := st.AuthMutate(ctx, func(auth store.AuthScope) error {
		var err error
		membership, err = auth.Memberships().Create(ctx, model.Membership{
			UserID: user.ID, TargetTenantID: tenantA, Role: "viewer",
		})
		if err != nil {
			return err
		}
		groupB, err = auth.Groups().Create(ctx, model.UserGroup{
			TargetTenantID: tenantB, DisplayName: "B",
		})
		if err != nil {
			return err
		}
		member, err = auth.GroupMembers().Create(ctx, model.UserGroupMember{
			GroupID: groupB.ID, UserID: user.ID,
		})
		return err
	}); err != nil {
		t.Fatalf("seed auth directory facts: %v", err)
	}
	directoryWriterTestWantEpoch(t, st, tenantA, 2)
	directoryWriterTestWantEpoch(t, st, tenantB, 2)
	directoryEpochTestWantSQLiteBaseline(t, ss.db)

	// Moving a membership discovers old A and new B while holding the global
	// lock, sorts them, and bumps both before the source UPDATE.
	membership.TargetTenantID = tenantB
	if err := st.AuthMutate(ctx, func(auth store.AuthScope) error {
		var err error
		membership, err = auth.Memberships().Update(ctx, membership)
		return err
	}); err != nil {
		t.Fatalf("move membership A to B: %v", err)
	}
	directoryWriterTestWantEpoch(t, st, tenantA, 3)
	directoryWriterTestWantEpoch(t, st, tenantB, 3)

	var groupA model.UserGroup
	if err := st.AuthMutate(ctx, func(auth store.AuthScope) error {
		var err error
		groupA, err = auth.Groups().Create(ctx, model.UserGroup{
			TargetTenantID: tenantA, DisplayName: "A",
		})
		return err
	}); err != nil {
		t.Fatalf("create group A: %v", err)
	}
	directoryWriterTestWantEpoch(t, st, tenantA, 4)

	// Moving a group-member resolves both the old and new groups. A subsequent
	// User.Update sees membership(B) union group(A), so both tenants advance.
	member.GroupID = groupA.ID
	if err := st.AuthMutate(ctx, func(auth store.AuthScope) error {
		var err error
		member, err = auth.GroupMembers().Update(ctx, member)
		return err
	}); err != nil {
		t.Fatalf("move group member B to A: %v", err)
	}
	directoryWriterTestWantEpoch(t, st, tenantA, 5)
	directoryWriterTestWantEpoch(t, st, tenantB, 4)

	user.DisplayName = "updated"
	if err := st.AuthMutate(ctx, func(auth store.AuthScope) error {
		var err error
		user, err = auth.Users().Update(ctx, user)
		return err
	}); err != nil {
		t.Fatalf("update user across membership and group tenants: %v", err)
	}
	directoryWriterTestWantEpoch(t, st, tenantA, 6)
	directoryWriterTestWantEpoch(t, st, tenantB, 5)

	// Identity, Agent, AgentGroup and AgentGroupMember are tenant-local and share
	// the same tracker, so four protected source writes produce one bump.
	var identity model.Identity
	if err := st.Mutate(ctx, tenantA, func(scope store.Scope) error {
		var err error
		identity, err = scope.Identities().Create(ctx, model.Identity{
			Name: "writer identity", Kind: "service", ExternalID: "writer-identity",
		})
		if err != nil {
			return err
		}
		agent, err := scope.Agents().Create(ctx, model.Agent{
			Name: "writer agent", Kind: "assistant", Status: model.StatusActive,
			IdentityID: identity.ID,
		})
		if err != nil {
			return err
		}
		group, err := scope.AgentGroups().Create(ctx, model.AgentGroup{
			Name: "writer group", Slug: "writer-group", Status: model.StatusActive,
		})
		if err != nil {
			return err
		}
		_, err = scope.AgentGroupMembers().Create(ctx, model.AgentGroupMember{
			GroupID: group.ID, AgentID: agent.ID,
		})
		return err
	}); err != nil {
		t.Fatalf("write tenant-local directory batch: %v", err)
	}
	directoryWriterTestWantEpoch(t, st, tenantA, 7)
	directoryWriterTestWantSQLiteBaseline(t, ss.db, tenantA)

	// Lock is a read fence. Under enforced guards it reserves SQLite through the
	// engine-owned scope row, reads Identity and does not bump the epoch.
	if err := st.Mutate(ctx, tenantA, func(scope store.Scope) error {
		locker, ok := scope.Identities().(store.RowLocker[model.Identity])
		if !ok {
			return errors.New("tracked identity repository lost RowLocker")
		}
		_, err := locker.Lock(ctx, identity.ID)
		return err
	}); err != nil {
		t.Fatalf("identity read fence under enforced directory guards: %v", err)
	}
	directoryWriterTestWantEpoch(t, st, tenantA, 7)

	// A swallowed source failure cannot commit its already-staged bump.
	beforePoison := directoryWriterTestEpoch(t, st, tenantA).Version
	err := st.Mutate(ctx, tenantA, func(scope store.Scope) error {
		_, _ = scope.Agents().Update(ctx, model.Agent{
			BaseFields: model.BaseFields{ID: model.NewID(), Version: 1},
			Name:       "missing", Kind: "assistant", Status: model.StatusActive,
		})
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "transaction poisoned") {
		t.Fatalf("discarded protected writer error = %v, want poisoned transaction", err)
	}
	directoryWriterTestWantEpoch(t, st, tenantA, beforePoison)
	directoryWriterTestWantSQLiteBaseline(t, ss.db, tenantA)

	// An ordinary callback rollback removes both source row and epoch bump.
	rollbackErr := errors.New("rollback directory batch")
	beforeRollback := directoryWriterTestEpoch(t, st, tenantA).Version
	err = st.Mutate(ctx, tenantA, func(scope store.Scope) error {
		if _, err := scope.Identities().Create(ctx, model.Identity{
			Name: "rolled back", Kind: "service", ExternalID: "rolled-back",
		}); err != nil {
			return err
		}
		return rollbackErr
	})
	if !errors.Is(err, rollbackErr) {
		t.Fatalf("directory callback rollback = %v, want sentinel", err)
	}
	directoryWriterTestWantEpoch(t, st, tenantA, beforeRollback)
	directoryWriterTestWantSQLiteBaseline(t, ss.db, tenantA)
	if beforeSourceCalls != len(preBumpVersions) {
		t.Fatalf("before-source observations = %d, want %d",
			beforeSourceCalls, len(preBumpVersions))
	}

	// Org status is a directory-estate write only when status actually changes.
	beforeStatus := directoryWriterTestEpoch(t, st, tenantB).Version
	if err := st.System(ctx, func(system store.SystemScope) error {
		_, err := system.SetOrgStatus(ctx, tenantB, model.StatusSuspended)
		return err
	}); err != nil {
		t.Fatalf("suspend tenant: %v", err)
	}
	directoryWriterTestWantEpoch(t, st, tenantB, beforeStatus+1)
	if err := st.System(ctx, func(system store.SystemScope) error {
		_, err := system.SetOrgStatus(ctx, tenantB, model.StatusSuspended)
		return err
	}); err != nil {
		t.Fatalf("repeat tenant suspension: %v", err)
	}
	directoryWriterTestWantEpoch(t, st, tenantB, beforeStatus+1)
	directoryEpochTestWantSQLiteBaseline(t, ss.db)
}

func TestDirectoryWritersBumpAtomicallyPoisonPreservesUnavailable(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	ss := st.(*sqlStore)
	tenant := provisionTenant(t, st, "directory-poison-unavailable")
	directoryWriterTestEnforceSQLite(t, ss, 19)

	backendErr := &pgconn.PgError{Code: "08006", Message: "connection failure"}
	directoryWriterBeforeSourceTestHook = func(
		context.Context,
		*directoryWriteTracker,
	) error {
		return backendErr
	}
	t.Cleanup(func() { directoryWriterBeforeSourceTestHook = nil })

	err := st.Mutate(ctx, tenant, func(scope store.Scope) error {
		_, _ = scope.Identities().Create(ctx, model.Identity{
			Name: "discarded unavailable", Kind: "service", ExternalID: "unavailable",
		})
		return nil
	})
	if !errors.Is(err, store.ErrStoreUnavailable) {
		t.Fatalf("discarded backend failure = %v, want ErrStoreUnavailable", err)
	}
	var gotBackendErr *pgconn.PgError
	if !errors.As(err, &gotBackendErr) || gotBackendErr != backendErr {
		t.Fatalf("discarded backend failure lost its cause: %v", err)
	}
	directoryWriterTestWantEpoch(t, st, tenant, 1)
	directoryEpochTestWantSQLiteBaseline(t, ss.db)
}

func TestDirectoryWritersBumpAtomicallyPinsPostgresReadCommitted(t *testing.T) {
	postgres, ok := dialect.New(store.EnginePostgres)
	if !ok {
		t.Fatal("construct PostgreSQL dialect")
	}
	options := directoryWriterTxOptions(postgres)
	if options == nil || options.Isolation != sql.LevelReadCommitted {
		t.Fatalf("PostgreSQL directory writer options = %#v, want ReadCommitted", options)
	}
	sqlite, ok := dialect.New(store.EngineSQLite)
	if !ok {
		t.Fatal("construct SQLite dialect")
	}
	if options := directoryWriterTxOptions(sqlite); options != nil {
		t.Fatalf("SQLite directory writer options = %#v, want native nil options", options)
	}
}

func TestDirectoryWritersBumpAtomicallyRejectsLegacyWhenEnforced(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	ss := st.(*sqlStore)
	tenant := provisionTenant(t, st, "directory-enforced-legacy")
	directoryWriterTestEnforceSQLite(t, ss, 21)

	err := st.Mutate(ctx, tenant, func(scope store.Scope) error {
		raw := scope.(*tenantScope)
		legacy := newTypedRepo(raw.repo(identityDescriptor), identityCodec)
		_, err := legacy.Create(ctx, model.Identity{
			Name: "legacy enforced", Kind: "service", ExternalID: "legacy-enforced",
		})
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "directory writer generation required") {
		t.Fatalf("legacy source write under enforced mode = %v, want guard refusal", err)
	}
	directoryWriterTestWantEpoch(t, st, tenant, 1)
	var rows int
	if err := ss.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM main.identities WHERE tenant_id = ?", tenant.String(),
	).Scan(&rows); err != nil {
		t.Fatalf("count legacy enforced identities: %v", err)
	}
	if rows != 0 {
		t.Fatalf("legacy enforced identity rows = %d, want 0", rows)
	}
	directoryEpochTestWantSQLiteBaseline(t, ss.db)
}

// TestDirectoryWriterCRUDMatrixBumps makes every publicly exposed CRUD leg run
// as the only protected source operation in its transaction. User and Identity
// deliberately omit Delete; their hard-delete legs are covered by the separate
// engine-owned retirement matrix. This shape ensures a
// missing decorator cannot borrow another repository's bump or armed marker and
// makes every leg independently discriminating under enforced guards.
func TestDirectoryWriterCRUDMatrixBumps(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	ss := st.(*sqlStore)
	tenantA := provisionTenant(t, st, "directory-crud-a")
	tenantB := provisionTenant(t, st, "directory-crud-b")
	directoryWriterTestEnforceSQLite(t, ss, 23)

	var user model.User
	directoryWriterTestAuthDeltas(t, st, nil, func(auth store.AuthScope) error {
		var err error
		user, err = auth.Users().Create(ctx, model.User{
			Email: "directory-crud@example.test", Status: model.StatusActive,
		})
		return err
	})

	var membership model.Membership
	directoryWriterTestAuthDeltas(t, st, []model.TenantID{tenantA}, func(auth store.AuthScope) error {
		var err error
		membership, err = auth.Memberships().Create(ctx, model.Membership{
			UserID: user.ID, TargetTenantID: tenantA, Role: "viewer",
		})
		return err
	})
	membership.TargetTenantID = tenantB
	directoryWriterTestAuthDeltas(t, st, []model.TenantID{tenantA, tenantB}, func(auth store.AuthScope) error {
		var err error
		membership, err = auth.Memberships().Update(ctx, membership)
		return err
	})
	directoryWriterTestAuthDeltas(t, st, []model.TenantID{tenantB}, func(auth store.AuthScope) error {
		return auth.Memberships().Delete(ctx, membership.ID)
	})

	var movingGroup model.UserGroup
	directoryWriterTestAuthDeltas(t, st, []model.TenantID{tenantA}, func(auth store.AuthScope) error {
		var err error
		movingGroup, err = auth.Groups().Create(ctx, model.UserGroup{
			TargetTenantID: tenantA, DisplayName: "moving group",
		})
		return err
	})
	movingGroup.TargetTenantID = tenantB
	directoryWriterTestAuthDeltas(t, st, []model.TenantID{tenantA, tenantB}, func(auth store.AuthScope) error {
		var err error
		movingGroup, err = auth.Groups().Update(ctx, movingGroup)
		return err
	})
	directoryWriterTestAuthDeltas(t, st, []model.TenantID{tenantB}, func(auth store.AuthScope) error {
		return auth.Groups().Delete(ctx, movingGroup.ID)
	})

	var groupA, groupB model.UserGroup
	directoryWriterTestAuthDeltas(t, st, []model.TenantID{tenantA}, func(auth store.AuthScope) error {
		var err error
		groupA, err = auth.Groups().Create(ctx, model.UserGroup{
			TargetTenantID: tenantA, DisplayName: "member group A",
		})
		return err
	})
	directoryWriterTestAuthDeltas(t, st, []model.TenantID{tenantB}, func(auth store.AuthScope) error {
		var err error
		groupB, err = auth.Groups().Create(ctx, model.UserGroup{
			TargetTenantID: tenantB, DisplayName: "member group B",
		})
		return err
	})
	var member model.UserGroupMember
	directoryWriterTestAuthDeltas(t, st, []model.TenantID{tenantA}, func(auth store.AuthScope) error {
		var err error
		member, err = auth.GroupMembers().Create(ctx, model.UserGroupMember{
			GroupID: groupA.ID, UserID: user.ID,
		})
		return err
	})
	member.GroupID = groupB.ID
	directoryWriterTestAuthDeltas(t, st, []model.TenantID{tenantA, tenantB}, func(auth store.AuthScope) error {
		var err error
		member, err = auth.GroupMembers().Update(ctx, member)
		return err
	})
	directoryWriterTestAuthDeltas(t, st, []model.TenantID{tenantB}, func(auth store.AuthScope) error {
		return auth.GroupMembers().Delete(ctx, member.ID)
	})

	// Rebuild two associations for User.Update. The operation must fence
	// the union of direct membership(A) and group membership(B).
	directoryWriterTestAuthDeltas(t, st, []model.TenantID{tenantA}, func(auth store.AuthScope) error {
		_, err := auth.Memberships().Create(ctx, model.Membership{
			UserID: user.ID, TargetTenantID: tenantA, Role: "viewer",
		})
		return err
	})
	directoryWriterTestAuthDeltas(t, st, []model.TenantID{tenantB}, func(auth store.AuthScope) error {
		_, err := auth.GroupMembers().Create(ctx, model.UserGroupMember{
			GroupID: groupB.ID, UserID: user.ID,
		})
		return err
	})
	user.DisplayName = "directory CRUD updated"
	directoryWriterTestAuthDeltas(t, st, []model.TenantID{tenantA, tenantB}, func(auth store.AuthScope) error {
		var err error
		user, err = auth.Users().Update(ctx, user)
		return err
	})

	var identity model.Identity
	directoryWriterTestTenantDelta(t, st, tenantA, func(scope store.Scope) error {
		var err error
		identity, err = scope.Identities().Create(ctx, model.Identity{
			Name: "CRUD identity", Kind: "service", ExternalID: "crud-identity",
		})
		return err
	})
	identity.Name = "CRUD identity updated"
	directoryWriterTestTenantDelta(t, st, tenantA, func(scope store.Scope) error {
		var err error
		identity, err = scope.Identities().Update(ctx, identity)
		return err
	})

	var agent model.Agent
	directoryWriterTestTenantDelta(t, st, tenantA, func(scope store.Scope) error {
		var err error
		agent, err = scope.Agents().Create(ctx, model.Agent{
			Name: "CRUD agent", Kind: "assistant", Status: model.StatusActive,
		})
		return err
	})
	agent.Name = "CRUD agent updated"
	directoryWriterTestTenantDelta(t, st, tenantA, func(scope store.Scope) error {
		var err error
		agent, err = scope.Agents().Update(ctx, agent)
		return err
	})
	directoryWriterTestTenantDelta(t, st, tenantA, func(scope store.Scope) error {
		return scope.Agents().Delete(ctx, agent.ID)
	})

	var agentGroup model.AgentGroup
	directoryWriterTestTenantDelta(t, st, tenantA, func(scope store.Scope) error {
		var err error
		agentGroup, err = scope.AgentGroups().Create(ctx, model.AgentGroup{
			Name: "CRUD agents", Slug: "crud-agents", Status: model.StatusActive,
		})
		return err
	})
	agentGroup.Name = "CRUD agents updated"
	directoryWriterTestTenantDelta(t, st, tenantA, func(scope store.Scope) error {
		var err error
		agentGroup, err = scope.AgentGroups().Update(ctx, agentGroup)
		return err
	})

	// The member needs a live agent; creating this dependency is itself checked.
	directoryWriterTestTenantDelta(t, st, tenantA, func(scope store.Scope) error {
		var err error
		agent, err = scope.Agents().Create(ctx, model.Agent{
			Name: "member agent", Kind: "assistant", Status: model.StatusActive,
		})
		return err
	})
	var agentMember model.AgentGroupMember
	directoryWriterTestTenantDelta(t, st, tenantA, func(scope store.Scope) error {
		var err error
		agentMember, err = scope.AgentGroupMembers().Create(ctx, model.AgentGroupMember{
			GroupID: agentGroup.ID, AgentID: agent.ID,
		})
		return err
	})
	directoryWriterTestTenantDelta(t, st, tenantA, func(scope store.Scope) error {
		var err error
		agentMember, err = scope.AgentGroupMembers().Update(ctx, agentMember)
		return err
	})
	directoryWriterTestTenantDelta(t, st, tenantA, func(scope store.Scope) error {
		return scope.AgentGroupMembers().Delete(ctx, agentMember.ID)
	})
	directoryWriterTestTenantDelta(t, st, tenantA, func(scope store.Scope) error {
		return scope.AgentGroups().Delete(ctx, agentGroup.ID)
	})
	directoryWriterTestWantSQLiteBaseline(t, ss.db, tenantA)
}

func TestDirectoryWriterRejectsMalformedOrMissingTargetBeforeSourceDML(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	ss := st.(*sqlStore)
	directoryWriterTestEnforceSQLite(t, ss, 19)

	tests := []struct {
		name   string
		tenant model.TenantID
	}{
		{name: "reserved SYSTEM", tenant: model.SystemTenantID},
		{name: "not UUIDv7", tenant: "11111111-1111-1111-1111-111111111111"},
		{name: "valid UUIDv7 without epoch", tenant: model.NewTenantID()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := st.AuthMutate(ctx, func(auth store.AuthScope) error {
				_, err := auth.Memberships().Create(ctx, model.Membership{
					UserID: model.NewID(), TargetTenantID: test.tenant, Role: "viewer",
				})
				return err
			})
			if !errors.Is(err, store.ErrDirectoryUnavailable) {
				t.Fatalf("membership target %q err = %v, want ErrDirectoryUnavailable",
					test.tenant, err)
			}
			var rows int
			if err := ss.db.QueryRowContext(ctx,
				"SELECT COUNT(*) FROM main.memberships WHERE target_tenant_id = ?",
				test.tenant.String(),
			).Scan(&rows); err != nil {
				t.Fatalf("count malformed target source rows: %v", err)
			}
			if rows != 0 {
				t.Fatalf("malformed target wrote %d membership rows, want 0", rows)
			}
			directoryEpochTestWantSQLiteBaseline(t, ss.db)
		})
	}
}

func TestDirectoryWriterSourcesIgnoreSQLiteTempShadows(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	ss := st.(*sqlStore)
	tenant := provisionTenant(t, st, "directory-temp-source")
	directoryWriterTestEnforceSQLite(t, ss, 29)

	for _, table := range []string{"memberships", "identities", "orgs"} {
		if _, err := ss.db.ExecContext(ctx, "CREATE TEMP TABLE "+table+
			" AS SELECT * FROM main."+table+" WHERE 0"); err != nil {
			t.Fatalf("create TEMP shadow for %s: %v", table, err)
		}
	}

	var user model.User
	if err := st.AuthMutate(ctx, func(auth store.AuthScope) error {
		var err error
		user, err = auth.Users().Create(ctx, model.User{
			Email: "directory-temp@example.test", Status: model.StatusActive,
		})
		return err
	}); err != nil {
		t.Fatalf("create user before shadowed membership: %v", err)
	}
	if err := st.AuthMutate(ctx, func(auth store.AuthScope) error {
		_, err := auth.Memberships().Create(ctx, model.Membership{
			UserID: user.ID, TargetTenantID: tenant, Role: "viewer",
		})
		return err
	}); err != nil {
		t.Fatalf("create membership past TEMP shadow: %v", err)
	}
	if err := st.Mutate(ctx, tenant, func(scope store.Scope) error {
		_, err := scope.Identities().Create(ctx, model.Identity{
			Name: "main identity", Kind: "service", ExternalID: "main-identity",
		})
		return err
	}); err != nil {
		t.Fatalf("create identity past TEMP shadow: %v", err)
	}
	if err := st.System(ctx, func(system store.SystemScope) error {
		_, err := system.SetOrgStatus(ctx, tenant, model.StatusSuspended)
		return err
	}); err != nil {
		t.Fatalf("set org status past TEMP shadow: %v", err)
	}

	for _, check := range []struct {
		table string
		main  int
	}{
		{table: "memberships", main: 1},
		{table: "identities", main: 1},
		{table: "orgs", main: 1},
	} {
		var mainRows, tempRows int
		if err := ss.db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM main."+check.table,
		).Scan(&mainRows); err != nil {
			t.Fatalf("count main.%s: %v", check.table, err)
		}
		if err := ss.db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM temp."+check.table,
		).Scan(&tempRows); err != nil {
			t.Fatalf("count temp.%s: %v", check.table, err)
		}
		if mainRows != check.main || tempRows != 0 {
			t.Fatalf("%s rows main/temp = %d/%d, want %d/0",
				check.table, mainRows, tempRows, check.main)
		}
	}
	var status string
	if err := ss.db.QueryRowContext(ctx,
		"SELECT status FROM main.orgs WHERE id = ? AND tenant_id = ?",
		tenant.String(), tenant.String(),
	).Scan(&status); err != nil {
		t.Fatalf("read main org status past TEMP shadow: %v", err)
	}
	if status != string(model.StatusSuspended) {
		t.Fatalf("main org status = %q, want suspended", status)
	}
	directoryWriterTestWantEpoch(t, st, tenant, 4)
	directoryEpochTestWantSQLiteBaseline(t, ss.db)
}

func TestDirectoryWriterCreateRequiresExactSourceRow(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	ss := st.(*sqlStore)
	tenant := provisionTenant(t, st, "directory-create-cardinality")
	directoryWriterTestEnforceSQLite(t, ss, 31)

	if _, err := ss.db.ExecContext(ctx, `
CREATE TRIGGER main.identities_test_ignore
BEFORE INSERT ON identities
BEGIN
  SELECT RAISE(IGNORE);
END`); err != nil {
		t.Fatalf("create source RAISE(IGNORE) trigger: %v", err)
	}
	before := directoryWriterTestEpoch(t, st, tenant).Version
	err := st.Mutate(ctx, tenant, func(scope store.Scope) error {
		_, err := scope.Identities().Create(ctx, model.Identity{
			Name: "ignored", Kind: "service", ExternalID: "ignored",
		})
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "want exactly one") {
		t.Fatalf("ignored source insert err = %v, want exact-cardinality refusal", err)
	}
	directoryWriterTestWantEpoch(t, st, tenant, before)
	var rows int
	if err := ss.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM main.identities WHERE tenant_id = ?", tenant.String(),
	).Scan(&rows); err != nil {
		t.Fatalf("count ignored identity rows: %v", err)
	}
	if rows != 0 {
		t.Fatalf("ignored identity source rows = %d, want 0", rows)
	}
	directoryEpochTestWantSQLiteBaseline(t, ss.db)
}

func TestDirectoryWriterStagedBumpsWithoutRejectingLegacy(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "directory-staged")
	directoryWriterTestWantEpoch(t, st, tenant, 1)

	if err := st.Mutate(ctx, tenant, func(scope store.Scope) error {
		_, err := scope.Identities().Create(ctx, model.Identity{
			Name: "tracked staged", Kind: "service", ExternalID: "tracked-staged",
		})
		return err
	}); err != nil {
		t.Fatalf("tracked writer in staged mode: %v", err)
	}
	directoryWriterTestWantEpoch(t, st, tenant, 2)

	// Model the previous binary with the package-private undecorated repo. Staged
	// intentionally permits the source write while K3 remains OFF; unlike the new
	// writer above, that legacy write has no epoch protocol.
	if err := st.Mutate(ctx, tenant, func(scope store.Scope) error {
		raw := scope.(*tenantScope)
		legacy := newTypedRepo(raw.repo(identityDescriptor), identityCodec)
		_, err := legacy.Create(ctx, model.Identity{
			Name: "legacy staged", Kind: "service", ExternalID: "legacy-staged",
		})
		return err
	}); err != nil {
		t.Fatalf("legacy writer was rejected in staged mode: %v", err)
	}
	directoryWriterTestWantEpoch(t, st, tenant, 2)
}

func directoryWriterTestAuthDeltas(
	t *testing.T,
	st store.Store,
	affected []model.TenantID,
	write func(store.AuthScope) error,
) {
	t.Helper()
	before := make(map[model.TenantID]int64, len(affected))
	for _, tenant := range affected {
		before[tenant] = directoryWriterTestEpoch(t, st, tenant).Version
	}
	if err := st.AuthMutate(context.Background(), write); err != nil {
		t.Fatalf("auth directory CRUD operation: %v", err)
	}
	for tenant, version := range before {
		directoryWriterTestWantEpoch(t, st, tenant, version+1)
	}
}

func directoryWriterTestTenantDelta(
	t *testing.T,
	st store.Store,
	tenant model.TenantID,
	write func(store.Scope) error,
) {
	t.Helper()
	before := directoryWriterTestEpoch(t, st, tenant).Version
	if err := st.Mutate(context.Background(), tenant, write); err != nil {
		t.Fatalf("tenant directory CRUD operation: %v", err)
	}
	directoryWriterTestWantEpoch(t, st, tenant, before+1)
}

func directoryWriterTestEnforceSQLite(t *testing.T, st *sqlStore, generation int64) {
	t.Helper()
	result, err := st.db.ExecContext(context.Background(), `
UPDATE main.directory_writer_control
SET mode = 'enforced', expected_generation = ?
WHERE control_key = ?`, generation, directoryWriterLockKey)
	if err != nil {
		t.Fatalf("enforce SQLite directory writer control: %v", err)
	}
	rows, err := result.RowsAffected()
	if err != nil || rows != 1 {
		t.Fatalf("enforce SQLite control affected %d rows, err=%v", rows, err)
	}
}

func directoryWriterTestWantSQLiteBaseline(
	t *testing.T,
	db interface {
		QueryRowContext(context.Context, string, ...any) *sql.Row
	},
	wantTenant model.TenantID,
) {
	t.Helper()
	var tenant string
	if err := db.QueryRowContext(context.Background(),
		"SELECT tenant_id FROM main."+dialect.ScopeTenantTable,
	).Scan(&tenant); err != nil {
		t.Fatalf("read SQLite tenant presentation: %v", err)
	}
	if tenant != wantTenant.String() {
		t.Fatalf("SQLite tenant presentation = %q, want %q", tenant, wantTenant)
	}
	var markers int
	if err := db.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM main."+dialect.DirectoryWriterMarkerTable,
	).Scan(&markers); err != nil {
		t.Fatalf("read SQLite directory marker baseline: %v", err)
	}
	if markers != 0 {
		t.Fatalf("SQLite directory marker baseline has %d rows, want 0", markers)
	}
}

func directoryWriterTestRequireSQLitePresentation(
	ctx context.Context,
	tracker *directoryWriteTracker,
	wantGeneration int64,
) error {
	var pin string
	if err := tracker.tx.QueryRowContext(ctx,
		"SELECT tenant_id FROM main."+dialect.ScopeTenantTable,
	).Scan(&pin); err != nil {
		return fmt.Errorf("read directory writer test tenant pin: %w", err)
	}
	if pin != tracker.presentationTenant.String() {
		return fmt.Errorf("directory writer test tenant pin = %q, want %q",
			pin, tracker.presentationTenant)
	}
	var rows, generation int64
	if err := tracker.tx.QueryRowContext(ctx, `
SELECT COUNT(*), COALESCE(MAX(generation), 0)
FROM main.directory_writer_marker`).Scan(&rows, &generation); err != nil {
		return fmt.Errorf("read directory writer test marker: %w", err)
	}
	if rows != 1 || generation != wantGeneration {
		return fmt.Errorf("directory writer test marker = rows:%d generation:%d, want 1/%d",
			rows, generation, wantGeneration)
	}
	return nil
}

func directoryWriterTestEpoch(
	t *testing.T,
	st store.Store,
	tenant model.TenantID,
) model.DirectoryEpoch {
	t.Helper()
	var epoch model.DirectoryEpoch
	if err := st.View(context.Background(), tenant, func(scope store.Scope) error {
		reader, ok := scope.(store.DirectorySnapshotReader)
		if !ok {
			return errors.New("scope lacks DirectorySnapshotReader")
		}
		var err error
		epoch, err = reader.ReadDirectoryEpoch(context.Background())
		return err
	}); err != nil {
		t.Fatalf("read directory epoch for %s: %v", tenant, err)
	}
	return epoch
}

func directoryWriterTestWantEpoch(
	t *testing.T,
	st store.Store,
	tenant model.TenantID,
	want int64,
) {
	t.Helper()
	if got := directoryWriterTestEpoch(t, st, tenant).Version; got != want {
		t.Fatalf("directory epoch for %s = %d, want %d", tenant, got, want)
	}
}
