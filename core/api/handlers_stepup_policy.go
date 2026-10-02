// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"errors"
	"net/http"

	"github.com/olivaresai/olivares/core/auth"
)

type stepUpPolicyInput struct {
	AdminStepUp *string `json:"admin_step_up"`
}

// handleStepUpPolicyGet reads what administrative actions demand beyond the
// sign-in (auth.StepUpSatisfied): none (the default), totp or passkey.
func (s *Server) handleStepUpPolicyGet(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authzSystem(w, r, "system:admin"); !ok {
		return
	}
	policy, err := s.authr.AdminStepUp(r.Context())
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"admin_step_up": policy})
}

// handleStepUpPolicyPut sets the policy. It is deliberately NOT step-up gated:
// lowering or turning the policy off always works at the caller's current
// strength, and raising it refuses unless this session already meets the new
// level (auth.SetAdminStepUp), so no administrator can lock themselves out.
func (s *Server) handleStepUpPolicyPut(w http.ResponseWriter, r *http.Request) {
	p, ok := s.authzSystem(w, r, "system:admin")
	if !ok {
		return
	}
	var in stepUpPolicyInput
	if err := decodeJSON(w, r, &in); err != nil || in.AdminStepUp == nil || !auth.ValidStepUpPolicy(*in.AdminStepUp) {
		s.badRequest(w, r, "invalid JSON body: expected {admin_step_up: \"none\" | \"totp\" | \"passkey\"}")
		return
	}
	err := s.authr.SetAdminStepUp(r.Context(), p, *in.AdminStepUp)
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, map[string]any{"admin_step_up": *in.AdminStepUp})
	case errors.Is(err, auth.ErrStepUpPasskeyUnproven):
		// The console answers step_up_required with the passkey ceremony, which
		// also proves this console address can use passkeys; then it retries.
		s.writeError(w, r, auth.ErrStepUpRequired)
	case errors.Is(err, auth.ErrStepUpPasskeyMissing):
		writeAPIError(w, http.StatusConflict, "passkey_not_enrolled", "Add a passkey to your account first, then turn this on.")
	case errors.Is(err, auth.ErrStepUpTOTPUnproven):
		writeAPIError(w, http.StatusConflict, "totp_sign_in_required",
			"Set up an authenticator app and sign in with its code first, then turn this on.")
	default:
		s.writeError(w, r, err)
	}
}

// writeAPIError writes the standard error envelope with a fixed code and message.
func writeAPIError(w http.ResponseWriter, status int, code, msg string) {
	var body errorBody
	body.Error.Code = code
	body.Error.Message = msg
	writeJSON(w, status, body)
}
