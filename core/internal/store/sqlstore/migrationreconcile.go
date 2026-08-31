// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import "fmt"

// This file answers the one question a retrying schema runner cannot get wrong:
// after an interruption whose outcome is unknown, WHAT DID THE UNIT ACTUALLY DO?
//
// The two available guesses are both destructive. "It did not run" re-applies a unit
// that already committed. "It ran" skips a unit that never did. So the answer is not
// guessed: it is derived from four independent readings — what the unit MEANT to do,
// what it SAW before starting, what the ledger says, and what the object looks like —
// and anything that does not fit a named row of the matrix is refused rather than
// resolved.
//
// TOTALITY IS ABOUT THE DOMAIN, NOT THE CONTROL FLOW. An earlier version returned a
// value for every combination of booleans and was therefore "exhaustive" in the way a
// compiler checks. It still blessed a DISABLED guard as a satisfied poststate, an
// empty intent as authorized, and a D -> A change as the sanctioned O -> A rollout.
// Being total over booleans is not being total over meanings.

// receiptProjection is what the receipt ledger says about this unit right now.
type receiptProjection struct {
	// Readable distinguishes "there is no receipt" from "I could not look".
	//
	// It is the first field because collapsing the two is the single most dangerous
	// simplification available here: an unreadable ledger rendered as an absent
	// receipt turns a committed unit into one that is about to be applied twice.
	Readable bool
	Present  bool
	// Epoch is the manifest revision the receipt was written under.
	Epoch int64
	// The rest of the durable binding the receipt RECORDED, compared field by field against
	// the binding the prestate carries. See prestate for why the epoch alone is not enough:
	// two editions may share an epoch and differ in their canonical bytes.
	RolloutID        string
	ManifestFormat   int64
	CodeSHA256       string
	RetainedRevision int64
	RetainedSHA256   string
	SpecSHA256       string
	DefinitionSHA256 string
}

// receiptBindingDifference names the first member of the durable binding in which the
// receipt and the authorizing prestate disagree, or "" when the whole tuple matches.
//
// It returns the FIELD rather than a boolean because the field is the diagnosis. "The
// receipt does not match" sends an operator to compare seven values by hand; "spec_sha256
// differs" says that this receipt was written for a different entry of the same edition.
//
// The comparison is TOLERANT OF AN EMPTY BINDING ON BOTH SIDES, and that is stated rather
// than hidden: a caller that carries no binding at all — a unit test of the matrix, a
// projection from before these fields existed — compares equal, because every member is
// zero on both sides. What it will not do is let a PARTIAL binding pass: a receipt carrying
// a rollout the prestate does not is a difference, in that direction too.
func receiptBindingDifference(pre prestate, r receiptProjection) string {
	for _, f := range []struct {
		what string
		a, b string
	}{
		{"epoch", fmt.Sprint(pre.Epoch), fmt.Sprint(r.Epoch)},
		{"rollout_id", pre.RolloutID, r.RolloutID},
		{"manifest_format", fmt.Sprint(pre.ManifestFormat), fmt.Sprint(r.ManifestFormat)},
		{"code_sha256", pre.CodeSHA256, r.CodeSHA256},
		{"retained_revision", fmt.Sprint(pre.RetainedRevision), fmt.Sprint(r.RetainedRevision)},
		{"retained_sha256", pre.RetainedSHA256, r.RetainedSHA256},
		{"spec_sha256", pre.SpecSHA256, r.SpecSHA256},
		{"definition_sha256", pre.DefinitionSHA256, r.DefinitionSHA256},
	} {
		if f.a != f.b {
			return fmt.Sprintf("%s (authorized %q, receipt %q)", f.what, f.a, f.b)
		}
	}
	return ""
}

// objectProjection is what the target object looks like right now.
type objectProjection struct {
	Readable bool
	Exists   bool
	// GuardPresent is whether the canonical guard object was found at all.
	GuardPresent bool
	// GuardEnableState is the exact tgenabled character ('O', 'A', 'D', 'R'), never
	// normalised to a boolean. "O or A" left unclassified was the defect in the
	// verifier that predates this contract, and it is the difference between evidence
	// that survives logical replication and evidence that is silently overwritten by
	// it.
	GuardEnableState string
	// MatchesCanonical is whether the guard's definition is byte-for-byte the one the
	// manifest declares, not merely a guard with the right name.
	MatchesCanonical bool
}

// The tgenabled characters PostgreSQL stores, spelled once.
//
// D and R are listed because they must be NAMED to be refused. A guard that is
// DISABLED, or restricted to replica-only, is precisely the absence of the protection
// this machinery exists to guarantee — so neither may ever count as a poststate any
// intent reached successfully.
const (
	guardStateOrigin  = "O"
	guardStateAlways  = "A"
	guardStateDisable = "D"
	guardStateReplica = "R"
)

// unitSpec is what the MANIFEST authorizes: the intent, and the exact poststate the
// object must end in.
//
// The poststate is not derivable from the intent, and assuming it was is what made a
// canonical repair whose declared state is ALWAYS resolve as divergent. C4 requires
// each durable entry to carry an exact expected state, and an engine-owned guard
// requires exactly 'A' — so creation and repair both end wherever the manifest says,
// not at a constant this file picked.
type unitSpec struct {
	Intent unitIntent
	// CanonicalEnableState is the tgenabled character the manifest declares for this
	// object: 'O' or 'A', never 'D' or 'R'.
	CanonicalEnableState string
}

// validate refuses a spec that cannot authorize anything.
//
// It runs BEFORE the prestate is projected, let alone before any statement. An
// unrecognized intent used to be caught only inside reconciliation — which is to say,
// only if something else failed first — so a unit with no intent at all could project,
// execute, write a receipt and commit, and be judged afterwards or never.
func (s unitSpec) validate() error {
	switch s.Intent {
	case intentCreateGuard, intentAdoptLegacy, intentTransitionLegacyOToA, intentRepair:
	default:
		return fmt.Errorf("%w: unrecognized intent %q", ErrMigrationUnauthorised, s.Intent)
	}
	switch s.CanonicalEnableState {
	case guardStateOrigin, guardStateAlways:
	default:
		// D and R are named to be refused: a guard that is DISABLED, or restricted to
		// replica-only, is precisely the absence of the protection this machinery
		// exists to guarantee, so no manifest may declare one as canonical.
		return fmt.Errorf("%w: intent %q declares canonical enable state %q, which is not O or A",
			ErrMigrationUnauthorised, s.Intent, s.CanonicalEnableState)
	}
	return nil
}

// guardStateIsCatalogValue reports whether s is a tgenabled character PostgreSQL can
// actually store, or the empty string that means "no guard was found".
func guardStateIsCatalogValue(s string) bool {
	switch s {
	case "", guardStateOrigin, guardStateAlways, guardStateDisable, guardStateReplica:
		return true
	default:
		return false
	}
}

// validate refuses a reading that DESCRIBES A DATABASE THAT CANNOT EXIST.
//
// Every field here is produced by a caller-supplied projection, so the runner's whole
// chain of reasoning rests on catalog readings it did not perform. The authorisation
// rules below interrogate these fields one at a time and none of them cross-checks the
// others, which leaves a projector with a bug — a mis-joined LEFT JOIN, a COALESCE that
// invents a default — able to hand over a combination the catalog could never produce
// and have it authorize a change.
//
// The concrete opening: a projection reporting GuardPresent=false alongside
// GuardEnableState='A'. intentCreateGuard asks only whether a guard is present, so it
// is authorized, and it then commits a CREATE over an object that — by the other half
// of the same reading — already carries an ALWAYS guard. That is exactly the silent
// adoption intentCreateGuard's precondition exists to prevent, walked around by an
// inconsistency inside a single reading.
//
// Four impossibilities, each grounded in the catalog rather than in taste:
//
//   - tgenabled is a single character with four defined values; anything else means the
//     projection is not reading tgenabled.
//   - a trigger cannot exist on a relation that does not, because pg_trigger.tgrelid is
//     a foreign key into pg_class.
//   - a guard that was found has a tgenabled character, and one that was not has no row
//     to read a character from. Present and stateless are not both true of any row.
//   - matching the canonical definition byte-for-byte presupposes something to compare.
func (p prestate) validate() error {
	if !guardStateIsCatalogValue(p.GuardEnableState) {
		return fmt.Errorf("%w: the prestate reports guard state %q, which is not a tgenabled value PostgreSQL stores",
			ErrMigrationUnauthorised, p.GuardEnableState)
	}
	if p.GuardPresent && !p.TargetExists {
		return fmt.Errorf("%w: the prestate reports a guard on a relation it also reports as absent",
			ErrMigrationUnauthorised)
	}
	if p.GuardPresent != (p.GuardEnableState != "") {
		return fmt.Errorf("%w: the prestate reports guard_present=%v with guard state %q; a guard that was found has a tgenabled character and one that was not has no row to read it from",
			ErrMigrationUnauthorised, p.GuardPresent, p.GuardEnableState)
	}
	if p.GuardMatchesCanonical && !p.GuardPresent {
		return fmt.Errorf("%w: the prestate reports the guard as canonical while reporting that there is no guard",
			ErrMigrationUnauthorised)
	}
	return nil
}

// expectedEnableState is the tgenabled character the unit must leave behind, and
// whether the intent is authorized AT ALL given what it found.
//
// It assumes a COHERENT prestate. prestate.validate() is what establishes that, and it
// runs before this on every path that reaches a database — in run(), on both the initial
// projection and the re-projection under the lock, and in reconcileOutcomeFor before the
// matrix consults it.
func expectedEnableState(spec unitSpec, pre prestate) (string, bool) {
	switch spec.Intent {
	case intentCreateGuard:
		// CREATION REQUIRES THAT THERE IS NOTHING TO CREATE. Returning the manifest
		// state unconditionally meant a create-guard aimed at an object that ALREADY
		// carried a guard sailed through its precondition — the intent says "emits a
		// guard on an object that had none", and nothing was checking the second half.
		// Silently re-creating over an existing guard is how an object somebody else
		// installed gets adopted without anyone deciding to.
		if pre.GuardPresent {
			return "", false
		}
		return spec.CanonicalEnableState, true

	case intentRepair:
		// Repair converges an object that EXISTS. There is nothing to converge on a
		// relation that is not there, and treating that as a satisfiable repair would
		// let the sanctioned repair path stand in for creation without the durable
		// capture creation requires.
		if !pre.TargetExists {
			return "", false
		}
		return spec.CanonicalEnableState, true

	case intentAdoptLegacy:
		// Adoption needs something to adopt.
		if !pre.GuardPresent {
			return "", false
		}
		// AND IT MUST BE THE DECLARED OBJECT, not merely an object with the right name.
		//
		// This was missing, and it was an authorisation hole rather than an optimisation.
		// Adoption is what puts an object under management: adopting a LOOKALIKE records a
		// guard the manifest never described as managed evidence, and every later
		// verification then compares that object against a golden it was never built from —
		// so the drift is not detected, it is ratified. Measured on 15.18, the lookalike this
		// closes is not exotic: `BEFORE UPDATE OF one_column OR DELETE` carries the same
		// tgtype=27 as the real guard and lets every other column be updated.
		//
		// The coordinator refuses a lookalike earlier, when it builds the plan. The rule lives
		// here as well because two authorities for one rule is how one of them drifts, and
		// this is the authority the runner itself consults.
		if !pre.GuardMatchesCanonical {
			return "", false
		}
		// Adoption alters nothing, so its poststate is whatever it found — but only if
		// what it found was one of the two states adoption may take under management.
		// A legacy guard sitting at D or R is not adoptable: adopting it would record
		// a disabled guard as managed evidence.
		if pre.GuardEnableState == guardStateOrigin || pre.GuardEnableState == guardStateAlways {
			return pre.GuardEnableState, true
		}
		return "", false

	case intentTransitionLegacyOToA:
		// A transition needs a guard to transition.
		if !pre.GuardPresent {
			return "", false
		}
		// And the same canonicality requirement, for the same reason plus one more: a
		// transition on a lookalike would run its ALTER, fail its postcondition and roll back
		// — wasted work whose diagnosis is "the poststate is wrong" when the truth is that
		// the object was never the right one to begin with.
		if !pre.GuardMatchesCanonical {
			return "", false
		}
		// The PRECONDITION is half of this intent's meaning. Only 'A' was required of
		// the result, so a guard found DISABLED and later seen as ALWAYS resolved as
		// the sanctioned rollout — D -> A and R -> A both blessed as O -> A. The
		// direction is what makes evidence survive logical replication, and it is
		// authorized from ORIGIN only.
		if pre.GuardEnableState != guardStateOrigin {
			return "", false
		}
		return guardStateAlways, true

	default:
		return "", false
	}
}

// satisfies reports whether o is the poststate intent was authorized to reach FROM
// pre.
//
// Split out from the matrix because "what done looks like" is intent-specific and
// precondition-specific, while the matrix below is neither. Mixing them produced the
// earlier verifier's central error: treating a canonical guard as success regardless
// of what the unit set out to do or what it started from.
func (o objectProjection) satisfies(spec unitSpec, pre prestate) bool {
	if !o.Exists || !o.GuardPresent || !o.MatchesCanonical {
		return false
	}
	want, ok := expectedEnableState(spec, pre)
	if !ok {
		return false
	}
	return o.GuardEnableState == want
}

// satisfies reports whether the state observed BEFORE the unit ran already met the
// intent's poststate.
//
// This is what separates "the object is canonical because this unit made it so" from
// "the object was already canonical and this unit never got to write its receipt" —
// two situations with identical object projections and opposite correct answers.
func (p prestate) satisfies(spec unitSpec) bool {
	return objectProjection{
		Readable:         true,
		Exists:           p.TargetExists,
		GuardPresent:     p.GuardPresent,
		GuardEnableState: p.GuardEnableState,
		MatchesCanonical: p.GuardMatchesCanonical,
	}.satisfies(spec, p)
}

// reconcileOutcomeFor is the decision matrix.
//
// The receipt and the object are written in the SAME transaction. That is the
// invariant the whole matrix leans on: any state where one is present without the
// other is not an interruption this runner can have produced, so it is not a state
// this runner may resolve.
func reconcileOutcomeFor(spec unitSpec, pre prestate, r receiptProjection, o objectProjection) reconcileOutcome {
	// FAIL-CLOSED FIRST, on both counts.
	//
	// An unrecognized intent cannot be reasoned about at all, and nothing below is
	// meaningful on a reading that did not happen. "Unknown" is a valid answer that
	// keeps the gate pending with a stable diagnosis.
	if err := spec.validate(); err != nil {
		return outcomeUnknown
	}
	// AND THE PRESTATE, on the same fail-closed footing. Every branch below is a
	// comparison against it, so a reading describing a database that cannot exist would
	// not be caught by any of them — it would simply steer the comparison. Unknown is
	// the honest verdict for a premise this runner cannot trust.
	if err := pre.validate(); err != nil {
		return outcomeUnknown
	}
	if !r.Readable || !o.Readable {
		return outcomeUnknown
	}

	// A RECEIPT THAT WAS THERE AND IS NOT IS ALWAYS DIVERGENT, before any other
	// question. The prestate recorded a durable receipt; receipts are append-only
	// evidence and this runner never deletes one. Its disappearance is exactly the
	// laundering this machinery exists to detect, and every branch below would
	// otherwise have to re-derive that — the "untouched" branch in particular used to
	// authorize a fresh retry over it.
	if pre.ReceiptPresent && !r.Present {
		return outcomeDivergent
	}

	// A PRESTATE THAT DOES NOT Authorize THE INTENT IS DIVERGENT, not untouched.
	//
	// Without this the fall-through reached satisfiesObserved, and an object that was
	// simply unchanged since an unauthorized attempt resolved as not-applied — which
	// invites the runner to try the unauthorized thing again.
	if _, ok := expectedEnableState(spec, pre); !ok {
		return outcomeDivergent
	}

	done := o.satisfies(spec, pre)
	alreadyDone := pre.satisfies(spec)

	switch {
	case r.Present && done:
		// The receipt attributes the unit and the object matches. The one remaining way this
		// is wrong is a receipt written under a different AUTHORISATION, and the epoch is only
		// the first member of that: two editions can share an epoch and differ in their
		// canonical bytes, which is exactly the drift condition the manifest names. So the
		// whole durable binding is compared — rollout, format, code and retained pairs, spec
		// and definition — and any difference is divergent rather than applied.
		if receiptBindingDifference(pre, r) != "" {
			return outcomeDivergent
		}
		return outcomeApplied

	case r.Present && !done:
		// A receipt for work that is not there. Since both are written together, this
		// cannot be a partial application — it is a receipt whose object was removed
		// or altered afterwards, which is exactly what this machinery exists to
		// detect rather than repair on the fly.
		return outcomeDivergent

	case !r.Present && done:
		// The object is in the intent's poststate with no receipt. Whether that is
		// benign depends on WHEN it got there, and only the prestate knows.
		if !alreadyDone {
			// It moved into the poststate during this unit, and the receipt commits
			// with it. Something outside this runner changed the object.
			return outcomeDivergent
		}
		// It was already there before the unit ran. That is the ordinary entry point
		// of ADOPTION on an install predating the ledger — and only of adoption. For
		// create-guard it means the guard this unit was told to create already
		// existed, which is a contradicted precondition rather than a fresh start; for
		// the O -> A transition, expectedEnableState has already refused any prestate
		// other than 'O', so reaching here means the object was at 'A' when the
		// prestate said 'O'.
		if spec.Intent != intentAdoptLegacy {
			return outcomeDivergent
		}
		return outcomeNotApplied

	default: // !r.Present && !done
		if pre.satisfiesObserved(o) {
			// Untouched: the object still looks exactly as it did before the attempt,
			// and the ledger agrees (the disappearance case was refused above).
			return outcomeNotApplied
		}
		// Neither applied nor untouched — the object moved, but not to the poststate
		// the intent authorizes.
		return outcomeDivergent
	}
}

// satisfiesObserved reports whether o is still the state p recorded.
//
// Equality on the OBSERVABLE fields, deliberately: a unit that failed before
// committing must leave the object bit-identical to its prestate, so any difference
// at all is evidence that something happened which this runner cannot account for.
func (p prestate) satisfiesObserved(o objectProjection) bool {
	return o.Exists == p.TargetExists &&
		o.GuardPresent == p.GuardPresent &&
		o.GuardEnableState == p.GuardEnableState &&
		o.MatchesCanonical == p.GuardMatchesCanonical
}
