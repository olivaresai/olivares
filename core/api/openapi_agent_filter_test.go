// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"net/http"
	"testing"
)

func TestAgentListOpenAPIWorkspaceFilterMatchesRuntime(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "workspace-filter-contract")
	workspaceIDs := make([]string, 0, 2)
	for _, slug := range []string{"one", "two"} {
		ws := h.do("POST", "/v1/workspaces", admin,
			map[string]any{"name": slug, "slug": slug}, tenantHdr(tenant))
		if ws.code != http.StatusCreated {
			t.Fatalf("create workspace: %d %s", ws.code, ws.raw)
		}
		id := ws.body["id"].(string)
		workspaceIDs = append(workspaceIDs, id)
		r := h.do("POST", "/v1/agents", admin,
			map[string]any{"name": slug, "kind": "claude-code", "workspace_id": id}, tenantHdr(tenant))
		if r.code != http.StatusCreated {
			t.Fatalf("create agent: %d %s", r.code, r.raw)
		}
	}
	page := h.do("GET", "/v1/agents?workspace_id="+workspaceIDs[0], admin, nil, tenantHdr(tenant))
	if page.code != http.StatusOK {
		t.Fatalf("filtered list: %d %s", page.code, page.raw)
	}
	items := page.body["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["workspace_id"] != workspaceIDs[0] {
		t.Fatalf("workspace filter returned another workspace's agents: %v", items)
	}
	doc := decodeDoc(t, rawGet(h, "/openapi.json", "", nil))
	op := doc["paths"].(map[string]any)["/v1/agents"].(map[string]any)["get"].(map[string]any)
	for _, raw := range op["parameters"].([]any) {
		param := raw.(map[string]any)
		if param["name"] == "workspace_id" && param["in"] == "query" {
			schema := param["schema"].(map[string]any)
			if param["required"] != false || schema["type"] != "string" || schema["format"] != "uuid" {
				t.Fatalf("workspace filter must be an optional UUID string: %v", param)
			}
			return
		}
	}
	t.Fatal("OpenAPI omits the workspace_id filter the handler accepts")
}
