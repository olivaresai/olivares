// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

var errUserAuthorityOrder = errors.New("User authority must be acquired before tenant facts in canonical order")

type userAuthorityHeldState uint8

const (
	userAuthorityRowHeld userAuthorityHeldState = iota + 1
	userAuthorityAbsenceReserved
)

func (a *authScope) PrepareUserAuthorityWrite(ctx context.Context, ids []model.ID) (retErr error) {
	if a.ts.readOnly || a.ts.directoryWriter == nil {
		return store.ErrReadOnly
	}
	t := a.ts.directoryWriter
	defer func() { t.poison(retErr) }()
	return t.prepare(ctx, func() ([]model.TenantID, error) {
		return nil, t.lockUserAuthorities(ctx, ids)
	})
}

// lockUserAuthorities runs only after the global directory lock. It discovers
// no later User implicitly and cannot turn a covered lock into a new grant.
func (t *directoryWriteTracker) lockUserAuthorities(ctx context.Context, ids []model.ID) error {
	if !t.locked {
		return directoryUnavailable("User writer lacks the global directory lock", nil)
	}
	refs := make([]store.UserAuthorityFactRef, 0, len(ids))
	for _, id := range ids {
		refs = append(refs, store.UserAuthorityFactRef{UserID: id, Version: 1})
	}
	ordered, err := canonicalUserAuthorities(refs)
	if err != nil {
		return err
	}
	if t.heldUsers == nil {
		t.heldUsers = make(map[model.ID]userAuthorityHeldState)
	}
	for _, ref := range ordered {
		if t.heldUsers[ref.UserID] != 0 {
			continue
		}
		if len(t.bumped) != 0 || ref.UserID < t.lastHeldUser {
			t.poison(errUserAuthorityOrder)
			return errUserAuthorityOrder
		}
	}
	if err := bindDirectoryTenant(ctx, t.tx, t.dia, model.SystemTenantID); err != nil {
		return err
	}
	for _, ref := range ordered {
		if t.heldUsers[ref.UserID] != 0 {
			continue
		}
		_, found, err := readUserAuthorityRow(ctx, t.tx, t.dia, ref.UserID)
		if err != nil {
			return err
		}
		if !found {
			if t.control.Mode != directoryWriterStaged || t.control.CoverageProtocol != coverageProtocolLegacy {
				return directoryUnavailable("writer User authority is absent", nil)
			}
			// This records absence, never an H row or version. The same transaction
			// retains global; only a subsequent real writer may create that H.
			t.heldUsers[ref.UserID], t.lastHeldUser = userAuthorityAbsenceReserved, ref.UserID
			continue
		}
		if t.dia.Name() == store.EnginePostgres {
			var version int64
			if err := t.tx.QueryRowContext(ctx, "SELECT public.olivares_lock_core_user_authority($1)", ref.UserID.String()).Scan(&version); err != nil {
				return directoryUnavailable("lock writer User authority", err)
			}
			if version < 1 {
				return directoryUnavailable("writer User authority version is invalid", nil)
			}
		}
		t.heldUsers[ref.UserID], t.lastHeldUser = userAuthorityRowHeld, ref.UserID
	}
	return nil
}

func (t *directoryWriteTracker) bumpUserAuthorities(ctx context.Context, ids ...model.ID) error {
	if err := t.lockUserAuthorities(ctx, ids); err != nil {
		return err
	}
	if err := armDirectoryWriter(ctx, t.tx, t.dia, t.control); err != nil {
		return err
	}
	refs := make([]store.UserAuthorityFactRef, 0, len(ids))
	for _, id := range ids {
		refs = append(refs, store.UserAuthorityFactRef{UserID: id, Version: 1})
	}
	ordered, err := canonicalUserAuthorities(refs)
	if err != nil {
		return err
	}
	for _, ref := range ordered {
		old, found, err := readUserAuthorityRow(ctx, t.tx, t.dia, ref.UserID)
		if err != nil {
			return err
		}
		if !found && t.heldUsers[ref.UserID] == userAuthorityAbsenceReserved && t.control.Mode == directoryWriterStaged && t.control.CoverageProtocol == coverageProtocolLegacy {
			var live int
			query := t.dia.Rebind("SELECT COUNT(*) FROM " + directoryWriterRelation(t.dia, userDescriptor.Table) + " WHERE id=? AND tenant_id=?")
			if err := t.tx.QueryRowContext(ctx, query, ref.UserID.String(), model.SystemTenantID.String()).Scan(&live); err != nil || live != 1 {
				return directoryUnavailable("reserved H has no unique existing legacy User", err)
			}
			if err := t.insertUserAuthorityRow(ctx, ref.UserID); err != nil {
				return err
			}
			t.heldUsers[ref.UserID] = userAuthorityRowHeld
			continue
		}
		if !found || old.Version == math.MaxInt64 {
			return directoryUnavailable("User authority absent or exhausted", nil)
		}
		now, err := directoryTransactionNow(ctx, t.tx, t.dia)
		if err != nil {
			return err
		}
		query := t.dia.Rebind("UPDATE " + directoryWriterRelation(t.dia, userAuthorityDescriptor.Table) + " SET version = version + 1, updated_at = ? WHERE id = ? AND tenant_id = ? AND version = ? AND version < ?")
		result, err := t.tx.ExecContext(ctx, query, now.String(), ref.UserID.String(), model.SystemTenantID.String(), old.Version, int64(math.MaxInt64))
		if err != nil {
			return directoryUnavailable("bump User authority", err)
		}
		if count, err := result.RowsAffected(); err != nil || count != 1 {
			return directoryUnavailable("User authority CAS did not affect one row", err)
		}
	}
	return nil
}

func (t *directoryWriteTracker) insertUserAuthority(ctx context.Context, id model.ID) error {
	if !t.locked {
		return directoryUnavailable("new User lacks directory writer lock", nil)
	}
	if err := validateCreateID(id); err != nil {
		return err
	}
	if len(t.bumped) != 0 || id < t.lastHeldUser {
		return errUserAuthorityOrder
	}
	if err := bindDirectoryTenant(ctx, t.tx, t.dia, model.SystemTenantID); err != nil {
		return err
	}
	if _, found, err := readUserAuthorityRow(ctx, t.tx, t.dia, id); err != nil {
		return err
	} else if found {
		return fmt.Errorf("%w: User authority identity cannot be reused", store.ErrDirectoryPrincipalRetired)
	}
	var retired int
	query := t.dia.Rebind("SELECT COUNT(*) FROM " + directoryWriterRelation(t.dia, userTombstoneDescriptor.Table) + " WHERE principal_ref=? AND tenant_id=?")
	if err := t.tx.QueryRowContext(ctx, query, id.String(), model.SystemTenantID.String()).Scan(&retired); err != nil {
		return err
	}
	if retired != 0 {
		return store.ErrDirectoryPrincipalRetired
	}
	if err := armDirectoryWriter(ctx, t.tx, t.dia, t.control); err != nil {
		return err
	}
	if err := t.insertUserAuthorityRow(ctx, id); err != nil {
		return err
	}
	if t.heldUsers == nil {
		t.heldUsers = make(map[model.ID]userAuthorityHeldState)
	}
	t.heldUsers[id], t.lastHeldUser = userAuthorityRowHeld, id
	return nil
}

func (t *directoryWriteTracker) insertUserAuthorityRow(ctx context.Context, id model.ID) error {
	now, err := directoryTransactionNow(ctx, t.tx, t.dia)
	if err != nil {
		return err
	}
	query := t.dia.Rebind("INSERT INTO " + directoryWriterRelation(t.dia, userAuthorityDescriptor.Table) + "(id,tenant_id,created_at,updated_at,version) VALUES (?,?,?,?,1)")
	if _, err := t.tx.ExecContext(ctx, query, id.String(), model.SystemTenantID.String(), now.String(), now.String()); err != nil {
		return directoryUnavailable("insert User authority", err)
	}
	return nil
}

// No embedding: Users must not regain Delete through the wider typed repo.
type userAuthorityUsers struct {
	ts    *tenantScope
	inner *typedRepo[model.User]
}

func (r *userAuthorityUsers) Get(ctx context.Context, id model.ID) (model.User, error) {
	return r.inner.Get(ctx, id)
}
func (r *userAuthorityUsers) Lock(ctx context.Context, id model.ID) (_ model.User, retErr error) {
	if r.ts.readOnly || r.ts.directoryWriter == nil {
		return model.User{}, store.ErrReadOnly
	}
	t := r.ts.directoryWriter
	defer func() { t.poison(retErr) }()
	if err := t.prepare(ctx, func() ([]model.TenantID, error) { return nil, t.lockUserAuthorities(ctx, []model.ID{id}) }); err != nil {
		return model.User{}, err
	}
	return r.inner.Lock(ctx, id)
}
func (r *userAuthorityUsers) List(ctx context.Context, query model.Query) ([]model.User, model.Page, error) {
	return r.inner.List(ctx, query)
}
func (r *userAuthorityUsers) Create(ctx context.Context, in model.User) (_ model.User, retErr error) {
	if r.ts.readOnly || r.ts.directoryWriter == nil {
		return model.User{}, store.ErrReadOnly
	}
	t := r.ts.directoryWriter
	defer func() { t.poison(retErr) }()
	// Preserve typed Create's allocation contract: caller-supplied base IDs are
	// not adopted. Allocate once so H and the User have the same engine-owned ID.
	id := model.NewID()
	record, err := userCodec.Encode(in)
	if err != nil {
		return model.User{}, err
	}
	if err := t.prepare(ctx, func() ([]model.TenantID, error) { return nil, t.insertUserAuthority(ctx, id) }); err != nil {
		return model.User{}, err
	}
	full, err := r.inner.g.CreateWithID(ctx, id, record)
	if err != nil {
		return model.User{}, err
	}
	return r.inner.decode(full)
}
func (r *userAuthorityUsers) Update(ctx context.Context, in model.User) (model.User, error) {
	return newDirectoryTrackedRepo(r.inner, r.ts.directoryWriter, authUserDirectoryResolver(r.ts)).Update(ctx, in)
}

var _ store.AuthUserAuthorityWriter = (*authScope)(nil)
var _ store.MutableRepository[model.User] = (*userAuthorityUsers)(nil)
