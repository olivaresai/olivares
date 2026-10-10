// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !enterprise

package postureexport

import (
	"net/http"

	"github.com/olivaresai/olivares/core/api"
)

func (*Module) handleExport(w http.ResponseWriter, _ *http.Request, _ api.ModuleContext) {
	api.WriteJSON(w, http.StatusNotImplemented, api.ErrorBody("posture_export_unavailable", "Posture export requires Business"), "application/json")
}
