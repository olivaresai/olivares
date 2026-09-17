// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package store

import (
	"context"

	"github.com/olivaresai/olivares/core/model"
)

// PolicyState is a policy row's enable/disable state and the identity needed to
// act on it. It carries NO Spec field, and that absence is the whole point: a
// type that cannot hold the stored document cannot re-encode it, so no future
// edit of a state path can silently rewrite a policy's exact bytes.
type PolicyState struct {
	ID        model.ID
	Kind      string
	Enabled   bool
	Version   int64
	UpdatedAt model.Timestamp
}

// PolicyStateWriter is the OPTIONAL state-only capability of a policy
// repository. Neither method reads, decodes, normalizes or writes Spec.
//
// It exists because there is no partial write in the ordinary path: the typed
// Update encodes the whole entity and the generic update sets every declared
// field, so changing only Enabled through them rewrites the stored document
// from whatever the decoder produced. These methods issue their own statements
// over a fixed column set instead.
//
// Keeping it separate from Repository follows RowLocker: not every store or
// test double can offer it, so a caller that needs it must assert for it and
// FAIL CLOSED when it is absent. There is no fallback to the generic update,
// and a caller must never treat a successful assertion as authorization —
// holding this capability grants no permission and no transactional authority.
type PolicyStateWriter interface {
	// GetPolicyState returns the state projection of one row in the pinned
	// tenant. It is valid in a View. ErrNotFound covers absent and foreign rows.
	GetPolicyState(ctx context.Context, id model.ID) (PolicyState, error)
	// SetPolicyEnabled changes only enabled, version and updated_at, under an
	// exact tenant, id, kind and expected-version predicate. It requires a
	// Mutate scope. A version mismatch on a row of the expected kind is
	// ErrConflict; an absent row, a foreign row and a row of another kind are
	// all ErrNotFound.
	//
	// PRECONDITION: the caller must have observed this transaction's engine time
	// through TransactionClock.TransactionNow on the SAME scope. The stored
	// updated_at is that observation, never the injected application clock, so a
	// missing or failed observation returns ErrTransactionTimeNotObserved BEFORE
	// any write. There is no fallback and no second transaction. GetPolicyState
	// requires no observation.
	SetPolicyEnabled(
		ctx context.Context,
		id model.ID,
		expectedKind string,
		expectedVersion int64,
		enabled bool,
	) (PolicyState, error)
}
