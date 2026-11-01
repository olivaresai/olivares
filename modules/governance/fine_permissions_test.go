// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
)

func TestCoreReadGrantsAreTenantOnly(t *testing.T) {
	for _, p := range []string{"audit:read", "authz:read", "accessgraph:read", "authenticator:write"} {
		if _, err := validatePerms([]string{p}); err != nil {
			t.Errorf("validate %s: %v", p, err)
		}
		roles := map[string]customRole{"single": {Name: "single", Perms: []string{p}}}
		perms := effectivePermsOf("single", true, "", roles, nil)
		if len(perms) != 1 || !perms[p] {
			t.Fatalf("%s not projected alone: %v", p, perms)
		}
		g := scopedGrant{SubjectKind: subjectUser, SubjectRef: "user", Role: "single", RoleCustom: true, Scope: scopeSpec{Tree: scopeTenant}}
		if err := rejectInertTreeScopedGrant(g, perms); err != nil {
			t.Errorf("tenant: %v", err)
		}
		g.Scope.Tree = scopeWorkspace
		if err := rejectInertTreeScopedGrant(g, perms); err == nil || !strings.Contains(err.Error(), "tenant scope") {
			t.Errorf("workspace grant should explain tenant scope: %v", err)
		}
		kind, _, _ := auth.SplitPermission(auth.Permission(p))
		if auth.IsTreeScopeableKind(kind) {
			t.Errorf("%s entered the tree", p)
		}
	}
}
