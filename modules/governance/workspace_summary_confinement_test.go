// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// Summary and ordinary GET must honor the same admitted Membership confinement.
// Keep the registered HTTP/auth/scoped-governance path: a handler-only test would
// miss the distinction between tenant:read permission and Workspace confinement.
func TestWorkspaceSummaryConfinement(t *testing.T) {
	t.Cleanup(auth.ResetModuleCatalog)
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "summary-confinement")
	hdr := tenantHdr(tenant)
	wsA := h.createWorkspace(tenant, "summary-scope-a")
	wsB := h.createWorkspace(tenant, "summary-scope-b")
	h.createAgentIn(tenant, "summary-agent-a", wsA)
	h.createAgentIn(tenant, "summary-agent-b1", wsB)
	h.createAgentIn(tenant, "summary-agent-b2", wsB)
	confinedUID, confinedToken := h.confinedUser(admin, tenant, "confined@summary.invalid", auth.RoleViewer, wsA)
	wideUID, wideToken := h.roleUser(admin, tenant, "wide@summary.invalid", auth.RoleViewer)

	created := h.do("POST", "/v1/users", admin, map[string]any{
		"email": "no-permission@summary.invalid", "password": "summary-fixture-only-1",
	}, nil)
	if created.code != http.StatusCreated {
		t.Fatalf("create no-permission user: status=%d", created.code)
	}
	noPermissionUID := created.body["id"].(string)
	login := h.do("POST", "/v1/auth/login", "", map[string]any{
		"email": "no-permission@summary.invalid", "password": "summary-fixture-only-1",
	}, nil)
	if login.code != http.StatusOK {
		t.Fatalf("login no-permission user: status=%d", login.code)
	}
	noPermissionToken := login.body["token"].(string)

	for _, actor := range []struct {
		name, token, uid string
		member           bool
		workspace        model.ID
	}{
		{"confined", confinedToken, confinedUID, true, wsA},
		{"unconfined", wideToken, wideUID, true, ""},
		{"no-permission", noPermissionToken, noPermissionUID, false, ""},
	} {
		p := h.principalOf(actor.token)
		role, member := p.RoleIn(tenant)
		workspace, confined := p.ConfinedWorkspaceIn(tenant)
		if p.Superadmin || p.Kind != auth.KindUser || p.UserID.String() != actor.uid || member != actor.member {
			t.Fatalf("%s principal identity or Membership mismatch", actor.name)
		}
		if member && (role != auth.RoleViewer || !auth.RoleGrants(role, "tenant:read")) {
			t.Fatalf("%s principal does not have the expected viewer permission", actor.name)
		}
		if workspace != actor.workspace || confined != !actor.workspace.IsZero() {
			t.Fatalf("%s principal confinement mismatch", actor.name)
		}
	}
	if !h.principalOf(admin).Superadmin {
		t.Fatal("global administrator control is not a superadmin")
	}

	for _, tc := range []struct {
		name, token, slug string
		workspace         model.ID
		summary           bool
		status, agents    int
	}{
		{"confined/get-A", confinedToken, "summary-scope-a", wsA, false, http.StatusOK, 1},
		{"confined/get-B", confinedToken, "summary-scope-b", wsB, false, http.StatusNotFound, 2},
		{"confined/summary-A", confinedToken, "summary-scope-a", wsA, true, http.StatusOK, 1},
		{"confined/summary-B", confinedToken, "summary-scope-b", wsB, true, http.StatusNotFound, 2},
		{"no-permission/get-A", noPermissionToken, "summary-scope-a", wsA, false, http.StatusForbidden, 1},
		{"no-permission/summary-A", noPermissionToken, "summary-scope-a", wsA, true, http.StatusForbidden, 1},
		{"unconfined/get-B", wideToken, "summary-scope-b", wsB, false, http.StatusOK, 2},
		{"unconfined/summary-B", wideToken, "summary-scope-b", wsB, true, http.StatusOK, 2},
		{"global/summary-B", admin, "summary-scope-b", wsB, true, http.StatusOK, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := "/v1/workspaces/" + tc.workspace.String()
			if tc.summary {
				path += "/summary"
			}
			r := h.do("GET", path, tc.token, nil, hdr)
			if r.code != tc.status {
				t.Fatalf("status=%d, want %d", r.code, tc.status)
			}
			if tc.status != http.StatusOK {
				for _, key := range []string{
					"id", "workspace_id", "tenant_id", "name", "slug", "is_default", "items",
					"agent_count", "session_count", "resource_count", "group_count",
					"agent_count_capped", "session_count_capped", "resource_count_capped", "group_count_capped",
				} {
					if _, present := r.body[key]; present {
						t.Errorf("refusal discloses protected field %s", key)
					}
				}
				if strings.Contains(r.raw, tc.workspace.String()) || strings.Contains(r.raw, tc.slug) {
					t.Error("refusal discloses the protected Workspace identity")
				}
				return
			}
			idKey := "id"
			if tc.summary {
				idKey = "workspace_id"
			}
			if r.body[idKey] != tc.workspace.String() || r.body["name"] != tc.slug || r.body["slug"] != tc.slug {
				t.Fatal("permitted response has incorrect Workspace identity")
			}
			if !tc.summary {
				return
			}
			if r.body["is_default"] != false {
				t.Error("named Workspace reported as default")
			}
			for key, count := range map[string]int{
				"agent_count": tc.agents, "session_count": 0, "resource_count": 0, "group_count": 0,
			} {
				if r.body[key] != float64(count) || r.body[key+"_capped"] != false {
					t.Errorf("incorrect exact count or capped flag for %s", key)
				}
			}
		})
	}
}
