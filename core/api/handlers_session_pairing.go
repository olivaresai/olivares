// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"errors"
	"net/http"

	"github.com/olivaresai/olivares/core/auth"
)

func (s *Server) handleSessionPairing(w http.ResponseWriter, r *http.Request, mc ModuleContext) {
	if !s.requireStepUp(w, r, mc.Principal) {
		return
	}
	var input struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		s.badRequest(w, r, RequestBodyErrorMessage(err, "invalid JSON body"))
		return
	}
	token, session, err := s.authr.CreateSessionPairing(r.Context(), mc.Principal, input.Name)
	if errors.Is(err, auth.ErrPairingName) || errors.Is(err, auth.ErrPairingLimit) {
		s.badRequest(w, r, err.Error())
		return
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"token": token, "expires_at": session.ExpiresAt.String()})
}

func (s *Server) handleSessionPairingAccept(w http.ResponseWriter, r *http.Request, mc ModuleContext) {
	if !BrowserSameOrigin(r) {
		s.writeError(w, r, errForbidden)
		return
	}
	var input struct {
		Token string `json:"token"`
	}
	if err := decodeJSON(w, r, &input); err != nil {
		s.badRequest(w, r, RequestBodyErrorMessage(err, "invalid JSON body"))
		return
	}
	token, session, err := s.authr.AcceptSessionPairing(r.Context(), input.Token, clientIP(r))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, SessionEnvelope(w, r, token, session))
}
