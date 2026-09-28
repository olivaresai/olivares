// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Recovery. A caller that stops between its claim and its publication, or whose give-back
// fails, leaves a pending row that names its hold and every hold it owed, and no other
// caller settles that money unless it retries the same key. The recovery pass does. It is
// a pass, not a loop: whoever schedules it runs it per tenant, and it schedules nothing.
// One pass reads the tenant's pending and owes_release rows through the indexed state and
// makes at most one decision per row; each write is its own transaction under the writer
// lock, fenced at the version the pass read.
//
//   - A claim of this build younger than the takeover bound is a caller in flight. It is
//     never written, whatever it owes.
//   - A stale claim is retired: every hold it owes and its own are settled — withholding
//     rows released with an actual of zero, lapsed rows left to the sweep — and the row is
//     released naming nothing. Under an activation frontier no ledger row is written: a
//     hold the attempt lifecycle owns is dropped from the list, and any other stays owed.
//   - A dated claim of this build whose owed list does not decode is cleared five minutes
//     after its date, and then retired; any other row whose list does not decode is only
//     reported.
//   - A row an earlier build left is settled by settleLegacyRow, under the operator's stop.
//
// A row that fails its integrity check is counted and never written, and the pass goes on.
// The pass never writes an actual amount, never changes a TTL, and never takes the age of
// a claim or a silent key for a sign that the earlier writers stopped.

// owedUndecodableWait is how long after a claim's date its owed list, when it does not
// decode, is cleared. Only a takeover adds a hold to a list, and it dates the row, so every
// hold such a list could name was taken before that date and has lapsed by then.
const owedUndecodableWait = 5 * time.Minute

// errClaimInFlight says a claim a pass read as stale is young at the instant of the write
// that would change it: its caller is still in flight. Nothing is written.
var errClaimInFlight = errors.New("finops: the claim is still in flight")

// AdmissionRecovery is what one recovery pass over a tenant's admission rows did and what
// it left. It carries counts and the stop's state only: no identity, no stored value.
type AdmissionRecovery struct {
	// PendingRetired counts the stale claims of this build the pass retired.
	PendingRetired int `json:"pending_retired"`
	// OwedReleased counts the ledger rows the pass released, each with an actual of zero.
	OwedReleased int `json:"owed_released"`
	// OwedCleared counts the owed holds the pass dropped without a release, because an
	// activation frontier gave their money to the attempt lifecycle.
	OwedCleared int `json:"owed_cleared"`
	// OwedRemaining counts the holds still owed by claims in flight, which their own create
	// settles, or a later pass once the claim is stale; in a reconciliation, also the holds
	// a published row still lists while a row under them is active, which no pass reads.
	OwedRemaining int `json:"owed_remaining"`
	// LegacyPending counts the claims an earlier build staged that the pass left, because
	// the operator's stop is not usable or the row moved.
	LegacyPending int `json:"legacy_pending"`
	// LegacyRetired counts the rows an earlier build left that the pass settled: legacy
	// claims retired under a usable stop, and owes_release rows.
	LegacyRetired int `json:"legacy_retired"`
	// LegacyOwesRelease counts the owes_release rows the pass left.
	LegacyOwesRelease int `json:"legacy_owes_release"`
	// LegacyStop is the state of the operator's stop of the earlier writers at the pass.
	LegacyStop string `json:"legacy_stop"`
	// Unresolved counts the writes that rolled back, or whose outcome could not be
	// established; the next pass decides again from the row.
	Unresolved int `json:"unresolved"`
	// Undecodable counts the rows whose owed list does not decode that the pass left.
	Undecodable int `json:"undecodable"`
	// UndecodableCleared counts the claims whose undecodable owed list the pass cleared.
	UndecodableCleared int `json:"undecodable_cleared"`
	// FrontierBlocked counts the holds the pass could neither settle nor drop: the attempt
	// lifecycle refuses the release, and does not own their money.
	FrontierBlocked int `json:"frontier_blocked"`
	// Corrupt counts the distinct admission rows found failing their integrity check. Such
	// a row enters no other counter and is never written.
	Corrupt int `json:"corrupt"`
}

// Outstanding is what the pass left for a later pass or an operator: owed holds, legacy
// claims and owes_release rows, unresolved writes, undecodable lists, blocked holds and
// corrupt rows.
func (r AdmissionRecovery) Outstanding() int {
	return r.OwedRemaining + r.LegacyPending + r.LegacyOwesRelease + r.Unresolved +
		r.Undecodable + r.FrontierBlocked + r.Corrupt
}

// Quiet reports that the pass did nothing and left nothing outstanding. The stop's state
// is not work: with no legacy row left, a pass is quiet under any stop.
func (r AdmissionRecovery) Quiet() bool {
	return r.PendingRetired == 0 && r.OwedReleased == 0 && r.OwedCleared == 0 &&
		r.LegacyRetired == 0 && r.UndecodableCleared == 0 && r.Outstanding() == 0
}

// corruptRows is the set of admission rows found failing their integrity check, by id,
// each with the column its fault was found in.
type corruptRows map[model.ID]string

// add records id with column, keeping the column it was first recorded with.
func (c corruptRows) add(id model.ID, column string) {
	if _, seen := c[id]; !seen {
		c[id] = column
	}
}

// RecoverAdmissions runs one recovery pass over the tenant's admission rows and reports
// what it did and left. It fails only when it cannot read the rows completely; a write
// that fails is counted and the pass goes on.
func (m *Module) RecoverAdmissions(ctx context.Context, tenant model.TenantID) (AdmissionRecovery, error) {
	rep, _, err := m.recoverAdmissions(ctx, tenant)
	return rep, err
}

// recoverAdmissions is the pass, returning with the report the corrupt rows it found.
func (m *Module) recoverAdmissions(ctx context.Context, tenant model.TenantID) (AdmissionRecovery, corruptRows, error) {
	corrupt := corruptRows{}
	if m.data == nil {
		return AdmissionRecovery{}, corrupt, attemptErr(errCodeCapabilityUnavailable, nil)
	}
	now := m.clock.Now()
	var claims, owes []model.Record
	err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		var err error
		if claims, err = admissionRowsIn(ctx, sc, admStatePending); err != nil {
			return err
		}
		owes, err = admissionRowsIn(ctx, sc, admStateOwesRelease)
		return err
	})
	if err != nil {
		return AdmissionRecovery{}, corrupt, err
	}
	// The rows just read are every legacy claim and owes_release row of the tenant, so they
	// also date the earlier writers the operator's stop speaks of.
	stop := legacyStopStateFor(m.legacyStop, now, newestLegacyDate(claims, owes))
	rep := AdmissionRecovery{LegacyStop: string(stop)}
	for _, rec := range claims {
		m.recoverClaim(ctx, tenant, rec, stop, now, &rep, corrupt)
	}
	for _, rec := range owes {
		row, err := admissionRowFrom(rec)
		switch {
		case err != nil:
			corrupt.add(model.ID(rec.String(model.ColID)), corruptColumnOf(rec))
		case row.owedErr != nil:
			rep.Undecodable++
		default:
			m.recoverLegacyRow(ctx, tenant, row, stop, &rep, corrupt)
		}
	}
	rep.Corrupt = len(corrupt)
	return rep, corrupt, nil
}

// admissionRowsIn reads, in the caller's transaction, every admission row in state through
// the indexed state column, completely or not at all.
func admissionRowsIn(ctx context.Context, sc store.Scope, state string) ([]model.Record, error) {
	repo, err := sc.Ext(admissionIdempotencyKind)
	if err != nil {
		return nil, err
	}
	recs, incomplete, err := scanReservations(ctx, repo, []model.Filter{eq(colAdmState, state)})
	if err != nil {
		return nil, err
	}
	if incomplete != "" {
		return nil, fmt.Errorf("%w: %s (%s admission rows)", errReservationScanIncomplete, incomplete, state)
	}
	return recs, nil
}

// corruptColumnOf names the slot of a stored row that is not a hold identity.
func corruptColumnOf(rec model.Record) string {
	if _, err := parseHoldID(rec.String(colAdmHandle)); err != nil {
		return colAdmHandle
	}
	return colAdmSpendHandle
}

// recoverClaim makes the pass's decision about one pending row and counts it.
func (m *Module) recoverClaim(ctx context.Context, tenant model.TenantID, rec model.Record, stop legacyStopState, now model.Timestamp, rep *AdmissionRecovery, corrupt corruptRows) {
	row, err := admissionRowFrom(rec)
	if err != nil {
		corrupt.add(model.ID(rec.String(model.ColID)), corruptColumnOf(rec))
		return
	}
	if row.owedErr != nil {
		if !undecodableClearable(row, now) {
			rep.Undecodable++
			return
		}
		switch m.clearUndecodable(ctx, tenant, row) {
		case recoveryDone:
			rep.UndecodableCleared++
			row.version++
			row.owed, row.owedRaw, row.owedErr = nil, nil, nil
		case recoveryNone:
			rep.Undecodable++
			return
		case recoveryCorrupt:
			m.noteCorrupt(ctx, tenant, row, nil, corrupt)
			return
		default:
			rep.Unresolved++
			return
		}
	}
	if row.handle.isZero() {
		m.recoverLegacyRow(ctx, tenant, row, stop, rep, corrupt)
		return
	}
	if !m.staleClaim(row) {
		rep.OwedRemaining += len(row.owed)
		return
	}
	out := m.retireClaim(ctx, tenant, row)
	switch out.step {
	case recoveryDone:
		if out.retired {
			rep.PendingRetired++
		}
		rep.OwedReleased += out.released
		rep.OwedCleared += out.dropped
		rep.FrontierBlocked += out.blocked
	case recoveryNone:
		rep.OwedRemaining += len(row.owed)
	case recoveryBlocked:
		if out.blocked == 0 {
			out.blocked = len(claimHolds(row))
		}
		rep.FrontierBlocked += out.blocked
	case recoveryCorrupt:
		m.noteCorrupt(ctx, tenant, row, claimHolds(row), corrupt)
	default:
		rep.Unresolved++
	}
}

// recoverLegacyRow settles one row an earlier build left, as far as the stop allows, and
// counts it: a legacy claim left is legacy_pending, an owes_release row left is
// legacy_owes_release.
func (m *Module) recoverLegacyRow(ctx context.Context, tenant model.TenantID, row admissionRow, stop legacyStopState, rep *AdmissionRecovery, corrupt corruptRows) {
	left := &rep.LegacyPending
	if row.state == admStateOwesRelease {
		left = &rep.LegacyOwesRelease
	}
	out := m.settleLegacyRow(ctx, tenant, row, stop)
	switch out.step {
	case recoveryDone:
		rep.LegacyRetired++
		rep.OwedReleased += out.released
		rep.OwedCleared += out.dropped
	case recoveryNone:
		*left++
	case recoveryBlocked:
		if out.blocked == 0 {
			out.blocked = len(legacyHolds(row))
		}
		rep.FrontierBlocked += out.blocked
	case recoveryCorrupt:
		m.noteCorrupt(ctx, tenant, row, legacyHolds(row), corrupt)
	default:
		rep.Unresolved++
	}
}

// claimHolds lists every hold a claim of this build names: those it owes and its own.
func claimHolds(row admissionRow) owedHolds {
	holds := append(owedHolds(nil), row.owed...)
	if !row.handle.isZero() && !holds.has(row.handle) {
		holds = append(holds, row.handle)
	}
	return holds
}

// undecodableClearable reports whether a claim's undecodable owed list may be cleared at
// now: the claim is one this build wrote, naming its own hold, and dated, and now is five
// minutes past that date. Only this build's takeover adds a hold to a list, and it dates
// the row, so only then has every hold the list could name lapsed. A claim an earlier
// build staged, or an undated one, is never cleared.
func undecodableClearable(row admissionRow, now model.Timestamp) bool {
	if row.handle.isZero() || row.stateAt.IsZero() {
		return false
	}
	return !now.Time().Before(row.stateAt.Time().Add(owedUndecodableWait))
}

// claimStepOf classifies a write to a claim that did not commit.
func claimStepOf(err error) recoveryStep {
	if errors.Is(err, errClaimInFlight) {
		return recoveryNone
	}
	return recoveryStepOf(err)
}

// clearUndecodable writes NULL over the owed list of a claim that does not decode, in one
// transaction: the writer lock; the row read again, which must still be the claim read at
// its version with its list still undecodable, and past the wait at this transaction's
// instant. Nothing else of the claim changes.
func (m *Module) clearUndecodable(ctx context.Context, tenant model.TenantID, read admissionRow) recoveryStep {
	w, err := m.mutateClassified(ctx, tenant, func(sc store.Scope) error {
		if err := lockFinOpsWriter(ctx, sc); err != nil {
			return err
		}
		row, found, err := rowOfKey(ctx, sc, read.key)
		if err != nil {
			return err
		}
		if !found || row.version != read.version || row.state != admStatePending || row.handle != read.handle || row.owedErr == nil {
			return errKeyMoved
		}
		if !undecodableClearable(row, m.clock.Now()) {
			return errClaimInFlight
		}
		row.owed, row.owedRaw, row.owedErr = nil, nil, nil
		_, err = updateAdmission(ctx, sc, row)
		return err
	})
	switch w {
	case writeCommitted:
		return recoveryDone
	case writeUncertain:
		row, found, err := m.readRow(ctx, tenant, read.key)
		if err != nil || !found || row.version != read.version+1 || row.state != admStatePending ||
			row.handle != read.handle || row.owedErr != nil || len(row.owed) != 0 {
			return recoveryUnresolved
		}
		return recoveryDone
	}
	return claimStepOf(err)
}

// claimRecovery is what the retirement of one stale claim did.
type claimRecovery struct {
	step recoveryStep
	// retired says the claim was released naming nothing; owed is the list a write that
	// did not retire it left.
	retired bool
	owed    owedHolds
	// released counts the ledger rows it released; dropped the owed holds it dropped
	// because the attempt lifecycle owns them; blocked the holds it could neither settle
	// nor drop.
	released, dropped, blocked int
}

// leaves reports whether row is what the write c describes left of the claim read.
func (c claimRecovery) leaves(row, read admissionRow) bool {
	if row.owedErr != nil {
		return false
	}
	if c.retired {
		return row.state == admStateReleased && row.handle.isZero() && len(row.owed) == 0
	}
	if row.state != admStatePending || row.handle != read.handle || len(row.owed) != len(c.owed) {
		return false
	}
	for _, h := range c.owed {
		if !row.owed.has(h) {
			return false
		}
	}
	return true
}

// retireClaim retires a stale claim of this build in one transaction: the writer lock;
// the tenant's activation frontier; the row read again, which must still be the claim
// read at its version and stale at this transaction's instant; no other row naming one of
// its holds. With no activation frontier, every hold it owes and its own are settled —
// withholding rows released with an actual of zero and settled now, lapsed rows left to
// the sweep — and the row is written released, naming nothing and owing nothing. Under a
// frontier no ledger row is written: each hold the attempt lifecycle owns is dropped; when
// all are, the row is retired, and otherwise it keeps owing the others and waits.
func (m *Module) retireClaim(ctx context.Context, tenant model.TenantID, read admissionRow) claimRecovery {
	var (
		out claimRecovery
		at  model.Timestamp
	)
	w, err := m.mutateClassified(ctx, tenant, func(sc store.Scope) error {
		out = claimRecovery{}
		if err := lockFinOpsWriter(ctx, sc); err != nil {
			return err
		}
		census, frontier, err := lifecycleCensus(ctx, sc)
		if err != nil {
			return err
		}
		row, found, err := rowOfKey(ctx, sc, read.key)
		if err != nil {
			return err
		}
		if !found || row.version != read.version || row.state != admStatePending || row.handle != read.handle || row.owedErr != nil {
			return errKeyMoved
		}
		if !m.staleClaim(row) {
			return errClaimInFlight
		}
		at = m.clock.Now()
		holds := claimHolds(row)
		if err := holdsOwnedBy(ctx, sc, row, holds...); err != nil {
			return err
		}
		if frontier {
			err = dropOwnedHolds(ctx, sc, census, row, holds, &out)
		} else {
			out.released, err = settleOwedInScope(ctx, sc, holds, at)
			out.retired = err == nil
		}
		if err != nil {
			return err
		}
		if out.retired {
			row.state = admStateReleased
			row.handle = ""
			row.owed = nil
			row.stateAt = at
		} else {
			row.owed = out.owed
		}
		_, err = updateAdmission(ctx, sc, row)
		return err
	})
	switch w {
	case writeCommitted:
		out.step = recoveryDone
		return out
	case writeUncertain:
		return m.retiredByIdentity(ctx, tenant, read, out, at)
	}
	out.step = claimStepOf(err)
	return out
}

// dropOwnedHolds decides, under an activation frontier, what a stale claim may stop owing
// without a ledger write: each hold the attempt lifecycle owns. The claim is retired when
// the lifecycle owns them all, its own included; otherwise the others stay on its list,
// and when there is nothing to drop the write is refused as the lifecycle refuses it.
func dropOwnedHolds(ctx context.Context, sc store.Scope, census map[string]bool, row admissionRow, holds owedHolds, out *claimRecovery) error {
	intentOwned := true
	for _, h := range holds {
		owned, err := lifecycleOwns(ctx, sc, census, h)
		if err != nil {
			return err
		}
		switch {
		case owned && h != row.handle:
			out.dropped++
		case !owned && h == row.handle:
			intentOwned = false
			out.blocked++
		case !owned:
			out.owed = append(out.owed, h)
			out.blocked++
		}
	}
	if intentOwned && len(out.owed) == 0 {
		out.retired = true
		return nil
	}
	if out.dropped == 0 {
		return attemptErr(errCodeLifecycleAPIRequired, nil)
	}
	return nil
}

// lifecycleCensus reads, in the caller's transaction, whether the tenant has an activation
// frontier and, if it has, the holds whose groups its census holds pending. A frontier that
// cannot be read or does not validate is an error: an unread frontier is not an absent one.
func lifecycleCensus(ctx context.Context, sc store.Scope) (map[string]bool, bool, error) {
	if _, found, err := readLifecycleScope(ctx, sc); err != nil || !found {
		return nil, false, err
	}
	rec, found, err := lifecycleScopeRow(ctx, sc)
	if err != nil {
		return nil, false, err
	}
	if !found {
		return nil, false, attemptErr(errCodeLedgerIndeterminate, nil)
	}
	doc, err := frontierDocumentOf(rec)
	if err != nil {
		return nil, false, err
	}
	named := make(map[string]bool, len(doc.PendingGroups))
	for _, g := range doc.PendingGroups {
		named[g.Handle] = true
	}
	return named, true, nil
}

// lifecycleOwns reports, under an activation frontier, whether the attempt lifecycle owns
// the money of h: the frontier's census holds h's group, or every row under h — none
// included — is settled or linked to an attempt. Such a hold may be dropped from a list
// without a ledger write.
func lifecycleOwns(ctx context.Context, sc store.Scope, census map[string]bool, h holdID) (bool, error) {
	if census[h.String()] {
		return true, nil
	}
	rows, err := rowsUnderHold(ctx, sc, h)
	if err != nil {
		return false, err
	}
	for _, r := range rows {
		state := r.String(colResvState)
		if link, _ := linkageOf(r); link != linkageV1 && state != resvStateCommitted && state != resvStateReleased {
			return false, nil
		}
	}
	return true, nil
}

// retiredByIdentity resolves a retirement whose outcome is unknown, in a read transaction
// of its own. The row one version on, as the write left it, is that write: done, with the
// rows under the claim's holds it released at its instant. Anything else — the claim as it
// was read, another write, a read that fails — is unresolved, and the next pass decides
// from the row as it then stands.
func (m *Module) retiredByIdentity(ctx context.Context, tenant model.TenantID, read admissionRow, wrote claimRecovery, at model.Timestamp) claimRecovery {
	out := claimRecovery{step: recoveryUnresolved}
	err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		out = claimRecovery{step: recoveryUnresolved}
		row, found, err := rowOfKey(ctx, sc, read.key)
		if err != nil {
			return err
		}
		if !found || row.version != read.version+1 || !wrote.leaves(row, read) {
			return nil
		}
		rows, err := ledgerRowsOfHolds(ctx, sc, claimHolds(read))
		if err != nil {
			return err
		}
		out = wrote
		out.step, out.released = recoveryDone, 0
		for _, r := range rows {
			if r.String(colResvState) == resvStateReleased && r.String(colResvSettledAt) == at.String() {
				out.released++
			}
		}
		return nil
	})
	if err != nil {
		return claimRecovery{step: recoveryUnresolved}
	}
	return out
}

// noteCorrupt adds to corrupt, in a read of its own, the rows behind an integrity fault
// found on row: row itself; every row its key's lookup returns, when that is more than
// one; and every row naming one of holds in its handle slot, when another row does too. A
// lookup whose page holds more adds the rows it read. A read that fails adds row alone.
func (m *Module) noteCorrupt(ctx context.Context, tenant model.TenantID, row admissionRow, holds owedHolds, corrupt corruptRows) {
	_ = m.data.View(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(admissionIdempotencyKind)
		if err != nil {
			return err
		}
		lookups := []model.Filter{eq(colAdmKey, row.key)}
		for _, h := range holds {
			lookups = append(lookups, eq(colAdmHandle, h.String()))
		}
		for _, f := range lookups {
			recs, page, err := repo.List(ctx, model.Query{Filters: []model.Filter{f}, Limit: admissionLookupPage})
			if err != nil {
				return err
			}
			if len(recs) < 2 && !page.HasMore {
				continue
			}
			for _, rec := range recs {
				corrupt.add(model.ID(rec.String(model.ColID)), f.Column)
			}
		}
		return nil
	})
	corrupt.add(row.id, colAdmHandle)
}
