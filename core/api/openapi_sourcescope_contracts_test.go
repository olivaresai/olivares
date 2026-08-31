// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"net/http"
	"reflect"
	"testing"
)

func TestSourceScopeRequestBodyCensus(t *testing.T) {
	t.Parallel()
	tests := []struct {
		method, pattern string
		kind            sourceScopeRequestBodyKind
	}{
		{http.MethodPost, "/bindings", sourceScopeBodyful},
		{http.MethodPut, "/bindings/{id}", sourceScopeBodyful},
		{http.MethodDelete, "/bindings/{id}", sourceScopeBodyless},
		{http.MethodPost, "/sources/disable-scoping", sourceScopeBodyful},
		{http.MethodPut, "/guard-postures", sourceScopeBodyful},
		{http.MethodPost, "/posture-requests/{id}/approve", sourceScopeBodyless},
		{http.MethodPost, "/posture-requests/{id}/reject", sourceScopeBodyless},
		{http.MethodPost, "/assignments", sourceScopeBodyful},
		{http.MethodPut, "/assignments/{id}", sourceScopeBodyful},
		{http.MethodDelete, "/assignments/{id}", sourceScopeBodyless},
		{http.MethodPost, "/workspace-connectors", sourceScopeBodyful},
		{http.MethodPut, "/workspace-connectors/{id}", sourceScopeBodyful},
		{http.MethodDelete, "/workspace-connectors/{id}", sourceScopeBodyless},
	}
	counts := map[sourceScopeRequestBodyKind]int{}
	for _, test := range tests {
		route := moduleRoute{ns: "sourcescope", method: test.method, pattern: test.pattern}
		decl, ok := sourceScopeRequestBodyDeclarationFor(route)
		if !ok || decl.kind != test.kind {
			t.Fatalf("%s %s = (%#v, %t)", test.method, test.pattern, decl, ok)
		}
		_, hasBody := sourceScopeRequestBody(route)
		if hasBody != (test.kind == sourceScopeBodyful) {
			t.Fatalf("%s %s requestBody presence = %t", test.method, test.pattern, hasBody)
		}
		counts[test.kind]++
	}
	want := map[sourceScopeRequestBodyKind]int{sourceScopeBodyful: 8, sourceScopeBodyless: 5}
	if !reflect.DeepEqual(counts, want) {
		t.Fatalf("census = %#v, want %#v", counts, want)
	}
}

func TestSourceScopeSchemasMatchStrictHandlerDTOs(t *testing.T) {
	t.Parallel()
	createBinding := sourceScopeBindingSchema(true)
	if createBinding["additionalProperties"] != false {
		t.Fatal("binding decoder rejects unknown fields")
	}
	wantBindingRequired := []string{"scope_tree", "source_ref", "source_type"}
	if got := capabilitiesSortedStrings(createBinding["required"]); !reflect.DeepEqual(got, wantBindingRequired) {
		t.Fatalf("binding required = %v, want %v", got, wantBindingRequired)
	}
	createConnector := sourceScopeWorkspaceConnectorSchema(true)
	wantConnectorRequired := []string{"kind", "name", "workspace_ref"}
	if got := capabilitiesSortedStrings(createConnector["required"]); !reflect.DeepEqual(got, wantConnectorRequired) {
		t.Fatalf("connector required = %v, want %v", got, wantConnectorRequired)
	}
}
