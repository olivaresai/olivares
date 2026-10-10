// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/olivaresai/olivares/core/auth"
)

func (s *Server) handleAccountPassword(w http.ResponseWriter, r *http.Request, mc ModuleContext) {
	p := mc.Principal
	if p.Kind != auth.KindUser {
		s.forbidden(w, r, "Sign in with your account to change your password.")
		return
	}
	var in struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		s.badRequest(w, r, RequestBodyErrorMessage(err, "Enter your current and new passwords."))
		return
	}
	err := s.authr.ChangeOwnPassword(r.Context(), p, in.CurrentPassword, in.NewPassword, clientIP(r), r.Header.Values("X-Forwarded-For"))
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, auth.ErrCurrentPasswordIncorrect), errors.Is(err, auth.ErrNoLocalPassword):
		s.badRequest(w, r, err.Error())
	case errors.Is(err, auth.ErrWeakPassword):
		s.badRequest(w, r, fmt.Sprintf("Choose a password of at least %d characters.", auth.MinPasswordLen))
	case errors.Is(err, auth.ErrLockedOut):
		var body errorBody
		body.Error.Code, body.Error.Message = "locked_out", "Too many attempts. Try again later."
		writeJSON(w, http.StatusTooManyRequests, body)
	default:
		s.writeError(w, r, err)
	}
}
