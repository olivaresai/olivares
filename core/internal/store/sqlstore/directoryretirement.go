// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/google/uuid"
	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// ErrDirectoryRetirementAuthority means a caller tried to recover the
// irreversible retirement capability through a decorated or substituted
// Store. Only the undecorated *sqlStore returned by Open owns this seam.
var ErrDirectoryRetirementAuthority = errors.New("directory retirement requires raw engine authority")

// DirectoryRetirementCode is a closed result vocabulary. A remaining binding
// is uncertainty about principal retirement, not uncertainty about which
// principal was deleted: an illegible source identity is refused and rolled
// back instead of being physically deleted.
type DirectoryRetirementCode string

const (
	DirectoryRetirementDefinitive          DirectoryRetirementCode = "definitive"
	DirectoryRetirementAgentBindingRemains DirectoryRetirementCode = "agent_bindings_remain"
)

// UserRetirementRequest is the already-validated internal input to the global
// User ceremony.
type UserRetirementRequest struct {
	UserID          model.ID
	ExpectedVersion int64
	Actor           string
	ActorKind       string
}

// DirectoryPrincipalRetirementRequest names a tenant-local source row. The
// canonical recipient is never supplied: Identity derives it from its own ID;
// Agent derives it from Agent.IdentityID plus effective workspace.
type DirectoryPrincipalRetirementRequest struct {
	TenantID        model.TenantID
	PrincipalKind   model.DirectoryPrincipalKind
	SourceID        model.ID
	ExpectedVersion int64
	Actor           string
	ActorKind       string
}

// DirectoryPrincipalRetirementResult distinguishes definitive principal
// retirement from physical retirement of one of several stable Agent bindings.
// Only the definitive result carries a Tombstone.
type DirectoryPrincipalRetirementResult struct {
	Code           DirectoryRetirementCode
	Definitive     bool
	Principal      store.DirectoryPrincipalRef
	SourceKind     model.Kind
	SourceID       model.ID
	ResultingEpoch int64
	Tombstone      *model.DirectoryTombstone
	AuditEventID   model.ID
	AuditSeq       int64
	AuditHash      []byte
}

// Test-only fault seams. Production leaves them nil; each sits after a real
// control boundary so a regression cannot pass by replacing the path it is
// meant to discriminate.
var (
	directoryRetirementAfterAuditTestHook   func(*model.AuditEvent)
	directoryRetirementBeforeFinishTestHook func(model.DirectoryPrincipalKind, model.ID) error
)

// RetireUser executes the engine-owned global retirement ceremony. raw must be
// the undecorated SQL store; the public core/engine entry point performs the
// same validation before entering this package, and this layer repeats the
// security-relevant checks so an internal caller cannot bypass them.
func RetireUser(
	ctx context.Context,
	raw store.Store,
	req UserRetirementRequest,
) (model.UserTombstone, error) {
	s, ok := raw.(*sqlStore)
	if !ok {
		return model.UserTombstone{}, ErrDirectoryRetirementAuthority
	}
	if err := validateUserRetirementRequest(req); err != nil {
		return model.UserTombstone{}, err
	}
	var out model.UserTombstone
	err := s.System(ctx, func(scope store.SystemScope) error {
		sys, ok := scope.(*systemScope)
		if !ok {
			return ErrDirectoryRetirementAuthority
		}
		var err error
		out, err = sys.retireUser(ctx, req)
		return err
	})
	return out, err
}

// RetireDirectoryPrincipal executes the tenant-local Identity/Agent ceremony.
func RetireDirectoryPrincipal(
	ctx context.Context,
	raw store.Store,
	req DirectoryPrincipalRetirementRequest,
) (DirectoryPrincipalRetirementResult, error) {
	s, ok := raw.(*sqlStore)
	if !ok {
		return DirectoryPrincipalRetirementResult{}, ErrDirectoryRetirementAuthority
	}
	if err := validateDirectoryPrincipalRetirementRequest(req); err != nil {
		return DirectoryPrincipalRetirementResult{}, err
	}
	var out DirectoryPrincipalRetirementResult
	err := s.System(ctx, func(scope store.SystemScope) error {
		sys, ok := scope.(*systemScope)
		if !ok {
			return ErrDirectoryRetirementAuthority
		}
		var err error
		out, err = sys.retireDirectoryPrincipal(ctx, req)
		return err
	})
	return out, err
}

func validateUserRetirementRequest(req UserRetirementRequest) error {
	if err := validateRetirementInput(req.UserID, req.ExpectedVersion, req.Actor, req.ActorKind); err != nil {
		return fmt.Errorf("retire user: %w", err)
	}
	return nil
}

func validateDirectoryPrincipalRetirementRequest(req DirectoryPrincipalRetirementRequest) error {
	if req.PrincipalKind != model.DirectoryPrincipalIdentity &&
		req.PrincipalKind != model.DirectoryPrincipalAgent {
		return fmt.Errorf("%w: directory retirement kind must be identity or agent",
			store.ErrInvalidDescriptor)
	}
	if err := (model.DirectoryEpoch{BaseFields: model.BaseFields{
		ID: model.ID(req.TenantID), TenantID: req.TenantID, Version: 1,
	}}).Validate(); err != nil {
		return fmt.Errorf("%w: invalid directory retirement tenant: %v",
			store.ErrInvalidDescriptor, err)
	}
	if err := validateRetirementInput(
		req.SourceID, req.ExpectedVersion, req.Actor, req.ActorKind,
	); err != nil {
		return fmt.Errorf("retire directory principal: %w", err)
	}
	return nil
}

func validateRetirementInput(
	id model.ID,
	expectedVersion int64,
	actor string,
	actorKind string,
) error {
	if err := validateCreateID(id); err != nil {
		return err
	}
	if expectedVersion < 1 {
		return fmt.Errorf("%w: expected version must be positive", store.ErrInvalidDescriptor)
	}
	if strings.TrimSpace(actor) == "" || strings.TrimSpace(actor) != actor ||
		strings.TrimSpace(actorKind) == "" || strings.TrimSpace(actorKind) != actorKind {
		return fmt.Errorf("%w: actor and actor kind must be non-empty and canonical",
			store.ErrInvalidDescriptor)
	}
	return nil
}

func (sys *systemScope) retireUser(
	ctx context.Context,
	req UserRetirementRequest,
) (_ model.UserTombstone, retErr error) {
	defer func() { sys.poison(retErr) }()
	if !sys.s.elector.active() {
		return model.UserTombstone{}, store.ErrNotLeader
	}
	writer, err := acquireDirectoryWriter(ctx, sys.tx, sys.s.dia)
	if err != nil {
		return model.UserTombstone{}, err
	}
	if writer.Mode != directoryWriterEnforced {
		return model.UserTombstone{}, fmt.Errorf(
			"%w: current mode is %s", store.ErrDirectoryRetirementNotEnforced, writer.Mode,
		)
	}

	// A global User tombstone must answer historical deliveries even after the
	// user has lost every current membership. Enumerate every real organization,
	// not the user's present memberships. PostgreSQL may call this authoritative
	// only through the separate boot-attested BYPASSRLS admin pool. CreateOrg and
	// DropTenant take the same writer lock, so this estate is stable until commit.
	queryer := directoryTenantEnumerator(sys.tx)
	var adminTx *sql.Tx
	if sys.s.dia.Name() == store.EnginePostgres {
		if sys.s.adminDB == nil || sys.s.adminDB == sys.s.db {
			return model.UserTombstone{}, fmt.Errorf(
				"%w: RetireUser requires AdminDSN to enumerate every real tenant",
				store.ErrEnumerationNotAuthoritative,
			)
		}
		adminTx, err = sys.s.adminDB.BeginTx(ctx, &sql.TxOptions{
			Isolation: sql.LevelRepeatableRead,
			ReadOnly:  true,
		})
		if err != nil {
			return model.UserTombstone{}, fmt.Errorf(
				"%w: begin pinned AdminDSN snapshot: %v",
				store.ErrEnumerationNotAuthoritative, err,
			)
		}
		defer adminTx.Rollback() //nolint:errcheck // read-only witness; operation error is authoritative
		posture, postureErr := sys.s.dia.ConnRolePosture(ctx, adminTx)
		if postureErr != nil || !posture.BypassRLS || posture.Superuser || posture.TriggersDisabled() {
			return model.UserTombstone{}, fmt.Errorf(
				"%w: pinned AdminDSN posture is not NOSUPERUSER BYPASSRLS: %v",
				store.ErrEnumerationNotAuthoritative, postureErr,
			)
		}
		if err := requirePinnedRetirementAdminRole(
			posture.Role, sys.s.directoryAdminRole,
		); err != nil {
			return model.UserTombstone{}, err
		}
		if err := verifyDirectoryActivationDatabaseIdentity(
			ctx, sys.tx, directoryActivationWitnesses{admin: adminTx},
		); err != nil {
			return model.UserTombstone{}, fmt.Errorf(
				"%w: pinned AdminDSN identity: %v",
				store.ErrEnumerationNotAuthoritative, err,
			)
		}
		// Enforced is durable writer state, not a permanent attestation of a DSN
		// supplied to a later process. Re-prove, under the global lock, that the
		// exact pinned admin role (and every SET ROLE path reachable from it) can
		// enumerate but cannot mutate the directory authority inventory.
		if err := verifyPostgresDirectoryActivationAdminReadOnly(
			ctx, sys.tx, posture.Role,
		); err != nil {
			return model.UserTombstone{}, fmt.Errorf(
				"%w: pinned AdminDSN authority is not read-only: %v",
				store.ErrEnumerationNotAuthoritative, err,
			)
		}
		queryer = adminTx
	}
	tenants, err := enumerateDirectoryTenants(ctx, queryer, sys.s.dia)
	if err != nil {
		return model.UserTombstone{}, directoryUnavailable(
			"enumerate every tenant for user retirement", err,
		)
	}
	if err := sys.bindFor(ctx, model.SystemTenantID); err != nil {
		return model.UserTombstone{}, err
	}
	preUserRec, found, err := readDirectoryRetirementRecord(
		ctx, sys.tx, sys.s.dia, userDescriptor, model.SystemTenantID, req.UserID, false,
	)
	if err != nil {
		return model.UserTombstone{}, err
	}
	if !found {
		replayed, replayFound, replayErr := sys.replayUserRetirement(ctx, req, tenants)
		if replayErr != nil {
			return model.UserTombstone{}, replayErr
		}
		if !replayFound {
			return model.UserTombstone{}, store.ErrNotFound
		}
		if err := restoreSystemDirectoryBaseline(ctx, sys.tx, sys.s.dia); err != nil {
			return model.UserTombstone{}, err
		}
		return replayed, nil
	}
	if _, receiptFound, receiptErr := sys.replayUserRetirement(ctx, req, tenants); receiptErr != nil {
		return model.UserTombstone{}, receiptErr
	} else if receiptFound {
		return model.UserTombstone{}, directoryUnavailable(
			"User source coexists with a prior retirement receipt", nil,
		)
	}
	preUserBase, err := baseFromRecord(preUserRec)
	if err != nil {
		return model.UserTombstone{}, directoryUnavailable("decode retiring user", err)
	}
	if preUserBase.Version != req.ExpectedVersion {
		return model.UserTombstone{}, store.ErrConflict
	}
	if _, err := userCodec.Decode(preUserBase, preUserRec); err != nil {
		return model.UserTombstone{}, directoryUnavailable("decode retiring user", err)
	}

	resulting := make(map[model.TenantID]int64, len(tenants))
	for _, tenant := range tenants {
		epoch, err := sys.bumpRetirementEpoch(ctx, tenant)
		if err != nil {
			return model.UserTombstone{}, err
		}
		resulting[tenant] = epoch
	}
	if err := sys.bindFor(ctx, model.SystemTenantID); err != nil {
		return model.UserTombstone{}, err
	}
	if err := armDirectoryWriter(ctx, sys.tx, sys.s.dia, writer); err != nil {
		return model.UserTombstone{}, err
	}
	userRec, found, err := readDirectoryRetirementRecord(
		ctx, sys.tx, sys.s.dia, userDescriptor, model.SystemTenantID, req.UserID, true,
	)
	if err != nil {
		return model.UserTombstone{}, err
	}
	if !found {
		return model.UserTombstone{}, store.ErrConflict
	}
	userBase, err := baseFromRecord(userRec)
	if err != nil {
		return model.UserTombstone{}, directoryUnavailable("decode locked retiring user", err)
	}
	if userBase.Version != req.ExpectedVersion || !reflect.DeepEqual(userRec, preUserRec) {
		return model.UserTombstone{}, store.ErrConflict
	}
	if err := sys.lockUserRetirementAuthorityTables(ctx); err != nil {
		return model.UserTombstone{}, err
	}

	now, err := directoryTransactionNow(ctx, sys.tx, sys.s.dia)
	if err != nil {
		return model.UserTombstone{}, directoryUnavailable("read user retirement DB time", err)
	}
	epochs, err := model.NewDirectoryEpochEvidence(resulting)
	if err != nil {
		return model.UserTombstone{}, directoryUnavailable("encode user retirement epochs", err)
	}
	tombstoneID := model.NewID()
	draft := model.AuditDraft{
		Actor: req.Actor, ActorKind: req.ActorKind,
		Action:     model.AuditActionUserRetire,
		TargetKind: model.UserTombstoneKind, TargetID: tombstoneID,
		Meta: userRetirementAuditMeta(req, len(epochs)),
	}
	event, err := sys.auditLogFor(model.SystemTenantID).Append(ctx, draft)
	if err != nil {
		return model.UserTombstone{}, err
	}
	if directoryRetirementAfterAuditTestHook != nil {
		directoryRetirementAfterAuditTestHook(&event)
	}
	if err := requireSystemAuditEvent(
		ctx, sys.tx, sys.s.dia, event, model.SystemTenantID, draft,
	); err != nil {
		return model.UserTombstone{}, err
	}
	if err := sys.retireUserAuthority(ctx, req.UserID); err != nil {
		return model.UserTombstone{}, err
	}
	if err := hardDeleteDirectorySource(
		ctx, sys.tx, sys.s.dia, userDescriptor, model.SystemTenantID,
		req.UserID, req.ExpectedVersion,
	); err != nil {
		return model.UserTombstone{}, err
	}

	tombstone := model.UserTombstone{
		BaseFields: model.BaseFields{
			ID: tombstoneID, TenantID: model.SystemTenantID,
			CreatedAt: now, UpdatedAt: now, Version: 1,
		},
		PrincipalKind:   model.DirectoryPrincipalUser,
		PrincipalRef:    req.UserID,
		SourceKind:      userDescriptor.Kind,
		SourceID:        req.UserID,
		ResultingEpochs: epochs,
		Cause:           model.DirectoryCauseUserErased,
		Actor:           req.Actor,
		RetiredAt:       now,
		AuditAnchor:     retirementAuditAnchor(event),
	}
	if err := insertUserRetirementTombstone(ctx, sys, tombstone); err != nil {
		return model.UserTombstone{}, err
	}
	if err := sys.requireUserRetirementWitnesses(ctx, tombstone, tenants, true); err != nil {
		return model.UserTombstone{}, err
	}
	if directoryRetirementBeforeFinishTestHook != nil {
		if err := directoryRetirementBeforeFinishTestHook(
			model.DirectoryPrincipalUser, req.UserID,
		); err != nil {
			return model.UserTombstone{}, err
		}
	}
	if err := restoreSystemDirectoryBaseline(ctx, sys.tx, sys.s.dia); err != nil {
		return model.UserTombstone{}, err
	}
	return tombstone, nil
}

func requirePinnedRetirementAdminRole(got string, boot guardRoleFact) error {
	if !boot.Known || strings.TrimSpace(boot.Role) == "" || got != boot.Role {
		return fmt.Errorf(
			"%w: pinned AdminDSN role changed since boot: got %q want %q known=%t",
			store.ErrEnumerationNotAuthoritative, got, boot.Role, boot.Known,
		)
	}
	return nil
}

func (sys *systemScope) retireDirectoryPrincipal(
	ctx context.Context,
	req DirectoryPrincipalRetirementRequest,
) (_ DirectoryPrincipalRetirementResult, retErr error) {
	defer func() { sys.poison(retErr) }()
	if !sys.s.elector.active() {
		return DirectoryPrincipalRetirementResult{}, store.ErrNotLeader
	}
	writer, err := acquireDirectoryWriter(ctx, sys.tx, sys.s.dia)
	if err != nil {
		return DirectoryPrincipalRetirementResult{}, err
	}
	if writer.Mode != directoryWriterEnforced {
		return DirectoryPrincipalRetirementResult{}, fmt.Errorf(
			"%w: current mode is %s", store.ErrDirectoryRetirementNotEnforced, writer.Mode,
		)
	}
	if err := sys.bindFor(ctx, req.TenantID); err != nil {
		return DirectoryPrincipalRetirementResult{}, err
	}
	sourceDescriptor := descriptorForRetirementKind(req.PrincipalKind)
	preRec, found, err := readDirectoryRetirementRecord(
		ctx, sys.tx, sys.s.dia, sourceDescriptor, req.TenantID, req.SourceID, false,
	)
	if err != nil {
		return DirectoryPrincipalRetirementResult{}, err
	}
	if !found {
		replayed, replayFound, replayErr := sys.replayDirectoryRetirement(ctx, req)
		if replayErr != nil {
			return DirectoryPrincipalRetirementResult{}, replayErr
		}
		if !replayFound {
			return DirectoryPrincipalRetirementResult{}, store.ErrNotFound
		}
		if err := restoreSystemDirectoryBaseline(ctx, sys.tx, sys.s.dia); err != nil {
			return DirectoryPrincipalRetirementResult{}, err
		}
		return replayed, nil
	}
	if _, receiptFound, receiptErr := sys.replayDirectoryRetirement(ctx, req); receiptErr != nil {
		return DirectoryPrincipalRetirementResult{}, receiptErr
	} else if receiptFound {
		return DirectoryPrincipalRetirementResult{}, directoryUnavailable(
			"directory source coexists with a prior retirement receipt", nil,
		)
	}
	preBase, err := baseFromRecord(preRec)
	if err != nil {
		return DirectoryPrincipalRetirementResult{}, directoryUnavailable(
			"decode retiring directory source", err,
		)
	}
	if preBase.Version != req.ExpectedVersion {
		return DirectoryPrincipalRetirementResult{}, store.ErrConflict
	}
	var preAgent model.Agent
	if req.PrincipalKind == model.DirectoryPrincipalIdentity {
		if _, err := identityCodec.Decode(preBase, preRec); err != nil {
			return DirectoryPrincipalRetirementResult{}, directoryUnavailable(
				"decode retiring identity", err,
			)
		}
	} else {
		preAgent, err = agentCodec.Decode(preBase, preRec)
		if err != nil {
			return DirectoryPrincipalRetirementResult{}, directoryUnavailable(
				"decode retiring Agent", err,
			)
		}
		if err := validateCreateID(preAgent.IdentityID); err != nil {
			return DirectoryPrincipalRetirementResult{}, directoryUnavailable(
				"retiring Agent has no canonical stable IdentityID", err,
			)
		}
		if !preAgent.WorkspaceID.IsZero() {
			if err := validateCreateID(preAgent.WorkspaceID); err != nil {
				return DirectoryPrincipalRetirementResult{}, directoryUnavailable(
					"retiring Agent workspace is not canonical", err,
				)
			}
		}
	}
	resultingEpoch, err := sys.bumpRetirementEpoch(ctx, req.TenantID)
	if err != nil {
		return DirectoryPrincipalRetirementResult{}, err
	}
	if err := armDirectoryWriter(ctx, sys.tx, sys.s.dia, writer); err != nil {
		return DirectoryPrincipalRetirementResult{}, err
	}

	var (
		principal  store.DirectoryPrincipalRef
		sourceKind model.Kind
		definitive = true
	)
	switch req.PrincipalKind {
	case model.DirectoryPrincipalIdentity:
		if err := lockDirectoryRetirementTable(
			ctx, sys.tx, sys.s.dia, identityDescriptor.Table, "SHARE ROW EXCLUSIVE",
		); err != nil {
			return DirectoryPrincipalRetirementResult{}, err
		}
		rec, found, err := readDirectoryRetirementRecord(
			ctx, sys.tx, sys.s.dia, identityDescriptor, req.TenantID, req.SourceID, true,
		)
		if err != nil {
			return DirectoryPrincipalRetirementResult{}, err
		}
		if !found {
			return DirectoryPrincipalRetirementResult{}, store.ErrNotFound
		}
		base, err := baseFromRecord(rec)
		if err != nil {
			return DirectoryPrincipalRetirementResult{}, directoryUnavailable(
				"decode retiring identity", err,
			)
		}
		if base.Version != req.ExpectedVersion || !reflect.DeepEqual(rec, preRec) {
			return DirectoryPrincipalRetirementResult{}, store.ErrConflict
		}
		if _, err := identityCodec.Decode(base, rec); err != nil {
			return DirectoryPrincipalRetirementResult{}, directoryUnavailable(
				"decode locked retiring identity", err,
			)
		}
		if err := lockDirectoryRetirementTable(
			ctx, sys.tx, sys.s.dia, agentDescriptor.Table, "SHARE ROW EXCLUSIVE",
		); err != nil {
			return DirectoryPrincipalRetirementResult{}, err
		}
		if err := sys.refuseIdentityWithRecoverableAgents(
			ctx, req.TenantID, req.SourceID,
		); err != nil {
			return DirectoryPrincipalRetirementResult{}, err
		}
		principal = store.DirectoryPrincipalRef{
			PrincipalKind: model.DirectoryPrincipalIdentity,
			PrincipalRef:  req.SourceID,
		}
		sourceKind = identityDescriptor.Kind

	case model.DirectoryPrincipalAgent:
		// preAgent was decoded before the epoch bump only to discover which
		// order-10 Identity must precede the order-20 Agent lock. The source row
		// is re-read authoritatively below and must remain byte-for-byte equal.
		if err := lockDirectoryRetirementTable(
			ctx, sys.tx, sys.s.dia, identityDescriptor.Table, "SHARE",
		); err != nil {
			return DirectoryPrincipalRetirementResult{}, err
		}
		identityRec, identityFound, err := readDirectoryRetirementRecord(
			ctx, sys.tx, sys.s.dia, identityDescriptor, req.TenantID,
			preAgent.IdentityID, true,
		)
		if err != nil {
			return DirectoryPrincipalRetirementResult{}, err
		}
		if !identityFound {
			return DirectoryPrincipalRetirementResult{}, directoryUnavailable(
				"retiring Agent stable Identity is absent", nil,
			)
		}
		identityBase, err := baseFromRecord(identityRec)
		if err != nil {
			return DirectoryPrincipalRetirementResult{}, directoryUnavailable(
				"decode retiring Agent stable Identity", err,
			)
		}
		if _, err := identityCodec.Decode(identityBase, identityRec); err != nil {
			return DirectoryPrincipalRetirementResult{}, directoryUnavailable(
				"decode retiring Agent stable Identity", err,
			)
		}
		identityReader := &tenantScope{
			s: sys.s, tx: sys.tx, tenant: req.TenantID, readOnly: true,
		}
		if _, _, err := identityReader.ReadDirectoryTombstone(ctx, store.DirectoryPrincipalRef{
			PrincipalKind: model.DirectoryPrincipalIdentity,
			PrincipalRef:  preAgent.IdentityID,
		}); err != nil {
			return DirectoryPrincipalRetirementResult{}, err
		}
		if err := lockDirectoryRetirementTable(
			ctx, sys.tx, sys.s.dia, agentDescriptor.Table, "SHARE ROW EXCLUSIVE",
		); err != nil {
			return DirectoryPrincipalRetirementResult{}, err
		}
		rec, found, err := readDirectoryRetirementRecord(
			ctx, sys.tx, sys.s.dia, agentDescriptor, req.TenantID, req.SourceID, true,
		)
		if err != nil {
			return DirectoryPrincipalRetirementResult{}, err
		}
		if !found {
			return DirectoryPrincipalRetirementResult{}, store.ErrNotFound
		}
		base, err := baseFromRecord(rec)
		if err != nil {
			return DirectoryPrincipalRetirementResult{}, directoryUnavailable(
				"decode locked retiring Agent", err,
			)
		}
		agent, err := agentCodec.Decode(base, rec)
		if err != nil {
			return DirectoryPrincipalRetirementResult{}, directoryUnavailable(
				"decode locked retiring Agent", err,
			)
		}
		if base.Version != req.ExpectedVersion || !reflect.DeepEqual(rec, preRec) ||
			agent.IdentityID != preAgent.IdentityID || agent.WorkspaceID != preAgent.WorkspaceID {
			return DirectoryPrincipalRetirementResult{}, store.ErrConflict
		}
		workspace, err := sys.effectiveAgentRetirementWorkspace(ctx, req.TenantID, agent)
		if err != nil {
			return DirectoryPrincipalRetirementResult{}, err
		}
		principal = store.DirectoryPrincipalRef{
			PrincipalKind: model.DirectoryPrincipalAgent,
			PrincipalRef:  agent.IdentityID,
			WorkspaceRef:  workspace,
		}
		if err := principal.Validate(); err != nil {
			return DirectoryPrincipalRetirementResult{}, directoryUnavailable(
				"retiring Agent principal is not canonical", err,
			)
		}
		siblings, err := sys.countRecoverableAgentBindings(ctx, req.TenantID, agent, workspace)
		if err != nil {
			return DirectoryPrincipalRetirementResult{}, err
		}
		definitive = siblings == 0
		sourceKind = agentDescriptor.Kind
	}

	now, err := directoryTransactionNow(ctx, sys.tx, sys.s.dia)
	if err != nil {
		return DirectoryPrincipalRetirementResult{}, directoryUnavailable(
			"read directory retirement DB time", err,
		)
	}
	result := DirectoryPrincipalRetirementResult{
		Code: DirectoryRetirementDefinitive, Definitive: definitive,
		Principal: principal, SourceKind: sourceKind, SourceID: req.SourceID,
		ResultingEpoch: resultingEpoch,
	}
	if !definitive {
		result.Code = DirectoryRetirementAgentBindingRemains
	}
	tombstoneID := model.NewID()
	draft := model.AuditDraft{
		Actor: req.Actor, ActorKind: req.ActorKind,
		Action:     model.AuditActionDirectoryPrincipalRetire,
		TargetKind: model.DirectoryTombstoneKind, TargetID: tombstoneID,
		Meta: directoryRetirementAuditMeta(req, result, definitive),
	}
	if !definitive {
		draft.Action = model.AuditActionAgentBindingRetire
		draft.TargetKind = agentDescriptor.Kind
		draft.TargetID = req.SourceID
	}
	event, err := sys.auditLogFor(req.TenantID).Append(ctx, draft)
	if err != nil {
		return DirectoryPrincipalRetirementResult{}, err
	}
	if directoryRetirementAfterAuditTestHook != nil {
		directoryRetirementAfterAuditTestHook(&event)
	}
	if err := requireSystemAuditEvent(
		ctx, sys.tx, sys.s.dia, event, req.TenantID, draft,
	); err != nil {
		return DirectoryPrincipalRetirementResult{}, err
	}
	result.AuditEventID = event.ID
	result.AuditSeq = event.Seq
	result.AuditHash = append([]byte(nil), event.Hash...)
	if err := hardDeleteDirectorySource(
		ctx, sys.tx, sys.s.dia, descriptorForRetirementKind(req.PrincipalKind),
		req.TenantID, req.SourceID, req.ExpectedVersion,
	); err != nil {
		return DirectoryPrincipalRetirementResult{}, err
	}

	if definitive {
		tombstone := model.DirectoryTombstone{
			BaseFields: model.BaseFields{
				ID: tombstoneID, TenantID: req.TenantID,
				CreatedAt: now, UpdatedAt: now, Version: 1,
			},
			PrincipalKind:  req.PrincipalKind,
			PrincipalRef:   principal.PrincipalRef,
			SourceKind:     sourceKind,
			SourceID:       req.SourceID,
			WorkspaceRef:   principal.WorkspaceRef,
			ResultingEpoch: resultingEpoch,
			Cause:          directoryRetirementCause(req.PrincipalKind),
			Actor:          req.Actor,
			RetiredAt:      now,
			AuditAnchor:    retirementAuditAnchor(event),
		}
		if err := insertDirectoryRetirementTombstone(ctx, sys, tombstone); err != nil {
			return DirectoryPrincipalRetirementResult{}, err
		}
		if err := sys.requireDirectoryRetirementWitness(ctx, tombstone, true); err != nil {
			return DirectoryPrincipalRetirementResult{}, err
		}
		result.Tombstone = &tombstone
	}
	if directoryRetirementBeforeFinishTestHook != nil {
		if err := directoryRetirementBeforeFinishTestHook(
			req.PrincipalKind, req.SourceID,
		); err != nil {
			return DirectoryPrincipalRetirementResult{}, err
		}
	}
	if err := restoreSystemDirectoryBaseline(ctx, sys.tx, sys.s.dia); err != nil {
		return DirectoryPrincipalRetirementResult{}, err
	}
	return result, nil
}

func (sys *systemScope) bumpRetirementEpoch(
	ctx context.Context,
	tenant model.TenantID,
) (int64, error) {
	if err := sys.bindFor(ctx, tenant); err != nil {
		return 0, err
	}
	if err := bumpDirectoryEpochExact(ctx, sys.tx, sys.s.dia, tenant); err != nil {
		return 0, err
	}
	epoch, found, err := readDirectoryEpochRow(ctx, sys.tx, sys.s.dia, tenant)
	if err != nil {
		return 0, directoryUnavailable("read resulting retirement epoch", err)
	}
	if !found || epoch.Version < 1 {
		return 0, directoryUnavailable("resulting retirement epoch is absent", nil)
	}
	return epoch.Version, nil
}

func lockDirectoryRetirementTable(
	ctx context.Context,
	tx *sql.Tx,
	dia dialect.Dialect,
	table string,
	mode string,
) error {
	if dia.Name() != store.EnginePostgres {
		return nil
	}
	if _, err := tx.ExecContext(ctx,
		"LOCK TABLE ONLY "+directoryWriterRelation(dia, table)+" IN "+mode+" MODE",
	); err != nil {
		return fmt.Errorf("lock directory retirement source %s: %w", table, err)
	}
	return nil
}

func readDirectoryRetirementRecord(
	ctx context.Context,
	tx *sql.Tx,
	dia dialect.Dialect,
	desc model.EntityDescriptor,
	tenant model.TenantID,
	id model.ID,
	lock bool,
) (model.Record, bool, error) {
	cols := desc.AllColumns()
	query := fmt.Sprintf(
		"SELECT %s FROM %s WHERE tenant_id = ? AND id = ?",
		strings.Join(cols, ", "), directoryWriterRelation(dia, desc.Table),
	)
	if lock {
		query += rowLockSuffix(dia.Name())
	}
	state, err := newScanState(desc, cols)
	if err != nil {
		return nil, false, err
	}
	err = tx.QueryRowContext(
		ctx, dia.Rebind(query), tenant.String(), id.String(),
	).Scan(state.dests...)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return state.record(), true, nil
}

func hardDeleteDirectorySource(
	ctx context.Context,
	tx *sql.Tx,
	dia dialect.Dialect,
	desc model.EntityDescriptor,
	tenant model.TenantID,
	id model.ID,
	expectedVersion int64,
) error {
	query := dia.Rebind(fmt.Sprintf(
		"DELETE FROM %s WHERE tenant_id = ? AND id = ? AND version = ?",
		directoryWriterRelation(dia, desc.Table),
	))
	result, err := tx.ExecContext(
		ctx, query, tenant.String(), id.String(), expectedVersion,
	)
	if err != nil {
		return mapWriteErr(err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		if rows == 0 {
			_, found, readErr := readDirectoryRetirementRecord(
				ctx, tx, dia, desc, tenant, id, false,
			)
			if readErr != nil {
				return readErr
			}
			if !found {
				return store.ErrNotFound
			}
			return store.ErrConflict
		}
		return fmt.Errorf("hard delete from %s affected %d rows, want exactly one",
			desc.Table, rows)
	}
	if _, found, err := readDirectoryRetirementRecord(
		ctx, tx, dia, desc, tenant, id, false,
	); err != nil {
		return err
	} else if found {
		return directoryUnavailable(
			fmt.Sprintf("hard delete left source row in %s", desc.Table), nil,
		)
	}
	return nil
}

func retirementAuditAnchor(event model.AuditEvent) model.RetirementAuditAnchor {
	return model.RetirementAuditAnchor{
		EventID:    event.ID,
		Seq:        event.Seq,
		Hash:       append([]byte(nil), event.Hash...),
		Action:     event.Action,
		TargetKind: event.TargetKind,
		TargetID:   event.TargetID,
	}
}

func insertUserRetirementTombstone(
	ctx context.Context,
	sys *systemScope,
	want model.UserTombstone,
) error {
	rec, err := userTombstoneCodec.Encode(want)
	if err != nil {
		return err
	}
	if err := insertDirectoryRetirementRecord(
		ctx, sys.tx, sys.s.dia, userTombstoneDescriptor, want.BaseFields, rec,
	); err != nil {
		return err
	}
	stored, found, err := readDirectoryRetirementRecord(
		ctx, sys.tx, sys.s.dia, userTombstoneDescriptor,
		model.SystemTenantID, want.ID, false,
	)
	if err != nil {
		return err
	}
	if !found {
		return directoryUnavailable("user tombstone insert has no readback", nil)
	}
	base, err := baseFromRecord(stored)
	if err != nil {
		return directoryUnavailable("decode user tombstone readback", err)
	}
	got, err := userTombstoneCodec.Decode(base, stored)
	if err != nil {
		return directoryUnavailable("decode user tombstone readback", err)
	}
	if !reflect.DeepEqual(got, want) {
		return directoryUnavailable("user tombstone readback diverged", nil)
	}
	return nil
}

func insertDirectoryRetirementTombstone(
	ctx context.Context,
	sys *systemScope,
	want model.DirectoryTombstone,
) error {
	rec, err := directoryTombstoneCodec.Encode(want)
	if err != nil {
		return err
	}
	if err := insertDirectoryRetirementRecord(
		ctx, sys.tx, sys.s.dia, directoryTombstoneDescriptor, want.BaseFields, rec,
	); err != nil {
		return err
	}
	stored, found, err := readDirectoryRetirementRecord(
		ctx, sys.tx, sys.s.dia, directoryTombstoneDescriptor,
		want.TenantID, want.ID, false,
	)
	if err != nil {
		return err
	}
	if !found {
		return directoryUnavailable("directory tombstone insert has no readback", nil)
	}
	base, err := baseFromRecord(stored)
	if err != nil {
		return directoryUnavailable("decode directory tombstone readback", err)
	}
	got, err := directoryTombstoneCodec.Decode(base, stored)
	if err != nil {
		return directoryUnavailable("decode directory tombstone readback", err)
	}
	if !reflect.DeepEqual(got, want) {
		return directoryUnavailable("directory tombstone readback diverged", nil)
	}
	return nil
}

func insertDirectoryRetirementRecord(
	ctx context.Context,
	tx *sql.Tx,
	dia dialect.Dialect,
	desc model.EntityDescriptor,
	base model.BaseFields,
	rec model.Record,
) error {
	baseToRecord(rec, base, false)
	cols := desc.AllColumns()
	args := make([]any, len(cols))
	for i, column := range cols {
		args[i] = rec[column]
	}
	query := dia.Rebind(fmt.Sprintf(
		"INSERT INTO %s (%s) VALUES (%s)",
		directoryWriterRelation(dia, desc.Table),
		strings.Join(cols, ", "), placeholders(len(cols)),
	))
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return mapWriteErr(err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return directoryUnavailable(
			fmt.Sprintf("insert into %s affected %d rows, want exactly one", desc.Table, rows),
			nil,
		)
	}
	return nil
}

func descriptorForRetirementKind(
	kind model.DirectoryPrincipalKind,
) model.EntityDescriptor {
	if kind == model.DirectoryPrincipalIdentity {
		return identityDescriptor
	}
	return agentDescriptor
}

func directoryRetirementCause(
	kind model.DirectoryPrincipalKind,
) model.DirectoryRetirementCause {
	if kind == model.DirectoryPrincipalIdentity {
		return model.DirectoryCauseIdentityRetired
	}
	return model.DirectoryCauseAgentRetired
}

func (sys *systemScope) effectiveAgentRetirementWorkspace(
	ctx context.Context,
	tenant model.TenantID,
	agent model.Agent,
) (model.ID, error) {
	if !agent.WorkspaceID.IsZero() {
		if err := validateCreateID(agent.WorkspaceID); err != nil {
			return "", directoryUnavailable("Agent workspace is not canonical", err)
		}
		return agent.WorkspaceID, nil
	}
	return sys.defaultRetirementWorkspaceID(ctx, tenant)
}

func (sys *systemScope) defaultRetirementWorkspaceID(
	ctx context.Context,
	tenant model.TenantID,
) (model.ID, error) {
	return defaultRetirementWorkspaceID(ctx, sys.tx, sys.s.dia, tenant)
}

func defaultRetirementWorkspaceID(
	ctx context.Context,
	tx *sql.Tx,
	dia dialect.Dialect,
	tenant model.TenantID,
) (model.ID, error) {
	query := dia.Rebind(fmt.Sprintf(
		"SELECT id FROM %s WHERE tenant_id = ? AND slug = ? ORDER BY id LIMIT 2",
		directoryWriterRelation(dia, workspaceDescriptor.Table),
	))
	rows, err := tx.QueryContext(
		ctx, query, tenant.String(), model.DefaultWorkspaceSlug,
	)
	if err != nil {
		return "", directoryUnavailable("resolve Agent default workspace", err)
	}
	defer rows.Close()
	var ids []model.ID
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return "", directoryUnavailable("resolve Agent default workspace", err)
		}
		ids = append(ids, model.ID(raw))
	}
	if err := rows.Err(); err != nil {
		return "", directoryUnavailable("resolve Agent default workspace", err)
	}
	if len(ids) != 1 {
		return "", directoryUnavailable(
			fmt.Sprintf("Agent default workspace count is %d, want one", len(ids)), nil,
		)
	}
	if err := validateCreateID(ids[0]); err != nil {
		return "", directoryUnavailable("Agent default workspace is not canonical", err)
	}
	return ids[0], nil
}

func (sys *systemScope) countRecoverableAgentBindings(
	ctx context.Context,
	tenant model.TenantID,
	source model.Agent,
	effectiveWorkspace model.ID,
) (int64, error) {
	return countPhysicalAgentPrincipalBindings(
		ctx, sys.tx, sys.s.dia, tenant, source.IdentityID,
		effectiveWorkspace, source.ID,
	)
}

// countPhysicalAgentPrincipalBindings enumerates rather than SQL-filtering the
// workspace because empty/all-zero values mean default while every other
// malformed value is uncertainty, not evidence that a sibling belongs
// elsewhere. Soft-deleted and inactive rows remain physical, recoverable
// bindings and are intentionally included.
func countPhysicalAgentPrincipalBindings(
	ctx context.Context,
	tx *sql.Tx,
	dia dialect.Dialect,
	tenant model.TenantID,
	identityID model.ID,
	effectiveWorkspace model.ID,
	excludeID model.ID,
) (int64, error) {
	if err := validateCreateID(identityID); err != nil {
		return 0, directoryUnavailable("Agent principal Identity is not canonical", err)
	}
	targetUUID, _ := uuid.Parse(identityID.String())
	// Do not filter deleted_at or status: inactive, disabled and soft-deleted
	// rows are reversible bindings and therefore prevent an irreversible
	// principal tombstone. Only the exact source row being hard-deleted is
	// excluded from the sibling count. Decode every matching physical row rather
	// than expressing workspace equality in SQL: a malformed non-empty workspace
	// is uncertainty about sibling existence and must deny closed, while the
	// empty and all-zero sentinels both resolve to the one default workspace.
	defaultWorkspace, err := defaultRetirementWorkspaceID(ctx, tx, dia, tenant)
	if err != nil {
		return 0, err
	}
	cols := agentDescriptor.AllColumns()
	query := dia.Rebind(fmt.Sprintf(
		"SELECT %s FROM %s WHERE tenant_id = ? ORDER BY id",
		strings.Join(cols, ", "),
		directoryWriterRelation(dia, agentDescriptor.Table),
	))
	rows, err := tx.QueryContext(
		ctx, query, tenant.String(),
	)
	if err != nil {
		return 0, directoryUnavailable("enumerate recoverable Agent bindings", err)
	}
	defer rows.Close() //nolint:errcheck // rows.Err below is authoritative
	var count int64
	for rows.Next() {
		state, err := newScanState(agentDescriptor, cols)
		if err != nil {
			return 0, directoryUnavailable("decode recoverable Agent binding", err)
		}
		if err := rows.Scan(state.dests...); err != nil {
			return 0, directoryUnavailable("decode recoverable Agent binding", err)
		}
		rec := state.record()
		base, err := baseFromRecord(rec)
		if err != nil {
			return 0, directoryUnavailable("decode recoverable Agent binding base", err)
		}
		binding, err := agentCodec.Decode(base, rec)
		if err != nil {
			return 0, directoryUnavailable("decode recoverable Agent binding", err)
		}
		if base.TenantID != tenant || base.Version < 1 {
			return 0, directoryUnavailable("recoverable Agent binding crossed its canonical owner", nil)
		}
		if err := validateCreateID(base.ID); err != nil {
			return 0, directoryUnavailable("recoverable Agent binding id is not canonical", err)
		}
		if !excludeID.IsZero() && base.ID == excludeID {
			continue
		}
		if binding.IdentityID.IsZero() {
			continue
		}
		bindingUUID, parseErr := uuid.Parse(binding.IdentityID.String())
		if parseErr != nil || bindingUUID.String() != binding.IdentityID.String() {
			return 0, directoryUnavailable(
				"recoverable Agent binding Identity is illegible or non-canonical", parseErr,
			)
		}
		if bindingUUID != targetUUID {
			continue
		}
		workspace := binding.WorkspaceID
		if workspace.IsZero() {
			workspace = defaultWorkspace
		} else if err := validateCreateID(workspace); err != nil {
			return 0, directoryUnavailable(
				"recoverable Agent binding workspace is not canonical", err,
			)
		}
		if workspace == effectiveWorkspace {
			count++
		}
	}
	if err := rows.Err(); err != nil {
		return 0, directoryUnavailable("enumerate recoverable Agent bindings", err)
	}
	return count, nil
}

// countPhysicalAgentsForIdentity is the workspace-independent Identity
// retirement predicate. It scans the complete tenant inventory so an alternate
// but parse-equivalent UUID spelling (for example uppercase legacy text) cannot
// evade an equality predicate and strand a physical Agent after the Identity is
// removed.
func countPhysicalAgentsForIdentity(
	ctx context.Context,
	tx *sql.Tx,
	dia dialect.Dialect,
	tenant model.TenantID,
	identityID model.ID,
) (int64, error) {
	if err := validateCreateID(identityID); err != nil {
		return 0, directoryUnavailable("Identity Agent predicate is not canonical", err)
	}
	targetUUID, _ := uuid.Parse(identityID.String())
	query := dia.Rebind(
		"SELECT id, identity_id FROM " + directoryWriterRelation(dia, agentDescriptor.Table) +
			" WHERE tenant_id = ? ORDER BY id",
	)
	rows, err := tx.QueryContext(ctx, query, tenant.String())
	if err != nil {
		return 0, directoryUnavailable("enumerate Identity Agent bindings", err)
	}
	defer rows.Close() //nolint:errcheck // rows.Err below is authoritative
	var count int64
	for rows.Next() {
		var rawID, rawIdentity string
		if err := rows.Scan(&rawID, &rawIdentity); err != nil {
			return 0, directoryUnavailable("decode Identity Agent binding", err)
		}
		if err := validateCreateID(model.ID(rawID)); err != nil {
			return 0, directoryUnavailable("Identity Agent binding id is not canonical", err)
		}
		if model.ID(rawIdentity).IsZero() {
			continue
		}
		parsed, parseErr := uuid.Parse(rawIdentity)
		if parseErr != nil || parsed.String() != rawIdentity {
			return 0, directoryUnavailable(
				"Identity Agent binding identity is illegible or non-canonical", parseErr,
			)
		}
		if parsed != targetUUID {
			continue
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return 0, directoryUnavailable("enumerate Identity Agent bindings", err)
	}
	return count, nil
}
