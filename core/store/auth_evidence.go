// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package store

import (
	"context"

	"github.com/olivaresai/olivares/core/model"
)

// AuthPrincipalEvidenceScope is an OPTIONAL capability of the AuthScope value
// passed to Store.AuthView. It exposes only the database time and the exact
// tenant-local directory generation needed to reconstruct an authenticated
// principal with durable provenance inside that one read transaction.
//
// ReadDirectoryEpochFact temporarily binds the requested business tenant for
// the epoch read and restores the auth partition before returning. Callers must
// fail closed when the capability is absent or either observation fails; they
// must not open a second Store transaction or substitute an application clock.
// Keeping this separate from AuthScope preserves source compatibility for store
// decorators and test fakes that do not yet provide principal evidence.
type AuthPrincipalEvidenceScope interface {
	TransactionClock
	ReadDirectoryEpochFact(context.Context, model.TenantID) (AuthorizationFactRef, error)
}
