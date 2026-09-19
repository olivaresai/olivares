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
		{
			method: http.MethodPost, pattern: "/admission/reserve",
			fields:   []string{"actor_ref", "dims", "estimate_micro_usd", "groups", "idempotency_key", "scope", "unreachable"},
			required: []string{"idempotency_key", "scope"},
		},
		{
			// No required list on purpose: Commit and Release treat an absent or empty
			// handle as a no-op, so claiming one would publish a rejection the handler
			// does not perform.
			method: http.MethodPost, pattern: "/admission/commit",
			fields: []string{"actual_micro_usd", "handle", "spend_handle"},
		},
		{
			method: http.MethodPost, pattern: "/admission/release",
			fields: []string{"handle", "spend_handle"},
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

	for _, bodyless := range []struct{ method, pattern string }{
		{http.MethodDelete, "/budgets/{id}"},
		{http.MethodDelete, "/cost-centers/{id}"},
		{http.MethodDelete, "/cost-centers/{id}/mappings/{mid}"},
		{http.MethodDelete, "/model-rates/{id}"},
		// The reconciliation job takes its subject from the authenticated tenant;
		// handleAdmissionReconcile never reads r.Body.
		{http.MethodPost, "/admission/reconcile"},
	} {
		method, pattern := bodyless.method, bodyless.pattern
		route := moduleRoute{ns: "finops", method: method, pattern: pattern}
		decl, ok := finopsRequestBodyDeclarationFor(route)
		if !ok || decl.kind != finopsBodyless {
			t.Errorf("%s %s declaration = (%#v, %t), want bodyless", method, pattern, decl, ok)
		}
		if body, found := finopsRequestBody(route); found || body != nil {
			t.Errorf("%s %s unexpectedly declares requestBody %#v", method, pattern, body)
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

func TestFinopsEvidenceReadContracts(t *testing.T) {
	for _, pattern := range []string{"/alerts", "/budgets/{id}/status"} {
		t.Run(pattern, func(t *testing.T) {
			route := moduleRoute{ns: "finops", method: http.MethodGet, pattern: pattern, perm: "finops:budget:read"}
			op := moduleOperation(route)
			if op["x-required-permission"] != "finops:budget:read" {
				t.Fatalf("permission=%v", op["x-required-permission"])
			}
			responses := mustMap(t, op["responses"], "responses")
			for _, code := range []string{"200", "400", "401", "403", "404", "500"} {
				if responses[code] == nil {
					t.Errorf("missing %s", code)
				}
			}
			schema := mustMap(t, mustMap(t, mustMap(t, responses["200"], "200")["content"], "content")["application/json"], "media")["schema"].(map[string]any)
			props := mustMap(t, schema["properties"], "properties")
			if pattern == "/alerts" {
				if props["cursor"] == nil || props["has_more"] == nil {
					t.Fatal("pagination disappeared")
				}
				item := props["items"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)
				for _, field := range []string{"id", "legacy_value_kind", "amount_evidence"} {
					if item[field] == nil {
						t.Errorf("missing alert %s", field)
					}
				}
				evidence := item["amount_evidence"].(map[string]any)["properties"].(map[string]any)
				env := evidence["envelope"].(map[string]any)["properties"].(map[string]any)
				value := env["amount"].(map[string]any)["properties"].(map[string]any)["value_micro_usd"].(map[string]any)
				if !reflect.DeepEqual(value["type"], oaEnum("string", "null")) {
					t.Fatalf("money=%v", value)
				}
				if len(moduleRouteParameters(route)) != 4 {
					t.Fatal("filter/cursor parameters missing")
				}
			} else {
				amount := props["amount"].(map[string]any)["properties"].(map[string]any)
				for _, field := range []string{"effective_micro_usd", "remaining_micro_usd", "components", "thresholds", "over_limit", "legacy_fields", "forecast_certified"} {
					if amount[field] == nil {
						t.Errorf("missing amount %s", field)
					}
				}
				if amount["forecast_certified"].(map[string]any)["const"] != false {
					t.Fatal("forecast certified")
				}
			}
		})
	}
	if finopsEvidenceReadRoute(moduleRoute{ns: "models", method: "GET", pattern: "/alerts"}) {
		t.Fatal("namespace leak")
	}
}

// TestFinopsStatementExportPublishesCSV pins the 200 of the chargeback statement
// export to what the handler actually writes.
//
// ⛔ THE DEFECT THIS CLOSES. modules/finops/statements.go handleExportStatement sets
// `Content-Type: text/csv; charset=utf-8` and writes rows with encoding/csv on its ONLY
// success path, but the document published a JSON object for that 200. The four
// generated SDKs believed the document and tried to JSON-decode a CSV body, so a
// perfectly valid 200 was unusable from every generated client. The console and the CLI
// escaped only because neither reads the generated operation.
//
// `text/csv` is the media-type KEY; the `; charset=utf-8` parameter belongs to the
// concrete HTTP header, not to the OpenAPI content map.
func TestFinopsStatementExportPublishesCSV(t *testing.T) {
	t.Parallel()

	route := moduleRoute{ns: "finops", method: http.MethodGet, pattern: "/statements/{id}/export", perm: "finops:spend:read"}
	responses := mustMap(t, moduleOperation(route)["responses"], "responses")

	ok := mustMap(t, responses["200"], "200")
	content := mustMap(t, ok["content"], "200.content")
	if got := sortedMapKeys(content); !reflect.DeepEqual(got, []string{"text/csv"}) {
		t.Fatalf("200 content types = %v, want [text/csv] exactly", got)
	}
	schema := mustMap(t, mustMap(t, content["text/csv"], "200.content[text/csv]")["schema"], "200 schema")
	if got := schema["type"]; got != "string" {
		t.Errorf("200 schema.type = %#v, want string", got)
	}

	// The errors stay JSON: only the success body is CSV (statements.go uses
	// writeJSON/writeStoreError on every error branch).
	for _, code := range []string{"400", "401", "403", "404", "409", "429"} {
		errContent := mustMap(t, mustMap(t, responses[code], code)["content"], code+".content")
		if got := sortedMapKeys(errContent); !reflect.DeepEqual(got, []string{"application/json"}) {
			t.Errorf("%s content types = %v, want [application/json]", code, got)
		}
	}
}

// TestModuleRouteRawContentTypeIsExactNotBySuffix kills the cheap fix. Classifying by
// the trailing "export" segment would pass the test above and be wrong: this tree has
// fifteen routes whose last segment is "export" and only TWO are always CSV. The rest
// answer the JSON envelope by default and switch format only on an opt-in query
// parameter, so a suffix rule would publish CSV for every one of them and break the
// generated clients in the opposite direction.
func TestModuleRouteRawContentTypeIsExactNotBySuffix(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		ns, method, pattern string
		want                string // "" means: not raw, the JSON envelope
	}{
		// The two always-CSV FinOps exports, by exact tuple.
		{ns: "finops", method: http.MethodGet, pattern: "/spend/export", want: "text/csv"},
		{ns: "finops", method: http.MethodGet, pattern: "/statements/{id}/export", want: "text/csv"},

		// Same namespace, same resource, NOT the export: the detail read stays JSON.
		{ns: "finops", method: http.MethodGet, pattern: "/statements/{id}"},
		{ns: "finops", method: http.MethodGet, pattern: "/statements"},

		// Routes that END in "export" and are JSON by default (format is opt-in).
		{ns: "compliance", method: http.MethodGet, pattern: "/evidence/{id}/export"},
		{ns: "compliance", method: http.MethodGet, pattern: "/dora/register/{id}/export"},
		{ns: "observability", method: http.MethodGet, pattern: "/traces/{id}/export"},
		{ns: "knowledge", method: http.MethodGet, pattern: "/memory/export"},
		{ns: "security", method: http.MethodGet, pattern: "/findings/export"},
		{ns: "recording", method: http.MethodGet, pattern: "/sessions/{id}/export"},

		// The tuple is exact in all four fields: another namespace with the same
		// pattern, or another method on the same path, is not this route.
		{ns: "reporting", method: http.MethodGet, pattern: "/statements/{id}/export"},
		{ns: "finops", method: http.MethodPost, pattern: "/statements/{id}/export"},
	} {
		route := moduleRoute{ns: tc.ns, method: tc.method, pattern: tc.pattern}
		ct, raw := moduleRouteRawContentType(route)
		if tc.want == "" {
			if raw || ct != "" {
				t.Errorf("%s /v1/m/%s%s: raw=(%q,%t), want the JSON envelope", tc.method, tc.ns, tc.pattern, ct, raw)
			}
			continue
		}
		if !raw || ct != tc.want {
			t.Errorf("%s /v1/m/%s%s: raw=(%q,%t), want (%q,true)", tc.method, tc.ns, tc.pattern, ct, raw, tc.want)
		}
	}
}
