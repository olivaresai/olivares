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

// `folder` scope tree for delegated administration. A grant bounded to a folder
// projects `resource in Resource::"<id>"`; the engine resolves the acted-on resource's folder
// ancestors from its materialized Path, so the permit covers the whole subtree (downward
// inheritance). These are the pure tests; the store-backed ceiling + enforcement live in the
// harness tests (s399_folder_test.go).

func TestProjectManagedCedarFolder(t *testing.T) {
	roles := map[string]customRole{}
	groups := map[string]permGroup{}
	grants := []scopedGrant{
		{SubjectKind: subjectUser, SubjectRef: "u1", Role: auth.RoleAdmin, Scope: scopeSpec{Tree: scopeFolder, Ref: "res-folder-1"}},
	}
	src := projectManagedCedar(grants, roles, groups)
	if !strings.Contains(src, `principal in User::"u1"`) {
		t.Errorf("missing user subject permit:\n%s", src)
	}
	// The folder scope rides the Resource tree — the SAME clause a free-form folder grant uses.
	if !strings.Contains(src, `resource in Resource::"res-folder-1"`) {
		t.Errorf("folder scope must project `resource in Resource::\"<id>\"`:\n%s", src)
	}
	if _, err := compileGrantSet(src); err != nil {
		t.Fatalf("folder projection does not compile: %v\n%s", err, src)
	}
	if src2 := projectManagedCedar(grants, roles, groups); src2 != src {
		t.Error("folder projection is not deterministic")
	}
}

// Sharing a folder is a scoped grant, so it is capped by the delegation ceiling: someone who
// holds Read and Access Control on a folder can share Read, never Write.
func TestFolderSharerCeiling(t *testing.T) {
	// The mounted governance module declares Access Control; without it the role below
	// would confer Read alone.
	auth.ResetModuleCatalog()
	if err := auth.RegisterModulePermissions([]auth.Permission{auth.PermRBACAdmin}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(auth.ResetModuleCatalog)
	tenant := model.TenantID("t1")
	sharer := auth.ScopedPrincipal("c", "sharer", tenant, auth.RoleViewer)
	roles := map[string]customRole{"folder-sharer": {Name: "folder-sharer", Perms: []string{"resource:read", string(auth.PermRBACAdmin)}}}
	folder := scopeSpec{Tree: scopeFolder, Ref: "f", Class: "resource"}
	held := []scopedGrant{{SubjectKind: subjectRole, SubjectRef: auth.RoleViewer, Role: "folder-sharer", RoleCustom: true, Scope: scopeSpec{Tree: scopeFolder, Ref: "f"}}}
	share := func(role string, scope scopeSpec) error {
		g := scopedGrant{SubjectKind: subjectGroup, SubjectRef: "g", Role: role, Scope: scope}
		return canDelegate(context.Background(), nil, sharer, tenant, g, held, roles, nil)
	}
	if err := share(auth.RoleViewer, folder); err != nil {
		t.Errorf("sharing Read within the sharer's rights: %v", err)
	}
	const sentence = "cannot delegate above your own scope, role or expiry"
	if msg, ok := asCeiling(share(auth.RoleEditor, folder)); !ok || msg != sentence {
		t.Errorf("sharing Write: %q, want the ceiling sentence %q", msg, sentence)
	}
}

// The folder-id is escaped like any other operator-influenced string, so it can never break
// out of the Cedar literal.
func TestCedarScopeWhenFolderEscapes(t *testing.T) {
	got := cedarScopeWhen(scopeSpec{Tree: scopeFolder, Ref: `a"b`})
	if !strings.Contains(got, `Resource::"a\"b"`) {
		t.Errorf("folder ref must be Cedar-escaped, got %q", got)
	}
}

// Pure scopeContains folder cases that need no store: the same-folder short-circuit, and the
// no-upward-escalation guards (a folder never contains a workspace/agent-group/tenant).
func TestScopeContainsFolderPure(t *testing.T) {
	ctx := context.Background()
	tt := []struct {
		name         string
		outer, inner scopeSpec
		want         bool
	}{
		{"folder ⊇ same folder", scopeSpec{Tree: scopeFolder, Ref: "f"}, scopeSpec{Tree: scopeFolder, Ref: "f"}, true},
		{"folder ⊉ tenant", scopeSpec{Tree: scopeFolder, Ref: "f"}, scopeSpec{Tree: scopeTenant}, false},
		{"folder ⊉ workspace", scopeSpec{Tree: scopeFolder, Ref: "f"}, scopeSpec{Tree: scopeWorkspace, Ref: "w"}, false},
		{"folder ⊉ agent_group", scopeSpec{Tree: scopeFolder, Ref: "f"}, scopeSpec{Tree: scopeAgentGroup, Ref: "g"}, false},
		{"tenant ⊇ folder", scopeSpec{Tree: scopeTenant}, scopeSpec{Tree: scopeFolder, Ref: "f"}, true},
		{"class mismatch blocks folder", scopeSpec{Tree: scopeFolder, Ref: "f", Class: "agent"}, scopeSpec{Tree: scopeFolder, Ref: "f", Class: "model"}, false},
		// Class asymmetry (empty = any): the over-delegation direction is the guarded one.
		{"folder any-class ⊇ specific-class", scopeSpec{Tree: scopeFolder, Ref: "f"}, scopeSpec{Tree: scopeFolder, Ref: "f", Class: "model"}, true},
		{"folder specific-class ⊉ any-class", scopeSpec{Tree: scopeFolder, Ref: "f", Class: "model"}, scopeSpec{Tree: scopeFolder, Ref: "f"}, false},
	}
	for _, tc := range tt {
		got, err := scopeContains(ctx, nil, tc.outer, tc.inner)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got != tc.want {
			t.Errorf("%s: scopeContains = %v, want %v", tc.name, got, tc.want)
		}
	}
}
