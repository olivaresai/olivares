// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"fmt"
	"sort"
	"strings"
)

// guardunits.go turns a manifest plus one reading of the catalog into the ordered plan of
// units a rollout will execute.
//
// The plan is DERIVED, never hand-written, and it is derived from an observation because
// the edges an entry needs depend on what is already there: a guard sitting at ORIGIN needs
// adoption AND the O -> A transition, while one already at ALWAYS needs adoption alone.
// Enumerating both edges unconditionally would put a transition in the plan whose
// precondition the object contradicts — expectedEnableState authorizes O -> A only from
// 'O' — so the unit would be refused rather than skipped, and a legitimate database would
// fail its boot for being in a legitimate state.
//
// WHERE THIS DIFFERS FROM THE DESIGN, and why. The step-2 and wiring designs both say the
// v6 migration creates "the first pending rollout" alongside the three relations. It
// cannot, and the reason is an ordering fact in store.go rather than a preference:
// migrate.Apply for the core tracking table runs BEFORE applyModuleTables, so at v6 time
// the module tables these units target DO NOT EXIST. A rollout opened there would have to
// enumerate either targets that are not there or edges that may be unreachable.
//
// So the v6 migration creates the relations, their guards, the ACL posture, the inventory
// activations and the bootstrap receipts; the ROLLOUT is opened by the coordinator at the
// insertion point, where every target exists and its lineage is observable. The bootstrap
// receipts still carry the same rollout id because that id is DERIVED from
// (format, code epoch, code sha256, retained pair) — all of which are known identically at
// both points — and a regression pins that the two computations agree.

// guardUnitPlan is one unit the rollout expects to execute.
type guardUnitPlan struct {
	UnitID string
	// Ordinal is the unit's 1-based position in the plan. It is part of the durable
	// enumeration, so a later boot can tell a missing unit from a reordered one.
	Ordinal int64
	Spec    guardSpec
	Intent  unitIntent
	// CanonicalEnableState is the state THIS edge must leave behind, which is not always
	// the entry's desired state: adoption of a legacy 'O' guard leaves it at 'O', and the
	// separate transition is what reaches 'A'.
	CanonicalEnableState string
	// IsTerminal marks the one unit per target whose poststate must hold NOW.
	//
	// Exactly one per target, and it is not the caller's choice: an adoption that was
	// superseded by a transition is history, and holding it against the 'A' its successor
	// was authorized to produce would declare a correct lineage divergent.
	IsTerminal bool
	// Predecessor is the unit whose receipt this one's lineage requires, or "" for the
	// first edge of a target.
	Predecessor string
}

// unitSpec renders the plan's authorisation for the runner.
func (p guardUnitPlan) unitSpec() unitSpec {
	return unitSpec{Intent: p.Intent, CanonicalEnableState: p.CanonicalEnableState}
}

// guardPlanRefusal is a target the plan will not build a unit for, with the reason.
//
// It is a VALUE rather than an error because a refusal is per target and the coordinator
// must be able to report every one of them at once. A boot that fails on the first
// unusable relation makes an operator fix them one restart at a time.
type guardPlanRefusal struct {
	Key    guardKey
	Code   string
	Detail string
}

// The refusal codes. They are the durable diagnostic_code values, so they are named
// constants rather than sentences: a later boot routes on the code and shows the sentence.
const (
	guardRefusalRelationMissing  = "RELATION_MISSING"
	guardRefusalRelationInvalid  = "RELATION_INELIGIBLE"
	guardRefusalGuardMissing     = "GUARD_MISSING"
	guardRefusalGuardLookalike   = "GUARD_LOOKALIKE"
	guardRefusalGuardStateUnsafe = "GUARD_STATE_UNSAFE"
)

func (r guardPlanRefusal) String() string {
	return fmt.Sprintf("%s: %s (%s)", r.Key, r.Code, r.Detail)
}

// buildGuardUnitPlans derives the ordered plan from the manifest and one catalog reading.
//
// observed must hold a row for every manifest key — the bulk projection returns one row per
// target whether or not the relation exists, so a missing map entry means the caller lost a
// row rather than that the relation is absent, and that is refused rather than treated as
// absence.
func buildGuardUnitPlans(m guardManifest, observed map[guardKey]guardCatalogRow) ([]guardUnitPlan, []guardPlanRefusal, error) {
	var plans []guardUnitPlan
	var refusals []guardPlanRefusal

	for _, spec := range m.Specs {
		row, ok := observed[spec.Key]
		if !ok {
			return nil, nil, fmt.Errorf("sqlstore: the guard plan has no catalog reading for %s; the projection returns a row for every target, so a missing one means the batch was truncated rather than that the relation is absent", spec.Key)
		}
		if !row.RelationExists {
			refusals = append(refusals, guardPlanRefusal{spec.Key, guardRefusalRelationMissing,
				"the relation does not exist in this schema"})
			continue
		}
		if ok, why := row.eligible(); !ok {
			refusals = append(refusals, guardPlanRefusal{spec.Key, guardRefusalRelationInvalid, why})
			continue
		}
		if !row.GuardExists {
			// NOT a create-guard. This edition's targets all have their guards emitted by the
			// DDL that creates the table, so an absent guard means somebody removed one — and
			// creating it here would launder that removal into "managed". Refusing keeps the
			// evidence of the removal.
			refusals = append(refusals, guardPlanRefusal{spec.Key, guardRefusalGuardMissing,
				"the guard is absent; this edition adopts guards the table DDL emits and never re-creates a removed one, because creating it would launder the removal"})
			continue
		}
		canonical, diff := row.matchesCanonical(spec)
		if !canonical {
			refusals = append(refusals, guardPlanRefusal{spec.Key, guardRefusalGuardLookalike,
				"the guard is present but not the declared object: " + strings.Join(diff, "; ")})
			continue
		}
		if !specAllowsLegacyState(spec, row.EnableState) {
			refusals = append(refusals, guardPlanRefusal{spec.Key, guardRefusalGuardStateUnsafe,
				fmt.Sprintf("the guard is in state %q, which this entry does not allow adoption from (allowed: %s)",
					row.EnableState, strings.Join(spec.LegacyAllowedStates, ","))})
			continue
		}

		adoptID, err := guardUnitID(m.Format, spec.Key, intentAdoptLegacy)
		if err != nil {
			return nil, nil, err
		}
		// Adoption always comes first and always adopts the state it FOUND. Its poststate is
		// not the entry's desired state: adoption alters nothing, so claiming 'A' for a guard
		// observed at 'O' would fail its own postcondition.
		adopt := guardUnitPlan{
			UnitID:               adoptID,
			Spec:                 spec,
			Intent:               intentAdoptLegacy,
			CanonicalEnableState: row.EnableState,
		}
		if row.EnableState == spec.DesiredEnableState {
			// Already where the edition wants it. Adoption is the whole lineage, so it is the
			// terminal edge, and no transition is invented: an O -> A unit built from an 'A'
			// prestate is exactly the unauthorized transition the matrix refuses.
			adopt.IsTerminal = true
			plans = append(plans, adopt)
			continue
		}
		transitionID, err := guardUnitID(m.Format, spec.Key, intentTransitionLegacyOToA)
		if err != nil {
			return nil, nil, err
		}
		plans = append(plans, adopt, guardUnitPlan{
			UnitID:               transitionID,
			Spec:                 spec,
			Intent:               intentTransitionLegacyOToA,
			CanonicalEnableState: spec.DesiredEnableState,
			IsTerminal:           true,
			Predecessor:          adoptID,
		})
	}

	// Ordered by identity, then by edge, so the plan is a function of the manifest and the
	// reading rather than of map iteration. The canonical bytes of pending-opened contain
	// this order.
	sort.SliceStable(plans, func(i, j int) bool {
		if plans[i].Spec.Key != plans[j].Spec.Key {
			return plans[i].Spec.Key.less(plans[j].Spec.Key)
		}
		// Adoption before transition: the transition's lineage requires the adoption's
		// receipt, so the reverse order would enumerate a unit before its predecessor.
		return plans[i].Intent == intentAdoptLegacy && plans[j].Intent != intentAdoptLegacy
	})
	seen := make(map[string]bool, len(plans))
	terminals := make(map[guardKey]int, len(plans))
	for i := range plans {
		plans[i].Ordinal = int64(i + 1)
		if seen[plans[i].UnitID] {
			return nil, nil, fmt.Errorf("sqlstore: the guard plan enumerates unit %s twice", plans[i].UnitID)
		}
		seen[plans[i].UnitID] = true
		if plans[i].IsTerminal {
			terminals[plans[i].Spec.Key]++
		}
	}
	// EXACTLY ONE TERMINAL PER TARGET, checked rather than assumed. Two terminals would let
	// two different poststates both be required now; zero would leave a target whose current
	// state nothing verifies, which is the shape in which a rollout reports success over an
	// object nobody looked at.
	for key, n := range terminals {
		if n != 1 {
			return nil, nil, fmt.Errorf("sqlstore: the guard plan marks %d terminal units for %s, and exactly one edge per target may be terminal", n, key)
		}
	}
	for key := range observed {
		if _, declared := m.lookup(key); !declared {
			// A reading for a key the manifest does not declare means the caller built the
			// batch from something other than the manifest. Refused rather than ignored: the
			// plan's cardinality is what the durable enumeration is checked against.
			return nil, nil, fmt.Errorf("sqlstore: the guard plan was handed a catalog reading for %s, which this edition does not declare", key)
		}
	}
	return plans, refusals, nil
}

// guardPlanFromEnumeration rebuilds the plan from the DURABLE list of expected units.
//
// This is the correction to a defect the PostgreSQL leg measured, and the defect is worth
// stating because the wrong version looked obviously right. The coordinator derived its plan
// from a fresh observation on EVERY boot — which is correct exactly once, when the rollout is
// opened, and wrong every time afterwards. On the second boot the guards are at ALWAYS, so a
// re-derived plan holds ONE adoption per target instead of the adoption-plus-transition pair
// the rollout actually enumerated, and that adoption is marked terminal. The matrix then
// compares an adoption whose judged reading said 'O' against an object that says 'A' and
// answers, correctly for that question and uselessly for this one:
//
//	the terminal unit for "public"."audit_events" reconciles as divergent rather than applied
//
// A rollout's enumeration is part of its authorisation. Once `pending-opened` records it, THAT
// is the plan — a later boot verifies the units the rollout opened, not the units a fresh
// reading would have opened.
//
// Unit identities are digests, so they are resolved through a forward index over the
// manifest's keys and this edition's intents rather than parsed. An identity that does not
// resolve is refused rather than skipped: it means the durable enumeration names a unit this
// binary cannot construct, which is a manifest disagreement and not a missing row.
func guardPlanFromEnumeration(
	m guardManifest,
	units []string,
	judged map[string]unitGateFold,
	observed map[guardKey]guardCatalogRow,
) ([]guardUnitPlan, error) {
	type identity struct {
		spec   guardSpec
		intent unitIntent
	}
	// ONLY THE INTENTS THIS EDITION EMITS are resolvable.
	//
	// Indexing create-guard and repair as well was a real hole rather than generosity: their ids
	// ARE computable from the manifest, so an enumeration naming one resolved cleanly and became
	// a plan entry. With no receipt it failed late, inside Execute; WITH a self-consistent
	// receipt and judged event it was skipped and then verified against a postcondition the
	// manifest never authorized. A plan language is a language: something outside it must be
	// refused by the parser, not by whatever runs last.
	index := make(map[string]identity, len(m.Specs)*2)
	for _, spec := range m.Specs {
		for _, intent := range guardEmittedIntents() {
			id, err := guardUnitID(m.Format, spec.Key, intent)
			if err != nil {
				return nil, err
			}
			index[id] = identity{spec: spec, intent: intent}
		}
	}

	plans := make([]guardUnitPlan, 0, len(units))
	lastOfTarget := map[guardKey]int{}
	seenUnit := make(map[string]bool, len(units))
	edgesOfTarget := map[guardKey][]unitIntent{}
	for i, unitID := range units {
		id, ok := index[unitID]
		if !ok {
			return nil, fmt.Errorf("sqlstore: the rollout enumerates unit %s, which this edition cannot construct; either the recorded plan and this edition disagree, or it names an intent this edition does not emit (%v)",
				unitID, guardEmittedIntents())
		}
		// NO DUPLICATES. A repeated identity would make one unit appear twice in the plan, and the
		// second copy would inherit the first's receipt — a unit verified against work it did not
		// enumerate.
		if seenUnit[unitID] {
			return nil, fmt.Errorf("sqlstore: the rollout enumerates unit %s twice", unitID)
		}
		seenUnit[unitID] = true
		// THE GRAMMAR IS `adopt -> [transition]`, per target, in that order. Anything else is a
		// lineage this engine cannot have opened: a transition with no adoption has no predecessor
		// receipt to point at, two adoptions would both claim the first edge, and a transition
		// before its adoption would enumerate a unit before the one it depends on.
		edges := edgesOfTarget[id.spec.Key]
		switch id.intent {
		case intentAdoptLegacy:
			if len(edges) != 0 {
				return nil, fmt.Errorf("sqlstore: the rollout enumerates an adoption of %s after %v; adoption is the first edge of a target and there is only one",
					id.spec.Key, edges)
			}
		case intentTransitionLegacyOToA:
			if len(edges) != 1 || edges[0] != intentAdoptLegacy {
				return nil, fmt.Errorf("sqlstore: the rollout enumerates a transition of %s after %v; a transition follows exactly one adoption of the same target",
					id.spec.Key, edges)
			}
		}
		edgesOfTarget[id.spec.Key] = append(edges, id.intent)
		p := guardUnitPlan{
			UnitID:  unitID,
			Ordinal: int64(i + 1),
			Spec:    id.spec,
			Intent:  id.intent,
		}
		// THE POSTSTATE OF AN ADOPTION IS WHAT IT ADOPTED, and only two places know it: the
		// judged reading, if the unit has run, or the current catalog, if it has not. Neither is
		// recoverable from the identity, which is a digest.
		//
		// It is also, deliberately, not load-bearing: expectedEnableState returns the JUDGED
		// state for an adoption and ignores the declared one entirely. Setting it correctly
		// anyway keeps spec.validate() satisfied and keeps a reader from having to know that.
		switch id.intent {
		case intentTransitionLegacyOToA:
			p.CanonicalEnableState = id.spec.DesiredEnableState
		default:
			p.CanonicalEnableState = guardStateOrigin
			if fold, ran := judged[unitID]; ran && fold.JudgedReadingValid && fold.JudgedReading.GuardEnableState != "" {
				p.CanonicalEnableState = fold.JudgedReading.GuardEnableState
			} else if row, seen := observed[id.spec.Key]; seen && row.GuardExists {
				p.CanonicalEnableState = row.EnableState
			}
			// A state the spec cannot declare would fail validate() later with a message about
			// the manifest rather than about the object, so it is normalised here to the one
			// value adoption can always declare. The runner still judges the real state under
			// the lock; this field is not what authorizes anything.
			if p.CanonicalEnableState != guardStateOrigin && p.CanonicalEnableState != guardStateAlways {
				p.CanonicalEnableState = guardStateOrigin
			}
		}
		if prev, seen := lastOfTarget[id.spec.Key]; seen {
			// The enumeration's order IS the lineage: a target's later edge follows its earlier
			// one, so the predecessor is whatever the enumeration listed before it.
			p.Predecessor = plans[prev].UnitID
			plans[prev].IsTerminal = false
		}
		p.IsTerminal = true
		plans = append(plans, p)
		lastOfTarget[id.spec.Key] = len(plans) - 1
	}
	// EXACTLY ONE TERMINAL PER TARGET, checked on the rebuilt plan too. It is derived here rather
	// than recorded, so a derivation that produced zero or two would leave a target whose current
	// state nothing verifies, or two poststates both required now.
	terminals := map[guardKey]int{}
	for _, p := range plans {
		if p.IsTerminal {
			terminals[p.Spec.Key]++
		}
	}
	for key, n := range terminals {
		if n != 1 {
			return nil, fmt.Errorf("sqlstore: the rebuilt plan marks %d terminal units for %s", n, key)
		}
	}
	if err := requireGuardEnumerationCoversManifest(m, edgesOfTarget, judged); err != nil {
		return nil, err
	}
	return plans, nil
}

// requireGuardEnumerationCoversManifest turns the enumeration from a SUBSET check into a
// BIJECTION with the manifest.
//
// THE HOLE THIS CLOSES was the plan language's largest: every rule above walks the units the
// enumeration DOES name, so a key it simply omits was never mentioned by anything. The terminal
// census iterates the targets present, so zero terminals for an absent key is unobservable; the
// grammar validates the order of the edges it receives, not that they cover anything; and an
// empty list satisfied all of it. The consequence was concrete — the close locks every target
// because `keys` comes from the manifest, while verifyGuardTerminals iterates `plans`, so an
// omitted target was locked, never judged, and recorded in `ready` as outside the expected set.
//
// TWO RULES, and the second is what catches an enumeration that drops only the transition:
//
//  1. every manifest key is adopted exactly once — adoption is the first edge of every lineage,
//     so a key with no adoption is a key this rollout would never look at again; and
//  2. a target whose adoption RAN and judged a state other than the entry's desired one must
//     also enumerate its transition. That is durable evidence rather than a fresh reading: the
//     judged prestate of the adoption is what the object looked like when the rollout was
//     authorized, and if it was not already where the edition wants it, the pair of edges is the
//     only lineage that could have been opened.
//
// Rule 2 is silent for a target whose adoption has not run yet, and that is honest: nothing
// durable yet says which state it was in. The boot that opens the rollout covers that case
// instead, by comparing the round trip against the plan it derived (guardPlansAgree).
func requireGuardEnumerationCoversManifest(m guardManifest, edges map[guardKey][]unitIntent, judged map[string]unitGateFold) error {
	for _, spec := range m.Specs {
		got := edges[spec.Key]
		if len(got) == 0 {
			return fmt.Errorf("sqlstore: the rollout's enumeration names no unit for %s, which this edition declares; an enumeration that omits a target authorizes every later boot to skip it",
				spec.Key)
		}
		if got[0] != intentAdoptLegacy {
			return fmt.Errorf("sqlstore: the rollout's enumeration opens %s with %s; adoption is the first edge of every lineage", spec.Key, got[0])
		}
		if len(got) > 1 {
			continue
		}
		adoptID, err := guardUnitID(m.Format, spec.Key, intentAdoptLegacy)
		if err != nil {
			return err
		}
		fold, ran := judged[adoptID]
		if !ran || !fold.JudgedReadingValid {
			continue
		}
		if fold.JudgedReading.GuardEnableState != spec.DesiredEnableState {
			return fmt.Errorf("sqlstore: the rollout adopted %s from state %q and declares %q, so its enumeration must also carry the transition; it names only the adoption",
				spec.Key, fold.JudgedReading.GuardEnableState, spec.DesiredEnableState)
		}
	}
	return nil
}

// guardEmittedIntents are the intents THIS edition can execute.
//
// It is a function rather than a comment because two places must agree about it: the plan parser,
// which refuses anything else, and the coordinator's execute, which would otherwise be the only
// thing standing between an unemitted intent and a unit that runs.
func guardEmittedIntents() []unitIntent {
	return []unitIntent{intentAdoptLegacy, intentTransitionLegacyOToA}
}

// guardPlansAgree reports whether two plans are the same plan.
//
// Compared on the ordered identities, which is what the durable enumeration records. It exists so
// the boot that OPENS a rollout proves its own round trip: the plan it derived must be the plan
// its enumeration rebuilds, and a comment claiming that was the purpose is not the same as a
// check.
func guardPlansAgree(derived, rebuilt []guardUnitPlan) error {
	if len(derived) != len(rebuilt) {
		return fmt.Errorf("the derived plan holds %d units and its durable enumeration rebuilds %d",
			len(derived), len(rebuilt))
	}
	for i := range derived {
		if derived[i].UnitID != rebuilt[i].UnitID {
			return fmt.Errorf("unit %d is %s in the derived plan and %s in the rebuild",
				i+1, derived[i].UnitID, rebuilt[i].UnitID)
		}
		if derived[i].Intent != rebuilt[i].Intent || derived[i].Spec.Key != rebuilt[i].Spec.Key {
			return fmt.Errorf("unit %d (%s) is %s on %s in the derived plan and %s on %s in the rebuild",
				i+1, derived[i].UnitID, derived[i].Intent, derived[i].Spec.Key,
				rebuilt[i].Intent, rebuilt[i].Spec.Key)
		}
		if derived[i].IsTerminal != rebuilt[i].IsTerminal || derived[i].Predecessor != rebuilt[i].Predecessor {
			return fmt.Errorf("unit %d (%s) differs in lineage: derived terminal=%v predecessor=%q, rebuilt terminal=%v predecessor=%q",
				i+1, derived[i].UnitID, derived[i].IsTerminal, derived[i].Predecessor,
				rebuilt[i].IsTerminal, rebuilt[i].Predecessor)
		}
	}
	return nil
}

// specAllowsLegacyState reports whether an entry may be adopted from this observed state.
func specAllowsLegacyState(spec guardSpec, state string) bool {
	for _, s := range spec.LegacyAllowedStates {
		if s == state {
			return true
		}
	}
	return false
}

// guardPlanUnitIDs is the ordered set of identities the plan enumerates, which is what
// pending-opened records.
func guardPlanUnitIDs(plans []guardUnitPlan) []string {
	out := make([]string, 0, len(plans))
	for _, p := range plans {
		out = append(out, p.UnitID)
	}
	return out
}
