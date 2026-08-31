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

// lockUserRetirementAuthorityTables closes the PostgreSQL raw-writer edge in
// addition to AuthMutate's cooperative global-lock entry. The latter is what
// prevents an issuer from validating a User before retirement and inserting a
// credential afterwards; these table locks are defense in depth against a
// legacy/raw writer that does not yet take that application lock. The order is
// fixed and lexical across every execution.
func (sys *systemScope) lockUserRetirementAuthorityTables(ctx context.Context) error {
	for _, table := range []string{
		apiTokenDescriptor.Table,
		authSessionDescriptor.Table,
		delegationHandleDescriptor.Table,
		membershipDescriptor.Table,
		pepServiceCredentialDescriptor.Table,
		userGroupMemberDescriptor.Table,
		webauthnCredentialDescriptor.Table,
	} {
		if err := lockDirectoryRetirementTable(
			ctx, sys.tx, sys.s.dia, table, "SHARE ROW EXCLUSIVE",
		); err != nil {
			return err
		}
	}
	return nil
}

// retireUserAuthority physically removes every direct grant and authentication
// factor that carries the retiring user, plus the complete descendant tree of
// each API token rooted at user_id or act_as_user_id. This is deliberately part
// of the same transaction and runs only after the required retirement audit is
// durable; any delete/readback failure rolls the audit, epoch bumps, source
// delete and tombstone back together.
func (sys *systemScope) retireUserAuthority(ctx context.Context, userID model.ID) error {
	tokenIDs, err := sys.userRetirementTokenClosure(ctx, userID)
	if err != nil {
		return err
	}
	sessionIDs, err := sys.userRetirementSessionIDs(ctx, userID)
	if err != nil {
		return err
	}

	for _, target := range []struct {
		table     string
		predicate string
		args      []any
	}{
		{
			table: membershipDescriptor.Table, predicate: "user_id = ?",
			args: []any{userID.String()},
		},
		{
			table: userGroupMemberDescriptor.Table, predicate: "user_id = ?",
			args: []any{userID.String()},
		},
		{
			table: authSessionDescriptor.Table, predicate: "user_id = ?",
			args: []any{userID.String()},
		},
		{
			table: webauthnCredentialDescriptor.Table, predicate: "user_id = ?",
			args: []any{userID.String()},
		},
		{
			table:     delegationHandleDescriptor.Table,
			predicate: "subject_user_id = ? OR act_as_user_id = ?",
			args:      []any{userID.String(), userID.String()},
		},
	} {
		if err := sys.deleteUserRetirementRows(
			ctx, target.table, target.predicate, target.args...,
		); err != nil {
			return err
		}
	}

	// A delegation handle whose direct subject columns are anomalous can still
	// derive authority from a token in the retired closure. A PEP binding to one
	// of those credentials is not independently authenticating, but removing it
	// prevents a dangling credential association from surviving the erasure.
	for _, tokenID := range tokenIDs {
		if err := sys.deleteUserRetirementRows(
			ctx,
			delegationHandleDescriptor.Table,
			"source_cred_kind = ? AND source_cred_id = ?",
			"token",
			tokenID.String(),
		); err != nil {
			return err
		}
		if err := sys.deleteUserRetirementRows(
			ctx, pepServiceCredentialDescriptor.Table, "token_id = ?", tokenID.String(),
		); err != nil {
			return err
		}
	}
	for _, sessionID := range sessionIDs {
		if err := sys.deleteUserRetirementRows(
			ctx,
			delegationHandleDescriptor.Table,
			"source_cred_kind = ? AND source_cred_id = ?",
			"user",
			sessionID.String(),
		); err != nil {
			return err
		}
	}
	for _, tokenID := range tokenIDs {
		if err := sys.deleteExactUserRetirementRow(
			ctx, apiTokenDescriptor.Table, tokenID,
		); err != nil {
			return err
		}
	}

	for _, target := range []struct {
		table     string
		predicate string
		args      []any
	}{
		{membershipDescriptor.Table, "user_id = ?", []any{userID.String()}},
		{userGroupMemberDescriptor.Table, "user_id = ?", []any{userID.String()}},
		{authSessionDescriptor.Table, "user_id = ?", []any{userID.String()}},
		{webauthnCredentialDescriptor.Table, "user_id = ?", []any{userID.String()}},
		{
			delegationHandleDescriptor.Table,
			"subject_user_id = ? OR act_as_user_id = ?",
			[]any{userID.String(), userID.String()},
		},
		{
			apiTokenDescriptor.Table,
			"user_id = ? OR act_as_user_id = ?",
			[]any{userID.String(), userID.String()},
		},
	} {
		if err := sys.requireNoUserRetirementRows(
			ctx, target.table, target.predicate, target.args...,
		); err != nil {
			return err
		}
	}
	for _, tokenID := range tokenIDs {
		for _, target := range []struct {
			table     string
			predicate string
			args      []any
		}{
			{
				apiTokenDescriptor.Table, "id = ? OR parent_token_id = ?",
				[]any{tokenID.String(), tokenID.String()},
			},
			{
				delegationHandleDescriptor.Table,
				"source_cred_kind = ? AND source_cred_id = ?",
				[]any{"token", tokenID.String()},
			},
			{pepServiceCredentialDescriptor.Table, "token_id = ?", []any{tokenID.String()}},
		} {
			if err := sys.requireNoUserRetirementRows(
				ctx, target.table, target.predicate, target.args...,
			); err != nil {
				return err
			}
		}
	}
	for _, sessionID := range sessionIDs {
		if err := sys.requireNoUserRetirementRows(
			ctx,
			delegationHandleDescriptor.Table,
			"source_cred_kind = ? AND source_cred_id = ?",
			"user",
			sessionID.String(),
		); err != nil {
			return err
		}
	}
	return nil
}

func (sys *systemScope) userRetirementSessionIDs(
	ctx context.Context,
	userID model.ID,
) ([]model.ID, error) {
	query := sys.s.dia.Rebind(
		"SELECT id FROM " + directoryWriterRelation(sys.s.dia, authSessionDescriptor.Table) +
			" WHERE tenant_id = ? AND user_id = ? ORDER BY id",
	)
	rows, err := sys.tx.QueryContext(
		ctx, query, model.SystemTenantID.String(), userID.String(),
	)
	if err != nil {
		return nil, directoryUnavailable("enumerate retiring User sessions", err)
	}
	defer rows.Close() //nolint:errcheck // rows.Err below is authoritative
	var ids []model.ID
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, directoryUnavailable("decode retiring User session", err)
		}
		id := model.ID(raw)
		if err := validateCreateID(id); err != nil {
			return nil, directoryUnavailable("retiring User session id is not canonical", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, directoryUnavailable("enumerate retiring User sessions", err)
	}
	return ids, nil
}

// requireNoUserRetirementAuthority is a read-only replay postcondition. A
// valid tombstone and audit receipt never authorize opportunistic cleanup on a
// retry: any surviving direct grant or credential makes the historical result
// unavailable until an operator repairs the contradiction explicitly.
func (sys *systemScope) requireNoUserRetirementAuthority(
	ctx context.Context,
	userID model.ID,
) error {
	tokenIDs, err := sys.userRetirementTokenClosure(ctx, userID)
	if err != nil {
		return err
	}
	sessionIDs, err := sys.userRetirementSessionIDs(ctx, userID)
	if err != nil {
		return err
	}
	for _, target := range []struct {
		table     string
		predicate string
		args      []any
	}{
		{membershipDescriptor.Table, "user_id = ?", []any{userID.String()}},
		{userGroupMemberDescriptor.Table, "user_id = ?", []any{userID.String()}},
		{authSessionDescriptor.Table, "user_id = ?", []any{userID.String()}},
		{webauthnCredentialDescriptor.Table, "user_id = ?", []any{userID.String()}},
		{
			delegationHandleDescriptor.Table,
			"subject_user_id = ? OR act_as_user_id = ?",
			[]any{userID.String(), userID.String()},
		},
		{
			apiTokenDescriptor.Table,
			"user_id = ? OR act_as_user_id = ?",
			[]any{userID.String(), userID.String()},
		},
	} {
		if err := sys.requireNoUserRetirementRows(
			ctx, target.table, target.predicate, target.args...,
		); err != nil {
			return err
		}
	}
	for _, tokenID := range tokenIDs {
		for _, target := range []struct {
			table     string
			predicate string
			args      []any
		}{
			{
				apiTokenDescriptor.Table, "id = ? OR parent_token_id = ?",
				[]any{tokenID.String(), tokenID.String()},
			},
			{
				delegationHandleDescriptor.Table,
				"source_cred_kind = ? AND source_cred_id = ?",
				[]any{"token", tokenID.String()},
			},
			{pepServiceCredentialDescriptor.Table, "token_id = ?", []any{tokenID.String()}},
		} {
			if err := sys.requireNoUserRetirementRows(
				ctx, target.table, target.predicate, target.args...,
			); err != nil {
				return err
			}
		}
	}
	for _, sessionID := range sessionIDs {
		if err := sys.requireNoUserRetirementRows(
			ctx,
			delegationHandleDescriptor.Table,
			"source_cred_kind = ? AND source_cred_id = ?",
			"user",
			sessionID.String(),
		); err != nil {
			return err
		}
	}
	return nil
}

func (sys *systemScope) userRetirementTokenClosure(
	ctx context.Context,
	userID model.ID,
) ([]model.ID, error) {
	relation := directoryWriterRelation(sys.s.dia, apiTokenDescriptor.Table)
	query := sys.s.dia.Rebind(fmt.Sprintf(`
WITH RECURSIVE retired_tokens(id) AS (
  SELECT root.id
  FROM %s AS root
  WHERE root.tenant_id = ? AND (root.user_id = ? OR root.act_as_user_id = ?)
  UNION
  SELECT child.id
  FROM %s AS child
  JOIN retired_tokens AS parent ON child.parent_token_id = parent.id
  WHERE child.tenant_id = ?
)
SELECT id FROM retired_tokens ORDER BY id`, relation, relation))
	rows, err := sys.tx.QueryContext(
		ctx, query,
		model.SystemTenantID.String(), userID.String(), userID.String(),
		model.SystemTenantID.String(),
	)
	if err != nil {
		return nil, directoryUnavailable("enumerate retiring User token descendants", err)
	}
	defer rows.Close() //nolint:errcheck // rows.Err below is authoritative
	var ids []model.ID
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, directoryUnavailable("decode retiring User token descendant", err)
		}
		id, err := model.ParseID(raw)
		if err != nil || id.String() != raw {
			return nil, directoryUnavailable("retiring User token id is not canonical", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, directoryUnavailable("enumerate retiring User token descendants", err)
	}
	return ids, nil
}

func (sys *systemScope) deleteUserRetirementRows(
	ctx context.Context,
	table string,
	predicate string,
	args ...any,
) error {
	query := sys.s.dia.Rebind(
		"DELETE FROM " + directoryWriterRelation(sys.s.dia, table) +
			" WHERE tenant_id = ? AND (" + predicate + ")",
	)
	allArgs := append([]any{model.SystemTenantID.String()}, args...)
	if _, err := sys.tx.ExecContext(ctx, query, allArgs...); err != nil {
		return directoryUnavailable("remove retiring User authority from "+table, mapWriteErr(err))
	}
	return nil
}

func (sys *systemScope) deleteExactUserRetirementRow(
	ctx context.Context,
	table string,
	id model.ID,
) error {
	query := sys.s.dia.Rebind(
		"DELETE FROM " + directoryWriterRelation(sys.s.dia, table) +
			" WHERE tenant_id = ? AND id = ?",
	)
	result, err := sys.tx.ExecContext(
		ctx, query, model.SystemTenantID.String(), id.String(),
	)
	if err != nil {
		return directoryUnavailable("remove retiring User token", mapWriteErr(err))
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return directoryUnavailable("count removed retiring User token", err)
	}
	if rows != 1 {
		return directoryUnavailable(
			fmt.Sprintf("retiring User token delete affected %d rows, want one", rows), nil,
		)
	}
	return nil
}

func (sys *systemScope) requireNoUserRetirementRows(
	ctx context.Context,
	table string,
	predicate string,
	args ...any,
) error {
	query := sys.s.dia.Rebind(
		"SELECT COUNT(*) FROM " + directoryWriterRelation(sys.s.dia, table) +
			" WHERE tenant_id = ? AND (" + predicate + ")",
	)
	allArgs := append([]any{model.SystemTenantID.String()}, args...)
	var count int64
	if err := sys.tx.QueryRowContext(ctx, query, allArgs...).Scan(&count); err != nil {
		return directoryUnavailable("verify retiring User authority in "+table, err)
	}
	if count != 0 {
		return fmt.Errorf(
			"%w: %w: %s retains %d row(s)",
			store.ErrDirectoryUnavailable,
			store.ErrDirectoryRetirementResidualAuthority,
			table,
			count,
		)
	}
	return nil
}

func (sys *systemScope) refuseIdentityWithRecoverableAgents(
	ctx context.Context,
	tenant model.TenantID,
	identityID model.ID,
) error {
	count, err := countPhysicalAgentsForIdentity(
		ctx, sys.tx, sys.s.dia, tenant, identityID,
	)
	if err != nil {
		return err
	}
	if count != 0 {
		return fmt.Errorf(
			"%w: identity %s has %d physically recoverable Agent binding(s)",
			store.ErrDirectoryPrincipalHasBindings, identityID, count,
		)
	}
	return nil
}
