// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"fmt"
	"net/url"
)

// authzresponse.go implements RFC 9207 authorization-response issuer validation as
// the MCP 2026-07-28 RC mandates it (SEP-2468) — the CLIENT-side mix-up defense.
// PKCE alone does not stop a mix-up: the client would send its code_verifier to the
// attacker's token endpoint. The defense is:
//
//  1. BEFORE redirecting the user-agent, record the issuer from the VALIDATED AS
//     metadata (discoverASMetadata already enforced RFC 8414 §3.3) together with
//     the per-request PKCE verifier and state — pendingAuthorization is that record;
//  2. on the authorization RESPONSE, apply the SEP-2468 decision table BEFORE the
//     code is redeemed at any token endpoint:
//
//     AS advertised iss support? | iss present? | action
//     ---------------------------+--------------+---------------------------------
//     yes                        | yes          | simple string comparison
//     yes                        | no           | REJECT (the AS promised it)
//     no                         | yes          | compare anyway (MCP extension)
//     no                         | no           | proceed
//
//     The comparison is BYTE-FOR-BYTE after form-urlencoded decoding (which
//     url.Values already performs): no scheme/host case folding, no default-port
//     elision, no trailing-slash or percent-encoding normalization. On mismatch the
//     response is rejected outright — including ERROR responses, whose error /
//     error_description / error_uri MUST NOT be acted on or surfaced (they may be
//     the attacker's).

// pendingAuthorization is the per-request record an authorization-code flow keeps
// between building the authorization URL and validating its response: the recorded
// issuer (RFC 9207 comparison anchor), whether that AS advertised the iss parameter
// (the decision-table row selector), the state, the PKCE verifier and the client
// identity/endpoint needed to redeem the code afterwards.
type pendingAuthorization struct {
	issuer        string // recorded from VALIDATED AS metadata (RFC 8414 §3.3)
	issSupported  bool   // authorization_response_iss_parameter_supported
	state         string
	pkce          pkce
	tokenEndpoint string
	redirectURI   string
	identity      clientIdentity
	resource      string
	// validated flips when validateAuthorizationResponse accepted the response;
	// redeemCode refuses to run without it (the RFC 9207 check is structurally
	// before any token-endpoint call).
	validated bool
}

// beginAuthorization builds the authorization-code request URL (PKCE S256 + RFC 8707
// resource indicator) and the pendingAuthorization record that MUST be used to
// validate its response. as is the VALIDATED metadata of the selected AS.
func (c *oauthClient) beginAuthorization(as authServerMetadata, identity clientIdentity, redirectURI, state string, scopes []string) (string, pendingAuthorization, error) {
	if as.AuthorizationEndpoint == "" {
		return "", pendingAuthorization{}, fmt.Errorf("mcp: oauth: authorization server metadata has no authorization_endpoint")
	}
	pk, err := newPKCE()
	if err != nil {
		return "", pendingAuthorization{}, err
	}
	authURL, err := buildAuthorizationURL(as.AuthorizationEndpoint, identity.clientID, redirectURI, c.resource, state, scopes, pk)
	if err != nil {
		return "", pendingAuthorization{}, err
	}
	return authURL, pendingAuthorization{
		issuer:        as.Issuer,
		issSupported:  as.AuthzResponseIssSupported,
		state:         state,
		pkce:          pk,
		tokenEndpoint: as.TokenEndpoint,
		redirectURI:   redirectURI,
		identity:      identity,
		resource:      c.resource,
	}, nil
}

// validateAuthorizationResponse applies the SEP-2468 decision table to the redirect
// query and returns the authorization code. It MUST be called before redeemCode —
// redeemCode re-checks that it was. Note url.Values has already form-urldecoded the
// parameters (RFC 9207 §2.4); from here every comparison is simple string equality.
func (p *pendingAuthorization) validateAuthorizationResponse(q url.Values) (string, error) {
	iss, issPresent := firstValue(q, "iss")
	switch {
	case issPresent && iss != p.issuer:
		// Mismatch — including on ERROR responses: the error fields are NOT read,
		// not propagated, not logged (they may be attacker-controlled).
		return "", fmt.Errorf("mcp: oauth: authorization response iss does not match the recorded issuer (RFC 9207 reject; response discarded)")
	case !issPresent && p.issSupported:
		return "", fmt.Errorf("mcp: oauth: authorization response carries no iss but issuer %q advertises authorization_response_iss_parameter_supported (RFC 9207 reject)", p.issuer)
	}
	// iss is valid (present+matching, or legitimately absent). Only NOW may the
	// response's other parameters be read.
	if p.state != "" && q.Get("state") != p.state {
		return "", fmt.Errorf("mcp: oauth: authorization response state mismatch")
	}
	if e := q.Get("error"); e != "" {
		// An honest AS's error response: surface the code (%q-escaped — it is still
		// third-party input), never the free-text description (minimal data).
		return "", fmt.Errorf("mcp: oauth: authorization request denied: %q", e)
	}
	code := q.Get("code")
	if code == "" {
		return "", fmt.Errorf("mcp: oauth: authorization response carries no code")
	}
	p.validated = true
	return code, nil
}

// redeemCode exchanges the validated authorization code at the recorded token
// endpoint (PKCE verifier + the SAME resource indicator). It refuses to run when the
// response was never validated (SEP-2468: validation happens BEFORE the code reaches
// any token endpoint — the order is structural, not advisory).
func (c *oauthClient) redeemCode(ctx context.Context, p *pendingAuthorization, code string) (tokenResponse, error) {
	if !p.validated {
		return tokenResponse{}, fmt.Errorf("mcp: oauth: authorization response was not validated (RFC 9207) — refusing to redeem the code")
	}
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {p.redirectURI},
		"code_verifier": {p.pkce.verifier},
		"resource":      {p.resource},
	}
	tok, err := c.tokenRequest(ctx, p.tokenEndpoint, p.identity, form)
	if err != nil {
		return tokenResponse{}, err
	}
	if tok.RefreshToken != "" {
		c.store.putRefreshToken(p.issuer, tok.RefreshToken)
	}
	return tok, nil
}

// firstValue returns a query parameter's first value and whether the parameter was
// present at all (q.Get cannot distinguish absent from empty).
func firstValue(q url.Values, key string) (string, bool) {
	vs, ok := q[key]
	if !ok || len(vs) == 0 {
		return "", false
	}
	return vs[0], true
}
