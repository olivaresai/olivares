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

func TestCatalogMutationRequestBodyCensus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		method   string
		pattern  string
		kind     catalogRequestBodyKind
		fields   []string
		required []string
	}{
		{
			method: http.MethodPost, pattern: "/entries", kind: catalogBodyful,
			fields:   []string{"approved_at", "approved_by", "content_hash", "id", "kind", "name", "owner_ref", "sig_alg", "signed", "signed_by", "slug", "spec", "status", "summary", "version"},
			required: []string{"kind", "name", "slug", "version"},
		},
		{
			method: http.MethodPut, pattern: "/entries/{id}", kind: catalogBodyful,
			fields:   []string{"approved_at", "approved_by", "content_hash", "id", "kind", "name", "owner_ref", "sig_alg", "signed", "signed_by", "slug", "spec", "status", "summary", "version"},
			required: []string{"kind", "name", "slug", "version"},
		},
		{method: http.MethodDelete, pattern: "/entries/{id}", kind: catalogBodyless},
		{method: http.MethodPost, pattern: "/entries/{id}/submit", kind: catalogBodyless},
		{method: http.MethodPost, pattern: "/entries/{id}/approve", kind: catalogBodyless},
		{method: http.MethodPost, pattern: "/entries/{id}/deprecate", kind: catalogBodyless},
		{
			method: http.MethodPut, pattern: "/mcp-admission/policy", kind: catalogBodyful,
			fields: []string{"allowed_identities", "allowed_issuers", "allowed_predicates", "attested_at", "attested_by", "note", "require_signed", "require_subject_digest", "trusted_keys", "trusted_roots"},
		},
		{
			method: http.MethodPost, pattern: "/entries/{id}/admit", kind: catalogBodyful,
			fields:   []string{"bundle", "expected_digest", "note", "predicate_types"},
			required: []string{"bundle"},
		},
		{
			method: http.MethodPut, pattern: "/connector-admission/policy", kind: catalogBodyful,
			fields: []string{"allowed_identities", "allowed_issuers", "allowed_predicates", "attested_at", "attested_by", "note", "require_signed", "require_subject_digest", "trusted_keys", "trusted_roots"},
		},
		{
			method: http.MethodPost, pattern: "/entries/{id}/instantiate", kind: catalogBodyful,
			fields:   []string{"name", "note", "target_ref"},
			required: []string{"name"},
		},
		{
			method: http.MethodPost, pattern: "/instances/{id}/transition", kind: catalogBodyful,
			fields:   []string{"note", "status"},
			required: []string{"status"},
		},
	}

	counts := map[catalogRequestBodyKind]int{
		catalogBodyful:         0,
		catalogBodyless:        0,
		catalogBodyNoDerivable: 0,
		catalogBodyPending:     0,
	}
	for _, test := range tests {
		test := test
		t.Run(test.method+" "+test.pattern, func(t *testing.T) {
			t.Parallel()
			route := moduleRoute{ns: "catalog", method: test.method, pattern: test.pattern}
			decl, ok := catalogRequestBodyDeclarationFor(route)
			if !ok {
				t.Fatal("mutation is absent from Catalog request-body census")
			}
			if decl.kind != test.kind {
				t.Fatalf("kind = %v, want %v", decl.kind, test.kind)
			}
			body, found := catalogRequestBody(route)
			if test.kind != catalogBodyful {
				if found || body != nil {
					t.Fatalf("non-bodyful mutation produced requestBody %#v", body)
				}
				return
			}
			if !found {
				t.Fatal("bodyful mutation has no requestBody")
			}
			if required, _ := body["required"].(bool); !required {
				t.Fatalf("requestBody.required = %#v, want true", body["required"])
			}
			schema := catalogBodySchema(t, body)
			if schema["type"] != "object" || schema["additionalProperties"] != false {
				t.Fatalf("schema is not a closed object: %#v", schema)
			}
			properties := catalogMustMap(t, schema["properties"], "schema.properties")
			if got := catalogSortedMapKeys(properties); !reflect.DeepEqual(got, test.fields) {
				t.Fatalf("property names = %v, want %v", got, test.fields)
			}
			if got := catalogSortedStrings(schema["required"]); !reflect.DeepEqual(got, test.required) {
				t.Fatalf("required = %v, want %v", got, test.required)
			}
		})
		counts[test.kind]++
	}

	wantCounts := map[catalogRequestBodyKind]int{
		catalogBodyful:         7,
		catalogBodyless:        4,
		catalogBodyNoDerivable: 0,
		catalogBodyPending:     0,
	}
	if !reflect.DeepEqual(counts, wantCounts) {
		t.Fatalf("Catalog mutation census = %#v, want %#v", counts, wantCounts)
	}
}

func TestCatalogRequestBodyValidationShapes(t *testing.T) {
	t.Parallel()

	entry := catalogEntryRequestSchema()
	entryProperties := catalogMustMap(t, entry["properties"], "entry.properties")
	kind := catalogMustMap(t, entryProperties["kind"], "entry.kind")
	if got := catalogSortedStrings(kind["enum"]); !reflect.DeepEqual(got,
		[]string{"agent", "connector", "mcp", "model", "skill", "template"}) {
		t.Fatalf("entry kind enum = %v", got)
	}
	version := catalogMustMap(t, entryProperties["version"], "entry.version")
	if version["maxLength"] != 64 || version["pattern"] == "" {
		t.Fatalf("entry version validation = %#v", version)
	}

	policy := catalogAdmissionPolicySchema()
	allOf, ok := policy["allOf"].([]any)
	if !ok || len(allOf) != 3 {
		t.Fatalf("policy allOf = %#v, want three handler validation rules", policy["allOf"])
	}

	admit := catalogAdmitEntrySchema()
	admitProperties := catalogMustMap(t, admit["properties"], "admit.properties")
	bundle := catalogMustMap(t, admitProperties["bundle"], "admit.bundle")
	if _, invented := bundle["type"]; invented {
		t.Fatalf("raw JSON bundle was narrowed to an invented type: %#v", bundle)
	}

	transition := catalogTransitionSchema()
	transitionProperties := catalogMustMap(t, transition["properties"], "transition.properties")
	status := catalogMustMap(t, transitionProperties["status"], "transition.status")
	if got := catalogSortedStrings(status["enum"]); !reflect.DeepEqual(got,
		[]string{"active", "approved", "rejected"}) {
		t.Fatalf("transition status enum = %v", got)
	}
}

func TestCatalogRequestBodyRegistryIsScopedAndFresh(t *testing.T) {
	t.Parallel()

	known := moduleRoute{ns: "catalog", method: http.MethodPost, pattern: "/entries"}
	first, ok := catalogRequestBody(known)
	if !ok {
		t.Fatal("known Catalog request body not found")
	}
	firstProperties := catalogMustMap(t, catalogBodySchema(t, first)["properties"], "properties")
	firstProperties["not_a_real_field"] = oaObj("type", "string")
	second, ok := catalogRequestBody(known)
	if !ok {
		t.Fatal("known Catalog request body disappeared")
	}
	secondProperties := catalogMustMap(t, catalogBodySchema(t, second)["properties"], "properties")
	if _, leaked := secondProperties["not_a_real_field"]; leaked {
		t.Fatal("request schema builders share mutable property maps")
	}

	for _, route := range []moduleRoute{
		{ns: "models", method: http.MethodPost, pattern: "/entries"},
		{ns: "catalog", method: http.MethodGet, pattern: "/entries"},
		{ns: "catalog", method: http.MethodPost, pattern: "/unknown"},
	} {
		if decl, found := catalogRequestBodyDeclarationFor(route); found {
			t.Fatalf("unexpected Catalog declaration for %#v: %#v", route, decl)
		}
		if body, found := catalogRequestBody(route); found || body != nil {
			t.Fatalf("unexpected requestBody for %#v: found=%v body=%#v", route, found, body)
		}
	}
}

func catalogBodySchema(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	content := catalogMustMap(t, body["content"], "requestBody.content")
	media := catalogMustMap(t, content["application/json"], "application/json")
	return catalogMustMap(t, media["schema"], "application/json.schema")
}

func catalogMustMap(t *testing.T, value any, label string) map[string]any {
	t.Helper()
	got, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("%s = %#v, want map[string]any", label, value)
	}
	return got
}

func catalogSortedMapKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for key := range m {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func catalogSortedStrings(value any) []string {
	var out []string
	switch values := value.(type) {
	case []string:
		out = append(out, values...)
	case []any:
		out = make([]string, 0, len(values))
		for _, value := range values {
			if item, ok := value.(string); ok {
				out = append(out, item)
			}
		}
	default:
		return nil
	}
	sort.Strings(out)
	return out
}
