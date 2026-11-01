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
	repo := a.ts.repo(apiTokenDescriptorV29)
	if err := repo.noteWrite(id); err != nil {
		return err
	}
	query := fmt.Sprintf("UPDATE %s SET last_used_at = ? WHERE id = ? AND tenant_id = ? AND (last_used_at IS NULL OR last_used_at <= ?)%s",
		repo.relation(), repo.softDeleteClause())
	repo.guard(query)
	result, err := a.ts.tx.ExecContext(ctx, a.ts.s.dia.Rebind(query),
		usedAt.String(), id.String(), a.ts.tenant.String(), usedAt.String())
	if err != nil {
		return mapWriteErr(err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		// A delayed request is a no-op; distinguish it from an absent credential.
		_, err := a.Tokens().Get(ctx, id)
		return err
	}
	return nil
}

// RecordAuthSessionUse writes only the human session's observed-use metadata.
// Like API-token use, it deliberately avoids the credential's versioned update.
func (a *authScope) RecordAuthSessionUse(ctx context.Context, id model.ID, usedAt model.Timestamp, clientKind string) error {
	if a.ts.readOnly {
		return store.ErrReadOnly
	}
	if id.IsZero() {
		return store.ErrNotFound
	}
	if clientKind != model.SessionClientBrowser && clientKind != model.SessionClientToken {
		return fmt.Errorf("invalid auth session client kind %q", clientKind)
	}
	// auth_sessions is guarded even for bookkeeping. Arm the existing writer
	// protocol without changing any user's authority or tenant directory epoch.
	if err := a.ts.directoryWriter.prepare(ctx, func() ([]model.TenantID, error) { return nil, nil }); err != nil {
		return err
	}
	repo := a.ts.repo(authSessionDescriptorV28)
	if err := repo.noteWrite(id); err != nil {
		return err
	}
	query := fmt.Sprintf("UPDATE %s SET last_used_at = ?, client_kind = ? WHERE id = ? AND tenant_id = ? AND (last_used_at IS NULL OR last_used_at <= ?)%s",
		repo.relation(), repo.softDeleteClause())
	repo.guard(query)
	result, err := a.ts.tx.ExecContext(ctx, a.ts.s.dia.Rebind(query),
		usedAt.String(), clientKind, id.String(), a.ts.tenant.String(), usedAt.String())
	if err != nil {
		return mapWriteErr(err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		_, err := a.Sessions().Get(ctx, id)
		return err
	}
	return nil
}
