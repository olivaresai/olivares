// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/internal/store/canon"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	oteltrace "go.opentelemetry.io/otel/trace"
)

const retirementTestActor = "user:directory-retirement-test"

type directoryRetirementSQLiteHarness struct {
	raw store.Store
	sql *sqlStore
	cfg store.Config
}

func newDirectoryRetirementSQLiteHarness(
	t *testing.T,
	cfg store.Config,
) directoryRetirementSQLiteHarness {
	t.Helper()
	if cfg.Engine == "" {
		cfg.Engine = store.EngineSQLite
	}
	if cfg.DSN == "" {
		cfg.DSN = filepath.Join(t.TempDir(), "directory-retirement.db")
	}
	cfg.Debug = true
	raw, err := Open(context.Background(), cfg, nil)
	if err != nil {
		t.Fatalf("open retirement SQLite store: %v", err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	return directoryRetirementSQLiteHarness{raw: raw, sql: raw.(*sqlStore), cfg: cfg}
}

func (h directoryRetirementSQLiteHarness) enforce(t *testing.T) {
	t.Helper()
	_, after, changed, err := ActivateDirectoryWriter(
		context.Background(), h.raw, h.cfg, 1,
	)
	if err != nil {
		t.Fatalf("activate directory writer: %v", err)
	}
	if !changed || after.ControlMode != store.DirectoryControlEnforced ||
		after.ExpectedGeneration != 2 {
		t.Fatalf("activation result = %+v changed=%t", after, changed)
	}
}

func retirementCreateUser(t *testing.T, st store.Store, tag string) model.User {
	t.Helper()
	var out model.User
	err := st.AuthMutate(context.Background(), func(auth store.AuthScope) error {
		var err error
		out, err = auth.Users().Create(context.Background(), model.User{
			Email: tag + "@example.test", DisplayName: tag, Status: model.StatusActive,
		})
		return err
	})
	if err != nil {
		t.Fatalf("create retirement User %q: %v", tag, err)
	}
	return out
}

func retirementCreateIdentity(
	t *testing.T,
	st store.Store,
	tenant model.TenantID,
	tag string,
) model.Identity {
	t.Helper()
	var out model.Identity
	err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		var err error
		out, err = sc.Identities().Create(context.Background(), model.Identity{
			Name: tag, Kind: "service", ExternalID: tag + "-external",
		})
		return err
	})
	if err != nil {
		t.Fatalf("create retirement Identity %q: %v", tag, err)
	}
	return out
}

func retirementCreateAgent(
	t *testing.T,
	st store.Store,
	tenant model.TenantID,
	identity model.ID,
	workspace model.ID,
	tag string,
	status model.LifecycleStatus,
) model.Agent {
	t.Helper()
	var out model.Agent
	err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		var err error
		out, err = sc.Agents().Create(context.Background(), model.Agent{
			Name: tag, Kind: "assistant", ExternalID: tag + "-external",
			Status: status, IdentityID: identity, WorkspaceID: workspace,
		})
		return err
	})
	if err != nil {
		t.Fatalf("create retirement Agent %q: %v", tag, err)
	}
	return out
}

func retirementDefaultWorkspace(
	t *testing.T,
	st store.Store,
	tenant model.TenantID,
) model.Workspace {
	t.Helper()
	var out model.Workspace
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		var err error
		out, err = sc.DefaultWorkspace(context.Background())
		return err
	}); err != nil {
		t.Fatalf("read default workspace: %v", err)
	}
	return out
}

func retirementRowCount(
	t *testing.T,
	ss *sqlStore,
	table string,
	predicate string,
	args ...any,
) int64 {
	t.Helper()
	query := "SELECT COUNT(*) FROM main." + quoteIdent(table)
	if predicate != "" {
		query += " WHERE " + predicate
	}
	var count int64
	if err := ss.db.QueryRowContext(context.Background(), query, args...).Scan(&count); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return count
}

func retirementReadWitness(
	t *testing.T,
	st store.Store,
	tenant model.TenantID,
	ref store.DirectoryPrincipalRef,
) (store.DirectoryTombstoneWitness, bool, error) {
	t.Helper()
	var witness store.DirectoryTombstoneWitness
	var found bool
	err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		reader, ok := sc.(store.DirectorySnapshotReader)
		if !ok {
			return errors.New("scope lacks DirectorySnapshotReader")
		}
		var err error
		witness, found, err = reader.ReadDirectoryTombstone(context.Background(), ref)
		return err
	})
	return witness, found, err
}

func retirementSeedUserTombstone(
	t *testing.T,
	h directoryRetirementSQLiteHarness,
	tenant model.TenantID,
	userID model.ID,
	validAnchor bool,
) model.UserTombstone {
	t.Helper()
	epoch := directoryWriterTestEpoch(t, h.raw, tenant).Version
	evidence, err := model.NewDirectoryEpochEvidence(map[model.TenantID]int64{tenant: epoch})
	if err != nil {
		t.Fatalf("build seeded User epoch evidence: %v", err)
	}
	var tombstone model.UserTombstone
	err = h.raw.System(context.Background(), func(scope store.SystemScope) error {
		sys := scope.(*systemScope)
		if err := sys.bindFor(context.Background(), model.SystemTenantID); err != nil {
			return err
		}
		now, err := directoryTransactionNow(context.Background(), sys.tx, sys.s.dia)
		if err != nil {
			return err
		}
		tombstoneID := model.NewID()
		anchor := model.RetirementAuditAnchor{
			EventID: model.NewID(), Seq: 1, Hash: bytes.Repeat([]byte{0x5a}, 32),
			Action:     model.AuditActionUserRetire,
			TargetKind: model.UserTombstoneKind, TargetID: tombstoneID,
		}
		if validAnchor {
			draft := model.AuditDraft{
				Actor: retirementTestActor, ActorKind: model.ActorUser,
				Action:     model.AuditActionUserRetire,
				TargetKind: model.UserTombstoneKind, TargetID: tombstoneID,
			}
			event, err := sys.auditLogFor(model.SystemTenantID).Append(context.Background(), draft)
			if err != nil {
				return err
			}
			if err := requireSystemAuditEvent(
				context.Background(), sys.tx, sys.s.dia,
				event, model.SystemTenantID, draft,
			); err != nil {
				return err
			}
			anchor = retirementAuditAnchor(event)
		}
		tombstone = model.UserTombstone{
			BaseFields: model.BaseFields{
				ID: tombstoneID, TenantID: model.SystemTenantID,
				CreatedAt: now, UpdatedAt: now, Version: 1,
			},
			PrincipalKind: model.DirectoryPrincipalUser, PrincipalRef: userID,
			SourceKind: userDescriptor.Kind, SourceID: userID,
			ResultingEpochs: evidence, Cause: model.DirectoryCauseUserErased,
			Actor: retirementTestActor, RetiredAt: now, AuditAnchor: anchor,
		}
		if err := insertUserRetirementTombstone(context.Background(), sys, tombstone); err != nil {
			return err
		}
		return restoreSystemDirectoryBaseline(context.Background(), sys.tx, sys.s.dia)
	})
	if err != nil {
		t.Fatalf("seed User tombstone: %v", err)
	}
	return tombstone
}

func retirementSeedDirectoryTombstone(
	t *testing.T,
	h directoryRetirementSQLiteHarness,
	tenant model.TenantID,
	kind model.DirectoryPrincipalKind,
	principal model.ID,
	workspace model.ID,
	source model.ID,
) model.DirectoryTombstone {
	t.Helper()
	epoch := directoryWriterTestEpoch(t, h.raw, tenant).Version
	var tombstone model.DirectoryTombstone
	err := h.raw.System(context.Background(), func(scope store.SystemScope) error {
		sys := scope.(*systemScope)
		if err := sys.bindFor(context.Background(), tenant); err != nil {
			return err
		}
		now, err := directoryTransactionNow(context.Background(), sys.tx, sys.s.dia)
		if err != nil {
			return err
		}
		tombstoneID := model.NewID()
		draft := model.AuditDraft{
			Actor: retirementTestActor, ActorKind: model.ActorUser,
			Action:     model.AuditActionDirectoryPrincipalRetire,
			TargetKind: model.DirectoryTombstoneKind, TargetID: tombstoneID,
		}
		event, err := sys.auditLogFor(tenant).Append(context.Background(), draft)
		if err != nil {
			return err
		}
		if err := requireSystemAuditEvent(
			context.Background(), sys.tx, sys.s.dia, event, tenant, draft,
		); err != nil {
			return err
		}
		sourceKind := identityDescriptor.Kind
		cause := model.DirectoryCauseIdentityRetired
		if kind == model.DirectoryPrincipalAgent {
			sourceKind = agentDescriptor.Kind
			cause = model.DirectoryCauseAgentRetired
		}
		tombstone = model.DirectoryTombstone{
			BaseFields: model.BaseFields{
				ID: tombstoneID, TenantID: tenant,
				CreatedAt: now, UpdatedAt: now, Version: 1,
			},
			PrincipalKind: kind, PrincipalRef: principal, WorkspaceRef: workspace,
			SourceKind: sourceKind, SourceID: source, ResultingEpoch: epoch,
			Cause: cause, Actor: retirementTestActor, RetiredAt: now,
			AuditAnchor: retirementAuditAnchor(event),
		}
		if err := insertDirectoryRetirementTombstone(
			context.Background(), sys, tombstone,
		); err != nil {
			return err
		}
		return restoreSystemDirectoryBaseline(context.Background(), sys.tx, sys.s.dia)
	})
	if err != nil {
		t.Fatalf("seed directory tombstone: %v", err)
	}
	return tombstone
}

func retirementWantMissing[T any](
	t *testing.T,
	name string,
	read func() (T, error),
) {
	t.Helper()
	if _, err := read(); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("%s read error = %v, want ErrNotFound", name, err)
	}
}

func TestDirectoryRetirementPublicRepositoriesDoNotExposeHardDeleteSQLite(t *testing.T) {
	ctx := context.Background()
	h := newDirectoryRetirementSQLiteHarness(t, store.Config{})
	tenant := provisionTenant(t, h.raw, "retirement-public-seam")

	if err := h.raw.AuthView(ctx, func(auth store.AuthScope) error {
		if _, ok := any(auth.Users()).(interface {
			Delete(context.Context, model.ID) error
		}); ok {
			t.Fatal("Users concrete wrapper re-exposes Delete")
		}
		if _, ok := any(auth.Users()).(store.Repository[model.User]); ok {
			t.Fatal("Users concrete wrapper re-exposes store.Repository")
		}
		return nil
	}); err != nil {
		t.Fatalf("inspect Users repository: %v", err)
	}
	if err := h.raw.View(ctx, tenant, func(sc store.Scope) error {
		if _, ok := any(sc.Identities()).(interface {
			Delete(context.Context, model.ID) error
		}); ok {
			t.Fatal("Identities concrete wrapper re-exposes Delete")
		}
		if _, ok := any(sc.Identities()).(store.Repository[model.Identity]); ok {
			t.Fatal("Identities concrete wrapper re-exposes store.Repository")
		}
		return nil
	}); err != nil {
		t.Fatalf("inspect Identities repository: %v", err)
	}

	agent := retirementCreateAgent(
		t, h.raw, tenant, "", "", "soft-delete-remains-reversible", model.StatusActive,
	)
	if err := h.raw.Mutate(ctx, tenant, func(sc store.Scope) error {
		return sc.Agents().Delete(ctx, agent.ID)
	}); err != nil {
		t.Fatalf("soft delete Agent: %v", err)
	}
	if got := retirementRowCount(
		t, h.sql, agentDescriptor.Table,
		"tenant_id = ? AND id = ? AND deleted_at IS NOT NULL",
		tenant.String(), agent.ID.String(),
	); got != 1 {
		t.Fatalf("physical soft-deleted Agent rows = %d, want 1", got)
	}
}

func TestDirectoryRetirementRefusesStagedControlWithoutMutationSQLite(t *testing.T) {
	ctx := context.Background()
	h := newDirectoryRetirementSQLiteHarness(t, store.Config{})
	tenant := provisionTenant(t, h.raw, "retirement-staged")
	identity := retirementCreateIdentity(t, h.raw, tenant, "staged-identity")
	beforeEpoch := directoryWriterTestEpoch(t, h.raw, tenant).Version
	beforeAudit := retirementRowCount(
		t, h.sql, auditTable, "tenant_id = ?", tenant.String(),
	)

	_, err := RetireDirectoryPrincipal(ctx, h.raw, DirectoryPrincipalRetirementRequest{
		TenantID: tenant, PrincipalKind: model.DirectoryPrincipalIdentity,
		SourceID: identity.ID, ExpectedVersion: identity.Version,
		Actor: retirementTestActor, ActorKind: model.ActorUser,
	})
	if !errors.Is(err, store.ErrDirectoryRetirementNotEnforced) {
		t.Fatalf("staged retirement error = %v, want ErrDirectoryRetirementNotEnforced", err)
	}
	if got := directoryWriterTestEpoch(t, h.raw, tenant).Version; got != beforeEpoch {
		t.Fatalf("staged retirement epoch = %d, want %d", got, beforeEpoch)
	}
	if got := retirementRowCount(
		t, h.sql, identityDescriptor.Table, "tenant_id = ? AND id = ?",
		tenant.String(), identity.ID.String(),
	); got != 1 {
		t.Fatalf("staged retirement source rows = %d, want 1", got)
	}
	if got := retirementRowCount(
		t, h.sql, directoryTombstoneDescriptor.Table, "tenant_id = ?",
		tenant.String(),
	); got != 0 {
		t.Fatalf("staged retirement tombstones = %d, want 0", got)
	}
	if got := retirementRowCount(
		t, h.sql, auditTable, "tenant_id = ?", tenant.String(),
	); got != beforeAudit {
		t.Fatalf("staged retirement audit rows = %d, want %d", got, beforeAudit)
	}
}

func TestDirectoryRetirementPinsExactBootAdminRole(t *testing.T) {
	if err := requirePinnedRetirementAdminRole(
		"olivares_admin", guardRoleFact{Role: "olivares_admin", Known: true},
	); err != nil {
		t.Fatalf("matching pinned AdminDSN role: %v", err)
	}
	for _, tc := range []struct {
		name string
		got  string
		boot guardRoleFact
	}{
		{
			name: "different-live-role", got: "alternate_admin",
			boot: guardRoleFact{Role: "olivares_admin", Known: true},
		},
		{name: "unknown-at-boot", got: "olivares_admin", boot: guardRoleFact{}},
		{
			name: "empty-boot-role", got: "olivares_admin",
			boot: guardRoleFact{Known: true},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := requirePinnedRetirementAdminRole(tc.got, tc.boot)
			if !errors.Is(err, store.ErrEnumerationNotAuthoritative) {
				t.Fatalf("AdminDSN role mutation = %v, want non-authoritative", err)
			}
		})
	}
}

func TestRetireIdentityDefinitiveReplayAndNoResurrectionSQLite(t *testing.T) {
	ctx := context.Background()
	h := newDirectoryRetirementSQLiteHarness(t, store.Config{})
	tenant := provisionTenant(t, h.raw, "retire-identity")
	identity := retirementCreateIdentity(t, h.raw, tenant, "identity-definitive")
	h.enforce(t)
	beforeEpoch := directoryWriterTestEpoch(t, h.raw, tenant).Version
	req := DirectoryPrincipalRetirementRequest{
		TenantID: tenant, PrincipalKind: model.DirectoryPrincipalIdentity,
		SourceID: identity.ID, ExpectedVersion: identity.Version,
		Actor: retirementTestActor, ActorKind: model.ActorUser,
	}

	result, err := RetireDirectoryPrincipal(ctx, h.raw, req)
	if err != nil {
		t.Fatalf("Retire Identity: %v", err)
	}
	if !result.Definitive || result.Code != DirectoryRetirementDefinitive ||
		result.Tombstone == nil || result.Principal.PrincipalRef != identity.ID ||
		result.Principal.PrincipalKind != model.DirectoryPrincipalIdentity ||
		!result.Principal.WorkspaceRef.IsZero() || result.AuditSeq < 1 ||
		len(result.AuditHash) != 32 {
		t.Fatalf("Identity retirement result = %+v", result)
	}
	if err := result.Tombstone.Validate(); err != nil {
		t.Fatalf("Identity tombstone Validate: %v", err)
	}
	if result.ResultingEpoch != beforeEpoch+1 {
		t.Fatalf("Identity resulting epoch = %d, want %d", result.ResultingEpoch, beforeEpoch+1)
	}
	if got := retirementRowCount(
		t, h.sql, identityDescriptor.Table, "tenant_id = ? AND id = ?",
		tenant.String(), identity.ID.String(),
	); got != 0 {
		t.Fatalf("retired Identity source rows = %d, want 0", got)
	}
	witness, found, err := retirementReadWitness(t, h.raw, tenant, result.Principal)
	if err != nil || !found || witness.TombstoneID != result.Tombstone.ID ||
		witness.RetirementEpoch != result.ResultingEpoch {
		t.Fatalf("Identity witness = %+v found=%t err=%v", witness, found, err)
	}

	replayed, err := RetireDirectoryPrincipal(ctx, h.raw, req)
	if err != nil {
		t.Fatalf("replay Identity retirement: %v", err)
	}
	if !reflect.DeepEqual(replayed, result) {
		t.Fatalf("Identity replay = %+v, want %+v", replayed, result)
	}
	if got := directoryWriterTestEpoch(t, h.raw, tenant).Version; got != result.ResultingEpoch {
		t.Fatalf("Identity replay bumped epoch to %d, want %d", got, result.ResultingEpoch)
	}

	err = h.raw.Mutate(ctx, tenant, func(sc store.Scope) error {
		_, createErr := sc.Agents().Create(ctx, model.Agent{
			Name: "cannot-bind-retired-identity", Kind: "assistant",
			Status: model.StatusActive, IdentityID: identity.ID,
		})
		return createErr
	})
	if !errors.Is(err, store.ErrDirectoryPrincipalRetired) {
		t.Fatalf("Agent Create after Identity retirement = %v, want retired", err)
	}
}

func TestRetireIdentityRefusesRecoverableAgentBindingAtomicallySQLite(t *testing.T) {
	ctx := context.Background()
	h := newDirectoryRetirementSQLiteHarness(t, store.Config{})
	tenant := provisionTenant(t, h.raw, "retire-identity-binding")
	identity := retirementCreateIdentity(t, h.raw, tenant, "identity-with-agent")
	agent := retirementCreateAgent(
		t, h.raw, tenant, identity.ID, "", "identity-binding", model.StatusActive,
	)
	h.enforce(t)
	beforeEpoch := directoryWriterTestEpoch(t, h.raw, tenant).Version
	beforeAudit := retirementRowCount(
		t, h.sql, auditTable, "tenant_id = ?", tenant.String(),
	)

	_, err := RetireDirectoryPrincipal(ctx, h.raw, DirectoryPrincipalRetirementRequest{
		TenantID: tenant, PrincipalKind: model.DirectoryPrincipalIdentity,
		SourceID: identity.ID, ExpectedVersion: identity.Version,
		Actor: retirementTestActor, ActorKind: model.ActorUser,
	})
	if !errors.Is(err, store.ErrDirectoryPrincipalHasBindings) {
		t.Fatalf("Identity with Agent retirement = %v, want recoverable bindings", err)
	}
	if got := directoryWriterTestEpoch(t, h.raw, tenant).Version; got != beforeEpoch {
		t.Fatalf("refused Identity retirement epoch = %d, want %d", got, beforeEpoch)
	}
	if got := retirementRowCount(
		t, h.sql, identityDescriptor.Table, "tenant_id = ? AND id = ?",
		tenant.String(), identity.ID.String(),
	); got != 1 {
		t.Fatalf("refused Identity source rows = %d, want 1", got)
	}
	if got := retirementRowCount(
		t, h.sql, agentDescriptor.Table, "tenant_id = ? AND id = ?",
		tenant.String(), agent.ID.String(),
	); got != 1 {
		t.Fatalf("refused Identity Agent rows = %d, want 1", got)
	}
	if got := retirementRowCount(
		t, h.sql, auditTable, "tenant_id = ?", tenant.String(),
	); got != beforeAudit {
		t.Fatalf("refused Identity audit rows = %d, want %d", got, beforeAudit)
	}
}

func TestRetireIdentityAgentBindingInterleavingsSQLite(t *testing.T) {
	t.Run("retirement-wins-agent-create-refused", func(t *testing.T) {
		h := newDirectoryRetirementSQLiteHarness(t, store.Config{})
		tenant := provisionTenant(t, h.raw, "retire-identity-race-retire-first")
		identity := retirementCreateIdentity(t, h.raw, tenant, "identity-race-retire-first")
		h.enforce(t)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		auditReached := make(chan struct{})
		releaseRetirement := make(chan struct{})
		directoryRetirementAfterAuditTestHook = func(*model.AuditEvent) {
			close(auditReached)
			<-releaseRetirement
		}
		t.Cleanup(func() { directoryRetirementAfterAuditTestHook = nil })
		retireDone := make(chan error, 1)
		go func() {
			_, err := RetireDirectoryPrincipal(ctx, h.raw, DirectoryPrincipalRetirementRequest{
				TenantID: tenant, PrincipalKind: model.DirectoryPrincipalIdentity,
				SourceID: identity.ID, ExpectedVersion: identity.Version,
				Actor: retirementTestActor, ActorKind: model.ActorUser,
			})
			retireDone <- err
		}()
		select {
		case <-auditReached:
		case <-ctx.Done():
			t.Fatalf("Identity retirement did not reach held audit: %v", ctx.Err())
		}
		agentDone := make(chan error, 1)
		go func() {
			agentDone <- h.raw.Mutate(ctx, tenant, func(sc store.Scope) error {
				_, err := sc.Agents().Create(ctx, model.Agent{
					Name: "stale-identity-agent", Kind: "assistant",
					Status: model.StatusActive, IdentityID: identity.ID,
				})
				return err
			})
		}()
		select {
		case err := <-agentDone:
			t.Fatalf("Agent Create crossed held Identity retirement: %v", err)
		case <-time.After(100 * time.Millisecond):
		}
		close(releaseRetirement)
		select {
		case err := <-retireDone:
			if err != nil {
				t.Fatalf("Identity retirement winner: %v", err)
			}
		case <-ctx.Done():
			t.Fatalf("Identity retirement winner deadlocked: %v", ctx.Err())
		}
		select {
		case err := <-agentDone:
			if !errors.Is(err, store.ErrDirectoryPrincipalRetired) {
				t.Fatalf("Agent Create after Identity retirement = %v, want retired", err)
			}
		case <-ctx.Done():
			t.Fatalf("Agent Create waiter deadlocked: %v", ctx.Err())
		}
		if got := retirementRowCount(
			t, h.sql, agentDescriptor.Table, "tenant_id = ?", tenant.String(),
		); got != 0 {
			t.Fatalf("retirement-first Agent rows = %d, want 0", got)
		}
	})

	t.Run("agent-create-wins-identity-retirement-refused", func(t *testing.T) {
		h := newDirectoryRetirementSQLiteHarness(t, store.Config{})
		tenant := provisionTenant(t, h.raw, "retire-identity-race-agent-first")
		identity := retirementCreateIdentity(t, h.raw, tenant, "identity-race-agent-first")
		h.enforce(t)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		beforeEpoch := directoryWriterTestEpoch(t, h.raw, tenant).Version
		writerReady := make(chan struct{})
		releaseWriter := make(chan struct{})
		var once sync.Once
		directoryWriterBeforeSourceTestHook = func(
			context.Context,
			*directoryWriteTracker,
		) error {
			once.Do(func() {
				close(writerReady)
				<-releaseWriter
			})
			return nil
		}
		t.Cleanup(func() { directoryWriterBeforeSourceTestHook = nil })
		agentDone := make(chan error, 1)
		go func() {
			agentDone <- h.raw.Mutate(ctx, tenant, func(sc store.Scope) error {
				_, err := sc.Agents().Create(ctx, model.Agent{
					Name: "agent-first-binding", Kind: "assistant",
					Status: model.StatusActive, IdentityID: identity.ID,
				})
				return err
			})
		}()
		select {
		case <-writerReady:
		case <-ctx.Done():
			t.Fatalf("Agent writer did not reach held source point: %v", ctx.Err())
		}
		retireDone := make(chan error, 1)
		go func() {
			_, err := RetireDirectoryPrincipal(ctx, h.raw, DirectoryPrincipalRetirementRequest{
				TenantID: tenant, PrincipalKind: model.DirectoryPrincipalIdentity,
				SourceID: identity.ID, ExpectedVersion: identity.Version,
				Actor: retirementTestActor, ActorKind: model.ActorUser,
			})
			retireDone <- err
		}()
		select {
		case err := <-retireDone:
			t.Fatalf("Identity retirement crossed held Agent writer: %v", err)
		case <-time.After(100 * time.Millisecond):
		}
		close(releaseWriter)
		select {
		case err := <-agentDone:
			if err != nil {
				t.Fatalf("Agent-first writer: %v", err)
			}
		case <-ctx.Done():
			t.Fatalf("Agent-first writer deadlocked: %v", ctx.Err())
		}
		select {
		case err := <-retireDone:
			if !errors.Is(err, store.ErrDirectoryPrincipalHasBindings) {
				t.Fatalf("Identity retirement after Agent commit = %v, want bindings refusal", err)
			}
		case <-ctx.Done():
			t.Fatalf("Identity retirement after Agent commit deadlocked: %v", ctx.Err())
		}
		if got := directoryWriterTestEpoch(t, h.raw, tenant).Version; got != beforeEpoch+1 {
			t.Fatalf("Agent-first epoch = %d, want %d", got, beforeEpoch+1)
		}
		if got := retirementRowCount(
			t, h.sql, identityDescriptor.Table,
			"tenant_id = ? AND id = ?", tenant.String(), identity.ID.String(),
		); got != 1 {
			t.Fatalf("Agent-first Identity source rows = %d, want 1", got)
		}
		if got := retirementRowCount(
			t, h.sql, agentDescriptor.Table,
			"tenant_id = ? AND identity_id = ?", tenant.String(), identity.ID.String(),
		); got != 1 {
			t.Fatalf("Agent-first binding rows = %d, want 1", got)
		}
	})
}

func TestRetireAgentLastBindingReplayAndNoResurrectionSQLite(t *testing.T) {
	ctx := context.Background()
	h := newDirectoryRetirementSQLiteHarness(t, store.Config{})
	tenant := provisionTenant(t, h.raw, "retire-agent-last")
	identity := retirementCreateIdentity(t, h.raw, tenant, "agent-last-identity")
	agent := retirementCreateAgent(
		t, h.raw, tenant, identity.ID, "", "agent-last", model.StatusActive,
	)
	workspace := retirementDefaultWorkspace(t, h.raw, tenant)
	h.enforce(t)
	beforeEpoch := directoryWriterTestEpoch(t, h.raw, tenant).Version
	req := DirectoryPrincipalRetirementRequest{
		TenantID: tenant, PrincipalKind: model.DirectoryPrincipalAgent,
		SourceID: agent.ID, ExpectedVersion: agent.Version,
		Actor: retirementTestActor, ActorKind: model.ActorUser,
	}

	result, err := RetireDirectoryPrincipal(ctx, h.raw, req)
	if err != nil {
		t.Fatalf("Retire last Agent binding: %v", err)
	}
	if !result.Definitive || result.Code != DirectoryRetirementDefinitive ||
		result.Tombstone == nil || result.Principal.PrincipalRef != identity.ID ||
		result.Principal.WorkspaceRef != workspace.ID || result.AuditSeq < 1 ||
		result.ResultingEpoch != beforeEpoch+1 {
		t.Fatalf("last Agent retirement result = %+v", result)
	}
	if err := result.Tombstone.Validate(); err != nil {
		t.Fatalf("last Agent tombstone Validate: %v", err)
	}
	replayed, err := RetireDirectoryPrincipal(ctx, h.raw, req)
	if err != nil || !reflect.DeepEqual(replayed, result) {
		t.Fatalf("last Agent replay = %+v err=%v, want %+v", replayed, err, result)
	}
	if got := directoryWriterTestEpoch(t, h.raw, tenant).Version; got != result.ResultingEpoch {
		t.Fatalf("last Agent replay bumped epoch to %d, want %d", got, result.ResultingEpoch)
	}

	err = h.raw.Mutate(ctx, tenant, func(sc store.Scope) error {
		_, createErr := sc.Agents().Create(ctx, model.Agent{
			Name: "agent-resurrection", Kind: "assistant", Status: model.StatusActive,
			IdentityID: identity.ID,
		})
		return createErr
	})
	if !errors.Is(err, store.ErrDirectoryPrincipalRetired) {
		t.Fatalf("Agent Create after definitive retirement = %v, want retired", err)
	}

	unbound := retirementCreateAgent(
		t, h.raw, tenant, "", "", "agent-update-source", model.StatusActive,
	)
	unbound.IdentityID = identity.ID
	err = h.raw.Mutate(ctx, tenant, func(sc store.Scope) error {
		_, updateErr := sc.Agents().Update(ctx, unbound)
		return updateErr
	})
	if !errors.Is(err, store.ErrDirectoryPrincipalRetired) {
		t.Fatalf("Agent Update to retired principal = %v, want retired", err)
	}
}

func TestRetiredAgentDefaultWorkspaceCannotBeReassignedOrResurrectedSQLite(t *testing.T) {
	h := newDirectoryRetirementSQLiteHarness(t, store.Config{})
	tenant := provisionTenant(t, h.raw, "retire-agent-default-workspace-stability")
	identity := retirementCreateIdentity(t, h.raw, tenant, "default-workspace-stable-identity")
	agent := retirementCreateAgent(
		t, h.raw, tenant, identity.ID, "", "default-workspace-stable-agent", model.StatusActive,
	)
	defaultWorkspace := retirementDefaultWorkspace(t, h.raw, tenant)
	// The alternate workspace has to be seeded BEFORE enforcement is activated -- once the
	// directory is enforced the create is refused -- so this seed is setup, not subject. It
	// runs on its own context for the same reason every other helper in this file does: the
	// 5s budget below exists to bound the retirement interleaving, and setup charged to it
	// turns a slow box into a false red. Nine budgets in this file opened before their own
	// setup; eight were moved behind h.enforce and this one kept the seed inside.
	var otherWorkspace model.Workspace
	if err := h.raw.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		var err error
		otherWorkspace, err = sc.Workspaces().Create(context.Background(), model.Workspace{
			Name: "Other", Slug: "other", Status: model.StatusActive,
		})
		return err
	}); err != nil {
		t.Fatalf("seed alternate workspace: %v", err)
	}
	h.enforce(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := RetireDirectoryPrincipal(ctx, h.raw, DirectoryPrincipalRetirementRequest{
		TenantID: tenant, PrincipalKind: model.DirectoryPrincipalAgent,
		SourceID: agent.ID, ExpectedVersion: agent.Version,
		Actor: retirementTestActor, ActorKind: model.ActorUser,
	})
	if err != nil || !result.Definitive || result.Principal.WorkspaceRef != defaultWorkspace.ID {
		t.Fatalf("retire zero-workspace Agent = %+v err=%v", result, err)
	}

	// Race both halves of a would-be default A→B reassignment. Each public
	// mutation is independently refused, so no scheduling order can expose a
	// different effective principal to an Agent writer.
	start := make(chan struct{})
	done := make(chan error, 2)
	for _, candidate := range []model.Workspace{
		func() model.Workspace {
			updated := defaultWorkspace
			updated.Slug = "former-default"
			return updated
		}(),
		func() model.Workspace {
			updated := otherWorkspace
			updated.Slug = model.DefaultWorkspaceSlug
			return updated
		}(),
	} {
		candidate := candidate
		go func() {
			<-start
			done <- h.raw.Mutate(ctx, tenant, func(sc store.Scope) error {
				_, err := sc.Workspaces().Update(ctx, candidate)
				return err
			})
		}()
	}
	close(start)
	for i := 0; i < 2; i++ {
		select {
		case err := <-done:
			if !errors.Is(err, store.ErrConflict) ||
				!strings.Contains(err.Error(), "default workspace assignment is immutable") {
				t.Fatalf("default reassignment race = %v, want immutable conflict", err)
			}
		case <-ctx.Done():
			t.Fatalf("default reassignment race did not finish: %v", ctx.Err())
		}
	}
	for name, mutate := range map[string]func(store.Scope) error{
		"delete-default": func(sc store.Scope) error {
			return sc.Workspaces().Delete(ctx, defaultWorkspace.ID)
		},
		"create-default": func(sc store.Scope) error {
			_, err := sc.Workspaces().Create(ctx, model.Workspace{
				Name: "Replacement", Slug: model.DefaultWorkspaceSlug, Status: model.StatusActive,
			})
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := h.raw.Mutate(ctx, tenant, mutate)
			if !errors.Is(err, store.ErrConflict) {
				t.Fatalf("reserved default mutation = %v, want conflict", err)
			}
		})
	}
	var discarded error
	err = h.raw.Mutate(ctx, tenant, func(sc store.Scope) error {
		candidate := defaultWorkspace
		candidate.Slug = "discarded-default-rename"
		_, discarded = sc.Workspaces().Update(ctx, candidate)
		return nil
	})
	if !errors.Is(discarded, store.ErrConflict) || !errors.Is(err, store.ErrConflict) ||
		!strings.Contains(err.Error(), "poisoned") {
		t.Fatalf("discarded default mutation=%v outer=%v, want poisoned conflict", discarded, err)
	}
	if got := retirementDefaultWorkspace(t, h.raw, tenant); got.ID != defaultWorkspace.ID ||
		got.Slug != model.DefaultWorkspaceSlug {
		t.Fatalf("default workspace assignment changed: %+v want ID=%s", got, defaultWorkspace.ID)
	}
	err = h.raw.Mutate(ctx, tenant, func(sc store.Scope) error {
		_, createErr := sc.Agents().Create(ctx, model.Agent{
			Name: "zero-workspace-resurrection", Kind: "assistant",
			Status: model.StatusActive, IdentityID: identity.ID,
		})
		return createErr
	})
	if !errors.Is(err, store.ErrDirectoryPrincipalRetired) {
		t.Fatalf("zero-workspace Agent resurrection = %v, want retired", err)
	}
	witness, found, err := retirementReadWitness(t, h.raw, tenant, result.Principal)
	if err != nil || !found || witness.TombstoneID != result.Tombstone.ID {
		t.Fatalf("stable default tombstone witness=%+v found=%t err=%v", witness, found, err)
	}
}

func TestRetireAgentNonLastBindingHasAuditReceiptAndNoTombstoneSQLite(t *testing.T) {
	for _, siblingState := range []struct {
		name       string
		status     model.LifecycleStatus
		softDelete bool
	}{
		{name: "active", status: model.StatusActive},
		{name: "inactive", status: model.StatusInactive},
		{name: "soft-deleted", status: model.StatusActive, softDelete: true},
	} {
		t.Run(siblingState.name, func(t *testing.T) {
			ctx := context.Background()
			h := newDirectoryRetirementSQLiteHarness(t, store.Config{})
			tenant := provisionTenant(t, h.raw, "retire-agent-sibling-"+siblingState.name)
			identity := retirementCreateIdentity(t, h.raw, tenant, "sibling-identity")
			target := retirementCreateAgent(
				t, h.raw, tenant, identity.ID, "", "sibling-target", model.StatusActive,
			)
			sibling := retirementCreateAgent(
				t, h.raw, tenant, identity.ID, "", "sibling-remains", siblingState.status,
			)
			if siblingState.softDelete {
				if err := h.raw.Mutate(ctx, tenant, func(sc store.Scope) error {
					return sc.Agents().Delete(ctx, sibling.ID)
				}); err != nil {
					t.Fatalf("soft delete sibling: %v", err)
				}
			}
			h.enforce(t)
			beforeEpoch := directoryWriterTestEpoch(t, h.raw, tenant).Version
			req := DirectoryPrincipalRetirementRequest{
				TenantID: tenant, PrincipalKind: model.DirectoryPrincipalAgent,
				SourceID: target.ID, ExpectedVersion: target.Version,
				Actor: retirementTestActor, ActorKind: model.ActorUser,
			}

			result, err := RetireDirectoryPrincipal(ctx, h.raw, req)
			if err != nil {
				t.Fatalf("Retire non-last Agent binding: %v", err)
			}
			if result.Definitive || result.Code != DirectoryRetirementAgentBindingRemains ||
				result.Tombstone != nil || result.AuditSeq < 1 || len(result.AuditHash) != 32 ||
				result.ResultingEpoch != beforeEpoch+1 {
				t.Fatalf("non-last Agent retirement result = %+v", result)
			}
			if got := retirementRowCount(
				t, h.sql, agentDescriptor.Table, "tenant_id = ? AND id = ?",
				tenant.String(), target.ID.String(),
			); got != 0 {
				t.Fatalf("non-last retired source rows = %d, want 0", got)
			}
			if got := retirementRowCount(
				t, h.sql, agentDescriptor.Table, "tenant_id = ? AND id = ?",
				tenant.String(), sibling.ID.String(),
			); got != 1 {
				t.Fatalf("recoverable sibling rows = %d, want 1", got)
			}
			if got := retirementRowCount(
				t, h.sql, directoryTombstoneDescriptor.Table,
				"tenant_id = ? AND source_id = ?", tenant.String(), target.ID.String(),
			); got != 0 {
				t.Fatalf("non-last principal tombstones = %d, want 0", got)
			}
			var action, meta string
			if err := h.sql.db.QueryRowContext(ctx, `
SELECT action, meta FROM main.audit_events
WHERE tenant_id = ? AND id = ?`, tenant.String(), result.AuditEventID.String()).Scan(
				&action, &meta,
			); err != nil {
				t.Fatalf("read non-last audit receipt: %v", err)
			}
			if action != model.AuditActionAgentBindingRetire ||
				strings.Contains(meta, string(model.DirectoryCauseAgentRetired)) {
				t.Fatalf("non-last audit action=%q meta=%s", action, meta)
			}

			replayed, err := RetireDirectoryPrincipal(ctx, h.raw, req)
			if err != nil || !reflect.DeepEqual(replayed, result) {
				t.Fatalf("non-last replay = %+v err=%v, want %+v", replayed, err, result)
			}
			if got := directoryWriterTestEpoch(t, h.raw, tenant).Version; got != result.ResultingEpoch {
				t.Fatalf("non-last replay bumped epoch to %d, want %d", got, result.ResultingEpoch)
			}
		})
	}
}

func TestRetireAgentConcurrentBindingsSerializeToOneReceiptAndOneTombstoneSQLite(t *testing.T) {
	h := newDirectoryRetirementSQLiteHarness(t, store.Config{})
	tenant := provisionTenant(t, h.raw, "retire-agent-concurrent-bindings")
	identity := retirementCreateIdentity(t, h.raw, tenant, "concurrent-agent-identity")
	agents := []model.Agent{
		retirementCreateAgent(
			t, h.raw, tenant, identity.ID, "", "concurrent-agent-a", model.StatusActive,
		),
		retirementCreateAgent(
			t, h.raw, tenant, identity.ID, "", "concurrent-agent-b", model.StatusInactive,
		),
	}
	h.enforce(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	beforeEpoch := directoryWriterTestEpoch(t, h.raw, tenant).Version
	type outcome struct {
		result DirectoryPrincipalRetirementResult
		err    error
	}
	start := make(chan struct{})
	done := make(chan outcome, len(agents))
	for _, agent := range agents {
		agent := agent
		go func() {
			<-start
			result, err := RetireDirectoryPrincipal(ctx, h.raw, DirectoryPrincipalRetirementRequest{
				TenantID: tenant, PrincipalKind: model.DirectoryPrincipalAgent,
				SourceID: agent.ID, ExpectedVersion: agent.Version,
				Actor: retirementTestActor, ActorKind: model.ActorUser,
			})
			done <- outcome{result: result, err: err}
		}()
	}
	close(start)
	var results []DirectoryPrincipalRetirementResult
	for range agents {
		select {
		case got := <-done:
			if got.err != nil {
				t.Fatalf("concurrent Agent retirement: %v", got.err)
			}
			results = append(results, got.result)
		case <-ctx.Done():
			t.Fatalf("concurrent Agent retirement deadlocked: %v", ctx.Err())
		}
	}
	definitive, binding := 0, 0
	for _, result := range results {
		switch {
		case result.Definitive && result.Code == DirectoryRetirementDefinitive && result.Tombstone != nil:
			definitive++
		case !result.Definitive && result.Code == DirectoryRetirementAgentBindingRemains && result.Tombstone == nil:
			binding++
		default:
			t.Fatalf("unexpected concurrent retirement result: %+v", result)
		}
	}
	if definitive != 1 || binding != 1 {
		t.Fatalf("concurrent results definitive=%d binding=%d, want 1/1: %+v",
			definitive, binding, results)
	}
	if got := directoryWriterTestEpoch(t, h.raw, tenant).Version; got != beforeEpoch+2 {
		t.Fatalf("concurrent Agent epoch = %d, want %d", got, beforeEpoch+2)
	}
	if got := retirementRowCount(
		t, h.sql, agentDescriptor.Table, "tenant_id = ?", tenant.String(),
	); got != 0 {
		t.Fatalf("concurrent Agent source rows = %d, want 0", got)
	}
	if got := retirementRowCount(
		t, h.sql, directoryTombstoneDescriptor.Table,
		"tenant_id = ? AND principal_kind = ? AND principal_ref = ?",
		tenant.String(), string(model.DirectoryPrincipalAgent), identity.ID.String(),
	); got != 1 {
		t.Fatalf("concurrent Agent tombstones = %d, want 1", got)
	}
	for action, want := range map[string]int64{
		model.AuditActionAgentBindingRetire:       1,
		model.AuditActionDirectoryPrincipalRetire: 1,
	} {
		if got := retirementRowCount(
			t, h.sql, auditTable, "tenant_id = ? AND action = ?", tenant.String(), action,
		); got != want {
			t.Fatalf("concurrent Agent audit action %q rows = %d, want %d", action, got, want)
		}
	}
}

func TestRetireAgentMalformedSiblingWorkspaceDeniesAndRollsBackSQLite(t *testing.T) {
	ctx := context.Background()
	h := newDirectoryRetirementSQLiteHarness(t, store.Config{})
	tenant := provisionTenant(t, h.raw, "retire-agent-malformed-sibling")
	identity := retirementCreateIdentity(t, h.raw, tenant, "malformed-sibling-identity")
	target := retirementCreateAgent(
		t, h.raw, tenant, identity.ID, "", "malformed-target", model.StatusActive,
	)
	sibling := retirementCreateAgent(
		t, h.raw, tenant, identity.ID, "", "malformed-sibling", model.StatusActive,
	)
	if _, err := h.sql.db.ExecContext(ctx, `
UPDATE main.agents SET workspace_id = 'not-a-canonical-workspace' WHERE id = ?`,
		sibling.ID.String(),
	); err != nil {
		t.Fatalf("seed malformed sibling workspace: %v", err)
	}
	h.enforce(t)
	beforeEpoch := directoryWriterTestEpoch(t, h.raw, tenant).Version
	beforeAudit := retirementRowCount(
		t, h.sql, auditTable, "tenant_id = ?", tenant.String(),
	)

	_, err := RetireDirectoryPrincipal(ctx, h.raw, DirectoryPrincipalRetirementRequest{
		TenantID: tenant, PrincipalKind: model.DirectoryPrincipalAgent,
		SourceID: target.ID, ExpectedVersion: target.Version,
		Actor: retirementTestActor, ActorKind: model.ActorUser,
	})
	if !errors.Is(err, store.ErrDirectoryUnavailable) {
		t.Fatalf("malformed sibling retirement = %v, want unavailable", err)
	}
	if got := directoryWriterTestEpoch(t, h.raw, tenant).Version; got != beforeEpoch {
		t.Fatalf("malformed sibling epoch = %d, want %d", got, beforeEpoch)
	}
	if got := retirementRowCount(
		t, h.sql, agentDescriptor.Table, "tenant_id = ? AND id = ?",
		tenant.String(), target.ID.String(),
	); got != 1 {
		t.Fatalf("malformed sibling target rows = %d, want 1", got)
	}
	if got := retirementRowCount(
		t, h.sql, auditTable, "tenant_id = ?", tenant.String(),
	); got != beforeAudit {
		t.Fatalf("malformed sibling audit rows = %d, want %d", got, beforeAudit)
	}
}

func TestRetireAgentNonCanonicalSiblingIdentityDeniesAndRollsBackSQLite(t *testing.T) {
	ctx := context.Background()
	h := newDirectoryRetirementSQLiteHarness(t, store.Config{})
	tenant := provisionTenant(t, h.raw, "retire-agent-noncanonical-identity")
	identity := retirementCreateIdentity(t, h.raw, tenant, "noncanonical-binding-identity")
	target := retirementCreateAgent(
		t, h.raw, tenant, identity.ID, "", "noncanonical-binding-target", model.StatusActive,
	)
	sibling := retirementCreateAgent(
		t, h.raw, tenant, identity.ID, "", "noncanonical-binding-sibling", model.StatusInactive,
	)
	alias := strings.ToUpper(identity.ID.String())
	if alias == identity.ID.String() {
		t.Fatal("UUID fixture unexpectedly has no hexadecimal letters")
	}
	if _, err := h.sql.db.ExecContext(ctx, `
UPDATE main.agents SET identity_id = ? WHERE tenant_id = ? AND id = ?`,
		alias, tenant.String(), sibling.ID.String(),
	); err != nil {
		t.Fatalf("seed non-canonical sibling identity alias: %v", err)
	}
	h.enforce(t)
	beforeEpoch := directoryWriterTestEpoch(t, h.raw, tenant).Version
	beforeAudit := retirementRowCount(t, h.sql, auditTable, "tenant_id = ?", tenant.String())
	_, err := RetireDirectoryPrincipal(ctx, h.raw, DirectoryPrincipalRetirementRequest{
		TenantID: tenant, PrincipalKind: model.DirectoryPrincipalAgent,
		SourceID: target.ID, ExpectedVersion: target.Version,
		Actor: retirementTestActor, ActorKind: model.ActorUser,
	})
	if !errors.Is(err, store.ErrDirectoryUnavailable) ||
		!strings.Contains(err.Error(), "Identity is illegible or non-canonical") {
		t.Fatalf("non-canonical sibling retirement = %v, want unavailable", err)
	}
	if got := directoryWriterTestEpoch(t, h.raw, tenant).Version; got != beforeEpoch {
		t.Fatalf("non-canonical sibling epoch = %d, want %d", got, beforeEpoch)
	}
	if got := retirementRowCount(
		t, h.sql, agentDescriptor.Table, "tenant_id = ? AND id = ?",
		tenant.String(), target.ID.String(),
	); got != 1 {
		t.Fatalf("non-canonical sibling target rows = %d, want 1", got)
	}
	if got := retirementRowCount(
		t, h.sql, auditTable, "tenant_id = ?", tenant.String(),
	); got != beforeAudit {
		t.Fatalf("non-canonical sibling audit rows = %d, want %d", got, beforeAudit)
	}
}

func TestRetireDirectoryPrincipalRollbackAfterDeleteTombstoneAndAuditSQLite(t *testing.T) {
	ctx := context.Background()
	h := newDirectoryRetirementSQLiteHarness(t, store.Config{})
	tenant := provisionTenant(t, h.raw, "retirement-rollback")
	identity := retirementCreateIdentity(t, h.raw, tenant, "rollback-identity")
	h.enforce(t)
	beforeEpoch := directoryWriterTestEpoch(t, h.raw, tenant).Version
	beforeAudit := retirementRowCount(
		t, h.sql, auditTable, "tenant_id = ?", tenant.String(),
	)
	forced := errors.New("forced rollback after retirement tuple")
	directoryRetirementBeforeFinishTestHook = func(
		kind model.DirectoryPrincipalKind,
		id model.ID,
	) error {
		if kind != model.DirectoryPrincipalIdentity || id != identity.ID {
			return fmt.Errorf("unexpected hook target %s/%s", kind, id)
		}
		return forced
	}
	t.Cleanup(func() { directoryRetirementBeforeFinishTestHook = nil })

	_, err := RetireDirectoryPrincipal(ctx, h.raw, DirectoryPrincipalRetirementRequest{
		TenantID: tenant, PrincipalKind: model.DirectoryPrincipalIdentity,
		SourceID: identity.ID, ExpectedVersion: identity.Version,
		Actor: retirementTestActor, ActorKind: model.ActorUser,
	})
	if !errors.Is(err, forced) {
		t.Fatalf("forced retirement rollback = %v, want sentinel", err)
	}
	if got := directoryWriterTestEpoch(t, h.raw, tenant).Version; got != beforeEpoch {
		t.Fatalf("rollback epoch = %d, want %d", got, beforeEpoch)
	}
	if got := retirementRowCount(
		t, h.sql, identityDescriptor.Table, "tenant_id = ? AND id = ?",
		tenant.String(), identity.ID.String(),
	); got != 1 {
		t.Fatalf("rollback source rows = %d, want 1", got)
	}
	if got := retirementRowCount(
		t, h.sql, directoryTombstoneDescriptor.Table, "tenant_id = ?",
		tenant.String(),
	); got != 0 {
		t.Fatalf("rollback tombstone rows = %d, want 0", got)
	}
	if got := retirementRowCount(
		t, h.sql, auditTable, "tenant_id = ?", tenant.String(),
	); got != beforeAudit {
		t.Fatalf("rollback audit rows = %d, want %d", got, beforeAudit)
	}
}

func TestRetireDirectoryPrincipalRejectsTamperedAuditAnchorSQLite(t *testing.T) {
	ctx := context.Background()
	h := newDirectoryRetirementSQLiteHarness(t, store.Config{})
	tenant := provisionTenant(t, h.raw, "retirement-audit-tamper")
	identity := retirementCreateIdentity(t, h.raw, tenant, "tampered-audit-identity")
	h.enforce(t)
	beforeEpoch := directoryWriterTestEpoch(t, h.raw, tenant).Version
	directoryRetirementAfterAuditTestHook = func(event *model.AuditEvent) {
		event.Hash = append([]byte(nil), event.Hash...)
		event.Hash[0] ^= 0xff
	}
	t.Cleanup(func() { directoryRetirementAfterAuditTestHook = nil })

	_, err := RetireDirectoryPrincipal(ctx, h.raw, DirectoryPrincipalRetirementRequest{
		TenantID: tenant, PrincipalKind: model.DirectoryPrincipalIdentity,
		SourceID: identity.ID, ExpectedVersion: identity.Version,
		Actor: retirementTestActor, ActorKind: model.ActorUser,
	})
	if !errors.Is(err, store.ErrDirectoryUnavailable) ||
		!strings.Contains(err.Error(), "absent or divergent") {
		t.Fatalf("tampered retirement audit error = %v, want unavailable divergence", err)
	}
	if got := directoryWriterTestEpoch(t, h.raw, tenant).Version; got != beforeEpoch {
		t.Fatalf("tampered audit epoch = %d, want %d", got, beforeEpoch)
	}
	if got := retirementRowCount(
		t, h.sql, identityDescriptor.Table, "tenant_id = ? AND id = ?",
		tenant.String(), identity.ID.String(),
	); got != 1 {
		t.Fatalf("tampered audit source rows = %d, want 1", got)
	}
}

func TestRetireDirectoryPrincipalAuditDegradeCannotCommitWithoutReceiptSQLite(t *testing.T) {
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "retirement-audit-degrade.db")
	initial := newDirectoryRetirementSQLiteHarness(t, store.Config{DSN: dsn})
	tenant := provisionTenant(t, initial.raw, "retirement-audit-degrade")
	identity := retirementCreateIdentity(t, initial.raw, tenant, "degraded-audit-identity")
	initial.enforce(t)
	beforeEpoch := directoryWriterTestEpoch(t, initial.raw, tenant).Version
	beforeAudit := retirementRowCount(
		t, initial.sql, auditTable, "tenant_id = ?", tenant.String(),
	)
	if err := initial.raw.Close(); err != nil {
		t.Fatalf("close initial retirement store: %v", err)
	}

	degraded := newDirectoryRetirementSQLiteHarness(t, store.Config{
		DSN: dsn, AuditSpoolMaxBytes: 1, AuditSpoolOnFull: store.AuditSpoolDegrade,
	})
	_, err := RetireDirectoryPrincipal(ctx, degraded.raw, DirectoryPrincipalRetirementRequest{
		TenantID: tenant, PrincipalKind: model.DirectoryPrincipalIdentity,
		SourceID: identity.ID, ExpectedVersion: identity.Version,
		Actor: retirementTestActor, ActorKind: model.ActorUser,
	})
	if !errors.Is(err, store.ErrAuditSpoolFull) {
		t.Fatalf("degraded retirement error = %v, want ErrAuditSpoolFull", err)
	}
	if got := directoryWriterTestEpoch(t, degraded.raw, tenant).Version; got != beforeEpoch {
		t.Fatalf("degraded retirement epoch = %d, want %d", got, beforeEpoch)
	}
	if got := retirementRowCount(
		t, degraded.sql, identityDescriptor.Table, "tenant_id = ? AND id = ?",
		tenant.String(), identity.ID.String(),
	); got != 1 {
		t.Fatalf("degraded retirement source rows = %d, want 1", got)
	}
	if got := retirementRowCount(
		t, degraded.sql, directoryTombstoneDescriptor.Table, "tenant_id = ?",
		tenant.String(),
	); got != 0 {
		t.Fatalf("degraded retirement tombstones = %d, want 0", got)
	}
	if got := retirementRowCount(
		t, degraded.sql, auditTable, "tenant_id = ?", tenant.String(),
	); got != beforeAudit {
		t.Fatalf("degraded retirement audit rows = %d, want %d", got, beforeAudit)
	}
}

func TestRetireDirectoryPrincipalDiscardedErrorPoisonsSystemTransactionSQLite(t *testing.T) {
	ctx := context.Background()
	h := newDirectoryRetirementSQLiteHarness(t, store.Config{})
	tenant := provisionTenant(t, h.raw, "retirement-poison")
	identity := retirementCreateIdentity(t, h.raw, tenant, "poison-identity")
	h.enforce(t)
	forced := errors.New("discarded retirement failure")
	directoryRetirementBeforeFinishTestHook = func(
		model.DirectoryPrincipalKind,
		model.ID,
	) error {
		return forced
	}
	t.Cleanup(func() { directoryRetirementBeforeFinishTestHook = nil })

	var discarded error
	err := h.raw.System(ctx, func(scope store.SystemScope) error {
		_, discarded = scope.(*systemScope).retireDirectoryPrincipal(
			ctx,
			DirectoryPrincipalRetirementRequest{
				TenantID: tenant, PrincipalKind: model.DirectoryPrincipalIdentity,
				SourceID: identity.ID, ExpectedVersion: identity.Version,
				Actor: retirementTestActor, ActorKind: model.ActorUser,
			},
		)
		return nil
	})
	if !errors.Is(discarded, forced) || !errors.Is(err, forced) ||
		!strings.Contains(err.Error(), "transaction poisoned") {
		t.Fatalf("discarded=%v outer=%v, want poisoned forced error", discarded, err)
	}
	if got := retirementRowCount(
		t, h.sql, identityDescriptor.Table, "tenant_id = ? AND id = ?",
		tenant.String(), identity.ID.String(),
	); got != 1 {
		t.Fatalf("poison rollback source rows = %d, want 1", got)
	}
}

func TestRetireDirectoryPrincipalIgnoresSQLiteTempShadows(t *testing.T) {
	ctx := context.Background()
	h := newDirectoryRetirementSQLiteHarness(t, store.Config{})
	tenant := provisionTenant(t, h.raw, "retirement-temp-shadows")
	identity := retirementCreateIdentity(t, h.raw, tenant, "temp-shadow-identity")
	h.enforce(t)
	for _, table := range []string{
		identityDescriptor.Table,
		directoryTombstoneDescriptor.Table,
		directoryEpochDescriptor.Table,
		auditTable,
	} {
		if _, err := h.sql.db.ExecContext(ctx,
			"CREATE TEMP TABLE "+quoteIdent(table)+" (marker TEXT NOT NULL)",
		); err != nil {
			t.Fatalf("create TEMP shadow %s: %v", table, err)
		}
		if _, err := h.sql.db.ExecContext(ctx,
			"INSERT INTO temp."+quoteIdent(table)+" (marker) VALUES ('shadow')",
		); err != nil {
			t.Fatalf("seed TEMP shadow %s: %v", table, err)
		}
	}

	result, err := RetireDirectoryPrincipal(ctx, h.raw, DirectoryPrincipalRetirementRequest{
		TenantID: tenant, PrincipalKind: model.DirectoryPrincipalIdentity,
		SourceID: identity.ID, ExpectedVersion: identity.Version,
		Actor: retirementTestActor, ActorKind: model.ActorUser,
	})
	if err != nil || !result.Definitive {
		t.Fatalf("retirement with TEMP shadows = %+v err=%v", result, err)
	}
	for _, table := range []string{
		identityDescriptor.Table,
		directoryTombstoneDescriptor.Table,
		directoryEpochDescriptor.Table,
		auditTable,
	} {
		var marker string
		if err := h.sql.db.QueryRowContext(
			ctx, "SELECT marker FROM temp."+quoteIdent(table),
		).Scan(&marker); err != nil || marker != "shadow" {
			t.Fatalf("TEMP shadow %s marker=%q err=%v", table, marker, err)
		}
	}
}

func TestRetireDirectoryPrincipalUsesDatabaseTimeSQLite(t *testing.T) {
	ctx := context.Background()
	fixed := model.NewTimestamp(time.Date(2001, time.February, 3, 4, 5, 6, 0, time.UTC))
	h := newDirectoryRetirementSQLiteHarness(t, store.Config{
		Clock: transactionClockFixedAppClock{now: fixed},
	})
	tenant := provisionTenant(t, h.raw, "retirement-database-time")
	identity := retirementCreateIdentity(t, h.raw, tenant, "database-time-identity")
	h.enforce(t)
	before := time.Now().UTC().Add(-time.Second)
	result, err := RetireDirectoryPrincipal(ctx, h.raw, DirectoryPrincipalRetirementRequest{
		TenantID: tenant, PrincipalKind: model.DirectoryPrincipalIdentity,
		SourceID: identity.ID, ExpectedVersion: identity.Version,
		Actor: retirementTestActor, ActorKind: model.ActorUser,
	})
	after := time.Now().UTC().Add(time.Second)
	if err != nil {
		t.Fatalf("database-time retirement: %v", err)
	}
	if result.Tombstone.RetiredAt.String() == fixed.String() ||
		result.Tombstone.RetiredAt.Time().Before(before) ||
		result.Tombstone.RetiredAt.Time().After(after) {
		t.Fatalf("retired_at = %s, fixed=%s range=[%s,%s]",
			result.Tombstone.RetiredAt.String(), fixed.String(), before, after)
	}
}

func TestDirectoryRetirementAuditHashesAreCopiedSQLite(t *testing.T) {
	// This tiny discriminator prevents a future result/tombstone refactor from
	// accidentally aliasing a mutable audit buffer after the transaction.
	h := newDirectoryRetirementSQLiteHarness(t, store.Config{})
	tenant := provisionTenant(t, h.raw, "retirement-hash-copy")
	identity := retirementCreateIdentity(t, h.raw, tenant, "hash-copy-identity")
	h.enforce(t)
	result, err := RetireDirectoryPrincipal(
		context.Background(), h.raw, DirectoryPrincipalRetirementRequest{
			TenantID: tenant, PrincipalKind: model.DirectoryPrincipalIdentity,
			SourceID: identity.ID, ExpectedVersion: identity.Version,
			Actor: retirementTestActor, ActorKind: model.ActorUser,
		},
	)
	if err != nil {
		t.Fatalf("retire hash-copy Identity: %v", err)
	}
	if !bytes.Equal(result.AuditHash, result.Tombstone.AuditAnchor.Hash) {
		t.Fatalf("result hash != tombstone anchor")
	}
	result.AuditHash[0] ^= 0xff
	if bytes.Equal(result.AuditHash, result.Tombstone.AuditAnchor.Hash) {
		t.Fatal("result audit hash aliases tombstone anchor")
	}
}

func TestRetireUserAllTenantsAuthorityCleanupReplayAndDatabaseTimeSQLite(t *testing.T) {
	ctx := context.Background()
	fixed := model.NewTimestamp(time.Date(2001, time.February, 3, 4, 5, 6, 0, time.UTC))
	h := newDirectoryRetirementSQLiteHarness(t, store.Config{
		Clock: transactionClockFixedAppClock{now: fixed},
	})
	tenantA := provisionTenant(t, h.raw, "retire-user-a")
	tenantB := provisionTenant(t, h.raw, "retire-user-b-no-membership")
	victim, err := directoryEpochTestSeedRetainedAuth(ctx, h.raw, "retire-user-victim")
	if err != nil {
		t.Fatalf("seed retiring User credentials: %v", err)
	}
	estate, err := directoryEpochTestSeedAuthEstate(
		ctx, h.raw, tenantA, "retire-user-estate", victim.user.ID,
	)
	if err != nil {
		t.Fatalf("seed retiring User tenant estate: %v", err)
	}
	owner := retirementCreateUser(t, h.raw, "retire-user-other-owner")

	var child, actAs model.APIToken
	var childHandle, sessionHandle, crossKindHandle model.DelegationHandle
	var crossKindSession model.AuthSession
	err = h.raw.AuthMutate(ctx, func(auth store.AuthScope) error {
		var err error
		child, err = auth.Tokens().Create(ctx, model.APIToken{
			Name: "descendant-with-other-owner", UserID: owner.ID,
			Selector: "retire-user-child", SecretHash: []byte("child-hash"),
			BoundTenantID: tenantA, Role: "viewer", ParentTokenID: estate.token.ID,
		})
		if err != nil {
			return err
		}
		actAs, err = auth.Tokens().Create(ctx, model.APIToken{
			Name: "act-as-retiring-user", UserID: owner.ID, ActAsUserID: victim.user.ID,
			Selector: "retire-user-act-as", SecretHash: []byte("act-as-hash"),
			BoundTenantID: tenantA, Role: "viewer",
		})
		if err != nil {
			return err
		}
		childHandle, err = auth.DelegationHandles().Create(ctx, model.DelegationHandle{
			TargetTenantID: tenantA, Selector: "retire-user-child-handle",
			SecretHash: []byte("child-handle-hash"), SourceCredKind: "token",
			SourceCredID: child.ID, SubjectUserID: owner.ID, MintRole: "viewer",
			PEPServiceID: estate.service.ID, Audience: estate.service.PDPAudience,
			Operations: []string{"messages"},
			ExpiresAt:  model.NewTimestamp(time.Now().UTC().Add(time.Hour)),
		})
		if err != nil {
			return err
		}
		// A legacy/anomalous cross-subject handle is still authority derived from
		// the victim's session and must be removed by source credential id.
		sessionHandle, err = auth.DelegationHandles().Create(ctx, model.DelegationHandle{
			TargetTenantID: tenantA, Selector: "retire-user-session-handle",
			SecretHash: []byte("session-handle-hash"), SourceCredKind: "user",
			SourceCredID: victim.session.ID, SubjectUserID: owner.ID, MintRole: "viewer",
			PEPServiceID: estate.service.ID, Audience: estate.service.PDPAudience,
			Operations: []string{"messages"},
			ExpiresAt:  model.NewTimestamp(time.Now().UTC().Add(time.Hour)),
		})
		if err != nil {
			return err
		}
		// Credential IDs are table-local. A live session may legitimately share
		// the UUID of the retiring User's token; kind must remain part of every
		// cleanup predicate so token retirement cannot destroy this user-session
		// handle belonging to another User.
		ts := auth.(*authScope).ts
		record, err := authSessionCodec.Encode(model.AuthSession{
			UserID: owner.ID, Selector: "cross-kind-session", SecretHash: []byte("cross-kind-hash"),
			ExpiresAt: model.NewTimestamp(time.Now().UTC().Add(time.Hour)),
		})
		if err != nil {
			return err
		}
		stored, err := ts.repo(authSessionDescriptor).CreateWithID(ctx, estate.token.ID, record)
		if err != nil {
			return err
		}
		base, err := baseFromRecord(stored)
		if err != nil {
			return err
		}
		crossKindSession, err = authSessionCodec.Decode(base, stored)
		if err != nil {
			return err
		}
		crossKindHandle, err = auth.DelegationHandles().Create(ctx, model.DelegationHandle{
			TargetTenantID: tenantA, Selector: "cross-kind-session-handle",
			SecretHash: []byte("cross-kind-handle-hash"), SourceCredKind: "user",
			SourceCredID: estate.token.ID, SubjectUserID: owner.ID, MintRole: "viewer",
			PEPServiceID: estate.service.ID, Audience: estate.service.PDPAudience,
			Operations: []string{"messages"},
			ExpiresAt:  model.NewTimestamp(time.Now().UTC().Add(time.Hour)),
		})
		return err
	})
	if err != nil {
		t.Fatalf("seed retiring User descendant authority: %v", err)
	}

	h.enforce(t)
	beforeA := directoryWriterTestEpoch(t, h.raw, tenantA).Version
	beforeB := directoryWriterTestEpoch(t, h.raw, tenantB).Version
	beforeWall := time.Now().UTC().Add(-time.Second)
	req := UserRetirementRequest{
		UserID: victim.user.ID, ExpectedVersion: victim.user.Version,
		Actor: retirementTestActor, ActorKind: model.ActorUser,
	}
	tombstone, err := RetireUser(ctx, h.raw, req)
	afterWall := time.Now().UTC().Add(time.Second)
	if err != nil {
		t.Fatalf("Retire User: %v", err)
	}
	if err := tombstone.Validate(); err != nil {
		t.Fatalf("User tombstone Validate: %v", err)
	}
	if tombstone.AuditAnchor.Seq < 1 || len(tombstone.AuditAnchor.Hash) != 32 {
		t.Fatalf("User tombstone audit anchor = %+v", tombstone.AuditAnchor)
	}
	if tombstone.RetiredAt.String() == fixed.String() ||
		tombstone.RetiredAt.Time().Before(beforeWall) ||
		tombstone.RetiredAt.Time().After(afterWall) {
		t.Fatalf("User retired_at = %s, fixed=%s range=[%s,%s]",
			tombstone.RetiredAt.String(), fixed.String(), beforeWall, afterWall)
	}
	wantEpochs := map[model.TenantID]int64{tenantA: beforeA + 1, tenantB: beforeB + 1}
	if len(tombstone.ResultingEpochs) != len(wantEpochs) {
		t.Fatalf("User epoch map = %+v, want both real tenants", tombstone.ResultingEpochs)
	}
	for tenant, want := range wantEpochs {
		got, carried := tombstone.ResultingEpochs.EpochFor(tenant)
		if !carried || got != want {
			t.Fatalf("User epoch for %s = %d carried=%t, want %d", tenant, got, carried, want)
		}
		witness, found, readErr := retirementReadWitness(t, h.raw, tenant, store.DirectoryPrincipalRef{
			PrincipalKind: model.DirectoryPrincipalUser, PrincipalRef: victim.user.ID,
		})
		if readErr != nil || !found || witness.TombstoneID != tombstone.ID ||
			witness.RetirementEpoch != want {
			t.Fatalf("User witness tenant=%s got=%+v found=%t err=%v",
				tenant, witness, found, readErr)
		}
	}
	if tombstone.ResultingEpochs[0].TenantID.String() >=
		tombstone.ResultingEpochs[1].TenantID.String() {
		t.Fatalf("User epoch evidence is not sorted: %+v", tombstone.ResultingEpochs)
	}

	err = h.raw.AuthView(ctx, func(auth store.AuthScope) error {
		checks := []struct {
			name string
			get  func() error
		}{
			{"User", func() error { _, err := auth.Users().Get(ctx, victim.user.ID); return err }},
			{"session", func() error { _, err := auth.Sessions().Get(ctx, victim.session.ID); return err }},
			{"WebAuthn", func() error { _, err := auth.WebAuthnCredentials().Get(ctx, victim.credential.ID); return err }},
			{"membership", func() error { _, err := auth.Memberships().Get(ctx, estate.membership.ID); return err }},
			{"group member", func() error { _, err := auth.GroupMembers().Get(ctx, estate.groupMember.ID); return err }},
			{"root token", func() error { _, err := auth.Tokens().Get(ctx, estate.token.ID); return err }},
			{"child token", func() error { _, err := auth.Tokens().Get(ctx, child.ID); return err }},
			{"act-as token", func() error { _, err := auth.Tokens().Get(ctx, actAs.ID); return err }},
			{"direct handle", func() error { _, err := auth.DelegationHandles().Get(ctx, estate.handle.ID); return err }},
			{"child handle", func() error { _, err := auth.DelegationHandles().Get(ctx, childHandle.ID); return err }},
			{"session-source handle", func() error { _, err := auth.DelegationHandles().Get(ctx, sessionHandle.ID); return err }},
			{"PEP token binding", func() error { _, err := auth.PEPServiceCredentials().Get(ctx, estate.credential.ID); return err }},
		}
		for _, check := range checks {
			if err := check.get(); !errors.Is(err, store.ErrNotFound) {
				return fmt.Errorf("%s survived User retirement: %w", check.name, err)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.raw.AuthView(ctx, func(auth store.AuthScope) error {
		if _, err := auth.Sessions().Get(ctx, crossKindSession.ID); err != nil {
			return fmt.Errorf("cross-kind session was removed: %w", err)
		}
		if _, err := auth.DelegationHandles().Get(ctx, crossKindHandle.ID); err != nil {
			return fmt.Errorf("cross-kind user-session handle was removed: %w", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	replayed, err := RetireUser(ctx, h.raw, req)
	if err != nil || !reflect.DeepEqual(replayed, tombstone) {
		t.Fatalf("User replay = %+v err=%v, want %+v", replayed, err, tombstone)
	}
	if got := directoryWriterTestEpoch(t, h.raw, tenantA).Version; got != beforeA+1 {
		t.Fatalf("User replay bumped tenant A to %d, want %d", got, beforeA+1)
	}
	if got := directoryWriterTestEpoch(t, h.raw, tenantB).Version; got != beforeB+1 {
		t.Fatalf("User replay bumped tenant B to %d, want %d", got, beforeB+1)
	}

	for _, attempt := range []struct {
		name string
		fn   func(store.AuthScope) error
	}{
		{"session", func(auth store.AuthScope) error {
			_, err := auth.Sessions().Create(ctx, model.AuthSession{
				UserID: victim.user.ID, Selector: "post-retire-session",
				SecretHash: []byte("hash"),
				ExpiresAt:  model.NewTimestamp(time.Now().UTC().Add(time.Hour)),
			})
			return err
		}},
		{"token", func(auth store.AuthScope) error {
			_, err := auth.Tokens().Create(ctx, model.APIToken{
				Name: "post-retire-token", UserID: victim.user.ID,
				Selector: "post-retire-token", SecretHash: []byte("hash"),
				BoundTenantID: tenantA, Role: "viewer",
			})
			return err
		}},
		{"WebAuthn", func(auth store.AuthScope) error {
			_, err := auth.WebAuthnCredentials().Create(ctx, model.WebAuthnCredential{
				UserID: victim.user.ID, CredentialID: "post-retire-webauthn",
				Credential: []byte(`{}`),
			})
			return err
		}},
		{"membership", func(auth store.AuthScope) error {
			_, err := auth.Memberships().Create(ctx, model.Membership{
				UserID: victim.user.ID, TargetTenantID: tenantA, Role: "viewer",
			})
			return err
		}},
		{"group member", func(auth store.AuthScope) error {
			_, err := auth.GroupMembers().Create(ctx, model.UserGroupMember{
				GroupID: estate.group.ID, UserID: victim.user.ID,
			})
			return err
		}},
		{"delegation", func(auth store.AuthScope) error {
			_, err := auth.DelegationHandles().Create(ctx, model.DelegationHandle{
				TargetTenantID: tenantA, Selector: "post-retire-handle",
				SecretHash: []byte("hash"), SourceCredKind: "token",
				SourceCredID: model.NewID(), SubjectUserID: victim.user.ID,
				MintRole: "viewer", PEPServiceID: estate.service.ID,
				Audience: estate.service.PDPAudience, Operations: []string{"messages"},
				ExpiresAt: model.NewTimestamp(time.Now().UTC().Add(time.Hour)),
			})
			return err
		}},
	} {
		t.Run("post-retire-guard-"+attempt.name, func(t *testing.T) {
			var discarded error
			err := h.raw.AuthMutate(ctx, func(auth store.AuthScope) error {
				discarded = attempt.fn(auth)
				return nil
			})
			if !errors.Is(discarded, store.ErrDirectoryPrincipalRetired) ||
				!errors.Is(err, store.ErrDirectoryPrincipalRetired) ||
				!strings.Contains(err.Error(), "poisoned") {
				t.Fatalf("discarded=%v outer=%v, want poisoned retired refusal", discarded, err)
			}
		})
	}
}

func TestDirectoryAuthorityGuardsTraverseFullTokenAncestryAndReplayRejectsResidualSQLite(t *testing.T) {
	ctx := context.Background()
	h := newDirectoryRetirementSQLiteHarness(t, store.Config{})
	tenant := provisionTenant(t, h.raw, "retirement-token-ancestry")
	victim := retirementCreateUser(t, h.raw, "retirement-token-ancestry-victim")
	owner := retirementCreateUser(t, h.raw, "retirement-token-ancestry-owner")
	var service model.PEPService
	if err := h.raw.AuthMutate(ctx, func(auth store.AuthScope) error {
		var err error
		service, err = auth.PEPServices().Create(ctx, model.PEPService{
			TargetTenantID: tenant, Name: "ancestry-pep",
			PDPAudience:  "urn:test:retirement-token-ancestry",
			Capabilities: map[string]bool{"streaming": true}, CapabilityVersion: 1,
		})
		return err
	}); err != nil {
		t.Fatalf("seed ancestry PEP service: %v", err)
	}
	h.enforce(t)
	req := UserRetirementRequest{
		UserID: victim.ID, ExpectedVersion: victim.Version,
		Actor: retirementTestActor, ActorKind: model.ActorUser,
	}
	if _, err := RetireUser(ctx, h.raw, req); err != nil {
		t.Fatalf("retire ancestry root User: %v", err)
	}

	// Simulate legacy/raw authority that appeared after the irreversible commit:
	// only the root names the retired User; two zero-User descendants hide it.
	var root, middle, leaf model.APIToken
	if err := h.raw.AuthMutate(ctx, func(auth store.AuthScope) error {
		ts := auth.(*authScope).ts
		raw := newTypedRepo(ts.repo(apiTokenDescriptor), apiTokenCodec)
		var err error
		root, err = raw.Create(ctx, model.APIToken{
			Name: "raw-retired-root", UserID: victim.ID, Selector: "raw-retired-root",
			SecretHash: []byte("root-hash"), BoundTenantID: tenant, Role: "viewer",
		})
		if err != nil {
			return err
		}
		middle, err = raw.Create(ctx, model.APIToken{
			Name: "raw-zero-middle", Selector: "raw-zero-middle",
			SecretHash: []byte("middle-hash"), BoundTenantID: tenant, Role: "viewer",
			ParentTokenID: root.ID,
		})
		if err != nil {
			return err
		}
		leaf, err = raw.Create(ctx, model.APIToken{
			Name: "raw-zero-leaf", Selector: "raw-zero-leaf",
			SecretHash: []byte("leaf-hash"), BoundTenantID: tenant, Role: "viewer",
			ParentTokenID: middle.ID,
		})
		return err
	}); err != nil {
		t.Fatalf("seed raw hidden retired ancestry: %v", err)
	}

	attempts := []struct {
		name string
		fn   func(store.AuthScope) error
	}{
		{
			name: "token-child",
			fn: func(auth store.AuthScope) error {
				_, err := auth.Tokens().Create(ctx, model.APIToken{
					Name: "guarded-token-child", Selector: "guarded-token-child",
					SecretHash: []byte("child-hash"), BoundTenantID: tenant, Role: "viewer",
					ParentTokenID: leaf.ID,
				})
				return err
			},
		},
		{
			name: "delegation-token-source",
			fn: func(auth store.AuthScope) error {
				_, err := auth.DelegationHandles().Create(ctx, model.DelegationHandle{
					TargetTenantID: tenant, Selector: "guarded-ancestry-handle",
					SecretHash: []byte("handle-hash"), SourceCredKind: "token",
					SourceCredID: leaf.ID, SubjectUserID: owner.ID, MintRole: "viewer",
					PEPServiceID: service.ID, Audience: service.PDPAudience,
					Operations: []string{"messages"},
					ExpiresAt:  model.NewTimestamp(time.Now().UTC().Add(time.Hour)),
				})
				return err
			},
		},
		{
			name: "pep-token-binding",
			fn: func(auth store.AuthScope) error {
				_, err := auth.PEPServiceCredentials().Create(ctx, model.PEPServiceCredential{
					ServiceID: service.ID, TokenID: leaf.ID,
				})
				return err
			},
		},
	}
	for _, attempt := range attempts {
		t.Run(attempt.name, func(t *testing.T) {
			err := h.raw.AuthMutate(ctx, attempt.fn)
			if !errors.Is(err, store.ErrDirectoryPrincipalRetired) {
				t.Fatalf("full ancestry guard error = %v, want retired", err)
			}
		})
	}
	if got := retirementRowCount(
		t, h.sql, apiTokenDescriptor.Table, "tenant_id = ?", model.SystemTenantID.String(),
	); got != 3 {
		t.Fatalf("API token rows after guarded attempts = %d, want raw ancestry only", got)
	}
	if got := retirementRowCount(
		t, h.sql, delegationHandleDescriptor.Table, "tenant_id = ?", model.SystemTenantID.String(),
	); got != 0 {
		t.Fatalf("delegation rows after guarded attempt = %d, want 0", got)
	}
	if got := retirementRowCount(
		t, h.sql, pepServiceCredentialDescriptor.Table, "tenant_id = ?", model.SystemTenantID.String(),
	); got != 0 {
		t.Fatalf("PEP credential rows after guarded attempt = %d, want 0", got)
	}
	_, err := RetireUser(ctx, h.raw, req)
	if !errors.Is(err, store.ErrDirectoryUnavailable) ||
		!errors.Is(err, store.ErrDirectoryRetirementResidualAuthority) {
		t.Fatalf("User replay with residual ancestry = %v, want residual unavailable", err)
	}
}

func TestDirectoryAuthorityRepositoriesIgnoreSQLiteTempSourcesAndTargets(t *testing.T) {
	ctx := context.Background()
	h := newDirectoryRetirementSQLiteHarness(t, store.Config{})
	tenant := provisionTenant(t, h.raw, "retirement-authority-temp")
	user := retirementCreateUser(t, h.raw, "retirement-authority-temp-user")
	var token model.APIToken
	var session model.AuthSession
	var service model.PEPService
	var handle model.DelegationHandle
	if err := h.raw.AuthMutate(ctx, func(auth store.AuthScope) error {
		var err error
		token, err = auth.Tokens().Create(ctx, model.APIToken{
			Name: "temp-main-token", UserID: user.ID, Selector: "temp-main-token",
			SecretHash: []byte("token-hash"), BoundTenantID: tenant, Role: "viewer",
		})
		if err != nil {
			return err
		}
		session, err = auth.Sessions().Create(ctx, model.AuthSession{
			UserID: user.ID, Selector: "temp-main-session", SecretHash: []byte("session-hash"),
			ExpiresAt: model.NewTimestamp(time.Now().UTC().Add(time.Hour)),
		})
		if err != nil {
			return err
		}
		service, err = auth.PEPServices().Create(ctx, model.PEPService{
			TargetTenantID: tenant, Name: "temp-main-pep", PDPAudience: "urn:test:temp-main-pep",
			Capabilities: map[string]bool{"streaming": true}, CapabilityVersion: 1,
		})
		if err != nil {
			return err
		}
		handle, err = auth.DelegationHandles().Create(ctx, model.DelegationHandle{
			TargetTenantID: tenant, Selector: "temp-main-handle", SecretHash: []byte("handle-hash"),
			SourceCredKind: "token", SourceCredID: token.ID, SubjectUserID: user.ID,
			MintRole: "viewer", PEPServiceID: service.ID, Audience: service.PDPAudience,
			Operations: []string{"messages"},
			ExpiresAt:  model.NewTimestamp(time.Now().UTC().Add(time.Hour)),
		})
		return err
	}); err != nil {
		t.Fatalf("seed authority TEMP fixture: %v", err)
	}
	h.enforce(t)
	for _, table := range []string{
		apiTokenDescriptor.Table,
		authSessionDescriptor.Table,
		delegationHandleDescriptor.Table,
		pepServiceCredentialDescriptor.Table,
	} {
		if _, err := h.sql.db.ExecContext(ctx,
			"CREATE TEMP TABLE "+quoteIdent(table)+" AS SELECT * FROM main."+
				quoteIdent(table)+" WHERE 0",
		); err != nil {
			t.Fatalf("create authority TEMP shadow %s: %v", table, err)
		}
	}
	for _, table := range []string{
		apiTokenDescriptor.Table, authSessionDescriptor.Table, delegationHandleDescriptor.Table,
	} {
		if _, err := h.sql.db.ExecContext(ctx,
			"INSERT INTO temp."+quoteIdent(table)+" SELECT * FROM main."+quoteIdent(table),
		); err != nil {
			t.Fatalf("copy authority row into TEMP %s: %v", table, err)
		}
	}

	// A successful guarded child and the specialized revoke must both target
	// main, even though identically named TEMP relations are closer in SQLite's
	// unqualified lookup order.
	var qualifiedChild model.APIToken
	if err := h.raw.AuthMutate(ctx, func(auth store.AuthScope) error {
		var err error
		qualifiedChild, err = auth.Tokens().Create(ctx, model.APIToken{
			Name: "qualified-main-child", Selector: "qualified-main-child",
			SecretHash: []byte("child-hash"), BoundTenantID: tenant, Role: "viewer",
			ParentTokenID: token.ID,
		})
		if err != nil {
			return err
		}
		changed, err := auth.RevokeDelegationHandle(
			ctx, handle.ID, model.NewTimestamp(time.Now().UTC()),
		)
		if err != nil {
			return err
		}
		if !changed {
			return errors.New("main delegation handle was not revoked")
		}
		return nil
	}); err != nil {
		t.Fatalf("qualified authority writes: %v", err)
	}
	if got := retirementRowCount(
		t, h.sql, apiTokenDescriptor.Table, "tenant_id = ?", model.SystemTenantID.String(),
	); got != 2 {
		t.Fatalf("main API token rows after qualified child = %d, want 2", got)
	}
	var tempTokens, mainRevoked, tempRevoked int64
	if err := h.sql.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM temp."+quoteIdent(apiTokenDescriptor.Table),
	).Scan(&tempTokens); err != nil || tempTokens != 1 {
		t.Fatalf("TEMP API token rows = %d err=%v, want unchanged 1", tempTokens, err)
	}
	if err := h.sql.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM main."+quoteIdent(delegationHandleDescriptor.Table)+
			" WHERE id = ? AND revoked_at IS NOT NULL", handle.ID.String(),
	).Scan(&mainRevoked); err != nil || mainRevoked != 1 {
		t.Fatalf("main revoked handle rows = %d err=%v, want 1", mainRevoked, err)
	}
	if err := h.sql.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM temp."+quoteIdent(delegationHandleDescriptor.Table)+
			" WHERE id = ? AND revoked_at IS NOT NULL", handle.ID.String(),
	).Scan(&tempRevoked); err != nil || tempRevoked != 0 {
		t.Fatalf("TEMP revoked handle rows = %d err=%v, want unchanged 0", tempRevoked, err)
	}

	// Leave the parent/session only in TEMP. Every source validation must read
	// main and refuse before it can write any real target row.
	if err := h.raw.AuthMutate(ctx, func(auth store.AuthScope) error {
		if err := auth.Tokens().Delete(ctx, qualifiedChild.ID); err != nil {
			return err
		}
		if err := auth.Tokens().Delete(ctx, token.ID); err != nil {
			return err
		}
		return auth.Sessions().Delete(ctx, session.ID)
	}); err != nil {
		t.Fatalf("remove main authority sources: %v", err)
	}
	for _, attempt := range []struct {
		name string
		fn   func(store.AuthScope) error
	}{
		{
			name: "token-parent",
			fn: func(auth store.AuthScope) error {
				_, err := auth.Tokens().Create(ctx, model.APIToken{
					Name: "temp-only-parent-child", Selector: "temp-only-parent-child",
					SecretHash: []byte("hash"), BoundTenantID: tenant, Role: "viewer",
					ParentTokenID: token.ID,
				})
				return err
			},
		},
		{
			name: "delegation-token-source",
			fn: func(auth store.AuthScope) error {
				_, err := auth.DelegationHandles().Create(ctx, model.DelegationHandle{
					TargetTenantID: tenant, Selector: "temp-only-token-handle",
					SecretHash: []byte("hash"), SourceCredKind: "token", SourceCredID: token.ID,
					SubjectUserID: user.ID, MintRole: "viewer", PEPServiceID: service.ID,
					Audience: service.PDPAudience, Operations: []string{"messages"},
					ExpiresAt: model.NewTimestamp(time.Now().UTC().Add(time.Hour)),
				})
				return err
			},
		},
		{
			name: "delegation-session-source",
			fn: func(auth store.AuthScope) error {
				_, err := auth.DelegationHandles().Create(ctx, model.DelegationHandle{
					TargetTenantID: tenant, Selector: "temp-only-session-handle",
					SecretHash: []byte("hash"), SourceCredKind: "user", SourceCredID: session.ID,
					SubjectUserID: user.ID, MintRole: "viewer", PEPServiceID: service.ID,
					Audience: service.PDPAudience, Operations: []string{"messages"},
					ExpiresAt: model.NewTimestamp(time.Now().UTC().Add(time.Hour)),
				})
				return err
			},
		},
		{
			name: "pep-token-source",
			fn: func(auth store.AuthScope) error {
				_, err := auth.PEPServiceCredentials().Create(ctx, model.PEPServiceCredential{
					ServiceID: service.ID, TokenID: token.ID,
				})
				return err
			},
		},
	} {
		t.Run(attempt.name, func(t *testing.T) {
			err := h.raw.AuthMutate(ctx, attempt.fn)
			if !errors.Is(err, store.ErrDirectoryUnavailable) {
				t.Fatalf("TEMP-only source guard = %v, want unavailable", err)
			}
		})
	}
	if got := retirementRowCount(
		t, h.sql, apiTokenDescriptor.Table, "tenant_id = ?", model.SystemTenantID.String(),
	); got != 0 {
		t.Fatalf("main API token rows after TEMP-only attempts = %d, want 0", got)
	}
	if got := retirementRowCount(
		t, h.sql, authSessionDescriptor.Table, "tenant_id = ?", model.SystemTenantID.String(),
	); got != 0 {
		t.Fatalf("main auth session rows after TEMP-only attempts = %d, want 0", got)
	}
	if got := retirementRowCount(
		t, h.sql, delegationHandleDescriptor.Table, "tenant_id = ?", model.SystemTenantID.String(),
	); got != 1 {
		t.Fatalf("main delegation rows after TEMP-only attempts = %d, want original 1", got)
	}
	if got := retirementRowCount(
		t, h.sql, pepServiceCredentialDescriptor.Table, "tenant_id = ?", model.SystemTenantID.String(),
	); got != 0 {
		t.Fatalf("main PEP credential rows after TEMP-only attempts = %d, want 0", got)
	}
}

func TestDirectoryAuthorityZeroUserTokenLocksBeforeCreateAndUpdateSQLite(t *testing.T) {
	ctx := context.Background()
	h := newDirectoryRetirementSQLiteHarness(t, store.Config{})
	tenant := provisionTenant(t, h.raw, "retirement-zero-user-token-lock")
	h.enforce(t)
	// This used to assert lockCount==1 as the FIRST statement of the callback, which
	// is not a property of the system: it is the unconditional prelude asserting its
	// own existence, and no source-bound acquisition can satisfy it by construction.
	// It is re-expressed as the two properties that are real and that the old shape
	// could not tell apart -- entering a callback takes NO lock, and reaching a
	// guarded source finds one already held.
	lockCount := 0
	directoryWriterAfterLockTestHook = func() { lockCount++ }
	lockedAtSource := 0
	directoryWriterBeforeSourceTestHook = func(
		_ context.Context, tracker *directoryWriteTracker,
	) error {
		if !tracker.locked {
			return errors.New("guarded source reached without the global directory lock")
		}
		lockedAtSource++
		return nil
	}
	t.Cleanup(func() {
		directoryWriterAfterLockTestHook = nil
		directoryWriterBeforeSourceTestHook = nil
	})
	var token model.APIToken
	if err := h.raw.AuthMutate(ctx, func(auth store.AuthScope) error {
		if lockCount != 0 {
			return fmt.Errorf("Create callback entry took a lock it does not need: count=%d", lockCount)
		}
		var err error
		token, err = auth.Tokens().Create(ctx, model.APIToken{
			Name: "zero-user-token", Selector: "zero-user-token", SecretHash: []byte("hash"),
			BoundTenantID: tenant, Role: "viewer",
		})
		return err
	}); err != nil {
		t.Fatalf("zero-User Token Create: %v", err)
	}
	if err := h.raw.AuthMutate(ctx, func(auth store.AuthScope) error {
		if lockCount != 1 {
			return fmt.Errorf("Update callback entry saw %d locks, want only the Create's", lockCount)
		}
		token.Name = "zero-user-token-updated"
		var err error
		token, err = auth.Tokens().Update(ctx, token)
		return err
	}); err != nil {
		t.Fatalf("zero-User Token Update: %v", err)
	}
	if lockCount != 2 {
		t.Fatalf("global authority lock count = %d, want one per WRITING AuthMutate", lockCount)
	}
	if lockedAtSource != 2 {
		t.Fatalf("guarded sources reached with the lock held = %d, want 2", lockedAtSource)
	}
}

func TestRetireUserAuthorityPostLockInterleavingsSQLite(t *testing.T) {
	t.Run("retirement-wins-stale-issuer-refused", func(t *testing.T) {
		h := newDirectoryRetirementSQLiteHarness(t, store.Config{})
		provisionTenant(t, h.raw, "retirement-race-wins")
		staleUser := retirementCreateUser(t, h.raw, "retirement-race-stale-user")
		h.enforce(t)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		auditReached := make(chan struct{})
		releaseRetirement := make(chan struct{})
		directoryRetirementAfterAuditTestHook = func(*model.AuditEvent) {
			close(auditReached)
			<-releaseRetirement
		}
		t.Cleanup(func() { directoryRetirementAfterAuditTestHook = nil })
		retireDone := make(chan error, 1)
		go func() {
			_, err := RetireUser(ctx, h.raw, UserRetirementRequest{
				UserID: staleUser.ID, ExpectedVersion: staleUser.Version,
				Actor: retirementTestActor, ActorKind: model.ActorUser,
			})
			retireDone <- err
		}()
		select {
		case <-auditReached:
		case <-ctx.Done():
			t.Fatalf("retirement did not reach held post-audit point: %v", ctx.Err())
		}
		issuerDone := make(chan error, 1)
		go func() {
			issuerDone <- h.raw.AuthMutate(ctx, func(auth store.AuthScope) error {
				_, err := auth.Sessions().Create(ctx, model.AuthSession{
					UserID: staleUser.ID, Selector: "stale-post-lock-session",
					SecretHash: []byte("hash"),
					ExpiresAt:  model.NewTimestamp(time.Now().UTC().Add(time.Hour)),
				})
				return err
			})
		}()
		select {
		case err := <-issuerDone:
			t.Fatalf("stale issuer crossed held retirement: %v", err)
		case <-time.After(100 * time.Millisecond):
		}
		close(releaseRetirement)
		if err := <-retireDone; err != nil {
			t.Fatalf("retirement winner: %v", err)
		}
		select {
		case err := <-issuerDone:
			if !errors.Is(err, store.ErrDirectoryPrincipalRetired) {
				t.Fatalf("stale issuer after retirement = %v, want retired", err)
			}
		case <-ctx.Done():
			t.Fatalf("stale issuer did not finish: %v", ctx.Err())
		}
		if got := retirementRowCount(
			t, h.sql, authSessionDescriptor.Table,
			"tenant_id = ? AND user_id = ?", model.SystemTenantID.String(), staleUser.ID.String(),
		); got != 0 {
			t.Fatalf("stale issuer committed %d session rows, want 0", got)
		}
	})

	t.Run("issuer-audit-before-credential-finishes-with-global-first", func(t *testing.T) {
		h := newDirectoryRetirementSQLiteHarness(t, store.Config{})
		tenant := provisionTenant(t, h.raw, "retirement-race-issuer-first")
		victim := retirementCreateUser(t, h.raw, "retirement-race-issuer-user")
		h.enforce(t)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		issuerLocked := make(chan struct{})
		releaseIssuer := make(chan struct{})
		var once sync.Once
		directoryWriterBeforeSourceTestHook = func(
			context.Context,
			*directoryWriteTracker,
		) error {
			once.Do(func() {
				close(issuerLocked)
				<-releaseIssuer
			})
			return nil
		}
		t.Cleanup(func() { directoryWriterBeforeSourceTestHook = nil })
		issuerDone := make(chan error, 1)
		go func() {
			issuerDone <- h.raw.AuthMutate(ctx, func(auth store.AuthScope) error {
				if _, err := auth.Audit().Append(ctx, model.AuditDraft{
					Actor: retirementTestActor, ActorKind: model.ActorUser,
					Action:     "test.authority.before_credential",
					TargetKind: userDescriptor.Kind, TargetID: victim.ID,
				}); err != nil {
					return err
				}
				_, err := auth.Tokens().Create(ctx, model.APIToken{
					Name: "issuer-first-token", UserID: victim.ID, Selector: "issuer-first-token",
					SecretHash: []byte("hash"), BoundTenantID: tenant, Role: "viewer",
				})
				return err
			})
		}()
		select {
		case <-issuerLocked:
		case <-ctx.Done():
			t.Fatalf("issuer did not acquire global lock before callback: %v", ctx.Err())
		}
		retireDone := make(chan error, 1)
		go func() {
			_, err := RetireUser(ctx, h.raw, UserRetirementRequest{
				UserID: victim.ID, ExpectedVersion: victim.Version,
				Actor: retirementTestActor, ActorKind: model.ActorUser,
			})
			retireDone <- err
		}()
		select {
		case err := <-retireDone:
			t.Fatalf("retirement crossed issuer's global reservation: %v", err)
		case <-time.After(100 * time.Millisecond):
		}
		close(releaseIssuer)
		select {
		case err := <-issuerDone:
			if err != nil {
				t.Fatalf("issuer audit→credential transaction: %v", err)
			}
		case <-ctx.Done():
			t.Fatalf("issuer audit→credential transaction deadlocked: %v", ctx.Err())
		}
		select {
		case err := <-retireDone:
			if err != nil {
				t.Fatalf("retirement after issuer commit: %v", err)
			}
		case <-ctx.Done():
			t.Fatalf("retirement after issuer commit deadlocked: %v", ctx.Err())
		}
		if got := retirementRowCount(
			t, h.sql, apiTokenDescriptor.Table,
			"tenant_id = ? AND user_id = ?", model.SystemTenantID.String(), victim.ID.String(),
		); got != 0 {
			t.Fatalf("retirement left %d issuer-first token rows, want 0", got)
		}
	})
}

func TestRetireUserMembershipGlobalLockInterleavingsSQLite(t *testing.T) {
	t.Run("retirement-wins-membership-refused", func(t *testing.T) {
		h := newDirectoryRetirementSQLiteHarness(t, store.Config{})
		tenant := provisionTenant(t, h.raw, "retirement-membership-race-retire-first")
		user := retirementCreateUser(t, h.raw, "retirement-membership-race-retire-first")
		h.enforce(t)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		beforeEpoch := directoryWriterTestEpoch(t, h.raw, tenant).Version
		auditReached := make(chan struct{})
		releaseRetirement := make(chan struct{})
		directoryRetirementAfterAuditTestHook = func(*model.AuditEvent) {
			close(auditReached)
			<-releaseRetirement
		}
		t.Cleanup(func() { directoryRetirementAfterAuditTestHook = nil })
		retireDone := make(chan error, 1)
		go func() {
			_, err := RetireUser(ctx, h.raw, UserRetirementRequest{
				UserID: user.ID, ExpectedVersion: user.Version,
				Actor: retirementTestActor, ActorKind: model.ActorUser,
			})
			retireDone <- err
		}()
		select {
		case <-auditReached:
		case <-ctx.Done():
			t.Fatalf("retirement did not reach held audit: %v", ctx.Err())
		}
		membershipDone := make(chan error, 1)
		go func() {
			membershipDone <- h.raw.AuthMutate(ctx, func(auth store.AuthScope) error {
				_, err := auth.Memberships().Create(ctx, model.Membership{
					UserID: user.ID, TargetTenantID: tenant, Role: "viewer",
				})
				return err
			})
		}()
		select {
		case err := <-membershipDone:
			t.Fatalf("Membership crossed held User retirement: %v", err)
		case <-time.After(100 * time.Millisecond):
		}
		close(releaseRetirement)
		select {
		case err := <-retireDone:
			if err != nil {
				t.Fatalf("retirement-first User: %v", err)
			}
		case <-ctx.Done():
			t.Fatalf("retirement-first User deadlocked: %v", ctx.Err())
		}
		select {
		case err := <-membershipDone:
			if !errors.Is(err, store.ErrDirectoryPrincipalRetired) {
				t.Fatalf("Membership after User retirement = %v, want retired", err)
			}
		case <-ctx.Done():
			t.Fatalf("Membership waiter did not finish: %v", ctx.Err())
		}
		if got := retirementRowCount(
			t, h.sql, membershipDescriptor.Table,
			"tenant_id = ? AND user_id = ?", model.SystemTenantID.String(), user.ID.String(),
		); got != 0 {
			t.Fatalf("retirement-first Membership rows = %d, want 0", got)
		}
		if got := directoryWriterTestEpoch(t, h.raw, tenant).Version; got != beforeEpoch+1 {
			t.Fatalf("retirement-first epoch = %d, want %d", got, beforeEpoch+1)
		}
	})

	t.Run("membership-wins-retirement-cleans", func(t *testing.T) {
		h := newDirectoryRetirementSQLiteHarness(t, store.Config{})
		tenant := provisionTenant(t, h.raw, "retirement-membership-race-writer-first")
		user := retirementCreateUser(t, h.raw, "retirement-membership-race-writer-first")
		h.enforce(t)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		beforeEpoch := directoryWriterTestEpoch(t, h.raw, tenant).Version
		writerLocked := make(chan struct{})
		releaseWriter := make(chan struct{})
		var once sync.Once
		directoryWriterBeforeSourceTestHook = func(
			context.Context,
			*directoryWriteTracker,
		) error {
			once.Do(func() {
				close(writerLocked)
				<-releaseWriter
			})
			return nil
		}
		t.Cleanup(func() { directoryWriterBeforeSourceTestHook = nil })
		membershipDone := make(chan error, 1)
		go func() {
			membershipDone <- h.raw.AuthMutate(ctx, func(auth store.AuthScope) error {
				_, err := auth.Memberships().Create(ctx, model.Membership{
					UserID: user.ID, TargetTenantID: tenant, Role: "viewer",
				})
				return err
			})
		}()
		select {
		case <-writerLocked:
		case <-ctx.Done():
			t.Fatalf("Membership writer did not acquire global lock: %v", ctx.Err())
		}
		retireDone := make(chan error, 1)
		go func() {
			_, err := RetireUser(ctx, h.raw, UserRetirementRequest{
				UserID: user.ID, ExpectedVersion: user.Version,
				Actor: retirementTestActor, ActorKind: model.ActorUser,
			})
			retireDone <- err
		}()
		select {
		case err := <-retireDone:
			t.Fatalf("User retirement crossed held Membership writer: %v", err)
		case <-time.After(100 * time.Millisecond):
		}
		close(releaseWriter)
		select {
		case err := <-membershipDone:
			if err != nil {
				t.Fatalf("Membership-first writer: %v", err)
			}
		case <-ctx.Done():
			t.Fatalf("Membership-first writer deadlocked: %v", ctx.Err())
		}
		select {
		case err := <-retireDone:
			if err != nil {
				t.Fatalf("User retirement after Membership: %v", err)
			}
		case <-ctx.Done():
			t.Fatalf("User retirement after Membership deadlocked: %v", ctx.Err())
		}
		if got := retirementRowCount(
			t, h.sql, membershipDescriptor.Table,
			"tenant_id = ? AND user_id = ?", model.SystemTenantID.String(), user.ID.String(),
		); got != 0 {
			t.Fatalf("Membership-first residual rows = %d, want 0", got)
		}
		if got := directoryWriterTestEpoch(t, h.raw, tenant).Version; got != beforeEpoch+2 {
			t.Fatalf("Membership-first epoch = %d, want %d", got, beforeEpoch+2)
		}
	})
}

func TestDirectoryRetirementRejectsTenantAuditBeforeDirectoryWriteSQLite(t *testing.T) {
	t.Run("identity-and-agent-reject-before-global", func(t *testing.T) {
		ctx := context.Background()
		h := newDirectoryRetirementSQLiteHarness(t, store.Config{})
		tenant := provisionTenant(t, h.raw, "retirement-audit-first-reject")
		identity := retirementCreateIdentity(t, h.raw, tenant, "audit-first-identity")
		agent := retirementCreateAgent(
			t, h.raw, tenant, identity.ID, "", "audit-first-agent", model.StatusActive,
		)
		h.enforce(t)
		beforeEpoch := directoryWriterTestEpoch(t, h.raw, tenant).Version
		beforeAudit := retirementRowCount(t, h.sql, auditTable, "tenant_id = ?", tenant.String())
		globalAcquisitions := 0
		directoryWriterAfterLockTestHook = func() { globalAcquisitions++ }
		t.Cleanup(func() { directoryWriterAfterLockTestHook = nil })
		for _, tc := range []struct {
			name   string
			update func(store.Scope) error
		}{
			{
				name: "Identity",
				update: func(sc store.Scope) error {
					candidate := identity
					candidate.Name = "audit-first-identity-mutated"
					_, err := sc.Identities().Update(ctx, candidate)
					return err
				},
			},
			{
				name: "Agent",
				update: func(sc store.Scope) error {
					candidate := agent
					candidate.Name = "audit-first-agent-mutated"
					_, err := sc.Agents().Update(ctx, candidate)
					return err
				},
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				var discarded error
				err := h.raw.Mutate(ctx, tenant, func(sc store.Scope) error {
					if _, err := sc.Audit().Append(ctx, model.AuditDraft{
						Actor: retirementTestActor, ActorKind: model.ActorUser,
						Action: "test.audit_first_" + strings.ToLower(tc.name),
					}); err != nil {
						return err
					}
					discarded = tc.update(sc)
					return nil
				})
				if !errors.Is(discarded, errDirectoryWriterAuditFirst) ||
					!errors.Is(err, errDirectoryWriterAuditFirst) ||
					!strings.Contains(err.Error(), "poisoned") {
					t.Fatalf("discarded audit-first error=%v outer=%v, want poisoned order refusal",
						discarded, err)
				}
			})
		}
		if globalAcquisitions != 0 {
			t.Fatalf("audit-first writers attempted global lock %d time(s), want 0", globalAcquisitions)
		}
		if got := directoryWriterTestEpoch(t, h.raw, tenant).Version; got != beforeEpoch {
			t.Fatalf("audit-first epoch = %d, want rollback %d", got, beforeEpoch)
		}
		if got := retirementRowCount(
			t, h.sql, auditTable, "tenant_id = ?", tenant.String(),
		); got != beforeAudit {
			t.Fatalf("audit-first ledger rows = %d, want rollback %d", got, beforeAudit)
		}
		if err := h.raw.View(ctx, tenant, func(sc store.Scope) error {
			gotIdentity, err := sc.Identities().Get(ctx, identity.ID)
			if err != nil {
				return err
			}
			gotAgent, err := sc.Agents().Get(ctx, agent.ID)
			if err != nil {
				return err
			}
			if gotIdentity.Name != identity.Name || gotAgent.Name != agent.Name {
				return fmt.Errorf("audit-first source mutation persisted: identity=%q agent=%q",
					gotIdentity.Name, gotAgent.Name)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("audit-first-loser-rolls-back-and-retirement-finishes", func(t *testing.T) {
		h := newDirectoryRetirementSQLiteHarness(t, store.Config{})
		tenant := provisionTenant(t, h.raw, "retirement-audit-first-concurrent")
		identity := retirementCreateIdentity(t, h.raw, tenant, "audit-first-concurrent-identity")
		agent := retirementCreateAgent(
			t, h.raw, tenant, identity.ID, "", "audit-first-concurrent-agent", model.StatusActive,
		)
		h.enforce(t)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		auditHeld := make(chan struct{})
		releaseAuditFirst := make(chan struct{})
		writerDone := make(chan error, 1)
		go func() {
			writerDone <- h.raw.Mutate(ctx, tenant, func(sc store.Scope) error {
				if _, err := sc.Audit().Append(ctx, model.AuditDraft{
					Actor: retirementTestActor, ActorKind: model.ActorUser,
					Action: "test.audit_first_concurrent",
				}); err != nil {
					return err
				}
				close(auditHeld)
				<-releaseAuditFirst
				candidate := agent
				candidate.Name = "must-not-persist"
				_, _ = sc.Agents().Update(ctx, candidate)
				return nil
			})
		}()
		select {
		case <-auditHeld:
		case <-ctx.Done():
			t.Fatalf("audit-first writer did not reach hold point: %v", ctx.Err())
		}
		type retireOutcome struct {
			result DirectoryPrincipalRetirementResult
			err    error
		}
		retireDone := make(chan retireOutcome, 1)
		go func() {
			result, err := RetireDirectoryPrincipal(ctx, h.raw, DirectoryPrincipalRetirementRequest{
				TenantID: tenant, PrincipalKind: model.DirectoryPrincipalAgent,
				SourceID: agent.ID, ExpectedVersion: agent.Version,
				Actor: retirementTestActor, ActorKind: model.ActorUser,
			})
			retireDone <- retireOutcome{result: result, err: err}
		}()
		select {
		case got := <-retireDone:
			t.Fatalf("retirement crossed held audit-first transaction: %+v err=%v", got.result, got.err)
		case <-time.After(100 * time.Millisecond):
		}
		close(releaseAuditFirst)
		select {
		case err := <-writerDone:
			if !errors.Is(err, errDirectoryWriterAuditFirst) ||
				!strings.Contains(err.Error(), "poisoned") {
				t.Fatalf("audit-first concurrent writer = %v, want poisoned order refusal", err)
			}
		case <-ctx.Done():
			t.Fatalf("audit-first concurrent writer deadlocked: %v", ctx.Err())
		}
		select {
		case got := <-retireDone:
			if got.err != nil || !got.result.Definitive {
				t.Fatalf("retirement after audit-first rollback = %+v err=%v", got.result, got.err)
			}
		case <-ctx.Done():
			t.Fatalf("retirement after audit-first rollback deadlocked: %v", ctx.Err())
		}
		if got := retirementRowCount(
			t, h.sql, auditTable,
			"tenant_id = ? AND action = ?", tenant.String(), "test.audit_first_concurrent",
		); got != 0 {
			t.Fatalf("rolled-back audit-first event rows = %d, want 0", got)
		}
	})
}

func TestRetireUserRollbackRestoresAuthoritySourceEpochAndAuditSQLite(t *testing.T) {
	ctx := context.Background()
	h := newDirectoryRetirementSQLiteHarness(t, store.Config{})
	tenantA := provisionTenant(t, h.raw, "retire-user-rollback-a")
	tenantB := provisionTenant(t, h.raw, "retire-user-rollback-b")
	victim, err := directoryEpochTestSeedRetainedAuth(ctx, h.raw, "retire-user-rollback")
	if err != nil {
		t.Fatalf("seed rollback User: %v", err)
	}
	estate, err := directoryEpochTestSeedAuthEstate(
		ctx, h.raw, tenantA, "retire-user-rollback-estate", victim.user.ID,
	)
	if err != nil {
		t.Fatalf("seed rollback User estate: %v", err)
	}
	h.enforce(t)
	beforeA := directoryWriterTestEpoch(t, h.raw, tenantA).Version
	beforeB := directoryWriterTestEpoch(t, h.raw, tenantB).Version
	beforeAudit := retirementRowCount(
		t, h.sql, auditTable, "tenant_id = ?", model.SystemTenantID.String(),
	)
	forced := errors.New("rollback complete User tuple")
	directoryRetirementBeforeFinishTestHook = func(
		kind model.DirectoryPrincipalKind,
		id model.ID,
	) error {
		if kind != model.DirectoryPrincipalUser || id != victim.user.ID {
			return fmt.Errorf("unexpected User rollback target %s/%s", kind, id)
		}
		return forced
	}
	t.Cleanup(func() { directoryRetirementBeforeFinishTestHook = nil })

	_, err = RetireUser(ctx, h.raw, UserRetirementRequest{
		UserID: victim.user.ID, ExpectedVersion: victim.user.Version,
		Actor: retirementTestActor, ActorKind: model.ActorUser,
	})
	if !errors.Is(err, forced) {
		t.Fatalf("forced User rollback = %v, want sentinel", err)
	}
	if got := directoryWriterTestEpoch(t, h.raw, tenantA).Version; got != beforeA {
		t.Fatalf("User rollback tenant A epoch = %d, want %d", got, beforeA)
	}
	if got := directoryWriterTestEpoch(t, h.raw, tenantB).Version; got != beforeB {
		t.Fatalf("User rollback tenant B epoch = %d, want %d", got, beforeB)
	}
	if got := retirementRowCount(
		t, h.sql, userDescriptor.Table, "tenant_id = ? AND id = ?",
		model.SystemTenantID.String(), victim.user.ID.String(),
	); got != 1 {
		t.Fatalf("User rollback source rows = %d, want 1", got)
	}
	if got := retirementRowCount(
		t, h.sql, userTombstoneDescriptor.Table, "tenant_id = ?",
		model.SystemTenantID.String(),
	); got != 0 {
		t.Fatalf("User rollback tombstones = %d, want 0", got)
	}
	if got := retirementRowCount(
		t, h.sql, auditTable, "tenant_id = ?", model.SystemTenantID.String(),
	); got != beforeAudit {
		t.Fatalf("User rollback audit rows = %d, want %d", got, beforeAudit)
	}
	if err := h.raw.AuthView(ctx, func(auth store.AuthScope) error {
		for name, get := range map[string]func() error{
			"session":    func() error { _, err := auth.Sessions().Get(ctx, victim.session.ID); return err },
			"WebAuthn":   func() error { _, err := auth.WebAuthnCredentials().Get(ctx, victim.credential.ID); return err },
			"membership": func() error { _, err := auth.Memberships().Get(ctx, estate.membership.ID); return err },
			"token":      func() error { _, err := auth.Tokens().Get(ctx, estate.token.ID); return err },
			"handle":     func() error { _, err := auth.DelegationHandles().Get(ctx, estate.handle.ID); return err },
			"PEP binding": func() error {
				_, err := auth.PEPServiceCredentials().Get(ctx, estate.credential.ID)
				return err
			},
		} {
			if err := get(); err != nil {
				return fmt.Errorf("%s not restored: %w", name, err)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestDirectoryRetirementReplayIgnoresRequestTraceCorrelationSQLite(t *testing.T) {
	h := newDirectoryRetirementSQLiteHarness(t, store.Config{})
	tenant := provisionTenant(t, h.raw, "retirement-trace-replay")
	identity := retirementCreateIdentity(t, h.raw, tenant, "trace-replay-identity")
	h.enforce(t)
	traceA, _ := oteltrace.TraceIDFromHex("4bf92f3577b34da6a3ce929d0e0e4736")
	spanA, _ := oteltrace.SpanIDFromHex("00f067aa0ba902b7")
	ctxA := oteltrace.ContextWithSpanContext(context.Background(), oteltrace.NewSpanContext(
		oteltrace.SpanContextConfig{TraceID: traceA, SpanID: spanA},
	))
	req := DirectoryPrincipalRetirementRequest{
		TenantID: tenant, PrincipalKind: model.DirectoryPrincipalIdentity,
		SourceID: identity.ID, ExpectedVersion: identity.Version,
		Actor: retirementTestActor, ActorKind: model.ActorUser,
	}
	result, err := RetireDirectoryPrincipal(ctxA, h.raw, req)
	if err != nil {
		t.Fatalf("retire under trace A: %v", err)
	}
	traceB, _ := oteltrace.TraceIDFromHex("0af7651916cd43dd8448eb211c80319c")
	spanB, _ := oteltrace.SpanIDFromHex("b7ad6b7169203331")
	ctxB := oteltrace.ContextWithSpanContext(context.Background(), oteltrace.NewSpanContext(
		oteltrace.SpanContextConfig{TraceID: traceB, SpanID: spanB},
	))
	for name, replayCtx := range map[string]context.Context{
		"different-trace": ctxB,
		"no-trace":        context.Background(),
	} {
		t.Run(name, func(t *testing.T) {
			replayed, err := RetireDirectoryPrincipal(replayCtx, h.raw, req)
			if err != nil || !reflect.DeepEqual(replayed, result) {
				t.Fatalf("trace-independent replay = %+v err=%v, want %+v", replayed, err, result)
			}
		})
	}
}

func TestDirectoryRetirementStableAuditMetaRejectsMalformedCorrelationSQLite(t *testing.T) {
	want := map[string]any{
		"cause":       string(model.DirectoryCauseIdentityRetired),
		"source_kind": string(identityDescriptor.Kind),
	}
	validTrace := "4bf92f3577b34da6a3ce929d0e0e4736"
	validSpan := "00f067aa0ba902b7"
	for _, tc := range []struct {
		name string
		meta map[string]any
	}{
		{
			name: "trace-without-span",
			meta: map[string]any{
				"cause": string(model.DirectoryCauseIdentityRetired), "source_kind": string(identityDescriptor.Kind),
				"trace_id": validTrace,
			},
		},
		{
			name: "span-without-trace",
			meta: map[string]any{
				"cause": string(model.DirectoryCauseIdentityRetired), "source_kind": string(identityDescriptor.Kind),
				"span_id": validSpan,
			},
		},
		{
			name: "zero-trace",
			meta: map[string]any{
				"cause": string(model.DirectoryCauseIdentityRetired), "source_kind": string(identityDescriptor.Kind),
				"trace_id": strings.Repeat("0", 32), "span_id": validSpan,
			},
		},
		{
			name: "zero-span",
			meta: map[string]any{
				"cause": string(model.DirectoryCauseIdentityRetired), "source_kind": string(identityDescriptor.Kind),
				"trace_id": validTrace, "span_id": strings.Repeat("0", 16),
			},
		},
		{
			name: "unexpected-key",
			meta: map[string]any{
				"cause": string(model.DirectoryCauseIdentityRetired), "source_kind": string(identityDescriptor.Kind),
				"trace_id": validTrace, "span_id": validSpan, "request_id": "not-stable",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stored, err := canon.CanonicalMeta(tc.meta)
			if err != nil {
				t.Fatalf("canonicalize malformed fixture: %v", err)
			}
			err = validateStableRetirementAuditMeta(stored, want)
			if !errors.Is(err, store.ErrDirectoryUnavailable) {
				t.Fatalf("malformed correlation accepted: %v", err)
			}
		})
	}
	valid, err := canon.CanonicalMeta(map[string]any{
		"cause": string(model.DirectoryCauseIdentityRetired), "source_kind": string(identityDescriptor.Kind),
		"trace_id": validTrace, "span_id": validSpan,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := validateStableRetirementAuditMeta(valid, want); err != nil {
		t.Fatalf("valid trace correlation rejected: %v", err)
	}
}

func TestDirectorySnapshotRejectsUserTombstoneSourceContradictionAndMissingAnchorSQLite(t *testing.T) {
	for _, tc := range []struct {
		name        string
		withSource  bool
		validAnchor bool
		wantText    string
	}{
		{
			name: "source-and-valid-tombstone", withSource: true, validAnchor: true,
			wantText: "coexists with its source",
		},
		{
			name: "missing-audit-anchor", withSource: false, validAnchor: false,
			wantText: "anchor is absent or ambiguous",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			h := newDirectoryRetirementSQLiteHarness(t, store.Config{})
			tenant := provisionTenant(t, h.raw, "user-tombstone-"+tc.name)
			userID := model.NewID()
			var user model.User
			if tc.withSource {
				user = retirementCreateUser(t, h.raw, "user-tombstone-"+tc.name)
				userID = user.ID
			}
			retirementSeedUserTombstone(t, h, tenant, userID, tc.validAnchor)
			h.enforce(t)

			_, found, err := retirementReadWitness(t, h.raw, tenant, store.DirectoryPrincipalRef{
				PrincipalKind: model.DirectoryPrincipalUser, PrincipalRef: userID,
			})
			if !errors.Is(err, store.ErrDirectoryUnavailable) || found ||
				!strings.Contains(err.Error(), tc.wantText) {
				t.Fatalf("contradictory User witness found=%t err=%v, want %q unavailable",
					found, err, tc.wantText)
			}
			err = h.raw.AuthMutate(ctx, func(auth store.AuthScope) error {
				_, createErr := auth.Sessions().Create(ctx, model.AuthSession{
					UserID: userID, Selector: "contradictory-user-session",
					SecretHash: []byte("hash"),
					ExpiresAt:  model.NewTimestamp(time.Now().UTC().Add(time.Hour)),
				})
				return createErr
			})
			if !errors.Is(err, store.ErrDirectoryUnavailable) ||
				errors.Is(err, store.ErrDirectoryPrincipalRetired) {
				t.Fatalf("contradictory User guard = %v, want unavailable not retired", err)
			}
			if tc.withSource {
				beforeEpoch := directoryWriterTestEpoch(t, h.raw, tenant).Version
				_, err = RetireUser(ctx, h.raw, UserRetirementRequest{
					UserID: user.ID, ExpectedVersion: user.Version,
					Actor: retirementTestActor, ActorKind: model.ActorUser,
				})
				if !errors.Is(err, store.ErrDirectoryUnavailable) {
					t.Fatalf("retire contradictory User = %v, want unavailable", err)
				}
				if got := directoryWriterTestEpoch(t, h.raw, tenant).Version; got != beforeEpoch {
					t.Fatalf("contradictory User retirement bumped epoch to %d, want %d", got, beforeEpoch)
				}
			}
		})
	}
}

func TestDirectorySnapshotRejectsIdentitySourceTombstoneContradictionSQLite(t *testing.T) {
	ctx := context.Background()
	h := newDirectoryRetirementSQLiteHarness(t, store.Config{})
	tenant := provisionTenant(t, h.raw, "identity-source-tombstone-contradiction")
	identity := retirementCreateIdentity(t, h.raw, tenant, "identity-live-with-tombstone")
	retirementSeedDirectoryTombstone(
		t, h, tenant, model.DirectoryPrincipalIdentity,
		identity.ID, "", identity.ID,
	)
	h.enforce(t)
	ref := store.DirectoryPrincipalRef{
		PrincipalKind: model.DirectoryPrincipalIdentity, PrincipalRef: identity.ID,
	}
	_, found, err := retirementReadWitness(t, h.raw, tenant, ref)
	if !errors.Is(err, store.ErrDirectoryUnavailable) || found ||
		!strings.Contains(err.Error(), "coexists with its source") {
		t.Fatalf("Identity contradictory witness found=%t err=%v", found, err)
	}
	beforeEpoch := directoryWriterTestEpoch(t, h.raw, tenant).Version
	_, err = RetireDirectoryPrincipal(ctx, h.raw, DirectoryPrincipalRetirementRequest{
		TenantID: tenant, PrincipalKind: model.DirectoryPrincipalIdentity,
		SourceID: identity.ID, ExpectedVersion: identity.Version,
		Actor: retirementTestActor, ActorKind: model.ActorUser,
	})
	if !errors.Is(err, store.ErrDirectoryUnavailable) {
		t.Fatalf("retire contradictory Identity = %v, want unavailable", err)
	}
	if got := directoryWriterTestEpoch(t, h.raw, tenant).Version; got != beforeEpoch {
		t.Fatalf("contradictory Identity retirement bumped epoch to %d, want %d", got, beforeEpoch)
	}
	if got := retirementRowCount(
		t, h.sql, identityDescriptor.Table, "tenant_id = ? AND id = ?",
		tenant.String(), identity.ID.String(),
	); got != 1 {
		t.Fatalf("contradictory Identity source rows = %d, want 1", got)
	}
}

func TestRetireAgentRejectsContradictoryStableIdentityTombstoneSQLite(t *testing.T) {
	ctx := context.Background()
	h := newDirectoryRetirementSQLiteHarness(t, store.Config{})
	tenant := provisionTenant(t, h.raw, "agent-stable-identity-contradiction")
	identity := retirementCreateIdentity(t, h.raw, tenant, "agent-contradictory-identity")
	agent := retirementCreateAgent(
		t, h.raw, tenant, identity.ID, "", "agent-with-contradictory-identity", model.StatusActive,
	)
	retirementSeedDirectoryTombstone(
		t, h, tenant, model.DirectoryPrincipalIdentity,
		identity.ID, "", identity.ID,
	)
	h.enforce(t)
	beforeEpoch := directoryWriterTestEpoch(t, h.raw, tenant).Version
	beforeAudit := retirementRowCount(t, h.sql, auditTable, "tenant_id = ?", tenant.String())
	_, err := RetireDirectoryPrincipal(ctx, h.raw, DirectoryPrincipalRetirementRequest{
		TenantID: tenant, PrincipalKind: model.DirectoryPrincipalAgent,
		SourceID: agent.ID, ExpectedVersion: agent.Version,
		Actor: retirementTestActor, ActorKind: model.ActorUser,
	})
	if !errors.Is(err, store.ErrDirectoryUnavailable) ||
		!strings.Contains(err.Error(), "coexists with its source") {
		t.Fatalf("Agent retirement with contradictory Identity = %v, want unavailable", err)
	}
	if got := directoryWriterTestEpoch(t, h.raw, tenant).Version; got != beforeEpoch {
		t.Fatalf("contradictory stable Identity bumped epoch to %d, want %d", got, beforeEpoch)
	}
	if got := retirementRowCount(
		t, h.sql, agentDescriptor.Table, "tenant_id = ? AND id = ?",
		tenant.String(), agent.ID.String(),
	); got != 1 {
		t.Fatalf("Agent source rows after Identity contradiction = %d, want 1", got)
	}
	if got := retirementRowCount(
		t, h.sql, auditTable, "tenant_id = ?", tenant.String(),
	); got != beforeAudit {
		t.Fatalf("audit rows after Identity contradiction = %d, want %d", got, beforeAudit)
	}
}

func TestDirectorySnapshotRejectsAgentTombstoneBindingsAndOldUpdateSQLite(t *testing.T) {
	ctx := context.Background()
	h := newDirectoryRetirementSQLiteHarness(t, store.Config{})
	tenant := provisionTenant(t, h.raw, "agent-tombstone-contradiction")
	oldIdentity := retirementCreateIdentity(t, h.raw, tenant, "agent-old-identity")
	newIdentity := retirementCreateIdentity(t, h.raw, tenant, "agent-new-identity")
	agent := retirementCreateAgent(
		t, h.raw, tenant, oldIdentity.ID, "", "agent-contradictory-source", model.StatusActive,
	)
	workspace := retirementDefaultWorkspace(t, h.raw, tenant)
	tombstone := retirementSeedDirectoryTombstone(
		t, h, tenant, model.DirectoryPrincipalAgent,
		oldIdentity.ID, workspace.ID, agent.ID,
	)
	h.enforce(t)
	ref := store.DirectoryPrincipalRef{
		PrincipalKind: model.DirectoryPrincipalAgent,
		PrincipalRef:  oldIdentity.ID, WorkspaceRef: workspace.ID,
	}
	_, found, err := retirementReadWitness(t, h.raw, tenant, ref)
	if !errors.Is(err, store.ErrDirectoryUnavailable) || found ||
		!strings.Contains(err.Error(), "coexists") {
		t.Fatalf("Agent contradictory witness found=%t err=%v", found, err)
	}

	beforeEpoch := directoryWriterTestEpoch(t, h.raw, tenant).Version
	agent.IdentityID = newIdentity.ID
	err = h.raw.Mutate(ctx, tenant, func(sc store.Scope) error {
		_, updateErr := sc.Agents().Update(ctx, agent)
		return updateErr
	})
	if !errors.Is(err, store.ErrDirectoryUnavailable) {
		t.Fatalf("move Agent away from contradictory old binding = %v, want unavailable", err)
	}
	if got := directoryWriterTestEpoch(t, h.raw, tenant).Version; got != beforeEpoch {
		t.Fatalf("contradictory Agent Update bumped epoch to %d, want %d", got, beforeEpoch)
	}
	if got := retirementRowCount(
		t, h.sql, agentDescriptor.Table,
		"tenant_id = ? AND id = ? AND identity_id = ?",
		tenant.String(), agent.ID.String(), oldIdentity.ID.String(),
	); got != 1 {
		t.Fatalf("contradictory Agent old binding rows = %d, want 1", got)
	}

	_, err = RetireDirectoryPrincipal(ctx, h.raw, DirectoryPrincipalRetirementRequest{
		TenantID: tenant, PrincipalKind: model.DirectoryPrincipalAgent,
		SourceID: agent.ID, ExpectedVersion: agent.Version,
		Actor: retirementTestActor, ActorKind: model.ActorUser,
	})
	if !errors.Is(err, store.ErrDirectoryUnavailable) {
		t.Fatalf("retire Agent with prior tombstone = %v, want unavailable", err)
	}
	if got := retirementRowCount(
		t, h.sql, directoryTombstoneDescriptor.Table, "tenant_id = ? AND id = ?",
		tenant.String(), tombstone.ID.String(),
	); got != 1 {
		t.Fatalf("seeded Agent tombstone rows = %d, want 1", got)
	}
}

func TestDirectorySnapshotRejectsRetiredAgentSourceIDReboundElsewhereSQLite(t *testing.T) {
	ctx := context.Background()
	h := newDirectoryRetirementSQLiteHarness(t, store.Config{})
	tenant := provisionTenant(t, h.raw, "agent-source-id-rebound")
	retiredIdentity := retirementCreateIdentity(t, h.raw, tenant, "agent-source-retired-identity")
	otherIdentity := retirementCreateIdentity(t, h.raw, tenant, "agent-source-other-identity")
	agent := retirementCreateAgent(
		t, h.raw, tenant, retiredIdentity.ID, "", "agent-source-to-retire", model.StatusActive,
	)
	h.enforce(t)
	result, err := RetireDirectoryPrincipal(ctx, h.raw, DirectoryPrincipalRetirementRequest{
		TenantID: tenant, PrincipalKind: model.DirectoryPrincipalAgent,
		SourceID: agent.ID, ExpectedVersion: agent.Version,
		Actor: retirementTestActor, ActorKind: model.ActorUser,
	})
	if err != nil || !result.Definitive {
		t.Fatalf("retire Agent before raw resurrection = %+v err=%v", result, err)
	}
	err = h.raw.Mutate(ctx, tenant, func(sc store.Scope) error {
		ts := sc.(*tenantScope)
		if err := ts.directoryWriter.prepare(ctx, func() ([]model.TenantID, error) {
			return []model.TenantID{tenant}, nil
		}); err != nil {
			return err
		}
		record, err := agentCodec.Encode(model.Agent{
			Name: "raw-source-id-rebound", Kind: "assistant", Status: model.StatusActive,
			IdentityID: otherIdentity.ID,
		})
		if err != nil {
			return err
		}
		_, err = ts.repo(agentDescriptor).CreateWithID(ctx, agent.ID, record)
		return err
	})
	if err != nil {
		t.Fatalf("seed exact retired Agent source id rebound: %v", err)
	}
	_, found, err := retirementReadWitness(t, h.raw, tenant, result.Principal)
	if !errors.Is(err, store.ErrDirectoryUnavailable) || found ||
		!strings.Contains(err.Error(), "coexists with its source") {
		t.Fatalf("rebound Agent source witness found=%t err=%v", found, err)
	}
}

func TestDirectorySnapshotRejectsIdentityTombstoneNewAgentBindingSQLite(t *testing.T) {
	ctx := context.Background()
	h := newDirectoryRetirementSQLiteHarness(t, store.Config{})
	tenant := provisionTenant(t, h.raw, "identity-tombstone-agent-contradiction")
	identity := retirementCreateIdentity(t, h.raw, tenant, "identity-tombstone-source")
	h.enforce(t)
	result, err := RetireDirectoryPrincipal(ctx, h.raw, DirectoryPrincipalRetirementRequest{
		TenantID: tenant, PrincipalKind: model.DirectoryPrincipalIdentity,
		SourceID: identity.ID, ExpectedVersion: identity.Version,
		Actor: retirementTestActor, ActorKind: model.ActorUser,
	})
	if err != nil {
		t.Fatalf("retire Identity before corruption: %v", err)
	}
	// Simulate a legacy/raw writer that ignored the engine guard after commit.
	err = h.raw.Mutate(ctx, tenant, func(sc store.Scope) error {
		ts := sc.(*tenantScope)
		if err := ts.directoryWriter.prepare(ctx, func() ([]model.TenantID, error) {
			return []model.TenantID{tenant}, nil
		}); err != nil {
			return err
		}
		rawAgents := newTypedRepo(ts.repo(agentDescriptor), agentCodec)
		_, err := rawAgents.Create(ctx, model.Agent{
			Name: "raw-post-retirement-binding", Kind: "assistant",
			Status: model.StatusActive, IdentityID: identity.ID,
		})
		return err
	})
	if err != nil {
		t.Fatalf("seed raw post-retirement Agent binding: %v", err)
	}
	_, found, err := retirementReadWitness(t, h.raw, tenant, result.Principal)
	if !errors.Is(err, store.ErrDirectoryUnavailable) || found ||
		!strings.Contains(err.Error(), "Agent binding") {
		t.Fatalf("Identity tombstone with new Agent found=%t err=%v", found, err)
	}
}
