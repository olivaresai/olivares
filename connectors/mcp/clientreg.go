// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	jwt "github.com/go-jose/go-jose/v4/jwt"
)

// clientreg.go selects HOW this connector identifies itself as an OAuth client to an
// MCP server's authorization server. The MCP spec (2025-11-25, unchanged in
// the 2026-07-28 RC) defines the priority order a client supporting all options
// SHOULD follow:
//
//	1. pre-registered client information, when available;
//	2. CIMD — Client ID Metadata Documents (SEP-991, SHOULD) — when the AS
//	   advertises client_id_metadata_document_supported;
//	3. Dynamic Client Registration (RFC 7591, MAY — DEPRECATED in the RC in favor
//	   of CIMD, kept for back-compat) when the AS exposes a registration_endpoint;
//	4. prompt the user — this is a HEADLESS client, so step 4 is a loud error.
//
// Credentials are KEYED BY THE ISSUER that owns them (SEP-2352, MUST): a DCR
// registration or refresh token obtained from issuer A is never presented to issuer
// B, and pre-registered credentials pinned to an issuer fail loudly when discovery
// resolves a different one — never a silent cross-issuer reuse.

// Client identification methods, in spec priority order.
const (
	identityPreRegistered = "pre-registered"
	identityCIMD          = "cimd"
	identityDCR           = "dcr"
)

// clientAssertionTypeJWTBearer is the RFC 7523 §2.2 client_assertion_type for
// private_key_jwt client authentication.
const clientAssertionTypeJWTBearer = "urn:ietf:params:oauth:client-assertion-type:jwt-bearer"

// defaultApplicationType is the OIDC application_type this connector registers with
// when the operator does not override it (SEP-837 MUST send one): the control plane
// is a server-side service, not a desktop/CLI/localhost-hosted client.
const defaultApplicationType = "web"

// clientIdentity is the resolved OAuth client identification for ONE authorization
// server. assertionKey, when set, switches client authentication to private_key_jwt
// (the only method a CIMD identity may carry — shared secrets are forbidden there).
type clientIdentity struct {
	method       string
	clientID     string
	clientSecret string
	assertionKey *jose.JSONWebKey
}

// issuerCredentialStore holds the credentials this client OBTAINED at runtime (DCR
// registrations, refresh tokens), keyed strictly by the issuing AS's issuer value
// (SEP-2352). Lookups use simple string comparison — no normalization. It is
// in-memory per connector run: the connector never persists secrets (docs/SECURITY-HARDENING.md).
type issuerCredentialStore struct {
	mu            sync.Mutex
	registrations map[string]clientIdentity
	refreshTokens map[string]string
}

func newIssuerCredentialStore() *issuerCredentialStore {
	return &issuerCredentialStore{
		registrations: map[string]clientIdentity{},
		refreshTokens: map[string]string{},
	}
}

// registrationFor returns the DCR registration previously obtained FROM this issuer,
// if any. A registration stored under any other issuer is unreachable by key.
func (s *issuerCredentialStore) registrationFor(issuer string) (clientIdentity, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.registrations[issuer]
	return id, ok
}

// putRegistration records a DCR registration under the issuer that minted it.
func (s *issuerCredentialStore) putRegistration(issuer string, id clientIdentity) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.registrations[issuer] = id
}

// putRefreshToken records (or rotates) the refresh token issued by issuer.
func (s *issuerCredentialStore) putRefreshToken(issuer, token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshTokens[issuer] = token
}

// refreshTokenFor returns the refresh token issued by THIS issuer, if any.
func (s *issuerCredentialStore) refreshTokenFor(issuer string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.refreshTokens[issuer]
	return t, ok
}

// clientIdentityFor resolves the client identity for the DISCOVERED authorization
// server, in spec priority order. Deny-closed at every step: a pre-registered
// credential pinned to a different issuer, an invalid CIMD URL, or an exhausted
// option list is an error, never a silent downgrade.
func (c *oauthClient) clientIdentityFor(ctx context.Context, as authServerMetadata) (clientIdentity, error) {
	// 1. Pre-registered credentials. SEP-2352: when the operator pinned them to an
	// issuer, a different discovered issuer is a hard error (the spec's "MUST NOT
	// reuse credentials from a different authorization server", surfaced rather
	// than silently presented). Without an explicit pin the credentials bind to the
	// FIRST issuer discovery resolves in this run, and a LATER change of issuer
	// (e.g. between the initial grant and a step-up, via a swapped PRM) is refused
	// the same way — the operator is told to pin auth.issuer explicitly.
	if c.auth.ClientID != "" {
		pin := strings.TrimSpace(c.auth.Issuer)
		if pin == "" {
			pin = c.bindIssuer(as.Issuer)
		}
		if pin != as.Issuer {
			return clientIdentity{}, fmt.Errorf("mcp: oauth: pre-registered credentials are bound to issuer %q but discovery resolved %q (SEP-2352: no cross-issuer credential reuse; set auth.issuer to pin explicitly)", pin, as.Issuer)
		}
		key, err := c.assertionKey()
		if err != nil {
			return clientIdentity{}, err
		}
		c.recordRegistration(identityPreRegistered, as)
		return clientIdentity{
			method:       identityPreRegistered,
			clientID:     c.auth.ClientID,
			clientSecret: c.auth.ClientSecret,
			assertionKey: key,
		}, nil
	}

	// 2. CIMD (SEP-991): only when the AS advertises support AND the operator hosts
	// a metadata document for this client. The URL is the client_id, verbatim.
	if as.ClientIDMetadataSupported && c.auth.ClientIDMetadataURL != "" {
		cimdURL := strings.TrimSpace(c.auth.ClientIDMetadataURL)
		if err := validateClientIDMetadataURL(cimdURL); err != nil {
			return clientIdentity{}, err
		}
		key, err := c.assertionKey()
		if err != nil {
			return clientIdentity{}, err
		}
		c.recordRegistration(identityCIMD, as)
		return clientIdentity{
			method:       identityCIMD,
			clientID:     cimdURL,
			assertionKey: key,
		}, nil
	}

	// 3. DCR fallback (RFC 7591, deprecated in the RC): explicit operator opt-in,
	// reusing a registration previously obtained from THIS issuer (keyed lookup).
	if c.auth.DynamicRegistration && as.RegistrationEndpoint != "" {
		if id, ok := c.store.registrationFor(as.Issuer); ok {
			c.recordRegistration(identityDCR, as)
			return id, nil
		}
		id, err := c.registerClient(ctx, as)
		if err != nil {
			return clientIdentity{}, err
		}
		c.store.putRegistration(as.Issuer, id)
		c.recordRegistration(identityDCR, as)
		return id, nil
	}

	// 4. The spec's step 4 is "prompt the user" — a headless introspector fails loudly.
	return clientIdentity{}, fmt.Errorf("mcp: oauth: no client identification available for issuer %q (no pre-registered credentials; CIMD %s; DCR %s)", as.Issuer,
		cimdAvailability(as, c.auth), dcrAvailability(as, c.auth))
}

// assertionKey parses the operator-supplied private JWK for private_key_jwt.
// (nil, nil) means no key was configured; a key that is CONFIGURED but unusable is
// an ERROR, never a silent absence — treating it as absent would let an identity
// with no client_secret downgrade to an unauthenticated (empty-secret basic auth)
// token request.
func (c *oauthClient) assertionKey() (*jose.JSONWebKey, error) {
	raw := strings.TrimSpace(c.auth.ClientAssertionJWK)
	if raw == "" {
		return nil, nil
	}
	var k jose.JSONWebKey
	if err := json.Unmarshal([]byte(raw), &k); err != nil {
		return nil, fmt.Errorf("mcp: oauth: client_assertion_jwk is not a parseable JWK")
	}
	if !k.Valid() || k.IsPublic() {
		return nil, fmt.Errorf("mcp: oauth: client_assertion_jwk must be a valid PRIVATE key (got a public or invalid one)")
	}
	return &k, nil
}

// cimdAvailability/dcrAvailability render WHY an option was not used (operator-facing
// error detail; never secrets).
func cimdAvailability(as authServerMetadata, auth *serverAuth) string {
	switch {
	case !as.ClientIDMetadataSupported:
		return "not advertised by the AS"
	case auth.ClientIDMetadataURL == "":
		return "advertised but no client_id_metadata_url configured"
	default:
		return "available"
	}
}

func dcrAvailability(as authServerMetadata, auth *serverAuth) string {
	switch {
	case as.RegistrationEndpoint == "":
		return "no registration_endpoint"
	case !auth.DynamicRegistration:
		return "registration_endpoint present but dynamic_registration not opted in"
	default:
		return "available"
	}
}

// dcrRequest is the RFC 7591 §2 registration request this connector sends. SEP-837:
// application_type (an OIDC Dynamic Client Registration parameter registered in the
// IANA OAuth DCR metadata registry) MUST be specified; a non-OIDC AS safely ignores
// it (RFC 7591: unrecognized metadata is ignored).
type dcrRequest struct {
	ClientName              string   `json:"client_name"`
	ApplicationType         string   `json:"application_type"`
	GrantTypes              []string `json:"grant_types"`
	RedirectURIs            []string `json:"redirect_uris,omitempty"`
	ResponseTypes           []string `json:"response_types,omitempty"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
	Scope                   string   `json:"scope,omitempty"`
}

// dcrResponse is the RFC 7591 §3.2.1 response subset this connector uses.
type dcrResponse struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
}

// dcrError is the RFC 7591 §3.2.2 error response.
type dcrError struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

// registerClient performs Dynamic Client Registration at the AS (the deprecated
// fallback). SEP-837: the request always carries application_type, and a rejected
// registration surfaces the AS's error code/description as a MEANINGFUL error (the
// spec's redirect-URI-constraint failures land here) instead of a bare status code.
func (c *oauthClient) registerClient(ctx context.Context, as authServerMetadata) (clientIdentity, error) {
	if err := validateOutboundURL(ctx, as.RegistrationEndpoint); err != nil {
		return clientIdentity{}, err
	}
	appType := strings.TrimSpace(c.auth.ApplicationType)
	if appType == "" {
		appType = defaultApplicationType
	}
	reg := dcrRequest{
		ClientName:              clientName,
		ApplicationType:         appType,
		GrantTypes:              []string{"client_credentials"},
		TokenEndpointAuthMethod: "client_secret_basic",
	}
	if len(c.auth.RedirectURIs) > 0 {
		// A host driving the authorization-code flow registers its redirect URIs;
		// the automated client-credentials introspector registers none.
		reg.RedirectURIs = append([]string(nil), c.auth.RedirectURIs...)
		reg.GrantTypes = append(reg.GrantTypes, "authorization_code")
		reg.ResponseTypes = []string{"code"}
	}
	if len(c.auth.Scopes) > 0 {
		reg.Scope = strings.Join(c.auth.Scopes, " ")
	}
	body, err := json.Marshal(reg)
	if err != nil {
		return clientIdentity{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, as.RegistrationEndpoint, bytes.NewReader(body))
	if err != nil {
		return clientIdentity{}, err
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("accept", "application/json")
	resp, err := c.doer.Do(req)
	if err != nil {
		return clientIdentity{}, fmt.Errorf("mcp: oauth: dynamic client registration at %q: %w", as.Issuer, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxMetaBody))
	if resp.StatusCode != http.StatusCreated && (resp.StatusCode < 200 || resp.StatusCode >= 300) {
		var de dcrError
		if json.Unmarshal(raw, &de) == nil && de.Error != "" {
			// Surface the error CODE only, %q-escaped — never error_description
			// (third-party free text; minimal data, same rule as authzresponse.go).
			// The code is the meaningful signal SEP-837 asks for: invalid_redirect_uri
			// et al. tell the operator to adjust application_type/redirect_uris.
			return clientIdentity{}, fmt.Errorf("mcp: oauth: registration rejected by issuer %q with error code %q — adjust application_type/redirect_uris and retry (SEP-837)", as.Issuer, de.Error)
		}
		return clientIdentity{}, fmt.Errorf("mcp: oauth: registration at issuer %q: http %d", as.Issuer, resp.StatusCode)
	}
	var dr dcrResponse
	if err := json.Unmarshal(raw, &dr); err != nil {
		return clientIdentity{}, fmt.Errorf("mcp: oauth: decode registration response: %w", err)
	}
	if dr.ClientID == "" {
		return clientIdentity{}, fmt.Errorf("mcp: oauth: registration response carried no client_id")
	}
	return clientIdentity{
		method:       identityDCR,
		clientID:     dr.ClientID,
		clientSecret: dr.ClientSecret,
	}, nil
}

// clientAssertionJWT signs the RFC 7523 §2.2 private_key_jwt client assertion:
// iss = sub = the client_id, aud = the token endpoint being addressed, short-lived,
// with a fresh jti. The signing algorithm comes from the key's own alg.
func clientAssertionJWT(clientID, tokenEndpoint string, key *jose.JSONWebKey, now time.Time) (string, error) {
	alg := jose.SignatureAlgorithm(key.Algorithm)
	if alg == "" {
		return "", fmt.Errorf("mcp: oauth: client_assertion_jwk must declare its alg")
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: alg, Key: key}, nil)
	if err != nil {
		return "", fmt.Errorf("mcp: oauth: client assertion signer: %w", err)
	}
	jti, err := newPKCE() // 32 bytes of entropy, base64url — reused as a nonce source
	if err != nil {
		return "", err
	}
	claims := jwt.Claims{
		Issuer:   clientID,
		Subject:  clientID,
		Audience: jwt.Audience{tokenEndpoint},
		IssuedAt: jwt.NewNumericDate(now),
		Expiry:   jwt.NewNumericDate(now.Add(60 * time.Second)),
		ID:       jti.verifier,
	}
	return jwt.Signed(signer).Claims(claims).Serialize()
}
