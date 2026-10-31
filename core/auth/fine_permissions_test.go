// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
)

func TestNarrowGrantableCorePermissions(t *testing.T) {
	for _, p := range []auth.Permission{"authenticator:write", "audit:read", "authz:read", "accessgraph:read"} {
		if !auth.IsGrantablePermission(p) {
			t.Errorf("%s must be grantable", p)
		}
		kind, _, _ := auth.SplitPermission(p)
		if !auth.IsScopeableKind(kind) || auth.IsTreeScopeableKind(kind) {
			t.Errorf("%s must be grantable outside the tree only", p)
		}
		if !slices.Contains(auth.RoleResourcePerms(auth.RoleAdmin), p) {
			t.Errorf("admin cannot delegate %s", p)
		}
	}
	for _, p := range []auth.Permission{"authenticator:read", "authenticator:admin", "audit:write", "authz:admin", "accessgraph:write", "membership:write"} {
		if auth.IsGrantablePermission(p) {
			t.Errorf("unexpected grantable permission %s", p)
		}
	}
	for _, role := range []string{auth.RoleViewer, auth.RoleEditor, auth.RoleAdmin, auth.RoleOwner} {
		if got, want := auth.RoleGrants(role, "authenticator:write"), auth.RoleRank(role) >= auth.RoleRank(auth.RoleAdmin); got != want {
			t.Errorf("reset: %s = %v want %v", role, got, want)
		}
		if got, want := auth.RoleGrants(role, "sessions:output:read"), auth.RoleRank(role) >= auth.RoleRank(auth.RoleEditor); got != want {
			t.Errorf("output: %s = %v want %v", role, got, want)
		}
	}
}

// Captured from the stable and beta documents before splitting the narrow routes.
func TestOpenAPIBuiltinRolePermissionsPreserved(t *testing.T) {
	raw, err := os.ReadFile("testdata/builtin-role-permissions.json")
	if err != nil {
		t.Fatal(err)
	}
	var base map[auth.Permission]string
	if err := json.Unmarshal(raw, &base); err != nil {
		t.Fatal(err)
	}
	for p, holders := range base {
		for role, letter := range map[string]string{"viewer": "v", "editor": "e", "admin": "a", "owner": "o"} {
			if got, want := auth.RoleGrants(role, p), strings.Contains(holders, letter); got != want {
				t.Errorf("%s / %s = %v, base %v", role, p, got, want)
			}
		}
	}
}

func TestTenantCorePermissionRoleMatrix(t *testing.T) {
	for _, tc := range []struct {
		permission auth.Permission
		minimum    string
	}{
		{"audit:read", auth.RoleViewer}, {"authz:read", auth.RoleEditor}, {"accessgraph:read", auth.RoleEditor}, {"authenticator:write", auth.RoleAdmin},
	} {
		for _, role := range []string{auth.RoleViewer, auth.RoleEditor, auth.RoleAdmin, auth.RoleOwner} {
			if got, want := auth.RoleGrants(role, tc.permission), auth.RoleRank(role) >= auth.RoleRank(tc.minimum); got != want {
				t.Errorf("%s on %s = %v, want %v", role, tc.permission, got, want)
			}
		}
	}
}
