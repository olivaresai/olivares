// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"net/http"
	"reflect"
	"testing"
)

func TestRedTeamRequestBodyCensus(t *testing.T) {
	t.Parallel()
	patterns := []string{"/targets", "/targets/{id}/authorize", "/runs"}
	for _, pattern := range patterns {
		route := moduleRoute{ns: "redteam", method: http.MethodPost, pattern: pattern}
		decl, ok := redTeamRequestBodyDeclarationFor(route)
		if !ok || decl.kind != redTeamBodyful {
			t.Fatalf("POST %s = (%#v, %t), want bodyful", pattern, decl, ok)
		}
		body, hasBody := redTeamRequestBody(route)
		if !hasBody || body["required"] != true {
			t.Fatalf("POST %s requestBody = (%#v, %t)", pattern, body, hasBody)
		}
	}
}

func TestRedTeamSchemasMatchHandlerDTOs(t *testing.T) {
	t.Parallel()
	register := redTeamRegisterTargetSchema()
	if got := capabilitiesSortedStrings(register["required"]); !reflect.DeepEqual(got, []string{"agent_ref"}) {
		t.Fatalf("register required = %v", got)
	}
	authorize := redTeamAuthorizeTargetSchema()
	if _, required := authorize["required"]; required {
		t.Fatal("authorize accepts an empty object as revocation")
	}
	launch := redTeamLaunchRunSchema()
	if got := capabilitiesSortedStrings(launch["required"]); !reflect.DeepEqual(got, []string{"target_ref"}) {
		t.Fatalf("launch required = %v", got)
	}
	for _, schema := range []map[string]any{register, authorize, launch} {
		if schema["additionalProperties"] != false {
			t.Fatal("red-team decoder is strict")
		}
	}
}
