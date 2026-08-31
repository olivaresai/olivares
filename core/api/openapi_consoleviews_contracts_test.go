// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"net/http"
	"reflect"
	"testing"
)

func TestConsoleViewsRequestBodyCensus(t *testing.T) {
	t.Parallel()
	tests := []struct {
		method, pattern string
		kind            consoleViewsRequestBodyKind
	}{
		{http.MethodPost, "/views", consoleViewsBodyful},
		{http.MethodPut, "/views/{id}", consoleViewsBodyful},
		{http.MethodDelete, "/views/{id}", consoleViewsBodyless},
	}
	counts := map[consoleViewsRequestBodyKind]int{}
	for _, test := range tests {
		route := moduleRoute{ns: "consoleviews", method: test.method, pattern: test.pattern}
		decl, ok := consoleViewsRequestBodyDeclarationFor(route)
		if !ok || decl.kind != test.kind {
			t.Fatalf("%s %s = (%#v, %t), want %v", test.method, test.pattern, decl, ok, test.kind)
		}
		_, hasBody := consoleViewsRequestBody(route)
		if hasBody != (test.kind == consoleViewsBodyful) {
			t.Fatalf("%s %s requestBody presence = %t", test.method, test.pattern, hasBody)
		}
		counts[test.kind]++
	}
	want := map[consoleViewsRequestBodyKind]int{consoleViewsBodyful: 2, consoleViewsBodyless: 1}
	if !reflect.DeepEqual(counts, want) {
		t.Fatalf("census = %#v, want %#v", counts, want)
	}
}

func TestConsoleViewsSchemaMatchesWritableDTO(t *testing.T) {
	t.Parallel()
	schema := consoleViewsInputSchema()
	if schema["additionalProperties"] != false {
		t.Fatal("saved-view decoder is strict")
	}
	properties := schema["properties"].(map[string]any)
	if got := capabilitiesSortedStrings(schema["required"]); !reflect.DeepEqual(got, []string{"feature_id", "name", "params"}) {
		t.Fatalf("required = %v", got)
	}
	if len(properties) != 5 {
		t.Fatalf("property count = %d, want 5", len(properties))
	}
	params := properties["params"].(map[string]any)
	if params["type"] != "object" || params["additionalProperties"] != true {
		t.Fatalf("params = %#v, want an open JSON object", params)
	}
}
