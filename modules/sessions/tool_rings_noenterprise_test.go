// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !enterprise

package sessions

import (
	"context"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Tool version rings (a candidate version tried on one agent group before it
// reaches everyone) are a Business capability. Community keeps the published
// routes and answers 501 business_required for each of them.
func TestCommunityToolRingsRequireBusiness(t *testing.T) {
	h := newHarness(t, New())
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	body := map[string]any{"group_ref": "00000000-0000-4000-8000-000000000001", "version": "9.9.9"}
	for _, c := range []struct{ method, path string }{
		{"GET", "/v1/m/sessions/tool-rings"},
		{"PUT", "/v1/m/sessions/tool-rings/codex"},
		{"POST", "/v1/m/sessions/tool-rings/codex/promote"},
		{"POST", "/v1/m/sessions/tool-rings/codex/rollback"},
	} {
		r := h.doJSON(c.method, c.path, admin, body, tenantHdr(tenant))
		if r.code != http.StatusNotImplemented {
			t.Fatalf("%s %s = %d %s, want 501", c.method, c.path, r.code, r.raw)
		}
		errBody, _ := r.body["error"].(map[string]any)
		if errBody["code"] != "business_required" {
			t.Fatalf("%s %s error = %s, want code business_required", c.method, c.path, r.raw)
		}
	}
}

// A ring stored by a Business build is data this build keeps and ignores: Community
// resolves a version from folder and agent pins only, so a downgraded or swapped
// binary never starts a session on a candidate nobody pinned.
func TestCommunityToolVersionIgnoresStoredRings(t *testing.T) {
	m, st := openProfileModule(t, profileBackends(t)[0], nil)
	defer st.Close()
	ctx := context.Background()
	tenant := ensureTenant(t, st, "rings")
	ws, err := m.createWorkspace(ctx, tenant, CreateWorkspaceParams{Name: "Ringed folder", RootPath: t.TempDir(), Actor: "admin", ActorKind: model.ActorUser})
	if err != nil {
		t.Fatal(err)
	}
	var agent model.Agent
	var group model.AgentGroup
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		var err error
		if agent, err = sc.Agents().Create(ctx, model.Agent{Name: "Member", ExternalID: "member", Kind: "codex", Status: model.StatusActive}); err != nil {
			return err
		}
		if group, err = sc.AgentGroups().Create(ctx, model.AgentGroup{Name: "Ring", Slug: "ring", Status: model.StatusActive}); err != nil {
			return err
		}
		_, err = sc.AgentGroupMembers().Create(ctx, model.AgentGroupMember{GroupID: group.ID, AgentID: agent.ID})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := m.SetToolVersionPin(ctx, tenant, ToolVersionPin{Driver: "codex", WorkspaceRef: ws.WorkspaceRef, Version: "0.1"}, "admin", model.ActorUser); err != nil {
		t.Fatal(err)
	}
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		org, err := sc.Org(ctx)
		if err != nil {
			return err
		}
		org.Settings["tool_version_rings"] = []map[string]any{{"driver": "codex", "group_ref": group.ID.String(), "version": "0.2", "state": "candidate"}}
		_, err = sc.SetOrgSettings(ctx, org.Settings)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if got, err := m.ToolVersion(ctx, tenant, "codex", ws.WorkspaceRef, string(agent.ID)); err != nil || got != "0.1" {
		t.Fatalf("member resolved %q %v, want the folder pin 0.1", got, err)
	}
}
