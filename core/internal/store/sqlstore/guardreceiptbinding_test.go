// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"strings"
	"testing"
)

// guardreceiptbinding_test.go pins validateGuardReceipt, which is what stops a receipt
// attesting work that did not happen.
//
// WHY IT IS TESTED HERE AND NOT THROUGH A BOOT, said plainly because the alternative looks
// more convincing and proves less. Forging a field of a durable receipt and re-booting does
// produce a refusal — but not this one. Three layers sit in front of the binding, and they
// fire first:
//
//  1. guardRolloutReceipts recomputes each receipt_id from the row's own body, so a field
//     edited in place is refused as a broken chain;
//  2. recomputing the id to match changes the chain digest, which breaks every successor; and
//  3. forging the LAST receipt instead changes the receipt head, which the closing event's
//     checkpoint attests — so that is refused as a checkpoint mismatch.
//
// Defense in depth is the right shape, and it is exactly why a boot-level test cannot say
// whether the binding itself holds. The binding is what a database whose ledger is internally
// consistent — one written by a forger who recomputed everything — runs into, and reaching it
// means calling it.

// bindingFixture builds one plan, its rollout, the judged fold and the matching receipt.
//
// THE FOLD IS PRODUCED BY THE FOLD, not written out by hand, and that is the correction rather
// than a stylistic preference. A hand-built unitGateFold is an oracle the production code never
// touches: deleting the lines of foldOne that copy Intent and Key out of the judged event, or
// the ones that carry its spec and definition digests, left this whole table green because the
// struct it validates against was assembled here. Folding real events means the binding is
// compared against what the ledger would actually project.
func bindingFixture(t *testing.T) (guardUnitPlan, guardRolloutContext, unitGateFold, guardReceipt) {
	t.Helper()
	plan, rollout, folds, receipt, _ := bindingFixtureWithLineage(t)
	return plan, rollout, folds[plan.UnitID], receipt
}

// bindingFixtureWithLineage returns the adoption's plan/receipt plus the folded gate of a real
// two-edge history, so the transition half of the table has a lineage to point at.
func bindingFixtureWithLineage(t *testing.T) (guardUnitPlan, guardRolloutContext, map[string]unitGateFold, guardReceipt, string) {
	t.Helper()
	m := guardTestManifest(t, "audit_events", "t")
	spec := m.Specs[0]
	rollout := guardTestRollout(t)
	unitID, err := guardUnitID(m.Format, spec.Key, intentAdoptLegacy)
	if err != nil {
		t.Fatal(err)
	}
	transitionID, err := guardUnitID(m.Format, spec.Key, intentTransitionLegacyOToA)
	if err != nil {
		t.Fatal(err)
	}
	reading := rollout.bind(prestate{
		TargetExists: true, GuardPresent: true,
		GuardEnableState: guardStateOrigin, GuardMatchesCanonical: true,
	}, spec)
	digest, err := prestateDigest(reading)
	if err != nil {
		t.Fatal(err)
	}
	base := gateEvent{
		RolloutID: rollout.RolloutID, Format: rollout.Format, CodeEpoch: rollout.CodeEpoch,
		CodeSHA256: rollout.CodeSHA256, RetainedRevision: rollout.RetainedRevision,
		RetainedSHA256: rollout.RetainedSHA256,
		Phase:          gatePhasePending, Condition: gateConditionClean,
		// appendGateEvent stamps this on every event it writes, so a fixture that folds without
		// going through the writer has to carry it or it is folding a history nobody produced.
		Actor: guardActor,
	}
	proj := gateProjection{RolloutID: rollout.RolloutID, Units: map[string]unitGateFold{}}
	fold := func(mut func(*gateEvent)) {
		t.Helper()
		ev := base
		ev.EventOrdinal = int64(proj.Events + 1)
		mut(&ev)
		if err := proj.foldOne(ev); err != nil {
			t.Fatalf("fold event %d (%s): %v", ev.EventOrdinal, ev.Kind, err)
		}
		proj.Events++
	}
	fold(func(ev *gateEvent) {
		ev.Kind, ev.ExpectedUnits = gateEventPendingOpened, []string{unitID, transitionID}
	})
	for _, edge := range []struct {
		id     string
		intent unitIntent
	}{{unitID, intentAdoptLegacy}, {transitionID, intentTransitionLegacyOToA}} {
		fold(func(ev *gateEvent) {
			ev.Kind, ev.UnitID, ev.AttemptID = gateEventAttemptStarted, edge.id, "attempt-1"
			ev.Intent, ev.Key = edge.intent, spec.Key
			ev.SpecSHA256, ev.DefinitionSHA256 = someDigest(spec.SpecSHA256), someDigest(spec.DefinitionSHA256)
			ev.PrestateSHA256, ev.PrestatePresent, ev.Prestate = someDigest(digest), true, reading
		})
		fold(func(ev *gateEvent) {
			ev.Kind, ev.UnitID, ev.AttemptID = gateEventAttemptJudged, edge.id, "attempt-1"
			ev.Intent, ev.Key = edge.intent, spec.Key
			ev.SpecSHA256, ev.DefinitionSHA256 = someDigest(spec.SpecSHA256), someDigest(spec.DefinitionSHA256)
			ev.PrestateSHA256, ev.PrestatePresent, ev.Prestate = someDigest(digest), true, reading
		})
	}
	plan := guardUnitPlan{
		UnitID: unitID, Ordinal: 1, Spec: spec, Intent: intentAdoptLegacy,
		CanonicalEnableState: guardStateOrigin, IsTerminal: false,
	}
	receipt := guardReceipt{
		RolloutID: rollout.RolloutID, UnitID: unitID, Kind: guardReceiptKindUnit,
		Intent: intentAdoptLegacy, Key: spec.Key,
		Epoch: rollout.CodeEpoch, Format: rollout.Format, CodeSHA256: rollout.CodeSHA256,
		RetainedRevision: rollout.RetainedRevision, RetainedSHA256: rollout.RetainedSHA256,
		SpecSHA256: spec.SpecSHA256, DefinitionSHA256: spec.DefinitionSHA256,
		PrestateSHA256: digest, ToEnableState: guardStateOrigin, AttemptID: "attempt-1",
	}
	return plan, rollout, proj.Units, receipt, transitionID
}

// TestGuardReceiptBindingRefusesEveryFieldThatCanBeForged.
func TestGuardReceiptBindingRefusesEveryFieldThatCanBeForged(t *testing.T) {
	t.Parallel()
	plan, rollout, fold, receipt := bindingFixture(t)
	if err := validateGuardReceipt(plan, rollout, fold, receipt); err != nil {
		t.Fatalf("the matching receipt was refused: %v", err)
	}

	for _, tc := range []struct {
		name string
		mut  func(r *guardReceipt)
		want string
	}{
		{"the receipt kind", func(r *guardReceipt) { r.Kind = guardReceiptKindBootstrap }, "receipt_kind"},
		{"the intent", func(r *guardReceipt) { r.Intent = intentTransitionLegacyOToA }, "intent"},
		{"the schema", func(r *guardReceipt) { r.Key.Schema = "elsewhere" }, "relation_schema"},
		{"the relation", func(r *guardReceipt) { r.Key.Relation = "somebody_elses_table" }, "relation_name"},
		{"the trigger", func(r *guardReceipt) { r.Key.Trigger = "somebody_elses_trigger" }, "trigger_name"},
		{"the unit", func(r *guardReceipt) { r.UnitID = "unit-somewhere-else" }, "unit_id"},
		{"the rollout", func(r *guardReceipt) { r.RolloutID = "rollout-somewhere-else" }, "rollout_id"},
		{"the epoch", func(r *guardReceipt) { r.Epoch++ }, "epoch"},
		{"the manifest format", func(r *guardReceipt) { r.Format++ }, "manifest_format"},
		{"the code digest", func(r *guardReceipt) { r.CodeSHA256[0] ^= 0xff }, "code_sha256"},
		{"the retained revision", func(r *guardReceipt) { r.RetainedRevision++ }, "retained_revision"},
		{"the retained digest", func(r *guardReceipt) { r.RetainedSHA256[0] ^= 0xff }, "retained_sha256"},
		{"the spec digest", func(r *guardReceipt) { r.SpecSHA256[0] ^= 0xff }, "spec_sha256"},
		{"the definition digest", func(r *guardReceipt) { r.DefinitionSHA256[0] ^= 0xff }, "definition_sha256"},
		{"the poststate", func(r *guardReceipt) { r.ToEnableState = guardStateAlways }, "to_enable_state"},
		// THE TWO THAT BIND IT TO ITS AUTHORISATION. A receipt says "I changed this"; the judged
		// event says "this is the reading that authorized it". Without the attempt, a receipt
		// could belong to a different attempt of the same unit; without the prestate, to work
		// authorized by a reading nobody recorded.
		{"the attempt", func(r *guardReceipt) { r.AttemptID = "forged" }, "attempt"},
		{"the judged reading", func(r *guardReceipt) { r.PrestateSHA256[0] ^= 0xff }, "hashing to"},
		// A from-state on an edge that never has one: only the transition records where it came
		// from, so an adoption carrying one is claiming a move it did not make.
		{"a from-state on an adoption", func(r *guardReceipt) { r.FromEnableState = someText(guardStateOrigin) }, "from-state"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mutated := receipt
			tc.mut(&mutated)
			err := validateGuardReceipt(plan, rollout, fold, mutated)
			if err == nil {
				t.Fatalf("a receipt with %s forged was accepted as this unit's attribution", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("the refusal does not name %q: %v", tc.want, err)
			}
		})
	}

	// AND THE AUTHORISATION MUST EXIST AT ALL. A unit whose gate fold holds no judged reading
	// has a receipt attesting work nothing authorized, and that is a refusal rather than a
	// receipt taken at face value.
	for _, tc := range []struct {
		name string
		fold unitGateFold
	}{
		{"no judged reading", unitGateFold{State: unitGateJudged, AttemptID: "attempt-1", JudgedPrestate: fold.JudgedPrestate}},
		{"no judged prestate", unitGateFold{State: unitGateJudged, AttemptID: "attempt-1", JudgedReadingValid: true}},
		{"an empty fold", unitGateFold{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateGuardReceipt(plan, rollout, tc.fold, receipt); err == nil {
				t.Fatal("a receipt with no judged event behind it was accepted")
			} else if !strings.Contains(err.Error(), "authorized") && !strings.Contains(err.Error(), "attempt") {
				t.Errorf("the refusal does not say the authorisation is missing: %v", err)
			}
		})
	}

	// THE JUDGED EVENT'S OWN SUBJECT, which neither the attempt nor the prestate digest pins.
	// A digest is computed from the reading and says nothing about which entry the reading
	// belongs to, so these four are the difference between "bound to an authorisation" and
	// "bound to a number the authorisation happens to share".
	for _, tc := range []struct {
		name string
		mut  func(f *unitGateFold)
		want string
	}{
		{"the judged intent", func(f *unitGateFold) { f.Intent = intentTransitionLegacyOToA }, "intent"},
		{"the judged key", func(f *unitGateFold) { f.Key.Relation = "somebody_elses_table" }, "judged event about"},
		{"the judged entry digest", func(f *unitGateFold) { f.JudgedSpecSHA256 = optDigest{} }, "recording entry"},
		{"the judged object digest", func(f *unitGateFold) { f.JudgedDefinitionSHA256.D[0] ^= 0xff }, "recording object"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mutated := fold
			tc.mut(&mutated)
			err := validateGuardReceipt(plan, rollout, mutated, receipt)
			if err == nil {
				t.Fatalf("a receipt bound to a judged event with %s forged was accepted", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("the refusal does not name %q: %v", tc.want, err)
			}
		})
	}

	// A FIRST EDGE MAY NOT CARRY A PREDECESSOR. The check used to run only when the plan named
	// one, so an adoption pointing at an arbitrary receipt id was accepted as the start of a
	// lineage that says it has no start.
	t.Run("an adoption carrying a predecessor", func(t *testing.T) {
		mutated := receipt
		mutated.PredecessorReceiptID = someDigest(receipt.PrestateSHA256)
		if err := validateGuardReceipt(plan, rollout, fold, mutated); err == nil {
			t.Fatal("an adoption whose receipt records a predecessor was accepted as the first edge")
		} else if !strings.Contains(err.Error(), "first edge") {
			t.Errorf("the refusal does not say it is the first edge: %v", err)
		}
	})

	// The transition's from-state is checked against the JUDGED reading, not against a
	// constant: a transition that lost it could not be told from one that never had it.
	_, _, folds, _, transitionID := bindingFixtureWithLineage(t)
	tplan := plan
	tplan.UnitID = transitionID
	tplan.Intent, tplan.CanonicalEnableState, tplan.IsTerminal = intentTransitionLegacyOToA, guardStateAlways, true
	tplan.Predecessor = plan.UnitID
	tfold := folds[transitionID]
	treceipt := receipt
	treceipt.UnitID = transitionID
	treceipt.Intent, treceipt.ToEnableState = intentTransitionLegacyOToA, guardStateAlways
	treceipt.FromEnableState = someText(guardStateOrigin)
	treceipt.PredecessorReceiptID = someDigest(receipt.PrestateSHA256)
	if err := validateGuardReceipt(tplan, rollout, tfold, treceipt); err != nil {
		t.Fatalf("a correct transition receipt was refused: %v", err)
	}
	for _, tc := range []struct {
		name string
		mut  func(r *guardReceipt)
	}{
		{"the from-state is dropped", func(r *guardReceipt) { r.FromEnableState = optText{} }},
		{"the from-state claims the poststate", func(r *guardReceipt) { r.FromEnableState = someText(guardStateAlways) }},
		{"the from-state claims a state nothing judged", func(r *guardReceipt) { r.FromEnableState = someText("D") }},
		// AND THE OTHER DIRECTION OF THE LINEAGE CHECK: a successor edge that records no
		// predecessor at all, which the plan says it must have.
		{"the predecessor is dropped", func(r *guardReceipt) { r.PredecessorReceiptID = optDigest{} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mutated := treceipt
			tc.mut(&mutated)
			if err := validateGuardReceipt(tplan, rollout, tfold, mutated); err == nil {
				t.Fatalf("a transition receipt where %s was accepted", tc.name)
			}
		})
	}
}

// TestGuardReceiptCensusRequiresTheExactSet pins verifyGuardReceiptCensus.
//
// EVERY OTHER CHECK WALKS THE PLAN and asks the ledger about each entry, so a receipt filed
// under this rollout for a unit the plan never enumerates was read by nothing and verified by
// nothing — and counted by the checkpoint, which is what made it permanent. The census walks the
// LEDGER instead, and it is the only thing that does.
func TestGuardReceiptCensusRequiresTheExactSet(t *testing.T) {
	t.Parallel()
	m := guardTestManifest(t, "audit_events", "rrw_widget")
	rollout := guardTestRollout(t)
	plans := make([]guardUnitPlan, 0, len(m.Specs))
	receipts := map[string]guardReceipt{}
	for i, spec := range m.Specs {
		id, err := guardUnitID(m.Format, spec.Key, intentAdoptLegacy)
		if err != nil {
			t.Fatal(err)
		}
		plans = append(plans, guardUnitPlan{
			UnitID: id, Ordinal: int64(i + 1), Spec: spec, Intent: intentAdoptLegacy,
			CanonicalEnableState: guardStateAlways, IsTerminal: true,
		})
		receipts[receiptLookupKey(id, guardReceiptKindUnit)] = guardReceipt{
			RolloutID: rollout.RolloutID, UnitID: id, Kind: guardReceiptKindUnit,
			Intent: intentAdoptLegacy, Key: spec.Key,
		}
	}

	if err := verifyGuardReceiptCensus(rollout, plans, receipts); err != nil {
		t.Fatalf("the exact set the plan enumerates was refused: %v", err)
	}

	// THE THREE METADATA BOOTSTRAP RECEIPTS ARE ALLOWED AND NOT REQUIRED. They belong to the migration,
	// and a rollout opened over a later retained pair legitimately holds none of them.
	withBootstrap := map[string]guardReceipt{}
	for k, v := range receipts {
		withBootstrap[k] = v
	}
	metaSpecs, err := guardMetadataSpecs(rollout.Format)
	if err != nil {
		t.Fatal(err)
	}
	for _, spec := range metaSpecs {
		id, ierr := guardBootstrapUnitID(rollout.Format, spec.Key)
		if ierr != nil {
			t.Fatal(ierr)
		}
		withBootstrap[receiptLookupKey(id, guardReceiptKindBootstrap)] = guardReceipt{
			RolloutID: rollout.RolloutID, UnitID: id, Kind: guardReceiptKindBootstrap, Key: spec.Key,
		}
	}
	if err := verifyGuardReceiptCensus(rollout, plans, withBootstrap); err != nil {
		t.Errorf("the migration's own bootstrap receipts were refused: %v", err)
	}

	for _, tc := range []struct {
		name string
		mut  func(map[string]guardReceipt)
		want string
	}{{
		name: "a receipt for a unit the plan does not enumerate",
		mut: func(r map[string]guardReceipt) {
			// A well-formed identity for an EDGE the plan does not contain: the transition of a
			// target whose adoption it does.
			id, err := guardUnitID(m.Format, m.Specs[0].Key, intentTransitionLegacyOToA)
			if err != nil {
				t.Fatal(err)
			}
			r[receiptLookupKey(id, guardReceiptKindUnit)] = guardReceipt{
				RolloutID: rollout.RolloutID, UnitID: id, Kind: guardReceiptKindUnit,
				Intent: intentTransitionLegacyOToA, Key: m.Specs[0].Key,
			}
		},
		want: "does not enumerate",
	}, {
		name: "a bootstrap receipt for a relation that is not the control plane",
		mut: func(r map[string]guardReceipt) {
			id, err := guardBootstrapUnitID(rollout.Format, m.Specs[0].Key)
			if err != nil {
				t.Fatal(err)
			}
			r[receiptLookupKey(id, guardReceiptKindBootstrap)] = guardReceipt{
				RolloutID: rollout.RolloutID, UnitID: id, Kind: guardReceiptKindBootstrap, Key: m.Specs[0].Key,
			}
		},
		want: "does not enumerate",
	}, {
		name: "a receipt the plan requires is missing",
		mut: func(r map[string]guardReceipt) {
			delete(r, receiptLookupKey(plans[0].UnitID, guardReceiptKindUnit))
		},
		want: "missing the receipt",
	}} {
		t.Run(tc.name, func(t *testing.T) {
			mutated := map[string]guardReceipt{}
			for k, v := range receipts {
				mutated[k] = v
			}
			tc.mut(mutated)
			err := verifyGuardReceiptCensus(rollout, plans, mutated)
			if err == nil {
				t.Fatalf("the census accepted a ledger with %s", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("the refusal does not say %q: %v", tc.want, err)
			}
		})
	}
}
