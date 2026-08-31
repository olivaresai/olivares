// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"errors"
	"math"
	"testing"
)

// canonicalObject is the poststate every intent except the O -> A transition is
// trying to reach.
func canonicalObject(state string) objectProjection {
	return objectProjection{
		Readable: true, Exists: true, GuardPresent: true,
		GuardEnableState: state, MatchesCanonical: true,
	}
}

// TestReconcileOutcomeMatrix pins the decision an interrupted unit is resolved by.
//
// Every row is a state a real interruption can leave behind, and each one names the
// cost of the wrong answer. Two of them — a receipt without its object, and an object
// without its receipt — are impossible for THIS runner to produce, because both are
// written in one transaction. That is exactly why they must be divergent rather than
// resolved: they are evidence of something outside the runner, and "resolve it
// helpfully" is how such evidence gets laundered.
func TestReconcileOutcomeMatrix(t *testing.T) {
	t.Parallel()

	freshInstall := prestate{Epoch: 7}
	adoptable := prestate{
		TargetExists: true, GuardPresent: true, GuardEnableState: "O",
		GuardMatchesCanonical: true, Epoch: 7,
	}
	created := prestate{TargetExists: true, Epoch: 7}

	cases := []struct {
		name string
		// canonical defaults to "O" when empty, which is what every pre-existing row
		// assumed before the manifest's declared poststate became explicit.
		intent    unitIntent
		canonical string
		pre       prestate
		r         receiptProjection
		o         objectProjection
		want      reconcileOutcome
		why       string
	}{
		{
			name: "receipt and canonical object under create", intent: intentCreateGuard,
			pre:  created,
			r:    receiptProjection{Readable: true, Present: true, Epoch: 7},
			o:    canonicalObject("O"),
			want: outcomeApplied,
			why:  "both halves of the one transaction are there; retrying would apply it twice",
		},
		{
			name: "a receipt from ANOTHER epoch does not ratify this unit", intent: intentCreateGuard,
			pre:  created,
			r:    receiptProjection{Readable: true, Present: true, Epoch: 6},
			o:    canonicalObject("O"),
			want: outcomeDivergent,
			why:  "one manifest revision's approval must not silently authorize another's change",
		},
		{
			name: "receipt without its object", intent: intentCreateGuard,
			pre:  created,
			r:    receiptProjection{Readable: true, Present: true, Epoch: 7},
			o:    objectProjection{Readable: true, Exists: true},
			want: outcomeDivergent,
			why:  "they commit together, so this is a receipt whose object was removed afterwards — the thing this machinery exists to detect",
		},
		{
			name: "untouched object with no receipt", intent: intentCreateGuard,
			pre:  created,
			r:    receiptProjection{Readable: true},
			o:    objectProjection{Readable: true, Exists: true},
			want: outcomeNotApplied,
			why:  "nothing of the unit survived and the object is bit-identical to its prestate",
		},
		{
			name: "canonical object with no receipt, and it was NOT canonical before", intent: intentCreateGuard,
			pre:  created,
			r:    receiptProjection{Readable: true},
			o:    canonicalObject("O"),
			want: outcomeDivergent,
			why:  "the object reached the poststate without the receipt it commits with, so something outside this runner changed it",
		},
		{
			name: "canonical object with no receipt, and it ALREADY was canonical", intent: intentAdoptLegacy,
			pre:  adoptable,
			r:    receiptProjection{Readable: true},
			o:    canonicalObject("O"),
			want: outcomeNotApplied,
			why:  "this is the ordinary entry point of adoption on an install that predates the ledger; calling it divergent would refuse to boot a perfectly healthy database",
		},
		{
			name: "the O -> A transition is not done while the guard is still at O", intent: intentTransitionLegacyOToA,
			pre:  adoptable,
			r:    receiptProjection{Readable: true},
			o:    canonicalObject("O"),
			want: outcomeNotApplied,
			why:  "a canonical guard at O is precisely the state this intent exists to leave behind, so 'canonical' must not count as done",
		},
		{
			name: "the O -> A transition applied", intent: intentTransitionLegacyOToA,
			pre:  adoptable,
			r:    receiptProjection{Readable: true, Present: true, Epoch: 7},
			o:    canonicalObject("A"),
			want: outcomeApplied,
			why:  "under logical replication the difference between O and A is whether evidence can be silently overwritten",
		},
		{
			name: "the object moved somewhere the intent does not authorize", intent: intentTransitionLegacyOToA,
			pre:  adoptable,
			r:    receiptProjection{Readable: true},
			o:    canonicalObject("D"),
			want: outcomeDivergent,
			why:  "neither applied nor untouched: the guard was DISABLED, which no intent here authorizes",
		},
		{
			name: "a DISABLED guard is not a poststate any intent reached", intent: intentCreateGuard,
			pre:  created,
			r:    receiptProjection{Readable: true, Present: true, Epoch: 7},
			o:    canonicalObject(guardStateDisable),
			want: outcomeDivergent,
			why:  "a disabled guard is precisely the absence of the protection this machinery exists to guarantee; calling it applied records the hole as covered",
		},
		{
			name: "a REPLICA-ONLY guard is not a poststate either", intent: intentAdoptLegacy,
			pre:  prestate{TargetExists: true, GuardPresent: true, GuardEnableState: guardStateReplica, GuardMatchesCanonical: true, Epoch: 7},
			r:    receiptProjection{Readable: true, Present: true, Epoch: 7},
			o:    canonicalObject(guardStateReplica),
			want: outcomeDivergent,
			why:  "adoption may take a guard under management only from O or A; adopting one at R would record a guard that does not fire on the primary as managed evidence",
		},
		{
			name: "D -> A is not the authorized O -> A transition", intent: intentTransitionLegacyOToA,
			pre:  prestate{TargetExists: true, GuardPresent: true, GuardEnableState: guardStateDisable, GuardMatchesCanonical: true, Epoch: 7},
			r:    receiptProjection{Readable: true, Present: true, Epoch: 7},
			o:    canonicalObject(guardStateAlways),
			want: outcomeDivergent,
			why:  "only the RESULT was being checked, so a guard found disabled and later seen as always was blessed as the sanctioned rollout; the precondition is half the intent's meaning",
		},
		{
			name: "R -> A is not it either", intent: intentTransitionLegacyOToA,
			pre:  prestate{TargetExists: true, GuardPresent: true, GuardEnableState: guardStateReplica, GuardMatchesCanonical: true, Epoch: 7},
			r:    receiptProjection{Readable: true, Present: true, Epoch: 7},
			o:    canonicalObject(guardStateAlways),
			want: outcomeDivergent,
			why:  "same hole, other starting state",
		},
		{
			name: "an unrecognized intent is refused, not defaulted", intent: unitIntent(""),
			pre:  created,
			r:    receiptProjection{Readable: true, Present: true, Epoch: 7},
			o:    canonicalObject(guardStateOrigin),
			want: outcomeUnknown,
			why:  "the zero value used to flow through to 'canonical means done', so a unit with no intent at all resolved as applied; an unknown intent is a caller bug, not a default to pick sensibly",
		},
		{
			name: "a REPAIR whose manifest poststate is ALWAYS", intent: intentRepair, canonical: guardStateAlways,
			pre: prestate{TargetExists: true, GuardPresent: true, GuardEnableState: guardStateOrigin,
				GuardMatchesCanonical: true, Epoch: 7},
			r:    receiptProjection{Readable: true, Present: true, Epoch: 7},
			o:    canonicalObject(guardStateAlways),
			want: outcomeApplied,
			why:  "the poststate is not a function of the intent: an engine-owned guard is canonical at A, and hard-coding O made a correct repair resolve as divergent",
		},
		{
			name: "a repair that stops at O when the manifest says ALWAYS", intent: intentRepair, canonical: guardStateAlways,
			pre: prestate{TargetExists: true, GuardPresent: true, GuardEnableState: guardStateOrigin,
				GuardMatchesCanonical: true, Epoch: 7},
			r:    receiptProjection{Readable: true, Present: true, Epoch: 7},
			o:    canonicalObject(guardStateOrigin),
			want: outcomeDivergent,
			why:  "under logical replication O is exactly the state that lets a publisher UPDATE overwrite the evidence with zero errors",
		},
		{
			name: "a manifest may not declare DISABLED as canonical", intent: intentCreateGuard, canonical: guardStateDisable,
			pre:  created,
			r:    receiptProjection{Readable: true, Present: true, Epoch: 7},
			o:    canonicalObject(guardStateDisable),
			want: outcomeUnknown,
			why:  "a spec that authorizes a disabled guard authorizes the absence of the protection, so it is refused before any state is judged",
		},
		{
			name: "a receipt that WAS there and is gone is always divergent", intent: intentAdoptLegacy,
			pre: prestate{TargetExists: true, GuardPresent: true, GuardEnableState: guardStateOrigin,
				GuardMatchesCanonical: true, ReceiptPresent: true, Epoch: 7},
			r:    receiptProjection{Readable: true},
			o:    canonicalObject(guardStateOrigin),
			want: outcomeDivergent,
			why:  "receipts are append-only evidence and this runner never deletes one; resolving its disappearance as not-applied authorizes a retry over exactly the laundering this machinery detects",
		},
		{
			name: "create-guard on an object that ALREADY had the guard", intent: intentCreateGuard,
			pre: prestate{TargetExists: true, GuardPresent: true, GuardEnableState: guardStateOrigin,
				GuardMatchesCanonical: true, Epoch: 7},
			r:    receiptProjection{Readable: true},
			o:    canonicalObject(guardStateOrigin),
			want: outcomeDivergent,
			why:  "the guard this unit was told to CREATE already existed before it ran; that is a contradicted precondition, and only adoption may resolve 'it was already there' as a fresh start",
		},
		{
			// THE DISCRIMINATING ROW. With a receipt present and the object canonical,
			// every other branch says "applied" — so this is the only shape where the
			// answer depends on the PRECONDITION and nothing else. The first version of
			// this row used an absent receipt and came out divergent through the
			// "already done" branch, which made it pass with the precondition removed.
			name: "create-guard that RECEIPTED over a guard that already existed", intent: intentCreateGuard,
			pre: prestate{TargetExists: true, GuardPresent: true, GuardEnableState: guardStateOrigin,
				GuardMatchesCanonical: true, Epoch: 7},
			r:    receiptProjection{Readable: true, Present: true, Epoch: 7},
			o:    canonicalObject(guardStateOrigin),
			want: outcomeDivergent,
			why:  "the intent is 'emits a guard on an object that had none', and nothing was checking the second half; a receipt claiming creation over a guard that was already there records somebody else's object as ours",
		},
		{
			name: "repair aimed at a relation that does not exist", intent: intentRepair,
			pre:  prestate{Epoch: 7},
			r:    receiptProjection{Readable: true},
			o:    objectProjection{Readable: true},
			want: outcomeDivergent,
			why:  "there is nothing to converge, and letting repair stand in for creation skips the durable capture creation requires",
		},
		{
			name: "adoption with no guard to adopt", intent: intentAdoptLegacy,
			pre:  prestate{TargetExists: true, Epoch: 7},
			r:    receiptProjection{Readable: true},
			o:    objectProjection{Readable: true, Exists: true},
			want: outcomeDivergent,
			why:  "adoption takes an EXISTING canonical guard under management; with none present there is nothing it could be adopting",
		},
		{
			name: "an unreadable ledger is never an absent receipt", intent: intentCreateGuard,
			pre:  created,
			r:    receiptProjection{},
			o:    canonicalObject("O"),
			want: outcomeUnknown,
			why:  "collapsing 'I could not look' into 'there is none' turns a committed unit into one about to be applied twice",
		},
		{
			name: "an unreadable object is never an absent object", intent: intentCreateGuard,
			pre:  created,
			r:    receiptProjection{Readable: true, Present: true, Epoch: 7},
			o:    objectProjection{},
			want: outcomeUnknown,
			why:  "fail closed: the gate stays pending with a stable diagnosis rather than resolving on a reading that did not happen",
		},
		{
			name: "a fresh install with nothing at all", intent: intentCreateGuard,
			pre:  freshInstall,
			r:    receiptProjection{Readable: true},
			o:    objectProjection{Readable: true},
			want: outcomeNotApplied,
			why:  "the target does not exist yet and never did; the unit simply has not run",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			canonical := tc.canonical
			if canonical == "" {
				canonical = guardStateOrigin
			}
			spec := unitSpec{Intent: tc.intent, CanonicalEnableState: canonical}
			if got := reconcileOutcomeFor(spec, tc.pre, tc.r, tc.o); got != tc.want {
				t.Errorf("reconciled as %q, want %q — %s", got, tc.want, tc.why)
			}
		})
	}
}

// TestReconcileNeverResolvesAnUnreadableProjection is the fail-closed property
// stated once, separately from the table, because it is the one that must survive
// every future row added to it.
//
// Mutation that must turn this red: default an unreadable projection to absent.
func TestReconcileNeverResolvesAnUnreadableProjection(t *testing.T) {
	t.Parallel()
	intents := []unitIntent{intentCreateGuard, intentAdoptLegacy, intentTransitionLegacyOToA, intentRepair}
	for _, in := range intents {
		spec := unitSpec{Intent: in, CanonicalEnableState: guardStateOrigin}
		for _, pair := range []struct {
			r receiptProjection
			o objectProjection
		}{
			{receiptProjection{}, canonicalObject("A")},
			{receiptProjection{Readable: true, Present: true}, objectProjection{}},
			{receiptProjection{}, objectProjection{}},
		} {
			if got := reconcileOutcomeFor(spec, prestate{}, pair.r, pair.o); got != outcomeUnknown {
				t.Errorf("intent %s with an unreadable projection resolved as %q, want %q: every other outcome is an assertion about state nobody observed",
					in, got, outcomeUnknown)
			}
		}
	}
}

// coherentPrestates enumerates the ten coherent OBJECT-STATE CLASSES, and the precision
// of that phrase is the correction.
//
// `prestate` has six input fields. This crosses FOUR of them — the ones describing the
// target object — and holds ReceiptPresent and Epoch fixed. So it is not, and must not be
// described as, "every prestate the catalog can produce": crossing the omitted boolean
// alone would give twenty, and Epoch is an int64.
//
// Fixing them is legitimate only because expectedEnableState is independently shown not
// to read them, which is what TestExpectedEnableStateIgnoresTheLedgerFields exists for.
// Without that proof this helper could not detect the rule starting to depend on either,
// and the word "exhaustive" would be doing work it had not earned — the same shape as
// the eight accidental passes this branch has already found.
//
// Built by cross product and filtered through prestate.validate() rather than listed by
// hand, then ASSERTING the size: a hand-written list drifts silently when the domain
// changes, while this breaks the count instead.
func coherentPrestates(t *testing.T) []prestate {
	t.Helper()
	var out []prestate
	incoherent := 0
	for _, exists := range []bool{false, true} {
		for _, present := range []bool{false, true} {
			for _, state := range []string{"", guardStateOrigin, guardStateAlways, guardStateDisable, guardStateReplica} {
				for _, canonical := range []bool{false, true} {
					p := prestate{
						TargetExists: exists, GuardPresent: present,
						GuardEnableState: state, GuardMatchesCanonical: canonical,
						Epoch: 7,
					}
					if err := p.validate(); err != nil {
						incoherent++
						continue
					}
					out = append(out, p)
				}
			}
		}
	}
	// 2 with no guard (absent or present relation) + 8 with a guard (4 tgenabled
	// characters x canonical-or-not, all on a relation that exists). Anything else means
	// validate() admits a database PostgreSQL cannot hold, or refuses one it can.
	if len(out) != 10 {
		t.Fatalf("the coherent prestate domain has %d members, want 10 (%d rejected as incoherent)", len(out), incoherent)
	}
	return out
}

// TestExpectedEnableStateOverTheWholeProduct is the exhaustive pass the fifth round
// asked for: every intent, against every canonical poststate a manifest may declare,
// against every coherent OBJECT-STATE class — see coherentPrestates for exactly what
// that phrase covers, and TestExpectedEnableStateIgnoresTheLedgerFields for why holding
// the other two fields fixed does not weaken the claim.
//
// It deliberately does NOT re-implement expectedEnableState and compare. That test
// passes whenever the two copies agree, including when they agree on being wrong — and
// the defect this file exists to prevent was precisely a rule that looked right in
// isolation. What is asserted instead are properties stated in the CONTRACT's own terms,
// plus the exact count of authorized combinations per intent. A count is the one
// assertion that cannot be satisfied by accident: changing any single cell moves it.
//
// Mutations that must turn this red, each for its own reason:
//   - let intentTransitionLegacyOToA authorize from 'A' as well as 'O'  -> count 2 -> 4
//   - let intentAdoptLegacy return the canonical state instead of what it found
//     -> the invariance property, not the count
//   - let intentCreateGuard proceed when a guard is already present     -> count 2 -> 10
//   - let any intent return 'D'                                         -> range property
func TestExpectedEnableStateOverTheWholeProduct(t *testing.T) {
	t.Parallel()

	pres := coherentPrestates(t)
	canonicals := []string{guardStateOrigin, guardStateAlways}

	// The authorized count per intent, for ONE canonical declaration. Derived from the
	// contract, not from the code:
	//
	//   create-guard  requires no guard at all                        -> the 2 guardless prestates
	//   repair        requires the relation to exist                  -> 9 of the 10
	//   adopt-legacy  requires a CANONICAL guard at 'O' or 'A'        -> 2 of the 8 guarded
	//   transition    requires a CANONICAL guard at 'O' exactly       -> 1 of the 8 guarded
	//
	// THE ADOPTION AND TRANSITION COUNTS HALVED, and that is a deliberate authorisation
	// change rather than a drift in the test. C4 requires both to demand
	// GuardMatchesCanonical: adoption is what puts an object under management, so adopting a
	// LOOKALIKE would record an object the manifest never described as managed evidence, and
	// every later verification would then compare it against a golden it was never built
	// from — ratifying the drift instead of detecting it. The lookalike is not exotic:
	// measured on 15.18, `BEFORE UPDATE OF one_column OR DELETE` carries the same tgtype=27
	// as the real guard and leaves every other column mutable.
	wantAuthorised := map[unitIntent]int{
		intentCreateGuard:          2,
		intentRepair:               9,
		intentAdoptLegacy:          2,
		intentTransitionLegacyOToA: 1,
	}

	for intent, want := range wantAuthorised {
		for _, canonical := range canonicals {
			spec := unitSpec{Intent: intent, CanonicalEnableState: canonical}
			if err := spec.validate(); err != nil {
				t.Fatalf("spec %s/%s should be valid: %v", intent, canonical, err)
			}
			got := 0
			for _, pre := range pres {
				state, ok := expectedEnableState(spec, pre)
				if !ok {
					// A refusal must be total: no state leaks out of an unauthorized
					// combination for a caller to use by mistake.
					if state != "" {
						t.Errorf("intent %s canonical %s refused %+v but still returned state %q",
							intent, canonical, pre, state)
					}
					continue
				}
				got++
				// THE POSTSTATE IS NEVER 'D' OR 'R'. A disabled guard, or one restricted
				// to replica-only, is the absence of the protection this machinery exists
				// to provide — so no intent may ever declare one as the state it is
				// working towards.
				if state != guardStateOrigin && state != guardStateAlways {
					t.Errorf("intent %s canonical %s authorized %+v with poststate %q, which is not O or A",
						intent, canonical, pre, state)
				}
			}
			if got != want {
				t.Errorf("intent %s with canonical %s authorized %d of %d coherent prestates, want %d",
					intent, canonical, got, len(pres), want)
			}
		}
	}
}

// TestExpectedEnableStateInvariance pins WHICH INPUTS EACH INTENT IS ALLOWED TO READ.
//
// The counts above would still pass if an intent quietly started depending on a field
// it has no business reading — adoption returning the manifest's canonical state rather
// than the state it found, say, which changes no count at all but converts "take this
// guard under management unchanged" into "declare it to be whatever we wanted".
//
// Two invariances, both contractual:
//
//   - adoption and the O -> A transition IGNORE CanonicalEnableState. Adoption's
//     poststate is what it found; the transition's is 'A' by definition. Neither is the
//     manifest's to choose, and D2's logical-replication certification is why: 'A' is
//     load-bearing, so a manifest able to redefine the transition's target could turn
//     the rollout into a no-op that still reports success.
//   - adoption and the transition DO read GuardMatchesCanonical, and create-guard and
//     repair do NOT.
//
// THE SECOND PROPERTY IS THE REVERSE OF WHAT THIS TEST ASSERTED BEFORE, and the inversion is
// the point rather than an accident. It used to claim that NO intent reads
// GuardMatchesCanonical for authorisation, on the argument that canonicality decides the
// POSTSTATE in objectProjection.satisfies and deciding it twice invites the two answers to
// drift. That argument was wrong about which question was being asked. Whether the object is
// the declared one is a PRECONDITION of putting it under management, not a property of the
// result: adopting a lookalike records an object the manifest never described as managed
// evidence, and from then on every verification compares it against a golden it was never
// built from — so the drift is ratified rather than detected. C4 therefore requires the
// check, and this test now pins that it is there.
//
// create-guard and repair still ignore it, for reasons that have not changed: creation
// requires that there be NOTHING there, so there is no object to be canonical; repair exists
// precisely to converge an object that is NOT canonical, and demanding canonicality of its
// input would make it unable to do the one thing it is for.
func TestExpectedEnableStateInvariance(t *testing.T) {
	t.Parallel()

	pres := coherentPrestates(t)

	for _, intent := range []unitIntent{intentAdoptLegacy, intentTransitionLegacyOToA} {
		for _, pre := range pres {
			gotO, okO := expectedEnableState(unitSpec{Intent: intent, CanonicalEnableState: guardStateOrigin}, pre)
			gotA, okA := expectedEnableState(unitSpec{Intent: intent, CanonicalEnableState: guardStateAlways}, pre)
			if okO != okA || gotO != gotA {
				t.Errorf("intent %s read the manifest's canonical state: %+v gave (%q,%v) under O and (%q,%v) under A",
					intent, pre, gotO, okO, gotA, okA)
			}
		}
	}

	// Flipping only GuardMatchesCanonical: it must MATTER for the two intents that put an
	// object under management, and must not for the two that do not.
	readsCanonical := map[unitIntent]bool{
		intentAdoptLegacy:          true,
		intentTransitionLegacyOToA: true,
		intentCreateGuard:          false,
		intentRepair:               false,
	}
	for intent, shouldRead := range readsCanonical {
		for _, canonical := range []string{guardStateOrigin, guardStateAlways} {
			spec := unitSpec{Intent: intent, CanonicalEnableState: canonical}
			// Counted rather than asserted per prestate: for the intents that DO read the
			// field, only the prestates that are otherwise authorized can show the difference,
			// so "at least one pair differs" is the property, and "no pair differs" is the
			// property for the others.
			differed := 0
			pairs := 0
			for _, pre := range pres {
				if !pre.GuardPresent {
					// The flip would make the prestate incoherent, so there is no pair.
					continue
				}
				pairs++
				flipped := pre
				flipped.GuardMatchesCanonical = !pre.GuardMatchesCanonical
				g1, ok1 := expectedEnableState(spec, pre)
				g2, ok2 := expectedEnableState(spec, flipped)
				if ok1 != ok2 || g1 != g2 {
					differed++
					if !shouldRead {
						t.Errorf("intent %s/%s must NOT read GuardMatchesCanonical, but %+v gave (%q,%v) and its flip gave (%q,%v)",
							intent, canonical, pre, g1, ok1, g2, ok2)
					}
				}
			}
			if pairs == 0 {
				t.Fatalf("intent %s/%s: no guarded prestate to flip, so this asserts nothing", intent, canonical)
			}
			if shouldRead && differed == 0 {
				t.Errorf("intent %s/%s ignores GuardMatchesCanonical across all %d guarded prestates; adopting or promoting a lookalike would be authorized",
					intent, canonical, pairs)
			}
		}
	}
}

// TestExpectedEnableStateNeverAuthorisesFromADisabledGuard is the security property
// stated on its own, because it is the one an operator's evidence depends on.
//
// A guard sitting at 'D' or 'R' is a table whose append-only protection is switched
// off. Adoption must not take it under management (that would record an unprotected
// table as managed evidence) and the O -> A transition must not claim it as the
// sanctioned rollout (D -> A and O -> A are indistinguishable afterwards, and only one
// of them is authorized).
//
// REPAIR IS THE DELIBERATE EXCEPTION, and naming it here is the point: converging a
// broken guard back to canonical is exactly what repair is for. It is gated elsewhere —
// reachable only from the sanctioned repair path, after the durable capture that records
// what is about to be overwritten — never from boot.
func TestExpectedEnableStateNeverAuthorisesFromADisabledGuard(t *testing.T) {
	t.Parallel()

	for _, state := range []string{guardStateDisable, guardStateReplica} {
		pre := prestate{
			TargetExists: true, GuardPresent: true, GuardEnableState: state,
			GuardMatchesCanonical: true, Epoch: 7,
		}
		if err := pre.validate(); err != nil {
			t.Fatalf("prestate with a %q guard should be coherent: %v", state, err)
		}
		for _, intent := range []unitIntent{intentCreateGuard, intentAdoptLegacy, intentTransitionLegacyOToA} {
			for _, canonical := range []string{guardStateOrigin, guardStateAlways} {
				if _, ok := expectedEnableState(unitSpec{Intent: intent, CanonicalEnableState: canonical}, pre); ok {
					t.Errorf("intent %s (canonical %s) authorized itself against a guard at %q; that guard is the absence of the protection this unit is supposed to be establishing",
						intent, canonical, state)
				}
			}
		}
		if _, ok := expectedEnableState(unitSpec{Intent: intentRepair, CanonicalEnableState: guardStateAlways}, pre); !ok {
			t.Errorf("repair refused a guard at %q, which is the one state repair exists to converge", state)
		}
	}
}

// TestPrestateValidateRefusesADatabaseThatCannotExist pins the coherence rule itself.
//
// Every field of a prestate comes from a caller-supplied projection, and the
// authorisation rules interrogate them ONE AT A TIME. That is what makes an incoherent
// reading dangerous rather than merely untidy: nothing downstream cross-checks it, so a
// projector with a mis-joined LEFT JOIN can hand over a combination the catalog could
// never produce and have it authorize a change.
//
// The first row below is the concrete opening. "No guard" plus "guard state A" reaches
// intentCreateGuard, which asks only about presence, and authorizes a CREATE over an
// object the same reading says already carries an ALWAYS guard.
//
// Mutation that must turn this red: delete any single clause of prestate.validate().
func TestPrestateValidateRefusesADatabaseThatCannotExist(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		pre  prestate
		why  string
	}{
		{
			name: "absent guard carrying a state",
			pre:  prestate{TargetExists: true, GuardPresent: false, GuardEnableState: guardStateAlways},
			why:  "create-guard asks only about presence, so this authorizes a CREATE over an existing ALWAYS guard",
		},
		{
			name: "present guard with no state",
			pre:  prestate{TargetExists: true, GuardPresent: true, GuardEnableState: ""},
			why:  "a pg_trigger row always has a tgenabled character",
		},
		{
			name: "guard on a relation that does not exist",
			pre:  prestate{TargetExists: false, GuardPresent: true, GuardEnableState: guardStateOrigin},
			why:  "pg_trigger.tgrelid is a foreign key into pg_class",
		},
		{
			name: "canonical with no guard",
			pre:  prestate{TargetExists: true, GuardPresent: false, GuardMatchesCanonical: true},
			why:  "matching a definition byte-for-byte presupposes something to compare",
		},
		{
			name: "a tgenabled character PostgreSQL does not store",
			pre:  prestate{TargetExists: true, GuardPresent: true, GuardEnableState: "X"},
			why:  "the projection is not reading tgenabled",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if err := tc.pre.validate(); err == nil {
				t.Fatalf("validate() accepted %+v; %s", tc.pre, tc.why)
			} else if !errors.Is(err, ErrMigrationUnauthorised) {
				t.Fatalf("validate() refused %+v with %v, which is not ErrMigrationUnauthorised; the runner routes on that sentinel", tc.pre, err)
			}
			// And the matrix must refuse it too, on every intent, rather than steering a
			// comparison by it.
			for _, intent := range []unitIntent{intentCreateGuard, intentAdoptLegacy, intentTransitionLegacyOToA, intentRepair} {
				spec := unitSpec{Intent: intent, CanonicalEnableState: guardStateAlways}
				got := reconcileOutcomeFor(spec, tc.pre, receiptProjection{Readable: true}, canonicalObject(guardStateAlways))
				if got != outcomeUnknown {
					t.Errorf("the matrix resolved intent %s against an incoherent prestate as %q, want %q",
						intent, got, outcomeUnknown)
				}
			}
		})
	}
}

// TestExpectedEnableStateIgnoresTheLedgerFields is what licenses the helper above to
// hold two of prestate's six fields fixed.
//
// coherentPrestates crosses only the four object-state fields. That is sound ONLY while
// expectedEnableState does not read ReceiptPresent or Epoch — and "does not read" is a
// property, not an assumption, so it is proved here rather than asserted in a comment.
// Without this, the day someone makes authorisation depend on either field, the
// "exhaustive" product would keep passing while covering a fraction of the domain.
//
// The fields are not arbitrary. Both are LEDGER facts, and authorisation must not turn
// on them: whether a receipt happens to exist, or which manifest revision is current, is
// the matrix's business (reconcileOutcomeFor compares r.Epoch against pre.Epoch and
// refuses a mismatch). An intent that consulted them here would be deciding twice, in two
// places, which is how the two answers drift apart.
//
// Epoch is covered at its boundaries rather than over int64: zero, one, the value the
// helper fixes, and both extremes. A rule that started reading Epoch would have to
// discriminate somewhere, and the edges are where a threshold lands.
//
// Mutation that must turn this red: make any intent consult pre.ReceiptPresent or
// pre.Epoch.
func TestExpectedEnableStateIgnoresTheLedgerFields(t *testing.T) {
	t.Parallel()

	base := coherentPrestates(t)
	intents := []unitIntent{intentCreateGuard, intentRepair, intentAdoptLegacy, intentTransitionLegacyOToA}
	epochs := []int64{0, 1, 7, -1, math.MaxInt64, math.MinInt64}

	for _, intent := range intents {
		for _, canonical := range []string{guardStateOrigin, guardStateAlways} {
			spec := unitSpec{Intent: intent, CanonicalEnableState: canonical}
			for _, pre := range base {
				wantState, wantOK := expectedEnableState(spec, pre)
				for _, receipt := range []bool{false, true} {
					for _, epoch := range epochs {
						v := pre
						v.ReceiptPresent = receipt
						v.Epoch = epoch
						// The variant must stay coherent: validate() has no opinion on
						// either field, and if that ever changes this test should say so
						// rather than silently skip.
						if err := v.validate(); err != nil {
							t.Fatalf("varying only the ledger fields made %+v incoherent: %v", v, err)
						}
						gotState, gotOK := expectedEnableState(spec, v)
						if gotOK != wantOK || gotState != wantState {
							t.Errorf("intent %s/%s authorisation depends on a ledger field: receipt=%v epoch=%d gave (%q,%v), want (%q,%v)",
								intent, canonical, receipt, epoch, gotState, gotOK, wantState, wantOK)
						}
					}
				}
			}
		}
	}
}
