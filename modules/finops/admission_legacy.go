// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Rows an earlier build left. That build wrote two kinds of admission row this build
// never writes: a claim naming no hold (pending, empty handle), staged before it took
// its holds, and an owes_release row naming holds it still had to hand back. Its writers
// may run beside this build until the operator states they stopped.
//
// An owes_release row is settled whatever that statement says: its holds are money
// nobody will settle otherwise, and releasing a withholding row is what that build
// itself would have done. A legacy claim is retired only once the operator's stop is
// usable. Its writer may still be evaluating, and nothing the store shows — the age of
// the claim, a key nobody retried — establishes that it stopped.

// legacyStopWait is how long after the stated stop every hold its writers took has
// lapsed: they held for the same five minutes the reservation TTL defaults to, and a
// hold taken before the stop expires before the stop plus this wait.
const legacyStopWait = 5 * time.Minute

// errLegacyStopInvalid is a stop whose text is not an RFC 3339 instant in UTC. The text
// itself is not repeated: it is operator configuration, read once at boot.
var errLegacyStopInvalid = errors.New("finops: the legacy writer stop is not an RFC 3339 instant in UTC with the Z designator")

// LegacyWriterStop is the operator's statement of the instant every writer of the
// earlier admission build stopped. The zero value states nothing. It is fixed at
// construction (WithLegacyWriterStop); no route, command or setter changes it.
type LegacyWriterStop struct {
	at      model.Timestamp
	invalid bool
}

// ParseLegacyWriterStop reads the operator's stop. Empty text states nothing. Any other
// text must be an RFC 3339 instant in UTC, written with the Z designator, after the zero
// instant; otherwise it returns an error and a stop that is invalid at every instant, so
// no legacy claim is retired under it.
func ParseLegacyWriterStop(raw string) (LegacyWriterStop, error) {
	if raw == "" {
		return LegacyWriterStop{}, nil
	}
	if !strings.HasSuffix(raw, "Z") {
		return LegacyWriterStop{invalid: true}, errLegacyStopInvalid
	}
	at, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil || at.IsZero() {
		return LegacyWriterStop{invalid: true}, errLegacyStopInvalid
	}
	return LegacyWriterStop{at: model.NewTimestamp(at)}, nil
}

// legacyStopState is what the operator's stop allows at one instant, for one tenant.
type legacyStopState string

// The stop states, in the order legacyStopStateFor decides them. Only usable lets a
// legacy claim be retired.
const (
	legacyStopAbsent       legacyStopState = "absent"
	legacyStopInvalid      legacyStopState = "invalid"
	legacyStopFuture       legacyStopState = "future"
	legacyStopContradicted legacyStopState = "contradicted"
	legacyStopWaiting      legacyStopState = "waiting"
	legacyStopUsable       legacyStopState = "usable"
)

// legacyStopStateFor decides the stop state at now. newestLegacy is the latest date of
// the tenant's legacy claims and owes_release rows, zero when none is dated: a legacy
// row dated after the stop shows a writer still writing then, which contradicts it.
func legacyStopStateFor(stop LegacyWriterStop, now, newestLegacy model.Timestamp) legacyStopState {
	switch {
	case !stop.invalid && stop.at.IsZero():
		return legacyStopAbsent
	case stop.invalid:
		return legacyStopInvalid
	case now.Before(stop.at):
		return legacyStopFuture
	case !newestLegacy.IsZero() && stop.at.Before(newestLegacy):
		return legacyStopContradicted
	case now.Time().Before(stop.at.Time().Add(legacyStopWait)):
		return legacyStopWaiting
	}
	return legacyStopUsable
}

// tenantLegacyStop reads, in the caller's transaction, the tenant's legacy claims and
// owes_release rows through the indexed state, and decides the stop state at now. A read
// that does not complete is an error: an unread row may be the one that contradicts it.
func tenantLegacyStop(ctx context.Context, sc store.Scope, stop LegacyWriterStop, now model.Timestamp) (legacyStopState, error) {
	if stop.invalid || stop.at.IsZero() {
		return legacyStopStateFor(stop, now, model.Timestamp{}), nil
	}
	claims, err := admissionRowsIn(ctx, sc, admStatePending)
	if err != nil {
		return "", err
	}
	owes, err := admissionRowsIn(ctx, sc, admStateOwesRelease)
	if err != nil {
		return "", err
	}
	return legacyStopStateFor(stop, now, newestLegacyDate(claims, owes)), nil
}

// newestLegacyDate is the latest date among the stored rows of recs that an earlier build
// wrote — legacy claims and owes_release rows — and zero when none is dated. It reads the
// stored cells, so a row that does not decode still dates the writer that wrote it, and a
// claim or an admission of this build dates nothing.
func newestLegacyDate(recs ...[]model.Record) model.Timestamp {
	var newest model.Timestamp
	for _, set := range recs {
		for _, rec := range set {
			switch rec.String(colAdmState) {
			case admStateOwesRelease:
			case admStatePending:
				if rec.String(colAdmHandle) != "" {
					continue
				}
			default:
				continue
			}
			if at := parseStateAt(rec[colAdmStateAt]); newest.Before(at) {
				newest = at
			}
		}
	}
	return newest
}

// recoveryStep is what one recovery write came to for the row it was given.
type recoveryStep int

const (
	// recoveryDone: the write committed, or a read after a write whose outcome was
	// unknown found it there.
	recoveryDone recoveryStep = iota
	// recoveryNone: nothing was written. The row as read needs no write, or it is no
	// longer the row that was read; the next pass reads it again.
	recoveryNone
	// recoveryUnresolved: the write rolled back, or whether it committed could not be
	// established. The row still names what it named, and the next pass writes again.
	recoveryUnresolved
	// recoveryBlocked: the write would change a ledger row the attempt lifecycle owns,
	// under an activation frontier or by its linkage. Nothing was written.
	recoveryBlocked
	// recoveryCorrupt: the row, or another row naming one of its holds, failed its
	// integrity check. Nothing was written.
	recoveryCorrupt
)

// String names the step.
func (s recoveryStep) String() string {
	switch s {
	case recoveryDone:
		return "done"
	case recoveryNone:
		return "none"
	case recoveryUnresolved:
		return "unresolved"
	case recoveryBlocked:
		return "blocked"
	case recoveryCorrupt:
		return "corrupt"
	}
	return "unknown"
}

// recoveryStepOf classifies a recovery write that did not commit: a known rollback
// whose cause is typed, or unresolved.
func recoveryStepOf(err error) recoveryStep {
	switch {
	case errors.Is(err, errKeyMoved):
		return recoveryNone
	case errors.Is(err, errAdmissionRowCorrupt):
		return recoveryCorrupt
	case attemptCode(err) == errCodeLifecycleAPIRequired:
		return recoveryBlocked
	}
	return recoveryUnresolved
}

// isLegacyRow reports whether row is one only an earlier build writes.
func isLegacyRow(row admissionRow) bool {
	return row.state == admStateOwesRelease || (row.state == admStatePending && row.handle.isZero())
}

// legacySettlement is what the one write to a row an earlier build left did.
type legacySettlement struct {
	step recoveryStep
	// released counts the ledger rows it released; dropped the holds it stopped naming
	// because, under an activation frontier, the attempt lifecycle owns their money;
	// blocked the holds it could neither settle nor drop.
	released, dropped, blocked int
}

// settleLegacy settles one row an earlier build left, as settleLegacyRow does, and
// returns the step and how many ledger rows the write released.
func (m *Module) settleLegacy(ctx context.Context, tenant model.TenantID, read admissionRow, stop legacyStopState) (recoveryStep, int) {
	out := m.settleLegacyRow(ctx, tenant, read, stop)
	return out.step, out.released
}

// settleLegacyRow is the one write recovery makes to a row an earlier build left, read as
// read. An owes_release row is settled under any stop; a legacy claim is retired only
// when stop is usable. Either way, in one transaction: the writer lock; when the row
// names holds, the tenant's activation frontier; the row read again, which must still be
// the row read at its version; each hold it names in either slot or owes named by no
// other row. With no frontier, every row under those holds — read completely — that
// still withholds is released with an actual of zero, settled now, and a lapsed row is
// left to the sweep. Under a frontier no ledger row is written, and the row is written
// only when the attempt lifecycle owns every hold it names; otherwise it is left whole,
// each of its holds blocked. The row is written released, naming nothing and owing
// nothing. A row whose owed list does not decode is not written.
func (m *Module) settleLegacyRow(ctx context.Context, tenant model.TenantID, read admissionRow, stop legacyStopState) legacySettlement {
	if m.data == nil || !isLegacyRow(read) || read.owedErr != nil {
		return legacySettlement{step: recoveryNone}
	}
	if read.state == admStatePending && stop != legacyStopUsable {
		return legacySettlement{step: recoveryNone}
	}
	now := m.clock.Now()
	var out legacySettlement
	w, err := m.mutateClassified(ctx, tenant, func(sc store.Scope) error {
		out = legacySettlement{}
		if err := lockFinOpsWriter(ctx, sc); err != nil {
			return err
		}
		var (
			census   map[string]bool
			frontier bool
		)
		if len(legacyHolds(read)) > 0 {
			var err error
			if census, frontier, err = lifecycleCensus(ctx, sc); err != nil {
				return err
			}
		}
		row, found, err := rowOfKey(ctx, sc, read.key)
		if err != nil {
			return err
		}
		if !found || row.version != read.version || row.state != read.state ||
			row.handle != read.handle || row.spendHandle != read.spendHandle || row.owedErr != nil {
			return errKeyMoved
		}
		holds := legacyHolds(row)
		if len(holds) > 0 {
			if err := holdsOwnedBy(ctx, sc, row, holds...); err != nil {
				return err
			}
			if frontier {
				for _, h := range holds {
					owned, err := lifecycleOwns(ctx, sc, census, h)
					if err != nil {
						return err
					}
					if !owned {
						out.blocked = len(holds)
						return attemptErr(errCodeLifecycleAPIRequired, nil)
					}
				}
				out.dropped = len(holds)
			} else if out.released, err = settleOwedInScope(ctx, sc, holds, now); err != nil {
				return err
			}
		}
		row.state = admStateReleased
		row.handle, row.spendHandle, row.owed = "", "", nil
		row.stateAt = now
		_, err = updateAdmission(ctx, sc, row)
		return err
	})
	switch w {
	case writeCommitted:
		out.step = recoveryDone
		return out
	case writeUncertain:
		return m.legacySettledByIdentity(ctx, tenant, read, now, out)
	}
	return legacySettlement{step: recoveryStepOf(err), blocked: out.blocked}
}

// legacyHolds lists every hold a legacy row names, in its slots and its owed list.
func legacyHolds(row admissionRow) owedHolds {
	holds := owedHolds(slotsOf(row))
	for _, h := range row.owed {
		if !holds.has(h) {
			holds = append(holds, h)
		}
	}
	return holds
}

// legacySettledByIdentity resolves a legacy settlement whose outcome is unknown, in a
// read transaction of its own. The row one version on, released and naming nothing, is
// that write, wrote: done, with the rows under its holds it released at its instant.
// Anything else — the row as it was read, another write, a read that fails — is
// unresolved, and the next pass decides from the row as it then stands.
func (m *Module) legacySettledByIdentity(ctx context.Context, tenant model.TenantID, read admissionRow, now model.Timestamp, wrote legacySettlement) legacySettlement {
	out := legacySettlement{step: recoveryUnresolved}
	err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		out = legacySettlement{step: recoveryUnresolved}
		row, found, err := rowOfKey(ctx, sc, read.key)
		if err != nil {
			return err
		}
		if !found || row.version != read.version+1 || row.state != admStateReleased ||
			!row.handle.isZero() || !row.spendHandle.isZero() || len(row.owed) != 0 {
			return nil
		}
		rows, err := ledgerRowsOfHolds(ctx, sc, legacyHolds(read))
		if err != nil {
			return err
		}
		out = legacySettlement{step: recoveryDone, dropped: wrote.dropped}
		for _, r := range rows {
			if r.String(colResvState) == resvStateReleased && r.String(colResvSettledAt) == now.String() {
				out.released++
			}
		}
		return nil
	})
	if err != nil {
		return legacySettlement{step: recoveryUnresolved}
	}
	return out
}
