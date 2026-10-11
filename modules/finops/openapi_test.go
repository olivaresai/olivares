// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"net/http"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/api/oas"
	"github.com/olivaresai/olivares/modules/internal/oastest"
)

func TestFinopsRequestBodyContracts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		method   string
		pattern  string
		fields   []string
		required []string
		// dims, when set, is the property set of the nested `dims` document.
		dims []string
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
			// The names finops.SpendDims carries on the wire (modules/finops/budgets_wire_test.go).
			dims: []string{"agent_group_refs", "agent_ref", "api_key_ref", "context_window", "cost_center_ref", "cost_type",
				"gateway", "identity_ref", "inference_geo", "model_ref", "project", "provider_ref", "routine_ref",
				"service_tier", "session_ref", "team", "user_group_refs", "workspace_ref"},
		},
		{
			// No required list: Commit and Release answer an absent or empty handle as a
			// no-op, so a required handle would publish a refusal the handler never makes.
			method: http.MethodPost, pattern: "/admission/commit",
			fields: []string{"actual_micro_usd", "handle"},
		},
		{
			method: http.MethodPost, pattern: "/admission/release",
			fields: []string{"handle"},
		},
	}

	if got, want := len(finopsOpenAPIContracts), len(tests); got != want {
		t.Fatalf("FinOps contract count = %d, want %d", got, want)
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.method+" "+tt.pattern, func(t *testing.T) {
			route := operation{method: tt.method, pattern: tt.pattern}
			body, ok := finopsRequestBody(route.method, route.pattern)
			if !ok {
				t.Fatal("requestBody contract not found")
			}
			if required, _ := body["required"].(bool); !required {
				t.Fatalf("requestBody.required = %#v, want true", body["required"])
			}
			content := oastest.Map(t, body["content"], "requestBody.content")
			if got := oastest.Keys(content); !reflect.DeepEqual(got, []string{"application/json"}) {
				t.Fatalf("requestBody content types = %v, want [application/json]", got)
			}
			media := oastest.Map(t, content["application/json"], "application/json")
			schema := oastest.Map(t, media["schema"], "application/json.schema")
			if got := schema["type"]; got != "object" {
				t.Fatalf("schema.type = %#v, want object", got)
			}
			if got := schema["additionalProperties"]; got != false {
				t.Fatalf("schema.additionalProperties = %#v, want false", got)
			}
			properties := oastest.Map(t, schema["properties"], "schema.properties")
			if got := oastest.Keys(properties); !reflect.DeepEqual(got, tt.fields) {
				t.Fatalf("property names = %v, want %v", got, tt.fields)
			}
			if got := sortedStrings(schema["required"]); !reflect.DeepEqual(got, tt.required) {
				t.Fatalf("required = %v, want %v", got, tt.required)
			}
			if tt.dims != nil {
				dims := oastest.Map(t, oastest.Map(t, properties["dims"], "schema.properties.dims")["properties"], "dims.properties")
				if got := oastest.Keys(dims); !reflect.DeepEqual(got, tt.dims) {
					t.Fatalf("dims property names = %v, want %v", got, tt.dims)
				}
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
		body, ok := finopsRequestBody(http.MethodPost, tt.pattern)
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

	known := operation{method: http.MethodPost, pattern: "/budgets"}
	first, ok := finopsRequestBody(known.method, known.pattern)
	if !ok {
		t.Fatal("known FinOps request body not found")
	}
	firstSchema := finopsBodySchema(t, first)
	firstProperties := oastest.Map(t, firstSchema["properties"], "schema.properties")
	firstProperties["not_a_real_field"] = oas.Obj("type", "string")

	second, ok := finopsRequestBody(known.method, known.pattern)
	if !ok {
		t.Fatal("known FinOps request body disappeared")
	}
	secondProperties := oastest.Map(t, finopsBodySchema(t, second)["properties"], "schema.properties")
	if _, leaked := secondProperties["not_a_real_field"]; leaked {
		t.Fatal("request schema builders share mutable property maps")
	}

	for _, route := range []operation{
		{method: http.MethodGet, pattern: "/budgets"},
		{method: http.MethodPost, pattern: "/unknown"},
	} {
		if body, found := finopsRequestBody(route.method, route.pattern); found || body != nil {
			t.Fatalf("unexpected request body for %#v: found=%v body=%#v", route, found, body)
		}
	}
}

func TestFinopsBodylessMutationsStayBodyless(t *testing.T) {
	t.Parallel()

	for _, route := range []operation{
		{method: http.MethodDelete, pattern: "/budgets/{id}"},
		{method: http.MethodDelete, pattern: "/cost-centers/{id}"},
		{method: http.MethodDelete, pattern: "/cost-centers/{id}/mappings/{mid}"},
		{method: http.MethodDelete, pattern: "/model-rates/{id}"},
		// The reconciliation job takes its subject from the authenticated tenant.
		{method: http.MethodPost, pattern: "/admission/reconcile"},
	} {
		decl, ok := finopsRequestBodyDeclarationFor(route.method, route.pattern)
		if !ok || decl.kind != finopsBodyless {
			t.Errorf("%s %s declaration = (%#v, %t), want bodyless", route.method, route.pattern, decl, ok)
		}
		if body, found := finopsRequestBody(route.method, route.pattern); found || body != nil {
			t.Errorf("%s %s unexpectedly declares requestBody %#v", route.method, route.pattern, body)
		}
	}
}

// TestContractsPublishOneHandle: an admission hold is settled by the one handle Reserve
// answers with, and the published documents say so. Commit takes the handle and the
// measured amount, release the handle alone, and no FinOps request document names a hold
// anywhere else.
func TestContractsPublishOneHandle(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		pattern string
		fields  []string
	}{
		{pattern: "/admission/commit", fields: []string{"actual_micro_usd", "handle"}},
		{pattern: "/admission/release", fields: []string{"handle"}},
	} {
		body, ok := finopsRequestBody(http.MethodPost, tt.pattern)
		if !ok {
			t.Errorf("POST %s publishes no request document", tt.pattern)
			continue
		}
		properties := oastest.Map(t, finopsBodySchema(t, body)["properties"], "schema.properties")
		if got := oastest.Keys(properties); !reflect.DeepEqual(got, tt.fields) {
			t.Errorf("POST %s publishes %v, want %v", tt.pattern, got, tt.fields)
		}
		if handle, _ := properties["handle"].(map[string]any); handle["type"] != "string" {
			t.Errorf("POST %s handle = %#v, want a string", tt.pattern, properties["handle"])
		}
	}

	for key, contract := range finopsOpenAPIContracts {
		for _, name := range holdNames(contract.schema(), "") {
			if name != "handle" || (key != http.MethodPost+" /admission/commit" && key != http.MethodPost+" /admission/release") {
				t.Errorf("%s publishes a hold under %q: the wire carries one handle, on commit and release only", key, name)
			}
		}
	}
}

// holdNames lists, with its path from the document's root, every property of schema whose
// name ends in "handle", through nested objects, arrays and composed branches.
func holdNames(schema map[string]any, at string) []string {
	var out []string
	properties, _ := schema["properties"].(map[string]any)
	for name, property := range properties {
		path := name
		if at != "" {
			path = at + "." + name
		}
		if strings.HasSuffix(name, "handle") {
			out = append(out, path)
		}
		if nested, ok := property.(map[string]any); ok {
			out = append(out, holdNames(nested, path)...)
		}
	}
	if items, ok := schema["items"].(map[string]any); ok {
		out = append(out, holdNames(items, at+"[]")...)
	}
	for _, keyword := range []string{"allOf", "anyOf", "oneOf"} {
		branches, _ := schema[keyword].([]any)
		for _, branch := range branches {
			if nested, ok := branch.(map[string]any); ok {
				out = append(out, holdNames(nested, at)...)
			}
		}
	}
	return out
}

func finopsBodySchema(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	content := oastest.Map(t, body["content"], "requestBody.content")
	media := oastest.Map(t, content["application/json"], "application/json")
	return oastest.Map(t, media["schema"], "application/json.schema")
}

func TestFinopsEvidenceReadContracts(t *testing.T) {
	for _, pattern := range []string{"/alerts", "/budgets/{id}/status"} {
		t.Run(pattern, func(t *testing.T) {
			op := oastest.Op(t, New(), "get", "/v1/m/finops"+pattern)
			if op["x-required-permission"] != "finops:budget:read" {
				t.Fatalf("permission=%v", op["x-required-permission"])
			}
			responses := oastest.Map(t, op["responses"], "responses")
			for _, code := range []string{"200", "400", "401", "403", "404", "500"} {
				if responses[code] == nil {
					t.Errorf("missing %s", code)
				}
			}
			schema := oastest.Map(t, oastest.Map(t, oastest.Map(t, responses["200"], "200")["content"], "content")["application/json"], "media")["schema"].(map[string]any)
			props := oastest.Map(t, schema["properties"], "properties")
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
				if !reflect.DeepEqual(value["type"], oas.Enum("string", "null")) {
					t.Fatalf("money=%v", value)
				}
				for _, name := range []string{"alert_id", "budget_id", "limit", "cursor"} {
					found := false
					for _, raw := range op["parameters"].([]any) {
						if p := raw.(map[string]any); p["name"] == name && p["in"] == "query" {
							found = true
						}
					}
					if !found {
						t.Errorf("filter/cursor parameter %s missing", name)
					}
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
	if finopsEvidenceReadRoute(http.MethodPost, "/alerts") || finopsEvidenceReadRoute(http.MethodGet, "/alerts/{id}") {
		t.Fatal("evidence contract leaked to another route")
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

	responses := oastest.Map(t, oastest.Op(t, New(), "get", "/v1/m/finops/statements/{id}/export")["responses"], "responses")

	ok := oastest.Map(t, responses["200"], "200")
	content := oastest.Map(t, ok["content"], "200.content")
	if got := oastest.Keys(content); !reflect.DeepEqual(got, []string{"text/csv"}) {
		t.Fatalf("200 content types = %v, want [text/csv] exactly", got)
	}
	schema := oastest.Map(t, oastest.Map(t, content["text/csv"], "200.content[text/csv]")["schema"], "200 schema")
	if got := schema["type"]; got != "string" {
		t.Errorf("200 schema.type = %#v, want string", got)
	}

	// The errors stay JSON: only the success body is CSV (statements.go uses
	// writeJSON/writeStoreError on every error branch).
	for _, code := range []string{"400", "401", "403", "404", "409", "429"} {
		errContent := oastest.Map(t, oastest.Map(t, responses[code], code)["content"], code+".content")
		if got := oastest.Keys(errContent); !reflect.DeepEqual(got, []string{"application/json"}) {
			t.Errorf("%s content types = %v, want [application/json]", code, got)
		}
	}
}

// TestModuleRouteRawContentTypeIsExactNotBySuffix kills the cheap fix. Classifying by
// the trailing "export" segment would pass the test above and be wrong: the tree has
// fifteen routes whose last segment is "export" and only TWO are always CSV. The rest
// answer the JSON envelope by default and switch format only on an opt-in query
// parameter, so a suffix rule would publish CSV for every one of them and break the
// generated clients in the opposite direction. The core's own rule never looks at
// "export" (core/api TestModuleRawContentTypeFollowsOnlyTheStreamSuffix).
func TestModuleRouteRawContentTypeIsExactNotBySuffix(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		method, pattern string
		csv             bool
	}{
		// The two always-CSV FinOps exports, by exact tuple.
		{method: http.MethodGet, pattern: "/spend/export", csv: true},
		{method: http.MethodGet, pattern: "/statements/{id}/export", csv: true},

		// Same resource, NOT the export: the detail read stays JSON.
		{method: http.MethodGet, pattern: "/statements/{id}"},
		{method: http.MethodGet, pattern: "/statements"},

		// The tuple is exact: another method on the same path, or another path
		// that ends in "export", is not this route.
		{method: http.MethodPost, pattern: "/statements/{id}/export"},
		{method: http.MethodGet, pattern: "/budgets/{id}/export"},
	} {
		if got := finopsCSVExport(tc.method, tc.pattern); got != tc.csv {
			t.Errorf("%s /v1/m/finops%s: csv=%t, want %t", tc.method, tc.pattern, got, tc.csv)
		}
	}
	content := oastest.Map(t, oastest.Map(t, oastest.Op(t, New(), "get", "/v1/m/finops/statements/{id}")["responses"], "responses")["200"], "200")["content"]
	if got := oastest.Keys(oastest.Map(t, content, "200.content")); !reflect.DeepEqual(got, []string{"application/json"}) {
		t.Errorf("GET /v1/m/finops/statements/{id} 200 content types = %v, want [application/json]", got)
	}
}

// TestOperationDocumentationDispositionSentinels pins the request-body kind
// this module publishes for representative mutations in the beta document.
func TestOperationDocumentationDispositionSentinels(t *testing.T) {
	t.Parallel()
	module := New()
	for _, test := range []struct{ name, method, pattern, want string }{
		{"delete", "delete", "/budgets/{id}", "bodyless"},
	} {
		op := oastest.Op(t, module, test.method, "/v1/m/finops"+test.pattern)
		if got := op["x-olivares-request-body-disposition"]; got != test.want {
			t.Errorf("%s: disposition = %v, want %s", test.name, got, test.want)
		}
	}
}

// operation is one route of this module: method and module-relative pattern.
type operation struct{ method, pattern string }

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
