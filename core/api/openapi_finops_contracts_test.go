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

func TestFinopsRequestBodyContracts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		method   string
		pattern  string
		fields   []string
		required []string
	}{
		{
			method: http.MethodPost, pattern: "/budgets",
			fields:   []string{"action", "currency", "dimension", "enabled", "fail_closed", "id", "key", "limit_micro_usd", "name", "period", "reserved_micro_usd", "thresholds"},
			required: []string{"limit_micro_usd", "name"},
		},
		{
			method: http.MethodPut, pattern: "/budgets/{id}",
			fields:   []string{"action", "currency", "dimension", "enabled", "fail_closed", "id", "key", "limit_micro_usd", "name", "period", "reserved_micro_usd", "thresholds"},
			required: []string{"limit_micro_usd", "name"},
		},
		{
			method: http.MethodPost, pattern: "/cost",
			fields: []string{"actor", "api_key_ref", "cache_creation_1h_tokens", "cache_creation_5m_tokens", "cache_read_tokens", "context_window", "cost_micro_usd", "cost_type", "gateway", "inference_geo", "input_tokens", "labels", "model_ref", "occurred_at", "output_tokens", "provenance", "provider_ref", "service_tier", "session_ref", "workspace_ref"},
		},
		{
			method: http.MethodPost, pattern: "/cost-centers",
			fields:   []string{"code", "created_at", "description", "id", "metadata", "name", "owner", "status", "updated_at"},
			required: []string{"code", "name"},
		},
		{
			method: http.MethodPut, pattern: "/cost-centers/{id}",
			fields:   []string{"code", "created_at", "description", "id", "metadata", "name", "owner", "status", "updated_at"},
			required: []string{"code", "name"},
		},
		{
			method: http.MethodPost, pattern: "/cost-centers/{id}/mappings",
			fields:   []string{"cost_center_id", "created_at", "id", "priority", "source_dimension", "source_key", "updated_at"},
			required: []string{"source_dimension", "source_key"},
		},
		{
			method: http.MethodPost, pattern: "/model-rates",
			fields:   []string{"cache_creation_rate_micro_usd", "cache_read_rate_micro_usd", "created_at", "effective_from", "effective_until", "id", "input_rate_micro_usd", "model", "notes", "output_rate_micro_usd", "provider", "updated_at"},
			required: []string{"effective_from", "input_rate_micro_usd", "model", "output_rate_micro_usd", "provider"},
		},
		{
			method: http.MethodPut, pattern: "/model-rates/{id}",
			fields:   []string{"cache_creation_rate_micro_usd", "cache_read_rate_micro_usd", "created_at", "effective_from", "effective_until", "id", "input_rate_micro_usd", "model", "notes", "output_rate_micro_usd", "provider", "updated_at"},
			required: []string{"effective_from", "input_rate_micro_usd", "model", "output_rate_micro_usd", "provider"},
		},
		{
			method: http.MethodPost, pattern: "/outcomes",
			fields:   []string{"occurred_at", "outcome_ref", "source", "subject_kind", "subject_ref", "value_micro_usd", "verdict"},
			required: []string{"subject_kind", "subject_ref", "verdict"},
		},
		{
			method: http.MethodPost, pattern: "/seats",
			fields:   []string{"assigned_seats", "day", "pending_invites", "premium_seats", "provider"},
			required: []string{"day", "provider"},
		},
		{
			method: http.MethodPost, pattern: "/statements/generate",
			fields:   []string{"period", "period_start"},
			required: []string{"period", "period_start"},
		},
	}

	if got, want := len(finopsOpenAPIContracts), len(tests); got != want {
		t.Fatalf("FinOps contract count = %d, want %d", got, want)
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.method+" "+tt.pattern, func(t *testing.T) {
			route := moduleRoute{ns: "finops", method: tt.method, pattern: tt.pattern}
			body, ok := finopsRequestBody(route)
			if !ok {
				t.Fatal("requestBody contract not found")
			}
			if required, _ := body["required"].(bool); !required {
				t.Fatalf("requestBody.required = %#v, want true", body["required"])
			}
			content := mustMap(t, body["content"], "requestBody.content")
			if got := sortedMapKeys(content); !reflect.DeepEqual(got, []string{"application/json"}) {
				t.Fatalf("requestBody content types = %v, want [application/json]", got)
			}
			media := mustMap(t, content["application/json"], "application/json")
			schema := mustMap(t, media["schema"], "application/json.schema")
			if got := schema["type"]; got != "object" {
				t.Fatalf("schema.type = %#v, want object", got)
			}
			if got := schema["additionalProperties"]; got != false {
				t.Fatalf("schema.additionalProperties = %#v, want false", got)
			}
			properties := mustMap(t, schema["properties"], "schema.properties")
			if got := sortedMapKeys(properties); !reflect.DeepEqual(got, tt.fields) {
				t.Fatalf("property names = %v, want %v", got, tt.fields)
			}
			if got := sortedStrings(schema["required"]); !reflect.DeepEqual(got, tt.required) {
				t.Fatalf("required = %v, want %v", got, tt.required)
			}
		})
	}
}

func TestFinopsRequestBodyConditionalValidationShape(t *testing.T) {
	t.Parallel()

	tests := []struct {
		pattern string
		keyword string
		count   int
	}{
		{pattern: "/cost", keyword: "anyOf", count: 2},
		{pattern: "/budgets", keyword: "allOf", count: 1},
		{pattern: "/outcomes", keyword: "anyOf", count: 2},
	}
	for _, tt := range tests {
		body, ok := finopsRequestBody(moduleRoute{ns: "finops", method: http.MethodPost, pattern: tt.pattern})
		if !ok {
			t.Fatalf("POST %s contract not found", tt.pattern)
		}
		schema := finopsBodySchema(t, body)
		branches, ok := schema[tt.keyword].([]any)
		if !ok || len(branches) != tt.count {
			t.Fatalf("POST %s %s = %#v, want %d branches", tt.pattern, tt.keyword, schema[tt.keyword], tt.count)
		}
	}
}

func TestFinopsRequestBodyRegistryIsScopedAndFresh(t *testing.T) {
	t.Parallel()

	known := moduleRoute{ns: "finops", method: http.MethodPost, pattern: "/budgets"}
	first, ok := finopsRequestBody(known)
	if !ok {
		t.Fatal("known FinOps request body not found")
	}
	firstSchema := finopsBodySchema(t, first)
	firstProperties := mustMap(t, firstSchema["properties"], "schema.properties")
	firstProperties["not_a_real_field"] = oaObj("type", "string")

	second, ok := finopsRequestBody(known)
	if !ok {
		t.Fatal("known FinOps request body disappeared")
	}
	secondProperties := mustMap(t, finopsBodySchema(t, second)["properties"], "schema.properties")
	if _, leaked := secondProperties["not_a_real_field"]; leaked {
		t.Fatal("request schema builders share mutable property maps")
	}

	for _, route := range []moduleRoute{
		{ns: "models", method: http.MethodPost, pattern: "/budgets"},
		{ns: "finops", method: http.MethodGet, pattern: "/budgets"},
		{ns: "finops", method: http.MethodPost, pattern: "/unknown"},
	} {
		if body, found := finopsRequestBody(route); found || body != nil {
			t.Fatalf("unexpected request body for %#v: found=%v body=%#v", route, found, body)
		}
	}
}

func TestFinopsBodylessMutationsStayBodyless(t *testing.T) {
	t.Parallel()

	for _, pattern := range []string{
		"/budgets/{id}",
		"/cost-centers/{id}",
		"/cost-centers/{id}/mappings/{mid}",
		"/model-rates/{id}",
	} {
		route := moduleRoute{ns: "finops", method: http.MethodDelete, pattern: pattern}
		decl, ok := finopsRequestBodyDeclarationFor(route)
		if !ok || decl.kind != finopsBodyless {
			t.Errorf("DELETE %s declaration = (%#v, %t), want bodyless", pattern, decl, ok)
		}
		if body, found := finopsRequestBody(route); found || body != nil {
			t.Errorf("DELETE %s unexpectedly declares requestBody %#v", pattern, body)
		}
	}
}

func finopsBodySchema(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	content := mustMap(t, body["content"], "requestBody.content")
	media := mustMap(t, content["application/json"], "application/json")
	return mustMap(t, media["schema"], "application/json.schema")
}

func mustMap(t *testing.T, value any, label string) map[string]any {
	t.Helper()
	got, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("%s = %#v, want map[string]any", label, value)
	}
	return got
}

func sortedMapKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for key := range m {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func sortedStrings(value any) []string {
	values, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if item, ok := value.(string); ok {
			out = append(out, item)
		}
	}
	sort.Strings(out)
	return out
}
