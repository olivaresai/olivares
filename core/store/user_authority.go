// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package store

import (
	"context"
	"github.com/olivaresai/olivares/core/model"
)

// AuthUserAuthorityWriter predeclares the complete, finite User lock set of a
// compound AuthMutate callback before it writes memberships, tokens or groups.
// It does not create Users/H, bump versions or grant authority. Repeating a
// covered subset is idempotent; a newly required H after G poisons the transaction.
// Only staged membership-union-v1 may reserve an observed absence under the
// transaction-held global lock. That reservation is not an authority fact; only
// an actual authority writer may create H for the existing legacy User. Target
// protocol requires an existing H and never repairs its absence.
// This optional capability does not widen AuthScope or expose any repository.
type AuthUserAuthorityWriter interface {
	PrepareUserAuthorityWrite(context.Context, []model.ID) error
}
