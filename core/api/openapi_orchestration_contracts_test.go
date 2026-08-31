// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"net/http"
	"reflect"
	"testing"
)

func TestOrchestrationRequestBodyCensus(t *testing.T) {
	t.Parallel()
	tests := []struct {
		method, pattern string
		kind            orchestrationRequestBodyKind
		required        bool
	}{
		{http.MethodPost, "/schedules", orchestrationBodyful, true},
		{http.MethodPatch, "/schedules/{id}", orchestrationBodyful, true},
		{http.MethodPost, "/schedules/{id}/fire", orchestrationBodyful, false},
		{http.MethodPost, "/schedules/{id}/restore", orchestrationBodyful, true},
		{http.MethodPost, "/workflows", orchestrationBodyful, true},
		{http.MethodPatch, "/workflows/{id}", orchestrationBodyful, true},
		{http.MethodPut, "/workflows/{id}/steps", orchestrationBodyful, true},
		{http.MethodPost, "/workflows/{id}/restore", orchestrationBodyful, true},
		{http.MethodPost, "/workflows/{id}/dry-run", orchestrationBodyless, false},
		{http.MethodPost, "/workflows/{id}/run", orchestrationBodyful, false},
	}
	counts := map[orchestrationRequestBodyKind]int{}
	for _, test := range tests {
		route := moduleRoute{ns: "orchestration", method: test.method, pattern: test.pattern}
		decl, ok := orchestrationRequestBodyDeclarationFor(route)
		if !ok || decl.kind != test.kind || decl.required != test.required {
			t.Fatalf("%s %s = (%#v, %t)", test.method, test.pattern, decl, ok)
		}
		body, hasBody := orchestrationRequestBody(route)
		if hasBody != (test.kind == orchestrationBodyful) {
			t.Fatalf("%s %s requestBody presence = %t", test.method, test.pattern, hasBody)
		}
		if hasBody && body["required"] != test.required {
			t.Fatalf("%s %s required = %#v", test.method, test.pattern, body["required"])
		}
		counts[test.kind]++
	}
	want := map[orchestrationRequestBodyKind]int{orchestrationBodyful: 9, orchestrationBodyless: 1}
	if !reflect.DeepEqual(counts, want) {
		t.Fatalf("census = %#v, want %#v", counts, want)
	}
}

func TestOrchestrationSchemasMatchStrictHandlerContracts(t *testing.T) {
	t.Parallel()
	create := orchestrationCreateScheduleSchema()
	if create["additionalProperties"] != false {
		t.Fatal("schedule decoder rejects unknown fields")
	}
	wantScheduleRequired := []string{"name", "subject_kind", "subject_ref", "trigger_kind"}
	if got := capabilitiesSortedStrings(create["required"]); !reflect.DeepEqual(got, wantScheduleRequired) {
		t.Fatalf("schedule required = %v, want %v", got, wantScheduleRequired)
	}
	variants := orchestrationStepVariants()
	if len(variants) != 20 {
		t.Fatalf("step variants = %d, want closed handler vocabulary of 20", len(variants))
	}
	seen := map[string]bool{}
	for _, raw := range variants {
		variant := raw.(map[string]any)
		if variant["additionalProperties"] != false {
			t.Fatal("step decoder rejects unknown fields")
		}
		props := variant["properties"].(map[string]any)
		kind := props["kind"].(map[string]any)["const"].(string)
		if seen[kind] {
			t.Fatalf("duplicate step kind %q", kind)
		}
		seen[kind] = true
	}
}
