// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !enterprise

package deploy

import (
	"context"
	"net/http"

	"github.com/olivaresai/olivares/core/api"
)

func (*Module) Start(context.Context) error { return nil }

// APIRoutes keeps the published routes and their permissions; every one answers 501
// and names Business. The implementation is private Business source.
func (m *Module) APIRoutes(reg api.RouteRegistrar) {
	reg.Handle("GET", "/definitions", permDeploymentRead, m.unavailable)
	reg.Handle("POST", "/definitions", permDeploymentWrite, m.unavailable)
	reg.Handle("GET", "/definitions/{id}", permDeploymentRead, m.unavailable)
	reg.Handle("PUT", "/definitions/{id}", permDeploymentWrite, m.unavailable)
	reg.Handle("DELETE", "/definitions/{id}", permDeploymentWrite, m.unavailable)
	reg.Handle("GET", "/definitions/{id}/revisions", permDeploymentRead, m.unavailable)
	reg.Handle("POST", "/definitions/{id}/rollback", permDeploymentWrite, m.unavailable)
	reg.Handle("POST", "/definitions/{id}/plan", permDeploymentWrite, m.unavailable)
	reg.Handle("POST", "/definitions/{id}/verify", permDeploymentWrite, m.unavailable)
	reg.Handle("POST", "/definitions/{id}/apply", permDeploymentAdmin, m.unavailable)
	reg.Handle("POST", "/definitions/{id}/retire", permDeploymentAdmin, m.unavailable)
	reg.Handle("GET", "/wirings", permWiringRead, m.unavailable)
	reg.Handle("GET", "/operations", permDeploymentRead, m.unavailable)
	reg.Handle("GET", "/executor", permDeploymentRead, m.unavailable)
	reg.Handle("GET", "/executor/config", permDeploymentAdmin, m.unavailable)
	reg.Handle("PUT", "/executor/config", permDeploymentAdmin, m.unavailable)
	reg.Handle("POST", "/executor/test", permDeploymentAdmin, m.unavailable)
}

func (*Module) unavailable(w http.ResponseWriter, _ *http.Request, _ api.ModuleContext) {
	api.WriteJSON(w, http.StatusNotImplemented, api.ModuleErrorBody("Deployment is a Business feature: https://olivares.ai/pricing"))
}
