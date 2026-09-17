// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"fmt"

	"github.com/olivaresai/olivares/core/dr/opgate"
)

// CustodyRequirement is the CLOSED answer the boot admission gives about what
// custody this destination's control demands, taken ONCE at acquisition and
// immutable for the whole admission.
//
// # Why it is closed and why it is frozen
//
// It is the input the composition root uses to decide whether the three signing-key
// loaders run in their ordinary mint-on-absent mode or in the strict enrolled mode,
// and that decision has to be made BEFORE the first key is loaded. A requirement
// that could be re-read later would let a control installed mid-boot change the
// rules after keys were already minted; one a caller could construct would be a
// field that turns a completed destination back into a legacy one.
//
// So every field is unexported, the only producer is this package's acquisition
// path, and the accessors return copies. There is deliberately no setter, no
// "skip" and no way to widen `required` from false to true or narrow it back.
//
// The two possible answers are exactly the ratified ones:
//
//   - required == false: `legacy_or_lost_unknown` with no surviving witness. The
//     ordinary boot continues with every guard it already had, and mints on a data
//     directory that has no keys — the genuine unenrolled first boot.
//   - required == true: a COMPLETE control exists for this destination, so the boot
//     must load the exact custody it published and prove it. Every other verdict
//     (pending, indeterminate, quarantined, malformed, unreadable, and absent with a
//     bound witness) never reaches here: it refuses during acquisition.
type CustodyRequirement struct {
	required bool
	// keysetSHA256 is the canonical digest the completed control authorized. It is
	// the one value that always exists when required is true: a PostgreSQL control
	// carries the digest alone, and a node restored onto a fresh host may hold no
	// local record at all.
	keysetSHA256 string
	// keys is the expected per-purpose selection, present only where LOCAL completed
	// evidence exists (the record carries the full fingerprint set; the database
	// column carries the digest). It is never synthesized from the digest.
	keys []opgate.KeyFingerprint
	// opID and planSHA256 bind the requirement to the operation and plan that
	// published it, so a control replaced under the boot is a refusal rather than a
	// silently different expectation.
	opID        string
	planSHA256  string
	destination string
}

// CustodyRequired reports whether this admission demands a proven completed custody.
func (r CustodyRequirement) CustodyRequired() bool { return r.required }

// KeysetSHA256 is the canonical digest the completed control authorized, or "".
func (r CustodyRequirement) KeysetSHA256() string { return r.keysetSHA256 }

// OperationID is the completed operation this requirement is bound to, or "".
func (r CustodyRequirement) OperationID() string { return r.opID }

// PlanSHA256 is the plan digest this requirement is bound to, or "".
func (r CustodyRequirement) PlanSHA256() string { return r.planSHA256 }

// Destination names the destination this requirement is about, for diagnostics only.
func (r CustodyRequirement) Destination() string { return r.destination }

// ExpectedKeys returns a COPY of the expected per-purpose selection, which is empty
// when only database evidence exists. A caller may use it to explain a refusal; it
// is never the measurement, and nothing here carries key bytes.
func (r CustodyRequirement) ExpectedKeys() []opgate.KeyFingerprint {
	if len(r.keys) == 0 {
		return nil
	}
	out := make([]opgate.KeyFingerprint, len(r.keys))
	copy(out, r.keys)
	return out
}

// CustodyObservation is the typed measurement of the signers this boot ACTUALLY
// loaded, and it is a different kind of thing from the requirement above.
//
// The requirement is durable metadata read from a control. This is derived from the
// key objects the process is about to sign with, and the two are compared. The
// defect that makes the separation load-bearing (F3-IR-5) was a comparison of the
// expected record against itself: a boot read the digest a control recorded, copied
// it into the value it called "observed", and compared them — which passes for every
// input, including a node that just minted three brand-new keys.
//
// So there is NO constructor that accepts a digest. The only producer takes the
// per-purpose selection and recomputes the canonical commitment from the public
// fingerprints through opgate's one encoder. A caller cannot hand a measurement in.
//
// It also carries no key bytes, no path, no DSN and no envelope: purpose, custody
// source and the SHA-256 of the raw public key, which is exactly what a refusal may
// be allowed to say out loud.
type CustodyObservation struct {
	present bool
	typed   opgate.SelectedKeyset
}

// NewCustodyObservation builds the immutable observation from the selection that was
// actually loaded. It is METADATA ONLY — this package never sees a private key, an
// envelope or a KMS client, and deliberately imports no loader to get one.
//
// The digest is recomputed here; the caller supplies purpose, source and public
// fingerprint and nothing else. An incomplete, duplicated, mis-ordered or
// unsupported-source selection is an error rather than a partial observation.
func NewCustodyObservation(keys []opgate.SelectedKey) (CustodyObservation, error) {
	wire, err := opgate.NewKeyset(keys)
	if err != nil {
		return CustodyObservation{}, err
	}
	typed, err := wire.Typed()
	if err != nil {
		return CustodyObservation{}, err
	}
	return CustodyObservation{present: true, typed: typed}, nil
}

// Present reports whether an actual selection was observed at all. The zero value is
// the absence a direct Open supplies, and absence never satisfies a requirement.
func (o CustodyObservation) Present() bool { return o.present }

// KeysetSHA256 is the canonical digest of the OBSERVED selection, or "".
func (o CustodyObservation) KeysetSHA256() string {
	if !o.present {
		return ""
	}
	return o.typed.Digest().String()
}

// Keys returns the observed per-purpose selection as wire fingerprints, for
// diagnostics and for the caller's own recheck. No key bytes.
func (o CustodyObservation) Keys() []opgate.KeyFingerprint {
	if !o.present {
		return nil
	}
	out := make([]opgate.KeyFingerprint, 0, 3)
	for _, k := range o.typed.Keys() {
		out = append(out, opgate.KeyFingerprint{
			Purpose: string(k.Purpose), Source: string(k.Source), PublicSHA256: k.PublicSHA256.String(),
		})
	}
	return out
}

// judgeObservedCustody spends the comparison the whole sublot exists to make: the
// custody a completed control authorized against the custody this boot loaded.
//
// It is called at the LAST readiness boundary, on the retained session, with a
// requirement frozen at acquisition and an observation built from the actual signers.
// Neither side can be derived from the other.
func judgeObservedCustody(req CustodyRequirement, observed CustodyObservation) error {
	if !req.required {
		// Nothing is enrolled here. An observation may still have been built — an
		// ordinary boot always measures what it loaded — and it authorizes nothing:
		// there is no completed control for it to satisfy, and it is not written
		// anywhere as a retrospective enrolment.
		return nil
	}
	if !observed.present {
		return fmt.Errorf(
			"%w: %s carries a COMPLETED restore control (operation %s) and this call supplied no observation of the custody it actually loaded, so the one comparison that control exists for cannot be made; open this destination through the boot publication admission, which loads the enrolled custody and measures it",
			ErrRestorePublicationFenced, req.destination, req.opID)
	}
	if observed.KeysetSHA256() != req.keysetSHA256 {
		// The per-purpose difference is reported where local evidence names the
		// expected selection, because "your catalog key is from the wrong source" is
		// an operator instruction and "two digests differ" is not.
		if why := explainKeysetDifference(req.ExpectedKeys(), observed.Keys()); why != "" {
			return fmt.Errorf(
				"%w: %s completed under custody %s and this boot loaded %s — %s; the signers this process would serve with are not the ones the completed operation published",
				ErrRestorePublicationFenced, req.destination, req.keysetSHA256, observed.KeysetSHA256(), why)
		}
		return fmt.Errorf(
			"%w: %s completed under custody %s and this boot loaded %s; the signers this process would serve with are not the ones the completed operation published",
			ErrRestorePublicationFenced, req.destination, req.keysetSHA256, observed.KeysetSHA256())
	}
	return nil
}

// explainKeysetDifference names the first purpose whose selection differs, and
// whether it differs in SOURCE or in the key itself. Both are refusals; they are
// different operator problems, and a digest comparison alone tells neither.
func explainKeysetDifference(expected, observed []opgate.KeyFingerprint) string {
	if len(expected) != len(observed) {
		return ""
	}
	for i := range expected {
		if expected[i].Purpose != observed[i].Purpose {
			return fmt.Sprintf("the %s selection is reported where %s was authorized", observed[i].Purpose, expected[i].Purpose)
		}
		if expected[i].PublicSHA256 != observed[i].PublicSHA256 {
			return fmt.Sprintf("the %s key loaded here is %s and the authorized one is %s",
				expected[i].Purpose, observed[i].PublicSHA256, expected[i].PublicSHA256)
		}
		if expected[i].Source != observed[i].Source {
			return fmt.Sprintf("the %s key is the authorized one but it resolved from %q and the completed operation authorized %q",
				expected[i].Purpose, observed[i].Source, expected[i].Source)
		}
	}
	return ""
}

// completedCustodyRequirement freezes the requirement a COMPLETE control produces.
//
// The digest is the DATABASE's where a database control exists, and the local
// record's where it is the only evidence (SQLite, whose control cannot live inside
// the file a restore replaces). The expected per-purpose set comes from local
// evidence alone and is dropped when the two disagree — the disagreement itself has
// already refused in judgeDRRestoreGate, and a requirement is never assembled from
// halves of two different controls.
func completedCustodyRequirement(destination, opID, plan, keysetSHA256 string, local opgate.Keyset) CustodyRequirement {
	req := CustodyRequirement{
		required:     true,
		keysetSHA256: keysetSHA256,
		opID:         opID,
		planSHA256:   plan,
		destination:  destination,
	}
	if local.SHA256 != "" && local.SHA256 == keysetSHA256 && len(local.Keys) == 3 {
		req.keys = append(req.keys, local.Keys...)
	}
	return req
}
