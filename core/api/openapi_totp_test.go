// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"github.com/olivaresai/olivares/core/api"
	"testing"
)

func TestTOTPOpenAPIContract(t *testing.T) {
	doc := api.OpenAPIDocument()
	paths := doc["paths"].(map[string]any)
	for _, route := range []struct{ method, path string }{
		{"post", "/v1/auth/totp/enrol"}, {"post", "/v1/auth/totp/activate"}, {"post", "/v1/auth/totp/challenge"},
		{"get", "/v1/auth/totp/status"}, {"delete", "/v1/auth/totp"}, {"get", "/v1/auth/totp/policy"}, {"put", "/v1/auth/totp/policy"},
		{"get", "/v1/users/{id}/totp"}, {"post", "/v1/users/{id}/totp/reset"},
	} {
		t.Run(route.method+route.path, func(t *testing.T) {
			p, ok := paths[route.path].(map[string]any)
			if !ok {
				t.Fatal("registered TOTP route missing from public contract")
			}
			op, ok := p[route.method].(map[string]any)
			if !ok || op["operationId"] == "" {
				t.Fatal("TOTP operation missing")
			}
			if op["description"] == nil {
				t.Fatal("TOTP operation has no behavior description")
			}
		})
	}
	login := paths["/v1/auth/login"].(map[string]any)["post"].(map[string]any)
	schema := login["responses"].(map[string]any)["200"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
	variants, ok := schema["oneOf"].([]any)
	if !ok || len(variants) != 2 {
		t.Fatal("login contract must describe completed session and pending MFA separately")
	}
}
