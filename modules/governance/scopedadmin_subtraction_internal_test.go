// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// Structured subtraction: a custom role is a live BASE minus an explicit exclusion set.
//
// The ordering is the whole guarantee, so it is PROVED here rather than asserted in a
// comment: the subtraction runs after the base is expanded, after the direct permissions
// and after every included permission-group, so nothing an include adds can put an
// excluded permission back. Each property below has a mutant that kills it.

func registerSubtractionPerms(t *testing.T) {
	t.Helper()
	auth.ResetModuleCatalog()
	err := auth.RegisterModulePermissions([]auth.Permission{
		"models:keys:read", "models:keys:write",
		"compliance:risk:read", "compliance:risk:write", "compliance:risk:admin",
		"governance:rbac:read", "governance:rbac:admin",
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	t.Cleanup(auth.ResetModuleCatalog)
}

// TestBaseIsResolvedLiveNotEnumerated: the reason a base exists at all. A role defined as
// "editor" must confer what editor confers TODAY, including permissions that entered the
// catalog after the role was written — which an enumerated copy never would.
func TestBaseIsResolvedLiveNotEnumerated(t *testing.T) {
	auth.ResetModuleCatalog()
	t.Cleanup(auth.ResetModuleCatalog)

	roles := map[string]customRole{"e": {Name: "e", Base: auth.RoleEditor}}
	groups := map[string]permGroup{}

	before := effectivePermsOf("e", true, "", roles, groups)
	if before["models:keys:write"] {
		t.Fatal("fixture broken: the permission is not registered yet")
	}
	// A module is mounted later and declares a new permission.
	if err := auth.RegisterModulePermissions([]auth.Permission{"models:keys:write"}); err != nil {
		t.Fatal(err)
	}
	after := effectivePermsOf("e", true, "", roles, groups)
	if !after["models:keys:write"] {
		t.Error("a BASE must be resolved live: an editor gained this permission and the role must follow")
	}
	if len(after) <= len(before) {
		t.Errorf("the live base did not grow: before=%d after=%d", len(before), len(after))
	}
}

// TestSubtractionRunsAfterEveryExpansion is property one. Each case adds the
// excluded permission through a DIFFERENT expansion path; all must end up subtracted.
func TestSubtractionRunsAfterEveryExpansion(t *testing.T) {
	registerSubtractionPerms(t)
	const target = "models:keys:write"

	groups := map[string]permGroup{
		"keys": {Name: "keys", Perms: []string{"models:keys:read", target}},
	}
	cases := []struct {
		name string
		role customRole
	}{
		{"via the base", customRole{Name: "r", Base: auth.RoleEditor, Excludes: []string{target}}},
		{"via a direct permission", customRole{Name: "r", Perms: []string{"models:keys:read", target}, Excludes: []string{target}}},
		{"via an included group", customRole{Name: "r", Groups: []string{"keys"}, Excludes: []string{target}}},
		{"via all three at once", customRole{
			Name: "r", Base: auth.RoleEditor, Perms: []string{target}, Groups: []string{"keys"},
			Excludes: []string{target},
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			eff := effectivePermsOf("r", true, "", map[string]customRole{"r": c.role}, groups)
			if eff[target] {
				t.Errorf("%s: the exclusion did not survive this expansion path — the operator authored a cap that does not cap", c.name)
			}
			// A control: the role must still confer SOMETHING, or the case proves nothing
			// (an empty set would satisfy the assertion above for the wrong reason).
			if len(eff) == 0 {
				t.Errorf("%s: the role confers nothing at all, so this case cannot show the exclusion worked", c.name)
			}
			if !eff["models:keys:read"] {
				t.Errorf("%s: the exclusion must remove ONE permission, not the neighboring one", c.name)
			}
		})
	}
}

// TestSubtractionNeverExceedsTheGrantor is property one stated as the invariant
// that matters: the result of a subtraction can never be a superset of the un-subtracted
// set, so it can never widen what the ceiling checks.
func TestSubtractionNeverExceedsTheGrantor(t *testing.T) {
	registerSubtractionPerms(t)
	groups := map[string]permGroup{"keys": {Name: "keys", Perms: []string{"models:keys:read", "models:keys:write"}}}

	for _, base := range []string{"", auth.RoleViewer, auth.RoleEditor, auth.RoleAdmin, auth.RoleOwner} {
		for _, ex := range [][]string{nil, {"models:keys:write"}, {"models:keys:read", "compliance:risk:admin"}, {"agent:read"}} {
			with := customRole{Name: "r", Base: base, Perms: []string{"compliance:risk:read"}, Groups: []string{"keys"}, Excludes: ex}
			without := with
			without.Excludes = nil

			got := effectivePermsOf("r", true, "", map[string]customRole{"r": with}, groups)
			full := effectivePermsOf("r", true, "", map[string]customRole{"r": without}, groups)
			for p := range got {
				if !full[p] {
					t.Errorf("base=%q excludes=%v: subtraction ADDED %q — it must only ever remove", base, ex, p)
				}
			}
			// And every excluded permission really is gone.
			for _, p := range ex {
				if got[p] {
					t.Errorf("base=%q: %q was excluded and is still present", base, p)
				}
			}
		}
	}
}

// TestExcludingAPermissionNobodyGrantsIsInert: an exclusion that removes nothing today is
// allowed on purpose — the base grows, and "never this, whatever editor becomes" is the
// durable statement the feature exists for. It must not error and must not remove anything
// else.
func TestExcludingAPermissionNobodyGrantsIsInert(t *testing.T) {
	registerSubtractionPerms(t)
	roles := map[string]customRole{
		"r": {Name: "r", Perms: []string{"compliance:risk:read"}, Excludes: []string{"models:keys:write"}},
	}
	eff := effectivePermsOf("r", true, "", roles, map[string]permGroup{})
	if !eff["compliance:risk:read"] || len(eff) != 1 {
		t.Errorf("an inert exclusion must change nothing else, got %v", eff)
	}
}

// TestSubtractionBindsTheDelegationPermit is property two: the delegation permit is
// SYNTHESIZED, not projected from the role's permissions, so it is the one path where a
// declared subtraction could be silently dropped.
func TestSubtractionBindsTheDelegationPermit(t *testing.T) {
	registerSubtractionPerms(t)
	groups := map[string]permGroup{}
	mk := func(role string) []scopedGrant {
		return []scopedGrant{{SubjectKind: subjectUser, SubjectRef: "u1", Role: role, RoleCustom: true, Scope: scopeSpec{Tree: scopeTenant}}}
	}

	// An admin-capable role with NO exclusions gets the delegation permit, as before.
	plain := map[string]customRole{"a": {Name: "a", Perms: []string{"compliance:risk:admin"}}}
	src := projectManagedCedar(mk("a"), plain, groups)
	if !strings.Contains(src, `Action::"governance:rbac:admin"`) || !strings.Contains(src, `Action::"governance:rbac:read"`) {
		t.Fatalf("fixture broken: an admin-capable grant should carry the delegation permit:\n%s", src)
	}

	// Excluding rbac:admin means "may administer this surface, may NOT re-delegate it".
	noAdmin := map[string]customRole{"a": {Name: "a", Perms: []string{"compliance:risk:admin"}, Excludes: []string{"governance:rbac:admin"}}}
	src = projectManagedCedar(mk("a"), noAdmin, groups)
	if strings.Contains(src, `Action::"governance:rbac:admin"`) {
		t.Errorf("the exclusion was silently ignored on the delegation permit — the operator capped nothing:\n%s", src)
	}
	if !strings.Contains(src, `Action::"governance:rbac:read"`) {
		t.Errorf("only the excluded action must be dropped, not the other one:\n%s", src)
	}
	if _, err := compileGrantSet(src); err != nil {
		t.Fatalf("projection does not compile: %v\n%s", err, src)
	}

	// Excluding BOTH drops the permit entirely rather than emitting an empty action list
	// (which Cedar would reject).
	neither := map[string]customRole{"a": {Name: "a", Perms: []string{"compliance:risk:admin"},
		Excludes: []string{"governance:rbac:read", "governance:rbac:admin"}}}
	src = projectManagedCedar(mk("a"), neither, groups)
	if strings.Contains(src, "governance:rbac:") {
		t.Errorf("with both excluded no delegation permit may be emitted:\n%s", src)
	}
	if !strings.Contains(src, `Action::"compliance:risk:admin"`) {
		t.Errorf("the grant must still confer its own permissions:\n%s", src)
	}
	if _, err := compileGrantSet(src); err != nil {
		t.Fatalf("projection does not compile: %v\n%s", err, src)
	}
}

// TestSubtractionUnderTheDelegationCeiling: a subtracted role is checked against the
// actor's domain on the SUBTRACTED set, so subtraction can only ever make a grant easier
// to authorize — never authorize something the actor lacks.
func TestSubtractionUnderTheDelegationCeiling(t *testing.T) {
	registerSubtractionPerms(t)
	ctx, tenant := context.Background(), model.TenantID("t1")
	admin := auth.ScopedPrincipal("c", "admin", tenant, auth.RoleAdmin)
	groups := map[string]permGroup{}

	// owner-based: confers the tree `admin` verb, which a tenant ADMIN does not hold.
	ownerish := customRole{Name: "o", Base: auth.RoleOwner}
	g := scopedGrant{SubjectKind: subjectUser, SubjectRef: "v", Role: "o", RoleCustom: true, Scope: scopeSpec{Tree: scopeTenant}}
	if err := canDelegate(ctx, nil, admin, tenant, g, nil, map[string]customRole{"o": ownerish}, groups); err == nil {
		t.Error("a tenant admin must not delegate an owner-based role: it confers the tree admin verb")
	}

	// The SAME role with the owner-only verbs subtracted falls inside the admin's domain.
	trimmed := ownerish
	for _, k := range auth.TreeScopeableKinds() {
		trimmed.Excludes = append(trimmed.Excludes, k+":"+auth.VerbAdmin)
	}
	if err := canDelegate(ctx, nil, admin, tenant, g, nil, map[string]customRole{"o": trimmed}, groups); err != nil {
		t.Errorf("subtracting exactly what the actor lacks must bring the grant inside its ceiling, got %v", err)
	}
}

// TestExcludingAdminVerbsDropsAdminCapability: subtracting the last admin verb ALSO makes
// the grant stop being admin-capable, which is a second, independent reason no delegation
// permit is emitted. The two mechanisms must agree — if they ever disagree, one of them is
// granting something the other believes it removed.
func TestExcludingAdminVerbsDropsAdminCapability(t *testing.T) {
	registerSubtractionPerms(t)
	groups := map[string]permGroup{}
	g := scopedGrant{SubjectKind: subjectUser, SubjectRef: "u1", Role: "a", RoleCustom: true, Scope: scopeSpec{Tree: scopeTenant}}

	capable := map[string]customRole{"a": {Name: "a", Perms: []string{"compliance:risk:admin", "compliance:risk:read"}}}
	if !isAdminCapableGrant(g, capable, groups) {
		t.Fatal("fixture broken: the role should be admin-capable")
	}
	trimmed := map[string]customRole{"a": {Name: "a",
		Perms: []string{"compliance:risk:admin", "compliance:risk:read"}, Excludes: []string{"compliance:risk:admin"}}}
	if isAdminCapableGrant(g, trimmed, groups) {
		t.Error("subtracting the only admin verb must remove admin capability")
	}
	src := projectManagedCedar([]scopedGrant{g}, trimmed, groups)
	if !strings.Contains(src, `Action::"compliance:risk:read"`) {
		t.Fatalf("the grant must still project its remaining permission:\n%s", src)
	}
	if strings.Contains(src, "governance:rbac:") {
		t.Errorf("a no-longer-admin-capable grant must emit no delegation permit:\n%s", src)
	}
}

// TestExcludeOfAnUnknownPermissionIsInert: a stored exclusion naming a permission the
// catalog no longer knows (its module was unmounted) must be inert — not an error, and not
// a way to remove something else.
func TestExcludeOfAnUnknownPermissionIsInert(t *testing.T) {
	registerSubtractionPerms(t)
	roles := map[string]customRole{"r": {Name: "r",
		Perms: []string{"compliance:risk:read"}, Excludes: []string{"gone:module:read"}}}
	eff := effectivePermsOf("r", true, "", roles, map[string]permGroup{})
	if !eff["compliance:risk:read"] || len(eff) != 1 {
		t.Errorf("an exclusion of an unknown permission must change nothing, got %v", eff)
	}
}

// TestDelegationPermitUnionsAcrossASubjectsGrants closes a defect the subtraction itself
// created. The delegation permit is emitted once per SUBJECT; before subtraction existed
// every such permit was byte-identical, so emitting the first and skipping the rest was
// lossless. It stopped being lossless the moment a role could subtract from it.
//
// The property under test is not just "the union is right" — it is that the result does
// not depend on the ROLE NAMES, because the grants are projected in name order. A
// first-wins emission would make a rename change who can re-delegate.
func TestDelegationPermitUnionsAcrossASubjectsGrants(t *testing.T) {
	registerSubtractionPerms(t)
	groups := map[string]permGroup{}

	// Two admin-capable roles for the SAME subject: one may re-delegate, one may not.
	// The names are chosen so that in one run the capped role sorts first and in the other
	// it sorts last.
	for _, names := range [][2]string{{"aaa-capped", "zzz-open"}, {"zzz-capped", "aaa-open"}} {
		capped, open := names[0], names[1]
		roles := map[string]customRole{
			capped: {Name: capped, Perms: []string{"compliance:risk:admin"}, Excludes: []string{"governance:rbac:admin"}},
			open:   {Name: open, Perms: []string{"compliance:risk:admin"}},
		}
		grants := []scopedGrant{
			{SubjectKind: subjectUser, SubjectRef: "u1", Role: capped, RoleCustom: true, Scope: scopeSpec{Tree: scopeTenant}},
			{SubjectKind: subjectUser, SubjectRef: "u1", Role: open, RoleCustom: true, Scope: scopeSpec{Tree: scopeTenant}},
		}
		src := projectManagedCedar(grants, roles, groups)

		// The UNCAPPED grant legitimately confers re-delegation; a capped sibling must not
		// take it away, whichever way the names happen to sort.
		if !strings.Contains(src, `Action::"governance:rbac:admin"`) {
			t.Errorf("capped=%q open=%q: the uncapped grant's re-delegation was suppressed by its sibling — an exclusion narrows the ROLE, not the subject:\n%s", capped, open, src)
		}
		// Exactly ONE delegation permit per subject: the union, not two permits.
		if got := strings.Count(src, `Action::"governance:rbac:read"`); got != 1 {
			t.Errorf("capped=%q: want exactly one delegation permit for the subject, got %d:\n%s", capped, got, src)
		}
		if _, err := compileGrantSet(src); err != nil {
			t.Fatalf("projection does not compile: %v\n%s", err, src)
		}
	}

	// And when EVERY admin-capable grant of the subject is capped, nothing is emitted.
	roles := map[string]customRole{
		"c1": {Name: "c1", Perms: []string{"compliance:risk:admin"}, Excludes: []string{"governance:rbac:read", "governance:rbac:admin"}},
		"c2": {Name: "c2", Perms: []string{"compliance:risk:admin"}, Excludes: []string{"governance:rbac:read", "governance:rbac:admin"}},
	}
	grants := []scopedGrant{
		{SubjectKind: subjectUser, SubjectRef: "u1", Role: "c1", RoleCustom: true, Scope: scopeSpec{Tree: scopeTenant}},
		{SubjectKind: subjectUser, SubjectRef: "u1", Role: "c2", RoleCustom: true, Scope: scopeSpec{Tree: scopeTenant}},
	}
	if src := projectManagedCedar(grants, roles, groups); strings.Contains(src, "governance:rbac:") {
		t.Errorf("every grant capped: no delegation permit may be emitted:\n%s", src)
	}
}
