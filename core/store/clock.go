// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package store

import (
	"context"

	"github.com/olivaresai/olivares/core/model"
)

// TransactionClock is an OPTIONAL Scope capability for decisions whose
// correctness depends on the database's view of time. The value is read through
// the same transaction as the surrounding View or Mutate callback; callers must
// fail closed when the Scope does not implement this capability or the read
// fails. In particular, they must never substitute an application wall clock.
//
// This is deliberately separate from Scope. Adding it to Scope would break every
// store implementation and test fake, while a capability assertion lets older
// implementations remain source-compatible and makes their lack of an
// authoritative transactional clock explicit to the caller.
type TransactionClock interface {
	// TransactionNow returns the database engine's current time, normalized to
	// model.Timestamp, from inside the surrounding transaction.
	TransactionNow(ctx context.Context) (model.Timestamp, error)
}
