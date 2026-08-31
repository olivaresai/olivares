// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"net/http"
	"strings"

	"github.com/olivaresai/olivares/core/auth"
)

// handleJWTBearerGrant implements the EMA jwt-bearer grant at POST /v1/auth/token.
// It speaks the OAuth wire contract (application/x-www-form-urlencoded request,
// RFC 6749 §5.1/§5.2 JSON response/error), so a standard MCP client can drive it.
//
// The flow: the MCP client presents an ID-JAG assertion from its enterprise IdP,
// authenticates itself (HTTP Basic or client_assertion), and receives an
// audience-bound at+jwt access token for the target MCP server.
//
// DENY-CLOSED: a nil EMAGrant means the feature is not configured — every request
// returns unsupported_grant_type. The handler validates:
//   - grant_type MUST be urn:ietf:params:oauth:grant-type:jwt-bearer
//   - assertion MUST be present (the ID-JAG JWT)
//   - resource MUST be present (the target MCP server URI, RFC 8707)
//   - client authentication MUST succeed (HTTP Basic required)
func (s *Server) handleJWTBearerGrant(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	if err := r.ParseForm(); err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "malformed form body")
		return
	}

	gt := r.PostForm.Get("grant_type")
	if gt != auth.GrantTypeJWTBearer {
		writeOAuthError(w, http.StatusBadRequest, "unsupported_grant_type",
			"grant_type must be "+auth.GrantTypeJWTBearer)
		return
	}

	if s.emaGrant == nil {
		writeOAuthError(w, http.StatusBadRequest, "unsupported_grant_type",
			"EMA jwt-bearer grant is not configured on this server")
		return
	}

	// Client authentication: HTTP Basic (client_id:client_secret).
	clientID, _, ok := r.BasicAuth()
	if !ok || strings.TrimSpace(clientID) == "" {
		writeOAuthError(w, http.StatusUnauthorized, "invalid_client",
			"client authentication required (HTTP Basic)")
		return
	}

	assertion := r.PostForm.Get("assertion")
	if strings.TrimSpace(assertion) == "" {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request",
			"assertion parameter is required (the ID-JAG JWT)")
		return
	}

	resource := r.PostForm.Get("resource")
	if strings.TrimSpace(resource) == "" {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request",
			"resource parameter is required (the target MCP server URI, RFC 8707)")
		return
	}

	requestedScopes := strings.Fields(r.PostForm.Get("scope"))

	res, err := s.emaGrant.Grant(r.Context(), assertion, clientID, resource, requestedScopes)
	if err != nil {
		status, code := classifyEMAError(err)
		writeOAuthError(w, status, code, err.Error())
		return
	}

	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	body := map[string]any{
		"access_token": res.AccessToken,
		"token_type":   res.TokenType,
		"expires_in":   res.ExpiresIn,
	}
	if res.Scope != "" {
		body["scope"] = res.Scope
	}
	writeJSON(w, http.StatusOK, body)
}

// classifyEMAError maps an EMA grant error to the appropriate OAuth error code
// and HTTP status. All EMA errors are client-facing (no internal fault hiding).
func classifyEMAError(err error) (int, string) {
	switch {
	case isEMAInvalidGrant(err):
		return http.StatusBadRequest, "invalid_grant"
	default:
		return http.StatusBadRequest, "invalid_request"
	}
}

// isEMAInvalidGrant reports whether err wraps the ID-JAG invalid_grant sentinel.
// Uses string comparison because the sentinel lives in the connector package
// (boundary: core does not import connectors). The EMAReceiver wraps its errors
// with the same "invalid_grant" text.
func isEMAInvalidGrant(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "invalid_grant")
}
