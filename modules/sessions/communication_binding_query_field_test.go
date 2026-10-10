// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
)

// An unconfined admin who lists protocol bindings or specs gets the published
// invalid_command refusal, and the body must name the query parameter to fix
// (evidence_ref), which the CLI prints as "; check <field>".
func TestProtocolBindingListRefusalNamesQueryField(t *testing.T) {
	h, _ := newProtocolBindingAPIHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "protocol-binding-query-field")
	workspaceID := workAPICreateWorkspace(t, h, tenant, "protocol-query-field")
	workspace := workspaceID.String()

	cases := []struct {
		name, query, field string
	}{
		{"missing workspace", "", "workspace_id"},
		{"malformed workspace", "?workspace_id=not-a-uuid", "workspace_id"},
		{"zero workspace", "?workspace_id=" + "00000000-0000-0000-0000-000000000000", "workspace_id"},
		{"bad limit", "?workspace_id=" + workspace + "&limit=0", "limit"},
		{"bad cursor", "?workspace_id=" + workspace + "&cursor=nope", "cursor"},
		{"repeated workspace", "?workspace_id=" + workspace + "&workspace_id=" + workspace, "workspace_id"},
		{"repeated limit", "?workspace_id=" + workspace + "&limit=1&limit=2", "limit"},
		{"unknown parameter", "?workspace_id=" + workspace + "&wokspace_id=x", "wokspace_id"},
		{"unknown uppercase parameter", "?workspace_id=" + workspace + "&Bad=x", "query"},
		{"unknown long parameter", "?workspace_id=" + workspace + "&" + strings.Repeat("a", 65) + "=x", "query"},
		{"unknown escape parameter", "?workspace_id=" + workspace + "&%1b[31m=x", "query"},
		{"repeated protocol", "?workspace_id=" + workspace + "&protocol=a2a&protocol=mcp", "protocol"},
		{"first of several bad parameters", "?zzz=1&aaa=1&mmm=1&workspace_id=" + workspace, "aaa"},
		{"repeated beats later unknown", "?aaa=1&aaa=2&zzz=1&workspace_id=" + workspace, "aaa"},
		{"parameters beat workspace", "?aaa=1", "aaa"},
		{"workspace beats limit", "?limit=0", "workspace_id"},
		{"limit beats cursor", "?workspace_id=" + workspace + "&limit=0&cursor=nope", "limit"},
		{"limit above maximum", "?workspace_id=" + workspace + "&limit=201", "limit"},
	}
	for _, route := range []string{
		"/v1/m/sessions/protocol-bindings",
		"/v1/m/sessions/protocol-binding-specs",
	} {
		for _, tc := range cases {
			// Repeated, so a refusal that depends on map order fails reliably.
			for range 10 {
				got := h.do(http.MethodGet, route+tc.query, admin, tenantHdr(tenant))
				if got.code != http.StatusBadRequest || workAPIErrorCode(got) != "invalid_command" || got.body["verdict"] != "ROTO" ||
					got.body["evidence_ref"] != tc.field || strings.Contains(got.raw, "\x1b") {
					t.Errorf("%s %s = %d %s, want 400 invalid_command verdict=ROTO evidence_ref=%s",
						route, tc.name, got.code, got.raw, tc.field)
					break
				}
			}
		}
	}

	bindingOnly := []struct{ name, query, field string }{
		{"bad work item", "?workspace_id=" + workspace + "&work_item_id=x", "work_item_id"},
		{"bad binding spec", "?workspace_id=" + workspace + "&binding_spec_id=x", "binding_spec_id"},
		{"bad terminal", "?workspace_id=" + workspace + "&terminal=maybe", "terminal"},
	}
	for _, tc := range bindingOnly {
		got := h.do(http.MethodGet, "/v1/m/sessions/protocol-bindings"+tc.query, admin, tenantHdr(tenant))
		if got.code != http.StatusBadRequest || workAPIErrorCode(got) != "invalid_command" || got.body["verdict"] != "ROTO" ||
			got.body["evidence_ref"] != tc.field {
			t.Errorf("bindings %s = %d %s, want evidence_ref=%s", tc.name, got.code, got.raw, tc.field)
		}
	}
	got := h.do(http.MethodGet, "/v1/m/sessions/protocol-binding-specs?workspace_id="+workspace+"&generation=0",
		admin, tenantHdr(tenant))
	if got.code != http.StatusBadRequest || workAPIErrorCode(got) != "invalid_command" || got.body["verdict"] != "ROTO" ||
		got.body["evidence_ref"] != "generation" {
		t.Errorf("specs bad generation = %d %s, want evidence_ref=generation", got.code, got.raw)
	}

	// A principal confined to one workspace may still omit workspace_id.
	confined := workAPIRoleTokenIn(
		t, h, admin, tenant, auth.RoleAdmin, "protocol-query-field@a.test", workspaceID,
	)
	for _, route := range []string{
		"/v1/m/sessions/protocol-bindings",
		"/v1/m/sessions/protocol-binding-specs",
	} {
		if got := h.do(http.MethodGet, route, confined, tenantHdr(tenant)); got.code != http.StatusOK {
			t.Errorf("%s as confined principal without workspace_id = %d %s, want 200", route, got.code, got.raw)
		}
		if got := h.do(http.MethodGet, route+"?limit=0", confined, tenantHdr(tenant)); got.code != http.StatusBadRequest ||
			got.body["evidence_ref"] != "limit" {
			t.Errorf("%s as confined principal with limit=0 = %d %s, want evidence_ref=limit", route, got.code, got.raw)
		}
	}
}

// The id filters of the binding list keep filtering after the parser change.
func TestProtocolBindingListIDFiltersStillFilter(t *testing.T) {
	h, _ := newProtocolBindingAPIHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "protocol-binding-id-filters")
	workspace := workAPICreateWorkspace(t, h, tenant, "protocol-id-filters")
	first := seedProtocolBindingAPI(t, h, tenant, workspace, "task:filter-1")
	seedProtocolBindingAPI(t, h, tenant, workspace, "task:filter-2")
	base := "/v1/m/sessions/protocol-bindings?workspace_id=" + workspace.String()

	for name, query := range map[string]string{
		"work_item_id":    "&work_item_id=" + first.WorkItemID.String(),
		"binding_spec_id": "&binding_spec_id=" + first.BindingSpecID.String(),
	} {
		got := h.do(http.MethodGet, base+query, admin, tenantHdr(tenant))
		items, _ := got.body["items"].([]any)
		if got.code != http.StatusOK || len(items) != 1 ||
			items[0].(map[string]any)["id"] != first.ID.String() {
			t.Errorf("filter %s = %d %s, want only %s", name, got.code, got.raw, first.ID)
		}
	}
}
