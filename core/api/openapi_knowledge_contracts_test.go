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

func TestKnowledgeRequestBodyClassifiesEveryMutation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		method  string
		pattern string
		kind    knowledgeRequestBodyKind
	}{
		{http.MethodPost, "/kbs", knowledgeBodyful},
		{http.MethodPut, "/kbs/{id}", knowledgeBodyful},
		{http.MethodDelete, "/kbs/{id}", knowledgeBodyless},
		{http.MethodPost, "/kbs/{id}/ingest", knowledgeBodyful},
		{http.MethodPost, "/kbs/{id}/reindex", knowledgeBodyless},
		{http.MethodPost, "/kbs/{id}/sync", knowledgeBodyful},
		{http.MethodPost, "/kbs/{id}/query", knowledgeBodyful},
		{http.MethodPost, "/prompts", knowledgeBodyful},
		{http.MethodPost, "/prompts/{id}/revisions", knowledgeBodyful},
		{http.MethodPost, "/prompts/{id}/rollback", knowledgeBodyful},
		{http.MethodPost, "/memory", knowledgeBodyful},
		{http.MethodPost, "/memory/import", knowledgeBodyOpaque},
		{http.MethodPost, "/memory/verify", knowledgeBodyless},
		{http.MethodDelete, "/memory/{id}", knowledgeBodyless},
		{http.MethodPost, "/memory/purge", knowledgeBodyless},
		{http.MethodPost, "/context-policies", knowledgeBodyful},
		{http.MethodPost, "/kbs/{id}/scan", knowledgeBodyless},
		{http.MethodPost, "/sources/{name}/scan", knowledgeBodyless},
		{http.MethodPut, "/dlp/rules", knowledgeBodyful},
		{http.MethodDelete, "/dlp/rules/{id}", knowledgeBodyless},
		{http.MethodPost, "/data-products", knowledgeBodyful},
		{http.MethodPut, "/data-products/{id}", knowledgeBodyful},
		{http.MethodDelete, "/data-products/{id}", knowledgeBodyless},
		{http.MethodPost, "/data-products/{id}/publish", knowledgeBodyless},
		{http.MethodPost, "/data-products/{id}/deprecate", knowledgeBodyless},
		{http.MethodPost, "/data-products/{id}/archive", knowledgeBodyless},
		{http.MethodPost, "/data-products/{id}/validate", knowledgeBodyful},
		{http.MethodPost, "/data-products/{id}/contracts", knowledgeBodyful},
	}

	wantCounts := map[knowledgeRequestBodyKind]int{
		knowledgeBodyful:     15,
		knowledgeBodyless:    12,
		knowledgeBodyOpaque:  1,
		knowledgeBodyPending: 0,
	}
	gotCounts := map[knowledgeRequestBodyKind]int{
		knowledgeBodyful:     0,
		knowledgeBodyless:    0,
		knowledgeBodyOpaque:  0,
		knowledgeBodyPending: 0,
	}
	seen := make(map[string]struct{}, len(tests))
	for _, tt := range tests {
		key := tt.method + " " + tt.pattern
		if _, duplicate := seen[key]; duplicate {
			t.Fatalf("duplicate mutation in Knowledge census: %s", key)
		}
		seen[key] = struct{}{}
		route := moduleRoute{ns: "knowledge", method: tt.method, pattern: tt.pattern}
		decl, ok := knowledgeRequestBodyDeclarationFor(route)
		if !ok {
			t.Errorf("%s is not classified", key)
			continue
		}
		if decl.kind != tt.kind {
			t.Errorf("%s kind = %v, want %v", key, decl.kind, tt.kind)
		}
		gotCounts[decl.kind]++

		body, hasBody := knowledgeRequestBody(route)
		wantBody := tt.kind == knowledgeBodyful || tt.kind == knowledgeBodyOpaque
		if hasBody != wantBody {
			t.Errorf("%s requestBody present = %t, want %t", key, hasBody, wantBody)
		}
		if !hasBody && body != nil {
			t.Errorf("%s returned a body with ok=false: %#v", key, body)
		}
	}
	if len(seen) != 28 {
		t.Fatalf("classified %d mutations, want 28", len(seen))
	}
	if !reflect.DeepEqual(gotCounts, wantCounts) {
		t.Fatalf("classification counts = %#v, want %#v", gotCounts, wantCounts)
	}
}

func TestKnowledgeRequestBodySchemasMatchHandlerDTOs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		method     string
		pattern    string
		properties []string
		required   []string
	}{
		{http.MethodPost, "/kbs", []string{"classification", "default_acl", "embed_policy", "name", "residency_region", "status"}, []string{"name"}},
		{http.MethodPut, "/kbs/{id}", []string{"classification", "default_acl", "embed_policy", "name", "residency_region", "status"}, nil},
		{http.MethodPost, "/kbs/{id}/ingest", []string{"documents", "source"}, nil},
		{http.MethodPost, "/kbs/{id}/sync", []string{"source"}, []string{"source"}},
		{http.MethodPost, "/kbs/{id}/query", []string{"agent_ref", "query", "session_ref", "top_k"}, []string{"query"}},
		{http.MethodPost, "/prompts", []string{"label", "name", "note", "template"}, []string{"name", "template"}},
		{http.MethodPost, "/prompts/{id}/revisions", []string{"label", "note", "template"}, []string{"template"}},
		{http.MethodPost, "/prompts/{id}/rollback", []string{"rev"}, []string{"rev"}},
		{http.MethodPost, "/memory", []string{"agent_ref", "classification", "content", "key", "residency_region", "session_ref", "ttl_seconds", "user_ref"}, []string{"agent_ref", "key"}},
		{http.MethodPost, "/context-policies", []string{"effect", "max_tokens", "redaction_required", "scope_kind", "scope_ref", "spec", "strategy"}, []string{"scope_kind", "scope_ref"}},
		{http.MethodPut, "/dlp/rules", []string{"action", "class", "note"}, []string{"action", "class"}},
		{http.MethodPost, "/data-products", []string{"availability_target", "description", "enforcement_mode", "freshness_sla_seconds", "kb_id", "kb_ref", "name", "owner_ref", "quality_score", "tags"}, []string{"name"}},
		{http.MethodPut, "/data-products/{id}", []string{"availability_target", "description", "enforcement_mode", "freshness_sla_seconds", "kb_id", "kb_ref", "name", "owner_ref", "quality_score", "tags"}, nil},
		{http.MethodPost, "/data-products/{id}/validate", []string{"metadata", "payload"}, nil},
		{http.MethodPost, "/data-products/{id}/contracts", []string{"completeness_threshold", "freshness_override_seconds", "note", "schema_definition", "validation_mode"}, nil},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.method+" "+tt.pattern, func(t *testing.T) {
			body, ok := knowledgeRequestBody(moduleRoute{
				ns: "knowledge", method: tt.method, pattern: tt.pattern,
			})
			if !ok {
				t.Fatal("requestBody missing")
			}
			if body["required"] != true {
				t.Fatalf("requestBody.required = %#v, want true", body["required"])
			}
			content := knowledgeMustMap(t, body["content"], "requestBody.content")
			if got := knowledgeSortedMapKeys(content); !reflect.DeepEqual(got, []string{"application/json"}) {
				t.Fatalf("requestBody media types = %v, want [application/json]", got)
			}
			schema := knowledgeBodySchema(t, body)
			if schema["type"] != "object" || schema["additionalProperties"] != false {
				t.Fatalf("root schema is not a closed object: %#v", schema)
			}
			properties := knowledgeMustMap(t, schema["properties"], "schema.properties")
			if got := knowledgeSortedMapKeys(properties); !reflect.DeepEqual(got, tt.properties) {
				t.Errorf("properties = %v, want %v", got, tt.properties)
			}
			if got := knowledgeSortedStrings(schema["required"]); !reflect.DeepEqual(got, tt.required) {
				t.Errorf("required = %v, want %v", got, tt.required)
			}
		})
	}
}

func TestKnowledgeRequestBodyConditionalShapes(t *testing.T) {
	t.Parallel()

	ingest := knowledgeSchemaFor(t, http.MethodPost, "/kbs/{id}/ingest")
	if branches := knowledgeMustSlice(t, ingest["anyOf"], "ingest.anyOf"); len(branches) != 2 {
		t.Fatalf("ingest.anyOf has %d branches, want 2", len(branches))
	}
	documents := knowledgeMustMap(t, knowledgeProperties(t, ingest)["documents"], "documents")
	if documents["maxItems"] != 200 {
		t.Fatalf("documents.maxItems = %#v, want 200", documents["maxItems"])
	}
	document := knowledgeMustMap(t, documents["items"], "documents.items")
	if document["additionalProperties"] != false {
		t.Fatalf("inline document must reject unknown fields: %#v", document)
	}
	if got := knowledgeSortedStrings(document["required"]); !reflect.DeepEqual(got, []string{"source_doc_id"}) {
		t.Fatalf("inline document required = %v, want [source_doc_id]", got)
	}

	query := knowledgeSchemaFor(t, http.MethodPost, "/kbs/{id}/query")
	topK := knowledgeMustMap(t, knowledgeProperties(t, query)["top_k"], "top_k")
	if _, overclaimed := topK["minimum"]; overclaimed {
		t.Fatalf("top_k incorrectly rejects values the handler defaults: %#v", topK)
	}
	if _, overclaimed := topK["maximum"]; overclaimed {
		t.Fatalf("top_k incorrectly rejects values the handler clamps: %#v", topK)
	}

	memory := knowledgeSchemaFor(t, http.MethodPost, "/memory")
	ttl := knowledgeMustMap(t, knowledgeProperties(t, memory)["ttl_seconds"], "ttl_seconds")
	if _, overclaimed := ttl["minimum"]; overclaimed {
		t.Fatalf("ttl_seconds incorrectly rejects non-positive no-expiry values: %#v", ttl)
	}

	validate := knowledgeSchemaFor(t, http.MethodPost, "/data-products/{id}/validate")
	if branches := knowledgeMustSlice(t, validate["anyOf"], "validate.anyOf"); len(branches) != 2 {
		t.Fatalf("validate.anyOf has %d branches, want 2", len(branches))
	}
}

func TestKnowledgeRequestBodyPreservesDecoderBoundaries(t *testing.T) {
	t.Parallel()

	contextPolicy := knowledgeSchemaFor(t, http.MethodPost, "/context-policies")
	spec := knowledgeObjectBranch(t, knowledgeProperties(t, contextPolicy)["spec"], "spec")
	if spec["additionalProperties"] != true {
		t.Fatalf("context policy spec must preserve arbitrary map keys: %#v", spec)
	}

	product := knowledgeSchemaFor(t, http.MethodPost, "/data-products")
	tags := knowledgeObjectBranch(t, knowledgeProperties(t, product)["tags"], "tags")
	if tags["additionalProperties"] != true {
		t.Fatalf("data-product tags must preserve arbitrary map keys: %#v", tags)
	}

	validate := knowledgeSchemaFor(t, http.MethodPost, "/data-products/{id}/validate")
	validateProperties := knowledgeProperties(t, validate)
	metadata := knowledgeObjectBranch(t, validateProperties["metadata"], "metadata")
	if metadata["additionalProperties"] != true {
		t.Fatalf("validation metadata must preserve arbitrary map keys: %#v", metadata)
	}
	payload := knowledgeMustMap(t, validateProperties["payload"], "payload")
	if _, narrowed := payload["type"]; narrowed {
		t.Fatalf("raw payload must accept any JSON value: %#v", payload)
	}

	contract := knowledgeSchemaFor(t, http.MethodPost, "/data-products/{id}/contracts")
	definition := knowledgeObjectBranch(t, knowledgeProperties(t, contract)["schema_definition"], "schema_definition")
	if definition["additionalProperties"] != true {
		t.Fatalf("schema_definition must preserve arbitrary JSON Schema keywords: %#v", definition)
	}
}

func TestKnowledgeMemoryImportPublishesOpaqueNDJSON(t *testing.T) {
	t.Parallel()

	route := moduleRoute{
		ns: "knowledge", method: http.MethodPost, pattern: "/memory/import",
	}
	decl, ok := knowledgeRequestBodyDeclarationFor(moduleRoute{
		ns: "knowledge", method: http.MethodPost, pattern: "/memory/import",
	})
	if !ok || decl.kind != knowledgeBodyOpaque {
		t.Fatalf("memory import declaration = %#v, %t", decl, ok)
	}
	if decl.mediaType != "application/x-ndjson" {
		t.Fatalf("memory import media type = %q, want application/x-ndjson", decl.mediaType)
	}

	body, ok := knowledgeRequestBody(route)
	if !ok {
		t.Fatal("opaque memory import requestBody missing")
	}
	if body["required"] != true {
		t.Fatalf("memory import requestBody.required = %#v, want true", body["required"])
	}
	content := knowledgeMustMap(t, body["content"], "requestBody.content")
	if got := knowledgeSortedMapKeys(content); !reflect.DeepEqual(got, []string{"application/x-ndjson"}) {
		t.Fatalf("memory import media types = %v, want [application/x-ndjson]", got)
	}
	media := knowledgeMustMap(t, content["application/x-ndjson"], "application/x-ndjson")
	schema := knowledgeMustMap(t, media["schema"], "application/x-ndjson.schema")
	wantSchema := oaObj(
		"type", "string",
		"description", "NDJSON memory-portability bundle: the first line is the signed manifest; each remaining line is one memory row.",
	)
	if !reflect.DeepEqual(schema, wantSchema) {
		t.Fatalf("memory import schema = %#v, want %#v", schema, wantSchema)
	}

	// The schema builder must remain fresh just like the JSON DTO builders.
	schema["properties"] = oaObj("invented", oaObj("type", "string"))
	second, ok := knowledgeRequestBody(route)
	if !ok {
		t.Fatal("opaque memory import requestBody disappeared")
	}
	secondContent := knowledgeMustMap(t, second["content"], "second requestBody.content")
	secondMedia := knowledgeMustMap(t, secondContent["application/x-ndjson"], "second application/x-ndjson")
	secondSchema := knowledgeMustMap(t, secondMedia["schema"], "second application/x-ndjson.schema")
	if _, leaked := secondSchema["properties"]; leaked {
		t.Fatalf("opaque memory import schema leaked invented line properties: %#v", secondSchema)
	}
}

func TestKnowledgeRequestBodyRegistryIsScopedAndFresh(t *testing.T) {
	t.Parallel()

	known := moduleRoute{ns: "knowledge", method: http.MethodPost, pattern: "/memory"}
	first, ok := knowledgeRequestBody(known)
	if !ok {
		t.Fatal("known Knowledge requestBody missing")
	}
	firstProperties := knowledgeProperties(t, knowledgeBodySchema(t, first))
	firstProperties["not_a_real_field"] = oaObj("type", "string")
	second, ok := knowledgeRequestBody(known)
	if !ok {
		t.Fatal("known Knowledge requestBody disappeared")
	}
	if _, leaked := knowledgeProperties(t, knowledgeBodySchema(t, second))["not_a_real_field"]; leaked {
		t.Fatal("request schema builders share mutable property maps")
	}

	for _, route := range []moduleRoute{
		{ns: "knowledge", method: http.MethodGet, pattern: "/memory"},
		{ns: "knowledge", method: http.MethodPost, pattern: "/unknown"},
		{ns: "models", method: http.MethodPost, pattern: "/memory"},
	} {
		if decl, ok := knowledgeRequestBodyDeclarationFor(route); ok {
			t.Errorf("unexpected declaration %#v for %#v", decl, route)
		}
		if body, ok := knowledgeRequestBody(route); ok || body != nil {
			t.Errorf("unexpected requestBody %#v for %#v", body, route)
		}
	}
}

func knowledgeSchemaFor(t *testing.T, method, pattern string) map[string]any {
	t.Helper()
	body, ok := knowledgeRequestBody(moduleRoute{ns: "knowledge", method: method, pattern: pattern})
	if !ok {
		t.Fatalf("%s %s requestBody missing", method, pattern)
	}
	return knowledgeBodySchema(t, body)
}

func knowledgeBodySchema(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	content := knowledgeMustMap(t, body["content"], "requestBody.content")
	media := knowledgeMustMap(t, content["application/json"], "application/json")
	return knowledgeMustMap(t, media["schema"], "application/json.schema")
}

func knowledgeProperties(t *testing.T, schema map[string]any) map[string]any {
	t.Helper()
	return knowledgeMustMap(t, schema["properties"], "schema.properties")
}

func knowledgeObjectBranch(t *testing.T, value any, name string) map[string]any {
	t.Helper()
	schema := knowledgeMustMap(t, value, name)
	branches := knowledgeMustSlice(t, schema["anyOf"], name+".anyOf")
	for _, branch := range branches {
		candidate := knowledgeMustMap(t, branch, name+".anyOf branch")
		if candidate["type"] == "object" {
			return candidate
		}
	}
	t.Fatalf("%s has no object branch: %#v", name, schema)
	return nil
}

func knowledgeMustMap(t *testing.T, value any, name string) map[string]any {
	t.Helper()
	got, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("%s = %#v, want map[string]any", name, value)
	}
	return got
}

func knowledgeMustSlice(t *testing.T, value any, name string) []any {
	t.Helper()
	got, ok := value.([]any)
	if !ok {
		t.Fatalf("%s = %#v, want []any", name, value)
	}
	return got
}

func knowledgeSortedMapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func knowledgeSortedStrings(value any) []string {
	var out []string
	switch values := value.(type) {
	case nil:
		return nil
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
