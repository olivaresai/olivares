// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"net/http"
	"reflect"
	"testing"
)

func TestSessionsClosureRequestBodyCensus(t *testing.T) {
	t.Parallel()
	tests := []struct {
		method, pattern string
		kind            sessionsClosureRequestBodyKind
		required        bool
	}{
		{http.MethodPost, "/runs", sessionsClosureBodyful, true},
		{http.MethodDelete, "/runs/{ref}", sessionsClosureBodyless, false},
		{http.MethodPost, "/runs/{ref}/cleanup", sessionsClosureBodyless, false},
		{http.MethodPost, "/runs/{ref}/resume", sessionsClosureBodyless, false},
		{http.MethodPost, "/templates", sessionsClosureBodyful, true},
		{http.MethodDelete, "/templates/{id}", sessionsClosureBodyless, false},
		{http.MethodPut, "/templates/{id}", sessionsClosureBodyful, true},
		{http.MethodPost, "/templates/{id}/apply", sessionsClosureBodyful, false},
		{http.MethodPost, "/templates/{id}/duplicate", sessionsClosureBodyful, true},
		{http.MethodPost, "/workspaces", sessionsClosureBodyful, true},
		{http.MethodDelete, "/workspaces/{ref}", sessionsClosureBodyless, false},
		{http.MethodDelete, "/workspaces/{ref}/files", sessionsClosureBodyless, false},
		{http.MethodPost, "/workspaces/{ref}/files/dir", sessionsClosureBodyless, false},
		{http.MethodPost, "/workspaces/{ref}/files/move", sessionsClosureBodyful, true},
		{http.MethodPost, "/provider-profiles", sessionsClosureBodyful, true},
		{http.MethodPatch, "/provider-profiles/{ref}", sessionsClosureBodyful, true},
		{http.MethodPost, "/provider-profiles/{ref}/retire", sessionsClosureBodyless, false},
		{http.MethodPost, "/provider-source-bindings", sessionsClosureBodyful, true},
		{http.MethodPost, "/provider-source-bindings/{ref}/revoke", sessionsClosureBodyless, false},
	}
	counts := map[sessionsClosureRequestBodyKind]int{}
	for _, test := range tests {
		route := moduleRoute{ns: "sessions", method: test.method, pattern: test.pattern}
		decl, ok := sessionsClosureRequestBodyDeclarationFor(route)
		if !ok || decl.kind != test.kind || decl.required != test.required {
			t.Fatalf("%s %s = (%#v, %t)", test.method, test.pattern, decl, ok)
		}
		body, hasBody := sessionsClosureRequestBody(route)
		if hasBody != (test.kind == sessionsClosureBodyful) {
			t.Fatalf("%s %s requestBody presence = %t", test.method, test.pattern, hasBody)
		}
		if hasBody && body["required"] != test.required {
			t.Fatalf("%s %s required = %#v", test.method, test.pattern, body["required"])
		}
		counts[test.kind]++
	}
	want := map[sessionsClosureRequestBodyKind]int{sessionsClosureBodyful: 10, sessionsClosureBodyless: 9}
	if !reflect.DeepEqual(counts, want) {
		t.Fatalf("census = %#v, want %#v", counts, want)
	}
	// /runs/{ref}/interrupt LEFT this census when it gained its optional fenced
	// body, and the departure is asserted rather than assumed. Declaring it bodyless
	// here would not have failed anything — sessionsClosureRequestBody publishes
	// nothing for a bodyless route and the run-control switch in openapi_modules.go
	// would still have emitted the schema — so the census would have gone on
	// claiming "no body" about a route that has one. Its contract is pinned with its
	// siblings in TestSessionsRuntimeWorkControlOpenAPI.
	interrupt := moduleRoute{ns: "sessions", method: http.MethodPost, pattern: "/runs/{ref}/interrupt"}
	if decl, ok := sessionsClosureRequestBodyDeclarationFor(interrupt); ok {
		t.Fatalf("interrupt is declared in the closure census as %#v; its body belongs with /input and /stop", decl)
	}
}

func TestSessionsClosureSchemasMatchStrictDTOs(t *testing.T) {
	t.Parallel()
	createTemplate := sessionsCreateTemplateSchema()
	if createTemplate["additionalProperties"] != false {
		t.Fatal("template decoder rejects unknown fields")
	}
	if got := capabilitiesSortedStrings(createTemplate["required"]); !reflect.DeepEqual(got, []string{"name"}) {
		t.Fatalf("template required = %v", got)
	}
	move := sessionsMoveFileSchema()
	if got := capabilitiesSortedStrings(move["required"]); !reflect.DeepEqual(got, []string{"from", "to"}) {
		t.Fatalf("move required = %v", got)
	}
	workspace := sessionsCreateWorkspaceSchema()
	if got := capabilitiesSortedStrings(workspace["required"]); !reflect.DeepEqual(got, []string{"root_path"}) {
		t.Fatalf("workspace required = %v", got)
	}
	profile := sessionsCreateProviderProfileSchema()
	if profile["additionalProperties"] != false {
		t.Fatal("provider profile decoder rejects unknown fields")
	}
	if got := capabilitiesSortedStrings(profile["required"]); !reflect.DeepEqual(got, []string{"config_home", "driver", "user_home"}) {
		t.Fatalf("provider profile required = %v", got)
	}
	binding := sessionsCreateProviderBindingSchema()
	if got := capabilitiesSortedStrings(binding["required"]); !reflect.DeepEqual(got, []string{"profile_ref", "source_id", "source_revision"}) {
		t.Fatalf("provider binding required = %v", got)
	}
}
