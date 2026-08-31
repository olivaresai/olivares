// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"fmt"
	"strings"
)

// guardprestate.go canonicalises a prestate: the digest the ledger compares, and the
// rendering an operator reads.
//
// Both come from ONE function over ONE value, which is the property that matters. The
// alternative — a digest computed in one place and a description written in another — lets
// the ledger record a hash of one reading next to a description of a different one, and
// nobody would notice until the two were compared for the first time, which is exactly the
// moment nobody can afford a surprise.

// prestateDigest is the value that must appear identically in attempt-judged and in the
// receipt.
//
// The equality of those two is a load-bearing invariant, not a checksum: the judged event
// says "this is what authorized the change" and the receipt says "this is what I changed",
// and if they can disagree then the receipt's authorisation is unverifiable. Computing both
// from this one function is what makes the equality structural rather than a convention two
// call sites have to remember.
func prestateDigest(p prestate) ([32]byte, error) {
	w := newCanonWriter(canonDomainPrestate, guardManifestFormat)
	p.canon(w)
	return w.sum()
}

// canon writes the prestate's canonical encoding.
//
// Every field is included, in a fixed order, under the type tags the encoder writes. The
// observable half (existence, presence, state, canonicality) is what the object was; the
// binding half (rollout, epoch, format, code and retained pairs, spec and definition) is
// what authorized acting on it. A digest over only the first would let the same reading
// authorize a change under any edition.
func (p prestate) canon(w *canonWriter) {
	w.boolean(p.TargetExists)
	w.boolean(p.GuardPresent)
	w.str(p.GuardEnableState)
	w.boolean(p.GuardMatchesCanonical)
	w.boolean(p.ReceiptPresent)
	w.i64(p.Epoch)
	w.str(p.RolloutID)
	w.i64(p.ManifestFormat)
	w.str(p.CodeSHA256)
	w.i64(p.RetainedRevision)
	w.str(p.RetainedSHA256)
	w.str(p.SpecSHA256)
	w.str(p.DefinitionSHA256)
}

// prestateRendering is the human-readable description stored beside the digest.
//
// It is DIAGNOSTIC and it is NOT the digest's preimage — that is the canonical binary
// encoding above. Saying so plainly matters, because a reader who assumed otherwise would
// try to verify a hash by hashing this string and conclude the ledger was corrupt. What it
// is for is the operator looking at a blocked rollout who needs to know what the runner saw
// without reconstructing it from a hash.
//
// Every value is rendered with %q, so the fields cannot run together and a value containing
// a separator cannot forge one.
func prestateRendering(p prestate) string {
	var b strings.Builder
	for i, f := range []struct {
		k string
		v any
	}{
		{"target_exists", p.TargetExists},
		{"guard_present", p.GuardPresent},
		{"guard_state", p.GuardEnableState},
		{"guard_canonical", p.GuardMatchesCanonical},
		{"receipt_present", p.ReceiptPresent},
		{"epoch", p.Epoch},
		{"rollout_id", p.RolloutID},
		{"manifest_format", p.ManifestFormat},
		{"code_sha256", p.CodeSHA256},
		{"retained_revision", p.RetainedRevision},
		{"retained_sha256", p.RetainedSHA256},
		{"spec_sha256", p.SpecSHA256},
		{"definition_sha256", p.DefinitionSHA256},
	} {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(f.k)
		b.WriteByte('=')
		fmt.Fprintf(&b, "%q", fmt.Sprint(f.v))
	}
	return b.String()
}

// guardRolloutContext is the immutable authorisation a rollout hands every unit.
//
// It is READ BACK from the durable pending-opened event rather than assembled from the
// binary's constants, and that direction is the point: a callback that built its own epoch
// could authorize a change the gate never opened. Project copies from here; it does not
// compute.
type guardRolloutContext struct {
	RolloutID        string
	Format           int64
	CodeEpoch        int64
	CodeSHA256       [32]byte
	RetainedRevision int64
	RetainedSHA256   [32]byte
}

// bind stamps a reading with the authorisation that permits acting on it.
//
// The entry's own two digests come from the spec, so a unit cannot be authorized by an
// edition that does not declare its entry: the spec digest travels into the prestate, into
// the judged event and into the receipt, and the matrix compares all three.
func (c guardRolloutContext) bind(p prestate, spec guardSpec) prestate {
	p.Epoch = c.CodeEpoch
	p.RolloutID = c.RolloutID
	p.ManifestFormat = c.Format
	p.CodeSHA256 = hexDigest(c.CodeSHA256)
	p.RetainedRevision = c.RetainedRevision
	p.RetainedSHA256 = hexDigest(c.RetainedSHA256)
	p.SpecSHA256 = hexDigest(spec.SpecSHA256)
	p.DefinitionSHA256 = hexDigest(spec.DefinitionSHA256)
	return p
}

// receiptProjectionFrom renders a durable receipt as the projection the matrix compares.
//
// The conversion lives here, next to bind, so the two halves of the comparison are written
// against each other: whatever bind puts into a prestate is what this reads out of a
// receipt. Splitting them across files is how one side gains a field the other does not
// read, and a field nobody reads is a field in which two different receipts compare equal.
func receiptProjectionFrom(r guardReceipt) receiptProjection {
	return receiptProjection{
		Readable:         true,
		Present:          true,
		Epoch:            r.Epoch,
		RolloutID:        r.RolloutID,
		ManifestFormat:   r.Format,
		CodeSHA256:       hexDigest(r.CodeSHA256),
		RetainedRevision: r.RetainedRevision,
		RetainedSHA256:   hexDigest(r.RetainedSHA256),
		SpecSHA256:       hexDigest(r.SpecSHA256),
		DefinitionSHA256: hexDigest(r.DefinitionSHA256),
	}
}

// prestateFromCatalog turns one catalog reading into the observable half of a prestate.
//
// receiptPresent is supplied by the caller rather than read here, because the two come from
// different places: the object from the catalog, the receipt from the ledger. Folding them
// into one query would make an unreadable ledger look like an absent receipt, which is the
// single most dangerous simplification available in this whole path.
func prestateFromCatalog(row guardCatalogRow, spec guardSpec, receiptPresent bool) prestate {
	canonical, _ := row.matchesCanonical(spec)
	return prestate{
		TargetExists:          row.RelationExists,
		GuardPresent:          row.GuardExists,
		GuardEnableState:      row.EnableState,
		GuardMatchesCanonical: canonical,
		ReceiptPresent:        receiptPresent,
	}
}

// objectProjectionFrom turns one catalog reading into the object projection the matrix and
// the postcondition compare.
func objectProjectionFrom(row guardCatalogRow, spec guardSpec) objectProjection {
	canonical, _ := row.matchesCanonical(spec)
	return objectProjection{
		Readable:         true,
		Exists:           row.RelationExists,
		GuardPresent:     row.GuardExists,
		GuardEnableState: row.EnableState,
		MatchesCanonical: canonical,
	}
}
