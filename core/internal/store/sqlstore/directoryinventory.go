// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"fmt"
	"sort"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/model"
)

// SystemOrgWitness is a separate, validated inventory fact. It is deliberately
// not a tenant in BusinessDirectoryInventory and can never enter a G loop.
type SystemOrgWitness struct {
	ID       model.ID
	TenantID model.TenantID
}

type BusinessDirectoryInventory struct {
	System          SystemOrgWitness
	BusinessTenants []model.TenantID
	Epochs          map[model.TenantID]int64
}

type directoryInventoryRow struct {
	kind, id, tenant string
	version          sql.NullInt64
}

// decodeDirectoryInventory requires the complete SYSTEM/business witness.
// Ordinary staged reconciliation uses the same row grammar below to prove its
// narrowly permitted pre-genesis empty state; that observation is not coverage.
func decodeDirectoryInventory(rows []directoryInventoryRow) (BusinessDirectoryInventory, error) {
	out, err := decodeDirectoryInventoryRows(rows)
	if err != nil {
		return out, err
	}
	return out, out.requireComplete()
}

// decodeDirectoryInventoryRows is the shared structural grammar. It refuses
// malformed witnesses and orphan G even for a partial staged observation.
func decodeDirectoryInventoryRows(rows []directoryInventoryRow) (BusinessDirectoryInventory, error) {
	out := BusinessDirectoryInventory{Epochs: make(map[model.TenantID]int64)}
	orgs := make(map[model.TenantID]bool)
	for _, row := range rows {
		id, tenant := model.ID(row.id), model.TenantID(row.tenant)
		switch row.kind {
		case "org":
			if row.version.Valid {
				return out, directoryUnavailable("organization inventory has a version", nil)
			}
			if tenant.IsSystem() {
				if id != model.ID(model.SystemTenantID) || out.System.ID != "" {
					return out, directoryUnavailable("invalid or duplicate SYSTEM organization witness", nil)
				}
				out.System = SystemOrgWitness{ID: id, TenantID: tenant}
				continue
			}
			if err := (model.DirectoryEpoch{BaseFields: model.BaseFields{ID: id, TenantID: tenant, Version: 1}}).Validate(); err != nil {
				return out, directoryUnavailable("noncanonical business organization inventory", err)
			}
			if orgs[tenant] {
				return out, directoryUnavailable("duplicate business organization", nil)
			}
			orgs[tenant] = true
			out.BusinessTenants = append(out.BusinessTenants, tenant)
		case "directory_epoch":
			if !row.version.Valid {
				return out, directoryUnavailable("directory epoch inventory lacks a version", nil)
			}
			if err := (model.DirectoryEpoch{BaseFields: model.BaseFields{ID: id, TenantID: tenant, Version: row.version.Int64}}).Validate(); err != nil {
				return out, directoryUnavailable("noncanonical directory epoch inventory", err)
			}
			if _, exists := out.Epochs[tenant]; exists {
				return out, directoryUnavailable("duplicate directory epoch", nil)
			}
			out.Epochs[tenant] = row.version.Int64
		default:
			return out, directoryUnavailable("unknown directory inventory kind", nil)
		}
	}
	sort.Slice(out.BusinessTenants, func(i, j int) bool { return out.BusinessTenants[i] < out.BusinessTenants[j] })
	for tenant := range out.Epochs {
		if !orgs[tenant] {
			return out, directoryUnavailable("orphan directory epoch", nil)
		}
	}
	return out, nil
}

func (i BusinessDirectoryInventory) requireComplete() error {
	if i.System.ID != model.ID(model.SystemTenantID) || i.System.TenantID != model.SystemTenantID {
		return directoryUnavailable("SYSTEM organization witness is absent", nil)
	}
	if len(i.BusinessTenants) != len(i.Epochs) {
		return directoryUnavailable("business organization/directory epoch inventory is incomplete", nil)
	}
	for _, tenant := range i.BusinessTenants {
		if i.Epochs[tenant] < 1 {
			return directoryUnavailable("business organization lacks directory epoch", nil)
		}
	}
	return nil
}

func readDirectoryInventory(ctx context.Context, q directoryTenantEnumerator, dia dialect.Dialect, closedRoutine, allowMissing bool) (BusinessDirectoryInventory, error) {
	query := "SELECT 'org', id, tenant_id, NULL FROM " + directoryWriterRelation(dia, orgDescriptor.Table) +
		" UNION ALL SELECT 'directory_epoch', id, tenant_id, version FROM " + directoryWriterRelation(dia, directoryEpochDescriptor.Table)
	if closedRoutine {
		query = "SELECT object_kind,id,tenant_id,version FROM public.olivares_directory_inventory_v1()"
	}
	rows, err := q.QueryContext(ctx, query)
	if err != nil {
		return BusinessDirectoryInventory{}, directoryUnavailable("read authoritative directory inventory", err)
	}
	var raw []directoryInventoryRow
	for rows.Next() {
		var row directoryInventoryRow
		if err := rows.Scan(&row.kind, &row.id, &row.tenant, &row.version); err != nil {
			_ = rows.Close()
			return BusinessDirectoryInventory{}, directoryUnavailable("decode directory inventory row", err)
		}
		raw = append(raw, row)
	}
	if err := closeCoreDirectoryRows(rows); err != nil {
		return BusinessDirectoryInventory{}, err
	}
	if allowMissing {
		return decodeDirectoryInventoryRows(raw)
	}
	return decodeDirectoryInventory(raw)
}

type userAuthorityStoredRow struct {
	CreatedAt, UpdatedAt string
	Version              int64
}
type userAuthorityCoverage struct {
	Rows    map[model.ID]userAuthorityStoredRow
	Users   []model.ID
	Missing []model.ID
}

// readUserAuthorityCoverage runs under SYSTEM on the caller's exact transaction.
// It validates every retained H, including retired identities, and never repairs.
func readUserAuthorityCoverage(ctx context.Context, tx *sql.Tx, dia dialect.Dialect) (userAuthorityCoverage, error) {
	out := userAuthorityCoverage{Rows: make(map[model.ID]userAuthorityStoredRow)}
	// #nosec G202 -- the only interpolated text is directoryWriterRelation(dia, userAuthorityDescriptor.Table), the compiled core descriptor's "core_user_authority" in engine identifier quoting; the projection is a fixed column list and the statement binds nothing.
	rows, err := tx.QueryContext(ctx, "SELECT id,tenant_id,created_at,updated_at,version FROM "+directoryWriterRelation(dia, userAuthorityDescriptor.Table)+" ORDER BY id")
	if err != nil {
		return out, directoryUnavailable("read User authority coverage", err)
	}
	for rows.Next() {
		var id model.ID
		var tenant model.TenantID
		var row userAuthorityStoredRow
		if err := rows.Scan(&id, &tenant, &row.CreatedAt, &row.UpdatedAt, &row.Version); err != nil {
			_ = rows.Close()
			return out, err
		}
		if err := (model.UserAuthority{BaseFields: model.BaseFields{ID: id, TenantID: tenant, Version: row.Version}}).Validate(); err != nil {
			_ = rows.Close()
			return out, err
		}
		if _, exists := out.Rows[id]; exists {
			_ = rows.Close()
			return out, directoryUnavailable("duplicate User authority", nil)
		}
		if _, err := model.ParseTimestamp(row.CreatedAt); err != nil {
			_ = rows.Close()
			return out, err
		}
		if _, err := model.ParseTimestamp(row.UpdatedAt); err != nil {
			_ = rows.Close()
			return out, err
		}
		out.Rows[id] = row
	}
	if err := closeCoreDirectoryRows(rows); err != nil {
		return out, err
	}
	// #nosec G202 -- same closed construction: directoryWriterRelation over userDescriptor.Table, the compiled core descriptor's "users".
	rows, err = tx.QueryContext(ctx, "SELECT id,tenant_id FROM "+directoryWriterRelation(dia, userDescriptor.Table)+" ORDER BY id")
	if err != nil {
		return out, err
	}
	seen := make(map[model.ID]bool)
	for rows.Next() {
		var id model.ID
		var tenant model.TenantID
		if err := rows.Scan(&id, &tenant); err != nil {
			_ = rows.Close()
			return out, err
		}
		if err := validateCreateID(id); err != nil || tenant != model.SystemTenantID || seen[id] {
			_ = rows.Close()
			return out, directoryUnavailable("noncanonical or duplicate User in authority coverage", err)
		}
		seen[id] = true
		out.Users = append(out.Users, id)
		if _, exists := out.Rows[id]; !exists {
			out.Missing = append(out.Missing, id)
		}
	}
	if err := closeCoreDirectoryRows(rows); err != nil {
		return out, err
	}
	return out, nil
}

func backfillUserAuthorityCoverage(ctx context.Context, tx *sql.Tx, dia dialect.Dialect, state directoryWriterControlState, before userAuthorityCoverage) error {
	if err := bindDirectoryTenant(ctx, tx, dia, model.SystemTenantID); err != nil {
		return err
	}
	if err := armLegacyDirectoryActivation(ctx, tx, dia, state); err != nil {
		return err
	}
	now, err := directoryTransactionNow(ctx, tx, dia)
	if err != nil {
		return err
	}
	for _, id := range before.Missing {
		query := dia.Rebind("INSERT INTO " + directoryWriterRelation(dia, userAuthorityDescriptor.Table) + "(id,tenant_id,created_at,updated_at,version) VALUES (?,?,?,?,1)")
		if _, err := tx.ExecContext(ctx, query, id.String(), model.SystemTenantID.String(), now.String(), now.String()); err != nil {
			return fmt.Errorf("backfill User authority: %w", err)
		}
	}
	return nil
}
