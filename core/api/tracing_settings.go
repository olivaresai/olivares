// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/olivaresai/olivares/core/auth"
	obstrace "github.com/olivaresai/olivares/core/observability/trace"
)

type TracingStatus struct {
	Settings  obstrace.Settings `json:"settings"`
	Effective obstrace.Settings `json:"effective"`
	Overrides []string          `json:"overrides"`
}

type TracingSettingsService interface {
	TracingSettings(context.Context) (TracingStatus, error)
	SaveTracingSettings(context.Context, auth.Principal, obstrace.Settings) (TracingStatus, error)
}

var ErrTracingUnavailable = errors.New("tracing settings are unavailable")

// handleTracingSettings reads or saves deployment tracing choices for a system administrator. Writes require the configured step-up and apply to the running provider without restarting sessions.
func (s *Server) handleTracingSettings(w http.ResponseWriter, r *http.Request, mc ModuleContext) {
	actor := mc.Principal
	if r.Method == http.MethodPut && !s.requireStepUp(w, r, actor) {
		return
	}
	if s.tracingSettings == nil {
		s.writeError(w, r, ErrTracingUnavailable)
		return
	}
	var dto TracingStatus
	var err error
	if r.Method == http.MethodPut {
		var in obstrace.Settings
		if err = decodeJSON(w, r, &in); err != nil {
			s.badRequest(w, r, "invalid tracing settings")
			return
		}
		if err = in.Validate(); err != nil {
			s.badRequest(w, r, err.Error())
			return
		}
		dto, err = s.tracingSettings.SaveTracingSettings(r.Context(), actor, in)
	} else {
		dto, err = s.tracingSettings.TracingSettings(r.Context())
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, dto)
}
