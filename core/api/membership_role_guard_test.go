// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// A role change is the same authority as a removal: a human session meets the
// deployment's administrative step-up, the organization never loses its last active
// owner, and a person may lower their own role while another owner remains.
func TestGrantMembershipKeepsTheLastOwnerAndAsksForStepUp(t *testing.T) {
	onEachEngine(t, nil, func(t *testing.T, h *harness) {
		ctx := context.Background()
		root := h.elevatedAdmin()
		tenant := h.createOrg(root, "role-guard")
		owner, ownerTok := h.guardMember(root, tenant, "owner@role-guard.test", auth.RoleOwner)
		second, _ := h.guardMember(root, tenant, "second@role-guard.test", auth.RoleViewer)
		grant := func(tok string, id model.ID, role string) resp {
			return h.do("POST", "/v1/memberships", tok, map[string]any{
				"user_id": id.String(), "tenant": tenant.String(), "role": role,
			}, nil)
		}
		roleOf := func(id model.ID) string {
			var role string
			if err := h.st.AuthView(ctx, func(as store.AuthScope) error {
				ms, _, err := as.Memberships().List(ctx, model.Query{Filters: []model.Filter{
					{Column: "user_id", Op: model.OpEq, Value: id.String()},
					{Column: "target_tenant_id", Op: model.OpEq, Value: tenant.String()},
				}})
				if len(ms) == 1 {
					role = ms[0].Role
				}
				return err
			}); err != nil {
				t.Fatalf("read membership: %v", err)
			}
			return role
		}
		lastOwner := func(name string, r resp) {
			t.Helper()
			e, _ := r.body["error"].(map[string]any)
			if r.code != http.StatusConflict || e["code"] != "last_owner" || e["remedy"] != "Make another member an owner first." {
				t.Errorf("%s = %d %s, want 409 last_owner with its remedy", name, r.code, r.raw)
			}
			if got := roleOf(owner); got != auth.RoleOwner {
				t.Errorf("%s: the refused request left the owner as %q", name, got)
			}
		}

		lastOwner("superadmin demoting the last owner", grant(root, owner, auth.RoleAdmin))
		ws := h.do("POST", "/v1/workspaces", root, map[string]any{"name": "Sales", "slug": "sales"}, tenantHdr(tenant))
		if ws.code != http.StatusCreated {
			t.Fatalf("create workspace = %d %s", ws.code, ws.raw)
		}
		lastOwner("superadmin confining the last owner to a workspace", h.do("POST", "/v1/memberships", root, map[string]any{
			"user_id": owner.String(), "tenant": tenant.String(), "role": auth.RoleOwner, "workspace_id": ws.body["id"],
		}, nil))
		lastOwner("onboarding the last owner again at a lower role", h.do("POST", "/v1/onboard", root, map[string]any{
			"email": "owner@role-guard.test", "role": auth.RoleViewer, "mode": "password", "password": guardPassword,
		}, tenantHdr(tenant)))

		if err := h.authr.SetAdminStepUp(ctx, h.principalOf(root), auth.StepUpPasskey); err != nil {
			t.Fatalf("require passkeys: %v", err)
		}
		for name, c := range map[string]struct {
			id   model.ID
			role string
		}{"another member's role": {second, auth.RoleEditor}, "own role": {owner, auth.RoleAdmin}} {
			r := grant(ownerTok, c.id, c.role)
			if r.code != http.StatusForbidden || errorCode(r) != "step_up_required" {
				t.Errorf("owner session changing %s without step-up = %d %s, want 403 step_up_required", name, r.code, r.raw)
			}
		}
		if got := roleOf(second); got != auth.RoleViewer {
			t.Errorf("a refused role change left the member as %q, want viewer", got)
		}

		h.elevate(ownerTok)
		lastOwner("the last owner lowering itself", grant(ownerTok, owner, auth.RoleAdmin))
		if r := grant(ownerTok, second, auth.RoleOwner); r.code != http.StatusCreated {
			t.Fatalf("make a second owner = %d %s, want 201", r.code, r.raw)
		}
		if r := grant(ownerTok, owner, auth.RoleAdmin); r.code != http.StatusCreated || r.body["role"] != auth.RoleAdmin {
			t.Errorf("an owner lowering itself while another owner remains = %d %s, want 201 admin", r.code, r.raw)
		}
		if got := roleOf(owner); got != auth.RoleAdmin {
			t.Errorf("after lowering itself the person is %q, want admin", got)
		}
	})
}
