// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package engine

import (
	"context"

	"github.com/olivaresai/olivares/core/internal/store/sqlstore"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// ErrDirectoryRetirementAuthority reports that the irreversible capability was
// invoked with a decorated or substituted Store rather than the raw engine
// store returned by sqlstore.Open.
var ErrDirectoryRetirementAuthority = sqlstore.ErrDirectoryRetirementAuthority

// DirectoryRetirementCode is the closed outcome of a tenant-local retirement.
type DirectoryRetirementCode string

const (
	// DirectoryRetirementDefinitive means source deletion, epoch bump, audit and
	// immutable principal tombstone committed atomically.
	DirectoryRetirementDefinitive DirectoryRetirementCode = "definitive"
	// DirectoryRetirementAgentBindingRemains means the named Agent binding was
	// physically retired and durably audited, but at least one recoverable
	// sibling still binds the stable principal in the same workspace. No
	// principal tombstone or retirement anchor was fabricated.
	DirectoryRetirementAgentBindingRemains DirectoryRetirementCode = "agent_bindings_remain"
)

// RetireUserRequest identifies one exact User generation and the actor whose
// irreversible erasure decision is written to the system audit chain.
type RetireUserRequest struct {
	UserID          model.ID
	ExpectedVersion int64
	Actor           string
	ActorKind       string
}

// RetireDirectoryPrincipalRequest identifies an exact tenant-local Identity or
// Agent source generation. PrincipalRef and workspace are intentionally absent:
// the store derives them from the locked source rows instead of trusting wire
// input to choose the tombstone key.
type RetireDirectoryPrincipalRequest struct {
	TenantID        model.TenantID
	PrincipalKind   model.DirectoryPrincipalKind
	SourceID        model.ID
	ExpectedVersion int64
	Actor           string
	ActorKind       string
}

// RetireDirectoryPrincipalResult reports what the atomic ceremony proved. A
// non-definitive Agent binding retirement has Tombstone=nil but still carries
// the exact durable audit event reference.
type RetireDirectoryPrincipalResult struct {
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

// RetireUser is the sole definitive User-deletion capability. Under the global
// directory lock it authoritatively enumerates every real organization, bumps
// every epoch, appends a durable system audit event, hard-deletes the exact User
// version and inserts/read-validates its global tombstone in one transaction.
// PostgreSQL therefore requires the store's separately attested AdminDSN.
func RetireUser(
	ctx context.Context,
	raw store.Store,
	req RetireUserRequest,
) (model.UserTombstone, error) {
	return sqlstore.RetireUser(ctx, raw, sqlstore.UserRetirementRequest{
		UserID: req.UserID, ExpectedVersion: req.ExpectedVersion,
		Actor: req.Actor, ActorKind: req.ActorKind,
	})
}

// RetireDirectoryPrincipal is the sole hard-delete path for tenant-local
// Identity and Agent sources. Agent recipient identity is derived from the
// locked Agent.IdentityID plus its effective workspace; all physically
// recoverable sibling bindings, including inactive and soft-deleted rows,
// prevent a definitive tombstone.
func RetireDirectoryPrincipal(
	ctx context.Context,
	raw store.Store,
	req RetireDirectoryPrincipalRequest,
) (RetireDirectoryPrincipalResult, error) {
	result, err := sqlstore.RetireDirectoryPrincipal(
		ctx, raw, sqlstore.DirectoryPrincipalRetirementRequest{
			TenantID:        req.TenantID,
			PrincipalKind:   req.PrincipalKind,
			SourceID:        req.SourceID,
			ExpectedVersion: req.ExpectedVersion,
			Actor:           req.Actor,
			ActorKind:       req.ActorKind,
		},
	)
	return RetireDirectoryPrincipalResult{
		Code:           DirectoryRetirementCode(result.Code),
		Definitive:     result.Definitive,
		Principal:      result.Principal,
		SourceKind:     result.SourceKind,
		SourceID:       result.SourceID,
		ResultingEpoch: result.ResultingEpoch,
		Tombstone:      result.Tombstone,
		AuditEventID:   result.AuditEventID,
		AuditSeq:       result.AuditSeq,
		AuditHash:      append([]byte(nil), result.AuditHash...),
	}, err
}
