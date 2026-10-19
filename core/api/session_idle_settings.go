// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/olivaresai/olivares/core/auth"
)

// SessionIdleSettings is the single instance-wide idle-stop policy. Zero is off.
// Enabled values count minutes after the runtime's five-minute idle projection.
type SessionIdleSettings struct {
	IdleStopMinutes int `json:"idle_stop_minutes"`
}

func (s SessionIdleSettings) Validate() error {
	if s.IdleStopMinutes != 0 && (s.IdleStopMinutes <= 5 || s.IdleStopMinutes > 10080) {
		return errors.New("idle_stop_minutes must be 0 (off) or between 6 and 10080")
	}
	return nil
}

type SessionIdleSettingsService interface {
	SessionIdleSettings(context.Context) (SessionIdleSettings, error)
	SaveSessionIdleSettings(context.Context, auth.Principal, SessionIdleSettings) (SessionIdleSettings, error)
}

var ErrSessionIdleSettingsUnavailable = errors.New("session idle settings are unavailable")

func (s *Server) handleSessionIdleSettings(w http.ResponseWriter, r *http.Request, mc ModuleContext) {
	if r.Method == http.MethodPut && !s.requireStepUp(w, r, mc.Principal) {
		return
	}
	if s.sessionIdleSettings == nil {
		s.writeError(w, r, ErrSessionIdleSettingsUnavailable)
		return
	}
	var dto SessionIdleSettings
	var err error
	if r.Method == http.MethodPut {
		// The field is required: an empty body must not silently turn the policy off.
		var in struct {
			IdleStopMinutes *int `json:"idle_stop_minutes"`
		}
		if err = decodeJSON(w, r, &in); err != nil || in.IdleStopMinutes == nil {
			s.badRequest(w, r, "invalid session idle settings: idle_stop_minutes is required")
			return
		}
		dto.IdleStopMinutes = *in.IdleStopMinutes
		if err = dto.Validate(); err != nil {
			s.badRequest(w, r, err.Error())
			return
		}
		dto, err = s.sessionIdleSettings.SaveSessionIdleSettings(r.Context(), mc.Principal, dto)
	} else {
		dto, err = s.sessionIdleSettings.SessionIdleSettings(r.Context())
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, dto)
}
