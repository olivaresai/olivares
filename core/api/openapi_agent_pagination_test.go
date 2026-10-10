// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"fmt"
	"net/http"
	"testing"
)

func TestAgentListOpenAPIDefaultMatchesRuntime(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "pagination-contract")
	for i := range 51 {
		r := h.do("POST", "/v1/agents", admin,
			map[string]any{"name": fmt.Sprintf("agent-%03d", i), "kind": "claude-code"}, tenantHdr(tenant))
		if r.code != http.StatusCreated {
			t.Fatalf("create agent %d: %d %s", i, r.code, r.raw)
		}
	}
	page := h.do("GET", "/v1/agents", admin, nil, tenantHdr(tenant))
	if page.code != http.StatusOK {
		t.Fatalf("list agents: %d %s", page.code, page.raw)
	}
	items := page.body["items"].([]any)
	if len(items) != 50 || page.body["has_more"] != true {
		t.Fatalf("published default must return 50 rows and a next page: rows=%d has_more=%v", len(items), page.body["has_more"])
	}
	doc := decodeDoc(t, rawGet(h, "/openapi.json", "", nil))
	op := doc["paths"].(map[string]any)["/v1/agents"].(map[string]any)["get"].(map[string]any)
	for _, raw := range op["parameters"].([]any) {
		param := raw.(map[string]any)
		if param["name"] == "limit" && param["in"] == "query" {
			got := param["schema"].(map[string]any)["default"]
			if got != float64(len(items)) {
				t.Fatalf("OpenAPI limit default=%v; HTTP returned %d rows without a limit", got, len(items))
			}
			return
		}
	}
	t.Fatal("list agents has no documented limit parameter")
}
