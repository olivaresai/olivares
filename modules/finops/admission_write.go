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

// The writers of an admission. Each is one transaction that takes the writer lock,
// reads the admission row once, touches ledger rows only after that read, and writes the
// row with exactly one insert or update — so each raises the row's version by exactly
// one, and a caller that lost the acknowledgment of a write can tell from the row alone
// whether it committed.

// writeOutcome says what is known about a write transaction once it has returned.
type writeOutcome int

const (
	// writeCommitted: the transaction committed.
	writeCommitted writeOutcome = iota
	// writeRolledBack: the transaction failed before its callback finished, so nothing
	// it would have written was written.
	writeRolledBack
	// writeUncertain: the callback finished and the transaction still failed, so what it
	// wrote may or may not have committed; only a read of the row can say which.
	writeUncertain
)

// errKeyMoved says the admission row is no longer this claimant's: its read found
// another version, state or hold, or its write lost to a newer one. The claimant writes
// nothing more and reads the row again.
var errKeyMoved = errors.New("finops: the admission key moved to another claim")

// errLegacyPairBusy refuses to take over a pair an earlier build published while it holds
// its key back: one of its holds still withholds inside the replay window.
var errLegacyPairBusy = errors.New("finops: a hold of the published admission still withholds")

// mutateClassified runs fn in one write transaction and classifies how it ended. The
// callback's first act marks it unfinished and its last marks it finished, so a store
// adapter that runs the callback again starts each run clean: an error that arrives
// while the callback is unfinished is a known rollback, and one that arrives after it
// finished is uncertain. That over-approximates — a fault before the commit that follows
// a finished callback also reads as uncertain — and a read of the row then finds the
// state before the write, which the caller treats as the rollback it was.
func (m *Module) mutateClassified(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) (writeOutcome, error) {
	done := false
	err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		done = false
		if err := fn(sc); err != nil {
			return err
		}
		done = true
		return nil
	})
	switch {
	case err == nil:
		return writeCommitted, nil
	case done:
		return writeUncertain, err
	}
	return writeRolledBack, err
}

// keyStep is what a pass over the key does after one of its writes returned.
type keyStep int

const (
	// stepOn: the write took effect; go on.
	stepOn keyStep = iota
	// stepAgain: the key moved; write nothing more and read the row again.
	stepAgain
	// stepRefuse: what the key holds cannot be established; write nothing more and
	// refuse deny-closed.
	stepRefuse
	// stepAbandon: the write failed and changed nothing; give the claim back, then refuse.
	stepAbandon
	// stepDenied: the evaluation refused the request; give the claim back and answer.
	stepDenied
)

// acquireKey claims a key nobody holds, or takes over the row view read, under the new
// hold identity h.
func (m *Module) acquireKey(ctx context.Context, tenant model.TenantID, req AdmissionRequest, hash string, view admissionView, h holdID) (admissionToken, keyStep, error) {
	if !view.found {
		stored, w, err := m.claim(ctx, tenant, admissionRow{
			key: req.IdempotencyKey, payloadHash: hash, scope: req.Scope,
			estimate: req.EstimateMicroUSD, state: admStatePending, stateAt: m.clock.Now(), handle: h,
		})
		return m.claimed(ctx, tenant, req.IdempotencyKey, h, stored, w, err)
	}
	owed, err := view.row.owed.with(view.row.handle, view.row.spendHandle)
	if err != nil {
		return admissionToken{}, stepRefuse, err
	}
	next := view.row
	next.owed = owed
	next.handle = h
	next.spendHandle = ""
	next.state = admStatePending
	next.stateAt = m.clock.Now()
	stored, w, err := m.takeOver(ctx, tenant, view.row, next)
	return m.claimed(ctx, tenant, req.IdempotencyKey, h, stored, w, err)
}

// claimed resolves a claim or a takeover. A lost acknowledgment is resolved by what the
// row now says: a pending row naming h is this caller's claim, whatever the write
// reported; anything else means the key is read again.
func (m *Module) claimed(ctx context.Context, tenant model.TenantID, key string, h holdID, stored admissionRow, w writeOutcome, err error) (admissionToken, keyStep, error) {
	switch {
	case w == writeCommitted:
		return stored.token(), stepOn, nil
	case w == writeRolledBack && (errors.Is(err, errReservationScanIncomplete) || errors.Is(err, errAdmissionRowCorrupt)):
		// The takeover could not read the holds it would move completely, or found one of
		// them named by another row: refuse, never guess.
		return admissionToken{}, stepRefuse, err
	case w == writeRolledBack:
		return admissionToken{}, stepAgain, nil
	}
	row, found, rerr := m.readRow(ctx, tenant, key)
	switch {
	case rerr != nil:
		return admissionToken{}, stepRefuse, rerr
	case found && row.state == admStatePending && row.handle == h:
		return row.token(), stepOn, nil
	}
	return admissionToken{}, stepAgain, nil
}

// createOutcome is what a create decided.
type createOutcome struct {
	// tok is the claim as the create left it.
	tok admissionToken
	// issued says at least one ledger row was inserted under the hold.
	issued bool
	// denied says the request was refused; nothing the create wrote survives.
	denied bool
	// verdict is the refusal, when denied.
	verdict BudgetReservation
	// spendLimit says the refusal came from a spend limit, not a budget.
	spendLimit bool
}

// createUnderClaim runs the create under the claim tok, h: again at the same claim after a
// lost seq race or another failure that changed nothing, and resolved by the row after a
// lost acknowledgment. A moved key is never created again.
func (m *Module) createUnderClaim(ctx context.Context, tenant model.TenantID, tok admissionToken, h holdID, budgets, seats []reservationTarget, estimate int64, now time.Time) (createOutcome, keyStep, error) {
	var lastErr error
	for try := 0; try < maxReserveRetries; try++ {
		out, w, err := m.create(ctx, tenant, tok, h, budgets, seats, estimate, now)
		switch {
		case w == writeCommitted:
			return out, stepOn, nil
		case w == writeUncertain:
			row, found, issued, rerr := m.readClaimAndHold(ctx, tenant, tok.key, h)
			switch {
			case rerr != nil:
				return createOutcome{}, stepRefuse, rerr
			case found && row.state == admStatePending && row.handle == h && row.version == tok.version+1:
				return createOutcome{tok: row.token(), issued: issued}, stepOn, nil
			case found && row.state == admStatePending && row.handle == h && row.version == tok.version:
				lastErr = err
				continue
			}
			return createOutcome{}, stepRefuse, err
		case errors.Is(err, errKeyMoved):
			return createOutcome{}, stepAgain, nil
		case errors.Is(err, errAdmissionRowCorrupt):
			return createOutcome{}, stepRefuse, err
		case out.denied:
			return out, stepDenied, err
		}
		lastErr = err
	}
	return createOutcome{}, stepAbandon, lastErr
}

// publishUnderClaim publishes the hold, retrying once after a publication that changed
// nothing, and resolving a lost acknowledgment by the row.
func (m *Module) publishUnderClaim(ctx context.Context, tenant model.TenantID, tok admissionToken, h holdID, issued bool) (keyStep, error) {
	var lastErr error
	for try := 0; try < 2; try++ {
		w, err := m.publish(ctx, tenant, tok, h, issued)
		switch {
		case w == writeCommitted:
			return stepOn, nil
		case errors.Is(err, errKeyMoved):
			return stepAgain, nil
		case w == writeUncertain:
			row, found, rerr := m.readRow(ctx, tenant, tok.key)
			switch {
			case rerr != nil:
				return stepRefuse, rerr
			case found && row.state == admStateReserved && row.handle == publishedHandle(h, issued) && row.version == tok.version+1:
				return stepOn, nil
			case found && row.state == admStatePending && row.handle == h && row.version == tok.version:
				lastErr = err
				continue
			}
			return stepRefuse, err
		}
		lastErr = err
	}
	return stepAbandon, lastErr
}

// claim inserts a new pending row naming h. The unique index on the key is its fence:
// another caller's claim of the same key is errKeyMoved.
func (m *Module) claim(ctx context.Context, tenant model.TenantID, row admissionRow) (admissionRow, writeOutcome, error) {
	var stored admissionRow
	w, err := m.mutateClassified(ctx, tenant, func(sc store.Scope) error {
		stored = admissionRow{}
		if err := lockFinOpsWriter(ctx, sc); err != nil {
			return err
		}
		s, err := insertAdmission(ctx, sc, row)
		stored = s
		return err
	})
	return stored, w, err
}

// takeOver writes next over the row as read, fenced at the version it was read at. next
// owes every hold read named. Inside the transaction the row is read again and must be
// at that version; each hold it names must be named by no other row; and a pair an
// earlier build published is asked again, both holds read completely at this
// transaction's instant, whether it holds its key back — the read that decided to take
// the key over never licenses the takeover by itself.
func (m *Module) takeOver(ctx context.Context, tenant model.TenantID, read, next admissionRow) (admissionRow, writeOutcome, error) {
	var stored admissionRow
	w, err := m.mutateClassified(ctx, tenant, func(sc store.Scope) error {
		stored = admissionRow{}
		if err := lockFinOpsWriter(ctx, sc); err != nil {
			return err
		}
		row, found, err := rowOfKey(ctx, sc, read.key)
		if err != nil {
			return err
		}
		if !found || row.version != read.version {
			return errKeyMoved
		}
		if err := holdsOwnedBy(ctx, sc, row, row.handle, row.spendHandle); err != nil {
			return err
		}
		back, err := pairHoldsBack(ctx, sc, row, m.clock.Now())
		if err != nil {
			return err
		}
		if back {
			return errLegacyPairBusy
		}
		s, err := updateAdmission(ctx, sc, next)
		stored = s
		return err
	})
	return stored, w, err
}

// create settles the claim's owed holds and inserts the hold's ledger rows, budgets
// first and then, only if no budget refused, spend limits, all at one instant and in one
// transaction with the claim's fence. A refusal rolls the whole transaction back, the
// settlement of the owed holds included.
func (m *Module) create(ctx context.Context, tenant model.TenantID, tok admissionToken, h holdID, budgets, seats []reservationTarget, estimate int64, now time.Time) (createOutcome, writeOutcome, error) {
	var out createOutcome
	w, err := m.mutateClassified(ctx, tenant, func(sc store.Scope) error {
		out = createOutcome{}
		if err := lockFinOpsWriter(ctx, sc); err != nil {
			return err
		}
		if err := guardLegacyReservationMutation(ctx, sc); err != nil {
			out.denied, out.verdict = true, frontierRefusal(frontierReasonFor(err))
			return err
		}
		row, err := fenceRead(ctx, sc, tok, h)
		if err != nil {
			return err
		}
		if err := holdsOwnedBy(ctx, sc, row, append(append(owedHolds(nil), row.owed...), h)...); err != nil {
			return err
		}
		if _, err := settleOwedInScope(ctx, sc, row.owed, model.NewTimestamp(now)); err != nil {
			return err
		}
		for i, phase := range [][]reservationTarget{budgets, seats} {
			res, err := reserveInScope(ctx, sc, phase, estimate, now, h)
			if res.inserted > 0 {
				out.issued = true
			}
			if res.decided {
				out.denied, out.verdict, out.spendLimit = true, res.result, i == 1
				if err == nil {
					err = errReservationDenied
				}
				return err
			}
			if err != nil {
				return err
			}
		}
		row.owed = nil
		stored, err := updateAdmission(ctx, sc, row)
		if err != nil {
			return err
		}
		out.tok = stored.token()
		return nil
	})
	return out, w, err
}

// publish makes the claim an admission: reserved, naming the hold when the create issued
// it and nothing when the create inserted no row.
func (m *Module) publish(ctx context.Context, tenant model.TenantID, tok admissionToken, h holdID, issued bool) (writeOutcome, error) {
	return m.mutateClassified(ctx, tenant, func(sc store.Scope) error {
		if err := lockFinOpsWriter(ctx, sc); err != nil {
			return err
		}
		row, err := fenceRead(ctx, sc, tok, h)
		if err != nil {
			return err
		}
		row.state = admStateReserved
		row.handle = publishedHandle(h, issued)
		row.owed = nil
		row.stateAt = m.clock.Now()
		_, err = updateAdmission(ctx, sc, row)
		return err
	})
}

// abandon gives a claim up: every hold it owes and its own are settled, and the row is
// released naming nothing. A claim that moved to another caller is errKeyMoved.
func (m *Module) abandon(ctx context.Context, tenant model.TenantID, tok admissionToken, h holdID) (writeOutcome, error) {
	return m.mutateClassified(ctx, tenant, func(sc store.Scope) error {
		if err := lockFinOpsWriter(ctx, sc); err != nil {
			return err
		}
		if err := guardLegacyReservationMutation(ctx, sc); err != nil {
			return err
		}
		row, err := fenceRead(ctx, sc, tok, h)
		if err != nil {
			return err
		}
		now := m.clock.Now()
		owed := append(append(owedHolds(nil), row.owed...), h)
		if err := holdsOwnedBy(ctx, sc, row, owed...); err != nil {
			return err
		}
		if _, err := settleOwedInScope(ctx, sc, owed, now); err != nil {
			return err
		}
		row.state = admStateReleased
		row.handle = ""
		row.owed = nil
		row.stateAt = now
		_, err = updateAdmission(ctx, sc, row)
		return err
	})
}

// fenceRead reads, inside a writer's transaction, the row of tok's key and returns it
// only while it is still the claim tok and h name: at tok's version, pending, naming h.
// Anything else is errKeyMoved. A claim whose owed list does not decode is not written.
func fenceRead(ctx context.Context, sc store.Scope, tok admissionToken, h holdID) (admissionRow, error) {
	row, found, err := rowOfKey(ctx, sc, tok.key)
	if err != nil {
		return admissionRow{}, err
	}
	if !found || row.version != tok.version || row.state != admStatePending || row.handle != h {
		return admissionRow{}, errKeyMoved
	}
	if row.owedErr != nil {
		return admissionRow{}, row.owedErr
	}
	return row, nil
}

// insertAdmission inserts a new row. The unique index on the key refuses a second
// claim of the key as errKeyMoved.
func insertAdmission(ctx context.Context, sc store.Scope, row admissionRow) (admissionRow, error) {
	repo, err := sc.Ext(admissionIdempotencyKind)
	if err != nil {
		return admissionRow{}, err
	}
	rec, err := row.record()
	if err != nil {
		return admissionRow{}, err
	}
	out, err := repo.Create(ctx, rec)
	if errors.Is(err, store.ErrConflict) {
		return admissionRow{}, errKeyMoved
	}
	if err != nil {
		return admissionRow{}, err
	}
	return admissionRowFrom(out)
}

// holdsOwnedBy confirms, inside a writer's transaction and before the writer moves any
// of holds to an owed list or touches a ledger row under one, that no admission row but
// row names one of them in its indexed handle slot. Another such row is the second row of
// one hold, which no writer stores: errAdmissionRowCorrupt, and the writer writes
// nothing. The spend slot is not indexed and is not searched here.
func holdsOwnedBy(ctx context.Context, sc store.Scope, row admissionRow, holds ...holdID) error {
	repo, err := sc.Ext(admissionIdempotencyKind)
	if err != nil {
		return err
	}
	shared := fmt.Errorf("%w: row %s: another row names a hold of this row", errAdmissionRowCorrupt, row.id)
	for _, h := range holds {
		if h.isZero() {
			continue
		}
		recs, page, err := repo.List(ctx, model.Query{Filters: []model.Filter{eq(colAdmHandle, h.String())}, Limit: admissionLookupPage})
		if err != nil {
			return err
		}
		if page.HasMore {
			return shared
		}
		for _, rec := range recs {
			if model.ID(rec.String(model.ColID)) != row.id {
				return shared
			}
		}
	}
	return nil
}

// updateAdmission writes row at the version it was read at; a row that moved since is
// errKeyMoved.
func updateAdmission(ctx context.Context, sc store.Scope, row admissionRow) (admissionRow, error) {
	repo, err := sc.Ext(admissionIdempotencyKind)
	if err != nil {
		return admissionRow{}, err
	}
	rec, err := row.record()
	if err != nil {
		return admissionRow{}, err
	}
	out, err := repo.Update(ctx, rec)
	if errors.Is(err, store.ErrConflict) {
		return admissionRow{}, errKeyMoved
	}
	if err != nil {
		return admissionRow{}, err
	}
	return admissionRowFrom(out)
}

// settleOwedInScope hands back, inside the caller's transaction, the money of every hold
// on an owed list. Each row under an owed hold that still withholds is released, with an
// actual of zero, settled now; a lapsed row is left to the sweep, which records what
// happened to it; a settled row stays as it is. Every hold is read completely or the
// settlement fails, and a row that belongs to the attempt lifecycle is refused as the
// ledger's own settlement refuses it. Any failure aborts the caller's transaction, so the
// owed list and the money move together. The caller holds the writer lock and has
// confirmed that the tenant has no activation frontier. It returns how many rows it
// released.
func settleOwedInScope(ctx context.Context, sc store.Scope, owed owedHolds, now model.Timestamp) (int, error) {
	if len(owed) == 0 {
		return 0, nil
	}
	repo, err := sc.Ext(budgetReservationKind)
	if err != nil {
		return 0, err
	}
	released := 0
	for _, h := range owed {
		rows, err := rowsUnderHold(ctx, sc, h)
		if err != nil {
			return released, err
		}
		for _, r := range rows {
			if link, _ := linkageOf(r); link != linkageLegacy {
				return released, attemptErr(errCodeLifecycleAPIRequired, nil)
			}
		}
		for _, r := range rows {
			if !anyWithholding([]model.Record{r}, now) {
				continue
			}
			r[colResvState] = resvStateReleased
			r[colResvActual] = int64(0)
			r[colResvSettledAt] = now.String()
			if _, err := repo.Update(ctx, r); err != nil {
				return released, err
			}
			released++
		}
	}
	return released, nil
}

// readRow reads the row of key in a read transaction of its own.
func (m *Module) readRow(ctx context.Context, tenant model.TenantID, key string) (admissionRow, bool, error) {
	var (
		row   admissionRow
		found bool
	)
	err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		var err error
		row, found, err = rowOfKey(ctx, sc, key)
		return err
	})
	return row, found, err
}

// readClaimAndHold reads the row of key and whether any ledger row exists under h, in one
// read transaction.
func (m *Module) readClaimAndHold(ctx context.Context, tenant model.TenantID, key string, h holdID) (admissionRow, bool, bool, error) {
	var (
		row    admissionRow
		found  bool
		issued bool
	)
	err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		var err error
		if row, found, err = rowOfKey(ctx, sc, key); err != nil {
			return err
		}
		rows, err := rowsUnderHold(ctx, sc, h)
		issued = len(rows) > 0
		return err
	})
	return row, found, issued, err
}
