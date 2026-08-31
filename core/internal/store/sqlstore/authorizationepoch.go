// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// authorizationEpochBeforeInsertTestHook is nil outside deterministic tests.
// It makes all-tenant backfill rollback observable without weakening the
// production transaction boundary.
var authorizationEpochBeforeInsertTestHook func(model.TenantID) error

// reconcileAuthorizationEpochs is the additive data forward path for existing
// tenants. The schema forward path is reconcileColumns: it creates the complete
// descriptor table when the frozen core migration already ran.
//
// The application transaction owns every write. PostgreSQL tenant enumeration
// uses only the independently configured read-only BYPASSRLS pool; without it
// the result is honestly incomplete and no generation is fabricated. The same
// global lifecycle lock as CreateOrg/DropTenant prevents orphan epoch rows.
func reconcileAuthorizationEpochs(
	ctx context.Context,
	db, adminDB *sql.DB,
	dia dialect.Dialect,
	adminRoleForBoot guardRoleFact,
) (bool, error) {
	if dia.Name() == store.EnginePostgres && adminDB == db {
		return false, nil
	}

	tx, err := db.BeginTx(ctx, directoryWriterTxOptions(dia))
	if err != nil {
		return false, fmt.Errorf("sqlstore: authorization epoch reconcile begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit

	if _, err := acquireDirectoryWriter(ctx, tx, dia); err != nil {
		return false, fmt.Errorf("sqlstore: authorization epoch reconcile lock: %w", err)
	}
	inventory, err := openDirectoryReconcileInventory(
		ctx, tx, adminDB, dia, adminRoleForBoot,
	)
	if err != nil {
		return false, fmt.Errorf("sqlstore: authorization epoch reconcile inventory: %w", err)
	}
	defer inventory.close()
	tenants, err := enumerateDirectoryTenants(ctx, inventory.queryer, dia)
	if err != nil {
		return false, fmt.Errorf("enumerate authorization epoch tenants: %w", err)
	}
	for _, tenant := range tenants {
		if err := bindDirectoryTenant(ctx, tx, dia, tenant); err != nil {
			return false, fmt.Errorf("bind authorization epoch tenant %s: %w", tenant, err)
		}
		_, found, err := readAuthorizationEpochRow(ctx, tx, dia, tenant)
		if err != nil {
			return false, fmt.Errorf("read authorization epoch for tenant %s: %w", tenant, err)
		}
		if found {
			continue
		}
		if err := insertAuthorizationEpochRow(ctx, tx, dia, tenant); err != nil {
			return false, fmt.Errorf("backfill authorization epoch for tenant %s: %w", tenant, err)
		}
	}
	if err := restoreSystemDirectoryBaseline(ctx, tx, dia); err != nil {
		return false, fmt.Errorf("restore system baseline after authorization backfill: %w", err)
	}
	if err := inventory.commit(); err != nil {
		return false, fmt.Errorf("sqlstore: authorization epoch reconcile inventory: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("sqlstore: authorization epoch reconcile commit: %w", err)
	}
	return true, nil
}

func insertAuthorizationEpochRow(
	ctx context.Context,
	tx *sql.Tx,
	dia dialect.Dialect,
	tenant model.TenantID,
) error {
	if authorizationEpochBeforeInsertTestHook != nil {
		if err := authorizationEpochBeforeInsertTestHook(tenant); err != nil {
			return err
		}
	}
	now, err := directoryTransactionNow(ctx, tx, dia)
	if err != nil {
		return err
	}
	epoch := model.AuthorizationEpoch{BaseFields: model.BaseFields{
		ID: model.ID(tenant), TenantID: tenant,
		CreatedAt: now, UpdatedAt: now, Version: 1,
	}}
	rec, err := authorizationEpochCodec.Encode(epoch)
	if err != nil {
		return err
	}
	baseToRecord(rec, epoch.BaseFields, false)
	cols := authorizationEpochDescriptor.AllColumns()
	args := make([]any, len(cols))
	for i, col := range cols {
		args[i] = rec[col]
	}
	query := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)",
		directoryWriterRelation(dia, authorizationEpochDescriptor.Table),
		strings.Join(cols, ", "), placeholders(len(cols)))
	result, err := tx.ExecContext(ctx, dia.Rebind(query), args...)
	if err != nil {
		return mapWriteErr(err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return fmt.Errorf("inserted %d authorization epoch rows, want exactly one", rows)
	}
	return nil
}

func readAuthorizationEpochRow(
	ctx context.Context,
	tx *sql.Tx,
	dia dialect.Dialect,
	tenant model.TenantID,
) (model.AuthorizationEpoch, bool, error) {
	cols := authorizationEpochDescriptor.AllColumns()
	query := dia.Rebind("SELECT " + strings.Join(cols, ", ") + " FROM " +
		directoryWriterRelation(dia, authorizationEpochDescriptor.Table) +
		" WHERE tenant_id = ? LIMIT 2")
	rows, err := tx.QueryContext(ctx, query, tenant.String())
	if err != nil {
		return model.AuthorizationEpoch{}, false, err
	}
	defer rows.Close()

	var records []model.Record
	for rows.Next() {
		state, err := newScanState(authorizationEpochDescriptor, cols)
		if err != nil {
			return model.AuthorizationEpoch{}, false, err
		}
		if err := rows.Scan(state.dests...); err != nil {
			return model.AuthorizationEpoch{}, false, err
		}
		records = append(records, state.record())
	}
	if err := rows.Err(); err != nil {
		return model.AuthorizationEpoch{}, false, err
	}
	switch len(records) {
	case 0:
		return model.AuthorizationEpoch{}, false, nil
	case 1:
		base, err := baseFromRecord(records[0])
		if err != nil {
			return model.AuthorizationEpoch{}, false, err
		}
		epoch, err := authorizationEpochCodec.Decode(base, records[0])
		if err != nil {
			return model.AuthorizationEpoch{}, false, err
		}
		if epoch.TenantID != tenant {
			return model.AuthorizationEpoch{}, false,
				fmt.Errorf("authorization epoch tenant %s does not match scope %s", epoch.TenantID, tenant)
		}
		return epoch, true, nil
	default:
		return model.AuthorizationEpoch{}, false,
			fmt.Errorf("more than one authorization epoch row for tenant %s", tenant)
	}
}

// ReadAuthorizationEpoch returns only the exact fact witness for this tenant's
// surrounding transaction. Missing, duplicate or malformed storage is UNKNOWN.
func (sc *tenantScope) ReadAuthorizationEpoch(
	ctx context.Context,
) (store.AuthorizationFactRef, error) {
	epoch, found, err := readAuthorizationEpochRow(ctx, sc.tx, sc.s.dia, sc.tenant)
	if err != nil {
		return store.AuthorizationFactRef{}, authorizationEpochUnavailable("read", err)
	}
	if !found {
		return store.AuthorizationFactRef{}, authorizationEpochUnavailable("row is absent", nil)
	}
	return store.AuthorizationFactRef{
		Kind: model.AuthorizationEpochKind, ID: epoch.ID, Version: epoch.Version,
	}, nil
}

// BumpAuthorizationEpoch advances the exact prior witness with a database-time
// CAS. It never inserts or treats absence as zero, and exhaustion cannot wrap a
// positive generation into authorization evidence.
func (sc *tenantScope) BumpAuthorizationEpoch(
	ctx context.Context,
	expected store.AuthorizationFactRef,
) (store.AuthorizationFactRef, error) {
	if sc.readOnly {
		return store.AuthorizationFactRef{}, store.ErrReadOnly
	}
	if expected.Kind != model.AuthorizationEpochKind || expected.ID != model.ID(sc.tenant) ||
		expected.Version < 1 {
		return store.AuthorizationFactRef{}, authorizationEpochUnavailable("expected witness is malformed", nil)
	}
	current, found, err := readAuthorizationEpochRow(ctx, sc.tx, sc.s.dia, sc.tenant)
	if err != nil {
		return store.AuthorizationFactRef{}, authorizationEpochUnavailable("read before bump", err)
	}
	if !found || current.ID != expected.ID || current.Version != expected.Version {
		return store.AuthorizationFactRef{}, authorizationEpochUnavailable("expected witness is stale", nil)
	}
	if current.Version == math.MaxInt64 {
		return store.AuthorizationFactRef{}, authorizationEpochUnavailable("generation is exhausted", nil)
	}
	now, err := directoryTransactionNow(ctx, sc.tx, sc.s.dia)
	if err != nil {
		return store.AuthorizationFactRef{}, authorizationEpochUnavailable("read database clock", err)
	}
	next := current.Version + 1
	query := sc.s.dia.Rebind("UPDATE " +
		directoryWriterRelation(sc.s.dia, authorizationEpochDescriptor.Table) +
		" SET updated_at = ?, version = ?" +
		" WHERE id = ? AND tenant_id = ? AND version = ?")
	result, err := sc.tx.ExecContext(ctx, query, now.String(), next,
		expected.ID.String(), sc.tenant.String(), expected.Version)
	if err != nil {
		return store.AuthorizationFactRef{}, authorizationEpochUnavailable("bump", mapWriteErr(err))
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return store.AuthorizationFactRef{}, authorizationEpochUnavailable("count bump", err)
	}
	if rows != 1 {
		return store.AuthorizationFactRef{}, authorizationEpochUnavailable(
			fmt.Sprintf("CAS affected %d rows", rows), nil,
		)
	}
	return store.AuthorizationFactRef{
		Kind: model.AuthorizationEpochKind, ID: expected.ID, Version: next,
	}, nil
}

func authorizationEpochUnavailable(what string, err error) error {
	if errors.Is(err, store.ErrAuthorizationEpochUnavailable) {
		return err
	}
	if err == nil {
		return fmt.Errorf("%w: %s", store.ErrAuthorizationEpochUnavailable, what)
	}
	return fmt.Errorf("%w: %s: %v", store.ErrAuthorizationEpochUnavailable, what, err)
}

var _ store.AuthorizationEpochStore = (*tenantScope)(nil)
