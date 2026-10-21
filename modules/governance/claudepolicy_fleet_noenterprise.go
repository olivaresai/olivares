//go:build !enterprise

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"crypto/ed25519"
	"net/http"

	"github.com/olivaresai/olivares/core/api"
)

// Fleet-wide Claude Code managed settings (publish, revision history, signed pull,
// attested check-in, drift) are Business. Community mounts the same routes with the
// same permissions and answers 501, so a published URL never turns into a 404.

// fleetManagedSettingsRequireBusiness returns HTTP 501 because fleet-wide Claude Code managed settings are a Business feature; no publish, history, pull, check-in or drift action is performed.
func fleetManagedSettingsRequireBusiness(w http.ResponseWriter, _ *http.Request, _ api.ModuleContext) {
	writeJSON(w, http.StatusNotImplemented, errorBody("Fleet-wide managed settings (publish, signed pull, attested check-in and drift) require Business."))
}

// fleetInventoryRequireBusiness returns HTTP 501 because the workstation tool inventory is a Business feature; no workstation is listed and no report is stored.
func fleetInventoryRequireBusiness(w http.ResponseWriter, _ *http.Request, _ api.ModuleContext) {
	writeJSON(w, http.StatusNotImplemented, errorBody("The workstation tool inventory (installed AI tools, versions and sign-in state per workstation) requires Business."))
}

func (c *PolicyConsole) registerFleetRoutes(reg api.RouteRegistrar) {
	reg.Handle("POST", "/{surface}/publish", permClaudePolicyAdmin, fleetManagedSettingsRequireBusiness)
	reg.Handle("GET", "/{surface}/versions", permClaudePolicyRead, fleetManagedSettingsRequireBusiness)
	reg.Handle("GET", "/{surface}/versions/{revision}", permClaudePolicyRead, fleetManagedSettingsRequireBusiness)
	reg.Handle("GET", "/{surface}/artifact", permClaudePolicyRead, fleetManagedSettingsRequireBusiness)
	reg.Handle("POST", "/{surface}/checkin", permClaudePolicyWrite, fleetManagedSettingsRequireBusiness)
	reg.Handle("GET", "/{surface}/distribution", permClaudePolicyRead, fleetManagedSettingsRequireBusiness)
	reg.Handle("GET", "/inventory", permClaudePolicyRead, fleetInventoryRequireBusiness)
}

// FleetPolicyOptions: Community has no distribution seam and no observed-config store.
func FleetPolicyOptions(ed25519.PrivateKey) ([]PolicyConsoleOption, func(api.ModuleData)) {
	return nil, nil
}
