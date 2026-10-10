// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sourcescope_test

import (
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

// A workspace-confined membership still carries its editor role. That role must
// not bypass connector assignments or source bindings outside the actor's scope.
func TestResolverConfinedEditorCannotUseTenantRBAC(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "confined-editor")
	wsA := h.createWorkspace(tenant, "a")
	h.createWorkspace(tenant, "b")
	actor := h.createAgent(tenant, "a-bot", wsA)
	session := h.createSession(tenant, "a-session", actor.ID, wsA)
	h.createAssignmentOK(admin, tenant, "own-connector", "a", true)
	h.createAssignmentOK(admin, tenant, "foreign-connector", "b", true)
	groupMember := h.createAgent(tenant, "default-bot", model.ID(""))
	h.addAgentToGroup(tenant, groupMember.ID, "default-bots", model.ID(""))
	if r := h.createBinding(admin, tenant, map[string]any{
		"source_type": "model", "source_ref": "group-model",
		"scope_tree": "agent_group", "scope_ref": "default-bots", "enabled": true,
	}); r.code != http.StatusCreated {
		t.Fatalf("create binding = %d %s", r.code, r.raw)
	}

	for _, confined := range []bool{true, false} {
		name := "unconfined"
		if confined {
			name = "confined"
		}
		t.Run(name, func(t *testing.T) {
			email := name + "@acme.io"
			user := map[string]any{
				"email": email, "password": "memberpass1", "tenant": tenant.String(), "role": "editor",
			}
			if confined {
				user["workspace_id"] = wsA.String()
			}
			if r := h.do("POST", "/v1/users", admin, user, nil); r.code != http.StatusCreated {
				t.Fatalf("create editor = %d %s", r.code, r.raw)
			}
			login := h.do("POST", "/v1/auth/login", "", map[string]any{"email": email, "password": "memberpass1"}, nil)
			if login.code != http.StatusOK {
				t.Fatalf("login editor = %d %s", login.code, login.raw)
			}
			principal, err := h.authr.Authenticate(t.Context(), login.body["token"].(string))
			if err != nil {
				t.Fatal(err)
			}
			if role, ok := principal.RoleIn(tenant); !ok || role != "editor" {
				t.Fatalf("authenticated role = %q, %v; want editor membership", role, ok)
			}
			if ws, ok := principal.ConfinedWorkspaceIn(tenant); ok != confined || (confined && ws != wsA) {
				t.Fatalf("authenticated confinement = %q, %v; want confined=%v to workspace A", ws, ok, confined)
			}

			for _, tc := range []struct {
				name, sourceType, sourceRef string
				wantAllowed                 bool
			}{
				{"own assignment", "data", "own-connector", true},
				{"foreign assignment", "data", "foreign-connector", !confined},
				{"default workspace group", "model", "group-model", !confined},
			} {
				t.Run(tc.name, func(t *testing.T) {
					agentDecision, err := h.resolver.ResolveForAgent(t.Context(), tenant, principal, actor.ExternalID, tc.sourceType, tc.sourceRef)
					if err != nil || agentDecision.Allowed != tc.wantAllowed {
						t.Errorf("agent: want allowed=%v, got %+v, err=%v", tc.wantAllowed, agentDecision, err)
					}
					sessionDecision, err := h.resolver.ResolveForSession(t.Context(), tenant, principal, session.ExternalID, tc.sourceType, tc.sourceRef)
					if err != nil || sessionDecision.Allowed != tc.wantAllowed {
						t.Errorf("session: want allowed=%v, got %+v, err=%v", tc.wantAllowed, sessionDecision, err)
					}
				})
			}
		})
	}
}
