// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"net/http"
	"reflect"
	"sort"
	"testing"
)

func TestGovernanceClosureRequirednessMatchesDecoders(t *testing.T) {
	t.Parallel()
	optional := map[string]bool{
		http.MethodPost + " /nhi/{ref}/rotate":            true,
		http.MethodPost + " /nhi/{ref}/offboard":          true,
		http.MethodPost + " /nhi/{ref}/offboard/finalize": true,
	}
	for _, route := range []operation{
		{method: http.MethodPost, pattern: "/pdp/validate"},
		{method: http.MethodPost, pattern: "/pdp/explain"},
		{method: http.MethodPost, pattern: "/pdp/dry-run"},
		{method: http.MethodPost, pattern: "/pdp/publish"},
		{method: http.MethodPost, pattern: "/pdp/rollback"},
		{method: http.MethodPost, pattern: "/decisions/replay"},
		{method: http.MethodPost, pattern: "/breakglass"},
		{method: http.MethodPost, pattern: "/breakglass/consume"},
		{method: http.MethodPost, pattern: "/breakglass/{id}/review"},
		{method: http.MethodPut, pattern: "/nhi/{ref}/ownership"},
		{method: http.MethodPut, pattern: "/nhi/{ref}/policy"},
		{method: http.MethodPost, pattern: "/nhi/{ref}/rotate"},
		{method: http.MethodPost, pattern: "/nhi/{ref}/offboard"},
		{method: http.MethodPost, pattern: "/nhi/{ref}/offboard/finalize"},
		{method: http.MethodPost, pattern: "/killswitch"},
		{method: http.MethodPost, pattern: "/killswitch/{id}/reenable"},
		{method: http.MethodPost, pattern: "/killswitch/{id}/review"},
		{method: http.MethodPost, pattern: "/guardian/rules"},
		{method: http.MethodPut, pattern: "/guardian/rules/{id}"},
		{method: http.MethodPost, pattern: "/rbac/roles"},
		{method: http.MethodPut, pattern: "/rbac/roles/{name}"},
		{method: http.MethodPost, pattern: "/rbac/permission-groups"},
		{method: http.MethodPut, pattern: "/rbac/permission-groups/{name}"},
		{method: http.MethodPost, pattern: "/rbac/grants"},
		{method: http.MethodPost, pattern: "/rbac/inheritance-filters"},
		{method: http.MethodPost, pattern: "/agent-risk-profiles/classify"},
		{method: http.MethodPut, pattern: "/agent-risk-profiles/{id}/tier"},
		{method: http.MethodPost, pattern: "/routine-policies"},
		{method: http.MethodPut, pattern: "/routine-policies/{id}"},
		{method: http.MethodPost, pattern: "/agentcore-export/plan"},
		{method: http.MethodPost, pattern: "/agentcore-export/apply"},
	} {
		body, ok := governanceRequestBody(route.method, route.pattern)
		if !ok {
			t.Fatalf("%s %s requestBody missing", route.method, route.pattern)
		}
		wantRequired := !optional[route.method+" "+route.pattern]
		if body["required"] != wantRequired {
			t.Errorf("%s %s required = %#v, want %t", route.method, route.pattern, body["required"], wantRequired)
		}
		schema := governanceClosureObjectSchema(t, governanceSchemaFromBody(t, body))
		if schema["additionalProperties"] != false {
			t.Errorf("%s %s is not the handler's strict object", route.method, route.pattern)
		}
	}
}

func TestGovernanceClosureBodylessHandlersStayBodyless(t *testing.T) {
	t.Parallel()
	for _, pattern := range []string{"/nhi/{ref}/restore", "/agent-risk-profiles/{id}/review"} {
		route := operation{method: http.MethodPost, pattern: pattern}
		decl, ok := governanceRequestBodyDeclarationFor(route.method, route.pattern)
		if !ok || decl.kind != governanceBodyless {
			t.Fatalf("POST %s = (%#v, %t), want bodyless", pattern, decl, ok)
		}
		if body, ok := governanceRequestBody(route.method, route.pattern); ok || body != nil {
			t.Fatalf("POST %s unexpectedly publishes %#v", pattern, body)
		}
	}
}

func TestGovernanceClosureSchemaLandmarks(t *testing.T) {
	t.Parallel()
	tests := []struct {
		schema   map[string]any
		required []string
	}{
		{governancePDPRollbackSchema(), []string{"engine", "revision"}},
		{governanceBreakGlassActivateSchema(), []string{"reason"}},
		{governanceKillSwitchEngageSchema(), []string{"reason", "scope_kind"}},
		{governanceGuardianCreateSchema(), []string{"action", "mode", "name"}},
		{governanceScopedGrantSchema(), []string{"role", "scope_tree", "subject_kind", "subject_ref"}},
		{governanceInheritanceFilterSchema(), []string{"scope_class", "scope_ref", "scope_tree"}},
		{governanceAgentRiskClassifySchema(), []string{"agent_id"}},
		{governanceRoutinePolicyCreateSchema(), []string{"name", "scope_kind"}},
		{governanceAgentCoreApplySchema(), []string{"plan_hash"}},
	}
	for _, test := range tests {
		if got := governanceClosureSortedStrings(test.schema["required"]); !reflect.DeepEqual(got, test.required) {
			t.Errorf("required = %v, want %v for %#v", got, test.required, test.schema)
		}
	}
	if got := len(governanceNHIOwnershipSchema()["anyOf"].([]any)); got != 2 {
		t.Fatalf("NHI ownership selector branches = %d, want 2", got)
	}
}

func governanceClosureSortedStrings(raw any) []string {
	var out []string
	switch values := raw.(type) {
	case []string:
		out = append(out, values...)
	case []any:
		for _, value := range values {
			if text, ok := value.(string); ok {
				out = append(out, text)
			}
		}
	}
	sort.Strings(out)
	return out
}

func governanceClosureObjectSchema(t *testing.T, schema map[string]any) map[string]any {
	t.Helper()
	if schema["type"] == "object" {
		return schema
	}
	for _, raw := range schema["anyOf"].([]any) {
		candidate := raw.(map[string]any)
		if candidate["type"] == "object" {
			return candidate
		}
	}
	t.Fatalf("schema has no object branch: %#v", schema)
	return nil
}
