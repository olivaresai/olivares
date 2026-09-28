// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The states of an admission row.
const (
	// admStatePending is a claim in flight; its handle is the claimant's intent.
	admStatePending = "pending"
	// admStateReserved is a published admission; its handle is the issued hold, or ""
	// when the admission holds nothing.
	admStateReserved = "reserved"
	// admStateCommitted and admStateReleased are settled admissions.
	admStateCommitted = "committed"
	admStateReleased  = "released"
	// admStateOwesRelease is written only by an earlier build: a row that was
	// superseded while its slots still named money it had not handed back.
	admStateOwesRelease = "owes_release"
)

// admissionLookupPage is how many rows one admission lookup reads. At most one row may
// match; the others are read only to show there is no second match, including after
// the spend-slot lookup sets aside rows that do not publish the hold. A lookup that
// finds more rows than this, or more than one match among them, is corrupt.
const admissionLookupPage = 4

// errAdmissionRowCorrupt says a stored admission row is not one any writer, current or
// earlier, writes: a slot that is not a hold identity, or two rows where one may exist.
// It is an integrity fault. It is refused, never read as "no hold", and an ambiguous hold
// is never resolved by picking one of its rows; nothing is decoded from such a row or
// written to it, its stored values stay as they are, and no log carries them.
var errAdmissionRowCorrupt = errors.New("finops: admission row is corrupt")

// admissionRow is one admission row as read.
type admissionRow struct {
	id          model.ID
	key         string
	payloadHash string
	scope       string
	estimate    int64
	state       string
	// stateAt is when the row entered its state; zero for an undated row.
	stateAt model.Timestamp
	// handle is the claimant's intent while pending, then the issued hold or "".
	handle holdID
	// spendHandle is the seat slot of a legacy pair. No current writer fills it.
	spendHandle holdID
	// owed is the decoded owed list. When the stored text does not decode, owedErr
	// says why, owed is empty, and owedRaw keeps the text to write back unchanged.
	owed    owedHolds
	owedRaw any
	owedErr error
	// version is the store version the row was read at: the fence of its next write.
	version int64
}

// token is the row's fence: its key and the version it was read at.
func (r admissionRow) token() admissionToken {
	return admissionToken{key: r.key, version: r.version}
}

// admissionRowFrom decodes a stored row. Both slots decode strictly, and a slot that is
// not a hold identity makes the row errAdmissionRowCorrupt. An owed list that does not
// decode does not: the row comes back with owedErr set, because a settlement of its own
// hold may still proceed and recovery must be able to count it. state_at keeps the
// earlier builds' rule: absent or unparsable text is an undated row.
func admissionRowFrom(rec model.Record) (admissionRow, error) {
	id := model.ID(rec.String(model.ColID))
	handle, err := parseHoldID(rec.String(colAdmHandle))
	if err != nil {
		return admissionRow{}, fmt.Errorf("%w: row %s: the handle slot is not a hold identity", errAdmissionRowCorrupt, id)
	}
	spend, err := parseHoldID(rec.String(colAdmSpendHandle))
	if err != nil {
		return admissionRow{}, fmt.Errorf("%w: row %s: the spend slot is not a hold identity", errAdmissionRowCorrupt, id)
	}
	row := admissionRow{
		id:          id,
		key:         rec.String(colAdmKey),
		payloadHash: rec.String(colAdmPayloadHash),
		scope:       rec.String(colAdmScope),
		estimate:    rec.Int(colAdmEstimate),
		state:       rec.String(colAdmState),
		stateAt:     parseStateAt(rec[colAdmStateAt]),
		handle:      handle,
		spendHandle: spend,
		version:     rec.Int(model.ColVersion),
	}
	if owed, err := decodeOwedHolds(rec[colAdmOwedHandles]); err != nil {
		row.owedRaw, row.owedErr = rec[colAdmOwedHandles], err
	} else {
		row.owed = owed
	}
	return row, nil
}

// parseStateAt reads a state_at cell. NULL, empty or unparsable text is the zero
// Timestamp: an undated row, which every reader treats as older than any window.
func parseStateAt(cell any) model.Timestamp {
	text, ok := cell.(string)
	if !ok || strings.TrimSpace(text) == "" {
		return model.Timestamp{}
	}
	ts, err := model.ParseTimestamp(text)
	if err != nil {
		return model.Timestamp{}
	}
	return ts
}

// record encodes the row for its one write. A row never stored (zero id) is an insert;
// any other carries its id and the version it was read at, so the store refuses the
// write if the row moved since. Every column is written, because the store's update
// sets them all, and an undecodable owed list goes back exactly as it was read.
func (r admissionRow) record() (model.Record, error) {
	owed := r.owedRaw
	if r.owedErr == nil {
		var err error
		if owed, err = r.owed.encode(); err != nil {
			return nil, err
		}
	}
	var spend, stateAt any
	if !r.spendHandle.isZero() {
		spend = r.spendHandle.String()
	}
	if !r.stateAt.IsZero() {
		stateAt = r.stateAt.String()
	}
	rec := model.Record{
		colAdmKey:         r.key,
		colAdmPayloadHash: r.payloadHash,
		colAdmHandle:      r.handle.String(),
		colAdmSpendHandle: spend,
		colAdmScope:       r.scope,
		colAdmEstimate:    r.estimate,
		colAdmState:       r.state,
		colAdmStateAt:     stateAt,
		colAdmOwedHandles: owed,
	}
	if !r.id.IsZero() {
		rec[model.ColID] = r.id.String()
		rec[model.ColVersion] = r.version
	}
	return rec, nil
}

// rowOfKey reads the admission row of key through its unique index. Like every lookup
// here it runs in the caller's View or Mutate, whose repository adds the tenant.
func rowOfKey(ctx context.Context, sc store.Scope, key string) (admissionRow, bool, error) {
	return oneAdmissionRow(ctx, sc, eq(colAdmKey, key), nil)
}

// rowNamingHold reads the admission row that names h. The indexed handle slot is read
// first and names h in any state. Only if no row names h there is the spend slot read —
// it is not indexed, and only a legacy pair fills it — and there a row names h only
// while it publishes it: reserved, committed or released. An owes_release row is
// settled by recovery, not by a caller holding one of its old handles. No hold is
// named by no row.
func rowNamingHold(ctx context.Context, sc store.Scope, h holdID) (admissionRow, bool, error) {
	if h.isZero() {
		return admissionRow{}, false, nil
	}
	row, found, err := oneAdmissionRow(ctx, sc, eq(colAdmHandle, h.String()), nil)
	if err != nil || found {
		return row, found, err
	}
	return oneAdmissionRow(ctx, sc, eq(colAdmSpendHandle, h.String()), publishesHold)
}

// publishesHold reports whether a row in state names its hold as an admission.
func publishesHold(state string) bool {
	switch state {
	case admStateReserved, admStateCommitted, admStateReleased:
		return true
	}
	return false
}

// oneAdmissionRow reads the admission rows matching filter, keeps those whose state
// keep accepts (all of them when keep is nil), and returns the one that remains. More
// than one is errAdmissionRowCorrupt: a key has one row, and a hold one generation.
func oneAdmissionRow(ctx context.Context, sc store.Scope, filter model.Filter, keep func(state string) bool) (admissionRow, bool, error) {
	repo, err := sc.Ext(admissionIdempotencyKind)
	if err != nil {
		return admissionRow{}, false, err
	}
	recs, page, err := repo.List(ctx, model.Query{Filters: []model.Filter{filter}, Limit: admissionLookupPage})
	if err != nil {
		return admissionRow{}, false, err
	}
	var matched []model.Record
	for _, rec := range recs {
		if keep == nil || keep(rec.String(colAdmState)) {
			matched = append(matched, rec)
		}
	}
	if page.HasMore || len(matched) > 1 {
		return admissionRow{}, false, fmt.Errorf("%w: more than one row matches %s", errAdmissionRowCorrupt, filter.Column)
	}
	if len(matched) == 0 {
		return admissionRow{}, false, nil
	}
	row, err := admissionRowFrom(matched[0])
	if err != nil {
		return admissionRow{}, false, err
	}
	return row, true, nil
}

// rowsUnderHold reads every ledger row under h — both components, every state —
// completely or not at all: a prefix of a hold's money is not its money.
func rowsUnderHold(ctx context.Context, sc store.Scope, h holdID) ([]model.Record, error) {
	if h.isZero() {
		return nil, nil
	}
	repo, err := sc.Ext(budgetReservationKind)
	if err != nil {
		return nil, err
	}
	rows, incomplete, err := scanReservations(ctx, repo, []model.Filter{eq(colResvHandle, h.String())})
	if err != nil {
		return nil, err
	}
	if incomplete != "" {
		return nil, fmt.Errorf("%w: %s (hold %s)", errReservationScanIncomplete, incomplete, h)
	}
	return rows, nil
}
