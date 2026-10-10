// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"fmt"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// RecordAPITokenUse writes the one API-token column authentication owns. It
// does not go through Tokens().Update: that bumps version, which every
// credential pin compares (queued credentials, principal evidence, session
// credentials), and a use grants nothing the directory-tracked wrapper guards.
func (a *authScope) RecordAPITokenUse(ctx context.Context, id model.ID, usedAt model.Timestamp) error {
	if a.ts.readOnly {
		return store.ErrReadOnly
	}
	if id.IsZero() {
		return store.ErrNotFound
	}
	repo := a.ts.repo(apiTokenDescriptor)
	if err := repo.noteWrite(id); err != nil {
		return err
	}
	query := fmt.Sprintf("UPDATE %s SET last_used_at = ? WHERE id = ? AND tenant_id = ?%s",
		repo.relation(), repo.softDeleteClause())
	repo.guard(query)
	result, err := a.ts.tx.ExecContext(ctx, a.ts.s.dia.Rebind(query),
		usedAt.String(), id.String(), a.ts.tenant.String())
	if err != nil {
		return mapWriteErr(err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return store.ErrNotFound
	}
	return nil
}
