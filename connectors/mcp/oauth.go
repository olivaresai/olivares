// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"syscall"
	"time"
)

// MCP authorization (revision 2025-11-25, audited against the 2026-07-28 RC auth
// SEPs —). This implements the client side of the OAuth 2.1 model the spec
// mandates:
//
//   - Phase 1 (detection): a 401 carrying WWW-Authenticate: Bearer
//     resource_metadata="…" marks an OAuth-protected server. The connector surfaces
//     it as a finding + an inventory flag and never silently treats the server as
//     open.
//   - Phase 2 (authorized introspection): consume the Protected Resource Metadata
//     (RFC 9728), discover the authorization server — trying the spec's well-known
//     candidate order (RFC 8414 + OIDC discovery, SEP-2351) and refusing a metadata
//     document whose issuer is not BYTE-IDENTICAL to the issuer it was fetched for
//     (RFC 8414 §3.3) — and obtain an access token bound to THIS server via a
//     resource indicator (RFC 8707), then introspect read-only with it.
//   - Client identification follows the spec's priority order: pre-registered
//     credentials → CIMD (Client ID Metadata Documents, SEP-991) → Dynamic Client
//     Registration (RFC 7591, deprecated in the RC, kept as fallback) → fail (this
//     is a headless client; it never prompts). See clientreg.go / cimd.go. Persisted
//     credentials are KEYED BY ISSUER and never cross issuers (SEP-2352).
//   - RFC 9207: the authorization-code path records the expected issuer with the
//     per-request PKCE state and validates the response's iss — byte-for-byte, no
//     normalization — BEFORE the code is redeemed (SEP-2468; authzresponse.go).
//   - Scope step-up (SEP-835/SEP-2350): an insufficient_scope challenge is answered
//     ONCE with a re-acquired token whose scopes are the UNION of the previously
//     requested set and the challenge (client-side accumulation), then the request
//     is retried.
//
// Token passthrough is forbidden by design: the connector only ever uses a token it
// obtained for the specific server (resource-bound), never one presented by a third
// party, and it never calls tools/call — only the read-only list methods.

// maxMetaBody caps an OAuth metadata/token response body.
const maxMetaBody = 1 << 20

// wellKnownPRM and wellKnownAS are the RFC 9728 / RFC 8414 discovery suffixes.
const (
	wellKnownPRM = "/.well-known/oauth-protected-resource"
	wellKnownAS  = "/.well-known/oauth-authorization-server"
)

// serverAuth is the optional OAuth configuration for one MCP server. With it the
// connector performs Phase-2 authorized introspection; without it an OAuth-protected
// server is only detected (Phase 1). A BearerToken short-circuits the flow (the
// operator obtained an audience-bound token out of band); otherwise the client
// identity is selected per the spec's priority order (clientreg.go): pre-registered
// ClientID/Secret → CIMD (ClientIDMetadataURL) → DCR — and drives the
// client-credentials grant.
type serverAuth struct {
	ClientID     string   `json:"client_id"`
	ClientSecret string   `json:"client_secret"`
	Scopes       []string `json:"scopes"`
	Resource     string   `json:"resource"`     // canonical MCP server URI override (else derived from URL)
	TokenURL     string   `json:"token_url"`    // optional explicit token endpoint (skip AS discovery)
	BearerToken  string   `json:"bearer_token"` // operator-supplied access token (used directly)
	// Issuer, when set, is the AS issuer the PRE-REGISTERED ClientID/Secret belong to
	// (SEP-2352): if discovery resolves a DIFFERENT issuer, the flow fails loudly
	// rather than silently presenting credentials to the wrong AS. Unset, the
	// credentials bind to the first issuer discovery resolves (recorded per run).
	Issuer string `json:"issuer"`
	// ClientIDMetadataURL is THIS client's CIMD URL (SEP-991): an operator-hosted
	// HTTPS document URL used verbatim as client_id when the AS advertises
	// client_id_metadata_document_supported. See cimd.go for the URL rules.
	ClientIDMetadataURL string `json:"client_id_metadata_url"`
	// ClientAssertionJWK is an optional PRIVATE JWK (JSON) for private_key_jwt client
	// authentication (RFC 7523 §2.2) — the only authentication a CIMD identity can
	// carry (the CIMD draft forbids shared symmetric secrets). Secret-bearing
	// operator config, never logged, never persisted by the connector.
	ClientAssertionJWK string `json:"client_assertion_jwk"`
	// ApplicationType is the OIDC application_type sent in the remaining DCR fallback
	// (SEP-837 MUST). Default "web" (this connector is a server-side service);
	// "native" is for desktop/CLI/localhost-hosted clients.
	ApplicationType string `json:"application_type"`
	// RedirectURIs, when set, are registered in the DCR fallback (a host driving the
	// authorization-code flow needs them; the automated client-credentials
	// introspector does not).
	RedirectURIs []string `json:"redirect_uris"`
	// OfflineAccess opts in to requesting a refresh token by adding the
	// offline_access scope — sent ONLY when the AS metadata advertises it in
	// scopes_supported (SEP-2207 MAY). The connector never assumes a refresh token
	// will be issued.
	OfflineAccess bool `json:"offline_access"`
	// DynamicRegistration explicitly opts in to the DCR fallback (RFC 7591 — MAY,
	// deprecated in the 2026-07-28 RC in favor of CIMD). Registration creates client
	// state at the AS, so it is deliberate operator intent, never implied.
	DynamicRegistration bool `json:"dynamic_registration"`
}

// configured reports whether any Phase-2 identification path is present.
func (a *serverAuth) configured() bool {
	return a != nil && (a.BearerToken != "" || a.ClientID != "" || a.TokenURL != "" ||
		a.ClientIDMetadataURL != "" || a.DynamicRegistration)
}

// protectedResourceMetadata is the RFC 9728 document. resource is REQUIRED;
// authorization_servers is OPTIONAL in RFC 9728 but the MCP spec requires at least
// one.
type protectedResourceMetadata struct {
	Resource             string   `json:"resource"`
	AuthorizationServers []string `json:"authorization_servers"`
	ScopesSupported      []string `json:"scopes_supported"`
}

// authServerMetadata is the subset of RFC 8414 AS metadata the client uses. The
// Additions are the three capability flags the RC auth SEPs key off:
// client_id_metadata_document_supported (CIMD, SEP-991),
// authorization_response_iss_parameter_supported (RFC 9207, SEP-2468) and
// scopes_supported (offline_access gating, SEP-2207).
type authServerMetadata struct {
	Issuer                        string   `json:"issuer"`
	AuthorizationEndpoint         string   `json:"authorization_endpoint"`
	TokenEndpoint                 string   `json:"token_endpoint"`
	RegistrationEndpoint          string   `json:"registration_endpoint"`
	CodeChallengeMethodsSupported []string `json:"code_challenge_methods_supported"`
	ScopesSupported               []string `json:"scopes_supported"`
	ClientIDMetadataSupported     bool     `json:"client_id_metadata_document_supported"`
	AuthzResponseIssSupported     bool     `json:"authorization_response_iss_parameter_supported"`
}

// oauthRequiredError marks a server that answered 401 with an OAuth challenge. It
// carries the PRM URL (the inventory signal) and whether a token binding was
// attempted, so Gather can emit the Phase-1 finding with the right dimension.
type oauthRequiredError struct {
	resourceMetadata string // the PRM URL from WWW-Authenticate (may be "")
	attempted        bool   // true if Phase-2 was configured and still failed
}

func (e *oauthRequiredError) Error() string {
	if e.attempted {
		return "mcp: oauth-protected server: token binding attempted but introspection still unauthorized"
	}
	return "mcp: oauth-protected server: not introspected (no token binding configured)"
}

// oauthClient performs discovery and token acquisition for one server. It uses an
// SSRF-guarded HTTP client for every metadata/token fetch. Credentials it obtains
// (DCR registrations, refresh tokens) live in store, KEYED BY ISSUER (SEP-2352);
// scopes holds the scope set most recently REQUESTED, the base of a step-up union
// (SEP-2350).
type oauthClient struct {
	auth     *serverAuth
	resource string // the canonical MCP server URI (resource indicator value)
	doer     httpDoer
	store    *issuerCredentialStore
	now      func() time.Time

	mu         sync.Mutex
	scopes     []string // scopes most recently requested (step-up accumulation base)
	tofuIssuer string   // first discovered issuer (pre-registered credential TOFU bind, SEP-2352)
	// regObs records the client-identification path actually taken plus the AS's
	// capability flags (DCR-deprecation posture). Set by clientIdentityFor.
	regObs *authRegistrationObservation
}

// httpDoer is the minimal HTTP interface (satisfied by *http.Client).
type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// newOAuthClient builds an OAuth client for serverURL with the given auth config. The
// resource indicator is the operator override or the canonical form of serverURL.
func newOAuthClient(serverURL string, auth *serverAuth, doer httpDoer) (*oauthClient, error) {
	res := ""
	if auth != nil {
		res = strings.TrimSpace(auth.Resource)
	}
	if res == "" {
		c, err := canonicalResourceURI(serverURL)
		if err != nil {
			return nil, err
		}
		res = c
	}
	if doer == nil {
		doer = ssrfSafeClient()
	}
	return &oauthClient{auth: auth, resource: res, doer: doer, store: newIssuerCredentialStore()}, nil
}

func (c *oauthClient) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// recordRequestedScopes remembers the scope set actually sent to the token endpoint
// (the accumulation base for a later SEP-2350 step-up).
func (c *oauthClient) recordRequestedScopes(scopes []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.scopes = append([]string(nil), scopes...)
}

// requestedScopes returns the last requested scope set (never the configured set —
// a prior step-up may already have widened it).
func (c *oauthClient) requestedScopes() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.scopes == nil {
		return append([]string(nil), c.auth.Scopes...)
	}
	return append([]string(nil), c.scopes...)
}

// recordRegistration notes the client-identification method that resolved against
// the discovered AS, with the AS capability flags the DCR-deprecation posture rules
// grade. The latest resolution wins (one AS per server in practice).
func (c *oauthClient) recordRegistration(method string, as authServerMetadata) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.regObs = &authRegistrationObservation{
		method:               method,
		cimdSupported:        as.ClientIDMetadataSupported,
		registrationEndpoint: as.RegistrationEndpoint != "",
	}
}

// registrationObservation returns the recorded client-identification observation,
// or nil when the identity-selection step never ran.
func (c *oauthClient) registrationObservation() *authRegistrationObservation {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.regObs == nil {
		return nil
	}
	obs := *c.regObs
	return &obs
}

// bindIssuer records the FIRST issuer discovery resolved for this client (the
// SEP-2352 TOFU bind for unpinned pre-registered credentials) and returns the bound
// value — the first caller wins, every later call sees the original.
func (c *oauthClient) bindIssuer(issuer string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.tofuIssuer == "" {
		c.tofuIssuer = issuer
	}
	return c.tofuIssuer
}

// ssrfSafeClient is the default HTTP client for OAuth metadata/token fetches. Its
// dialer re-checks the CONCRETE resolved IP at connect time (net.Dialer.Control),
// which closes the DNS-rebinding TOCTOU a pre-flight resolve cannot: a hostname that
// validates as public but rebinds to a reserved IP is refused when the socket is
// actually dialed. Loopback is allowed for local development.
func ssrfSafeClient() *http.Client {
	dialer := &net.Dialer{
		Timeout: 10 * time.Second,
		Control: func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			ip := net.ParseIP(host)
			if ip == nil {
				return fmt.Errorf("mcp: oauth: cannot parse dial address %q", address)
			}
			if reservedIP(ip) {
				return fmt.Errorf("mcp: oauth: refusing to dial reserved address %s", ip)
			}
			return nil
		},
	}
	return &http.Client{
		Timeout:   30 * time.Second,
		Transport: &http.Transport{DialContext: dialer.DialContext},
	}
}

// reservedIP reports whether ip is a private, link-local, multicast or unspecified
// address that an outbound metadata fetch must never reach. Loopback is exempted (it
// is the only special range allowed, for local development).
func reservedIP(ip net.IP) bool {
	if ip.IsLoopback() {
		return false
	}
	return ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified()
}

// bearer obtains an access token bound to this server for authorized introspection.
// It returns the configured bearer token directly, or runs discovery
// (WWW-Authenticate → PRM → AS metadata) and the client-credentials grant with a
// resource indicator. wwwAuth is the 401's WWW-Authenticate header.
func (c *oauthClient) bearer(ctx context.Context, wwwAuth string) (string, error) {
	return c.bearerWithScopes(ctx, wwwAuth, c.auth.Scopes)
}

// bearerWithScopes is bearer with an explicit scope set — the step-up path
// (SEP-2350) re-enters here with the accumulated union. The scopes actually
// REQUESTED are recorded on the client so a later step-up can union against them.
func (c *oauthClient) bearerWithScopes(ctx context.Context, wwwAuth string, scopes []string) (string, error) {
	if c.auth.BearerToken != "" {
		return c.auth.BearerToken, nil
	}

	// Explicit token endpoint: no AS metadata is available, so the only usable
	// identity is the pre-registered one (CIMD/DCR both require discovery).
	if tokenURL := strings.TrimSpace(c.auth.TokenURL); tokenURL != "" {
		identity := clientIdentity{
			method:   identityPreRegistered,
			clientID: c.auth.ClientID, clientSecret: c.auth.ClientSecret,
		}
		if identity.clientID == "" {
			return "", fmt.Errorf("mcp: oauth: token_url requires pre-registered client credentials (CIMD/DCR need AS discovery)")
		}
		tok, err := c.grantClientCredentials(ctx, tokenURL, identity, scopes)
		if err != nil {
			return "", err
		}
		c.recordRequestedScopes(scopes)
		return tok.AccessToken, nil
	}

	as, err := c.discoverAS(ctx, wwwAuth)
	if err != nil {
		return "", err
	}
	identity, err := c.clientIdentityFor(ctx, as)
	if err != nil {
		return "", err
	}
	// SEP-2207: offline_access is added ONLY on explicit operator opt-in AND when the
	// AS advertises it — never assumed, never demanded by the resource.
	if c.auth.OfflineAccess && containsScope(as.ScopesSupported, scopeOfflineAccess) && !containsScope(scopes, scopeOfflineAccess) {
		scopes = append(append([]string(nil), scopes...), scopeOfflineAccess)
	}
	tok, err := c.grantClientCredentials(ctx, as.TokenEndpoint, identity, scopes)
	if err != nil {
		return "", err
	}
	c.recordRequestedScopes(scopes)
	if tok.RefreshToken != "" {
		c.store.putRefreshToken(as.Issuer, tok.RefreshToken)
	}
	return tok.AccessToken, nil
}

// stepUpBearer answers ONE insufficient_scope challenge (SEP-835/SEP-2350): it
// computes the UNION of the scopes previously requested and the scopes the challenge
// names (client-side accumulation — the server is allowed to be stateless and only
// name the current operation's scopes), then re-acquires the bearer with that set.
// It returns ("", false, nil) when the challenge is not a scope challenge.
func (c *oauthClient) stepUpBearer(ctx context.Context, wwwAuth string) (string, bool, error) {
	challenged, ok := insufficientScopeChallenge(wwwAuth)
	if !ok {
		return "", false, nil
	}
	union := accumulateScopes(c.requestedScopes(), challenged)
	tok, err := c.bearerWithScopes(ctx, wwwAuth, union)
	if err != nil {
		return "", true, err
	}
	return tok, true, nil
}

// discoverAS walks WWW-Authenticate → PRM → AS metadata (SEP-2351 candidate order +
// the RFC 8414 §3.3 issuer check) and requires a token_endpoint.
func (c *oauthClient) discoverAS(ctx context.Context, wwwAuth string) (authServerMetadata, error) {
	prmURL := resourceMetadataURL(wwwAuth)
	if prmURL == "" {
		return authServerMetadata{}, fmt.Errorf("mcp: oauth: WWW-Authenticate challenge carried no resource_metadata to discover from")
	}
	prm, err := fetchJSON[protectedResourceMetadata](ctx, c.doer, prmURL)
	if err != nil {
		return authServerMetadata{}, fmt.Errorf("mcp: oauth: fetch protected resource metadata: %w", err)
	}
	if len(prm.AuthorizationServers) == 0 {
		return authServerMetadata{}, fmt.Errorf("mcp: oauth: protected resource metadata lists no authorization server")
	}
	// RFC 9728 §3.3: the PRM's resource value MUST identify the protected resource
	// the client is addressing — simple string comparison against the resource
	// indicator this client binds its tokens to. A mismatched (or absent — the
	// field is REQUIRED) value is an impersonation signal: a PRM answering for a
	// different resource must not steer this client's authorization. An operator
	// whose server legitimately canonicalizes differently sets auth.resource.
	if prm.Resource != c.resource {
		return authServerMetadata{}, fmt.Errorf("mcp: oauth: protected resource metadata declares resource %q, expected %q (RFC 9728 §3.3 reject; set auth.resource if the server's canonical URI differs)", prm.Resource, c.resource)
	}
	as, err := discoverASMetadata(ctx, c.doer, prm.AuthorizationServers[0])
	if err != nil {
		return authServerMetadata{}, err
	}
	if as.TokenEndpoint == "" {
		return authServerMetadata{}, fmt.Errorf("mcp: oauth: authorization server metadata has no token_endpoint")
	}
	return as, nil
}

// tokenResponse is an RFC 6749 §5.1 token endpoint response (the fields this client
// uses). RefreshToken is parsed per SEP-2207 but never assumed to be present.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
}

// grantClientCredentials runs the client-credentials grant with the resource
// indicator bound to this MCP server, so the issued token's audience is this server.
// The identity decides client authentication: pre-registered/DCR secrets use HTTP
// basic; a CIMD identity authenticates with private_key_jwt (a CIMD client cannot
// hold a shared secret) or is refused — the client-credentials grant requires a
// confidential client.
func (c *oauthClient) grantClientCredentials(ctx context.Context, tokenEndpoint string, identity clientIdentity, scopes []string) (tokenResponse, error) {
	form := url.Values{
		"grant_type": {"client_credentials"},
		"resource":   {c.resource}, // RFC 8707 — audience-bind the token to this server
	}
	if len(scopes) > 0 {
		form.Set("scope", strings.Join(scopes, " "))
	}
	return c.tokenRequest(ctx, tokenEndpoint, identity, form)
}

// refreshGrant redeems a refresh token (SEP-2207), keeping the resource indicator so
// the refreshed token stays audience-bound. A rotated refresh token replaces the
// stored one (keyed by issuer); the old value is forgotten.
func (c *oauthClient) refreshGrant(ctx context.Context, tokenEndpoint, issuer string, identity clientIdentity, refreshToken string) (tokenResponse, error) {
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"resource":      {c.resource},
	}
	tok, err := c.tokenRequest(ctx, tokenEndpoint, identity, form)
	if err != nil {
		return tokenResponse{}, err
	}
	if tok.RefreshToken != "" {
		c.store.putRefreshToken(issuer, tok.RefreshToken)
	}
	return tok, nil
}

// tokenRequest POSTs a token-endpoint form with the identity's client
// authentication and decodes the response.
func (c *oauthClient) tokenRequest(ctx context.Context, tokenEndpoint string, identity clientIdentity, form url.Values) (tokenResponse, error) {
	if err := validateOutboundURL(ctx, tokenEndpoint); err != nil {
		return tokenResponse{}, err
	}
	switch {
	case identity.assertionKey != nil:
		// private_key_jwt (RFC 7523 §2.2): the only authentication a CIMD identity
		// can carry, also usable by pre-registered/DCR clients that prefer it.
		assertion, err := clientAssertionJWT(identity.clientID, tokenEndpoint, identity.assertionKey, c.clock())
		if err != nil {
			return tokenResponse{}, err
		}
		form.Set("client_id", identity.clientID)
		form.Set("client_assertion_type", clientAssertionTypeJWTBearer)
		form.Set("client_assertion", assertion)
	case identity.clientSecret != "":
		// handled below via basic auth
	case identity.method == identityCIMD:
		return tokenResponse{}, fmt.Errorf("mcp: oauth: CIMD identity %q has no client_assertion_jwk — a CIMD client without a key is public, and the client-credentials grant requires client authentication", identity.clientID)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return tokenResponse{}, err
	}
	req.Header.Set("content-type", "application/x-www-form-urlencoded")
	req.Header.Set("accept", "application/json")
	if identity.assertionKey == nil && identity.clientID != "" {
		req.SetBasicAuth(url.QueryEscape(identity.clientID), url.QueryEscape(identity.clientSecret))
	}
	resp, err := c.doer.Do(req)
	if err != nil {
		return tokenResponse{}, fmt.Errorf("mcp: oauth: token request: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxMetaBody))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return tokenResponse{}, fmt.Errorf("mcp: oauth: token endpoint http %d", resp.StatusCode)
	}
	var tok tokenResponse
	if err := json.Unmarshal(raw, &tok); err != nil {
		return tokenResponse{}, fmt.Errorf("mcp: oauth: decode token: %w", err)
	}
	if tok.AccessToken == "" {
		return tokenResponse{}, fmt.Errorf("mcp: oauth: token response carried no access_token")
	}
	return tok, nil
}

// buildAuthorizationURL constructs the authorization-code request URL with PKCE S256
// and the resource indicator, per the MCP spec. It is the interactive/host-driven
// path (an automated introspector uses client-credentials); the connector exposes it
// so a host can complete a user-consent flow and obtain an audience-bound token.
func buildAuthorizationURL(authEndpoint, clientID, redirectURI, resource, state string, scopes []string, pk pkce) (string, error) {
	u, err := url.Parse(authEndpoint)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set("response_type", "code")
	q.Set("client_id", clientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("resource", resource) // RFC 8707 — present in BOTH authorization and token requests
	q.Set("code_challenge", pk.challenge)
	q.Set("code_challenge_method", pkceMethodS256)
	q.Set("state", state)
	if len(scopes) > 0 {
		q.Set("scope", strings.Join(scopes, " "))
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// resourceMetadataURL extracts the resource_metadata parameter from a Bearer
// WWW-Authenticate challenge (RFC 9728 §5.1). It is lenient about other auth-params
// (realm, scope, error) and quoting.
func resourceMetadataURL(header string) string {
	h := strings.TrimSpace(header)
	if h == "" {
		return ""
	}
	// Drop the scheme token ("Bearer") if present.
	if i := strings.IndexByte(h, ' '); i >= 0 && !strings.Contains(h[:i], "=") {
		h = h[i+1:]
	}
	for _, part := range splitAuthParams(h) {
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(k), "resource_metadata") {
			return strings.Trim(strings.TrimSpace(v), `"`)
		}
	}
	return ""
}

// splitAuthParams splits comma-separated auth-params, not splitting inside quotes.
func splitAuthParams(s string) []string {
	var parts []string
	var cur strings.Builder
	inQuote := false
	for _, r := range s {
		switch {
		case r == '"':
			inQuote = !inQuote
			cur.WriteRune(r)
		case r == ',' && !inQuote:
			parts = append(parts, strings.TrimSpace(cur.String()))
			cur.Reset()
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		parts = append(parts, strings.TrimSpace(cur.String()))
	}
	return parts
}

// wellKnownOIDC is the OpenID Connect Discovery well-known suffix (the spec's
// fallback candidates after RFC 8414's oauth-authorization-server — SEP-2351 keeps
// MCP on the DEFAULT suffixes; no application-specific suffix exists).
const wellKnownOIDC = "/.well-known/openid-configuration"

// asMetadataCandidates builds the AS metadata discovery URLs in the EXACT order the
// MCP spec mandates (MUST, 2025-11-25 §AS discovery; restated by SEP-2351):
//
//	issuer WITH a path component (https://as.example/tenant):
//	  1. https://as.example/.well-known/oauth-authorization-server/tenant  (RFC 8414 insertion)
//	  2. https://as.example/.well-known/openid-configuration/tenant       (OIDC insertion)
//	  3. https://as.example/tenant/.well-known/openid-configuration       (OIDC appending)
//	issuer WITHOUT a path:
//	  1. https://as.example/.well-known/oauth-authorization-server
//	  2. https://as.example/.well-known/openid-configuration
func asMetadataCandidates(issuer string) ([]string, error) {
	u, err := url.Parse(issuer)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("mcp: oauth: invalid issuer %q", issuer)
	}
	u.RawQuery = ""
	u.Fragment = ""
	path := strings.TrimSuffix(u.Path, "/")

	build := func(p string) string {
		c := *u
		c.Path = p
		return c.String()
	}
	if path == "" {
		return []string{build(wellKnownAS), build(wellKnownOIDC)}, nil
	}
	return []string{
		build(wellKnownAS + path),
		build(wellKnownOIDC + path),
		build(path + wellKnownOIDC),
	}, nil
}

// discoverASMetadata fetches AS metadata trying the spec's candidate order, and
// REFUSES a document whose issuer is not BYTE-IDENTICAL to the issuer it was fetched
// for (RFC 8414 §3.3 / OIDC Discovery §4.3, a MUST in the 2026-07-28 RC draft): a
// mismatched document is an impersonation signal, and the validated issuer value is
// what the RFC 9207 comparison later keys on. A candidate that fails to fetch moves
// on to the next; an issuer MISMATCH is terminal (the document answered for this
// issuer and lied — trying further candidates cannot make it honest).
func discoverASMetadata(ctx context.Context, doer httpDoer, issuer string) (authServerMetadata, error) {
	candidates, err := asMetadataCandidates(issuer)
	if err != nil {
		return authServerMetadata{}, err
	}
	var lastErr error
	for _, cand := range candidates {
		as, err := fetchJSON[authServerMetadata](ctx, doer, cand)
		if err != nil {
			lastErr = err
			continue
		}
		if as.Issuer != issuer {
			return authServerMetadata{}, fmt.Errorf("mcp: oauth: AS metadata at %s declares issuer %q, expected %q (RFC 8414 §3.3 reject)", cand, as.Issuer, issuer)
		}
		return as, nil
	}
	return authServerMetadata{}, fmt.Errorf("mcp: oauth: no AS metadata for issuer %q: %w", issuer, lastErr)
}

// insufficientScopeChallenge parses a WWW-Authenticate header and, when it is a
// Bearer challenge with error="insufficient_scope" (SEP-835), returns the challenged
// scopes. The challenge's scope list is authoritative for the current request.
func insufficientScopeChallenge(header string) ([]string, bool) {
	h := strings.TrimSpace(header)
	if h == "" {
		return nil, false
	}
	if i := strings.IndexByte(h, ' '); i >= 0 && !strings.Contains(h[:i], "=") {
		if !strings.EqualFold(h[:i], "Bearer") {
			return nil, false
		}
		h = h[i+1:]
	}
	var scopes []string
	isInsufficient := false
	for _, part := range splitAuthParams(h) {
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		v = strings.Trim(strings.TrimSpace(v), `"`)
		switch strings.ToLower(strings.TrimSpace(k)) {
		case "error":
			isInsufficient = v == "insufficient_scope"
		case "scope":
			scopes = strings.Fields(v)
		}
	}
	if !isInsufficient {
		return nil, false
	}
	return scopes, true
}

// accumulateScopes is the SEP-2350 client-side union: previously requested scopes
// first (order preserved), then the newly challenged ones — so a step-up never loses
// a previously granted permission and the result is deterministic.
func accumulateScopes(previous, challenged []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(previous)+len(challenged))
	for _, group := range [][]string{previous, challenged} {
		for _, s := range group {
			if s = strings.TrimSpace(s); s == "" {
				continue
			}
			if _, dup := seen[s]; dup {
				continue
			}
			seen[s] = struct{}{}
			out = append(out, s)
		}
	}
	return out
}

// containsScope reports whether list contains scope s (exact match).
func containsScope(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// canonicalResourceURI derives the RFC 8707 / MCP canonical resource URI from a
// server URL: lowercase scheme and host, no fragment, no trailing slash, keeping the
// path that identifies the specific server.
func canonicalResourceURI(serverURL string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(serverURL))
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("mcp: oauth: cannot derive resource uri from %q", serverURL)
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	u.Fragment = ""
	u.RawQuery = ""
	if u.Path == "/" {
		u.Path = ""
	} else {
		u.Path = strings.TrimSuffix(u.Path, "/")
	}
	return u.String(), nil
}

// fetchJSON GETs an SSRF-guarded URL and decodes the JSON body into T.
func fetchJSON[T any](ctx context.Context, doer httpDoer, rawURL string) (T, error) {
	var zero T
	if err := validateOutboundURL(ctx, rawURL); err != nil {
		return zero, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return zero, err
	}
	req.Header.Set("accept", "application/json")
	resp, err := doer.Do(req)
	if err != nil {
		return zero, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return zero, fmt.Errorf("mcp: oauth: GET %s: http %d", rawURL, resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxMetaBody))
	if err != nil {
		return zero, err
	}
	var out T
	if err := json.Unmarshal(raw, &out); err != nil {
		return zero, err
	}
	return out, nil
}

// validateOutboundURL is the up-front SSRF guard for every metadata/token fetch: it
// enforces HTTPS (except a loopback host for local development) and fast-rejects a
// literal reserved IP. The AUTHORITATIVE block on a reserved address a hostname
// RESOLVES to is the dial-time check in ssrfSafeClient (closing the rebinding TOCTOU,
// RFC 9728 §7.7); doing it here too would be a redundant, racy second resolution.
func validateOutboundURL(_ context.Context, rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("mcp: oauth: bad url %q: %w", rawURL, err)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("mcp: oauth: url %q has no host", rawURL)
	}
	ip := net.ParseIP(host) // nil for a hostname (checked at dial time instead)
	loopbackHost := host == "localhost" || (ip != nil && ip.IsLoopback())
	secure := u.Scheme == "https" || (u.Scheme == "http" && loopbackHost)
	if !secure {
		return fmt.Errorf("mcp: oauth: refusing non-HTTPS url %q", rawURL)
	}
	if ip != nil && reservedIP(ip) {
		return fmt.Errorf("mcp: oauth: refusing reserved address %s for %q", ip, rawURL)
	}
	return nil
}
