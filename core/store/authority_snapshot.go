// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package store

import (
	"context"
	"errors"

	"github.com/olivaresai/olivares/core/model"
)

var errMalformedAuthorizationLeaseFence = errors.New(
	"store: malformed authorization lease/fence witness",
)

// AuthorizationLeaseFenceWitness is the minimal server-observed semantic
// witness needed to pin a leased fact. Its fields stay private so it cannot be
// populated by decoding an external payload or serialized as part of an
// AuthorizationFactRef. Callers construct one only through
// NewLeaseFenceAuthorizationFactRef.
type AuthorizationLeaseFenceWitness struct {
	subject  string
	fence    int64
	deadline model.Timestamp
}

// AuthorizationFactRef is an opaque version witness for one server-selected
// authorization fact. It carries no exported or serializable row fields: a
// workspace-confined caller may ask the store to keep a previously observed
// decision stable, but cannot use this capability to read tenant-wide data.
type AuthorizationFactRef struct {
	Kind    model.Kind
	ID      model.ID
	Version int64

	leaseFence AuthorizationLeaseFenceWitness
}

// NewLeaseFenceAuthorizationFactRef constructs an opaque leased authority
// reference from a row already observed by trusted server code. The store still
// revalidates every value after taking the row lock; this constructor is shape
// validation, not an authorization decision.
func NewLeaseFenceAuthorizationFactRef(
	kind model.Kind,
	id model.ID,
	version int64,
	subject string,
	fence int64,
	deadline model.Timestamp,
) (AuthorizationFactRef, error) {
	if !kind.Valid() || id.IsZero() || version < 1 || subject == "" || len(subject) > 1024 ||
		fence < 1 || deadline.IsZero() {
		return AuthorizationFactRef{}, errMalformedAuthorizationLeaseFence
	}
	if _, err := model.ParseTimestamp(deadline.String()); err != nil {
		return AuthorizationFactRef{}, errMalformedAuthorizationLeaseFence
	}
	return AuthorizationFactRef{
		Kind: kind, ID: id, Version: version,
		leaseFence: AuthorizationLeaseFenceWitness{
			subject: subject, fence: fence, deadline: deadline,
		},
	}, nil
}

// LeaseFenceWitness returns the semantic witness, when this ref was created as
// a leased fact. It never reads or returns a store row.
func (r AuthorizationFactRef) LeaseFenceWitness() (
	subject string,
	fence int64,
	deadline model.Timestamp,
	ok bool,
) {
	w := r.leaseFence
	if w.subject == "" || w.fence < 1 || w.deadline.IsZero() {
		return "", 0, model.Timestamp{}, false
	}
	return w.subject, w.fence, w.deadline, true
}

// AuthoritySnapshotLocker is an OPTIONAL Mutate-scope capability. It locks
// every referenced row on the surrounding transaction and succeeds only when
// all versions still match. A descriptor may additionally opt into an exact
// lease/fence validation and transaction-stamped OCC touch. Implementations
// must accept only entity descriptors explicitly marked as authorization facts,
// must return no row payload, and must fail closed when any reference is
// malformed, missing, stale or not allowlisted. A View scope returns ErrReadOnly.
//
// Workspace confinement preserves this capability because it reveals no row;
// the repositories themselves remain confined. This is the narrow bridge for
// atomically pinning tenant-wide authority facts to a workspace-local write.
type AuthoritySnapshotLocker interface {
	LockAuthoritySnapshot(context.Context, []AuthorizationFactRef) error
}
