// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md

package api_test

import (
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// Deleting an agent must take its agent-group memberships with it. The membership table
// has no foreign key to agents, so a row that outlives its agent is never cleaned up by
// the database and every group reader then trips over it (finops#2944).
func TestDeleteAgentRemovesItsGroupMemberships(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	r := h.do("POST", "/v1/agent-groups", admin, map[string]any{"name": "Payments bots", "slug": "payments-bots"}, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("create group = %d %s", r.code, r.raw)
	}
	groupID := r.body["id"].(string)

	var agentIDs []string
	for _, name := range []string{"bot-a", "bot-b"} {
		ar := h.do("POST", "/v1/agents", admin, map[string]any{"name": name, "kind": "claude-code"}, tenantHdr(tenant))
		if ar.code != http.StatusCreated {
			t.Fatalf("create agent = %d %s", ar.code, ar.raw)
		}
		agentID := ar.body["id"].(string)
		if r := h.do("PUT", "/v1/agent-groups/"+groupID+"/members/"+agentID, admin, nil, tenantHdr(tenant)); r.code != http.StatusCreated {
			t.Fatalf("add member = %d %s", r.code, r.raw)
		}
		agentIDs = append(agentIDs, agentID)
	}

	if r := h.do("DELETE", "/v1/agents/"+agentIDs[0], admin, nil, tenantHdr(tenant)); r.code != http.StatusNoContent {
		t.Fatalf("delete agent = %d %s", r.code, r.raw)
	}

	r = h.do("GET", "/v1/agent-groups/"+groupID+"/members", admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("roster = %d %s", r.code, r.raw)
	}
	items := r.body["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["agent_id"] != agentIDs[1] {
		t.Fatalf("roster after deleting one member = %s, want only %s", r.raw, agentIDs[1])
	}

	// The rows are hard-deleted, so the drop is audited on the group as a member removal is.
	r = h.do("GET", "/v1/audit?action=agent_group.member.remove&target_id="+groupID, admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK || len(r.body["items"].([]any)) != 1 {
		t.Fatalf("member-remove audit events of the group = %d %s, want exactly 1", r.code, r.raw)
	}
}

// An agent id that does not exist is still a 404, and the memberships of the agents that do
// exist are left alone: the drain only matches the id it was given.
func TestDeleteMissingAgentIs404AndKeepsOtherMemberships(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme-missing")

	gr := h.do("POST", "/v1/agent-groups", admin, map[string]any{"name": "Bots", "slug": "bots"}, tenantHdr(tenant))
	if gr.code != http.StatusCreated {
		t.Fatalf("create group = %d %s", gr.code, gr.raw)
	}
	groupID := gr.body["id"].(string)
	ar := h.do("POST", "/v1/agents", admin, map[string]any{"name": "bot-a", "kind": "claude-code"}, tenantHdr(tenant))
	if ar.code != http.StatusCreated {
		t.Fatalf("create agent = %d %s", ar.code, ar.raw)
	}
	agentID := ar.body["id"].(string)
	if r := h.do("PUT", "/v1/agent-groups/"+groupID+"/members/"+agentID, admin, nil, tenantHdr(tenant)); r.code != http.StatusCreated {
		t.Fatalf("add member = %d %s", r.code, r.raw)
	}

	if r := h.do("DELETE", "/v1/agents/"+model.NewID().String(), admin, nil, tenantHdr(tenant)); r.code != http.StatusNotFound {
		t.Fatalf("delete missing agent = %d %s, want 404", r.code, r.raw)
	}
	r := h.do("GET", "/v1/agent-groups/"+groupID+"/members", admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK || len(r.body["items"].([]any)) != 1 {
		t.Fatalf("roster after deleting a missing agent = %d %s, want the 1 member kept", r.code, r.raw)
	}
}

// A principal confined to a workspace may delete an agent of its own workspace (core
// agent:write is allowed at the entity route inside the boundary). The handler runs on
// the tenant scope after that route check, so the memberships are dropped with the agent
// whichever workspace their group is in, and the delete neither fails for the confined
// caller nor leaves a row behind. A handler moved onto the confined scope would fail here:
// confinedMemberRepo refuses List.
func TestConfinedAdminDeletesAnAgentWithGroupMemberships(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme-confined")

	ws := h.do("POST", "/v1/workspaces", admin, map[string]any{"name": "Sales", "slug": "sales"}, tenantHdr(tenant))
	if ws.code != http.StatusCreated {
		t.Fatalf("create workspace = %d %s", ws.code, ws.raw)
	}
	salesID := ws.body["id"].(string)

	ar := h.do("POST", "/v1/agents", admin, map[string]any{"name": "bot-a", "kind": "claude-code", "workspace_id": salesID}, tenantHdr(tenant))
	if ar.code != http.StatusCreated {
		t.Fatalf("create agent = %d %s", ar.code, ar.raw)
	}
	agentID := ar.body["id"].(string)
	// One group inside the confined caller's workspace, one outside it (tenant-wide).
	groupIDs := map[string]string{}
	for name, workspace := range map[string]string{"inside": salesID, "outside": ""} {
		gr := h.do("POST", "/v1/agent-groups", admin, map[string]any{"name": name, "slug": name, "workspace_id": workspace}, tenantHdr(tenant))
		if gr.code != http.StatusCreated {
			t.Fatalf("create group %s = %d %s", name, gr.code, gr.raw)
		}
		groupIDs[name] = gr.body["id"].(string)
		if r := h.do("PUT", "/v1/agent-groups/"+groupIDs[name]+"/members/"+agentID, admin, nil, tenantHdr(tenant)); r.code != http.StatusCreated {
			t.Fatalf("add member to %s = %d %s", name, r.code, r.raw)
		}
	}

	if r := h.do("POST", "/v1/users", admin, map[string]any{
		"email": "confined@delete-agent.test", "password": "delete-agent-test1", "tenant": tenant.String(),
		"role": auth.RoleAdmin, "workspace_id": salesID,
	}, nil); r.code != http.StatusCreated {
		t.Fatalf("create confined admin = %d %s", r.code, r.raw)
	}
	confined := h.login("confined@delete-agent.test", "delete-agent-test1")
	if r := h.do("DELETE", "/v1/agents/"+agentID, confined, nil, tenantHdr(tenant)); r.code != http.StatusNoContent {
		t.Fatalf("confined delete agent = %d %s", r.code, r.raw)
	}
	for name, id := range groupIDs {
		r := h.do("GET", "/v1/agent-groups/"+id+"/members", admin, nil, tenantHdr(tenant))
		if r.code != http.StatusOK || len(r.body["items"].([]any)) != 0 {
			t.Errorf("roster of the %s group after the delete = %d %s, want empty", name, r.code, r.raw)
		}
	}
}
