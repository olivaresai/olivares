// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !enterprise

package redteam

import (
	"context"
	"net/http"

	"github.com/olivaresai/olivares/core/api"
)

func (*Module) Start(context.Context) error { return nil }

func (m *Module) APIRoutes(reg api.RouteRegistrar) {

	// The battery catalog (the test taxonomy — metadata, NOT weaponized payloads).
	reg.Handle("GET", "/catalog", permRunRead, m.unavailable)

	// Targets: the CONSENT surface. Registering and authorizing a target are
	// admin-tier (granting permission to test) and audited (docs/SECURITY-HARDENING.md).
	reg.Handle("GET", "/targets", permTargetRead, m.unavailable)
	reg.Handle("POST", "/targets", permTargetAdmin, m.unavailable)
	reg.Handle("GET", "/targets/{id}", permTargetRead, m.unavailable)
	reg.Handle("POST", "/targets/{id}/authorize", permTargetAdmin, m.unavailable)

	// Runs: launching a run is the privileged adversarial action (admin-tier,
	// audited), and only against an AUTHORIZED target (docs/SECURITY-HARDENING.md).
	reg.Handle("GET", "/runs", permRunRead, m.unavailable)
	reg.Handle("POST", "/runs", permScanAdmin, m.unavailable)
	reg.Handle("GET", "/runs/{id}", permRunRead, m.unavailable)
	reg.Handle("GET", "/runs/{id}/results", permRunRead, m.unavailable)
}

func (*Module) unavailable(w http.ResponseWriter, _ *http.Request, _ api.ModuleContext) {
	api.WriteJSON(w, http.StatusNotImplemented, map[string]string{"error": "Red team is a Business feature: https://olivares.ai/pricing"})
}
