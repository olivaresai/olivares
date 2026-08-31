// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// folder-scoped delegated administration, end to end: a folder-scoped grant
// authored via REST enforces on the real path with downward subtree inheritance, the
// delegation ceiling confines a folder-admin to its subtree, and the catalog advertises the
// new tree.

func TestScopedAdminFolderScopeE2E(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "folderco")

	// Seed /finance (folder) → /finance/reports (a leaf resource), plus /public outside it.
	var rootID, childID, outsideID model.ID
	if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		root, err := sc.Resources().Create(context.Background(), model.Resource{Name: "finance", Kind: "folder"})
		if err != nil {
			return err
		}
		child, err := sc.Resources().CreateUnder(context.Background(), root.ID, model.Resource{Name: "reports", Kind: "postgres.table"})
		if err != nil {
			return err
		}
		outside, err := sc.Resources().Create(context.Background(), model.Resource{Name: "public", Kind: "s3.bucket"})
		rootID, childID, outsideID = root.ID, child.ID, outside.ID
		return err
	}); err != nil {
		t.Fatalf("seed resource tree: %v", err)
	}

	// The catalog advertises the folder tree.
	if r := h.rbac("GET", "catalog", admin, tenant, nil); r.code != http.StatusOK || !strings.Contains(r.raw, `"folder"`) {
		t.Errorf("catalog must advertise the folder scope tree, got %d %s", r.code, r.raw)
	}

	// The superadmin elevates every viewer to admin WITHIN the /finance subtree.
	if r := h.rbac("POST", "grants", admin, tenant, map[string]any{
		"subject_kind": "role", "subject_ref": "viewer", "role": "admin",
		"scope_tree": "folder", "scope_ref": rootID.String(),
	}); r.code != http.StatusCreated {
		t.Fatalf("create folder-scoped grant = %d %s", r.code, r.raw)
	}

	viewer := auth.ScopedPrincipal("cred-v", "v", tenant, auth.RoleViewer)
	// A resource UNDER the folder inherits the grant (downward)...
	if sd := h.scoped(tenant, viewer, "resource:write", childID); sd.Effect != auth.EffectGrant {
		t.Errorf("a resource under the granted folder must be GRANTED, got %v (%s)", sd.Effect, sd.Reason)
	}
	// ...but a resource outside the folder does not.
	if sd := h.scoped(tenant, viewer, "resource:write", outsideID); sd.Effect != auth.EffectAbstain {
		t.Errorf("a resource outside the folder must ABSTAIN, got %v (%s)", sd.Effect, sd.Reason)
	}

	// Validation: a folder scope needs a ref, and the ref must name a real resource.
	if r := h.rbac("POST", "grants", admin, tenant, map[string]any{
		"subject_kind": "role", "subject_ref": "viewer", "role": "editor", "scope_tree": "folder",
	}); r.code != http.StatusBadRequest {
		t.Errorf("folder scope without a ref must be 400, got %d %s", r.code, r.raw)
	}
	if r := h.rbac("POST", "grants", admin, tenant, map[string]any{
		"subject_kind": "role", "subject_ref": "viewer", "role": "editor",
		"scope_tree": "folder", "scope_ref": "01JZZZZZZZZZZZZZZZZZZZZZZZ",
	}); r.code != http.StatusBadRequest {
		t.Errorf("folder scope with an unknown resource id must be 400, got %d %s", r.code, r.raw)
	}
}

// Downward-only inheritance is the whole security property of folder scope: a folder grant
// reaches DESCENDANTS but never an ANCESTOR or a SIBLING subtree (asserted at ENFORCEMENT, not
// just the delegation ceiling).
func TestScopedAdminFolderGrantIsDownwardOnly(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "folderinh")

	// /parent → /parent/anchor → /parent/anchor/child, plus /parent/sibling.
	var parentID, anchorID, childID, siblingID model.ID
	if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		parent, err := sc.Resources().Create(context.Background(), model.Resource{Name: "parent", Kind: "folder"})
		if err != nil {
			return err
		}
		anchor, err := sc.Resources().CreateUnder(context.Background(), parent.ID, model.Resource{Name: "anchor", Kind: "folder"})
		if err != nil {
			return err
		}
		child, err := sc.Resources().CreateUnder(context.Background(), anchor.ID, model.Resource{Name: "child", Kind: "postgres.table"})
		if err != nil {
			return err
		}
		sibling, err := sc.Resources().CreateUnder(context.Background(), parent.ID, model.Resource{Name: "sibling", Kind: "postgres.table"})
		parentID, anchorID, childID, siblingID = parent.ID, anchor.ID, child.ID, sibling.ID
		return err
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Every viewer is elevated to admin on the ANCHOR folder's subtree.
	if r := h.rbac("POST", "grants", admin, tenant, map[string]any{
		"subject_kind": "role", "subject_ref": "viewer", "role": "admin", "scope_tree": "folder", "scope_ref": anchorID.String(),
	}); r.code != http.StatusCreated {
		t.Fatalf("grant = %d %s", r.code, r.raw)
	}

	viewer := auth.ScopedPrincipal("cred-v", "v", tenant, auth.RoleViewer)
	if sd := h.scoped(tenant, viewer, "resource:write", childID); sd.Effect != auth.EffectGrant {
		t.Errorf("a descendant must be GRANTED, got %v (%s)", sd.Effect, sd.Reason)
	}
	if sd := h.scoped(tenant, viewer, "resource:write", parentID); sd.Effect == auth.EffectGrant {
		t.Error("a folder grant must NOT inherit UPWARD to the parent")
	}
	if sd := h.scoped(tenant, viewer, "resource:write", siblingID); sd.Effect == auth.EffectGrant {
		t.Error("a folder grant must NOT reach a SIBLING subtree")
	}
}

// A folder-admin may sub-delegate ONLY within its subtree — never a sibling, an ancestor, or
// a broader tenant scope (no upward escalation).
func TestScopedAdminFolderSubDelegation(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "folderdel")

	// Tree: /parent → /parent/mid → /parent/mid/leaf, plus /parent/sibling.
	var parentID, midID, leafID, siblingID model.ID
	if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		parent, err := sc.Resources().Create(context.Background(), model.Resource{Name: "parent", Kind: "folder"})
		if err != nil {
			return err
		}
		mid, err := sc.Resources().CreateUnder(context.Background(), parent.ID, model.Resource{Name: "mid", Kind: "folder"})
		if err != nil {
			return err
		}
		leaf, err := sc.Resources().CreateUnder(context.Background(), mid.ID, model.Resource{Name: "leaf", Kind: "folder"})
		if err != nil {
			return err
		}
		sibling, err := sc.Resources().CreateUnder(context.Background(), parent.ID, model.Resource{Name: "sibling", Kind: "folder"})
		parentID, midID, leafID, siblingID = parent.ID, mid.ID, leaf.ID, sibling.ID
		return err
	}); err != nil {
		t.Fatalf("seed folder tree: %v", err)
	}

	uUID, uTok := h.roleUser(admin, tenant, "fa@folderdel.io", auth.RoleViewer)
	vUID, _ := h.roleUser(admin, tenant, "d@folderdel.io", auth.RoleViewer)

	// Superadmin makes U a FOLDER-admin of /parent/mid (admin-capable ⇒ may sub-delegate).
	if r := h.rbac("POST", "grants", admin, tenant, map[string]any{
		"subject_kind": "user", "subject_ref": uUID, "role": "admin", "scope_tree": "folder", "scope_ref": midID.String(),
	}); r.code != http.StatusCreated {
		t.Fatalf("seed folder-admin = %d %s", r.code, r.raw)
	}

	del := func(scopeRef string) int {
		return h.rbac("POST", "grants", uTok, tenant, map[string]any{
			"subject_kind": "user", "subject_ref": vUID, "role": "editor", "scope_tree": "folder", "scope_ref": scopeRef,
		}).code
	}
	if c := del(midID.String()); c != http.StatusCreated {
		t.Errorf("folder-admin must sub-delegate within its own folder, got %d", c)
	}
	if c := del(leafID.String()); c != http.StatusCreated {
		t.Errorf("folder-admin must sub-delegate to a descendant folder, got %d", c)
	}
	if c := del(siblingID.String()); c != http.StatusForbidden {
		t.Errorf("folder-admin must NOT delegate to a sibling folder, got %d", c)
	}
	if c := del(parentID.String()); c != http.StatusForbidden {
		t.Errorf("folder-admin must NOT delegate UP to the parent folder, got %d", c)
	}
	// Nor escape to a tenant scope.
	if r := h.rbac("POST", "grants", uTok, tenant, map[string]any{
		"subject_kind": "user", "subject_ref": vUID, "role": "editor", "scope_tree": "tenant",
	}); r.code != http.StatusForbidden {
		t.Errorf("folder-admin must NOT escalate to a tenant scope, got %d %s", r.code, r.raw)
	}
}

// A workspace-admin may NOT delegate a folder grant at all (adversarial-review fix): a
// Resource's workspace_id is decoupled from its tree position, so a folder anchored in the
// admin's workspace could enclose descendants in OTHER workspaces and the folder permit
// carries no workspace bound — allowing the delegation would be a cross-workspace escape.
// Only a tenant admin or a folder admin may delegate folders.
func TestScopedAdminWorkspaceAdminCannotDelegateFolder(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "wsfolder")
	wsID := h.createWorkspace(tenant, "eng")

	var inWs model.ID
	if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		f1, err := sc.Resources().Create(context.Background(), model.Resource{Name: "eng-secrets", Kind: "folder", WorkspaceID: wsID})
		inWs = f1.ID
		return err
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	uUID, uTok := h.roleUser(admin, tenant, "wa@wsfolder.io", auth.RoleViewer)
	vUID, _ := h.roleUser(admin, tenant, "d@wsfolder.io", auth.RoleViewer)

	if r := h.rbac("POST", "grants", admin, tenant, map[string]any{
		"subject_kind": "user", "subject_ref": uUID, "role": "admin", "scope_tree": "workspace", "scope_ref": "eng",
	}); r.code != http.StatusCreated {
		t.Fatalf("seed workspace-admin = %d %s", r.code, r.raw)
	}

	// Even for a folder that lives in its OWN workspace, a workspace-admin cannot delegate it.
	if r := h.rbac("POST", "grants", uTok, tenant, map[string]any{
		"subject_kind": "user", "subject_ref": vUID, "role": "editor", "scope_tree": "folder", "scope_ref": inWs.String(),
	}); r.code != http.StatusForbidden {
		t.Errorf("a workspace-admin must NOT delegate a folder grant (cross-workspace escape), got %d %s", r.code, r.raw)
	}
	// A tenant admin (superadmin here) still can.
	if r := h.rbac("POST", "grants", admin, tenant, map[string]any{
		"subject_kind": "user", "subject_ref": vUID, "role": "editor", "scope_tree": "folder", "scope_ref": inWs.String(),
	}); r.code != http.StatusCreated {
		t.Errorf("a tenant admin must still delegate folder grants, got %d %s", r.code, r.raw)
	}
}
