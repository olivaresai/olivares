// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package store

import "context"

// TransactionLocker is an OPTIONAL Scope capability for decisions that must be
// serialized across every process using the same database. The lock belongs to
// the surrounding View or Mutate transaction and is released by its commit or
// rollback; implementations must not use a session-scoped lock that can outlive
// the unit of work.
//
// This remains separate from Scope so stores and test fakes that cannot provide
// cluster-wide serialization stay source-compatible. Correctness-sensitive
// callers must fail closed when the capability is absent or LockTransaction
// fails.
type TransactionLocker interface {
	// LockTransaction serializes this transaction with every other transaction
	// using the same key. Implementations may treat it as a no-op when their
	// engine already serializes all writers, but a read-only scope must return
	// ErrReadOnly rather than claim to have acquired write authority.
	LockTransaction(ctx context.Context, key string) error
}
