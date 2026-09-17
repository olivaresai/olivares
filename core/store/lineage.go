// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package store

import (
	"context"
	"errors"

	"github.com/olivaresai/olivares/core/model"
)

var ErrLineageUnavailable = errors.New("lineage authority unavailable")

// LineageEpochReader reads only a fact, through the surrounding tenant View.
// It never initializes missing coverage. relation is a core entity kind from
// the closed lineage inventory, not a caller-supplied table or tenant.
type LineageEpochReader interface {
	ReadLineageEpoch(context.Context, model.Kind) (AuthorizationFactRef, error)
}

// AuthoritySnapshotReader revalidates the locker's same closed fact vocabulary
// without locks or writes, using the current View and its database clock.
type AuthoritySnapshotReader interface {
	ValidateAuthoritySnapshot(context.Context, []AuthorizationFactRef) error
}

// ReadLineageFact preserves confinement without exposing the wrapped tenant
// repositories or advertising optional capabilities absent from the raw store.
func ReadLineageFact(ctx context.Context, sc Scope, relation model.Kind) (AuthorizationFactRef, error) {
	raw := authorityReadScope(sc)
	reader, ok := raw.(LineageEpochReader)
	if !ok {
		return AuthorizationFactRef{}, ErrLineageUnavailable
	}
	return reader.ReadLineageEpoch(ctx, relation)
}

// ValidateReadAuthority accepts facts only; it never returns unconfined rows.
// The SQL reader enforces the same closed descriptor allowlist as the locker.
func ValidateReadAuthority(ctx context.Context, sc Scope, facts []AuthorizationFactRef) error {
	reader, ok := authorityReadScope(sc).(AuthoritySnapshotReader)
	if !ok {
		return ErrLineageUnavailable
	}
	return reader.ValidateAuthoritySnapshot(ctx, facts)
}

// ValidateReadAuthorityBundle preserves confinement while validating the exact
// supplied User fences and tenant facts. Missing capability is unavailable;
// neither the legacy validator nor a mutation locker is a fallback.
func ValidateReadAuthorityBundle(ctx context.Context, sc Scope, bundle AuthoritySnapshotBundle) error {
	reader, ok := authorityReadScope(sc).(AuthoritySnapshotBundleReader)
	if !ok {
		return ErrLineageUnavailable
	}
	return reader.ValidateAuthoritySnapshotBundle(ctx, bundle)
}

// Only this package's confinement adapter can reveal this handle internally.
// No public method returns the raw Scope to callers.
func authorityReadScope(sc Scope) Scope {
	if wrapped, ok := sc.(interface{ authorityReadScope() Scope }); ok {
		return wrapped.authorityReadScope()
	}
	return sc
}
