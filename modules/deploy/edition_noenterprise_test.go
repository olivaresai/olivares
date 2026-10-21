// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !enterprise

package deploy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
)

// publishedRoute is one route of the module's published contract with the permission
// that guards it. The Business build registers the same table.
type publishedRoute struct {
	method, pattern string
	permission      auth.Permission
}

var publishedRoutes = []publishedRoute{
	{"GET", "/definitions", permDeploymentRead},
	{"POST", "/definitions", permDeploymentWrite},
	{"GET", "/definitions/{id}", permDeploymentRead},
	{"PUT", "/definitions/{id}", permDeploymentWrite},
	{"DELETE", "/definitions/{id}", permDeploymentWrite},
	{"GET", "/definitions/{id}/revisions", permDeploymentRead},
	{"POST", "/definitions/{id}/rollback", permDeploymentWrite},
	{"POST", "/definitions/{id}/plan", permDeploymentWrite},
	{"POST", "/definitions/{id}/verify", permDeploymentWrite},
	{"POST", "/definitions/{id}/apply", permDeploymentAdmin},
	{"POST", "/definitions/{id}/retire", permDeploymentAdmin},
	{"GET", "/wirings", permWiringRead},
	{"GET", "/operations", permDeploymentRead},
	{"GET", "/executor", permDeploymentRead},
	{"GET", "/executor/config", permDeploymentAdmin},
	{"PUT", "/executor/config", permDeploymentAdmin},
	{"POST", "/executor/test", permDeploymentAdmin},
}

// editionRegistrar records every registered route and answers it as a client would,
// with an empty module context: a route that still reached the deployment code would
// fail here.
type editionRegistrar struct {
	t      *testing.T
	routes []publishedRoute
}

func (reg *editionRegistrar) Handle(method, pattern string, p auth.Permission, h api.ModuleHandler) {
	reg.routes = append(reg.routes, publishedRoute{method, pattern, p})
	w := httptest.NewRecorder()
	r := httptest.NewRequest(method, "/v1/m/deploy"+strings.ReplaceAll(pattern, "{id}", "id"), strings.NewReader(`{}`))
	h(w, r, api.ModuleContext{})
	if w.Code != http.StatusNotImplemented {
		reg.t.Errorf("%s %s = %d; want 501", method, pattern, w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		reg.t.Errorf("%s %s Content-Type = %q; want JSON", method, pattern, ct)
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		reg.t.Errorf("%s %s answered %q: %v", method, pattern, w.Body.String(), err)
		return
	}
	// The module error envelope the engine's other module errors use.
	want := map[string]any{"error": map[string]any{
		"code": "module_error", "message": "Deployment is a Business feature: https://olivares.ai/pricing",
	}}
	if !reflect.DeepEqual(got, want) {
		reg.t.Errorf("%s %s answered %v; want %v", method, pattern, got, want)
	}
}

func (reg *editionRegistrar) HandleEntity(method, pattern string, p auth.Permission, _ api.EntityRef, h api.ModuleHandler) {
	reg.Handle(method, pattern, p, h)
}

// TestCommunityDeployUnavailable pins that Community keeps every published route,
// method and permission, and refuses each one with the Business answer.
func TestCommunityDeployUnavailable(t *testing.T) {
	m := New()
	reg := &editionRegistrar{t: t}
	m.APIRoutes(reg)
	if !reflect.DeepEqual(reg.routes, publishedRoutes) {
		t.Errorf("registered routes = %v; want the published table %v", reg.routes, publishedRoutes)
	}
	declared := map[auth.Permission]bool{}
	for _, p := range m.Permissions() {
		declared[p] = true
	}
	for _, route := range publishedRoutes {
		if !declared[route.permission] {
			t.Errorf("%s %s needs %s, which the module does not declare", route.method, route.pattern, route.permission)
		}
	}
}
