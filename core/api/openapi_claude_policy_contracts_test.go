// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"net/http"
	"reflect"
	"testing"
)

func TestClaudePolicyRequestBodyCensus(t *testing.T) {
	t.Parallel()
	patterns := []string{"/{surface}/validate", "/{surface}/dry-run", "/{surface}/publish", "/{surface}/checkin"}
	for _, pattern := range patterns {
		route := moduleRoute{ns: "claude-policy", method: http.MethodPost, pattern: pattern}
		decl, ok := claudePolicyRequestBodyDeclarationFor(route)
		if !ok || decl.kind != claudePolicyBodyful {
			t.Fatalf("POST %s = (%#v, %t), want bodyful", pattern, decl, ok)
		}
		body, hasBody := claudePolicyRequestBody(route)
		if !hasBody || body["required"] != true {
			t.Fatalf("POST %s requestBody = (%#v, %t)", pattern, body, hasBody)
		}
	}
}

func TestClaudePolicySchemasMatchHandlerSemantics(t *testing.T) {
	t.Parallel()
	validate := claudePolicyContentSchema(false, false)
	if _, required := validate["required"]; required {
		t.Fatal("validate accepts empty content and returns diagnostics")
	}
	for _, schema := range []map[string]any{
		claudePolicyContentSchema(true, false),
		claudePolicyContentSchema(true, true),
	} {
		if got := capabilitiesSortedStrings(schema["required"]); !reflect.DeepEqual(got, []string{"content"}) {
			t.Fatalf("content required = %v", got)
		}
		if schema["additionalProperties"] != false {
			t.Fatal("content decoder is strict")
		}
	}
	checkin := claudePolicyCheckinSchema()
	if got := capabilitiesSortedStrings(checkin["required"]); !reflect.DeepEqual(got, []string{"scope"}) {
		t.Fatalf("checkin required = %v", got)
	}
	if got := len(checkin["properties"].(map[string]any)); got != 5 {
		t.Fatalf("checkin property count = %d, want 5", got)
	}
}
