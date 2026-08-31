// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
)

// guardgrammar_test.go pins the two languages the gate speaks: the sequence of EVENTS a
// rollout's history may contain, and the ENUMERATION a rollout may be opened over.
//
// Both used to be validated only where they happened to be looked at. The fold applied
// whatever arrived to p.Units[unit_id]; the plan parser checked the order of the edges it was
// handed and never that they covered anything. The SQL CHECK constraints validate a
// VOCABULARY — which words may appear in which column — and no constraint can express "this
// event follows that one" or "every declared target is named". That is what these two tables
// are for.

// grammarFixture builds a projection seeded from a real opening, plus the two unit ids of one
// target's lineage.
func grammarFixture(t *testing.T) (gateProjection, guardRolloutContext, guardSpec, string, string) {
	t.Helper()
	m := guardTestManifest(t, "audit_events", "t")
	spec := m.Specs[0]
	rollout := guardTestRollout(t)
	adoptID, err := guardUnitID(m.Format, spec.Key, intentAdoptLegacy)
	if err != nil {
		t.Fatal(err)
	}
	transitionID, err := guardUnitID(m.Format, spec.Key, intentTransitionLegacyOToA)
	if err != nil {
		t.Fatal(err)
	}
	proj := gateProjection{RolloutID: rollout.RolloutID, Units: map[string]unitGateFold{}}
	return proj, rollout, spec, adoptID, transitionID
}

// grammarEvent is one well-formed event of that rollout, before the mutation under test.
func grammarEvent(rollout guardRolloutContext, ordinal int64, kind gateEventKind) gateEvent {
	ev := gateEvent{
		RolloutID: rollout.RolloutID, Kind: kind, EventOrdinal: ordinal,
		Format: rollout.Format, CodeEpoch: rollout.CodeEpoch, CodeSHA256: rollout.CodeSHA256,
		RetainedRevision: rollout.RetainedRevision, RetainedSHA256: rollout.RetainedSHA256,
		Phase: gatePhasePending, Condition: gateConditionClean,
		// The actor is what appendGateEvent puts on every event it writes, so an event built
		// here and folded WITHOUT going through the writer has to carry it too. Otherwise these
		// fixtures fold histories no writer produces, and the fold's rule about attribution is
		// exercised only by its own negative case.
		Actor: guardActor,
	}
	// Likewise the prestate: guardUnitRunner.gateEvent carries the reading it took on ALL THREE
	// attempt kinds, the announcement included. Several fixtures used to leave it off the
	// announcement, so their "legal prefix" was a history production cannot emit.
	switch kind {
	case gateEventAttemptStarted, gateEventAttemptJudged, gateEventAttemptFailed:
		ev.PrestatePresent = true
		ev.PrestateSHA256 = someDigest(sha256.Sum256([]byte("grammar-fixture-prestate")))
	}
	return ev
}

// TestGuardGateGrammarRefusesAHistoryThisEngineCannotWrite walks the state machine.
//
// Every case is ONE event appended to a legal prefix, and every one of them was accepted
// before: each simply overwrote part of the fold. They are listed in the order of the rule
// they violate rather than by severity, because the point is that the machine is total —
// there is no kind for which "anything goes" is the answer.
func TestGuardGateGrammarRefusesAHistoryThisEngineCannotWrite(t *testing.T) {
	t.Parallel()

	// The legal prefix every case starts from: opened, then the adoption announced and judged.
	prefix := func(t *testing.T) (gateProjection, guardRolloutContext, guardSpec, string, string) {
		t.Helper()
		proj, rollout, spec, adoptID, transitionID := grammarFixture(t)
		opened := grammarEvent(rollout, 1, gateEventPendingOpened)
		opened.ExpectedUnits = []string{adoptID, transitionID}
		if err := proj.foldOne(opened); err != nil {
			t.Fatalf("the opening was refused: %v", err)
		}
		started := grammarEvent(rollout, 2, gateEventAttemptStarted)
		started.UnitID, started.AttemptID = adoptID, "attempt-1"
		started.Intent, started.Key = intentAdoptLegacy, spec.Key
		started.SpecSHA256, started.DefinitionSHA256 = someDigest(spec.SpecSHA256), someDigest(spec.DefinitionSHA256)
		if err := proj.foldOne(started); err != nil {
			t.Fatalf("the announced attempt was refused: %v", err)
		}
		return proj, rollout, spec, adoptID, transitionID
	}

	// The prefix itself must be legal, or every case below would pass for the wrong reason.
	t.Run("the legal prefix is accepted", func(t *testing.T) {
		proj, rollout, spec, adoptID, _ := prefix(t)
		judged := grammarEvent(rollout, 3, gateEventAttemptJudged)
		judged.UnitID, judged.AttemptID = adoptID, "attempt-1"
		judged.Intent, judged.Key = intentAdoptLegacy, spec.Key
		judged.SpecSHA256, judged.DefinitionSHA256 = someDigest(spec.SpecSHA256), someDigest(spec.DefinitionSHA256)
		judged.PrestatePresent = true
		if err := proj.foldOne(judged); err != nil {
			t.Fatalf("a history this engine writes was refused: %v", err)
		}
	})

	for _, tc := range []struct {
		name string
		// mut returns the event to append to the prefix.
		mut  func(rollout guardRolloutContext, spec guardSpec, adoptID, transitionID string) gateEvent
		want string
	}{{
		name: "a second opening resets the rollout",
		mut: func(r guardRolloutContext, _ guardSpec, adoptID, transitionID string) gateEvent {
			ev := grammarEvent(r, 3, gateEventPendingOpened)
			ev.ExpectedUnits = []string{adoptID, transitionID}
			return ev
		},
		want: "opened twice",
	}, {
		name: "an event names a unit the opening did not enumerate",
		mut: func(r guardRolloutContext, spec guardSpec, _, _ string) gateEvent {
			ev := grammarEvent(r, 3, gateEventAttemptStarted)
			// A well-formed identity for an entry this rollout never enumerated.
			other := spec
			other.Key.Relation = "some_other_table"
			id, err := guardUnitID(r.Format, other.Key, intentAdoptLegacy)
			if err != nil {
				panic(err)
			}
			ev.UnitID, ev.AttemptID = id, "attempt-1"
			ev.Intent, ev.Key = intentAdoptLegacy, other.Key
			ev.SpecSHA256, ev.DefinitionSHA256 = someDigest(other.SpecSHA256), someDigest(other.DefinitionSHA256)
			return ev
		},
		want: "did not enumerate",
	}, {
		name: "an event's own key does not produce the unit it is filed under",
		mut: func(r guardRolloutContext, spec guardSpec, adoptID, _ string) gateEvent {
			ev := grammarEvent(r, 3, gateEventAttemptJudged)
			ev.UnitID, ev.AttemptID = adoptID, "attempt-1"
			ev.Intent, ev.Key = intentAdoptLegacy, spec.Key
			ev.SpecSHA256, ev.DefinitionSHA256 = someDigest(spec.SpecSHA256), someDigest(spec.DefinitionSHA256)
			ev.SpecSHA256, ev.DefinitionSHA256 = someDigest(spec.SpecSHA256), someDigest(spec.DefinitionSHA256)
			ev.Key.Relation = "somebody_elses_table"
			ev.PrestatePresent = true
			return ev
		},
		want: "identifies unit",
	}, {
		name: "an event's own intent does not produce the unit it is filed under",
		mut: func(r guardRolloutContext, spec guardSpec, adoptID, _ string) gateEvent {
			ev := grammarEvent(r, 3, gateEventAttemptJudged)
			ev.UnitID, ev.AttemptID = adoptID, "attempt-1"
			ev.Intent, ev.Key = intentTransitionLegacyOToA, spec.Key
			ev.SpecSHA256, ev.DefinitionSHA256 = someDigest(spec.SpecSHA256), someDigest(spec.DefinitionSHA256)
			ev.SpecSHA256, ev.DefinitionSHA256 = someDigest(spec.SpecSHA256), someDigest(spec.DefinitionSHA256)
			ev.PrestatePresent = true
			return ev
		},
		want: "identifies unit",
	}, {
		name: "an event carries a different edition from the opening",
		mut: func(r guardRolloutContext, spec guardSpec, adoptID, _ string) gateEvent {
			ev := grammarEvent(r, 3, gateEventAttemptJudged)
			ev.UnitID, ev.AttemptID = adoptID, "attempt-1"
			ev.Intent, ev.Key = intentAdoptLegacy, spec.Key
			ev.SpecSHA256, ev.DefinitionSHA256 = someDigest(spec.SpecSHA256), someDigest(spec.DefinitionSHA256)
			ev.SpecSHA256, ev.DefinitionSHA256 = someDigest(spec.SpecSHA256), someDigest(spec.DefinitionSHA256)
			ev.CodeEpoch++
			ev.PrestatePresent = true
			return ev
		},
		want: "carries edition",
	}, {
		name: "an event carries a different retained pair from the opening",
		mut: func(r guardRolloutContext, spec guardSpec, adoptID, _ string) gateEvent {
			ev := grammarEvent(r, 3, gateEventAttemptJudged)
			ev.UnitID, ev.AttemptID = adoptID, "attempt-1"
			ev.Intent, ev.Key = intentAdoptLegacy, spec.Key
			ev.SpecSHA256, ev.DefinitionSHA256 = someDigest(spec.SpecSHA256), someDigest(spec.DefinitionSHA256)
			ev.SpecSHA256, ev.DefinitionSHA256 = someDigest(spec.SpecSHA256), someDigest(spec.DefinitionSHA256)
			ev.RetainedRevision++
			ev.PrestatePresent = true
			return ev
		},
		want: "carries edition",
	}, {
		name: "a judged reading carries an attempt nothing announced",
		mut: func(r guardRolloutContext, spec guardSpec, adoptID, _ string) gateEvent {
			ev := grammarEvent(r, 3, gateEventAttemptJudged)
			ev.UnitID, ev.AttemptID = adoptID, "attempt-forged"
			ev.Intent, ev.Key = intentAdoptLegacy, spec.Key
			ev.SpecSHA256, ev.DefinitionSHA256 = someDigest(spec.SpecSHA256), someDigest(spec.DefinitionSHA256)
			ev.SpecSHA256, ev.DefinitionSHA256 = someDigest(spec.SpecSHA256), someDigest(spec.DefinitionSHA256)
			ev.PrestatePresent = true
			return ev
		},
		want: "last announced attempt",
	}, {
		name: "a unit is judged before any attempt started",
		mut: func(r guardRolloutContext, spec guardSpec, _, transitionID string) gateEvent {
			ev := grammarEvent(r, 3, gateEventAttemptJudged)
			ev.UnitID, ev.AttemptID = transitionID, "attempt-1"
			ev.Intent, ev.Key = intentTransitionLegacyOToA, spec.Key
			ev.SpecSHA256, ev.DefinitionSHA256 = someDigest(spec.SpecSHA256), someDigest(spec.DefinitionSHA256)
			ev.SpecSHA256, ev.DefinitionSHA256 = someDigest(spec.SpecSHA256), someDigest(spec.DefinitionSHA256)
			ev.PrestatePresent = true
			return ev
		},
		want: "no attempt ever started",
	}, {
		name: "a reconciled event is interpreted at all",
		mut: func(r guardRolloutContext, spec guardSpec, adoptID, _ string) gateEvent {
			ev := grammarEvent(r, 3, gateEventReconciled)
			ev.UnitID, ev.AttemptID = adoptID, "attempt-1"
			ev.Intent, ev.Key = intentAdoptLegacy, spec.Key
			ev.SpecSHA256, ev.DefinitionSHA256 = someDigest(spec.SpecSHA256), someDigest(spec.DefinitionSHA256)
			ev.Condition = gateConditionClean
			return ev
		},
		want: "no writer for one",
	}} {
		t.Run(tc.name, func(t *testing.T) {
			proj, rollout, spec, adoptID, transitionID := prefix(t)
			err := proj.foldOne(tc.mut(rollout, spec, adoptID, transitionID))
			if err == nil {
				t.Fatalf("the fold accepted an event this engine cannot write (%s)", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("the refusal does not say %q: %v", tc.want, err)
			}
		})
	}
}

// TestGuardGateGrammarRefusesTheOpeningOutOfPlace covers the two rules about `pending-opened`
// that need a prefix of their own: it must be the FIRST event, and nothing may precede it.
//
// The ordinal rule is the one that matters operationally. Without it, appending an opening
// AFTER a `verification-failed` produced a rollout whose fold began again at clean — a blocked
// history erased by one row that carries no evidence of anything.
func TestGuardGateGrammarRefusesTheOpeningOutOfPlace(t *testing.T) {
	t.Parallel()
	proj, rollout, spec, adoptID, transitionID := grammarFixture(t)
	blocked := grammarEvent(rollout, 1, gateEventVerificationFailed)
	blocked.UnitID, blocked.Key = adoptID, spec.Key
	blocked.Condition = gateConditionBlocked
	blocked.Diagnostic = guardDiagnostic{Code: "DRIFT", RetryClass: guardRetryClassPermanent, UnblockPolicy: guardUnblockOperator}
	if err := proj.foldOne(blocked); err == nil {
		t.Fatal("a verification failure was folded into a rollout that was never opened")
	} else if !strings.Contains(err.Error(), "before any opening") {
		t.Errorf("the refusal does not say the rollout was never opened: %v", err)
	}

	// And an opening that is not the first event.
	fresh, rollout2, _, adoptID2, transitionID2 := grammarFixture(t)
	_ = adoptID2
	late := grammarEvent(rollout2, 4, gateEventPendingOpened)
	late.ExpectedUnits = []string{adoptID2, transitionID2}
	if err := fresh.foldOne(late); err == nil {
		t.Fatal("an opening at ordinal 4 was accepted as the start of a rollout")
	} else if !strings.Contains(err.Error(), "first event") {
		t.Errorf("the refusal does not say the opening must be first: %v", err)
	}
	_ = adoptID
	_ = transitionID
}

// TestGuardGateGrammarSealsAClosedRollout pins what may follow `ready`.
//
// A closed rollout has attested every terminal object and recorded a checkpoint over the other
// two logs. An attempt appended after that is work nothing authorized; a second `ready` would
// attest a checkpoint over rows the first never saw; and a `ready` whose expected units differ
// from the opening's was invisible entirely, because the fold kept the opening's list and threw
// the closing one away.
func TestGuardGateGrammarSealsAClosedRollout(t *testing.T) {
	t.Parallel()
	closed := func(t *testing.T) (gateProjection, guardRolloutContext, guardSpec, string, string) {
		t.Helper()
		proj, rollout, spec, adoptID, transitionID := grammarFixture(t)
		opened := grammarEvent(rollout, 1, gateEventPendingOpened)
		opened.ExpectedUnits = []string{adoptID, transitionID}
		if err := proj.foldOne(opened); err != nil {
			t.Fatal(err)
		}
		ready := grammarEvent(rollout, 2, gateEventReady)
		ready.Phase, ready.Condition = gatePhaseReady, gateConditionVerified
		ready.ExpectedUnits = []string{adoptID, transitionID}
		ready.CheckpointPresent = true
		if err := proj.foldOne(ready); err != nil {
			t.Fatalf("a well-formed close was refused: %v", err)
		}
		return proj, rollout, spec, adoptID, transitionID
	}

	t.Run("an attempt after the close", func(t *testing.T) {
		proj, rollout, spec, adoptID, _ := closed(t)
		ev := grammarEvent(rollout, 3, gateEventAttemptStarted)
		ev.UnitID, ev.AttemptID = adoptID, "attempt-2"
		ev.Intent, ev.Key = intentAdoptLegacy, spec.Key
		ev.SpecSHA256, ev.DefinitionSHA256 = someDigest(spec.SpecSHA256), someDigest(spec.DefinitionSHA256)
		if err := proj.foldOne(ev); err == nil {
			t.Fatal("an attempt was started on a closed rollout")
		} else if !strings.Contains(err.Error(), "closed") {
			t.Errorf("the refusal does not say the rollout is closed: %v", err)
		}
	})

	t.Run("a second close", func(t *testing.T) {
		proj, rollout, _, adoptID, transitionID := closed(t)
		ev := grammarEvent(rollout, 3, gateEventReady)
		ev.Phase, ev.Condition = gatePhaseReady, gateConditionVerified
		ev.ExpectedUnits = []string{adoptID, transitionID}
		ev.CheckpointPresent = true
		if err := proj.foldOne(ev); err == nil {
			t.Fatal("a rollout was closed twice")
		}
	})

	t.Run("drift found after the close is the one thing that may follow", func(t *testing.T) {
		proj, rollout, spec, adoptID, _ := closed(t)
		ev := grammarEvent(rollout, 3, gateEventVerificationFailed)
		ev.UnitID = adoptID
		// THE FOLDED PHASE, not `pending`. A verification failure is written ABOUT a history
		// rather than moving it, so declaring a phase the rollout is not in makes the durable row
		// — the one an operator reads — contradict the history it sits in.
		ev.Phase, ev.Condition = gatePhaseReady, gateConditionBlocked
		ev.Key = spec.Key
		ev.Diagnostic = guardDiagnostic{Code: "DRIFT", RetryClass: guardRetryClassPermanent, UnblockPolicy: guardUnblockOperator}
		if err := proj.foldOne(ev); err != nil {
			t.Fatalf("drift found after the close was refused: %v", err)
		}
		if proj.Phase != gatePhaseReady || proj.Condition != gateConditionBlocked {
			t.Errorf("drift after the close left the rollout %s/%s, want ready/blocked", proj.Phase, proj.Condition)
		}
	})

	t.Run("a close enumerating a different set from the opening", func(t *testing.T) {
		proj, rollout, _, adoptID, transitionID := grammarFixture(t)
		opened := grammarEvent(rollout, 1, gateEventPendingOpened)
		opened.ExpectedUnits = []string{adoptID, transitionID}
		if err := proj.foldOne(opened); err != nil {
			t.Fatal(err)
		}
		ready := grammarEvent(rollout, 2, gateEventReady)
		ready.Phase, ready.Condition = gatePhaseReady, gateConditionVerified
		ready.ExpectedUnits = []string{adoptID}
		ready.CheckpointPresent = true
		if err := proj.foldOne(ready); err == nil {
			t.Fatal("a close enumerating fewer units than its opening was accepted")
		} else if !strings.Contains(err.Error(), "enumerating") {
			t.Errorf("the refusal does not compare the two enumerations: %v", err)
		}
	})
}

// TestGuardPlanEnumerationMustCoverTheManifest is the bijection.
//
// THE OMISSION WAS INVISIBLE BY CONSTRUCTION. Every other rule in guardPlanFromEnumeration
// walks the units the enumeration names, so a key it does not mention is a key nothing checks:
// the terminal census counts per target present, the grammar validates the order of the edges
// it receives, and an empty list satisfied both. The close then locked every target — `keys`
// comes from the manifest — while verifyGuardTerminals iterated the plan, so the omitted
// target was held still, never judged, and written into `ready` as outside the expected set.
func TestGuardPlanEnumerationMustCoverTheManifest(t *testing.T) {
	t.Parallel()
	m := guardTestManifest(t, "audit_events", "rrw_widget")
	if len(m.Specs) != 2 {
		t.Fatalf("the fixture declares %d entries, want 2", len(m.Specs))
	}
	observed := map[guardKey]guardCatalogRow{}
	full := make([]string, 0, len(m.Specs))
	for _, spec := range m.Specs {
		observed[spec.Key] = canonicalRowFor(t, spec, guardStateAlways)
		id, err := guardUnitID(m.Format, spec.Key, intentAdoptLegacy)
		if err != nil {
			t.Fatal(err)
		}
		full = append(full, id)
	}

	// The complete enumeration rebuilds.
	if _, err := guardPlanFromEnumeration(m, full, map[string]unitGateFold{}, observed); err != nil {
		t.Fatalf("a complete enumeration was refused: %v", err)
	}

	for _, tc := range []struct {
		name  string
		units []string
		want  string
	}{
		{"a target is dropped entirely", full[:1], "names no unit for"},
		{"the enumeration is empty", nil, "names no unit for"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := guardPlanFromEnumeration(m, tc.units, map[string]unitGateFold{}, observed)
			if err == nil {
				t.Fatalf("an enumeration where %s was accepted", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("the refusal does not say %q: %v", tc.want, err)
			}
		})
	}

	// AND THE TRANSITION A DURABLE ADOPTION PROVES IS REQUIRED. Dropping only the second edge
	// leaves a plan that covers every key and is still a lie: the adoption's own judged reading
	// says the guard was at 'O' while the entry declares 'A', so the lineage cannot have ended
	// there. This is the N-1 case a key-coverage check alone does not catch.
	t.Run("the transition its own adoption proves is dropped", func(t *testing.T) {
		spec := m.Specs[0]
		adoptID, err := guardUnitID(m.Format, spec.Key, intentAdoptLegacy)
		if err != nil {
			t.Fatal(err)
		}
		judged := map[string]unitGateFold{adoptID: {
			State: unitGateJudged, Intent: intentAdoptLegacy, Key: spec.Key,
			JudgedReading:      prestate{TargetExists: true, GuardPresent: true, GuardEnableState: guardStateOrigin},
			JudgedReadingValid: true,
		}}
		if _, err := guardPlanFromEnumeration(m, full, judged, observed); err == nil {
			t.Fatal("an enumeration whose adoption judged 'O' and carries no transition was accepted")
		} else if !strings.Contains(err.Error(), "must also carry the transition") {
			t.Errorf("the refusal does not name the missing transition: %v", err)
		}
	})
}

// gateShapeFixture is the material a generated case needs to make a field present.
type gateShapeFixture struct {
	rollout      guardRolloutContext
	spec         guardSpec
	adoptID      string
	transitionID string
}

// gateFieldSetters says, for every durable field the fold governs, how a test turns it ON and
// OFF — and nothing else. One field, one setter, so a refusal can be required to name the field
// that was mutated rather than merely to exist.
//
// THE MAP IS EXHAUSTIVE BY ASSERTION, not by convention. The generator below fails if
// gateEventFields() names a field this map does not, so a durable field added to the fold
// without a mutation here turns the suite RED instead of escaping it. That is the property round
// four measured as absent: the old loop mutated four flags out of eight, and deleting four
// production checks left all forty cases green.
func gateFieldSetters() map[string]func(*gateEvent, bool, gateShapeFixture) {
	digest := func(b [32]byte, on bool) optDigest {
		if on {
			return someDigest(b)
		}
		return optDigest{}
	}
	return map[string]func(*gateEvent, bool, gateShapeFixture){
		"a unit id": func(e *gateEvent, on bool, fx gateShapeFixture) {
			e.UnitID = ""
			if on {
				e.UnitID = fx.adoptID
			}
		},
		"an attempt id": func(e *gateEvent, on bool, _ gateShapeFixture) {
			e.AttemptID = ""
			if on {
				e.AttemptID = "attempt-1"
			}
		},
		"an intent": func(e *gateEvent, on bool, _ gateShapeFixture) {
			e.Intent = ""
			if on {
				e.Intent = intentAdoptLegacy
			}
		},
		// THE KEY IS THREE SETTERS, not one, and that is the point of splitting it: turning
		// off "a target relation" leaves the schema and the trigger in place, so the case
		// that reaches the fold is the PARTIAL key the reader can build — the one an
		// aggregate presence test (`e.Key != guardKey{}`) called present.
		"a target schema": func(e *gateEvent, on bool, fx gateShapeFixture) {
			e.Key.Schema = ""
			if on {
				e.Key.Schema = fx.spec.Key.Schema
			}
		},
		"a target relation": func(e *gateEvent, on bool, fx gateShapeFixture) {
			e.Key.Relation = ""
			if on {
				e.Key.Relation = fx.spec.Key.Relation
			}
		},
		"a target trigger": func(e *gateEvent, on bool, fx gateShapeFixture) {
			e.Key.Trigger = ""
			if on {
				e.Key.Trigger = fx.spec.Key.Trigger
			}
		},
		"an entry digest": func(e *gateEvent, on bool, fx gateShapeFixture) {
			e.SpecSHA256 = digest(fx.spec.SpecSHA256, on)
		},
		"an object digest": func(e *gateEvent, on bool, fx gateShapeFixture) {
			e.DefinitionSHA256 = digest(fx.spec.DefinitionSHA256, on)
		},
		"a prestate": func(e *gateEvent, on bool, _ gateShapeFixture) { e.PrestatePresent = on },
		"a prestate digest": func(e *gateEvent, on bool, fx gateShapeFixture) {
			e.PrestateSHA256 = digest(fx.spec.SpecSHA256, on)
		},
		"a rendered prestate": func(e *gateEvent, on bool, _ gateShapeFixture) {
			e.PrestateBytes = ""
			if on {
				e.PrestateBytes = "guard=A"
			}
		},
		"a checkpoint": func(e *gateEvent, on bool, _ gateShapeFixture) { e.CheckpointPresent = on },
		"an enumeration": func(e *gateEvent, on bool, fx gateShapeFixture) {
			e.ExpectedUnits = nil
			if on {
				e.ExpectedUnits = []string{fx.adoptID, fx.transitionID}
			}
		},
		// The two diagnostic setters touch DISJOINT fields, which is what lets each case be
		// refused by its own rule: the body is everything except the code, and the code is only
		// the code. A setter that cleared both would make a body case fail on the code's rule and
		// the message would name the wrong field.
		// EVERY non-Code member, not two of them. The earlier setter turned on RetryClass and
		// UnblockPolicy and left SQLState, ExpectedSHA, ObservedSHA and Details untouched, so
		// the generated case labeled "a diagnostic body" exercised a THIRD of the body it
		// named. They are set together rather than one at a time because the shape declares one
		// rule for the whole body and two of them — SQLState on a non-server error, Details
		// after redaction — are legitimately empty on a well-formed event, so a per-member
		// `required` would be a rule production does not keep.
		"a diagnostic body": func(e *gateEvent, on bool, _ gateShapeFixture) {
			code := e.Diagnostic.Code
			e.Diagnostic = guardDiagnostic{Code: code}
			if on {
				e.Diagnostic.RetryClass = guardRetryClassPermanent
				e.Diagnostic.UnblockPolicy = guardUnblockOperator
				e.Diagnostic.SQLState = "55P03"
				e.Diagnostic.ExpectedSHA = "aa"
				e.Diagnostic.ObservedSHA = "bb"
				e.Diagnostic.Details = "redacted"
			}
		},
		"a diagnostic code": func(e *gateEvent, on bool, _ gateShapeFixture) {
			e.Diagnostic.Code = ""
			if on {
				e.Diagnostic.Code = "X"
			}
		},
		"an actor": func(e *gateEvent, on bool, _ gateShapeFixture) {
			e.Actor = ""
			if on {
				e.Actor = "operator:fixture"
			}
		},
		"a build id": func(e *gateEvent, on bool, _ gateShapeFixture) {
			e.BuildID = ""
			if on {
				e.BuildID = "build-1"
			}
		},
	}
}

// wellFormedGateEvent builds the event of `kind` that its declared shape accepts, ENTIRELY from
// the table: every required and optional field is turned on, every forbidden one off.
//
// Building it from the declaration rather than by hand is what makes the negatives below
// meaningful. A hand-built "well-formed" event agrees with whatever the author believed, and the
// only thing it then proves is that the author and the code hold the same belief.
func wellFormedGateEvent(t *testing.T, fx gateShapeFixture, ordinal int64, kind gateEventKind, folded gatePhase) gateEvent {
	t.Helper()
	shape := guardGateEventShapes()[kind]
	ev := grammarEvent(fx.rollout, ordinal, kind)
	ev.Phase = shape.Phase
	if kind == gateEventVerificationFailed {
		ev.Phase = folded
	}
	ev.Condition = shape.Conditions[0]
	setters := gateFieldSetters()
	for _, f := range gateEventFields() {
		set, ok := setters[f.What]
		if !ok {
			t.Fatalf("gateEventFields() declares %q and gateFieldSetters() has no mutation for it, so that field is governed by the fold and exercised by nothing", f.What)
		}
		set(&ev, f.Rule(shape) != fieldForbidden, fx)
	}
	return ev
}

// TestGuardGateShapeTableRefusesEveryFieldOutOfPlace is generated FROM the declared table, and
// this version is generated from ALL of it.
//
// WHAT THE PREVIOUS VERSION MEASURED, and why round four called it vacuous: it walked the kinds
// but only four of the fields, skipped the opening entirely, and never asserted WHICH rule
// refused. Deleting the intent, key, spec-digest and definition-digest checks from production
// left its forty cases green. A generator that iterates the wrong axis is a hand-written test
// wearing a loop.
//
// Three families per kind now: the PHASE and CONDITION it declares, and the presence rule of
// EVERY field in gateEventFields() — each mutated on its own, with the refusal required to name
// that field. The opening is included, folded against an EMPTY projection because that is the
// only state it is legal in.
func TestGuardGateShapeTableRefusesEveryFieldOutOfPlace(t *testing.T) {
	t.Parallel()

	// A projection with an open rollout and one announced attempt, so every other kind has
	// somewhere legal to land. The opening gets an empty one.
	seed := func(t *testing.T, kind gateEventKind) (gateProjection, gateShapeFixture, int64) {
		t.Helper()
		proj, rollout, spec, adoptID, transitionID := grammarFixture(t)
		fx := gateShapeFixture{rollout: rollout, spec: spec, adoptID: adoptID, transitionID: transitionID}
		if kind == gateEventPendingOpened {
			return proj, fx, 1
		}
		opened := grammarEvent(rollout, 1, gateEventPendingOpened)
		opened.ExpectedUnits = []string{adoptID, transitionID}
		if err := proj.foldOne(opened); err != nil {
			t.Fatalf("the opening was refused: %v", err)
		}
		// The announced attempt is seeded for every kind that must FOLLOW one — and NOT for
		// `attempt-started` itself, which would then be announcing an attempt already on the
		// record. The first version of this fixture did seed it, and the duplicate-announcement
		// rule caught it: the well-formed case failed on a rule that was working.
		if kind == gateEventAttemptStarted {
			return proj, fx, 2
		}
		started := wellFormedGateEvent(t, fx, 2, gateEventAttemptStarted, proj.Phase)
		if err := proj.foldOne(started); err != nil {
			t.Fatalf("the announced attempt was refused: %v", err)
		}
		return proj, fx, 3
	}

	kinds := make([]gateEventKind, 0, len(guardGateEventShapes()))
	for k := range guardGateEventShapes() {
		kinds = append(kinds, k)
	}
	sort.Slice(kinds, func(i, j int) bool { return kinds[i] < kinds[j] })
	if len(kinds) == 0 {
		t.Fatal("the shape table is empty, so this test would pass vacuously")
	}

	for _, kind := range kinds {
		shape := guardGateEventShapes()[kind]
		t.Run(string(kind), func(t *testing.T) {
			// The well-formed event of this kind must be accepted, or every negative below would
			// pass for the wrong reason.
			t.Run("the well-formed event is accepted", func(t *testing.T) {
				proj, fx, ord := seed(t, kind)
				ev := wellFormedGateEvent(t, fx, ord, kind, proj.Phase)
				// A judged reading must follow the attempt the seed announced; the started event
				// the seed folded carries the same attempt id by construction.
				if err := proj.foldOne(ev); err != nil {
					t.Fatalf("the shape's own well-formed %s was refused: %v", kind, err)
				}
			})

			// THE PHASE. Every kind but the verification failure declares a constant; that one
			// must declare the phase the fold is in.
			t.Run("a phase its kind does not declare", func(t *testing.T) {
				proj, fx, ord := seed(t, kind)
				ev := wellFormedGateEvent(t, fx, ord, kind, proj.Phase)
				if ev.Phase == gatePhasePending {
					ev.Phase = gatePhaseReady
				} else {
					ev.Phase = gatePhasePending
				}
				if err := proj.foldOne(ev); err == nil {
					t.Fatalf("a %s declaring phase %q was folded and silently normalised", kind, ev.Phase)
				} else if !strings.Contains(err.Error(), "declares phase") {
					t.Errorf("the refusal does not name the phase: %v", err)
				}
			})

			// THE CONDITION.
			t.Run("a condition its kind does not allow", func(t *testing.T) {
				proj, fx, ord := seed(t, kind)
				ev := wellFormedGateEvent(t, fx, ord, kind, proj.Phase)
				forbidden := gateConditionVerified
				for _, c := range shape.Conditions {
					if c == gateConditionVerified {
						forbidden = gateConditionRetryable
					}
				}
				ev.Condition = forbidden
				if err := proj.foldOne(ev); err == nil {
					t.Fatalf("a %s declaring condition %q was accepted", kind, forbidden)
				} else if !strings.Contains(err.Error(), "declares condition") {
					t.Errorf("the refusal does not name the condition: %v", err)
				}
			})

			// EVERY FIELD, one at a time, exactly the wrong way round — and the refusal must name
			// the field that was moved. Naming is what makes the mutants specific: without it,
			// deleting one production rule would leave its case green on the strength of a
			// DIFFERENT rule refusing the same event.
			for _, f := range gateEventFields() {
				rule := f.Rule(shape)
				if rule == fieldOptional {
					// Declared optional: both states are legal and there is nothing to refuse.
					// Assert exactly that, so "optional" cannot be used to hide an unchecked field.
					t.Run("the shape allows either way of "+f.What, func(t *testing.T) {
						setters := gateFieldSetters()
						for _, on := range []bool{true, false} {
							proj, fx, ord := seed(t, kind)
							ev := wellFormedGateEvent(t, fx, ord, kind, proj.Phase)
							setters[f.What](&ev, on, fx)
							if err := proj.foldOne(ev); err != nil {
								t.Fatalf("a %s with %s present=%t was refused, so the field is not optional: %v", kind, f.What, on, err)
							}
						}
					})
					continue
				}
				t.Run("the shape's rule about "+f.What, func(t *testing.T) {
					proj, fx, ord := seed(t, kind)
					ev := wellFormedGateEvent(t, fx, ord, kind, proj.Phase)
					gateFieldSetters()[f.What](&ev, rule == fieldForbidden, fx)
					err := proj.foldOne(ev)
					if err == nil {
						t.Fatalf("a %s with %s the wrong way round (rule=%v) was accepted", kind, f.What, rule)
					}
					if !strings.Contains(err.Error(), f.What) {
						t.Errorf("a %s with %s the wrong way round was refused by a DIFFERENT rule, so this case does not pin its own: %v", kind, f.What, err)
					}
				})
			}
		})
	}
}

// TestGuardGateTransitionsRefuseTheHistoriesNoWriterEmits is the transition half of the table:
// previous state, kind, next state.
//
// The shape table says what an event may CARRY. It says nothing about what may FOLLOW what, and
// that is the axis round four found holes in: two identical `attempt-started` events were
// accepted, because the started case returned before looking at the fold at all.
func TestGuardGateTransitionsRefuseTheHistoriesNoWriterEmits(t *testing.T) {
	t.Parallel()

	open := func(t *testing.T) (gateProjection, gateShapeFixture) {
		t.Helper()
		proj, rollout, spec, adoptID, transitionID := grammarFixture(t)
		fx := gateShapeFixture{rollout: rollout, spec: spec, adoptID: adoptID, transitionID: transitionID}
		opened := grammarEvent(rollout, 1, gateEventPendingOpened)
		opened.ExpectedUnits = []string{adoptID, transitionID}
		if err := proj.foldOne(opened); err != nil {
			t.Fatalf("the opening was refused: %v", err)
		}
		return proj, fx
	}

	t.Run("the same attempt announced twice", func(t *testing.T) {
		proj, fx := open(t)
		first := wellFormedGateEvent(t, fx, 2, gateEventAttemptStarted, proj.Phase)
		if err := proj.foldOne(first); err != nil {
			t.Fatalf("the first announcement was refused: %v", err)
		}
		again := wellFormedGateEvent(t, fx, 3, gateEventAttemptStarted, proj.Phase)
		if again.AttemptID != first.AttemptID {
			t.Fatalf("the fixture built two DIFFERENT attempts (%q, %q), so this case would test a legitimate retry", first.AttemptID, again.AttemptID)
		}
		err := proj.foldOne(again)
		if err == nil {
			t.Fatal("the same attempt was announced twice and the history was accepted; a writer that announces one attempt twice either re-dates work or lost track of its own attempt")
		}
		if !strings.Contains(err.Error(), "already on the record") {
			t.Errorf("the refusal is not the duplicate-announcement one: %v", err)
		}
	})

	t.Run("a different attempt after a failure is a legitimate retry", func(t *testing.T) {
		proj, fx := open(t)
		started := wellFormedGateEvent(t, fx, 2, gateEventAttemptStarted, proj.Phase)
		if err := proj.foldOne(started); err != nil {
			t.Fatalf("the announcement was refused: %v", err)
		}
		failed := wellFormedGateEvent(t, fx, 3, gateEventAttemptFailed, proj.Phase)
		if err := proj.foldOne(failed); err != nil {
			t.Fatalf("the failure was refused: %v", err)
		}
		retry := wellFormedGateEvent(t, fx, 4, gateEventAttemptStarted, proj.Phase)
		retry.AttemptID = "attempt-2"
		if err := proj.foldOne(retry); err != nil {
			t.Fatalf("a NEW attempt after a failure was refused, so the duplicate rule is forbidding the retry this ledger is built around: %v", err)
		}
	})

	t.Run("a judged reading after that same attempt failed", func(t *testing.T) {
		proj, fx := open(t)
		started := wellFormedGateEvent(t, fx, 2, gateEventAttemptStarted, proj.Phase)
		if err := proj.foldOne(started); err != nil {
			t.Fatalf("the announcement was refused: %v", err)
		}
		failed := wellFormedGateEvent(t, fx, 3, gateEventAttemptFailed, proj.Phase)
		if err := proj.foldOne(failed); err != nil {
			t.Fatalf("the failure was refused: %v", err)
		}
		judged := wellFormedGateEvent(t, fx, 4, gateEventAttemptJudged, proj.Phase)
		err := proj.foldOne(judged)
		if err == nil {
			t.Fatal("a judged reading was folded over an attempt that already failed; the failure's condition is the only thing between a blocked rollout and another run")
		}
		if !strings.Contains(err.Error(), "already recorded as failed") {
			t.Errorf("the refusal is not the failed-then-judged one: %v", err)
		}
	})

	t.Run("a judged reading for an attempt nobody announced", func(t *testing.T) {
		proj, fx := open(t)
		judged := wellFormedGateEvent(t, fx, 2, gateEventAttemptJudged, proj.Phase)
		err := proj.foldOne(judged)
		if err == nil {
			t.Fatal("a judged reading with no announced attempt was accepted")
		}
		if !strings.Contains(err.Error(), "no attempt ever started") {
			t.Errorf("the refusal is not the never-announced one: %v", err)
		}
	})

	t.Run("a second opening under the same identity", func(t *testing.T) {
		proj, fx := open(t)
		again := wellFormedGateEvent(t, fx, 2, gateEventPendingOpened, proj.Phase)
		err := proj.foldOne(again)
		if err == nil {
			t.Fatal("a rollout was opened twice; the second opening resets whatever condition the first reached")
		}
		if !strings.Contains(err.Error(), "opened twice") {
			t.Errorf("the refusal is not the double-opening one: %v", err)
		}
	})
}

// rolloutSpecOf and adoptIDOf give the enumeration case a unit id that is deliberately NOT one
// the seeded rollout enumerates, so "an enumeration where none belongs" is refused for the
// shape rather than for coincidence.
func rolloutSpecOf(t *testing.T) guardSpec {
	t.Helper()
	return guardTestManifest(t, "audit_events").Specs[0]
}

func adoptIDOf(t *testing.T, spec guardSpec) string {
	t.Helper()
	id, err := guardUnitID(guardManifestFormat, spec.Key, intentAdoptLegacy)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// TestGuardInventoryRefusesATupleItsOwnDigestDoesNotProduce is r2/r3 F-05's mutant table.
//
// THE CHAIN DIGEST PROVES THE ROW IS THE ROW THE WRITER CHAINED. It proves nothing about
// whether spec_sha256 describes the rest of that row — and spec_sha256 is the value the manifest
// comparison trusts. So an event could carry the expected entry digest beside a different
// producer, a different legacy policy or a different desired state and satisfy every check: the
// digest agreeing with the manifest, and the tuple agreeing with nothing.
//
// EVERY MUTANT HERE KEEPS THE STORED spec_sha256 AND RECHAINS. That is what makes the table a
// test of the recomputation rather than of the chain: each case writes a complete, internally
// consistent inventory whose only defect is that its tuple does not hash to the digest it
// carries. Deleting the recomputation in verifyInventoryChain turns every one of them green.
func TestGuardInventoryRefusesATupleItsOwnDigestDoesNotProduce(t *testing.T) {
	t.Parallel()
	m := guardTestManifest(t, "audit_events")
	spec := m.Specs[0]

	// The activation this edition writes, which is the control. The retained pair is the EMPTY
	// stream's — activating a declared entry does not change the set of entries this database
	// keeps and the code no longer declares — and verifyInventoryChain recomputes it rather than
	// reading the row's claim, so getting it wrong here fails for that reason instead.
	emptyRetained, err := emptyRetainedDigest()
	if err != nil {
		t.Fatal(err)
	}
	canonical := inventoryEvent{
		Kind: inventoryActivate, Key: spec.Key, Producer: spec.Producer,
		Format: m.Format, CodeEpoch: m.CodeEpoch,
		DefinitionSHA256: spec.DefinitionSHA256, SpecSHA256: spec.SpecSHA256,
		DesiredEnableState: spec.DesiredEnableState, LegacyAllowedStates: spec.LegacyAllowedStates,
		RetainedRevision: 0, RetainedSHA256: emptyRetained,
	}

	seed := func(t *testing.T, ev inventoryEvent) (*sql.DB, dialect.Dialect) {
		t.Helper()
		db, dia := guardLedgerFixture(t)
		tx, err := db.BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer func() { _ = tx.Rollback() }()
		if _, err := appendInventoryEvent(context.Background(), tx, dia, ev); err != nil {
			t.Fatalf("append the activation: %v", err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit: %v", err)
		}
		return db, dia
	}

	t.Run("the activation this edition writes is accepted", func(t *testing.T) {
		db, dia := seed(t, canonical)
		if _, _, err := verifyInventoryChain(context.Background(), db, dia, m); err != nil {
			t.Fatalf("the canonical activation was refused: %v", err)
		}
	})

	for _, tc := range []struct {
		name string
		mut  func(*inventoryEvent)
		want string
	}{
		{"the producer", func(e *inventoryEvent) { e.Producer = guardProducer("somebody-else") }, "hashes to"},
		{"the desired state", func(e *inventoryEvent) { e.DesiredEnableState = guardStateOrigin }, "hashes to"},
		{"the legacy policy", func(e *inventoryEvent) { e.LegacyAllowedStates = []string{"O", "A", "D"} }, "hashes to"},
		{"the object digest", func(e *inventoryEvent) { e.DefinitionSHA256[0] ^= 0xff }, "hashes to"},
		// EVERY MEMBER OF THE KEY, because the key is three fields and mutating one of them was
		// standing in for all three. A schema or a trigger name is as much a target's identity as
		// its relation, and an inventory that attributes an activation to the wrong one attributes
		// it to a different object entirely.
		{"the target relation", func(e *inventoryEvent) { e.Key.Relation = "somebody_elses_table" }, "hashes to"},
		{"the target schema", func(e *inventoryEvent) { e.Key.Schema = "somebody_elses_schema" }, "hashes to"},
		{"the target trigger", func(e *inventoryEvent) { e.Key.Trigger = "somebody_elses_trigger" }, "hashes to"},
		// AND THE TWO FIELDS THAT ARE NOT IN THE ENTRY DIGEST AT ALL — they are compared against
		// the manifest directly, so their mutants must fail for a DIFFERENT reason. Listing them
		// here is what stops "everything fails, so everything is checked" being the whole
		// argument, and the format one was missing: round four removed its production check and
		// this table stayed green.
		{"the code epoch", func(e *inventoryEvent) { e.CodeEpoch++ }, "code epoch"},
		{"the manifest format", func(e *inventoryEvent) { e.Format++ }, "manifest format"},
	} {
		t.Run("a tuple whose "+tc.name+" moved while its digest did not", func(t *testing.T) {
			mutated := canonical
			mutated.LegacyAllowedStates = append([]string(nil), canonical.LegacyAllowedStates...)
			tc.mut(&mutated)
			// THE STORED DIGEST IS THE CANONICAL ONE. That is the whole point: the row is
			// internally consistent as a CHAIN and inconsistent as a STATEMENT.
			mutated.SpecSHA256 = canonical.SpecSHA256
			db, dia := seed(t, mutated)
			_, _, err := verifyInventoryChain(context.Background(), db, dia, m)
			if err == nil {
				t.Fatalf("an inventory whose %s moved while its entry digest did not was accepted", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("the refusal does not say %q: %v", tc.want, err)
			}
		})
	}
}

// TestGuardCloseBudgetsBoundBothPhases is r3 F-12's arithmetic, on an injected clock.
//
// TWO CEILINGS, because they bound different things. The acquisition budget bounds WAITING for
// other sessions: `lock_timeout` restarts on every acquisition, so N locks at 15s each is a
// worst case of N*15s and only a budget re-clamped before each one makes it total. The work
// budget bounds the statements AFTER every lock is held — and it exists
// because `statement_timeout` is also per statement, so arming it once at 60s bounded each of
// the six statements at 60s and their sum at nothing, under a comment that claimed otherwise.
//
// It is tested on an injected clock rather than by wall time: a test that waits out a real
// three-minute budget is a test nobody runs.
func TestGuardCloseBudgetsBoundBothPhases(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		total time.Duration
		want  time.Duration
	}{
		{"the per-statement ceiling passes through while the budget is wide", guardCloseAcquisitionBudget, guardCloseLockTimeout},
		{"and is clamped to what the budget has left", 5 * time.Second, 5 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Unix(0, 0)
			b := newLockBudget(tc.total, func() time.Time { return now }, sleepCtx, jitterFloat)
			got, ok := b.clampPositive(guardCloseLockTimeout)
			if !ok {
				t.Fatal("a fresh budget reports nothing left")
			}
			if got != tc.want {
				t.Errorf("the armed ceiling is %s, want %s", got, tc.want)
			}
		})
	}

	// A SPENT BUDGET ARMS NOTHING. That is what turns N per-statement ceilings into one
	// deadline: the acquisition that would exceed it is not issued at all.
	now := time.Unix(0, 0)
	spent := newLockBudget(time.Second, func() time.Time { return now }, sleepCtx, jitterFloat)
	now = now.Add(2 * time.Second)
	if _, ok := spent.clampPositive(guardCloseLockTimeout); ok {
		t.Error("a spent budget still armed an acquisition, so the total ceiling is not total")
	}
	if !spent.expired() {
		t.Error("a budget past its deadline does not report itself expired")
	}

	// AND THE WORK PHASE'S BUDGET IS A WALL-CLOCK ONE, carried by a context rather than by a
	// server-side knob, which is the only shape that bounds a SUM of statements plus the commit.
	//
	// This half uses the REAL clock deliberately: a context deadline is an absolute instant that
	// the runtime compares against the real clock, so an injected one would be measuring the
	// fake and asserting about the real. That is a property of context, not of lockBudget.
	before := time.Now()
	work := newLockBudget(guardCloseWorkBudget, time.Now, sleepCtx, jitterFloat)
	wctx, cancel := work.context(context.Background())
	defer cancel()
	deadline, ok := wctx.Deadline()
	if !ok {
		t.Fatal("the work budget's context carries no deadline, so several statements can each take the per-statement ceiling")
	}
	// `before` is sampled a few microseconds before the budget starts, so the deadline is the
	// budget plus that gap. A small tolerance keeps the assertion about the BUDGET rather than
	// about how fast two consecutive calls to time.Now() are.
	if got := deadline.Sub(before); got > guardCloseWorkBudget+time.Second {
		t.Errorf("the work context's deadline is %s away, which is more than the budget %s", got, guardCloseWorkBudget)
	}
	if deadline.Before(before) {
		t.Errorf("the work context is born expired (deadline %s, taken at %s)", deadline, before)
	}
	if guardCloseWorkTimeout >= guardCloseWorkBudget {
		t.Errorf("the per-statement work ceiling (%s) is not smaller than the phase budget (%s), so the budget can never bind",
			guardCloseWorkTimeout, guardCloseWorkBudget)
	}
	t.Logf("GUARD_CLOSE_BUDGETS|acquisition=%s|per_lock=%s|work_phase=%s|per_statement=%s",
		guardCloseAcquisitionBudget, guardCloseLockTimeout, guardCloseWorkBudget, guardCloseWorkTimeout)
}
