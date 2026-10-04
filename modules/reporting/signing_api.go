// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package reporting

import (
	"errors"
	"net/http"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/license"
)

const signingNotIncluded = "Report signing is part of Business; this edition does not include it."

func (m *Module) registerSigningRoutes(reg api.RouteRegistrar) {
	// This key signs reports across the deployment. A tenant role cannot change it.
	if system, ok := reg.(api.SystemRouteRegistrar); ok {
		system.HandleSystem("GET", "/signing", m.handleSigningStatus)
		system.HandleSystem("PUT", "/signing", m.handleSetSigning)
	}
}

func (m *Module) OperationDocumentation(method, pattern string) (api.ModuleOperationDocumentation, bool) {
	if method != http.MethodPut || pattern != "/signing" {
		return api.ModuleOperationDocumentation{}, false
	}
	return api.ModuleOperationDocumentation{
		BodyKind: api.ModuleOperationJSONBody,
		RequestBody: map[string]any{"required": true, "content": map[string]any{
			"application/json": map[string]any{"schema": map[string]any{
				"type": "object", "required": []string{"enabled"}, "additionalProperties": false,
				"properties": map[string]any{"enabled": map[string]any{"type": "boolean"}},
			}},
		}},
		SuccessResponses: map[string]any{"200": map[string]any{"description": "Signing setting applied in the engine"}},
	}, true
}

// handleSigningStatus reports whether evidence bundles are signed on this deployment: enabled, ready (and why not), the signing key's ID and public key, and where the key comes from. System administrators only.
func (m *Module) handleSigningStatus(w http.ResponseWriter, r *http.Request, _ api.ModuleContext) {
	manager, ok := m.enterprise.(SigningManager)
	if !ok {
		writeError(w, http.StatusNotImplemented, signingNotIncluded)
		return
	}
	status, err := manager.ReportingSigning(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "Report signing status is unavailable. Retry or check the engine log.")
		return
	}
	writeJSON(w, http.StatusOK, status)
}

// handleSetSigning turns evidence bundle signing on or off for the whole deployment and answers with the resulting signing status. Enabling signing needs a valid license covering reporting; system administrators only.
func (m *Module) handleSetSigning(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var input struct {
		Enabled *bool `json:"enabled"`
	}
	if err := api.DecodeRequestBody(w, r, &input, api.RequestBodySpec{MaxBytes: 1024}); err != nil || input.Enabled == nil {
		message := "Choose whether report signing is enabled."
		if input.Enabled != nil && errors.Is(err, api.ErrTrailingJSON) {
			message = "Send one signing setting."
		}
		writeError(w, http.StatusBadRequest, message)
		return
	}
	manager, ok := m.enterprise.(SigningManager)
	if !ok {
		writeError(w, http.StatusNotImplemented, signingNotIncluded)
		return
	}
	status, err := manager.SetReportingSigning(r.Context(), mc.Principal, *input.Enabled)
	if err != nil {
		if errors.Is(err, license.ErrAddonRequiresLicense) {
			writeJSON(w, http.StatusForbidden, map[string]any{"error": map[string]string{"code": "addon_requires_license", "message": err.Error()}})
			return
		}
		if m.log != nil {
			m.log.Warn("report signing setting was not applied", "err", err)
		}
		writeError(w, http.StatusServiceUnavailable, "Report signing could not be updated. Retry or check the engine log.")
		return
	}
	writeJSON(w, http.StatusOK, status)
}
