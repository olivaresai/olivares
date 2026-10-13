// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import (
	"net/http"
	"testing"
)

func TestConfinedCollectionCreationFromBody(t *testing.T) {
	h := newHarness(t)
	owner := h.adminLogin()
	tenant := h.createOrg(owner, "collection-body")
	own := h.createWorkspace(tenant, "own")
	other := h.createWorkspace(tenant, "other")
	_, admin := h.confinedUser(owner, tenant, "admin@collection.example", "admin", own)
	for _, route := range []string{"agents", "agent-groups"} {
		t.Run(route, func(t *testing.T) {
			in := map[string]any{"name": "created", "workspace_id": own.String()}
			if route == "agents" {
				in["kind"] = "claude-code"
			} else {
				in["slug"] = "created"
			}
			r := h.do("POST", "/v1/"+route, admin, in, tenantHdr(tenant))
			if r.code != http.StatusCreated {
				t.Errorf("create inside membership = %d %s, want 201", r.code, r.raw)
			} else if r.body["workspace_id"] != own.String() {
				t.Errorf("created in another workspace: %s", r.raw)
			}
			for _, ws := range []string{other.String(), "", "not-a-uuid"} {
				in["workspace_id"] = ws
				r = h.do("POST", "/v1/"+route, admin, in, tenantHdr(tenant))
				if r.code != http.StatusForbidden && r.code != http.StatusBadRequest {
					t.Errorf("unsafe create workspace %q = %d %s", ws, r.code, r.raw)
				}
			}
		})
	}
}
