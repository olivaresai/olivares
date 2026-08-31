// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"strings"
	"testing"
)

// guardunits_test.go pins the plan factory: which edges an observation authorizes, and which
// observations authorize none.

// guardTestManifest builds a manifest over the given tables.
func guardTestManifest(t *testing.T, tables ...string) guardManifest {
	t.Helper()
	m, err := buildGuardManifest(tables)
	if err != nil {
		t.Fatalf("build the manifest: %v", err)
	}
	return m
}

// canonicalRowFor renders the catalog reading a canonical guard in the given state produces.
func canonicalRowFor(t *testing.T, spec guardSpec, state string) guardCatalogRow {
	t.Helper()
	def := canonicalGuardDefinition()
	return guardCatalogRow{
		Key:            spec.Key,
		RelationExists: true,
		RelationOID:    "1",
		Relation:       def.Relation,
		GuardExists:    true,
		TriggerOID:     "2",
		Trigger:        def.Trigger,
		EnableState:    state,
		FunctionExists: true,
		Function:       def.Function,
	}
}

// TestGuardPlanDerivesTheEdgesFromWhatItObserved is the factory's central property.
//
// A guard at ORIGIN needs adoption AND the O -> A transition; one already at ALWAYS needs
// adoption alone. Enumerating both edges unconditionally would put a transition in the plan
// whose precondition the object contradicts — expectedEnableState authorizes O -> A only from
// 'O' — so a legitimate database in a legitimate state would fail its boot.
func TestGuardPlanDerivesTheEdgesFromWhatItObserved(t *testing.T) {
	t.Parallel()
	m := guardTestManifest(t, "audit_events", "t_origin", "t_always")
	observed := map[guardKey]guardCatalogRow{}
	states := map[string]string{
		"audit_events": guardStateOrigin,
		"t_origin":     guardStateOrigin,
		"t_always":     guardStateAlways,
	}
	for _, spec := range m.Specs {
		observed[spec.Key] = canonicalRowFor(t, spec, states[spec.Key.Relation])
	}

	plans, refusals, err := buildGuardUnitPlans(m, observed)
	if err != nil {
		t.Fatalf("build the plan: %v", err)
	}
	if len(refusals) != 0 {
		t.Fatalf("three canonical guards produced %d refusals: %v", len(refusals), refusals)
	}
	// Two entries at 'O' contribute two edges each; the one at 'A' contributes one.
	if want := 2*2 + 1; len(plans) != want {
		t.Fatalf("the plan holds %d units, want %d", len(plans), want)
	}

	byKey := map[string][]unitIntent{}
	for _, p := range plans {
		byKey[p.Spec.Key.Relation] = append(byKey[p.Spec.Key.Relation], p.Intent)
	}
	for _, rel := range []string{"audit_events", "t_origin"} {
		got := byKey[rel]
		if len(got) != 2 || got[0] != intentAdoptLegacy || got[1] != intentTransitionLegacyOToA {
			t.Errorf("%s at ORIGIN produced %v, want adoption then transition", rel, got)
		}
	}
	if got := byKey["t_always"]; len(got) != 1 || got[0] != intentAdoptLegacy {
		t.Errorf("t_always at ALWAYS produced %v, want adoption alone; inventing an O -> A edge from 'A' is the unauthorized transition the matrix refuses", got)
	}

	// Ordinals are dense and 1-based, and exactly one edge per target is terminal.
	terminals := map[string]int{}
	for i, p := range plans {
		if p.Ordinal != int64(i+1) {
			t.Errorf("unit %d carries ordinal %d", i, p.Ordinal)
		}
		if p.IsTerminal {
			terminals[p.Spec.Key.Relation]++
		}
	}
	for rel, n := range terminals {
		if n != 1 {
			t.Errorf("%s has %d terminal units, want exactly 1", rel, n)
		}
	}
	if len(terminals) != 3 {
		t.Errorf("%d targets have a terminal unit, want 3", len(terminals))
	}
	// The transition's lineage names the adoption it follows — and names it EXACTLY.
	//
	// "Not empty" was the whole assertion here, and it is the one a mutation walks straight
	// past: writing any non-empty string as the predecessor kept this green while the lineage
	// pointed at a unit that does not exist. The identity is derivable — it is the adoption's
	// unit id for the same key — so the test derives it independently rather than reading it
	// back from the plan it is checking.
	adoptions := map[guardKey]string{}
	for _, p := range plans {
		if p.Intent == intentAdoptLegacy {
			adoptions[p.Spec.Key] = p.UnitID
		}
	}
	for _, p := range plans {
		switch p.Intent {
		case intentAdoptLegacy:
			if p.Predecessor != "" {
				t.Errorf("the adoption of %s declares predecessor %s; it is the first edge", p.Spec.Key, p.Predecessor)
			}
			want, err := guardUnitID(m.Format, p.Spec.Key, intentAdoptLegacy)
			if err != nil {
				t.Fatal(err)
			}
			if p.UnitID != want {
				t.Errorf("the adoption of %s carries unit id %s, want %s", p.Spec.Key, p.UnitID, want)
			}
		case intentTransitionLegacyOToA:
			want, err := guardUnitID(m.Format, p.Spec.Key, intentAdoptLegacy)
			if err != nil {
				t.Fatal(err)
			}
			if p.Predecessor != want {
				t.Errorf("the transition of %s declares predecessor %q, want the adoption of the SAME key (%s); any other value is a lineage pointing at work that did not happen",
					p.Spec.Key, p.Predecessor, want)
			}
			if p.Predecessor != adoptions[p.Spec.Key] {
				t.Errorf("the transition of %s does not name the adoption present in this same plan (%s)",
					p.Spec.Key, adoptions[p.Spec.Key])
			}
			own, err := guardUnitID(m.Format, p.Spec.Key, intentTransitionLegacyOToA)
			if err != nil {
				t.Fatal(err)
			}
			if p.UnitID != own {
				t.Errorf("the transition of %s carries unit id %s, want %s", p.Spec.Key, p.UnitID, own)
			}
			if p.UnitID == p.Predecessor {
				t.Errorf("the transition of %s is its own predecessor", p.Spec.Key)
			}
		}
	}
}

// TestGuardPlanRefusesRatherThanRepairs pins that every unusable observation produces a NAMED
// refusal and no unit.
//
// The absent-guard case is the one worth stating: it does NOT become a create-guard. Every
// target of this edition has its guard emitted by the DDL that creates the table, so an absent
// guard means somebody removed one — and creating it here would launder that removal into
// "managed". Refusing keeps the evidence.
func TestGuardPlanRefusesRatherThanRepairs(t *testing.T) {
	t.Parallel()
	m := guardTestManifest(t, "t")
	spec := m.Specs[0]

	mutate := func(f func(r *guardCatalogRow)) map[guardKey]guardCatalogRow {
		row := canonicalRowFor(t, spec, guardStateOrigin)
		f(&row)
		return map[guardKey]guardCatalogRow{spec.Key: row}
	}

	cases := []struct {
		name string
		obs  map[guardKey]guardCatalogRow
		code string
	}{
		{"the relation is absent", mutate(func(r *guardCatalogRow) { r.RelationExists = false }), guardRefusalRelationMissing},
		{"a leaf partition", mutate(func(r *guardCatalogRow) { r.Relation.IsPartition = true }), guardRefusalRelationInvalid},
		{"an inheritance parent", mutate(func(r *guardCatalogRow) { r.Relation.HasChild = true }), guardRefusalRelationInvalid},
		{"an inheritance child", mutate(func(r *guardCatalogRow) { r.Relation.HasParent = true }), guardRefusalRelationInvalid},
		{"an unlogged table", mutate(func(r *guardCatalogRow) { r.Relation.Persistence = "u" }), guardRefusalRelationInvalid},
		{"a view", mutate(func(r *guardCatalogRow) { r.Relation.Kind = "v" }), guardRefusalRelationInvalid},
		{"the guard is absent", mutate(func(r *guardCatalogRow) { r.GuardExists = false }), guardRefusalGuardMissing},
		{"a column-limited lookalike", mutate(func(r *guardCatalogRow) { r.Trigger.AttrCount = 1 }), guardRefusalGuardLookalike},
		{"a different function body", mutate(func(r *guardCatalogRow) { r.Function.Src = "\nBEGIN\n  RETURN NEW;\nEND;\n" }), guardRefusalGuardLookalike},
		{"a security-definer function", mutate(func(r *guardCatalogRow) { r.Function.SecurityDefiner = true }), guardRefusalGuardLookalike},
		{"a disabled guard", mutate(func(r *guardCatalogRow) { r.EnableState = guardStateDisable }), guardRefusalGuardStateUnsafe},
		{"a replica-only guard", mutate(func(r *guardCatalogRow) { r.EnableState = guardStateReplica }), guardRefusalGuardStateUnsafe},
	}
	for _, tc := range cases {
		plans, refusals, err := buildGuardUnitPlans(m, tc.obs)
		if err != nil {
			t.Errorf("%s: the factory errored instead of refusing: %v", tc.name, err)
			continue
		}
		if len(plans) != 0 {
			t.Errorf("%s: produced %d units", tc.name, len(plans))
		}
		if len(refusals) != 1 {
			t.Errorf("%s: produced %d refusals, want 1", tc.name, len(refusals))
			continue
		}
		if refusals[0].Code != tc.code {
			t.Errorf("%s: refused with %s, want %s (%s)", tc.name, refusals[0].Code, tc.code, refusals[0].Detail)
		}
	}
}

// TestGuardPlanRefusesAnIncompleteBatch pins that a missing reading is not read as an absent
// relation.
//
// The bulk projection returns a row for EVERY target, so a key with no entry means the batch
// was truncated. Treating that as "the relation is absent" would turn a lost row into a
// refusal about the database instead of a refusal about the reading.
func TestGuardPlanRefusesAnIncompleteBatch(t *testing.T) {
	t.Parallel()
	m := guardTestManifest(t, "t", "u")
	only := map[guardKey]guardCatalogRow{m.Specs[0].Key: canonicalRowFor(t, m.Specs[0], guardStateOrigin)}
	_, _, err := buildGuardUnitPlans(m, only)
	if err == nil {
		t.Fatal("a truncated batch was accepted")
	}
	if !strings.Contains(err.Error(), "truncated") {
		t.Errorf("the refusal was %q, which does not say the batch was truncated", err)
	}
}

// TestGuardPlanRefusesAReadingItDidNotAskFor is the other direction.
//
// A reading for a key the manifest does not declare means the caller built the batch from
// something other than the manifest, and the plan's cardinality is what the durable
// enumeration is later checked against.
func TestGuardPlanRefusesAReadingItDidNotAskFor(t *testing.T) {
	t.Parallel()
	m := guardTestManifest(t, "t")
	obs := map[guardKey]guardCatalogRow{
		m.Specs[0].Key: canonicalRowFor(t, m.Specs[0], guardStateOrigin),
		{Schema: guardSchema, Relation: "stranger", Trigger: "stranger" + guardTriggerSuffix}: {},
	}
	if _, _, err := buildGuardUnitPlans(m, obs); err == nil {
		t.Fatal("a reading for an undeclared key was accepted")
	}
}

// TestGuardLockPlanValidates pins that the plan every unit shares is a legal one.
//
// It is worth a test rather than an assumption because lockPlan.validate enforces four
// properties this plan has to satisfy at once: a sorted metadata prefix, no duplicate
// relation, a target the metadata modes are covered by, and a target statement that is
// EXACTLY the generated one. Any of them failing would refuse every unit at runtime.
func TestGuardLockPlanValidates(t *testing.T) {
	t.Parallel()
	for _, intent := range []unitIntent{intentAdoptLegacy, intentTransitionLegacyOToA} {
		plan := guardLockPlan("audit_events", intent)
		if err := plan.validate(); err != nil {
			t.Fatalf("the %s lock plan is invalid: %v", intent, err)
		}
		if len(plan.Metadata) != 3 {
			t.Errorf("%s: the metadata prefix holds %d relations, want the three control-plane logs", intent, len(plan.Metadata))
		}
		for _, m := range plan.Metadata {
			if m.Mode != lockModeRowExclusive {
				t.Errorf("%s: %s is taken at %s; ROW EXCLUSIVE is the only mode a self-revoked append-only table allows", intent, m, m.Mode)
			}
		}
		// THE SENTINEL IS ALWAYS ROW EXCLUSIVE, whatever the plan declares.
		//
		// This is the measured constraint, not a preference: LOCK TABLE checks privileges per
		// mode, and every mode above ROW EXCLUSIVE needs UPDATE, DELETE or TRUNCATE — which an
		// append-only table has revoked, with ownership granting no exemption. A plan whose
		// sentinel asked for more failed 42501 on the default topology before any unit ran.
		if got := plan.acquireMode(); got != lockModeRowExclusive {
			t.Errorf("%s: the sentinel takes %s; only ROW EXCLUSIVE is available on a guarded table", intent, got)
		}
		if plan.TargetStatement != plan.targetAcquireStatement() {
			t.Errorf("%s: the target statement %q is not the generated %q",
				intent, plan.TargetStatement, plan.targetAcquireStatement())
		}
		if !strings.Contains(plan.TargetStatement, " ONLY ") {
			t.Errorf("%s: the target statement %q does not say ONLY, so a partitioned parent would pull in every partition",
				intent, plan.TargetStatement)
		}
		// The DECLARED mode is what the unit ends up holding, and it differs by intent:
		// adoption performs no DDL, the transition's ALTER escalates to SHARE ROW EXCLUSIVE.
		wantDeclared := lockModeRowExclusive
		if intent == intentTransitionLegacyOToA {
			wantDeclared = lockModeShareRowExclusive
		}
		if plan.Target.Mode != wantDeclared {
			t.Errorf("%s: the target declares %s, want %s", intent, plan.Target.Mode, wantDeclared)
		}
		// And the acquisition view of the same plan must also validate, since that is what the
		// first footprint check is compared against.
		if err := plan.acquisitionPlan().validate(); err != nil {
			t.Errorf("%s: the acquisition view of the plan is invalid: %v", intent, err)
		}
	}
}

// TestLockPlanRefusesAnUncoveredSentinel pins the one rule TargetAcquire must obey.
//
// A sentinel mode the DECLARED mode does not cover would mean the pre-commit footprint
// authorizes less than the acquisition already took — the check reading its own weaker claim
// and passing.
func TestLockPlanRefusesAnUncoveredSentinel(t *testing.T) {
	t.Parallel()
	tgt := plannedLock{Schema: guardSchema, Name: "t", Mode: lockModeRowExclusive}
	sentinel := lockModeShareRowExclusive
	plan := lockPlan{Target: tgt, TargetAcquire: &sentinel}
	plan.TargetStatement = plan.targetAcquireStatement()
	if err := plan.validate(); err == nil {
		t.Fatal("a plan whose sentinel takes more than it declares was accepted")
	} else if !strings.Contains(err.Error(), "does not cover") {
		t.Errorf("refused with %q, which does not name the coverage rule", err)
	}

	bogus := lockMode(99)
	plan2 := lockPlan{Target: tgt, TargetAcquire: &bogus}
	plan2.TargetStatement = plan2.targetAcquireStatement()
	if err := plan2.validate(); err == nil {
		t.Error("a plan with an unknown sentinel mode was accepted")
	}
}

// TestGuardCatalogBatchFoldRefusesAnIncoherentReading exercises the batch's completeness rules
// DIRECTLY, which is the only way anything exercises them.
//
// The PostgreSQL test over the bulk query asserts one row per target and the right contents.
// It never observes an ordinal, because the real query returns them in order — so deleting the
// ordinal check from production code left that test green. These checks decide whether a
// reading may be compared with a plan at all, and a check nothing can falsify is a check
// nothing is holding.
func TestGuardCatalogBatchFoldRefusesAnIncoherentReading(t *testing.T) {
	t.Parallel()
	m := guardTestManifest(t, "audit_events", "t")
	keys := []guardKey{m.Specs[0].Key, m.Specs[1].Key}
	rowFor := func(spec guardSpec) guardCatalogRow {
		return canonicalRowFor(t, spec, guardStateAlways)
	}
	good := []guardBatchRow{
		{Ordinal: 1, Row: rowFor(m.Specs[0])},
		{Ordinal: 2, Row: rowFor(m.Specs[1])},
	}

	out, err := foldGuardCatalogBatch(good, keys)
	if err != nil {
		t.Fatalf("a complete, ordered batch was refused: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("the fold produced %d readings for 2 targets", len(out))
	}
	for _, k := range keys {
		if _, ok := out[k]; !ok {
			t.Errorf("the fold lost %s", k)
		}
	}

	foreign := m.Specs[1]
	foreign.Key.Relation = "somebody_elses_table"

	for _, tc := range []struct {
		name string
		read []guardBatchRow
		want string
	}{{
		name: "the first ordinal is not 1",
		read: []guardBatchRow{{Ordinal: 2, Row: rowFor(m.Specs[0])}, {Ordinal: 3, Row: rowFor(m.Specs[1])}},
		want: "ordinal 2 where 1 was expected",
	}, {
		name: "an ordinal is skipped",
		read: []guardBatchRow{{Ordinal: 1, Row: rowFor(m.Specs[0])}, {Ordinal: 3, Row: rowFor(m.Specs[1])}},
		want: "ordinal 3 where 2 was expected",
	}, {
		name: "the rows arrive out of order",
		read: []guardBatchRow{{Ordinal: 2, Row: rowFor(m.Specs[1])}, {Ordinal: 1, Row: rowFor(m.Specs[0])}},
		want: "ordinal 2 where 1 was expected",
	}, {
		name: "the same target comes back twice",
		read: []guardBatchRow{{Ordinal: 1, Row: rowFor(m.Specs[0])}, {Ordinal: 2, Row: rowFor(m.Specs[0])}},
		want: "twice",
	}, {
		name: "a target is missing",
		read: []guardBatchRow{{Ordinal: 1, Row: rowFor(m.Specs[0])}},
		want: "1 rows for 2 targets",
	}, {
		// The count matches and one key does not. Without the per-key check this reads as a
		// complete batch, and the target it displaced looks like a relation that is simply
		// absent — which the plan then refuses for the wrong reason, or adopts for one.
		name: "a target is substituted for one nobody asked about",
		read: []guardBatchRow{{Ordinal: 1, Row: rowFor(m.Specs[0])}, {Ordinal: 2, Row: rowFor(foreign)}},
		want: "holds no reading for",
	}} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := foldGuardCatalogBatch(tc.read, keys)
			if err == nil {
				t.Fatal("the fold accepted a reading that cannot be compared with a plan")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("the refusal does not say %q: %v", tc.want, err)
			}
		})
	}
}
