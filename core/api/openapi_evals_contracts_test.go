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

func TestEvalsRequestBodiesClassifyAllMutations(t *testing.T) {
	t.Parallel()

	tests := []struct {
		method  string
		pattern string
		kind    evalsRequestBodyKind
	}{
		{http.MethodPost, "/suites", evalsBodyful},
		{http.MethodPost, "/suites/{id}/cases", evalsBodyful},
		{http.MethodPost, "/suites/{id}/archive", evalsBodyless},
		{http.MethodPost, "/runs", evalsBodyful},
		{http.MethodPost, "/ab", evalsBodyful},
		{http.MethodPost, "/monitor", evalsBodyful},
		{http.MethodPost, "/baselines", evalsBodyful},
		{http.MethodPost, "/calibration/items", evalsBodyful},
		{http.MethodPost, "/calibration/run", evalsBodyful},
		{http.MethodPost, "/gate", evalsBodyful},
		{http.MethodPost, "/gate/{id}/override", evalsBodyful},
	}

	wantCounts := map[evalsRequestBodyKind]int{
		evalsBodyful:         10,
		evalsBodyless:        1,
		evalsBodyNoDerivable: 0,
		evalsBodyPending:     0,
	}
	gotCounts := map[evalsRequestBodyKind]int{
		evalsBodyful:         0,
		evalsBodyless:        0,
		evalsBodyNoDerivable: 0,
		evalsBodyPending:     0,
	}
	seen := make(map[string]struct{}, len(tests))
	for _, tt := range tests {
		key := tt.method + " " + tt.pattern
		if _, duplicate := seen[key]; duplicate {
			t.Fatalf("duplicate mutation in test catalog: %s", key)
		}
		seen[key] = struct{}{}
		decl, ok := evalsRequestBodyDeclarationFor(moduleRoute{
			ns: "evals", method: tt.method, pattern: tt.pattern,
		})
		if !ok {
			t.Errorf("%s is not classified", key)
			continue
		}
		if decl.kind != tt.kind {
			t.Errorf("%s kind = %v, want %v", key, decl.kind, tt.kind)
		}
		gotCounts[decl.kind]++
		body, hasBody := evalsRequestBody(moduleRoute{
			ns: "evals", method: tt.method, pattern: tt.pattern,
		})
		if hasBody != (tt.kind == evalsBodyful) {
			t.Errorf("%s requestBody present = %t, want %t", key, hasBody, tt.kind == evalsBodyful)
		}
		if !hasBody && body != nil {
			t.Errorf("%s returned a body with ok=false: %#v", key, body)
		}
	}
	if len(seen) != 11 {
		t.Fatalf("classified %d mutations, want 11", len(seen))
	}
	if !reflect.DeepEqual(gotCounts, wantCounts) {
		t.Fatalf("classification counts = %#v, want %#v", gotCounts, wantCounts)
	}
}

func TestEvalsRequestBodiesMatchHandlerDTOs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		pattern    string
		properties []string
		required   []string
	}{
		{"/suites", []string{"criterion", "description", "judge_model", "name", "pass_threshold", "regression_threshold", "scorer", "subject_kind", "suite_version"}, []string{"name", "scorer", "subject_kind"}},
		{"/suites/{id}/cases", []string{"case_key", "expected", "input", "metadata", "weight"}, []string{"case_key"}},
		{"/runs", []string{"baseline_ref", "model_ref", "outputs", "prompt_variant", "subject_kind", "subject_ref", "suite_ref"}, []string{"outputs", "suite_ref"}},
		{"/ab", []string{"a", "b", "pairwise", "subject_kind", "subject_ref", "suite_ref"}, []string{"a", "b", "suite_ref"}},
		{"/monitor", []string{"limit", "subject_kind", "subject_ref", "suite"}, nil},
		{"/baselines", []string{"run_ref", "subject_ref", "suite_ref"}, []string{"run_ref", "suite_ref"}},
		{"/calibration/items", []string{"items", "set_name"}, []string{"items"}},
		{"/calibration/run", []string{"judge_model", "kappa_floor", "set_name", "target"}, nil},
		{"/gate", []string{"baseline_ref", "outputs", "sample_size", "seed", "subject_kind", "subject_ref", "suite_ref"}, []string{"outputs", "suite_ref"}},
		{"/gate/{id}/override", []string{"reason"}, []string{"reason"}},
	}

	for _, tt := range tests {
		body, ok := evalsRequestBody(moduleRoute{
			ns: "evals", method: http.MethodPost, pattern: tt.pattern,
		})
		if !ok {
			t.Fatalf("POST %s requestBody missing", tt.pattern)
		}
		if body["required"] != true {
			t.Errorf("POST %s requestBody.required = %#v", tt.pattern, body["required"])
		}
		schema := evalsSchemaFromBody(t, body)
		if schema["type"] != "object" || schema["additionalProperties"] != false {
			t.Errorf("POST %s is not a closed object: %#v", tt.pattern, schema)
		}
		properties := schema["properties"].(map[string]any)
		if got := evalsSortedMapKeys(properties); !reflect.DeepEqual(got, tt.properties) {
			t.Errorf("POST %s properties = %v, want %v", tt.pattern, got, tt.properties)
		}
		gotRequired, _ := schema["required"].([]string)
		sort.Strings(gotRequired)
		if !reflect.DeepEqual(gotRequired, tt.required) {
			t.Errorf("POST %s required = %v, want %v", tt.pattern, gotRequired, tt.required)
		}
	}
}

func TestEvalsRequestBodyNestedContracts(t *testing.T) {
	t.Parallel()

	abBody, _ := evalsRequestBody(moduleRoute{ns: "evals", method: http.MethodPost, pattern: "/ab"})
	ab := evalsSchemaFromBody(t, abBody)
	for _, name := range []string{"a", "b"} {
		variant := ab["properties"].(map[string]any)[name].(map[string]any)
		if variant["additionalProperties"] != false {
			t.Errorf("variant %s is not closed", name)
		}
		if got := variant["required"]; !reflect.DeepEqual(got, []string{"outputs"}) {
			t.Errorf("variant %s required = %#v", name, got)
		}
		outputs := variant["properties"].(map[string]any)["outputs"].(map[string]any)
		if !reflect.DeepEqual(outputs["additionalProperties"], oaObj("type", "string")) {
			t.Errorf("variant %s outputs = %#v", name, outputs)
		}
	}

	itemsBody, _ := evalsRequestBody(moduleRoute{ns: "evals", method: http.MethodPost, pattern: "/calibration/items"})
	itemsSchema := evalsSchemaFromBody(t, itemsBody)
	items := itemsSchema["properties"].(map[string]any)["items"].(map[string]any)
	if items["minItems"] != 1 {
		t.Errorf("calibration items minItems = %#v", items["minItems"])
	}
	item := items["items"].(map[string]any)
	if item["additionalProperties"] != false {
		t.Errorf("calibration item is not closed: %#v", item)
	}
	if got := item["required"]; !reflect.DeepEqual(got, []string{"case_key", "output"}) {
		t.Errorf("calibration item required = %#v", got)
	}
	humanScore := item["properties"].(map[string]any)["human_score"].(map[string]any)
	if got := len(humanScore["anyOf"].([]any)); got != 2 {
		t.Errorf("human_score anyOf has %d branches, want number|null", got)
	}

	caseBody, _ := evalsRequestBody(moduleRoute{ns: "evals", method: http.MethodPost, pattern: "/suites/{id}/cases"})
	caseSchema := evalsSchemaFromBody(t, caseBody)
	metadata := caseSchema["properties"].(map[string]any)["metadata"].(map[string]any)
	if metadata["additionalProperties"] != true {
		t.Error("case metadata must preserve map[string]any values")
	}
}

func TestEvalsRequestBodyDoesNotClaimUnknownRoutes(t *testing.T) {
	t.Parallel()

	for _, route := range []moduleRoute{
		{ns: "evals", method: http.MethodGet, pattern: "/suites"},
		{ns: "evals", method: http.MethodPost, pattern: "/unknown"},
		{ns: "governance", method: http.MethodPost, pattern: "/gate"},
	} {
		if decl, ok := evalsRequestBodyDeclarationFor(route); ok {
			t.Errorf("unexpected declaration %#v for %#v", decl, route)
		}
		if body, ok := evalsRequestBody(route); ok || body != nil {
			t.Errorf("unexpected requestBody %#v for %#v", body, route)
		}
	}
}

func evalsSchemaFromBody(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	content := body["content"].(map[string]any)
	media := content["application/json"].(map[string]any)
	return media["schema"].(map[string]any)
}

func evalsSortedMapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
