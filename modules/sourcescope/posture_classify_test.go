// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sourcescope

import (
	"reflect"
	"sort"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

// posture_classify_test.go pins the whitelist in classifyUpdate. It is the only
// IN-PACKAGE test file of the module (the other ten are `sourcescope_test`) because the
// three things it asserts are unreachable from outside: the unexported classifier, the
// unexported bindingDTO, and containsActor. The end-to-end proof that a shrinking forbid
// now returns 202 and is NOT applied lives in posture_test.go, on the real HTTP path.
//
// What is asserted here, and why each one earns its place:
//
//   - the transition TABLE — the readable spec, one hand-reasoned verdict per interesting
//     shape, including the two holes closed;
//   - MONOTONICITY against the verbatim pre classifier over the whole input space —
//     the machine-checked form of "this change only ever ADDED a gate";
//   - the whitelist DEFAULT is unreachable — the enumeration really is exhaustive, so the
//     conservative fallback is a guard for the next edit and not a live behavior;
//   - the POPULATION oracle — the premise the policy rests on, computed with the real
//     containsActor: two different scopes are not nested, so there is nothing to compare
//     them by and identity is the only certificate;
//   - a TRIPWIRE on bindingDTO's shape, so a new field cannot silently join the wire
//     without someone deciding whether it decides access.

// bd builds a binding DTO with only the fields that decide access.
func bd(tree, ref, effect string, enabled bool) bindingDTO {
	return bindingDTO{ScopeTree: tree, ScopeRef: ref, Effect: effect, Enabled: enabled}
}

// --- the readable spec ---------------------------------------------------------------

// TestClassifyUpdateTable is the hand-reasoned verdict for every interesting shape of an
// update. Rows 2, 3 and 12 are the two holes closed; the rest pin the behavior that
// must NOT change, so a whitelist that simply gated everything would fail here.
func TestClassifyUpdateTable(t *testing.T) {
	const (
		wsDefault = model.DefaultWorkspaceSlug
	)
	cases := []struct {
		name        string
		old, upd    bindingDTO
		otherAllows int
		wantRelax   bool
	}{
		// --- FORBID: the polarity ADR-0022 §5 had inverted ---------------------------
		{"forbid: note edit on a standing restriction", bd(scopeUser, "u1", effectForbid, true), bd(scopeUser, "u1", effectForbid, true), 0, false},
		{"forbid: scope moved within the tree", bd(scopeUser, "u1", effectForbid, true), bd(scopeUser, "u2", effectForbid, true), 0, true},
		{"forbid: scope moved across trees", bd(scopeUser, "u1", effectForbid, true), bd(scopeWorkspace, "eng", effectForbid, true), 0, true},
		{"forbid: disabled", bd(scopeUser, "u1", effectForbid, true), bd(scopeUser, "u1", effectForbid, false), 0, true},
		{"forbid: flipped to allow", bd(scopeUser, "u1", effectForbid, true), bd(scopeUser, "u1", effectAllow, true), 0, true},
		{"forbid: enabled from parked", bd(scopeUser, "u1", effectForbid, false), bd(scopeUser, "u1", effectForbid, true), 0, false},
		{"forbid: enabled from parked, elsewhere", bd(scopeUser, "u1", effectForbid, false), bd(scopeUser, "u2", effectForbid, true), 0, false},
		{"forbid: parked row moved", bd(scopeUser, "u1", effectForbid, false), bd(scopeUser, "u2", effectForbid, false), 0, false},
		{"forbid: parked row flipped to allow (gate kept)", bd(scopeUser, "u1", effectForbid, false), bd(scopeUser, "u1", effectAllow, false), 0, true},

		// --- ALLOW -------------------------------------------------------------------
		{"allow: note edit", bd(scopeUser, "u1", effectAllow, true), bd(scopeUser, "u1", effectAllow, true), 1, false},
		{"allow: moved within the tree", bd(scopeUser, "u1", effectAllow, true), bd(scopeUser, "u2", effectAllow, true), 1, true},
		{"allow: moved to a MORE-specific tree", bd(scopeWorkspace, "eng", effectAllow, true), bd(scopeUser, "u1", effectAllow, true), 1, true},
		{"allow: broadened to a less-specific tree", bd(scopeUser, "u1", effectAllow, true), bd(scopeWorkspace, "eng", effectAllow, true), 1, true},
		{"allow: flipped to forbid", bd(scopeUser, "u1", effectAllow, true), bd(scopeUser, "u1", effectForbid, true), 1, false},
		{"allow: flipped to forbid, elsewhere", bd(scopeUser, "u1", effectAllow, true), bd(scopeWorkspace, "eng", effectForbid, true), 1, false},
		{"allow: non-last disabled", bd(scopeUser, "u1", effectAllow, true), bd(scopeUser, "u1", effectAllow, false), 1, false},
		{"allow: LAST disabled (source goes global)", bd(scopeUser, "u1", effectAllow, true), bd(scopeUser, "u1", effectAllow, false), 0, true},

		// The LAST enabled allow also stops being one when it is turned into a FORBID, and the
		// resolver cannot tell the two spellings apart: both leave zero enabled allows, so the
		// source is unconfined and global. The panel found this leg still open after the first
		// Pass — the whitelist had reasoned about the row's effect and forgotten that the
		// row was also the confinement signal. These four rows are what pin it shut.
		{"allow: LAST became a forbid (source goes global)", bd(scopeUser, "u1", effectAllow, true), bd(scopeUser, "u1", effectForbid, true), 0, true},
		{"allow: LAST became a forbid elsewhere", bd(scopeUser, "u1", effectAllow, true), bd(scopeWorkspace, "eng", effectForbid, true), 0, true},
		{"allow: LAST became a DISABLED forbid", bd(scopeUser, "u1", effectAllow, true), bd(scopeUser, "u1", effectForbid, false), 0, true},
		{"allow: non-last became a disabled forbid", bd(scopeUser, "u1", effectAllow, true), bd(scopeUser, "u1", effectForbid, false), 1, false},
		{"allow: enabled from parked", bd(scopeUser, "u1", effectAllow, false), bd(scopeUser, "u1", effectAllow, true), 1, true},
		{"allow: parked row moved", bd(scopeUser, "u1", effectAllow, false), bd(scopeUser, "u2", effectAllow, false), 1, false},
		{"allow: legacy empty effect is an allow", bd(scopeUser, "u1", "", true), bd(scopeUser, "u2", "", true), 1, true},

		// --- the default workspace is ONE population, however it is spelled ----------
		{"workspace: stored default, incoming empty", bd(scopeWorkspace, wsDefault, effectAllow, true), bd(scopeWorkspace, "", effectAllow, true), 1, false},
		{"workspace: stored empty, incoming default", bd(scopeWorkspace, "", effectForbid, true), bd(scopeWorkspace, wsDefault, effectForbid, true), 0, false},
		{"workspace: forbid really moved off the default", bd(scopeWorkspace, wsDefault, effectForbid, true), bd(scopeWorkspace, "eng", effectForbid, true), 0, true},
	}
	for _, c := range cases {
		relax, reason := classifyUpdate(c.old, c.upd, c.otherAllows)
		if relax != c.wantRelax {
			t.Errorf("%s: relaxing = %v (%q), want %v", c.name, relax, reason, c.wantRelax)
			continue
		}
		if relax && reason == "" {
			t.Errorf("%s: a relaxation must carry a reason for the audit trail", c.name)
		}
		if !relax && reason != "" {
			t.Errorf("%s: an ordinary write must carry no reason, got %q", c.name, reason)
		}
	}
}

// TestClassifyDeleteTable pins all eight cells of classifyDelete. Did not rewrite it —
// a delete has no NEW scope, which is the dimension that leaked in classifyUpdate — but the
// adversarial panel measured that its last-allow gate had ZERO coverage: neutering
// `otherEnabledAllows == 0` left the whole package suite green, so a single actor deleting
// the only enabled allow could unconfine a source and nothing would notice. The gate was
// correct and untested, which is a defect with a longer fuse than a wrong one.
func TestClassifyDeleteTable(t *testing.T) {
	cases := []struct {
		name        string
		deleted     bindingDTO
		otherAllows int
		wantRelax   bool
	}{
		{"enabled forbid deleted (a restriction removed)", bd(scopeUser, "u1", effectForbid, true), 1, true},
		{"enabled forbid deleted, no allows either", bd(scopeUser, "u1", effectForbid, true), 0, true},
		{"LAST enabled allow deleted (the source becomes global)", bd(scopeUser, "u1", effectAllow, true), 0, true},
		{"legacy empty effect is an allow", bd(scopeUser, "u1", "", true), 0, true},
		{"non-last allow deleted (source stays confined)", bd(scopeUser, "u1", effectAllow, true), 1, false},
		{"disabled forbid deleted (enforced nothing)", bd(scopeUser, "u1", effectForbid, false), 0, false},
		{"disabled allow deleted, no other allows", bd(scopeUser, "u1", effectAllow, false), 0, false},
		{"disabled allow deleted, others remain", bd(scopeUser, "u1", effectAllow, false), 1, false},
	}
	for _, c := range cases {
		relax, reason := classifyDelete(c.deleted, c.otherAllows)
		if relax != c.wantRelax {
			t.Errorf("%s: relaxing = %v (%q), want %v", c.name, relax, reason, c.wantRelax)
		}
		if relax && reason == "" {
			t.Errorf("%s: a relaxation must carry a reason for the audit trail", c.name)
		}
	}
}

// --- the input space, and the two properties over it ----------------------------------

// classifyInputs enumerates the space classifyUpdate decides over: both effects (plus the
// legacy empty value normalizeEffect maps to allow), both enabled states on each side, every
// scope TREE on each side — all eight of validScopeTrees, so no tree is silently outside the
// sweep — plus a second ref within one tree and both spellings of the default workspace, and
// the presence or absence of another enabled allow.
func classifyInputs(yield func(old, upd bindingDTO, otherAllows int)) {
	effects := []string{"", effectAllow, effectForbid}
	scopes := []struct{ tree, ref string }{
		{scopeWorkspace, ""}, {scopeWorkspace, model.DefaultWorkspaceSlug}, {scopeWorkspace, "eng"},
		{scopeAgentGroup, "core"}, {scopeFolder, "f1"},
		{scopeSession, "s1"}, {scopeAgent, "a1"},
		{scopeUser, "u1"}, {scopeUser, "u2"},
		{scopeUserGroup, "g1"}, {scopeRole, "admin"},
	}
	for _, oe := range effects {
		for _, oEn := range []bool{false, true} {
			for _, os := range scopes {
				for _, ne := range effects {
					for _, nEn := range []bool{false, true} {
						for _, ns := range scopes {
							for _, other := range []int{0, 1} {
								yield(bd(os.tree, os.ref, oe, oEn), bd(ns.tree, ns.ref, ne, nEn), other)
							}
						}
					}
				}
			}
		}
	}
}

// classifyUpdatePreS590 is the classifier EXACTLY as it stood before
// (posture.go:87-117 at bb3b17185), kept verbatim as the reference for the monotonicity
// property below. It is not called by production code and must never be "fixed" — its whole
// value is being the thing is compared against.
func classifyUpdatePreS590(old, updated bindingDTO, otherEnabledAllows int) (bool, string) {
	oldEff, newEff := normalizeEffect(old.Effect), normalizeEffect(updated.Effect)

	if oldEff == effectForbid && newEff == effectAllow {
		return true, "forbid changed to allow (a restriction becomes a grant)"
	}
	if oldEff == effectForbid && old.Enabled && !updated.Enabled {
		return true, "forbid disabled (a restriction removed)"
	}
	if newEff == effectAllow {
		if !old.Enabled && updated.Enabled {
			return true, "allow enabled (a grant added)"
		}
		if old.Enabled && !updated.Enabled && otherEnabledAllows == 0 {
			return true, "last allow disabled (the source becomes global)"
		}
		if oldEff == effectAllow && old.Enabled && updated.Enabled {
			if specificityRank(updated.ScopeTree) > specificityRank(old.ScopeTree) {
				return true, "allow scope broadened to a less-specific tree"
			}
			if updated.ScopeTree == old.ScopeTree && updated.ScopeRef != old.ScopeRef {
				return true, "allow scope moved to a different " + old.ScopeTree
			}
		}
	}
	return false, ""
}

// TestClassifyUpdateOnlyEverAddsAGate checks, over the whole input space against the
// verbatim pre classifier, the property ADR-0022 §5 promises in words: "it can only
// ever add a dual-control gate, never remove one".
//
// Removes gates from EXACTLY ONE declared shape, and this test pins that the exemption
// is that shape and nothing else. The pre code compared scope_ref by raw string, so a
// workspace binding stored as the default slug and PUT back with scope_ref omitted looked
// like "allow scope moved to a different workspace" — a relaxation report about a write that
// moved nothing. resolveScope maps "" to the default slug on the way in (binding.go:221-226)
// and containsActor maps it the same way at decision time (resolver.go:437-442), so the two
// spellings are provably ONE population. Keeping that gate would not have been caution: it
// would file an approval request whose stored Reason — which lands in the immutable audit
// ledger — asserts a move that did not happen, and it fires on something as ordinary as
// editing a note, which is how a two-person control gets rubber-stamped into uselessness.
func TestClassifyUpdateOnlyEverAddsAGate(t *testing.T) {
	var gained int
	exempt := 0
	classifyInputs(func(old, upd bindingDTO, other int) {
		wasRelax, _ := classifyUpdatePreS590(old, upd, other)
		isRelax, _ := classifyUpdate(old, upd, other)
		switch {
		case wasRelax && !isRelax:
			// The one sanctioned exemption: the same workspace population, spelled two ways.
			sameCanonical := scopeOf(old) == scopeOf(upd)
			spelledDifferently := old.ScopeTree == scopeWorkspace && upd.ScopeTree == scopeWorkspace && old.ScopeRef != upd.ScopeRef
			if !sameCanonical || !spelledDifferently {
				t.Errorf("UNDECLARED gate removal: %s/%v/%v → %s/%v/%v (otherAllows=%d)",
					scopeOf(old), normalizeEffect(old.Effect), old.Enabled,
					scopeOf(upd), normalizeEffect(upd.Effect), upd.Enabled, other)
				return
			}
			exempt++
		case !wasRelax && isRelax:
			gained++
		}
	})
	// The exemption must be REAL (the enumeration reaches it) and the fix must gate
	// something new, or the "whitelist" is decoration.
	if exempt == 0 {
		t.Error("the default-workspace spelling exemption was never exercised — the sweep no longer covers it")
	}
	if gained == 0 {
		t.Error("the whitelist gated nothing the previous classifier let through — the fix is inert")
	}
	t.Logf("monotone: %d shapes newly gated, %d exempt (default-workspace spelling only)", gained, exempt)
}

// TestClassifyUpdateWhitelistDefaultUnreachable checks that no enumerated input reaches the
// conservative fallback.
//
// Be honest about what this is worth: the switch is exhaustive BY CONSTRUCTION, because
// normalizeEffect can only return allow or forbid (binding.go:59-64) and the cases partition
// (effect × enabled) completely — so the fallback is dead code today and a one-input test
// would pass just as well. It earns its place only as a guard for the NEXT edit: a case keyed
// on a specific TREE, or a new enabled/effect state, would fall through, and this names the
// shape that did instead of letting it surface as an approval nobody can explain. The
// load-bearing rows of the whitelist are the `moved` cases and the confinement case, and
// those are pinned by the table and by mutation, not by this.
func TestClassifyUpdateWhitelistDefaultUnreachable(t *testing.T) {
	const fallback = "unclassified posture change (conservative default)"
	hits := 0
	classifyInputs(func(old, upd bindingDTO, other int) {
		if _, reason := classifyUpdate(old, upd, other); reason == fallback {
			hits++
			if hits <= 3 {
				t.Errorf("fell through to the whitelist default: %+v → %+v (otherAllows=%d)", old, upd, other)
			}
		}
	})
	if hits > 0 {
		t.Errorf("%d input(s) reached the conservative default", hits)
	}
}

// --- the premise the policy rests on --------------------------------------------------

// TestScopesAreNotNestedPopulations is the POPULATION oracle: it computes, with the real
// containsActor, that no two distinct scopes are nested — for every ordered pair there is an
// actor inside the first that is outside the second. That is why classifyUpdate compares
// scopes by IDENTITY and refuses to rank them: there is no containment to rank by, and
// specificityRank (a credential-selection order over trees) is not one.
//
// scopeFolder is excluded from the pair sweep and asserted separately: it has NO containment
// dimension at all (resolver.go:465-467) and is decided by the per-entity grant, so
// containment is not even the whole story — one more reason the certificate is identity.
func TestScopesAreNotNestedPopulations(t *testing.T) {
	type witness struct {
		scope postureScope
		actor actorIdentity
	}
	ws := []witness{
		{postureScope{scopeWorkspace, model.DefaultWorkspaceSlug}, actorIdentity{workspaceSlug: model.DefaultWorkspaceSlug}},
		{postureScope{scopeWorkspace, "eng"}, actorIdentity{workspaceSlug: "eng"}},
		{postureScope{scopeAgentGroup, "core"}, actorIdentity{groups: []string{"core"}}},
		{postureScope{scopeSession, "s1"}, actorIdentity{sessionRef: "s1"}},
		{postureScope{scopeAgent, "a1"}, actorIdentity{agentExternalID: "a1"}},
		{postureScope{scopeUser, "u1"}, actorIdentity{userID: "u1"}},
		{postureScope{scopeUserGroup, "g1"}, actorIdentity{userGroups: []string{"g1"}}},
		{postureScope{scopeRole, "admin"}, actorIdentity{role: "admin"}},
	}
	for _, a := range ws {
		ab := binding{scopeTree: a.scope.tree, scopeRef: a.scope.ref}
		if !containsActor(a.actor, ab) {
			t.Fatalf("witness for %s does not match its own scope — the oracle is broken", a.scope)
		}
		for _, b := range ws {
			if a.scope == b.scope {
				continue
			}
			bb := binding{scopeTree: b.scope.tree, scopeRef: b.scope.ref}
			if containsActor(a.actor, bb) {
				t.Errorf("%s is contained by %s — the trees WOULD be nested, revisit the scope-identity policy", a.scope, b.scope)
			}
		}
	}
	// A folder binding contains no actor at all: it rides the grant, not containment.
	fb := binding{scopeTree: scopeFolder, scopeRef: "f1"}
	for _, a := range ws {
		if containsActor(a.actor, fb) {
			t.Errorf("folder binding claimed containment over %s (resolver.go:465-467 says it never does)", a.scope)
		}
	}
}

// TestScopeOfCanonicalisesTheDefaultWorkspace: the workspace tree's empty ref and the default
// slug are the SAME population — resolveScope stores the slug (binding.go:221-226) and
// containsActor maps "" to it (resolver.go:437-442). A client that omits scope_ref on a PUT
// must not look like a move, or every note edit on a default-workspace binding would demand
// an approval. Every other tree requires a non-empty ref at validate(), so none is
// canonicalised.
func TestScopeOfCanonicalisesTheDefaultWorkspace(t *testing.T) {
	if a, b := scopeOf(bd(scopeWorkspace, "", effectAllow, true)), scopeOf(bd(scopeWorkspace, model.DefaultWorkspaceSlug, effectAllow, true)); a != b {
		t.Errorf("workspace ref %q and %q must be one population, got %s vs %s", "", model.DefaultWorkspaceSlug, a, b)
	}
	if a, b := scopeOf(bd(scopeUser, "", effectAllow, true)), scopeOf(bd(scopeUser, model.DefaultWorkspaceSlug, effectAllow, true)); a == b {
		t.Error("only the workspace tree may canonicalise an empty ref")
	}
	// The canonical form is what the resolver matches, not a private convention.
	if !containsActor(actorIdentity{workspaceSlug: model.DefaultWorkspaceSlug}, binding{scopeTree: scopeWorkspace, scopeRef: ""}) {
		t.Error("containsActor no longer maps an empty workspace ref to the default slug — scopeOf must follow it")
	}
}

// --- the tripwire ---------------------------------------------------------------------

// TestBindingDTOShapeIsPinned fails when bindingDTO grows, loses or renames a field, so a
// new wire field cannot join the binding without someone deciding whether it decides ACCESS.
// classifyUpdate reads exactly three dimensions — Effect, Enabled and the scope pair; the
// rest are either the immutable natural key (forced from the stored row, binding.go:454-455),
// output-only (FolderPath), or access-neutral by construction (Note and the credential
// REFERENCE, which selects WHICH credential an authorized actor receives, never whether it
// is authorized). If you add a field that changes who may reach the source, add it to
// classifyUpdate and to this list — in that order.
func TestBindingDTOShapeIsPinned(t *testing.T) {
	want := []string{
		"CredHint", "CredName", "CredRef", "CredRefKind", // access-neutral: locator, not a decision
		"Effect", "Enabled", "ScopeRef", "ScopeTree", // ← the four classifyUpdate reads
		"FolderPath",              // output-only, resolved server-side
		"ID",                      // identity
		"Note",                    // access-neutral
		"SourceRef", "SourceType", // the immutable natural key
	}
	sort.Strings(want)

	rt := reflect.TypeOf(bindingDTO{})
	got := make([]string, 0, rt.NumField())
	for i := range rt.NumField() {
		got = append(got, rt.Field(i).Name)
	}
	sort.Strings(got)

	if !reflect.DeepEqual(got, want) {
		t.Errorf("bindingDTO changed shape.\n got: %v\nwant: %v\n"+
			"Decide whether the new/changed field decides WHO may reach the source. If it does, "+
			"classifyUpdate must read it (an unread access dimension is a dual-control bypass); "+
			"then update this list.", got, want)
	}
}
