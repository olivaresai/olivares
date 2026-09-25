// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"context"
	"errors"
	"fmt"

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
// caller ingests first. Every ledger row of the hold becomes committed with
// actualMicroUSD, and the admission row that publishes the hold becomes committed. The
// rules, the first that matches deciding:
//
//   - no row under the hold: nothing is written;
//   - a row committed with another amount: ErrSettlementConflict, nothing is written;
//   - a row still active, withholding or lapsed: every row not committed is committed
//     with the amount, and an active one is settled now;
//   - every row committed with this amount: a repeat, nothing is written;
//   - no row active, some released or expired: a late commit — each is committed with the
//     amount and keeps the instant its withholding ended.
//
// An empty handle is a no-op. A handle that is not a hold identity, or a negative amount,
// is ErrInvalidAdmission. A hold whose admission is still pending is ErrAdmissionPending.
// A hold whose admission row is corrupt is ErrAdmissionIntegrity. Under an activation
// frontier, or when a row of the hold belongs to the attempt lifecycle, the typed
// lifecycle_api_required refusal is returned. None of these writes anything.
func (m *Module) Commit(ctx context.Context, tenant model.TenantID, handle string, actualMicroUSD int64) error {
	h, err := parseHoldID(handle)
	if err != nil || h.isZero() {
		return err
	}
	if actualMicroUSD < 0 {
		return fmt.Errorf("%w: the actual amount must not be negative", ErrInvalidAdmission)
	}
	return m.settle(ctx, tenant, h, resvStateCommitted, actualMicroUSD)
}

// Release returns the headroom of a hold whose effect did not run. It has no amount, so
// it changes only rows still active, withholding or lapsed: each becomes released with an
// actual of zero, settled now. A committed row is never released, and neither is a
// committed admission; a reserved admission that publishes the hold becomes released. An
// empty handle and the refusals are as for Commit.
func (m *Module) Release(ctx context.Context, tenant model.TenantID, handle string) error {
	h, err := parseHoldID(handle)
	if err != nil || h.isZero() {
		return err
	}
	return m.settle(ctx, tenant, h, resvStateReleased, 0)
}

// settle runs one settlement of h to the ledger state to — committed with actual, or
// released — in one transaction.
func (m *Module) settle(ctx context.Context, tenant model.TenantID, h holdID, to string, actual int64) error {
	if m.data == nil {
		return attemptErr(errCodeCapabilityUnavailable, nil)
	}
	// confirmed records that the callback established the tenant has no activation
	// frontier; until then a failure has classified nothing.
	confirmed := false
	err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		confirmed = false
		if err := lockFinOpsWriter(ctx, sc); err != nil {
			return storeErr(err)
		}
		if err := guardLegacyReservationMutation(ctx, sc); err != nil {
			return err
		}
		confirmed = true
		now := m.clock.Now()
		row, found, err := rowNamingHold(ctx, sc, h)
		if err != nil {
			return integrityFault(err)
		}
		holds := []holdID{h}
		if found {
			if row.state == admStatePending {
				return ErrAdmissionPending
			}
			if err := holdsOwnedBy(ctx, sc, row, row.handle, row.spendHandle); err != nil {
				return integrityFault(err)
			}
			holds = slotsOf(row)
		}
		rows, err := ledgerRowsOfHolds(ctx, sc, holds)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		for _, r := range rows {
			if link, _ := linkageOf(r); link != linkageLegacy {
				return attemptErr(errCodeLifecycleAPIRequired, nil)
			}
		}
		changed, err := settledRows(rows, to, actual, now)
		if err != nil {
			return err
		}
		repo, err := sc.Ext(budgetReservationKind)
		if err != nil {
			return err
		}
		for _, r := range changed {
			if _, err := repo.Update(ctx, r); err != nil {
				return err
			}
		}
		if !found {
			return nil
		}
		next, write := settledAdmission(row, to, now)
		if !write {
			return nil
		}
		_, err = updateAdmission(ctx, sc, next)
		return err
	})
	return classifyPreConfirmationFailure(err, confirmed)
}

// integrityFault reports a corrupt admission row as the settlement's integrity failure;
// any other error is returned as it is.
func integrityFault(err error) error {
	if errors.Is(err, errAdmissionRowCorrupt) {
		return fmt.Errorf("%w: %w", ErrAdmissionIntegrity, err)
	}
	return err
}

// slotsOf lists the holds row names in its slots: its hold, and for a pair an earlier
// build published, the other one too.
func slotsOf(row admissionRow) []holdID {
	var out []holdID
	for _, h := range []holdID{row.handle, row.spendHandle} {
		if !h.isZero() {
			out = append(out, h)
		}
	}
	return out
}

// ledgerRowsOfHolds reads every ledger row under each of holds, completely or not at all.
func ledgerRowsOfHolds(ctx context.Context, sc store.Scope, holds []holdID) ([]model.Record, error) {
	var out []model.Record
	for _, h := range holds {
		rows, err := rowsUnderHold(ctx, sc, h)
		if err != nil {
			return nil, err
		}
		out = append(out, rows...)
	}
	return out, nil
}

// settledRows decides, by the rules Commit and Release state, which of rows a settlement
// to the ledger state to writes, and with what; ErrSettlementConflict refuses the whole
// settlement. An active row, withholding or lapsed, is settled now with actual, which is
// zero for a release. A released or expired row is written only by a commit — a late
// commit: the effect ran all the same, and the row keeps the instant its withholding
// ended. A committed row is never written again.
func settledRows(rows []model.Record, to string, actual int64, now model.Timestamp) ([]model.Record, error) {
	if to == resvStateCommitted {
		for _, r := range rows {
			if r.String(colResvState) == resvStateCommitted && r.Int(colResvActual) != actual {
				return nil, ErrSettlementConflict
			}
		}
	}
	var out []model.Record
	for _, r := range rows {
		switch state := r.String(colResvState); {
		case state == resvStateActive:
			r[colResvSettledAt] = now.String()
		case to == resvStateCommitted && (state == resvStateReleased || state == resvStateExpired):
			// A late commit: settled_at stays as it is.
		default:
			continue
		}
		r[colResvState] = to
		r[colResvActual] = actual
		out = append(out, r)
	}
	return out, nil
}

// settledAdmission is the admission row after a settlement of a hold it names, and
// whether it is written. Only a row that publishes the hold is: Commit makes it
// committed, and Release makes a reserved row released. A row already in that state is
// not written again, so the instant it entered the state — where the replay window
// starts — stays. A committed row is never released.
func settledAdmission(row admissionRow, to string, now model.Timestamp) (admissionRow, bool) {
	if !publishesHold(row.state) {
		return row, false
	}
	switch {
	case to == resvStateCommitted && row.state != admStateCommitted:
		row.state = admStateCommitted
	case to == resvStateReleased && row.state == admStateReserved:
		row.state = admStateReleased
	default:
		return row, false
	}
	row.stateAt = now
	return row, true
}
