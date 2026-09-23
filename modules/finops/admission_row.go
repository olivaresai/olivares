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

// errAdmissionRowCorrupt says a stored admission row is not one any writer, current or
// earlier, writes: a slot that is not a hold identity, or two rows where one may exist.
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

// token is the row's fence. Not implemented yet: it is the zero token.
func (r admissionRow) token() admissionToken {
	return admissionToken{}
}

// admissionRowFrom decodes a stored row. Not implemented yet: every record decodes as
// the zero row.
func admissionRowFrom(rec model.Record) (admissionRow, error) {
	return admissionRow{}, nil
}

// record encodes the row for its one write. Not implemented yet: it encodes nothing.
func (r admissionRow) record() (model.Record, error) {
	return nil, nil
}

// rowOfKey reads the admission row of key. Not implemented yet: it reads nothing and
// answers a row that carries only the key.
func rowOfKey(ctx context.Context, sc store.Scope, key string) (admissionRow, bool, error) {
	return admissionRow{key: key}, true, nil
}

// rowNamingHold reads the admission row that names h. Not implemented yet: no row
// names any hold.
func rowNamingHold(ctx context.Context, sc store.Scope, h holdID) (admissionRow, bool, error) {
	return admissionRow{}, false, nil
}

// rowsUnderHold reads every ledger row under h. Not implemented yet: it reads none.
func rowsUnderHold(ctx context.Context, sc store.Scope, h holdID) ([]model.Record, error) {
	return nil, nil
}
