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

func TestClaudeAgentsRequestBodiesClassifyAllMutations(t *testing.T) {
	t.Parallel()

	route := moduleRoute{
		ns: "claude-agents", method: http.MethodPost,
		pattern: "/sessions/{id}/tool-confirmation",
	}
	decl, ok := claudeAgentsRequestBodyDeclarationFor(route)
	if !ok || decl.kind != claudeAgentsBodyful {
		t.Fatalf("classification = (%#v, %t), want bodyful", decl, ok)
	}
	body, ok := claudeAgentsRequestBody(route)
	if !ok || body["required"] != true {
		t.Fatalf("requestBody = (%#v, %t), want required body", body, ok)
	}
}

func TestClaudeAgentsRequestBodyMatchesHandlerDTO(t *testing.T) {
	t.Parallel()

	body, ok := claudeAgentsRequestBody(moduleRoute{
		ns: "claude-agents", method: http.MethodPost,
		pattern: "/sessions/{id}/tool-confirmation",
	})
	if !ok {
		t.Fatal("tool-confirmation requestBody missing")
	}
	content := body["content"].(map[string]any)
	schema := content["application/json"].(map[string]any)["schema"].(map[string]any)
	if schema["type"] != "object" || schema["additionalProperties"] != false {
		t.Fatalf("schema is not a strict object: %#v", schema)
	}
	properties := schema["properties"].(map[string]any)
	gotProperties := make([]string, 0, len(properties))
	for name := range properties {
		gotProperties = append(gotProperties, name)
	}
	sort.Strings(gotProperties)
	wantProperties := []string{"deny_message", "result", "tool_use_id"}
	if !reflect.DeepEqual(gotProperties, wantProperties) {
		t.Fatalf("properties = %v, want %v", gotProperties, wantProperties)
	}
	if got := schema["required"]; !reflect.DeepEqual(got, []string{"tool_use_id", "result"}) {
		t.Fatalf("required = %#v", got)
	}
}

func TestClaudeAgentsRequestBodyDoesNotClaimOtherRoutes(t *testing.T) {
	t.Parallel()

	for _, route := range []moduleRoute{
		{ns: "claude-agents", method: http.MethodGet, pattern: "/sessions/{id}/events"},
		{ns: "claude-agents", method: http.MethodPost, pattern: "/unknown"},
		{ns: "governance", method: http.MethodPost, pattern: "/sessions/{id}/tool-confirmation"},
	} {
		if body, ok := claudeAgentsRequestBody(route); ok || body != nil {
			t.Errorf("claimed unexpected route %#v: %#v", route, body)
		}
	}
}
