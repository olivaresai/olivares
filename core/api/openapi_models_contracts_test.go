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

func TestModelsRequestBodyClassifiesEveryMutation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		method  string
		pattern string
		kind    modelsRequestBodyKind
	}{
		{http.MethodPost, "/routing-policies", modelsBodyful},
		{http.MethodPut, "/routing-policies/{id}", modelsBodyful},
		{http.MethodDelete, "/routing-policies/{id}", modelsDeleteBodyless},
		{http.MethodPost, "/routing-policies/{id}/resolve", modelsPostBodyless},
		{http.MethodPost, "/routing-policies/{id}/execute", modelsBodyful},
		{http.MethodPost, "/keys", modelsBodyful},
		{http.MethodPut, "/keys/{id}", modelsBodyful},
		{http.MethodDelete, "/keys/{id}", modelsDeleteBodyless},
		{http.MethodPut, "/workspace-residency", modelsBodyful},
		{http.MethodPut, "/access-tier-entitlements", modelsBodyful},
		{http.MethodPost, "/owned-models", modelsBodyful},
		{http.MethodPut, "/owned-models/{id}", modelsBodyful},
		{http.MethodDelete, "/owned-models/{id}", modelsDeleteBodyless},
		{http.MethodPost, "/model-versions", modelsBodyful},
		{http.MethodDelete, "/model-versions/{id}", modelsDeleteBodyless},
		{http.MethodPost, "/inference-deployments", modelsBodyful},
		{http.MethodPut, "/inference-deployments/{id}", modelsBodyful},
		{http.MethodDelete, "/inference-deployments/{id}", modelsDeleteBodyless},
		{http.MethodPost, "/finetune-jobs", modelsBodyful},
		{http.MethodPut, "/finetune-jobs/{id}", modelsBodyful},
		{http.MethodPut, "/gpai-posture", modelsBodyful},
		{http.MethodPut, "/admission-policy", modelsBodyful},
		{http.MethodPost, "/model-versions/{id}/admit", modelsBodyful},
		{http.MethodPost, "/datasets", modelsBodyful},
		{http.MethodDelete, "/datasets/{id}", modelsDeleteBodyless},
		{http.MethodPost, "/owned-models/{id}/aibom", modelsPostBodyless},
		{http.MethodPost, "/agent-artifacts", modelsBodyful},
		{http.MethodDelete, "/agent-artifacts/{id}", modelsDeleteBodyless},
		{http.MethodPost, "/agent-artifacts/aibom", modelsPostBodyless},
		{http.MethodPost, "/model-groups", modelsBodyful},
		{http.MethodPut, "/model-groups/{id}", modelsBodyful},
		{http.MethodDelete, "/model-groups/{id}", modelsDeleteBodyless},
		{http.MethodPost, "/model-access", modelsBodyful},
		{http.MethodPut, "/model-access/{id}", modelsBodyful},
		{http.MethodDelete, "/model-access/{id}", modelsDeleteBodyless},
	}

	wantCounts := map[modelsRequestBodyKind]int{
		modelsBodyful:        23,
		modelsPostBodyless:   3,
		modelsDeleteBodyless: 9,
	}
	gotCounts := make(map[modelsRequestBodyKind]int)
	seen := make(map[string]struct{}, len(tests))
	for _, tt := range tests {
		key := tt.method + " " + tt.pattern
		if _, duplicate := seen[key]; duplicate {
			t.Fatalf("duplicate mutation in test catalog: %s", key)
		}
		seen[key] = struct{}{}

		route := moduleRoute{ns: "models", method: tt.method, pattern: tt.pattern}
		decl, ok := modelsRequestBodyDeclarationFor(route)
		if !ok {
			t.Errorf("%s is not classified", key)
			continue
		}
		if decl.kind != tt.kind {
			t.Errorf("%s kind = %v, want %v", key, decl.kind, tt.kind)
		}
		gotCounts[decl.kind]++

		body, hasBody := modelsRequestBody(route)
		if hasBody != (tt.kind == modelsBodyful) {
			t.Errorf("%s requestBody present = %t, want %t", key, hasBody, tt.kind == modelsBodyful)
		}
		if !hasBody && body != nil {
			t.Errorf("%s returned a body with ok=false: %#v", key, body)
		}
	}
	if !reflect.DeepEqual(gotCounts, wantCounts) {
		t.Fatalf("classification counts = %#v, want %#v", gotCounts, wantCounts)
	}
	if len(seen) != 35 {
		t.Fatalf("classified %d mutations, want 35", len(seen))
	}
}

func TestModelsRequestBodySchemasMatchHandlerDTOs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		method     string
		pattern    string
		properties []string
		required   []string
	}{
		{
			http.MethodPost, "/routing-policies",
			[]string{"access_tiers", "allow_deprecated", "deny_deprecated", "deny_retired", "enabled", "gateway_endpoint", "id", "min_context_window", "name", "pinned_model", "preferred_providers", "require_zdr", "required_capabilities", "strategy"},
			[]string{"name"},
		},
		{
			http.MethodPost, "/routing-policies/{id}/execute",
			[]string{"input", "max_tokens", "session_ref", "surface"},
			[]string{"input"},
		},
		{
			http.MethodPost, "/keys",
			[]string{"created_at", "ext_id", "hint", "id", "name", "owner_ref", "provider_ref", "ref_kind", "status", "workspace_ref"},
			[]string{"ext_id", "provider_ref", "ref_kind"},
		},
		{
			http.MethodPut, "/workspace-residency",
			[]string{"allowed_geos", "as_of", "default_geo", "id", "workspace_geo", "workspace_ref"},
			[]string{"workspace_ref"},
		},
		{
			http.MethodPut, "/access-tier-entitlements",
			[]string{"as_of", "id", "note", "state", "tier", "updated_by"},
			[]string{"state", "tier"},
		},
		{
			http.MethodPost, "/owned-models",
			[]string{"base_ref", "id", "kind", "name", "note", "owner_ref", "provider_ref", "status", "visibility"},
			[]string{"kind", "name"},
		},
		{
			http.MethodPost, "/model-versions",
			[]string{"artifact_ref", "id", "note", "owned_ref", "parent_ref", "source_ref", "status", "version"},
			[]string{"owned_ref", "version"},
		},
		{
			http.MethodPost, "/inference-deployments",
			[]string{"deployment_type", "endpoint_ref", "governed", "id", "name", "note", "owned_ref", "runtime", "status", "version_ref"},
			[]string{"name", "runtime"},
		},
		{
			http.MethodPost, "/finetune-jobs",
			[]string{"base_ref", "dataset_ref", "ended_at", "id", "name", "note", "result_version_ref", "runtime", "started_at", "status"},
			[]string{"name"},
		},
		{
			http.MethodPut, "/gpai-posture",
			[]string{"attested_at", "attested_by", "cop_signatory", "copyright_policy", "downstream_info", "id", "note", "provider_ref", "safety_report", "systemic_risk", "technical_docs", "training_data_summary", "verification_method", "verified"},
			[]string{"provider_ref"},
		},
		{
			http.MethodPut, "/admission-policy",
			[]string{"allowed_identities", "allowed_issuers", "attested_at", "attested_by", "note", "require_artifact_digests", "require_signed", "trusted_keys", "trusted_roots"},
			nil,
		},
		{
			http.MethodPost, "/model-versions/{id}/admit",
			[]string{"aibom_ref", "bundle", "model_ref", "note", "resolved_digests"},
			[]string{"bundle"},
		},
		{
			http.MethodPost, "/datasets",
			[]string{"attested_at", "attested_by", "classification", "content_alg", "content_hash", "governance", "id", "name", "note", "owned_ref", "source_ref", "verified"},
			[]string{"name"},
		},
		{
			http.MethodPost, "/agent-artifacts",
			[]string{"artifact_class", "attested_at", "attested_by", "content_alg", "content_hash", "id", "name", "note", "posture_grade", "posture_issues", "posture_scanned", "provenance", "source_ref", "verified", "version"},
			[]string{"artifact_class", "name"},
		},
		{
			http.MethodPost, "/model-groups",
			[]string{"description", "family_selectors", "id", "member_refs", "name", "tier_selectors"},
			[]string{"name"},
		},
		{
			http.MethodPost, "/model-access",
			[]string{"budget_ref", "description", "effect", "id", "subject_kind", "subject_ref", "surfaces", "target_kind", "target_ref", "workspace_ref"},
			[]string{"subject_kind", "subject_ref", "target_kind", "target_ref"},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.method+" "+tt.pattern, func(t *testing.T) {
			body, ok := modelsRequestBody(moduleRoute{
				ns: "models", method: tt.method, pattern: tt.pattern,
			})
			if !ok {
				t.Fatal("requestBody missing")
			}
			if got := body["required"]; got != true {
				t.Fatalf("requestBody.required = %#v, want true", got)
			}
			content := modelsMustMap(t, body["content"], "requestBody.content")
			if got := modelsSortedMapKeys(content); !reflect.DeepEqual(got, []string{"application/json"}) {
				t.Fatalf("requestBody media types = %v, want [application/json]", got)
			}
			schema := modelsBodySchema(t, body)
			if schema["type"] != "object" || schema["additionalProperties"] != false {
				t.Fatalf("root schema is not a closed object: %#v", schema)
			}
			properties := modelsMustMap(t, schema["properties"], "schema.properties")
			if got := modelsSortedMapKeys(properties); !reflect.DeepEqual(got, tt.properties) {
				t.Errorf("properties = %v, want %v", got, tt.properties)
			}
			if got := modelsSortedStrings(schema["required"]); !reflect.DeepEqual(got, tt.required) {
				t.Errorf("required = %v, want %v", got, tt.required)
			}
		})
	}
}

func TestModelsRequestBodyConditionalValidationShape(t *testing.T) {
	t.Parallel()

	tests := []struct {
		method  string
		pattern string
		keyword string
		count   int
	}{
		{http.MethodPost, "/inference-deployments", "allOf", 2},
		{http.MethodPut, "/admission-policy", "allOf", 3},
		{http.MethodPost, "/agent-artifacts", "allOf", 1},
		{http.MethodPost, "/model-groups", "anyOf", 3},
	}
	for _, tt := range tests {
		body, ok := modelsRequestBody(moduleRoute{
			ns: "models", method: tt.method, pattern: tt.pattern,
		})
		if !ok {
			t.Fatalf("%s %s requestBody missing", tt.method, tt.pattern)
		}
		schema := modelsBodySchema(t, body)
		branches, ok := schema[tt.keyword].([]any)
		if !ok || len(branches) != tt.count {
			t.Fatalf("%s %s %s = %#v, want %d branches", tt.method, tt.pattern, tt.keyword, schema[tt.keyword], tt.count)
		}
	}
}

func TestModelsRequestBodyOMSWireKeepsPermissiveDecoderBoundaries(t *testing.T) {
	t.Parallel()

	body, ok := modelsRequestBody(moduleRoute{
		ns: "models", method: http.MethodPost, pattern: "/model-versions/{id}/admit",
	})
	if !ok {
		t.Fatal("admission requestBody missing")
	}
	root := modelsBodySchema(t, body)
	properties := modelsMustMap(t, root["properties"], "schema.properties")

	resolved := modelsMustMap(t, properties["resolved_digests"], "resolved_digests")
	additional := modelsMustMap(t, resolved["additionalProperties"], "resolved_digests.additionalProperties")
	if additional["type"] != "string" {
		t.Fatalf("resolved_digests values = %#v, want strings", additional)
	}

	bundle := modelsMustMap(t, properties["bundle"], "bundle")
	if bundle["additionalProperties"] != true {
		t.Fatalf("bundle additionalProperties = %#v, want true", bundle["additionalProperties"])
	}
	if got := modelsSortedStrings(bundle["required"]); !reflect.DeepEqual(got, []string{"dsseEnvelope"}) {
		t.Fatalf("bundle required = %v, want [dsseEnvelope]", got)
	}
	bundleProperties := modelsMustMap(t, bundle["properties"], "bundle.properties")
	verification := modelsMustMap(t, bundleProperties["verificationMaterial"], "verificationMaterial")
	if verification["additionalProperties"] != true {
		t.Fatalf("verificationMaterial additionalProperties = %#v, want true", verification["additionalProperties"])
	}
	envelope := modelsMustMap(t, bundleProperties["dsseEnvelope"], "dsseEnvelope")
	if envelope["additionalProperties"] != true {
		t.Fatalf("dsseEnvelope additionalProperties = %#v, want true", envelope["additionalProperties"])
	}
	if got := modelsSortedStrings(envelope["required"]); !reflect.DeepEqual(got, []string{"payloadType", "signatures"}) {
		t.Fatalf("DSSE required = %v", got)
	}
	envelopeProperties := modelsMustMap(t, envelope["properties"], "dsseEnvelope.properties")
	payload := modelsMustMap(t, envelopeProperties["payload"], "payload")
	if payload["contentEncoding"] != "base64" || payload["contentMediaType"] != "application/vnd.in-toto+json" {
		t.Fatalf("DSSE payload encoding = %#v", payload)
	}
	statement := modelsMustMap(t, payload["contentSchema"], "payload.contentSchema")
	if statement["additionalProperties"] != true {
		t.Fatalf("OMS statement additionalProperties = %#v, want true", statement["additionalProperties"])
	}
	statementProperties := modelsMustMap(t, statement["properties"], "OMS statement properties")
	if got := modelsSortedMapKeys(statementProperties); !reflect.DeepEqual(got, []string{"_type", "predicate", "predicateType", "subject"}) {
		t.Fatalf("OMS statement properties = %v", got)
	}
	predicate := modelsMustMap(t, statementProperties["predicate"], "OMS predicate")
	predicateProperties := modelsMustMap(t, predicate["properties"], "OMS predicate properties")
	resources := modelsMustMap(t, predicateProperties["resources"], "OMS resources")
	if _, overclaimed := resources["minItems"]; overclaimed {
		t.Fatalf("OMS resources incorrectly claims a handler minimum: %#v", resources)
	}
}

func TestModelsRequestBodyRegistryIsScopedAndFresh(t *testing.T) {
	t.Parallel()

	known := moduleRoute{ns: "models", method: http.MethodPost, pattern: "/owned-models"}
	first, ok := modelsRequestBody(known)
	if !ok {
		t.Fatal("known Models requestBody missing")
	}
	firstProperties := modelsMustMap(t, modelsBodySchema(t, first)["properties"], "schema.properties")
	firstProperties["not_a_real_field"] = oaObj("type", "string")
	second, ok := modelsRequestBody(known)
	if !ok {
		t.Fatal("known Models requestBody disappeared")
	}
	secondProperties := modelsMustMap(t, modelsBodySchema(t, second)["properties"], "schema.properties")
	if _, leaked := secondProperties["not_a_real_field"]; leaked {
		t.Fatal("request schema builders share mutable property maps")
	}

	for _, route := range []moduleRoute{
		{ns: "models", method: http.MethodGet, pattern: "/owned-models"},
		{ns: "models", method: http.MethodPost, pattern: "/unknown"},
		{ns: "finops", method: http.MethodPost, pattern: "/owned-models"},
	} {
		if decl, found := modelsRequestBodyDeclarationFor(route); found {
			t.Errorf("unexpected declaration %#v for %#v", decl, route)
		}
		if body, found := modelsRequestBody(route); found || body != nil {
			t.Errorf("unexpected requestBody %#v for %#v", body, route)
		}
	}
}

func modelsBodySchema(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	content := modelsMustMap(t, body["content"], "requestBody.content")
	media := modelsMustMap(t, content["application/json"], "application/json")
	return modelsMustMap(t, media["schema"], "application/json.schema")
}

func modelsMustMap(t *testing.T, value any, label string) map[string]any {
	t.Helper()
	got, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("%s = %#v, want map[string]any", label, value)
	}
	return got
}

func modelsSortedMapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func modelsSortedStrings(value any) []string {
	if value == nil {
		return nil
	}
	var out []string
	switch values := value.(type) {
	case []any:
		for _, value := range values {
			if item, ok := value.(string); ok {
				out = append(out, item)
			}
		}
	case []string:
		out = append(out, values...)
	}
	sort.Strings(out)
	return out
}
