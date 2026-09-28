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

// The write seam. A tenant row whose counted columns (the ones whose reference
// lets an account act in the tenant or binds it to a duty there) name an
// existing account is written only by a transaction that pinned that account's
// authority version through the directory authority barrier. The barrier is
// what makes a concurrent offboard and this write serialize: the offboard moves
// the version, so a writer that pinned the old one conflicts, re-reads the
// account's standing and refuses. A write that did not pin it is refused here,
// before any SQL, whichever writer issued it. An id that names no account passes:
// account ids are allocated by the engine, so such an id can never name one.
//
// One exception, and only one: the recipients of a message delivery and of an
// audience arc (memberBoundColumns). Their writers fan out to current members of
// the tenant's directory under the directory fact they already lock, so a fan-out
// may name more accounts than one barrier pins; the offboard removes the
// membership that admission requires, and it moves that fact. The seam admits a
// reference there without a pin only in a transaction that locked the tenant's
// directory fact. A member-bound declaration anywhere else is read as an
// ordinary counted reference, and readiness reports it.

// memberBoundColumns are the counted columns, as kind.column, whose member-bound
// declaration the store honours.
var memberBoundColumns = map[string]bool{
	"sessions.message_delivery.recipient_ref":           true,
	"sessions.message_audience_recipient.recipient_ref": true,
}

// memberBoundColumn reports whether f of desc is a member-bound declaration the
// store honours.
func memberBoundColumn(desc model.EntityDescriptor, f model.FieldSpec) bool {
	return f.Principal != nil && f.Principal.MemberBound && memberBoundColumns[string(desc.Kind)+"."+f.Name]
}

// userReference is one account id a row names through a counted column, and
// whether that column is one of the member-bound columns.
type userReference struct {
	id          model.ID
	memberBound bool
}

// countedUserReferences returns the account ids rec names through desc's
// counted column declarations.
func countedUserReferences(desc model.EntityDescriptor, rec model.Record) ([]userReference, error) {
	var out []userReference
	for _, f := range desc.Fields {
		if f.Principal == nil || !f.Principal.Counted() {
			continue
		}
		ids, err := f.Principal.CountedUserIDs(rec, f.Name)
		if err != nil {
			return nil, fmt.Errorf("sqlstore: %s.%s: %w", desc.Kind, f.Name, err)
		}
		bound := memberBoundColumn(desc, f)
		for _, id := range ids {
			out = append(out, userReference{id: id, memberBound: bound})
		}
	}
	return out, nil
}

// checkUserReferences runs the seam for a row about to be inserted or updated.
func (r *genericRepo) checkUserReferences(ctx context.Context, rec model.Record) error {
	if r.userRefs == nil {
		return nil
	}
	refs, err := countedUserReferences(r.desc, rec)
	if err != nil || len(refs) == 0 {
		return err
	}
	return r.userRefs(ctx, refs)
}

// checkChangedUserReferences runs the seam for an update: only the accounts the
// new values name and the stored row does not are checked, so an update that
// leaves the counted values as they were needs no fence.
func (r *genericRepo) checkChangedUserReferences(ctx context.Context, id model.ID, rec model.Record) error {
	if r.userRefs == nil {
		return nil
	}
	refs, err := countedUserReferences(r.desc, rec)
	if err != nil || len(refs) == 0 {
		return err
	}
	stored, err := r.get(ctx, id, false)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return r.userRefs(ctx, refs)
	case err != nil:
		return err
	}
	before, err := countedUserReferences(r.desc, stored)
	if err != nil {
		// A stored value the declarations cannot read vouches for nothing.
		return r.userRefs(ctx, refs)
	}
	had := make(map[model.ID]bool, len(before))
	for _, b := range before {
		had[b.id] = true
	}
	var added []userReference
	for _, n := range refs {
		if !had[n.id] {
			added = append(added, n)
		}
	}
	if len(added) == 0 {
		return nil
	}
	return r.userRefs(ctx, added)
}

// guardUserReferences refuses references that name an existing account whose
// authority this transaction has not pinned. A member-bound reference passes in
// a transaction that locked the tenant's directory fact.
func (sc *tenantScope) guardUserReferences(ctx context.Context, refs []userReference) error {
	strict := make(map[model.ID]bool, len(refs))
	var order []model.ID
	for _, ref := range refs {
		admitted := ref.memberBound && sc.directoryFactLocked
		if prior, seen := strict[ref.id]; seen {
			strict[ref.id] = prior || !admitted
			continue
		}
		strict[ref.id] = !admitted
		order = append(order, ref.id)
	}
	for _, id := range order {
		if !strict[id] {
			continue
		}
		if t := sc.directoryWriter; t != nil && t.heldUsers[id] == userAuthorityRowHeld {
			continue
		}
		exists, err := sc.accountExists(ctx, id)
		if err != nil {
			return err
		}
		if exists {
			return fmt.Errorf("%w: the row names an account whose authority this transaction did not pin",
				store.ErrUserAuthorityNotHeld)
		}
	}
	return nil
}

// accountExists reports whether id names an account: its authority row or its
// user row is stored. A value that is not an engine-allocated id names none.
func (sc *tenantScope) accountExists(ctx context.Context, id model.ID) (bool, error) {
	if validateCreateID(id) != nil {
		return false, nil
	}
	found := false
	err := sc.withUserAuthorityBinding(ctx, func() error {
		_, held, err := readUserAuthorityRow(ctx, sc.tx, sc.s.dia, id)
		if err != nil || held {
			found = held
			return err
		}
		query := sc.s.dia.Rebind("SELECT COUNT(*) FROM " + directoryWriterRelation(sc.s.dia, "users") +
			" WHERE id = ? AND tenant_id = ?")
		var n int
		if err := sc.tx.QueryRowContext(ctx, query, id.String(), model.SystemTenantID.String()).Scan(&n); err != nil {
			return directoryUnavailable("read account", err)
		}
		found = n > 0
		return nil
	})
	return found, err
}

// userReferenceSeam returns the seam for this scope's repositories: every
// business tenant's, never the SYSTEM tenant's, whose rows are the auth
// partition's own.
func (sc *tenantScope) userReferenceSeam() func(context.Context, []userReference) error {
	if sc.tenant.IsSystem() {
		return nil
	}
	return sc.guardUserReferences
}
