// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	jose "github.com/go-jose/go-jose/v4"
)

// cimd.go implements the client half of OAuth Client ID Metadata Documents (CIMD,
// draft-ietf-oauth-client-id-metadata-document; MCP SEP-991 — the RECOMMENDED client
// registration mechanism since 2025-11-25, with RFC 7591 DCR deprecated in its favor
// by the 2026-07-28 RC):
//
//   - validateClientIDMetadataURL enforces the draft §3 rules on the URL this client
//     presents as its client_id;
//   - ClientMetadataDocument + NewClientMetadataDocument build the JSON document the
//     plane hosts at that URL, enforcing the draft's hard rules at construction
//     (client_id == document URL by simple string comparison; NO shared-symmetric
//     client authentication method; no client_secret fields exist on the type at
//     all); the document is the plane's PUBLIC client identity — it carries public
//     keys only, never a private key.
//
// The AS-side obligations (fetching, caching, SSRF, redirect-URI matching) belong to
// the authorization server, not to this connector; the IAM posture contract
// spells out that division.

// validateClientIDMetadataURL enforces the CIMD draft §3 client_id URL rules: https
// scheme, a host, a NON-EMPTY path component, no single-dot/double-dot path
// segments, no fragment, no username/password. The draft's "SHOULD NOT include a
// query string" is enforced as a refusal too — this is the plane's OWN identity URL,
// and there is no legitimate reason for it to carry a query.
func validateClientIDMetadataURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("mcp: cimd: invalid client_id URL %q: %w", raw, err)
	}
	switch {
	case u.Scheme != "https":
		return fmt.Errorf("mcp: cimd: client_id URL %q must use the https scheme", raw)
	case u.Host == "":
		return fmt.Errorf("mcp: cimd: client_id URL %q has no host", raw)
	case u.Path == "" || u.Path == "/":
		return fmt.Errorf("mcp: cimd: client_id URL %q must contain a path component", raw)
	case u.Fragment != "" || strings.Contains(raw, "#"):
		return fmt.Errorf("mcp: cimd: client_id URL %q must not contain a fragment", raw)
	case u.User != nil:
		return fmt.Errorf("mcp: cimd: client_id URL %q must not contain a username or password", raw)
	case u.RawQuery != "":
		return fmt.Errorf("mcp: cimd: client_id URL %q must not contain a query string", raw)
	}
	for _, seg := range strings.Split(u.Path, "/") {
		if seg == "." || seg == ".." {
			return fmt.Errorf("mcp: cimd: client_id URL %q must not contain dot path segments", raw)
		}
	}
	return nil
}

// ClientMetadataDocument is the CIMD JSON document (draft §4.1: values from the IANA
// OAuth Dynamic Client Registration Metadata registry). There are deliberately NO
// client_secret / client_secret_expires_at fields: the draft forbids them, so they
// are unrepresentable here. JWKS, when set, publishes the client's PUBLIC keys and
// makes it a confidential private_key_jwt client (draft §6.2).
type ClientMetadataDocument struct {
	ClientID                string              `json:"client_id"`
	ClientName              string              `json:"client_name"`
	RedirectURIs            []string            `json:"redirect_uris"`
	ClientURI               string              `json:"client_uri,omitempty"`
	GrantTypes              []string            `json:"grant_types,omitempty"`
	ResponseTypes           []string            `json:"response_types,omitempty"`
	TokenEndpointAuthMethod string              `json:"token_endpoint_auth_method,omitempty"`
	JWKS                    *jose.JSONWebKeySet `json:"jwks,omitempty"`
}

// forbiddenCIMDAuthMethods are the shared-symmetric-secret client authentication
// methods the CIMD draft forbids (§4.1: a document URL is public, so a secret
// registered through it could never be confidential).
var forbiddenCIMDAuthMethods = map[string]struct{}{
	"client_secret_post":  {},
	"client_secret_basic": {},
	"client_secret_jwt":   {},
}

// NewClientMetadataDocument builds the plane's CIMD document, enforcing the draft's
// construction rules fail-closed:
//
//   - documentURL passes the §3 client_id URL rules and becomes client_id verbatim
//     (the §4.1 "MUST match the URL using simple string comparison" is true by
//     construction);
//   - client_name and at least one redirect_uri are required (the MCP profile's
//     minimum document);
//   - the token_endpoint_auth_method must not be a shared-symmetric method; setting
//     private_key_jwt requires a published key set, and any published key MUST be
//     public (a private key in a CIMD document would be a credential leak).
func NewClientMetadataDocument(documentURL, name string, redirectURIs []string, authMethod string, keys *jose.JSONWebKeySet) (ClientMetadataDocument, error) {
	if err := validateClientIDMetadataURL(documentURL); err != nil {
		return ClientMetadataDocument{}, err
	}
	if strings.TrimSpace(name) == "" {
		return ClientMetadataDocument{}, fmt.Errorf("mcp: cimd: client_name is required")
	}
	if len(redirectURIs) == 0 {
		return ClientMetadataDocument{}, fmt.Errorf("mcp: cimd: at least one redirect_uri is required (the MCP CIMD minimum document)")
	}
	authMethod = strings.TrimSpace(authMethod)
	if _, forbidden := forbiddenCIMDAuthMethods[authMethod]; forbidden || (authMethod != "" && authMethod != "none" && authMethod != "private_key_jwt") {
		return ClientMetadataDocument{}, fmt.Errorf("mcp: cimd: token_endpoint_auth_method %q is not allowed in a client metadata document (no shared-symmetric secrets)", authMethod)
	}
	if authMethod == "private_key_jwt" && (keys == nil || len(keys.Keys) == 0) {
		return ClientMetadataDocument{}, fmt.Errorf("mcp: cimd: private_key_jwt requires a published jwks")
	}
	if keys != nil {
		for _, k := range keys.Keys {
			if !k.IsPublic() {
				return ClientMetadataDocument{}, fmt.Errorf("mcp: cimd: the published jwks must contain PUBLIC keys only (kid %q is private)", k.KeyID)
			}
		}
	}
	return ClientMetadataDocument{
		ClientID:                documentURL,
		ClientName:              strings.TrimSpace(name),
		RedirectURIs:            append([]string(nil), redirectURIs...),
		TokenEndpointAuthMethod: authMethod,
		JWKS:                    keys,
	}, nil
}

// ServeHTTP serves the document (GET-only, application/json) so the composition root
// can mount the plane's client identity at its HTTPS client_id URL. The modest
// Cache-Control bounds how long an AS may cache a rotated identity (the draft lets
// the AS respect HTTP cache headers).
func (d ClientMetadataDocument) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "max-age=300")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(d)
}
