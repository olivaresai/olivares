// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"net/http"
	"reflect"
	"sort"
	"testing"
)

func TestGovernanceRequestBodiesClassifyAllMutations(t *testing.T) {
	t.Parallel()

	tests := []struct {
		method  string
		pattern string
		kind    governanceRequestBodyKind
	}{
		{http.MethodPost, "/roster/sync", governanceBodyless},
		{http.MethodPost, "/agents/{agentID}/identity", governanceBodyful},
		{http.MethodDelete, "/agents/{agentID}/identity", governanceBodyless},
		{http.MethodPost, "/policies", governanceBodyful},
		{http.MethodPut, "/policies/{id}", governanceBodyful},
		{http.MethodDelete, "/policies/{id}", governanceBodyless},
		{http.MethodPost, "/pdp/validate", governanceBodyful},
		{http.MethodPost, "/pdp/explain", governanceBodyful},
		{http.MethodPost, "/pdp/dry-run", governanceBodyful},
		{http.MethodPost, "/pdp/publish", governanceBodyful},
		{http.MethodPost, "/pdp/rollback", governanceBodyful},
		{http.MethodPost, "/approvals", governanceBodyful},
		{http.MethodPost, "/approvals/{id}/decisions", governanceBodyful},
		{http.MethodPost, "/approvals/{id}/cancel", governanceBodyless},
		{http.MethodPost, "/approvals/{id}/consume", governanceBodyful},
		{http.MethodPost, "/approvals/sweep", governanceBodyless},
		{http.MethodPost, "/breakglass", governanceBodyful},
		{http.MethodPost, "/breakglass/consume", governanceBodyful},
		{http.MethodPost, "/breakglass/{id}/revoke", governanceBodyless},
		{http.MethodPost, "/breakglass/{id}/review", governanceBodyful},
		{http.MethodPut, "/nhi/{ref}/ownership", governanceBodyful},
		{http.MethodPut, "/nhi/{ref}/policy", governanceBodyful},
		{http.MethodPost, "/nhi/{ref}/rotate", governanceBodyful},
		{http.MethodPost, "/nhi/{ref}/offboard", governanceBodyful},
		{http.MethodPost, "/nhi/{ref}/offboard/finalize", governanceBodyful},
		{http.MethodPost, "/nhi/{ref}/restore", governanceBodyless},
		{http.MethodPost, "/nhi/sweep", governanceBodyless},
		{http.MethodPost, "/agents", governanceBodyful},
		{http.MethodPost, "/killswitch", governanceBodyful},
		{http.MethodPost, "/killswitch/{id}/reenable", governanceBodyful},
		{http.MethodPost, "/killswitch/{id}/review", governanceBodyful},
		{http.MethodPost, "/guardian/rules", governanceBodyful},
		{http.MethodPut, "/guardian/rules/{id}", governanceBodyful},
		{http.MethodDelete, "/guardian/rules/{id}", governanceBodyless},
		{http.MethodPost, "/rbac/roles", governanceBodyful},
		{http.MethodPut, "/rbac/roles/{name}", governanceBodyful},
		{http.MethodDelete, "/rbac/roles/{name}", governanceBodyless},
		{http.MethodPost, "/rbac/permission-groups", governanceBodyful},
		{http.MethodPut, "/rbac/permission-groups/{name}", governanceBodyful},
		{http.MethodDelete, "/rbac/permission-groups/{name}", governanceBodyless},
		{http.MethodPost, "/rbac/grants", governanceBodyful},
		{http.MethodDelete, "/rbac/grants/{id}", governanceBodyless},
		{http.MethodPost, "/agent-risk-profiles/classify", governanceBodyful},
		{http.MethodPut, "/agent-risk-profiles/{id}/tier", governanceBodyful},
		{http.MethodPost, "/agent-risk-profiles/{id}/review", governanceBodyless},
		{http.MethodPost, "/routine-policies", governanceBodyful},
		{http.MethodPut, "/routine-policies/{id}", governanceBodyful},
		{http.MethodDelete, "/routine-policies/{id}", governanceBodyless},
		{http.MethodPost, "/agentcore-export/plan", governanceBodyful},
		{http.MethodPost, "/agentcore-export/apply", governanceBodyful},
	}

	wantCounts := map[governanceRequestBodyKind]int{
		governanceBodyful:         36,
		governanceBodyless:        14,
		governanceBodyNoDerivable: 0,
		governanceBodyPending:     0,
	}
	gotCounts := map[governanceRequestBodyKind]int{
		governanceBodyful:         0,
		governanceBodyless:        0,
		governanceBodyNoDerivable: 0,
		governanceBodyPending:     0,
	}
	seen := make(map[string]struct{}, len(tests))
	for _, tt := range tests {
		key := tt.method + " " + tt.pattern
		if _, duplicate := seen[key]; duplicate {
			t.Fatalf("duplicate mutation in test catalog: %s", key)
		}
		seen[key] = struct{}{}
		decl, ok := governanceRequestBodyDeclarationFor(moduleRoute{
			ns: "governance", method: tt.method, pattern: tt.pattern,
		})
		if !ok {
			t.Errorf("%s is not classified", key)
			continue
		}
		if decl.kind != tt.kind {
			t.Errorf("%s kind = %v, want %v", key, decl.kind, tt.kind)
		}
		gotCounts[decl.kind]++
		body, hasBody := governanceRequestBody(moduleRoute{
			ns: "governance", method: tt.method, pattern: tt.pattern,
		})
		if hasBody != (tt.kind == governanceBodyful) {
			t.Errorf("%s requestBody present = %t, want %t", key, hasBody, tt.kind == governanceBodyful)
		}
		if !hasBody && body != nil {
			t.Errorf("%s returned a body with ok=false: %#v", key, body)
		}
	}
	if len(seen) != 50 {
		t.Fatalf("classified %d mutations, want 50", len(seen))
	}
	if !reflect.DeepEqual(gotCounts, wantCounts) {
		t.Fatalf("classification counts = %#v, want %#v", gotCounts, wantCounts)
	}
}

func TestGovernanceRequestBodiesMatchHandlerDTOs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		method     string
		pattern    string
		properties []string
		required   []string
		closed     bool
	}{
		{http.MethodPost, "/agents/{agentID}/identity", []string{"allow_unknown", "identity_id", "identity_ref", "mint"}, nil, true},
		{http.MethodPost, "/policies", []string{"enabled", "kind", "name", "spec"}, []string{"kind", "name"}, true},
		{http.MethodPut, "/policies/{id}", []string{"enabled", "kind", "name", "spec"}, []string{"kind", "name"}, true},
		{http.MethodPost, "/approvals", []string{"action", "escalate_in_seconds", "expires_in_seconds", "reason", "required_approvals", "subject_kind", "subject_ref"}, []string{"action"}, true},
		{http.MethodPost, "/approvals/{id}/decisions", []string{"decision", "note"}, []string{"decision"}, true},
		{http.MethodPost, "/approvals/{id}/consume", []string{"consumer_id", "policy_version"}, []string{"consumer_id"}, true},
		{http.MethodPost, "/agents", []string{"criticality", "identity_ref", "source", "sponsor_ref"}, []string{"identity_ref", "sponsor_ref"}, false},
	}

	for _, tt := range tests {
		body, ok := governanceRequestBody(moduleRoute{
			ns: "governance", method: tt.method, pattern: tt.pattern,
		})
		if !ok {
			t.Fatalf("%s %s requestBody missing", tt.method, tt.pattern)
		}
		if body["required"] != true {
			t.Errorf("%s %s requestBody.required = %#v", tt.method, tt.pattern, body["required"])
		}
		schema := governanceSchemaFromBody(t, body)
		if schema["type"] != "object" {
			t.Errorf("%s %s schema type = %#v", tt.method, tt.pattern, schema["type"])
		}
		if schema["additionalProperties"] != !tt.closed {
			t.Errorf("%s %s additionalProperties = %#v, want %t", tt.method, tt.pattern, schema["additionalProperties"], !tt.closed)
		}
		properties := schema["properties"].(map[string]any)
		if got := governanceSortedMapKeys(properties); !reflect.DeepEqual(got, tt.properties) {
			t.Errorf("%s %s properties = %v, want %v", tt.method, tt.pattern, got, tt.properties)
		}
		gotRequired, _ := schema["required"].([]string)
		sort.Strings(gotRequired)
		if !reflect.DeepEqual(gotRequired, tt.required) {
			t.Errorf("%s %s required = %v, want %v", tt.method, tt.pattern, gotRequired, tt.required)
		}
	}
}

func TestGovernanceRequestBodyConditions(t *testing.T) {
	t.Parallel()

	bindBody, _ := governanceRequestBody(moduleRoute{
		ns: "governance", method: http.MethodPost, pattern: "/agents/{agentID}/identity",
	})
	bind := governanceSchemaFromBody(t, bindBody)
	if got := len(bind["anyOf"].([]any)); got != 3 {
		t.Fatalf("identity selector anyOf has %d branches, want 3", got)
	}

	policyBody, _ := governanceRequestBody(moduleRoute{
		ns: "governance", method: http.MethodPost, pattern: "/policies",
	})
	policy := governanceSchemaFromBody(t, policyBody)
	branches := policy["oneOf"].([]any)
	if len(branches) != 2 {
		t.Fatalf("policy kind oneOf has %d branches, want 2", len(branches))
	}
	abacBranch := branches[0].(map[string]any)
	abacProperties := abacBranch["properties"].(map[string]any)
	abacSpec := abacProperties["spec"].(map[string]any)
	rules := abacSpec["properties"].(map[string]any)["rules"].(map[string]any)
	if rules["minItems"] != 1 || rules["maxItems"] != 64 {
		t.Errorf("ABAC rule bounds = %#v", rules)
	}
	rule := rules["items"].(map[string]any)
	if got := len(rule["anyOf"].([]any)); got != 5 {
		t.Errorf("ABAC selector anyOf has %d branches, want 5", got)
	}
	if rule["additionalProperties"] != false {
		t.Errorf("ABAC rule is not closed: %#v", rule)
	}

	agentBody, _ := governanceRequestBody(moduleRoute{
		ns: "governance", method: http.MethodPost, pattern: "/agents",
	})
	agent := governanceSchemaFromBody(t, agentBody)
	if agent["additionalProperties"] != true {
		t.Fatal("agent registration must preserve the handler's unknown-field tolerance")
	}
}

func TestGovernanceRequestBodyDoesNotClaimOtherNamespaces(t *testing.T) {
	t.Parallel()

	for _, route := range []moduleRoute{
		{ns: "governance", method: http.MethodGet, pattern: "/policies"},
		{ns: "governance", method: http.MethodPost, pattern: "/unknown"},
		{ns: "claude-policy", method: http.MethodPost, pattern: "/policies"},
	} {
		if decl, ok := governanceRequestBodyDeclarationFor(route); ok {
			t.Errorf("unexpected declaration %#v for %#v", decl, route)
		}
		if body, ok := governanceRequestBody(route); ok || body != nil {
			t.Errorf("unexpected requestBody %#v for %#v", body, route)
		}
	}
}

func governanceSchemaFromBody(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	content := body["content"].(map[string]any)
	media := content["application/json"].(map[string]any)
	return media["schema"].(map[string]any)
}

func governanceSortedMapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
