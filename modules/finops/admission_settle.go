// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"context"
	"errors"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Settlement. Commit and Release settle one hold in one transaction: the writer lock, the
// frontier guard, the admission row that names the hold, every ledger row under the hold
// — and, for a pair an earlier build published, under its other hold — and last the
// admission row, written only while it still names the hold. The money of a hold is
// settled however late the settlement arrives, because it records what the effect did;
// the key is written only by a settlement of a hold the key still names, so an old hold
// never overwrites the admission of a newer call.

// ErrSettlementConflict is a Commit of a hold that is already committed with another
// amount. Nothing is written: the first measured cost stands.
var ErrSettlementConflict = errors.New("finops: the hold is already committed with another amount")

// ErrAdmissionPending is a settlement of a hold whose admission is still a claim in
// flight: no caller was answered with that hold, so none can settle it. Nothing is
// written.
var ErrAdmissionPending = errors.New("finops: the hold's admission is still pending")

// ErrAdmissionIntegrity is a settlement whose hold leads to an admission row no writer
// stores: a slot that is not a hold identity, or a hold that more than one row names. The
// settlement cannot be identified, so nothing is written — no admission row and no ledger
// row. It is neither a success nor a conflict: the caller keeps the cost it ingested and
// retries the same call once the row is repaired. The error also matches the corruption
// it reports.
var ErrAdmissionIntegrity = errors.New("finops: the hold's admission row failed its integrity check")

// Commit records that the effect a hold admitted ran, at its measured cost, which the
// caller ingests first. Not implemented yet: its transaction writes nothing.
func (m *Module) Commit(ctx context.Context, tenant model.TenantID, handle string, actualMicroUSD int64) error {
	return m.data.Mutate(ctx, tenant, func(store.Scope) error { return nil })
}

// Release returns the headroom of a hold whose effect did not run. Not implemented yet:
// its transaction writes nothing.
func (m *Module) Release(ctx context.Context, tenant model.TenantID, handle string) error {
	return m.data.Mutate(ctx, tenant, func(store.Scope) error { return nil })
}
