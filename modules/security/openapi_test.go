// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package security

import (
	"net/http"
	"reflect"
	"testing"

	"github.com/olivaresai/olivares/modules/internal/oastest"
)

func TestSecurityRequestBodyCensus(t *testing.T) {
	t.Parallel()
	tests := []operation{
		{method: http.MethodPatch, pattern: "/findings/{id}"},
		{method: http.MethodPost, pattern: "/guardrails/inspect"},
		{method: http.MethodPut, pattern: "/enforcement"},
		{method: http.MethodPost, pattern: "/cases"},
		{method: http.MethodPatch, pattern: "/cases/{id}"},
		{method: http.MethodPost, pattern: "/cases/{id}/links"},
	}
	for _, route := range tests {
		decl, ok := securityRequestBodyDeclarationFor(route.method, route.pattern)
		if !ok || decl.kind != securityBodyful {
			t.Fatalf("%s %s = (%#v, %t), want bodyful", route.method, route.pattern, decl, ok)
		}
		body, hasBody := securityRequestBody(route.method, route.pattern)
		if !hasBody || body["required"] != true {
			t.Fatalf("%s %s requestBody = (%#v, %t)", route.method, route.pattern, body, hasBody)
		}
	}
}

func TestSecuritySchemasMatchHandlerSemantics(t *testing.T) {
	t.Parallel()
	tests := []struct {
		schema   map[string]any
		required []string
	}{
		{securityTriageSchema(), []string{"status"}},
		{securityInspectSchema(), []string{"surface"}},
		{securityEnforcementSchema(), []string{"class"}},
		{securityCreateCaseSchema(), []string{"title"}},
		{securityUpdateCaseSchema(), []string{}},
		{securityCaseLinkSchema(), []string{"link_kind"}},
	}
	for _, test := range tests {
		if test.schema["additionalProperties"] != false {
			t.Fatal("security decoder is strict")
		}
		if got := oastest.Strings(test.schema["required"]); !reflect.DeepEqual(got, test.required) {
			t.Fatalf("required = %v, want %v", got, test.required)
		}
	}
	if _, conditional := securityCaseLinkSchema()["allOf"]; !conditional {
		t.Fatal("non-note case links must require link_ref")
	}
}

// operation is one route of this module: method and module-relative pattern.
type operation struct{ method, pattern string }
