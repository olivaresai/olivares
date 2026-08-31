// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

const (
	communicationGuardWorkspaceInitializerKey = "sessions.communication_guard.v1"
	communicationGuardWorkspacePageSize       = 128
)

// CommunicationGuardReconcileMode distinguishes upgrade repair from the final
// enforced witness. Staged may create or advance guards. Enforced is verify-only:
// a missing or lagging guard is UNKNOWN and is never repaired as a side effect of
// a readiness check.
type CommunicationGuardReconcileMode string

const (
	CommunicationGuardReconcileStaged   CommunicationGuardReconcileMode = "staged"
	CommunicationGuardReconcileEnforced CommunicationGuardReconcileMode = "enforced"
)

func (m CommunicationGuardReconcileMode) valid() bool {
	return m == CommunicationGuardReconcileStaged || m == CommunicationGuardReconcileEnforced
}

type communicationGuardReconcileAction uint8

const (
	communicationGuardNoop communicationGuardReconcileAction = iota
	communicationGuardCreate
	communicationGuardUpdate
)

type communicationGuardReconcilePlan struct {
	Action   communicationGuardReconcileAction
	Before   *CommunicationGuard
	After    CommunicationGuard
	Required int64
}

type communicationGuardReconcileInput struct {
	Kind        CommunicationGuardKind
	Tenant      model.TenantID
	Workspace   model.ID
	Existing    *CommunicationGuard
	ObservedMax int64
	// LatestSourceDBTime is max(updated_at) over the complete source set. It is
	// deliberately independent of the row that supplied ObservedMax: a lower
	// sequence row written by a rolled-forward clock is still evidence that the
	// database clock has moved backwards.
	LatestSourceDBTime time.Time
	DBNow              time.Time
	CreateID           model.ID
	Mode               CommunicationGuardReconcileMode
}

// planCommunicationGuardReconcile is pure: it computes max(source)+1, refuses
// overflow and clock rollback, and never lowers an ahead guard. ID allocation is
// supplied by the caller so tests and retries do not hide nondeterminism here.
func planCommunicationGuardReconcile(
	in communicationGuardReconcileInput,
) (communicationGuardReconcilePlan, error) {
	if !in.Mode.valid() || !in.Kind.Valid() ||
		!validCanonicalCommunicationTenant(in.Tenant) ||
		!validCanonicalCommunicationID(in.Workspace) ||
		in.ObservedMax < 0 || in.ObservedMax >= math.MaxInt64-1 || in.DBNow.IsZero() ||
		(in.ObservedMax > 0 && in.LatestSourceDBTime.IsZero()) {
		return communicationGuardReconcilePlan{}, communicationError(
			ErrInvalidCommunicationTransition,
			"communication guard reconcile input is invalid or overflows",
		)
	}
	required := in.ObservedMax + 1
	if !in.LatestSourceDBTime.IsZero() && in.DBNow.Before(in.LatestSourceDBTime) {
		return communicationGuardReconcilePlan{}, communicationError(
			ErrCommunicationEvidenceUnknown,
			"communication guard %q maximum source row carries future database time", in.Kind,
		)
	}
	if in.Existing == nil {
		if in.Mode == CommunicationGuardReconcileEnforced {
			return communicationGuardReconcilePlan{}, communicationError(
				ErrCommunicationEvidenceUnknown,
				"communication guard %q is missing in enforced mode", in.Kind,
			)
		}
		if !validCanonicalCommunicationID(in.CreateID) {
			return communicationGuardReconcilePlan{}, communicationError(
				ErrInvalidCommunicationTransition,
				"communication guard %q create ID is invalid", in.Kind,
			)
		}
		after := CommunicationGuard{
			MutableCommunicationEntity: MutableCommunicationEntity{
				CommunicationEntity: CommunicationEntity{
					ID: in.CreateID, TenantID: in.Tenant, WorkspaceID: in.Workspace,
					Version: 1, CreatedAt: in.DBNow,
				},
				UpdatedAt: in.DBNow,
			},
			Kind: in.Kind, NextSeq: required, LastDBTime: in.DBNow,
		}
		if err := ValidateCommunicationGuard(after); err != nil {
			return communicationGuardReconcilePlan{}, err
		}
		return communicationGuardReconcilePlan{
			Action: communicationGuardCreate, After: after, Required: required,
		}, nil
	}

	before := *in.Existing
	if err := ValidateCommunicationGuard(before); err != nil {
		return communicationGuardReconcilePlan{}, err
	}
	if before.Kind != in.Kind || before.TenantID != in.Tenant ||
		before.WorkspaceID != in.Workspace {
		return communicationGuardReconcilePlan{}, communicationError(
			ErrCommunicationEvidenceUnknown,
			"communication guard %q has the wrong identity or lineage", in.Kind,
		)
	}
	if before.NextSeq == math.MaxInt64 {
		return communicationGuardReconcilePlan{}, communicationError(
			ErrCommunicationEvidenceUnknown,
			"communication guard %q sequence space is exhausted", in.Kind,
		)
	}
	if in.DBNow.Before(before.LastDBTime) || in.DBNow.Before(before.UpdatedAt) {
		return communicationGuardReconcilePlan{}, communicationError(
			ErrCommunicationEvidenceUnknown,
			"communication guard %q carries future database time", in.Kind,
		)
	}
	// Sequence and database-time evidence are independent high-water marks. A
	// guard may be ahead of max(sequence)+1 while a lower-sequence source row was
	// updated later; treating sequence coverage alone as a noop would discard that
	// clock observation and let a subsequent rollback pass the publish fence.
	if before.NextSeq >= required && !before.LastDBTime.Before(in.LatestSourceDBTime) {
		return communicationGuardReconcilePlan{
			Action: communicationGuardNoop, Before: &before, After: before, Required: required,
		}, nil
	}
	if in.Mode == CommunicationGuardReconcileEnforced {
		return communicationGuardReconcilePlan{}, communicationError(
			ErrCommunicationEvidenceUnknown,
			"communication guard %q does not cover source: next=%d required=%d last_db_time=%s latest_source_db_time=%s",
			in.Kind, before.NextSeq, required, before.LastDBTime, in.LatestSourceDBTime,
		)
	}
	after := before
	after.Version++
	after.UpdatedAt = in.DBNow
	after.LastDBTime = in.DBNow
	if after.NextSeq < required {
		after.NextSeq = required
	}
	if err := ValidateCommunicationGuard(after); err != nil {
		return communicationGuardReconcilePlan{}, err
	}
	return communicationGuardReconcilePlan{
		Action: communicationGuardUpdate, Before: &before, After: after, Required: required,
	}, nil
}

type communicationGuardBootstrapScope interface {
	Tenant() model.TenantID
	store.TransactionClock
	store.TransactionLocker
	GuardRepository() (store.GenericRepo, error)
	Source(model.Kind) (communicationGuardSourceReader, error)
}

type communicationGuardRawScope interface {
	Tenant() model.TenantID
	store.TransactionClock
	store.TransactionLocker
	Ext(model.Kind) (store.GenericRepo, error)
}

type communicationGuardSourceReader interface {
	List(context.Context, model.Query) ([]model.Record, model.Page, error)
}

// communicationGuardSourceList exposes a single read method. The underlying
// GenericRepo is captured only as a List closure, so its dynamic type cannot be
// asserted back to GenericRepo or TransactionStampedGenericRepo by the module.
type communicationGuardSourceList struct {
	list func(context.Context, model.Query) ([]model.Record, model.Page, error)
}

var _ communicationGuardSourceReader = (*communicationGuardSourceList)(nil)

func (r *communicationGuardSourceList) List(
	ctx context.Context,
	query model.Query,
) ([]model.Record, model.Page, error) {
	return r.list(ctx, query)
}

type communicationGuardBootstrapRepo interface {
	store.TransactionStampedGenericRepo
	store.RowLocker[model.Record]
}

// CommunicationGuardReconciliationData is the deliberately opaque data handle
// used only by the upgrade reconciler. Its representation is closures only: it
// retains no api.ModuleData field and never exposes a store.Scope to module
// reconciliation code.
type CommunicationGuardReconciliationData struct {
	listWorkspacePageFn func(
		context.Context, model.TenantID, model.Query,
	) ([]model.Workspace, model.Page, error)
	mutateWorkspaceFn func(
		context.Context,
		model.TenantID,
		model.ID,
		func(communicationGuardBootstrapScope) error,
	) error
}

// communicationGuardReconciliationScope is the complete authority handed to
// the module reconciler. It intentionally does not embed or implement
// store.Scope. Ext is an exact allowlist over the three guard-bootstrap kinds.
type communicationGuardReconciliationScope struct {
	tenant          model.TenantID
	transactionNow  func(context.Context) (model.Timestamp, error)
	lockTransaction func(context.Context, string) error
	guardRepo       store.GenericRepo
	channelSource   communicationGuardSourceReader
	deliverySource  communicationGuardSourceReader
}

var _ communicationGuardBootstrapScope = (*communicationGuardReconciliationScope)(nil)

func (s *communicationGuardReconciliationScope) Tenant() model.TenantID { return s.tenant }

func (s *communicationGuardReconciliationScope) TransactionNow(
	ctx context.Context,
) (model.Timestamp, error) {
	return s.transactionNow(ctx)
}

func (s *communicationGuardReconciliationScope) LockTransaction(
	ctx context.Context,
	key string,
) error {
	return s.lockTransaction(ctx, key)
}

func (s *communicationGuardReconciliationScope) GuardRepository() (store.GenericRepo, error) {
	if s == nil || s.guardRepo == nil {
		return nil, store.ErrStoreUnavailable
	}
	return s.guardRepo, nil
}

func (s *communicationGuardReconciliationScope) Source(
	kind model.Kind,
) (communicationGuardSourceReader, error) {
	switch kind {
	case channelKind:
		return s.channelSource, nil
	case messageDeliveryKind:
		return s.deliverySource, nil
	default:
		return nil, fmt.Errorf("%w: communication guard source kind %s", store.ErrUnknownEntity, kind)
	}
}

func newCommunicationGuardNarrowScope(
	raw communicationGuardRawScope,
) (*communicationGuardReconciliationScope, error) {
	if raw == nil {
		return nil, store.ErrStoreUnavailable
	}
	guardRepo, err := raw.Ext(communicationGuardKind)
	if err != nil {
		return nil, err
	}
	channelRepo, err := raw.Ext(channelKind)
	if err != nil {
		return nil, err
	}
	deliveryRepo, err := raw.Ext(messageDeliveryKind)
	if err != nil {
		return nil, err
	}
	return &communicationGuardReconciliationScope{
		tenant: raw.Tenant(), transactionNow: raw.TransactionNow,
		lockTransaction: raw.LockTransaction, guardRepo: guardRepo,
		channelSource:  &communicationGuardSourceList{list: channelRepo.List},
		deliverySource: &communicationGuardSourceList{list: deliveryRepo.List},
	}, nil
}

type communicationGuardReconciliationData interface {
	listWorkspacePage(
		context.Context, model.TenantID, model.Query,
	) ([]model.Workspace, model.Page, error)
	mutateWorkspace(
		context.Context,
		model.TenantID,
		model.ID,
		func(communicationGuardBootstrapScope) error,
	) error
}

var _ communicationGuardReconciliationData = (*CommunicationGuardReconciliationData)(nil)

// NewCommunicationGuardReconciliationData narrows a tenant-scoped module data
// handle to the two operations guard upgrade needs: short workspace pages and
// one workspace-confined mutation at a time.
func NewCommunicationGuardReconciliationData(
	data api.ModuleData,
) *CommunicationGuardReconciliationData {
	if data == nil {
		return nil
	}
	view := data.View
	mutate := data.Mutate
	return &CommunicationGuardReconciliationData{
		listWorkspacePageFn: func(
			ctx context.Context,
			tenant model.TenantID,
			query model.Query,
		) ([]model.Workspace, model.Page, error) {
			var (
				workspaces []model.Workspace
				page       model.Page
			)
			err := view(ctx, tenant, func(scope store.Scope) error {
				var err error
				workspaces, page, err = scope.Workspaces().List(ctx, query)
				return err
			})
			return workspaces, page, err
		},
		mutateWorkspaceFn: func(
			ctx context.Context,
			tenant model.TenantID,
			workspace model.ID,
			fn func(communicationGuardBootstrapScope) error,
		) error {
			return mutate(ctx, tenant, func(raw store.Scope) error {
				confined, err := store.ConfineWorkspace(ctx, raw, workspace)
				if err != nil {
					return fmt.Errorf("confine communication guard workspace %s: %w", workspace, err)
				}
				clock, ok := confined.(store.TransactionClock)
				if !ok {
					return communicationError(
						ErrCommunicationEvidenceUnknown,
						"workspace %s lacks communication guard transaction clock", workspace,
					)
				}
				locker, ok := confined.(store.TransactionLocker)
				if !ok {
					return communicationError(
						ErrCommunicationEvidenceUnknown,
						"workspace %s lacks communication guard transaction lock", workspace,
					)
				}
				if confined.Tenant() != tenant {
					return communicationError(
						ErrCommunicationEvidenceUnknown,
						"workspace %s changed communication guard tenant lineage", workspace,
					)
				}
				adapter, err := newCommunicationGuardNarrowScope(struct {
					store.Scope
					store.TransactionClock
					store.TransactionLocker
				}{confined, clock, locker})
				if err != nil {
					return err
				}
				return fn(adapter)
			})
		},
	}
}

func (d *CommunicationGuardReconciliationData) listWorkspacePage(
	ctx context.Context,
	tenant model.TenantID,
	query model.Query,
) ([]model.Workspace, model.Page, error) {
	if d == nil || d.listWorkspacePageFn == nil {
		return nil, model.Page{}, store.ErrStoreUnavailable
	}
	return d.listWorkspacePageFn(ctx, tenant, query)
}

func (d *CommunicationGuardReconciliationData) mutateWorkspace(
	ctx context.Context,
	tenant model.TenantID,
	workspace model.ID,
	fn func(communicationGuardBootstrapScope) error,
) error {
	if d == nil || d.mutateWorkspaceFn == nil {
		return store.ErrStoreUnavailable
	}
	return d.mutateWorkspaceFn(ctx, tenant, workspace, fn)
}

// initializeCommunicationWorkspace is registered with the core engine. Fresh
// workspaces always use staged semantics because there is no legacy estate to
// attest; the enclosing workspace insert and both guard writes commit together.
func initializeCommunicationWorkspace(
	ctx context.Context,
	scope store.WorkspaceInitializationScope,
) error {
	if scope == nil {
		return communicationError(ErrCommunicationEvidenceUnknown, "workspace initializer scope is nil")
	}
	workspace := scope.Workspace()
	narrow, err := newCommunicationGuardNarrowScope(scope)
	if err != nil {
		return err
	}
	return reconcileCommunicationGuardsInScope(
		ctx, narrow, workspace.ID, CommunicationGuardReconcileStaged,
	)
}

// ReconcileCommunicationGuards reconciles every non-deleted workspace of tenant
// with bounded transactions: one short read transaction per workspace page and
// exactly one mutation per workspace. A late failure therefore preserves prior
// progress, and a retry converges through no-op plans instead of restarting one
// unbounded tenant-wide transaction. It neither enumerates tenants nor claims
// global readiness.
func (m *Module) ReconcileCommunicationGuards(
	ctx context.Context,
	tenant model.TenantID,
	mode CommunicationGuardReconcileMode,
) error {
	if m == nil || m.communicationGuardData == nil {
		return store.ErrStoreUnavailable
	}
	if !mode.valid() || !validCanonicalCommunicationTenant(tenant) {
		return communicationError(
			ErrInvalidCommunicationTransition, "communication guard reconcile request is invalid",
		)
	}
	query := model.Query{Limit: communicationGuardWorkspacePageSize}
	seenCursors := make(map[string]struct{})
	seenWorkspaces := make(map[model.ID]struct{})
	for {
		workspaces, page, err := m.communicationGuardData.listWorkspacePage(ctx, tenant, query)
		if err != nil {
			return fmt.Errorf("list communication guard workspaces: %w", err)
		}
		if page.HasMore {
			if len(workspaces) == 0 {
				return communicationError(
					ErrCommunicationEvidenceUnknown,
					"workspace enumeration returned an empty continuation page",
				)
			}
			if page.Cursor == "" || page.Cursor == query.Cursor {
				return communicationError(
					ErrCommunicationEvidenceUnknown,
					"workspace enumeration cannot make progress",
				)
			}
			if _, repeated := seenCursors[page.Cursor]; repeated {
				return communicationError(
					ErrCommunicationEvidenceUnknown,
					"workspace enumeration repeated a prior cursor",
				)
			}
		}
		for _, workspace := range workspaces {
			if workspace.TenantID != tenant || !validCanonicalCommunicationID(workspace.ID) {
				return communicationError(
					ErrCommunicationEvidenceUnknown,
					"workspace enumeration returned invalid lineage for tenant %s", tenant,
				)
			}
			if _, duplicate := seenWorkspaces[workspace.ID]; duplicate {
				return communicationError(
					ErrCommunicationEvidenceUnknown,
					"workspace enumeration repeated workspace %s", workspace.ID,
				)
			}
			seenWorkspaces[workspace.ID] = struct{}{}
		}
		for _, workspace := range workspaces {
			if err := m.communicationGuardData.mutateWorkspace(
				ctx, tenant, workspace.ID,
				func(scope communicationGuardBootstrapScope) error {
					return reconcileCommunicationGuardsInScope(ctx, scope, workspace.ID, mode)
				},
			); err != nil {
				return fmt.Errorf("workspace %s: %w", workspace.ID, err)
			}
		}
		if !page.HasMore {
			return nil
		}
		seenCursors[page.Cursor] = struct{}{}
		query.Cursor = page.Cursor
	}
}

// VerifyCommunicationGuards runs the enforced, verify-only path for one tenant.
// A caller may compose it into a store-readiness witness only after it has
// enumerated the complete tenant set through an engine-owned System seam.
func (m *Module) VerifyCommunicationGuards(
	ctx context.Context,
	tenant model.TenantID,
) error {
	return m.ReconcileCommunicationGuards(ctx, tenant, CommunicationGuardReconcileEnforced)
}

func reconcileCommunicationGuardsInScope(
	ctx context.Context,
	scope communicationGuardBootstrapScope,
	workspace model.ID,
	mode CommunicationGuardReconcileMode,
) error {
	if scope == nil || !mode.valid() || !validCanonicalCommunicationTenant(scope.Tenant()) ||
		!validCanonicalCommunicationID(workspace) {
		return communicationError(
			ErrInvalidCommunicationTransition, "communication guard bootstrap scope is invalid",
		)
	}
	lockKey := fmt.Sprintf(
		"sessions.communication_guard.bootstrap.v1:%s:%s", scope.Tenant(), workspace,
	)
	if err := scope.LockTransaction(ctx, lockKey); err != nil {
		return communicationError(
			ErrCommunicationEvidenceUnknown, "acquire communication guard bootstrap lock: %v", err,
		)
	}
	guardRepo, err := communicationGuardBootstrapRepository(scope)
	if err != nil {
		return err
	}

	// This order is shared with publish: route first, delivery second. The
	// bootstrap lock serializes the missing-row case, while row locks serialize
	// reconciliation with normal writers once the rows exist.
	routeGuard, err := lockOptionalCommunicationGuard(
		ctx, guardRepo, CommunicationGuardRouteRevision,
	)
	if err != nil {
		return err
	}
	deliveryGuard, err := lockOptionalCommunicationGuard(
		ctx, guardRepo, CommunicationGuardDeliverySequence,
	)
	if err != nil {
		return err
	}

	routeSource, err := observeCommunicationGuardSource(ctx, scope, channelKind)
	if err != nil {
		return fmt.Errorf("read maximum Channel route revision: %w", err)
	}
	deliverySource, err := observeCommunicationGuardSource(ctx, scope, messageDeliveryKind)
	if err != nil {
		return fmt.Errorf("read maximum MessageDelivery sequence: %w", err)
	}
	dbNow, err := scope.TransactionNow(ctx)
	if err != nil {
		return communicationError(
			ErrCommunicationEvidenceUnknown, "observe communication guard database time: %v", err,
		)
	}

	routePlan, err := planCommunicationGuardReconcile(communicationGuardReconcileInput{
		Kind: CommunicationGuardRouteRevision, Tenant: scope.Tenant(), Workspace: workspace,
		Existing: routeGuard, ObservedMax: routeSource.Max,
		LatestSourceDBTime: routeSource.LatestDBTime, DBNow: dbNow.Time(),
		CreateID: model.NewID(), Mode: mode,
	})
	if err != nil {
		return err
	}
	deliveryPlan, err := planCommunicationGuardReconcile(communicationGuardReconcileInput{
		Kind: CommunicationGuardDeliverySequence, Tenant: scope.Tenant(), Workspace: workspace,
		Existing: deliveryGuard, ObservedMax: deliverySource.Max,
		LatestSourceDBTime: deliverySource.LatestDBTime, DBNow: dbNow.Time(),
		CreateID: model.NewID(), Mode: mode,
	})
	if err != nil {
		return err
	}

	if err := applyCommunicationGuardReconcile(ctx, guardRepo, routePlan); err != nil {
		return fmt.Errorf("apply route revision guard: %w", err)
	}
	if err := applyCommunicationGuardReconcile(ctx, guardRepo, deliveryPlan); err != nil {
		return fmt.Errorf("apply delivery sequence guard: %w", err)
	}
	return verifyCommunicationGuardsInScope(
		ctx, guardRepo, scope.Tenant(), workspace, routePlan.Required, deliveryPlan.Required,
	)
}

func communicationGuardBootstrapRepository(
	scope communicationGuardBootstrapScope,
) (communicationGuardBootstrapRepo, error) {
	repo, err := scope.GuardRepository()
	if err != nil {
		return nil, err
	}
	stamped, ok := repo.(store.TransactionStampedGenericRepo)
	if !ok {
		return nil, communicationError(
			ErrCommunicationEvidenceUnknown,
			"communication guard repository lacks transaction-stamped writes",
		)
	}
	locker, ok := repo.(store.RowLocker[model.Record])
	if !ok {
		return nil, communicationError(
			ErrCommunicationEvidenceUnknown, "communication guard repository lacks row locks",
		)
	}
	return struct {
		store.TransactionStampedGenericRepo
		store.RowLocker[model.Record]
	}{stamped, locker}, nil
}

func lockOptionalCommunicationGuard(
	ctx context.Context,
	repo communicationGuardBootstrapRepo,
	kind CommunicationGuardKind,
) (*CommunicationGuard, error) {
	rows, page, err := repo.List(ctx, model.Query{
		Filters: []model.Filter{{Column: colCommGuardKind, Op: model.OpEq, Value: string(kind)}},
		Limit:   2,
	})
	if err != nil {
		return nil, err
	}
	if page.HasMore || len(rows) > 1 {
		return nil, communicationError(
			ErrCommunicationEvidenceUnknown, "communication guard %q is duplicated", kind,
		)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	id, err := model.ParseID(rows[0].String(model.ColID))
	if err != nil || !validCanonicalCommunicationID(id) {
		return nil, communicationError(
			ErrCommunicationEvidenceUnknown, "communication guard %q has an invalid ID", kind,
		)
	}
	locked, err := repo.Lock(ctx, id)
	if err != nil {
		return nil, err
	}
	guard, err := communicationGuardFromRecord(locked)
	if err != nil {
		return nil, err
	}
	if guard.Kind != kind {
		return nil, communicationError(
			ErrCommunicationEvidenceUnknown, "locked communication guard changed identity",
		)
	}
	return &guard, nil
}

type communicationGuardSourceObservation struct {
	Max          int64
	LatestDBTime time.Time
}

func observeCommunicationGuardSource(
	ctx context.Context,
	scope communicationGuardBootstrapScope,
	kind model.Kind,
) (communicationGuardSourceObservation, error) {
	reader, err := scope.Source(kind)
	if err != nil {
		return communicationGuardSourceObservation{}, err
	}
	var sequenceColumn string
	switch kind {
	case channelKind:
		sequenceColumn = colCommRouteRevision
	case messageDeliveryKind:
		sequenceColumn = colCommDeliverySeq
	default:
		return communicationGuardSourceObservation{}, communicationError(
			ErrInvalidCommunicationTransition, "unsupported communication guard source %s", kind,
		)
	}
	// Two independent, index-backed probes keep lock time and retry work bounded
	// regardless of source cardinality. The descriptor owns exact tenant/workspace
	// sort indexes with an id tiebreaker for non-unique values; DeliverySeq's
	// existing unique index needs no id suffix. Schema reconciliation creates each
	// newly declared index additively on upgrades.
	sequenceRows, _, err := reader.List(ctx, model.Query{
		Sort: []model.Sort{
			{Column: sequenceColumn, Desc: true},
			{Column: model.ColID, Desc: true},
		},
		Limit: 1, IncludeDeleted: true,
	})
	if err != nil {
		return communicationGuardSourceObservation{}, err
	}
	latestRows, _, err := reader.List(ctx, model.Query{
		Sort: []model.Sort{
			{Column: model.ColUpdatedAt, Desc: true},
			{Column: model.ColID, Desc: true},
		},
		Limit: 1, IncludeDeleted: true,
	})
	if err != nil {
		return communicationGuardSourceObservation{}, err
	}
	if len(sequenceRows) > 1 || len(latestRows) > 1 ||
		(len(sequenceRows) == 0) != (len(latestRows) == 0) {
		return communicationGuardSourceObservation{}, communicationError(
			ErrCommunicationEvidenceUnknown,
			"communication guard source probes returned an inconsistent bounded result",
		)
	}
	if len(sequenceRows) == 0 {
		return communicationGuardSourceObservation{}, nil
	}
	sequence, _, err := communicationGuardSourceValues(kind, sequenceRows[0])
	if err != nil {
		return communicationGuardSourceObservation{}, err
	}
	_, latest, err := communicationGuardSourceValues(kind, latestRows[0])
	if err != nil {
		return communicationGuardSourceObservation{}, err
	}
	return communicationGuardSourceObservation{Max: sequence, LatestDBTime: latest}, nil
}

func communicationGuardSourceValues(
	kind model.Kind,
	record model.Record,
) (int64, time.Time, error) {
	switch kind {
	case channelKind:
		channel, err := channelFromRecord(record)
		if err != nil {
			return 0, time.Time{}, err
		}
		return channel.RouteRevision, channel.UpdatedAt, nil
	case messageDeliveryKind:
		delivery, err := messageDeliveryFromRecord(record)
		if err != nil {
			return 0, time.Time{}, err
		}
		return delivery.DeliverySeq, delivery.UpdatedAt, nil
	default:
		return 0, time.Time{}, communicationError(
			ErrInvalidCommunicationTransition, "unsupported communication guard source %s", kind,
		)
	}
}

func applyCommunicationGuardReconcile(
	ctx context.Context,
	repo communicationGuardBootstrapRepo,
	plan communicationGuardReconcilePlan,
) error {
	switch plan.Action {
	case communicationGuardNoop:
		return nil
	case communicationGuardCreate:
		record, err := communicationGuardToRecord(plan.After)
		if err != nil {
			return err
		}
		created, err := repo.CreateWithIDAtTransactionTime(ctx, plan.After.ID, record)
		if err != nil {
			return err
		}
		persisted, err := communicationGuardFromRecord(created)
		if err != nil {
			return err
		}
		if persisted.Kind != plan.After.Kind || persisted.NextSeq != plan.After.NextSeq {
			return communicationError(
				ErrCommunicationEvidenceUnknown, "created communication guard differs from plan",
			)
		}
		return nil
	case communicationGuardUpdate:
		if plan.Before == nil {
			return communicationError(
				ErrInvalidCommunicationTransition, "communication guard update lacks prior state",
			)
		}
		record, err := communicationGuardToRecord(plan.After)
		if err != nil {
			return err
		}
		// UpdateAtTransactionTime performs the version increment. Its input is the
		// compare-and-swap version, not the planned post-write version.
		record[model.ColVersion] = plan.Before.Version
		updated, err := repo.UpdateAtTransactionTime(ctx, record)
		if err != nil {
			return err
		}
		persisted, err := communicationGuardFromRecord(updated)
		if err != nil {
			return err
		}
		if persisted.Version != plan.After.Version || persisted.Kind != plan.After.Kind ||
			persisted.NextSeq != plan.After.NextSeq {
			return communicationError(
				ErrCommunicationEvidenceUnknown, "updated communication guard differs from plan",
			)
		}
		return nil
	default:
		return communicationError(
			ErrInvalidCommunicationTransition, "unknown communication guard reconcile action",
		)
	}
}

func verifyCommunicationGuardsInScope(
	ctx context.Context,
	repo communicationGuardBootstrapRepo,
	tenant model.TenantID,
	workspace model.ID,
	routeRequired int64,
	deliveryRequired int64,
) error {
	rows, page, err := repo.List(ctx, model.Query{Limit: 3, IncludeDeleted: true})
	if err != nil {
		return err
	}
	if page.HasMore || len(rows) != 2 {
		return communicationError(
			ErrCommunicationEvidenceUnknown,
			"workspace communication guard set has %d rows, want exactly two", len(rows),
		)
	}
	seen := map[CommunicationGuardKind]bool{}
	for _, row := range rows {
		guard, err := communicationGuardFromRecord(row)
		if err != nil {
			return err
		}
		if guard.TenantID != tenant || guard.WorkspaceID != workspace || seen[guard.Kind] {
			return communicationError(
				ErrCommunicationEvidenceUnknown, "workspace communication guard set is inconsistent",
			)
		}
		seen[guard.Kind] = true
		required := routeRequired
		if guard.Kind == CommunicationGuardDeliverySequence {
			required = deliveryRequired
		}
		if guard.NextSeq < required {
			return communicationError(
				ErrCommunicationEvidenceUnknown,
				"communication guard %q remains behind after reconcile", guard.Kind,
			)
		}
	}
	if !seen[CommunicationGuardRouteRevision] || !seen[CommunicationGuardDeliverySequence] {
		return communicationError(
			ErrCommunicationEvidenceUnknown,
			"workspace communication guard set lacks a required kind",
		)
	}
	return nil
}
