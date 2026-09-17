// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

const (
	coreUserAuthorityMigrationVersion = 10
	coverageProtocolLegacy            = "membership-union-v1"
	coverageProtocolTarget            = "user-authority-v1"
	directoryCoverageProtocolGUC      = "app.directory_coverage_protocol"
)

// H is retained SYSTEM evidence. It has no public repository, payload, delete
// operation or soft-delete column, and does not join the append-only edition.
var userAuthorityDescriptor = model.EntityDescriptor{
	Kind:                   model.UserAuthorityKind,
	Table:                  "core_user_authority",
	AuthorizationFact:      true,
	AuthorizationLockOrder: 4,
	RetainOnTenantDrop:     true,
	Checks: []string{
		fmt.Sprintf("tenant_id = '%s'", model.SystemTenantID),
		"version >= 1",
	},
}

func canonicalUserAuthorities(in []store.UserAuthorityFactRef) ([]store.UserAuthorityFactRef, error) {
	byID := make(map[model.ID]int64, len(in))
	for _, ref := range in {
		if err := (model.UserAuthority{BaseFields: model.BaseFields{
			ID: ref.UserID, TenantID: model.SystemTenantID, Version: ref.Version,
		}}).Validate(); err != nil {
			return nil, directoryUnavailable("malformed User authority witness", err)
		}
		if version, present := byID[ref.UserID]; present && version != ref.Version {
			return nil, directoryUnavailable("conflicting User authority versions", nil)
		}
		byID[ref.UserID] = ref.Version
	}
	out := make([]store.UserAuthorityFactRef, 0, len(byID))
	for id, version := range byID {
		out = append(out, store.UserAuthorityFactRef{UserID: id, Version: version})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UserID < out[j].UserID })
	return out, nil
}

// readUserAuthorityRow is read-only and never substitutes zero or repairs a
// missing row. Its caller has already bound SYSTEM on this exact transaction.
func readUserAuthorityRow(ctx context.Context, q directoryTenantEnumerator, dia dialect.Dialect, id model.ID) (store.UserAuthorityFactRef, bool, error) {
	if err := validateCreateID(id); err != nil {
		return store.UserAuthorityFactRef{}, false, directoryUnavailable("User authority id", err)
	}
	query := dia.Rebind("SELECT id, tenant_id, version FROM " + directoryWriterRelation(dia, userAuthorityDescriptor.Table) + " WHERE id = ? AND tenant_id = ?")
	rows, err := q.QueryContext(ctx, query, id.String(), model.SystemTenantID.String())
	if err != nil {
		return store.UserAuthorityFactRef{}, false, directoryUnavailable("read User authority", err)
	}
	defer rows.Close()
	var out store.UserAuthorityFactRef
	var count int
	for rows.Next() {
		count++
		var observedID, tenant string
		if err := rows.Scan(&observedID, &tenant, &out.Version); err != nil {
			return store.UserAuthorityFactRef{}, false, directoryUnavailable("decode User authority", err)
		}
		if count != 1 || observedID != id.String() || tenant != model.SystemTenantID.String() || out.Version < 1 {
			return store.UserAuthorityFactRef{}, false, directoryUnavailable("invalid User authority row", nil)
		}
		out.UserID = id
	}
	if err := rows.Err(); err != nil {
		return store.UserAuthorityFactRef{}, false, directoryUnavailable("read User authority rows", err)
	}
	return out, count == 1, nil
}

var userAuthorityRestoreTestHook func() error

// withUserAuthorityBinding retains both writer protocols and all acquired row
// locks. In particular it must not call bindDirectoryTenant, which clears the
// SQLite directory marker. No rebind capability escapes this package.
func (sc *tenantScope) withUserAuthorityBinding(ctx context.Context, fn func() error) (retErr error) {
	if sc.bindingPoison != nil {
		return sc.bindingPoison
	}
	presented, err := readUserAuthorityPresentation(ctx, sc.tx, sc.s.dia)
	if err != nil || presented != sc.tenant.String() {
		sc.bindingPoison = directoryUnavailable("User authority binding does not match scope", err)
		return sc.bindingPoison
	}
	defer func() {
		restoreCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), systemRestoreTimeout)
		defer cancel()
		var err error
		if userAuthorityRestoreTestHook != nil {
			err = userAuthorityRestoreTestHook()
		}
		if err == nil {
			err = sc.s.dia.BindTenant(restoreCtx, sc.tx, model.TenantID(presented))
		}
		if err == nil {
			var restored string
			restored, err = readUserAuthorityPresentation(restoreCtx, sc.tx, sc.s.dia)
			if err == nil && restored != presented {
				err = errors.New("restored tenant differs from captured presentation")
			}
		}
		if err != nil {
			sc.bindingPoison = directoryUnavailable("restore User authority binding", err)
			retErr = errors.Join(retErr, sc.bindingPoison)
		}
	}()
	if err := sc.s.dia.BindTenant(ctx, sc.tx, model.SystemTenantID); err != nil {
		return err
	}
	return fn()
}

func readUserAuthorityPresentation(ctx context.Context, tx *sql.Tx, dia dialect.Dialect) (string, error) {
	if dia.Name() == store.EnginePostgres {
		var tenant string
		err := tx.QueryRowContext(ctx, "SELECT pg_catalog.current_setting('app.tenant_id',true)").Scan(&tenant)
		return tenant, err
	}
	// #nosec G202 -- SQLite branch: the only interpolated text is quoteIdent(dialect.ScopeTenantTable), the package constant "_scope_tenant" in identifier quoting; a SQL identifier cannot be a bind parameter and this statement carries no values.
	rows, err := tx.QueryContext(ctx, "SELECT tenant_id,typeof(tenant_id) FROM main."+quoteIdent(dialect.ScopeTenantTable))
	if err != nil {
		return "", err
	}
	var tenant, storage string
	count := 0
	for rows.Next() {
		count++
		if err := rows.Scan(&tenant, &storage); err != nil {
			_ = rows.Close()
			return "", err
		}
		if count > 1 || storage != "text" {
			_ = rows.Close()
			return "", directoryUnavailable("SQLite tenant presentation is not a unique text row", nil)
		}
	}
	if err := closeCoreDirectoryRows(rows); err != nil {
		return "", err
	}
	if count != 1 {
		return "", directoryUnavailable("SQLite tenant presentation is absent", nil)
	}
	return tenant, nil
}

// LockAuthoritySnapshotBundle validates the entire grammar before acquiring H,
// then retains every H while applying the unchanged tenant-fact algorithm.
func (sc *tenantScope) LockAuthoritySnapshotBundle(ctx context.Context, bundle store.AuthoritySnapshotBundle) error {
	if sc.readOnly {
		return store.ErrReadOnly
	}
	if sc.authorityLocked {
		return errors.New("sqlstore: authority bundle must precede other authority locks")
	}
	sc.authorityLocked = true
	if sc.directoryWriter != nil && !sc.directoryWriter.locked {
		sc.directoryWriter.authorityBeforeDirectory = true
	}
	facts, identityTable, err := sc.prepareAuthoritySnapshot(bundle.Facts)
	if err != nil {
		return err
	}
	users, err := canonicalUserAuthorities(bundle.UserAuthorities)
	if err != nil {
		return err
	}
	if len(users) != 0 {
		if err := reserveSQLiteDirectoryWriter(ctx, sc.tx, sc.s.dia); err != nil {
			return err
		}
		err = sc.withUserAuthorityBinding(ctx, func() error {
			for _, ref := range users {
				if sc.s.engine == store.EnginePostgres {
					var version int64
					if err := sc.tx.QueryRowContext(ctx, "SELECT public.olivares_lock_core_user_authority($1)", ref.UserID.String()).Scan(&version); err != nil {
						return directoryUnavailable("lock User authority", err)
					}
					if version != ref.Version {
						return store.ErrConflict
					}
				} else {
					current, found, err := readUserAuthorityRow(ctx, sc.tx, sc.s.dia, ref.UserID)
					if err != nil {
						return err
					}
					if !found {
						return directoryUnavailable("User authority is absent", nil)
					}
					if current != ref {
						return store.ErrConflict
					}
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return sc.applyAuthoritySnapshot(ctx, facts, identityTable, true)
}

var _ store.AuthoritySnapshotBundleLocker = (*tenantScope)(nil)
var _ directoryTenantEnumerator = (*sql.Tx)(nil)
