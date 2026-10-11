//go:build !enterprise

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"net/http"

	"github.com/olivaresai/olivares/core/api"
)

func businessRequired(w http.ResponseWriter, _ *http.Request, _ api.ModuleContext) {
	writeJSON(w, http.StatusNotImplemented, errorBodyCode("business_required", "This capability requires Olivares Business."))
}

func (m *Module) registerBreakGlassRoutes(reg api.RouteRegistrar) {
	reg.Handle("GET", "/breakglass", permBreakGlassRead, businessRequired)
	reg.Handle("POST", "/breakglass", permBreakGlassAdmin, businessRequired)
	reg.Handle("POST", "/breakglass/consume", permBreakGlassUse, businessRequired)
	reg.Handle("GET", "/breakglass/{id}", permBreakGlassRead, businessRequired)
	reg.Handle("GET", "/breakglass/{id}/uses", permBreakGlassRead, businessRequired)
	reg.Handle("POST", "/breakglass/{id}/revoke", permBreakGlassAdmin, businessRequired)
	reg.Handle("POST", "/breakglass/{id}/review", permBreakGlassAdmin, businessRequired)
}

func (m *Module) consumeEmergency(context.Context, approvalData, api.ModuleContext, consumeBreakGlassRequest) (EmergencyConsumption, error) {
	return EmergencyConsumption{}, nil
}

func (m *Module) sweepBreakGlass(http.ResponseWriter, *http.Request, api.ModuleContext) (int, error) {
	return 0, nil
}
