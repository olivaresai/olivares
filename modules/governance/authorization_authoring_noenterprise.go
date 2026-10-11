//go:build !enterprise

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"net/http"

	"github.com/olivaresai/olivares/core/api"
)

func authorizationRequiresBusiness(w http.ResponseWriter) {
	writeJSON(w, http.StatusNotImplemented, errorBody("Editing custom policies, roles and grants requires Business."))
}

func (m *Module) handleCreateCustomRole(w http.ResponseWriter, _ *http.Request, _ api.ModuleContext) {
	authorizationRequiresBusiness(w)
}

func (m *Module) handleUpdateCustomRole(w http.ResponseWriter, _ *http.Request, _ api.ModuleContext) {
	authorizationRequiresBusiness(w)
}

func (m *Module) handleCreatePermGroup(w http.ResponseWriter, _ *http.Request, _ api.ModuleContext) {
	authorizationRequiresBusiness(w)
}

func (m *Module) handleUpdatePermGroup(w http.ResponseWriter, _ *http.Request, _ api.ModuleContext) {
	authorizationRequiresBusiness(w)
}

func (m *Module) handleCreateScopedGrant(w http.ResponseWriter, _ *http.Request, _ api.ModuleContext) {
	authorizationRequiresBusiness(w)
}

func (m *Module) handleCedarPublish(w http.ResponseWriter, _ *http.Request, _ api.ModuleContext, _ pdpEngineSourceBody) {
	authorizationRequiresBusiness(w)
}

func (m *Module) handleCedarRollback(w http.ResponseWriter, _ *http.Request, _ api.ModuleContext, _ pdpRollbackBody) {
	authorizationRequiresBusiness(w)
}
