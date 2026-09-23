// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/olivaresai/olivares/core/model"
)

// ErrInvalidAdmission is admission input that cannot be acted on. It is returned for a
// handle that is not a hold identity: neither empty nor the canonical text of a
// non-zero UUID.
var ErrInvalidAdmission = errors.New("finops: invalid admission request")

// errOwedUndecodable says the owed list STORED on a row is not text this module writes.
// Nothing is guessed from it: the decoded row keeps that text and writes it back
// unchanged whenever the row is encoded.
var errOwedUndecodable = errors.New("finops: owed hold list is undecodable")

// errOwedEntryInvalid refuses to encode an owed list that names no hold, or one hold
// twice. Only a writer's own fault builds such a list in memory; nothing stores it.
var errOwedEntryInvalid = errors.New("finops: owed hold list names no hold or one hold twice")

// errOwedFull refuses an owed list that would name more than maxOwedHolds identities.
var errOwedFull = errors.New("finops: owed hold list is full")

// maxOwedHolds bounds the owed list of one row: a longer list is neither decoded,
// encoded nor built by a union.
const maxOwedHolds = 16

// holdID is the durable identity of one generation of a key's money. It is minted
// before any ledger row of its generation exists, and every such row — budget and
// spend limit alike — carries it as its handle, so the money can be found by it
// whatever later happens to the key. The zero value is "no hold". A holdID is only
// built by newHoldID or parseHoldID, so it is canonical or empty.
type holdID string

// newHoldID mints the identity of a new generation. An identity is never reused.
func newHoldID() holdID { return holdID(model.NewID()) }

// parseHoldID decodes a handle a caller gave or a slot stored. "" is no hold. A value
// is an identity only if it is a non-zero UUID written in its own canonical text: the
// store compares text, so a spelling the general parser also accepts — uppercase,
// braces, a urn prefix, no dashes — would name no ledger row. Anything else is
// ErrInvalidAdmission.
func parseHoldID(s string) (holdID, error) {
	if s == "" {
		return "", nil
	}
	id, err := model.ParseID(s)
	if err != nil || id.IsZero() || id.String() != s {
		return "", fmt.Errorf("%w: the handle is not a hold identity", ErrInvalidAdmission)
	}
	return holdID(s), nil
}

// String returns the identity's canonical text, or "" for no hold.
func (h holdID) String() string { return string(h) }

// isZero reports "no hold".
func (h holdID) isZero() bool { return h == "" }

// owedHolds is the owed list of one admission row: the identities of superseded
// generations whose withholding rows the row still has to release. Its entries are
// distinct and never "", and there are at most maxOwedHolds of them.
type owedHolds []holdID

// decodeOwedHolds decodes a stored owed_handles cell. NULL is nothing owed. Any other
// value must be exactly what encode writes — a JSON array of one to maxOwedHolds
// distinct identities in canonical text, and no other bytes — or it is
// errOwedUndecodable: a list this module did not write is not guessed at.
func decodeOwedHolds(cell any) (owedHolds, error) {
	if cell == nil {
		return nil, nil
	}
	text, ok := cell.(string)
	if !ok {
		return nil, errOwedUndecodable
	}
	var raw []string
	if err := json.Unmarshal([]byte(text), &raw); err != nil {
		return nil, errOwedUndecodable
	}
	if len(raw) == 0 || len(raw) > maxOwedHolds {
		return nil, errOwedUndecodable
	}
	out := make(owedHolds, 0, len(raw))
	for _, s := range raw {
		h, err := parseHoldID(s)
		if err != nil || h.isZero() || out.has(h) {
			return nil, errOwedUndecodable
		}
		out = append(out, h)
	}
	if enc, err := out.encode(); err != nil || enc != text {
		return nil, errOwedUndecodable
	}
	return out, nil
}

// encode returns the owed_handles cell of the list: NULL when nothing is owed, else its
// canonical JSON array. A list outside the bounds cannot be encoded, so no writer can
// store one: more than maxOwedHolds entries is errOwedFull, and an entry that names no
// hold or repeats one is errOwedEntryInvalid.
func (o owedHolds) encode() (any, error) {
	if len(o) == 0 {
		return nil, nil
	}
	if len(o) > maxOwedHolds {
		return nil, errOwedFull
	}
	raw := make([]string, 0, len(o))
	for i, h := range o {
		if h.isZero() || o[:i].has(h) {
			return nil, errOwedEntryInvalid
		}
		raw = append(raw, h.String())
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	return string(b), nil
}

// has reports whether the list names h.
func (o owedHolds) has(h holdID) bool {
	for _, x := range o {
		if x == h {
			return true
		}
	}
	return false
}

// with returns the list plus every identity of hs it does not already name; "" adds
// nothing. A union that would pass maxOwedHolds is errOwedFull, returns nil, and
// leaves o as it was.
func (o owedHolds) with(hs ...holdID) (owedHolds, error) {
	out := append(owedHolds(nil), o...)
	for _, h := range hs {
		if h.isZero() || out.has(h) {
			continue
		}
		if len(out) == maxOwedHolds {
			return nil, errOwedFull
		}
		out = append(out, h)
	}
	return out, nil
}

// admissionToken is (key, version) of an admission row as it was read: the fence of the
// row's next write, which a row found at another version refuses.
type admissionToken struct {
	key     string
	version int64
}
