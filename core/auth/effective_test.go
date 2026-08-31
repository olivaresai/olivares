// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"sort"
	"testing"
)

// testCatalog is a stand-in for what a binary serves: module permissions across the
// three verbs, one of them a privileged read that is ALSO module-namespaced (the entry
// whose ordering inside RoleGrants is load-bearing), plus noise the function must cope
// with — a duplicate, an out-of-order entry and an empty string.
var testCatalog = []Permission{
	"rrw:access_path:write",
	"rrw:access_path:read",
	"rrw:access_path:admin",
	"accessmap:graph:read",
	"rrw:access_path:read", // duplicate
	"",                     // empty
}

func setOf(perms []Permission) map[Permission]struct{} {
	m := make(map[Permission]struct{}, len(perms))
	for _, p := range perms {
		m[p] = struct{}{}
	}
	return m
}

// TestEffectivePermissionsEqualsRoleGrants is the invariant the whole design rests on:
// the set the console receives must answer EXACTLY what the engine would answer, in
// BOTH directions, for every permission the binary can 403 on. A set that under-reports
// hides a legitimate button; a set that over-reports offers one that 403s — the very
// defect the client-side mirror kept producing.
func TestEffectivePermissionsEqualsRoleGrants(t *testing.T) {
	// The universe the set is claimed to cover: the catalog, plus the two forms that
	// cannot come from a catalog (the per-role core sets and the privileged reads).
	universe := map[Permission]struct{}{}
	for _, p := range testCatalog {
		if p != "" {
			universe[p] = struct{}{}
		}
	}
	for _, r := range []string{RoleViewer, RoleEditor, RoleAdmin, RoleOwner} {
		for _, p := range PermissionsForRole(r) {
			universe[p] = struct{}{}
		}
	}
	for _, p := range PrivilegedReadPerms() {
		universe[p] = struct{}{}
	}
	if len(universe) < 10 {
		t.Fatalf("universe has %d permissions; every assertion below would be near-vacuous", len(universe))
	}

	for _, role := range []string{RoleViewer, RoleEditor, RoleAdmin, RoleOwner} {
		got := setOf(EffectivePermissions(role, testCatalog, false))
		if len(got) == 0 {
			t.Fatalf("%s: empty effective set; the comparison below would pass vacuously", role)
		}
		for p := range universe {
			_, in := got[p]
			want := RoleGrants(role, p)
			if in != want {
				t.Errorf("%s: %q in effective set = %v, RoleGrants = %v", role, p, in, want)
			}
		}
		// Nothing OUTSIDE the universe may appear: the set must not invent.
		for p := range got {
			if _, ok := universe[p]; !ok {
				t.Errorf("%s: effective set carries %q, which is in neither the catalog, the core sets nor the privileged reads", role, p)
			}
		}
	}
}

// TestEffectivePermissionsSortedAndDeduplicated pins the wire shape. The duplicate and
// the out-of-order entry in testCatalog are there to be reduced.
func TestEffectivePermissionsSortedAndDeduplicated(t *testing.T) {
	got := EffectivePermissions(RoleOwner, testCatalog, false)
	if len(got) == 0 {
		t.Fatal("owner effective set is empty")
	}
	if !sort.SliceIsSorted(got, func(i, j int) bool { return got[i] < got[j] }) {
		t.Errorf("effective set is not sorted: %v", got)
	}
	seen := map[Permission]int{}
	for _, p := range got {
		seen[p]++
		if p == "" {
			t.Error("effective set carries the empty permission")
		}
	}
	for p, n := range seen {
		if n > 1 {
			t.Errorf("%q appears %d times", p, n)
		}
	}
	// The duplicated catalog entry really did reach the function twice.
	if n := countIn(testCatalog, "rrw:access_path:read"); n != 2 {
		t.Fatalf("precondition: testCatalog holds rrw:access_path:read %d times, want 2 — the dedup assertion above is vacuous", n)
	}
}

// TestRoleGrantsDeniesTheEmptyPermission is load-bearing for EffectivePermissions, which
// carries NO skip of its own for "": it relies on this. "" has no colon, so it is
// core-shaped, and no per-role core set contains it. If that ever stopped holding, the
// empty string would reach the wire as a permission — so the reliance is pinned here
// rather than restated as a second guard that no test could kill.
func TestRoleGrantsDeniesTheEmptyPermission(t *testing.T) {
	for _, role := range []string{RoleViewer, RoleEditor, RoleAdmin, RoleOwner, "", "unknown"} {
		if RoleGrants(role, "") {
			t.Errorf("RoleGrants(%q, \"\") = true; EffectivePermissions relies on this being false", role)
		}
	}
}

func countIn(perms []Permission, want Permission) int {
	n := 0
	for _, p := range perms {
		if p == want {
			n++
		}
	}
	return n
}

// TestEffectivePermissionsUnknownRoleIsEmpty: a role the engine does not know grants
// nothing, so the console offers nothing. Deny-closed.
func TestEffectivePermissionsUnknownRoleIsEmpty(t *testing.T) {
	for _, role := range []string{"", "superuser", "Owner", "root"} {
		if got := EffectivePermissions(role, testCatalog, false); len(got) != 0 {
			t.Errorf("role %q: effective set = %v, want empty", role, got)
		}
	}
}

// TestEffectivePermissionsConfinedRemovesExactlyTheReconReads is the F2/Q4 closure and
// its BOUNDARY: confinement removes the tenant-wide access-MATRIX reads (which have no
// per-workspace view to fall back to, so the engine forbids them whatever the action
// targets) and NOTHING ELSE. Removing more would hide actions a confined principal may
// legitimately perform inside its own workspace.
func TestEffectivePermissionsConfinedRemovesExactlyTheReconReads(t *testing.T) {
	for _, role := range []string{RoleViewer, RoleEditor, RoleAdmin, RoleOwner} {
		open := setOf(EffectivePermissions(role, testCatalog, false))
		confined := setOf(EffectivePermissions(role, testCatalog, true))

		want := map[Permission]struct{}{}
		for p := range open {
			if IsAccessGraphReconPerm(p) {
				want[p] = struct{}{}
			}
		}
		removed := map[Permission]struct{}{}
		for p := range open {
			if _, still := confined[p]; !still {
				removed[p] = struct{}{}
			}
		}
		if len(removed) != len(want) {
			t.Errorf("%s: confinement removed %v, want exactly the recon reads %v", role, keysOf(removed), keysOf(want))
			continue
		}
		for p := range want {
			if _, ok := removed[p]; !ok {
				t.Errorf("%s: confinement did NOT remove the recon read %q", role, p)
			}
		}
		// Confinement never ADDS.
		for p := range confined {
			if _, ok := open[p]; !ok {
				t.Errorf("%s: confinement ADDED %q", role, p)
			}
		}
	}
	// The editor-and-up roles must actually have had something to lose, or every
	// assertion above is satisfied by an empty removal set.
	adminOpen := setOf(EffectivePermissions(RoleAdmin, testCatalog, false))
	n := 0
	for p := range adminOpen {
		if IsAccessGraphReconPerm(p) {
			n++
		}
	}
	if n == 0 {
		t.Fatal("precondition: an unconfined admin holds no recon read, so the removal assertions are vacuous")
	}
}

func keysOf(m map[Permission]struct{}) []Permission {
	out := make([]Permission, 0, len(m))
	for p := range m {
		out = append(out, p)
	}
	sortPerms(out)
	return out
}

// TestEffectivePermissionsNeverCarriesSystemAdmin: the system permission is held by the
// superadmin FLAG, never by a tenant role. The console short-circuits on that flag; the
// per-tenant set must not carry it, or a non-superadmin would be offered system actions.
func TestEffectivePermissionsNeverCarriesSystemAdmin(t *testing.T) {
	catalog := append([]Permission{PermSystemAdmin}, testCatalog...)
	for _, role := range []string{RoleViewer, RoleEditor, RoleAdmin, RoleOwner} {
		for _, confined := range []bool{false, true} {
			for _, p := range EffectivePermissions(role, catalog, confined) {
				if p == PermSystemAdmin {
					t.Errorf("%s (confined=%v): effective set carries %q", role, confined, PermSystemAdmin)
				}
			}
		}
	}
}

// TestEffectivePermissionsIsBoundedByTheCatalog pins the documented, deliberate limit:
// module permissions are an OPEN set, so the answer is what THIS BINARY SERVES. A
// permission RoleGrants would grant, from a module that is not loaded, is absent — and
// that costs nothing, because a module with no routes cannot 403.
func TestEffectivePermissionsIsBoundedByTheCatalog(t *testing.T) {
	const unloaded Permission = "notmounted:thing:read"
	if !RoleGrants(RoleViewer, unloaded) {
		t.Fatal("precondition: RoleGrants must GRANT this module read, or the test proves nothing about the catalog bound")
	}
	for _, p := range EffectivePermissions(RoleViewer, testCatalog, false) {
		if p == unloaded {
			t.Fatalf("effective set carries %q, which no catalog entry declares", unloaded)
		}
	}
	// …and it appears as soon as the binary serves it.
	withIt := setOf(EffectivePermissions(RoleViewer, append([]Permission{unloaded}, testCatalog...), false))
	if _, ok := withIt[unloaded]; !ok {
		t.Errorf("effective set omits %q even though the catalog declares it", unloaded)
	}
}
