//go:build !enterprise

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"net/http"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
)

// Tool version rings (a candidate release tried on one agent group before it
// reaches every folder) are a Business capability. Community keeps the published
// routes and answers 501; its version resolution reads folder and agent pins only.

// toolRingsBusinessRequired returns HTTP 501 with business_required because tool version rings require Business; no ring is read or changed.
func toolRingsBusinessRequired(w http.ResponseWriter, _ *http.Request, _ api.ModuleContext) {
	writeJSON(w, http.StatusNotImplemented, api.ErrorBody("business_required", "This capability requires Olivares Business."))
}

func (m *Module) registerToolRingRoutes(reg api.RouteRegistrar) {
	reg.Handle("GET", "/tool-rings", permRunAdmin, toolRingsBusinessRequired)
	reg.Handle("PUT", "/tool-rings/{driver}", permRunAdmin, toolRingsBusinessRequired)
	reg.Handle("POST", "/tool-rings/{driver}/promote", permRunAdmin, toolRingsBusinessRequired)
	reg.Handle("POST", "/tool-rings/{driver}/rollback", permRunAdmin, toolRingsBusinessRequired)
}

// ringToolVersion is the Business hook of ToolVersion: the candidate version a
// ring gives this agent, or "" when none applies. Community has no rings.
func (m *Module) ringToolVersion(context.Context, model.TenantID, string, string, string) (string, error) {
	return "", nil
}
