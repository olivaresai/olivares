// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/olivaresai/olivares/core/auth"
)

// handleTokenExchange implements the RFC 8693 token-exchange endpoint at
// POST /v1/auth/token-exchange. It speaks the OAuth wire contract
// (application/x-www-form-urlencoded request, OAuth JSON response/error,
// Cache-Control: no-store), NOT the core API envelope, so a standard OAuth client
// can drive it. The caller must itself be authenticated (the bearer that
// authorizes calling the endpoint); the authority to down-scope comes from
// possession of the subject_token, which can only ever yield a lesser token.
func (s *Server) handleTokenExchange(w http.ResponseWriter, r *http.Request) {
	caller, ok := principalFrom(r.Context())
	if !ok {
		// invalid_client is the RFC 6749 §5.2 code for a failed client auth (401).
		writeOAuthError(w, http.StatusUnauthorized, "invalid_client", "authentication required")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	if err := r.ParseForm(); err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "malformed form body")
		return
	}
	if gt := r.PostForm.Get("grant_type"); gt != auth.GrantTypeTokenExchange {
		writeOAuthError(w, http.StatusBadRequest, "unsupported_grant_type",
			"grant_type must be "+auth.GrantTypeTokenExchange)
		return
	}

	req := auth.ExchangeRequest{
		SubjectToken:       r.PostForm.Get("subject_token"),
		SubjectTokenType:   r.PostForm.Get("subject_token_type"),
		ActorToken:         r.PostForm.Get("actor_token"),
		ActorTokenType:     r.PostForm.Get("actor_token_type"),
		Resources:          r.PostForm["resource"], // repeated param (RFC 8707) — read ALL values
		Audiences:          r.PostForm["audience"], // repeated param (RFC 8693)
		Scope:              strings.Fields(r.PostForm.Get("scope")),
		RequestedTokenType: r.PostForm.Get("requested_token_type"),
		Name:               r.PostForm.Get("name"),
		RequestedActorRef:  r.PostForm.Get("requested_actor"),
	}

	res, err := s.authr.ExchangeToken(r.Context(), caller, req)
	if err != nil {
		status, code, internal := classifyExchangeError(err)
		if internal {
			s.log.Error("api: token-exchange failed", "err", err, "request_id", requestID(r.Context()))
			writeOAuthError(w, status, code, "internal error")
			return
		}
		writeOAuthError(w, status, code, err.Error())
		return
	}

	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	body := map[string]any{
		"access_token":      res.AccessToken,
		"issued_token_type": res.IssuedTokenType,
		"token_type":        res.TokenType,
		"expires_in":        res.ExpiresIn,
	}
	if len(res.Scope) > 0 {
		body["scope"] = strings.Join(res.Scope, " ")
	}
	writeJSON(w, http.StatusOK, body)
}

// classifyExchangeError maps a token-exchange error to an HTTP status, an OAuth
// error code (RFC 6749 §5.2 + RFC 8693 invalid_target), and whether it is an
// internal fault (so the handler logs it and hides the detail). All client-facing
// token-endpoint errors are HTTP 400 by the OAuth contract.
func classifyExchangeError(err error) (status int, code string, internal bool) {
	switch {
	case errors.Is(err, auth.ErrInvalidTarget):
		return http.StatusBadRequest, "invalid_target", false
	case errors.Is(err, auth.ErrInvalidSubjectToken),
		errors.Is(err, auth.ErrInvalidExchange),
		errors.Is(err, auth.ErrRoleCeiling):
		return http.StatusBadRequest, "invalid_request", false
	case errors.Is(err, auth.ErrAgentBlocked):
		return http.StatusForbidden, "agent_blocked", false
	case errors.Is(err, auth.ErrWorkspaceConfined):
		// a workspace-confined subject/actor cannot be exchanged into an unconfined
		// token — a deliberate deny (403), never an internal fault.
		return http.StatusForbidden, "access_denied", false
	default:
		return http.StatusInternalServerError, "server_error", true
	}
}

// writeOAuthError writes an RFC 6749 §5.2 OAuth error JSON body (NOT the core API
// envelope) with no-store caching, as RFC 8693 clients expect.
func writeOAuthError(w http.ResponseWriter, status int, code, desc string) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	writeJSON(w, status, map[string]string{"error": code, "error_description": desc})
}
