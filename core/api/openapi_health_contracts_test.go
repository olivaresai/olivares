// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"net/http"
	"reflect"
	"testing"
)

func TestHealthRequestBodyCensus(t *testing.T) {
	t.Parallel()
	tests := []struct {
		method, pattern string
		kind            healthRequestBodyKind
	}{
		{http.MethodPost, "/checks", healthBodyful},
		{http.MethodPut, "/checks/{id}", healthBodyful},
		{http.MethodDelete, "/checks/{id}", healthBodyless},
		{http.MethodPost, "/checks/{id}/report", healthBodyful},
		{http.MethodPost, "/incidents/{id}/resolve", healthBodyless},
	}
	counts := map[healthRequestBodyKind]int{}
	for _, test := range tests {
		route := moduleRoute{ns: "health", method: test.method, pattern: test.pattern}
		decl, ok := healthRequestBodyDeclarationFor(route)
		if !ok || decl.kind != test.kind {
			t.Fatalf("%s %s = (%#v, %t), want %v", test.method, test.pattern, decl, ok, test.kind)
		}
		_, hasBody := healthRequestBody(route)
		if hasBody != (test.kind == healthBodyful) {
			t.Fatalf("%s %s requestBody presence = %t", test.method, test.pattern, hasBody)
		}
		counts[test.kind]++
	}
	want := map[healthRequestBodyKind]int{healthBodyful: 3, healthBodyless: 2}
	if !reflect.DeepEqual(counts, want) {
		t.Fatalf("census = %#v, want %#v", counts, want)
	}
}

func TestHealthSchemasMatchHandlerDTOs(t *testing.T) {
	t.Parallel()
	create := healthCreateCheckSchema()
	if create["additionalProperties"] != false {
		t.Fatal("create decoder is strict")
	}
	if got := capabilitiesSortedStrings(create["required"]); !reflect.DeepEqual(got, []string{"subject_kind", "subject_ref"}) {
		t.Fatalf("create required = %v", got)
	}
	update := healthUpdateCheckSchema()
	if _, required := update["required"]; required {
		t.Fatal("an empty update object is accepted by the handler")
	}
	report := healthReportSchema()
	if got := capabilitiesSortedStrings(report["required"]); !reflect.DeepEqual(got, []string{"state"}) {
		t.Fatalf("report required = %v", got)
	}
	state := report["properties"].(map[string]any)["state"].(map[string]any)
	if got := capabilitiesSortedStrings(state["enum"]); !reflect.DeepEqual(got, []string{"degraded", "down", "healthy"}) {
		t.Fatalf("report states = %v", got)
	}
}
