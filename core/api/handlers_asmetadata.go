// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"net/http"

	"github.com/olivaresai/olivares/core/auth"
)

const (
	// OAuthAuthorizationServerMetadataPath is the RFC 8414 authorization-server
	// metadata document. It is public and setup-exempt so an EMA-capable MCP client
	// can discover the ID-JAG grant profile before it has a token.
	OAuthAuthorizationServerMetadataPath = "/.well-known/oauth-authorization-server"

	idJAGGrantProfile = "urn:ietf:params:oauth:grant-profile:id-jag"
)

type authorizationServerMetadata struct {
	Issuer                              string   `json:"issuer"`
	TokenEndpoint                       string   `json:"token_endpoint"`
	GrantTypesSupported                 []string `json:"grant_types_supported,omitempty"`
	TokenEndpointAuthMethodsSupported   []string `json:"token_endpoint_auth_methods_supported"`
	ResponseTypesSupported              []string `json:"response_types_supported"`
	AuthorizationGrantProfilesSupported []string `json:"authorization_grant_profiles_supported,omitempty"`
}

func (s *Server) handleAuthorizationServerMetadata(w http.ResponseWriter, r *http.Request) {
	base := schemeHost(r)
	doc := authorizationServerMetadata{
		Issuer:                            base,
		TokenEndpoint:                     base + "/v1/auth/token",
		TokenEndpointAuthMethodsSupported: []string{"client_secret_basic"},
		ResponseTypesSupported:            []string{},
	}
	if s.emaGrant != nil {
		// The metadata issuer is the SAME value the grant handler mints under
		// (EMAGrant.Issuer) — discovery and minting can never disagree.
		if issuer := s.emaGrant.Issuer(); issuer != "" {
			doc.Issuer = issuer
		}
		doc.GrantTypesSupported = []string{auth.GrantTypeJWTBearer}
		doc.AuthorizationGrantProfilesSupported = []string{idJAGGrantProfile}
	}
	writeJSON(w, http.StatusOK, doc)
}
