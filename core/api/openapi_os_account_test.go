// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"github.com/olivaresai/olivares/core/api"
	"testing"
)

func TestOSAccountOpenAPIContract(t *testing.T) {
	doc := api.OpenAPIDocument()
	paths := doc["paths"].(map[string]any)
	for _, route := range []struct{ method, path, permission string }{
		{"post", "/v1/auth/os-account-bindings", "user:write"}, {"post", "/v1/auth/os-account-bindings/complete", ""},
		{"get", "/v1/auth/os-account-bindings/{id}", "user:write"}, {"delete", "/v1/auth/os-account-bindings/{id}", "user:write"},
	} {
		item, ok := paths[route.path].(map[string]any)
		if !ok {
			t.Fatalf("missing %s", route.path)
		}
		op, ok := item[route.method].(map[string]any)
		if !ok || op["description"] == nil || op["security"] == nil {
			t.Fatalf("unusable operation %s", route.path)
		}
		if route.permission != "" && op["x-required-permission"] != route.permission {
			t.Fatal("administration permission changed")
		}
	}
	schemas := doc["components"].(map[string]any)["schemas"].(map[string]any)
	for _, name := range []string{"OSAccountBeginInput", "OSAccountCompleteInput", "OSAccountCeremony", "OSAccountMapping"} {
		schema := schemas[name].(map[string]any)
		if schema["additionalProperties"] != false {
			t.Fatal("open request/result shape")
		}
		fields := schema["properties"].(map[string]any)
		for _, forbidden := range []string{"principal", "session_id", "credential_id", "binding_id", "binding_handle", "admin_proof"} {
			if _, ok := fields[forbidden]; ok {
				t.Fatalf("private/claimed authority field %s", forbidden)
			}
		}
	}
	proof := schemas["OSAccountCompleteInput"].(map[string]any)["properties"].(map[string]any)["password"].(map[string]any)
	if proof["format"] != "byte" || proof["writeOnly"] != true {
		t.Fatal("proof encoding/privacy is ambiguous")
	}
}
