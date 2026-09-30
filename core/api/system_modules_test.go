// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

type systemScopeModule struct{}

func (systemScopeModule) APINamespace() string           { return "system-scope-test" }
func (systemScopeModule) Permissions() []auth.Permission { return nil }
func (systemScopeModule) APIRoutes(reg api.RouteRegistrar) {
	reg.(api.SystemRouteRegistrar).HandleSystem("GET", "/status", func(w http.ResponseWriter, _ *http.Request, mc api.ModuleContext) {
		_ = json.NewEncoder(w).Encode(map[string]any{"tenant": mc.Tenant, "superadmin": mc.Principal.Superadmin})
	})
}
func TestSystemModuleRoutesUseDeploymentAuthorityWithoutTenantSelection(t *testing.T) {
	h := newHarness(t, systemScopeModule{})
	root := h.adminLogin()
	const path = "/v1/m/system-scope-test/status"
	for _, tenant := range []string{"", model.NewTenantID().String(), "invalid"} {
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Authorization", "Bearer "+root)
		req.Header.Set("X-Olivares-Tenant", tenant)
		w := httptest.NewRecorder()
		h.srv.Handler().ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatalf("tenant %q: %d %s", tenant, w.Code, w.Body.String())
		}
		var got struct {
			Tenant     model.TenantID `json:"tenant"`
			Superadmin bool           `json:"superadmin"`
		}
		json.Unmarshal(w.Body.Bytes(), &got)
		if got.Tenant != model.SystemTenantID || !got.Superadmin {
			t.Fatalf("context %+v", got)
		}
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("system response can be cached")
		}
	}
	req := httptest.NewRequest("GET", path, nil)
	w := httptest.NewRecorder()
	h.srv.Handler().ServeHTTP(w, req)
	if w.Code != 401 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("anonymous %d headers=%v", w.Code, w.Header())
	}
}

type documentedSystemModule struct{ systemScopeModule }

func (documentedSystemModule) APIRoutes(reg api.RouteRegistrar) {
	reg.(api.SystemRouteRegistrar).HandleSystem("POST", "/status", func(http.ResponseWriter, *http.Request, api.ModuleContext) {})
}
func (documentedSystemModule) OperationDocumentation(string, string) (api.ModuleOperationDocumentation, bool) {
	return api.ModuleOperationDocumentation{BodyKind: api.ModuleOperationJSONBody, RequestBody: map[string]any{"required": true, "content": map[string]any{"application/json": map[string]any{"schema": map[string]any{"type": "object"}}}}, SuccessResponses: map[string]any{"202": map[string]any{"description": "accepted"}}, RequiredAssurance: 3}, true
}
func TestSystemModuleOperationDocumentationPreservesAuthority(t *testing.T) {
	doc := api.ModuleOpenAPIDocument([]api.Module{documentedSystemModule{}})
	operation := doc["paths"].(map[string]any)["/v1/m/system-scope-test/status"].(map[string]any)["post"].(map[string]any)
	if operation["x-olivares-scope"] != "system" || operation["x-required-permission"] != "system:admin" || operation["x-required-assurance"] != 3 || operation["x-olivares-request-body-disposition"] != "schema-published" || operation["requestBody"] == nil {
		t.Fatalf("operation %#v", operation)
	}
	responses := operation["responses"].(map[string]any)
	if responses["202"] == nil || responses["200"] != nil {
		t.Fatalf("responses %#v", responses)
	}
	parameters, _ := operation["parameters"].([]any)
	for _, raw := range parameters {
		parameter := raw.(map[string]any)
		if parameter["name"] == "X-Olivares-Tenant" {
			t.Fatal("system documentation introduces tenant selection")
		}
	}
}
