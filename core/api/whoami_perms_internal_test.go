// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"testing"

	"github.com/olivaresai/olivares/core/auth"
)

// catalogModule declares one permission its routes do NOT require, and requires one its
// Permissions() does NOT declare. Both directions matter: the declaration and the route
// requirement are distinct facts, and the catalog must carry the union — the route form
// is the one that actually produces the 403.
type catalogModule struct{}

func (catalogModule) APINamespace() string { return "catmod" }
func (catalogModule) Permissions() []auth.Permission {
	return []auth.Permission{"catmod:declared:read", "catmod:both:write"}
}
func (catalogModule) APIRoutes(reg RouteRegistrar) {
	reg.Handle("GET", "/both", "catmod:both:write", nil)
	reg.Handle("GET", "/routed", "catmod:routeonly:read", nil)
}

// searchModule contributes a search kind gated by a permission that appears in NO route
// and in NO Permissions() declaration. handleSearch gates on it, so it is a real 403
// source and belongs in the catalog.
//
// It ALSO declares and routes an EMPTY permission. That is the only way the catalog's
// empty-string skip can be shown to do anything: with no module emitting "", asserting
// that the catalog does not contain it passes whether the skip is there or not.
type searchModule struct{ catalogModule }

func (searchModule) APINamespace() string { return "searchmod" }
func (searchModule) Permissions() []auth.Permission {
	return []auth.Permission{"searchmod:thing:read", ""}
}
func (searchModule) APIRoutes(reg RouteRegistrar) {
	reg.Handle("GET", "/things", "searchmod:thing:read", nil)
	reg.Handle("GET", "/unguarded", "", nil)
}

// TestBuildPermCatalogCoversAllThreeForms: declaration, route requirement and search
// kind. A form the catalog cannot see is a permission the console never learns it holds
// — a button hidden forever, with no error anywhere.
func TestBuildPermCatalogCoversAllThreeForms(t *testing.T) {
	kinds := []SearchKind{
		{Kind: "searchmod.thing", Permission: "searchmod:searchonly:read"},
		{Kind: "searchmod.unguarded", Permission: ""}, // the empty-skip's third entry point
	}
	got := map[auth.Permission]bool{}
	for _, p := range buildPermCatalog([]Module{catalogModule{}, searchModule{}}, kinds) {
		got[p] = true
	}
	for _, want := range []auth.Permission{
		"catmod:declared:read",      // declared, never routed
		"catmod:both:write",         // declared AND routed
		"catmod:routeonly:read",     // routed, never declared
		"searchmod:searchonly:read", // gated by a search kind only
	} {
		if !got[want] {
			t.Errorf("catalog is missing %q", want)
		}
	}
	if got[""] {
		t.Error("catalog carries the empty permission")
	}
}

// TestBuildPermCatalogIsSortedAndDeduplicated: two modules declaring the same
// permission, and a permission that is both declared and routed, must each appear once.
func TestBuildPermCatalogIsSortedAndDeduplicated(t *testing.T) {
	got := buildPermCatalog([]Module{catalogModule{}, catalogModule2{}}, nil)
	seen := map[auth.Permission]int{}
	for i, p := range got {
		seen[p]++
		if i > 0 && got[i-1] >= p {
			t.Errorf("catalog is not strictly sorted at %d: %q then %q", i, got[i-1], p)
		}
	}
	if seen["catmod:both:write"] != 1 {
		t.Errorf("catmod:both:write appears %d times (declared AND routed), want 1", seen["catmod:both:write"])
	}
	if seen["shared:thing:read"] != 1 {
		t.Errorf("shared:thing:read appears %d times (declared by two modules), want 1", seen["shared:thing:read"])
	}
}

// catalogModule2 shares a permission with catalogModule to exercise cross-module dedup.
type catalogModule2 struct{}

func (catalogModule2) APINamespace() string { return "catmod2" }
func (catalogModule2) Permissions() []auth.Permission {
	return []auth.Permission{"shared:thing:read"}
}
func (catalogModule2) APIRoutes(reg RouteRegistrar) {
	reg.Handle("GET", "/shared", "shared:thing:read", nil)
}

// TestBuildRolePermsCoversBothConfinementStates: eight precomputed sets, and the
// confined ones really are narrower for the roles that hold a recon read. A missing key
// would make effectivePermsFor return an empty set — deny-closed, but it would blank the
// console for every confined operator, so it must be pinned.
func TestBuildRolePermsCoversBothConfinementStates(t *testing.T) {
	rp := buildRolePerms(buildPermCatalog([]Module{catalogModule{}}, nil))
	if len(rp) != len(builtinRoles)*2 {
		t.Fatalf("buildRolePerms produced %d sets, want %d", len(rp), len(builtinRoles)*2)
	}
	for _, role := range builtinRoles {
		for _, confined := range []bool{false, true} {
			if _, ok := rp[rolePermKey{role: role, confined: confined}]; !ok {
				t.Errorf("no set for role=%s confined=%v", role, confined)
			}
		}
	}
	// editor and up hold the privileged recon reads, so confinement must narrow them;
	// a viewer holds none, so its two sets are identical.
	for _, role := range []string{auth.RoleEditor, auth.RoleAdmin, auth.RoleOwner} {
		open := len(rp[rolePermKey{role: role, confined: false}])
		conf := len(rp[rolePermKey{role: role, confined: true}])
		if conf >= open {
			t.Errorf("%s: confined set has %d perms, unconfined %d — confinement removed nothing", role, conf, open)
		}
	}
	if a, b := len(rp[rolePermKey{role: auth.RoleViewer, confined: false}]), len(rp[rolePermKey{role: auth.RoleViewer, confined: true}]); a != b {
		t.Errorf("viewer: confined %d vs unconfined %d — a viewer holds no privileged read, so confinement must change nothing", b, a)
	}
}

// TestEffectivePermsForUnknownRoleIsEmptyNotNil: an unknown role must marshal as [], not
// null. `null` would reach the console's Set() constructor and throw, taking the whole
// panel down instead of denying quietly.
func TestEffectivePermsForUnknownRoleIsEmptyNotNil(t *testing.T) {
	s := &Server{rolePerms: buildRolePerms(buildPermCatalog([]Module{catalogModule{}}, nil))}
	got := s.effectivePermsFor("archduke", false)
	if got == nil {
		t.Fatal("effectivePermsFor returned nil for an unknown role; it marshals as null")
	}
	if len(got) != 0 {
		t.Errorf("unknown role got %d permissions, want 0", len(got))
	}
	// …and the known-role path is not vacuously empty.
	if n := len(s.effectivePermsFor(auth.RoleOwner, false)); n == 0 {
		t.Fatal("owner got 0 permissions; the assertion above proves nothing")
	}
}

// TestEffectivePermsForDoesNotAliasTheCache: the handler must never hand out a slice
// backed by the precomputed set. If it did, one request mutating its copy would corrupt
// every later request's answer.
func TestEffectivePermsForDoesNotAliasTheCache(t *testing.T) {
	s := &Server{rolePerms: buildRolePerms(buildPermCatalog([]Module{catalogModule{}}, nil))}
	first := s.effectivePermsFor(auth.RoleOwner, false)
	if len(first) == 0 {
		t.Fatal("owner set is empty; nothing to alias")
	}
	original := first[0]
	first[0] = "tampered:by:caller"
	second := s.effectivePermsFor(auth.RoleOwner, false)
	if second[0] != original {
		t.Errorf("mutating a returned slice changed the next answer: got %q, want %q", second[0], original)
	}
}
