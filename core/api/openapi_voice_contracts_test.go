// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"net/http"
	"reflect"
	"testing"
)

func TestVoiceRequestBodyCensus(t *testing.T) {
	t.Parallel()
	tests := []moduleRoute{
		{ns: "voice", method: http.MethodPut, pattern: "/policies"},
		{ns: "voice", method: http.MethodPost, pattern: "/sessions/open"},
	}
	for _, route := range tests {
		decl, ok := voiceRequestBodyDeclarationFor(route)
		if !ok || decl.kind != voiceBodyful {
			t.Fatalf("%s %s = (%#v, %t), want bodyful", route.method, route.pattern, decl, ok)
		}
		body, hasBody := voiceRequestBody(route)
		if !hasBody || body["required"] != true {
			t.Fatalf("%s %s requestBody = (%#v, %t)", route.method, route.pattern, body, hasBody)
		}
	}
}

func TestVoiceSchemasMatchHandlerDTOs(t *testing.T) {
	t.Parallel()
	policy := voicePolicySchema()
	if policy["additionalProperties"] != false {
		t.Fatal("voice policy decoder is strict")
	}
	if got := capabilitiesSortedStrings(policy["required"]); !reflect.DeepEqual(got, []string{"agent_ref", "allowed_model_ref", "allowed_provider_ref"}) {
		t.Fatalf("policy required = %v", got)
	}
	if got := len(policy["properties"].(map[string]any)); got != 6 {
		t.Fatalf("policy property count = %d, want 6", got)
	}
	open := voiceOpenSchema()
	if got := capabilitiesSortedStrings(open["required"]); !reflect.DeepEqual(got, []string{"agent_ref", "model_ref", "provider_ref", "session_ref"}) {
		t.Fatalf("open required = %v", got)
	}
}
