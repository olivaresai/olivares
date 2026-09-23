// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"errors"

	"github.com/olivaresai/olivares/core/model"
)

// ErrInvalidAdmission is admission input that cannot be acted on. It is returned for a
// handle that is not a hold identity: neither empty nor the canonical text of a
// non-zero UUID.
var ErrInvalidAdmission = errors.New("finops: invalid admission request")

// errOwedUndecodable says the owed list STORED on a row is not text this module writes.
var errOwedUndecodable = errors.New("finops: owed hold list is undecodable")

// errOwedEntryInvalid refuses to encode an owed list that names no hold, or one hold
// twice.
var errOwedEntryInvalid = errors.New("finops: owed hold list names no hold or one hold twice")

// errOwedFull refuses an owed list that would name more than maxOwedHolds identities.
var errOwedFull = errors.New("finops: owed hold list is full")

// maxOwedHolds bounds the owed list of one row.
const maxOwedHolds = 16

// holdID is the durable identity of one generation of a key's money. The zero value
// is "no hold".
type holdID string

// newHoldID mints the identity of a new generation. An identity is never reused.
func newHoldID() holdID { return holdID(model.NewID()) }

// parseHoldID decodes a handle a caller gave or a slot stored. Not implemented yet:
// every text decodes as itself.
func parseHoldID(s string) (holdID, error) {
	return holdID(s), nil
}

// String returns the identity's canonical text, or "" for no hold.
func (h holdID) String() string { return string(h) }

// isZero reports "no hold".
func (h holdID) isZero() bool { return h == "" }

// owedHolds is the owed list of one admission row: the identities of superseded
// generations whose withholding rows the row still has to release.
type owedHolds []holdID

// decodeOwedHolds decodes a stored owed_handles cell. Not implemented yet: every cell
// decodes as nothing owed.
func decodeOwedHolds(cell any) (owedHolds, error) {
	return nil, nil
}

// encode returns the owed_handles cell of the list. Not implemented yet: every list
// encodes as NULL.
func (o owedHolds) encode() (any, error) {
	return nil, nil
}

// with returns the list plus every identity of hs it does not already name. Not
// implemented yet: it returns the list unchanged.
func (o owedHolds) with(hs ...holdID) (owedHolds, error) {
	return o, nil
}

// admissionToken is (key, version) of an admission row as it was read: the fence of the
// row's next write, which a row found at another version refuses.
type admissionToken struct {
	key     string
	version int64
}
