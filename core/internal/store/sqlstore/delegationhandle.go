// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// RevokeDelegationHandle flips a delegation handle to revoked in a single
// revoked_at IS NULL guarded UPDATE, writing ONLY revoked_at (from the caller),
// updated_at (from the store clock) and version+1. It is the ONLY post-mint
// handle mutation on AuthScope: the generic Update is not exposed, so a core
// caller can neither rewrite a handle's ceiling, expiry or audience nor set an
// arbitrary column here. The changed return reports whether the guarded UPDATE
// actually flipped a row: true only when this call performed the revocation, so a
// caller can audit delegation.revoke exactly once and a concurrent second revoke
// (which changes nothing) emits no duplicate event. Zero affected rows means the
// handle is either already revoked (changed=false, nil — an idempotent no-op) or
// absent (ErrNotFound); the two are distinguished by a follow-up Get, mirroring
// FinalizeDecisionClaim. A reload Get error that is NOT ErrNotFound is PROPAGATED,
// never swallowed as success.
func (a *authScope) RevokeDelegationHandle(ctx context.Context, jti model.ID, revokedAt model.Timestamp) (bool, error) {
	if a.ts.readOnly {
		return false, store.ErrReadOnly
	}
	if jti.IsZero() {
		return false, store.ErrNotFound
	}
	now := a.ts.s.clock.Now()
	repo := a.ts.repo(delegationHandleDescriptor)
	query := fmt.Sprintf(
		`UPDATE %s
SET revoked_at = ?, updated_at = ?, version = version + 1
WHERE id = ? AND tenant_id = ? AND revoked_at IS NULL`,
		repo.relation(),
	)
	repo.guard(query)
	result, err := a.ts.tx.ExecContext(ctx, a.ts.s.dia.Rebind(query),
		revokedAt.String(),
		now.String(),
		jti.String(),
		a.ts.tenant.String(),
	)
	if err != nil {
		return false, mapWriteErr(err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if rows == 0 {
		// Distinguish an absent/other-tenant handle from an already-revoked one. A
		// reload fault that is NOT ErrNotFound must PROPAGATE — swallowing it as a
		// success would report a phantom revoke the guarded UPDATE never performed.
		_, gerr := a.DelegationHandles().Get(ctx, jti)
		if errors.Is(gerr, store.ErrNotFound) {
			return false, store.ErrNotFound
		}
		if gerr != nil {
			return false, gerr
		}
		return false, nil // already revoked → idempotent no-op, nothing changed
	}
	return true, nil
}
