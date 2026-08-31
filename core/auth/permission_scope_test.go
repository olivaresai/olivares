// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"testing"

	"github.com/olivaresai/olivares/core/auth"
)

func TestScopeableKinds(t *testing.T) {
	kinds := auth.ScopeableKinds()
	if !auth.IsScopeableKind("agent") || !auth.IsScopeableKind("model") || !auth.IsScopeableKind("resource") {
		t.Error("agent/model/resource must be scopeable")
	}
	// IAM / system / unknown kinds are NOT scopeable (their resources are not in the
	// Tree — see the contract's honest limits).
	for _, k := range []string{"user", "membership", "token", "tenant", "audit", "system", "nope"} {
		if auth.IsScopeableKind(k) {
			t.Errorf("%q must not be scopeable", k)
		}
	}
	// The returned slice is a copy — mutating it must not corrupt the catalog.
	kinds[0] = "MUTATED"
	if auth.IsScopeableKind("MUTATED") {
		t.Error("ScopeableKinds must return a defensive copy")
	}
}

func TestRoleResourcePerms(t *testing.T) {
	has := func(ps []auth.Permission, want string) bool {
		for _, p := range ps {
			if string(p) == want {
				return true
			}
		}
		return false
	}

	viewer := auth.RoleResourcePerms(auth.RoleViewer)
	if !has(viewer, "agent:read") || has(viewer, "agent:write") {
		t.Errorf("viewer resource perms wrong: %v", viewer)
	}
	// Viewer's audit:read / tenant:read are NOT resource perms (not scopeable kinds).
	if has(viewer, "audit:read") || has(viewer, "tenant:read") {
		t.Errorf("viewer must not expose non-resource reads in RoleResourcePerms: %v", viewer)
	}

	admin := auth.RoleResourcePerms(auth.RoleAdmin)
	if !has(admin, "agent:write") {
		t.Error("admin must confer agent:write")
	}
	if has(admin, "agent:admin") {
		t.Error("built-in admin must NOT confer the resource admin verb (only owner)")
	}
	// IAM permissions are filtered out (not scopeable).
	if has(admin, "user:write") || has(admin, "membership:write") || has(admin, "tenant:admin") {
		t.Errorf("admin RoleResourcePerms must exclude IAM/tenant perms: %v", admin)
	}

	owner := auth.RoleResourcePerms(auth.RoleOwner)
	if !has(owner, "agent:admin") {
		t.Error("owner must confer the resource admin verb")
	}

	// Every returned permission is a scopeable "<kind>:<verb>".
	for _, role := range []string{auth.RoleViewer, auth.RoleEditor, auth.RoleAdmin, auth.RoleOwner} {
		for _, p := range auth.RoleResourcePerms(role) {
			if !auth.IsScopeableKind(p.Resource()) {
				t.Errorf("role %q yielded non-scopeable perm %q", role, p)
			}
		}
	}

	if got := auth.RoleResourcePerms("nonexistent"); len(got) != 0 {
		t.Errorf("unknown role must yield no perms, got %v", got)
	}
}
