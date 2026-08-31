// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"net/http"
	"reflect"
	"testing"
)

func TestSandboxRequestBodyCensus(t *testing.T) {
	t.Parallel()
	tests := []struct {
		pattern string
		kind    sandboxRequestBodyKind
	}{
		{"/scenarios", sandboxBodyful},
		{"/scenarios/{id}/archive", sandboxBodyless},
		{"/scenarios/{id}/run", sandboxBodyful},
		{"/replay", sandboxBodyful},
		{"/compare", sandboxBodyful},
	}
	counts := map[sandboxRequestBodyKind]int{}
	for _, test := range tests {
		route := moduleRoute{ns: "sandbox", method: http.MethodPost, pattern: test.pattern}
		decl, ok := sandboxRequestBodyDeclarationFor(route)
		if !ok || decl.kind != test.kind {
			t.Fatalf("POST %s = (%#v, %t), want %v", test.pattern, decl, ok, test.kind)
		}
		_, hasBody := sandboxRequestBody(route)
		if hasBody != (test.kind == sandboxBodyful) {
			t.Fatalf("POST %s requestBody presence = %t", test.pattern, hasBody)
		}
		counts[test.kind]++
	}
	want := map[sandboxRequestBodyKind]int{sandboxBodyful: 4, sandboxBodyless: 1}
	if !reflect.DeepEqual(counts, want) {
		t.Fatalf("census = %#v, want %#v", counts, want)
	}
}

func TestSandboxSchemasMatchHandlerDTOs(t *testing.T) {
	t.Parallel()
	create := sandboxCreateScenarioSchema()
	if got := capabilitiesSortedStrings(create["required"]); !reflect.DeepEqual(got, []string{"name"}) {
		t.Fatalf("create required = %v", got)
	}
	run := sandboxRunScenarioSchema()
	if _, required := run["required"]; required {
		t.Fatal("scenario run accepts an empty JSON object")
	}
	replay := sandboxReplaySchema()
	if got := capabilitiesSortedStrings(replay["required"]); !reflect.DeepEqual(got, []string{"session_ref"}) {
		t.Fatalf("replay required = %v", got)
	}
	compare := sandboxCompareSchema()
	if got := capabilitiesSortedStrings(compare["required"]); !reflect.DeepEqual(got, []string{"baseline_variant", "candidate_variant"}) {
		t.Fatalf("compare required = %v", got)
	}
	if _, oneSubject := compare["anyOf"]; !oneSubject {
		t.Fatal("compare must identify a scenario or session")
	}
}
