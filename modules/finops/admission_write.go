// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"context"
	"errors"
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

// mutateClassified runs fn in one write transaction and classifies how it ended. Not
// implemented yet: every end is writeCommitted, with the transaction's error.
func (m *Module) mutateClassified(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) (writeOutcome, error) {
	return writeCommitted, m.data.Mutate(ctx, tenant, fn)
}

// createOutcome is what a create decided.
type createOutcome struct {
	// tok is the claim as the create left it.
	tok admissionToken
	// issued says at least one ledger row was inserted under the hold.
	issued bool
	// denied says the request was refused; nothing the create wrote survives.
	denied bool
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

// takeOver writes next over the row as read, fenced at the version it was read at. Not
// implemented yet: its transaction writes nothing.
func (m *Module) takeOver(ctx context.Context, tenant model.TenantID, read, next admissionRow) (admissionRow, writeOutcome, error) {
	w, err := m.mutateClassified(ctx, tenant, func(store.Scope) error { return nil })
	return admissionRow{}, w, err
}

// create settles the claim's owed holds and inserts the hold's ledger rows. Not
// implemented yet: under the writer lock and the claim's fence it inserts the rows of
// both phases, and it settles, refuses and writes nothing else.
func (m *Module) create(ctx context.Context, tenant model.TenantID, tok admissionToken, h holdID, budgets, seats []reservationTarget, estimate int64, now time.Time) (createOutcome, writeOutcome, error) {
	var out createOutcome
	w, err := m.mutateClassified(ctx, tenant, func(sc store.Scope) error {
		out = createOutcome{}
		if err := lockFinOpsWriter(ctx, sc); err != nil {
			return err
		}
		if _, err := fenceRead(ctx, sc, tok, h); err != nil {
			return err
		}
		for _, phase := range [][]reservationTarget{budgets, seats} {
			res, err := reserveInScope(ctx, sc, phase, estimate, now, h)
			if res.inserted > 0 {
				out.issued = true
			}
			if err != nil {
				return err
			}
		}
		return nil
	})
	return out, w, err
}

// publish makes the claim an admission. Not implemented yet: its transaction writes
// nothing.
func (m *Module) publish(ctx context.Context, tenant model.TenantID, tok admissionToken, h holdID, issued bool) (writeOutcome, error) {
	return m.mutateClassified(ctx, tenant, func(store.Scope) error { return nil })
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
