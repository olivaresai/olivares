// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"errors"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

var _ store.AuthUserAuthorityEvidenceScope = (*authScope)(nil)
var _ store.AuthoritySnapshotBundleReader = (*tenantScope)(nil)

func (a *authScope) ReadUserAuthorityFact(ctx context.Context, id model.ID) (store.UserAuthorityFactRef, error) {
	if a.ts.bindingPoison != nil {
		return store.UserAuthorityFactRef{}, a.ts.bindingPoison
	}
	if err := validateCreateID(id); err != nil {
		return store.UserAuthorityFactRef{}, directoryUnavailable("User authority id", err)
	}
	if a.ts.tenant != model.SystemTenantID {
		return store.UserAuthorityFactRef{}, a.ts.abortReadBinding(directoryUnavailable("User authority requires SYSTEM scope", nil))
	}
	if _, err := a.ts.readAuthorityPresentation(ctx); err != nil {
		return store.UserAuthorityFactRef{}, err
	}
	return a.ts.readUserAuthorityFact(ctx, id)
}

func (sc *tenantScope) ValidateAuthoritySnapshotBundle(ctx context.Context, bundle store.AuthoritySnapshotBundle) error {
	if sc.bindingPoison != nil {
		return sc.bindingPoison
	}
	if !sc.readOnly {
		return errors.New("sqlstore: read authority barrier requires View")
	}
	if len(bundle.UserAuthorities) > 64 {
		return directoryUnavailable("read authority bundle exceeds 64 User references", nil)
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
		if err := sc.withReadAuthorityBinding(ctx, model.SystemTenantID, func() error {
			for _, ref := range users {
				current, err := sc.readUserAuthorityFact(ctx, ref.UserID)
				if err != nil {
					return err
				}
				if current != ref {
					return store.ErrConflict
				}
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return sc.applyAuthoritySnapshot(ctx, facts, identityTable, false)
}

// The strict read path is separate from the mutation reader's decoding contract.
func (sc *tenantScope) readUserAuthorityFact(ctx context.Context, id model.ID) (store.UserAuthorityFactRef, error) {
	projection := "id, tenant_id, version"
	sqlite := sc.s.dia.Name() == store.EngineSQLite
	if sqlite {
		projection += ", typeof(id), typeof(tenant_id), typeof(version)"
	}
	query := sc.s.dia.Rebind("SELECT " + projection + " FROM " + directoryWriterRelation(sc.s.dia, userAuthorityDescriptor.Table) + " WHERE id = ? AND tenant_id = ?")
	rows, err := sc.tx.QueryContext(ctx, query, id.String(), model.SystemTenantID.String())
	if err != nil {
		return store.UserAuthorityFactRef{}, directoryUnavailable("read User authority", err)
	}
	return decodeReadUserAuthority(rows, id, sqlite)
}

type userAuthorityReadRows interface {
	Next() bool
	Scan(...any) error
	Err() error
	Close() error
}

func decodeReadUserAuthority(rows userAuthorityReadRows, id model.ID, sqlite bool) (out store.UserAuthorityFactRef, retErr error) {
	defer func() {
		retErr = errors.Join(retErr, rows.Close(), rows.Err())
		if retErr != nil {
			out = store.UserAuthorityFactRef{}
			retErr = directoryUnavailable("decode User authority read", retErr)
		}
	}()
	count := 0
	for rows.Next() {
		count++
		var observedID, tenant string
		var version int64
		var idStorage, tenantStorage, versionStorage string
		columns := []any{&observedID, &tenant, &version}
		if sqlite {
			columns = append(columns, &idStorage, &tenantStorage, &versionStorage)
		}
		if err := rows.Scan(columns...); err != nil {
			return out, err
		}
		if count != 1 || observedID != id.String() || tenant != model.SystemTenantID.String() || version < 1 ||
			(sqlite && (idStorage != "text" || tenantStorage != "text" || versionStorage != "integer")) {
			return out, errors.New("noncanonical User authority row")
		}
		out = store.UserAuthorityFactRef{UserID: id, Version: version}
	}
	if count != 1 {
		return out, errors.New("User authority is absent")
	}
	return out, nil
}

func (sc *tenantScope) readAuthorityPresentation(ctx context.Context) (string, error) {
	if sc.bindingPoison != nil {
		return "", sc.bindingPoison
	}
	presented, err := readUserAuthorityPresentation(ctx, sc.tx, sc.s.dia)
	if err != nil || presented != sc.tenant.String() {
		return "", sc.abortReadBinding(directoryUnavailable("read authority binding does not match scope", err))
	}
	return presented, nil
}

// Termination also disables repositories obtained before the binding failure.
// Mutate retains its existing poison-at-commit behavior.
func (sc *tenantScope) abortReadBinding(cause error) error {
	if sc.bindingPoison == nil {
		sc.bindingPoison = cause
		if sc.readOnly {
			sc.bindingPoison = errors.Join(sc.bindingPoison, sc.tx.Rollback())
		}
	}
	return sc.bindingPoison
}

func (sc *tenantScope) withReadAuthorityBinding(ctx context.Context, tenant model.TenantID, fn func() error) (retErr error) {
	presented, err := sc.readAuthorityPresentation(ctx)
	if err != nil {
		return err
	}
	var bindErr error
	defer func() {
		restoreCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), systemRestoreTimeout)
		defer cancel()
		restoreErr := sc.s.dia.BindTenant(restoreCtx, sc.tx, model.TenantID(presented))
		if restoreErr == nil {
			var restored string
			restored, restoreErr = readUserAuthorityPresentation(restoreCtx, sc.tx, sc.s.dia)
			if restoreErr == nil && restored != presented {
				restoreErr = errors.New("restored tenant differs from captured presentation")
			}
		}
		if restoreErr != nil {
			restoreErr = directoryUnavailable("restore read authority binding", restoreErr)
		}
		if bindErr != nil || restoreErr != nil {
			retErr = errors.Join(retErr, sc.abortReadBinding(errors.Join(bindErr, restoreErr)))
		}
	}()
	if err := sc.s.dia.BindTenant(ctx, sc.tx, tenant); err != nil {
		bindErr = directoryUnavailable("bind read authority partition", err)
		return bindErr
	}
	return fn()
}
