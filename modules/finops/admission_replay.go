// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"context"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// admissionReplayWindow is how long one admission answers for RETRIES of the call that
// wrote it, measured from the moment its row entered its state. Inside it an identical
// request is the same call and is handed the same hold; outside it the request is
// evaluated again against the ledger.
//
// It is a constant set to the reservation TTL's value, five minutes, and it must never
// be shorter than the TTL: every row of a hold expires at its creation plus the TTL,
// which is no later than its publication plus the TTL, so with a window at least that
// long every row of a received hold has lapsed once the window has passed, and taking
// the key over cannot take a hold that still withholds. The TTL is a variable only so
// that tests can shorten it, which keeps the relation; TestReplayReturnsLiveHold checks
// it. A longer window would not hand back a lapsed hold either: a reserved row replays
// only while its hold withholds.
const admissionReplayWindow = 5 * time.Minute

// replayOutcome is what an admission row says about a request that carries its key.
type replayOutcome int

const (
	// replayUnknown comes with every error: the row's answer could not be read. It is
	// the zero value, so an outcome read past its error is never "evaluate".
	replayUnknown replayOutcome = iota
	// replayEvaluate: the row does not answer for the request, which is evaluated.
	replayEvaluate
	// replayAnswer: the request is a retry of the call that wrote the row, and is
	// answered with that call's hold.
	replayAnswer
	// replayConflict: the key was written for another payload. Never a replay.
	replayConflict
)

// String names the outcome.
func (o replayOutcome) String() string {
	switch o {
	case replayEvaluate:
		return "evaluate"
	case replayAnswer:
		return "replay"
	case replayConflict:
		return "conflict"
	}
	return "unknown"
}

// replayFor decides whether row answers for a request whose payload hashes to hash, and
// with which hold. It is the admission's whole memo, and it answers in two cases only,
// each with a complete read set:
//
//   - A COMMITTED row inside the window answers with its hold. It reads the row's
//     payload hash, state and state_at, and the clock. The call already ran and was
//     charged, so nothing else is read: no ledger row, spend, policy, identity or group.
//   - A RESERVED row inside the window answers with its hold while that hold still
//     withholds. It reads the same, plus both slots and the state and expiry of the
//     rows under them. Spend and policy are not read: the hold is that call's money,
//     and a retry must not hold twice.
//
// Every other row answers nothing and the request is evaluated: a row outside the
// window or undated, a row that holds nothing, a released, pending or owes_release row.
// Another payload under the key is replayConflict, inside the window or not. An error
// is a read that did not complete; it is not "no longer withholding", its outcome is
// replayUnknown, and a caller must refuse on it deny-closed.
func (m *Module) replayFor(ctx context.Context, sc store.Scope, row admissionRow, hash string) (replayOutcome, holdID, error) {
	if row.payloadHash != hash {
		return replayConflict, "", nil
	}
	now := m.clock.Now()
	if !withinReplayWindow(row, now) {
		return replayEvaluate, "", nil
	}
	switch row.state {
	case admStateCommitted:
		return replayAnswer, row.answerHold(), nil
	case admStateReserved:
		live, err := handlesLive(ctx, sc, row.handle, row.spendHandle, now)
		if err != nil {
			return replayUnknown, "", err
		}
		if live {
			return replayAnswer, row.answerHold(), nil
		}
	}
	return replayEvaluate, "", nil
}

// answerHold is the hold a replay of the row hands back: its handle slot, or, for a
// spend-only legacy row, its seat slot.
func (r admissionRow) answerHold() holdID {
	if !r.handle.isZero() {
		return r.handle
	}
	return r.spendHandle
}

// withinReplayWindow reports whether row is young enough to answer for retries of the
// call that wrote it. An undated row is outside: it cannot be shown to be a retry.
func withinReplayWindow(row admissionRow, now model.Timestamp) bool {
	if row.stateAt.IsZero() {
		return false
	}
	return now.Time().Sub(row.stateAt.Time()) < admissionReplayWindow
}

// handlesLive reports whether the holds a row names still withhold headroom: the one
// question the replay of a reserved row asks, because what it hands back IS the hold.
//
// No hold is not a live hold, so a row that holds nothing is re-evaluated on every call
// and never frozen under its key. Every slot the row names is read completely; a slot
// whose rows exist and none of which withholds makes the row not live; and the row is
// live only if some row withholds. A slot whose complete read finds no row withholds
// nothing: it neither makes the row live nor stops the other slot from doing so, and a
// row whose only hold has no row is evaluated in full. A slot whose rows cannot be read
// completely is an ERROR, not "not live": that answer would re-evaluate the key and move
// a live, received hold to owed.
func handlesLive(ctx context.Context, sc store.Scope, handle, spend holdID, now model.Timestamp) (bool, error) {
	live := false
	for _, h := range []holdID{handle, spend} {
		if h.isZero() {
			continue
		}
		rows, err := rowsUnderHold(ctx, sc, h)
		if err != nil {
			return false, err
		}
		if len(rows) == 0 {
			continue
		}
		if !anyWithholding(rows, now) {
			return false, nil
		}
		live = true
	}
	return live, nil
}

// pairHoldsBack reports whether row is a pair an earlier build published — reserved, a
// hold in each slot — that holds its key back at now: dated inside the replay window,
// with a row under either slot that withholds. Such a row does not answer a retry while
// one of its holds has lapsed, and while it holds back nobody takes the key over,
// releases one of its holds or takes a new hold under the key: either hold may be money
// a caller received. The key is busy until both holds lapse or are settled.
//
// Both slots are read completely before anything is decided, and a slot that cannot be
// read so is an error. An undated row is outside the window and never holds back.
//
// The window bounds the wait. That build created both holds of a pair before it
// published the row and dated the row at publication, reading one clock, so every row of
// the pair expires at its creation plus the TTL — no later than the row's date plus the
// TTL, which the window is never shorter than — unless that clock stepped back between
// the reads. Inside the window a takeover asks this again in its own transaction, so it
// never needs that premise.
func pairHoldsBack(ctx context.Context, sc store.Scope, row admissionRow, now model.Timestamp) (bool, error) {
	if row.state != admStateReserved || row.handle.isZero() || row.spendHandle.isZero() || !withinReplayWindow(row, now) {
		return false, nil
	}
	back := false
	for _, h := range []holdID{row.handle, row.spendHandle} {
		rows, err := rowsUnderHold(ctx, sc, h)
		if err != nil {
			return false, err
		}
		if anyWithholding(rows, now) {
			back = true
		}
	}
	return back, nil
}

// anyWithholding reports whether one of rows keeps money from other callers at now:
// active, and its expiry not yet reached. An expiry that does not parse counts as
// withholding: a row whose end cannot be read is not shown to have ended.
func anyWithholding(rows []model.Record, now model.Timestamp) bool {
	for _, r := range rows {
		if r.String(colResvState) != resvStateActive {
			continue
		}
		exp, err := model.ParseTimestamp(r.String(colResvExpiresAt))
		if err == nil && !exp.Time().After(now.Time()) {
			continue
		}
		return true
	}
	return false
}
