// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	jwt "github.com/go-jose/go-jose/v4/jwt"
)

// tokenvalidate.go is the audience-binding, FAIL-CLOSED bearer validator at the heart
// of the inline MCP Resource Server (AIP-02 §b). It is the direct defense against the
// OAuth confused-deputy: this server MUST reject any token not issued FOR it, and MUST
// NOT accept or transit a token minted for another audience (RFC 8707 / RFC 9068
// §4). Every bearer is validated two ways depending on its form:
//
//   - JWT access token (RFC 9068 "at+jwt"): the token's iss claim selects the trusted
//     issuer by SIMPLE STRING COMPARISON (issuertrust.go) — a token with no iss, or an
//     iss that is not a configured trusted issuer, is refused BEFORE any signature
//     check. The signature is then verified against THAT issuer's own JWKS (inline
//     anchor, or an SSRF-guarded fetch by kid; never another issuer's keys), and the
//     claims validated: iss MUST exactly match the selected issuer, aud MUST contain
//     this server's canonical resource URI, exp/nbf within skew.
//   - opaque token: introspected (RFC 7662) at each trusted issuer's OWN endpoint with
//     the RS's OWN per-issuer credentials, in declaration order; the first active
//     answer wins, and an active answer whose iss names a DIFFERENT issuer than the
//     endpoint it came from is a hard reject (cross-issuer confusion), never a
//     fallthrough. active==true AND the introspected aud MUST contain this server's
//     resource URI.
//
// The iss check is NOT optional. RFC 9068 §4 binds the RS: "The issuer
// identifier ... MUST exactly match the value of the iss claim" and "The resource
// server MUST use the keys provided by the authorization server" — the keys of the
// issuer the token names. (The MCP 2026-07-28 RC's RFC 9207 iss rule, SEP-2468, is
// the CLIENT-side mirror of the same mix-up defense; see oauth.go.)
//
// FAIL CLOSED: a missing token, a bad signature, a missing/unknown/foreign iss, a
// missing/foreign aud, an expired token, or an introspection that says inactive ALL
// return an error → the RS answers 401 (the gate, not this validator, turns a scope
// shortfall into a 403). We DO NOT hand-roll JWT/JWKS/audience/IP parsing — go-jose
// (vetted) does the crypto and claims, canonicalResourceURI (oauth.go) does the
// RFC 8707 URI, and the SSRF guard (ssrfSafeClient/validateOutboundURL) does the
// network.

// mcpAllowedAlgs pins the asymmetric signature algorithms accepted when validating an
// inbound access token — the defense against an algorithm-confusion downgrade (HMAC and
// "none" are rejected by omission). Mirrors a2aAllowedAlgs in the A2A connector.
var mcpAllowedAlgs = []jose.SignatureAlgorithm{
	jose.EdDSA,
	jose.ES256, jose.ES384, jose.ES512,
	jose.RS256, jose.RS384, jose.RS512,
	jose.PS256, jose.PS384, jose.PS512,
}

// tokenClockSkew is the leeway applied to exp/nbf/iat when validating a token (modest,
// to tolerate clock drift between the AS and this RS without widening the window).
const tokenClockSkew = 60 * time.Second

// validatedToken is the result of a successful bearer validation: the authenticated
// subject, its granted scopes (a set), its ROLES (a set, for the E1 per-role tool
// allowlist), and provenance. It carries NO raw token — the raw bearer NEVER leaves the
// validator, so it can never reach an upstream request builder (the no-token-passthrough
// invariant is structural).
type validatedToken struct {
	Subject     string
	IsDelegated bool
	ActAs       string
	// ClientID is the normalized OAuth client identity (RFC 9068 client_id, with
	// the OIDC azp claim as fallback; RFC 7662 client_id on introspection).:
	// it namespaces the client-supplied operation key in the OperationID
	// derivation — without it, DIFFERENT clients acting as the same subject would
	// share one idempotency namespace (the design's shared-namespace risk). Empty
	// when the AS mints neither claim (legacy tokens — a documented residual).
	ClientID        string
	Scopes          map[string]struct{}
	Roles           map[string]struct{}
	Issuer          string
	Audience        []string
	TokenType       string // "at+jwt" | "opaque"
	Binding         string // "bearer" | "dpop" | "mtls"
	ConfirmationJKT string // cnf.jkt (RFC 9449)
	ConfirmationX5T string // cnf["x5t#S256"] (RFC 8705)
}

// hasScope reports whether the token was granted scope s (empty s ⇒ true: no scope
// beyond a valid, audience-bound token is required).
func (v validatedToken) hasScope(s string) bool {
	if strings.TrimSpace(s) == "" {
		return true
	}
	_, ok := v.Scopes[s]
	return ok
}

// defaultRoleClaim is the JWT/introspection claim the validator reads roles from when the
// operator does not name one. `roles` is the common convention; the Claude MCP API has no
// native role concept (E1 is a control-plane layer), so the claim is operator-configured.
const defaultRoleClaim = "roles"

const (
	tokenBindingBearer = "bearer"
	tokenBindingDPoP   = "dpop"
	tokenBindingMTLS   = "mtls"
)

type tokenConfirmation struct {
	JKT string `json:"jkt"`
	X5T string `json:"x5t#S256"`
}

// rolesFromClaim parses an OAuth/OIDC roles claim into a set. It accepts the three shapes
// IdPs emit: a JSON array of strings, a single string, or a space/comma-delimited string.
// A nil/absent claim yields an empty set (the caller then holds no roles — deny-closed for
// any role-restricted tool).
func rolesFromClaim(raw json.RawMessage) map[string]struct{} {
	out := map[string]struct{}{}
	if len(raw) == 0 {
		return out
	}
	var arr []string
	if json.Unmarshal(raw, &arr) == nil {
		for _, r := range arr {
			if r = strings.TrimSpace(r); r != "" {
				out[r] = struct{}{}
			}
		}
		return out
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		for _, r := range strings.FieldsFunc(s, func(c rune) bool { return c == ' ' || c == ',' }) {
			if r = strings.TrimSpace(r); r != "" {
				out[r] = struct{}{}
			}
		}
	}
	return out
}

// tokenValidator validates inbound bearers against this server's identity. resource is
// the canonical RFC 8707 resource URI this RS represents (the mandatory audience);
// keyring is the issuer-keyed trust store (issuertrust.go) — there is no skip-the-iss
// mode anymore: a validator without at least one trusted issuer is never built.
type tokenValidator struct {
	resource    string         // canonical resource URI (REQUIRED audience)
	keyring     *issuerKeyring // issuer-keyed trust anchors (REQUIRED; iss is mandatory)
	requireType bool           // RFC 9068 strict: require typ=at+jwt on a JWT access token
	roleClaim   string         // the claim roles are read from (E1 per-role allowlist); "" ⇒ defaultRoleClaim
	doer        httpDoer
	now         func() time.Time
}

// rolesClaimName returns the configured role-claim name, defaulting to "roles".
func (tv *tokenValidator) rolesClaimName() string {
	if c := strings.TrimSpace(tv.roleClaim); c != "" {
		return c
	}
	return defaultRoleClaim
}

// validate authenticates a raw bearer and returns the validated token, or an error
// (the RS maps any error to 401 invalid_token — including a cross-audience or
// foreign-issuer token, which is the confused-deputy rejection). It tries the JWT
// path first; a token that is not a parseable JWS falls back to introspection when
// any trusted issuer configures an endpoint.
func (tv *tokenValidator) validate(ctx context.Context, raw string) (validatedToken, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return validatedToken{}, fmt.Errorf("mcp: rs: no bearer token")
	}
	parsed, perr := jwt.ParseSigned(raw, mcpAllowedAlgs)
	if perr == nil {
		return tv.validateJWT(ctx, parsed)
	}
	if len(tv.keyring.introspectors()) > 0 {
		return tv.validateOpaque(ctx, raw)
	}
	return validatedToken{}, fmt.Errorf("mcp: rs: token is not a verifiable JWT and no introspection endpoint is configured: %w", perr)
}

// validateJWT verifies a JWS access token, issuer-first (RFC 9068 §4):
//
//  1. the UNVERIFIED iss claim selects the trusted issuer — simple string comparison,
//     fail-closed on a missing or unknown iss (no skip mode);
//  2. the signature is verified against THAT issuer's own JWKS (by kid) — a key
//     configured for another issuer is unreachable by construction;
//  3. the now-VERIFIED claims are validated: iss MUST exactly match the selected
//     issuer (go-jose re-compares the verified claim, closing the step-1 unverified
//     read), aud MUST contain this server's resource URI (RFC 8707), exp/nbf within
//     skew.
//
// The scope claim is read from `scope` (string) or `scp` (array). FAIL CLOSED on any
// failure.
func (tv *tokenValidator) validateJWT(ctx context.Context, parsed *jwt.JSONWebToken) (validatedToken, error) {
	if len(parsed.Headers) == 0 {
		return validatedToken{}, fmt.Errorf("mcp: rs: token has no JOSE header")
	}
	// RFC 9068 strict (opt-in): an access token's typ MUST be at+jwt. This rejects an
	// id_token or other-typ JWT replayed as an access token — defense-in-depth on top
	// of the always-on audience binding below.
	if tv.requireType && !isAccessTokenType(headerType(parsed.Headers[0])) {
		return validatedToken{}, fmt.Errorf("mcp: rs: token typ is not at+jwt (RFC 9068 strict)")
	}
	// Issuer selection from the UNVERIFIED claims: this read only chooses which trust
	// anchor MAY verify the token; the same iss is re-validated below against the
	// verified claims, so a forged iss buys an attacker nothing but the wrong keyring
	// entry (and a signature failure).
	var unverified jwt.Claims
	if err := parsed.UnsafeClaimsWithoutVerification(&unverified); err != nil {
		return validatedToken{}, fmt.Errorf("mcp: rs: token claims unreadable: %w", err)
	}
	if unverified.Issuer == "" {
		return validatedToken{}, fmt.Errorf("mcp: rs: token carries no iss claim (RFC 9068 §4: iss is mandatory)")
	}
	issuer, ok := tv.keyring.lookup(unverified.Issuer)
	if !ok {
		return validatedToken{}, fmt.Errorf("mcp: rs: token issuer is not a trusted issuer of this server")
	}
	kid := parsed.Headers[0].KeyID
	key, err := issuer.resolveKey(ctx, tv.doer, kid, tv.clock())
	if err != nil {
		return validatedToken{}, err
	}
	var std jwt.Claims
	var ext struct {
		Scope string   `json:"scope"`
		Scp   []string `json:"scp"`
	}
	// raw captures all claims so the operator-configured role claim (E1) can be read by
	// name in the SAME verified pass — no second signature check.
	var raw map[string]json.RawMessage
	if err := parsed.Claims(key, &std, &ext, &raw); err != nil {
		return validatedToken{}, fmt.Errorf("mcp: rs: token signature: %w", err)
	}
	// Audience binding (RFC 8707 / RFC 9068): the token MUST name this server. An
	// empty AnyAudience would SKIP the check, so we always pass the resource URI — a
	// token with no aud, or an aud that does not include us, fails here (the
	// confused-deputy rejection). Issuer is ALWAYS expected (exact string equality on
	// the VERIFIED claim); exp/nbf are checked with a small skew leeway.
	expected := jwt.Expected{
		Issuer:      issuer.issuer,
		AnyAudience: jwt.Audience{tv.resource},
		Time:        tv.clock(),
	}
	if err := std.ValidateWithLeeway(expected, tokenClockSkew); err != nil {
		return validatedToken{}, fmt.Errorf("mcp: rs: token claims: %w", err)
	}
	// exp PRESENCE is mandatory: go-jose only enforces expiry when the claim
	// exists, so an exp-less token would otherwise become a never-expiring bearer.
	// A JWT has no liveness signal besides exp (unlike introspection's active flag)
	// — RFC 9068 §4 requires the check, which requires the claim.
	if std.Expiry == nil {
		return validatedToken{}, fmt.Errorf("mcp: rs: token has no exp claim (RFC 9068 §4: expiry is mandatory)")
	}
	// F-06: sub is mandatory (mirror idjag.go's IDJAG assertion check). Every PEP decision
	// downstream (audit actor, HITL plan hash, COAZ subject, per-tool approval) keys off the
	// Subject; an empty sub would produce an unattributable, plan-colliding identity, so a
	// subject-less token is rejected here rather than admitted as an anonymous principal.
	if strings.TrimSpace(std.Subject) == "" {
		return validatedToken{}, fmt.Errorf("mcp: rs: token has no sub claim (subject is mandatory)")
	}
	scopes := scopesFromString(ext.Scope)
	for _, s := range ext.Scp {
		if s = strings.TrimSpace(s); s != "" {
			scopes[s] = struct{}{}
		}
	}
	cnf, err := confirmationFromClaim(raw["cnf"])
	if err != nil {
		return validatedToken{}, err
	}
	actAs, delegated, err := delegationFromClaims(raw)
	if err != nil {
		return validatedToken{}, err
	}
	return validatedToken{
		Subject:         std.Subject,
		IsDelegated:     delegated,
		ActAs:           actAs,
		ClientID:        clientIDFromClaims(raw),
		Scopes:          scopes,
		Roles:           rolesFromClaim(raw[tv.rolesClaimName()]),
		Issuer:          std.Issuer,
		Audience:        []string(std.Audience),
		TokenType:       "at+jwt",
		Binding:         tokenBindingBearer,
		ConfirmationJKT: strings.TrimSpace(cnf.JKT),
		ConfirmationX5T: strings.TrimSpace(cnf.X5T),
	}, nil
}

// clientIDFromClaims reads the normalized OAuth client identity: the RFC 9068
// client_id claim first, the OIDC azp claim as fallback. A malformed (non-string)
// claim yields "" rather than an error — the field is attribution/namespacing, not
// admission (admission stays on iss/aud/exp/sub).
func clientIDFromClaims(raw map[string]json.RawMessage) string {
	for _, claim := range []string{"client_id", "azp"} {
		if len(raw[claim]) == 0 {
			continue
		}
		var s string
		if json.Unmarshal(raw[claim], &s) == nil {
			if s = strings.TrimSpace(s); s != "" {
				return s
			}
		}
	}
	return ""
}

// validateOpaque introspects an opaque token (RFC 7662) against each trusted issuer's
// OWN endpoint with that issuer's OWN credentials (issuer-keyed), in declaration
// order. An opaque token does not say who minted it, so the endpoints — all
// operator-declared trusted ASes — are consulted until one answers ACTIVE:
//
//   - an inactive/erroring answer moves on to the next issuer (the token may belong
//     to another trusted AS; a single-issuer config behaves exactly as before);
//   - an ACTIVE answer is authoritative and terminal: its aud MUST contain this
//     server's resource URI, and a present iss MUST equal the issuer whose endpoint
//     produced it — an active answer claiming a DIFFERENT issuer is a hard reject
//     (cross-issuer confusion is an attack or a misconfiguration, never a reason to
//     keep trying). An absent iss (RFC 7662 makes it optional) attributes the token
//     to the endpoint's configured issuer — the endpoint itself is the trust anchor.
func (tv *tokenValidator) validateOpaque(ctx context.Context, raw string) (validatedToken, error) {
	var lastErr error
	for _, issuer := range tv.keyring.introspectors() {
		tok, terminal, err := tv.introspectAt(ctx, issuer, raw)
		if err == nil {
			return tok, nil
		}
		if terminal {
			return validatedToken{}, err
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("mcp: rs: no introspection endpoint configured")
	}
	return validatedToken{}, fmt.Errorf("mcp: rs: opaque token not recognized by any trusted issuer: %w", lastErr)
}

// introspectAt introspects raw at ONE trusted issuer's endpoint. terminal=true marks a
// hard reject that must not fall through to other issuers (an ACTIVE token failing the
// aud/iss binding); a non-terminal error lets the caller try the next trusted issuer.
func (tv *tokenValidator) introspectAt(ctx context.Context, issuer *trustedIssuer, raw string) (validatedToken, bool, error) {
	if err := validateOutboundURL(ctx, issuer.introspectionURL); err != nil {
		return validatedToken{}, false, err
	}
	form := url.Values{"token": {raw}, "token_type_hint": {"access_token"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, issuer.introspectionURL, strings.NewReader(form.Encode()))
	if err != nil {
		return validatedToken{}, false, err
	}
	req.Header.Set("content-type", "application/x-www-form-urlencoded")
	req.Header.Set("accept", "application/json")
	if issuer.introspectAuth != "" {
		req.Header.Set("Authorization", issuer.introspectAuth)
	}
	resp, err := tv.doer.Do(req)
	if err != nil {
		return validatedToken{}, false, fmt.Errorf("mcp: rs: introspection at %q: %w", issuer.issuer, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxMetaBody))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return validatedToken{}, false, fmt.Errorf("mcp: rs: introspection at %q: http %d", issuer.issuer, resp.StatusCode)
	}
	var ir struct {
		Active bool        `json:"active"`
		Scope  string      `json:"scope"`
		Aud    audienceRaw `json:"aud"`
		Exp    int64       `json:"exp"`
		Sub    string      `json:"sub"`
		Iss    string      `json:"iss"`
	}
	if err := json.Unmarshal(body, &ir); err != nil {
		return validatedToken{}, false, fmt.Errorf("mcp: rs: decode introspection at %q: %w", issuer.issuer, err)
	}
	if !ir.Active {
		return validatedToken{}, false, fmt.Errorf("mcp: rs: token inactive (introspection at %q)", issuer.issuer)
	}
	// ACTIVE: this answer is authoritative — every failure below is terminal.
	if ir.Exp > 0 && tv.clock().After(time.Unix(ir.Exp, 0).Add(tokenClockSkew)) {
		return validatedToken{}, true, fmt.Errorf("mcp: rs: token expired (introspection)")
	}
	if !audienceContains(ir.Aud, tv.resource) {
		return validatedToken{}, true, fmt.Errorf("mcp: rs: token audience does not name this server (confused-deputy reject)")
	}
	// Issuer binding, simple string comparison: a present iss MUST be the issuer this
	// endpoint was configured for (no normalization — RFC 9068 §4 "exactly match").
	if ir.Iss != "" && ir.Iss != issuer.issuer {
		return validatedToken{}, true, fmt.Errorf("mcp: rs: introspection at %q returned a token of issuer %q (cross-issuer reject)", issuer.issuer, ir.Iss)
	}
	// F-06: sub is mandatory (mirror idjag.go and the JWT path) — an ACTIVE token with no
	// subject is unattributable and is a terminal reject, never an anonymous principal.
	if strings.TrimSpace(ir.Sub) == "" {
		return validatedToken{}, true, fmt.Errorf("mcp: rs: introspection at %q returned no sub (subject is mandatory)", issuer.issuer)
	}
	// Read the operator-configured role claim by name from the raw introspection response
	// (RFC 7662 responses are extensible — roles ride a non-standard claim, E1).
	var rawIR map[string]json.RawMessage
	_ = json.Unmarshal(body, &rawIR)
	cnf, err := confirmationFromClaim(rawIR["cnf"])
	if err != nil {
		return validatedToken{}, true, err
	}
	actAs, delegated, err := delegationFromClaims(rawIR)
	if err != nil {
		return validatedToken{}, true, err
	}
	return validatedToken{
		Subject:         ir.Sub,
		IsDelegated:     delegated,
		ActAs:           actAs,
		ClientID:        clientIDFromClaims(rawIR),
		Scopes:          scopesFromString(ir.Scope),
		Roles:           rolesFromClaim(rawIR[tv.rolesClaimName()]),
		Issuer:          issuer.issuer,
		Audience:        []string(ir.Aud),
		TokenType:       "opaque",
		Binding:         tokenBindingBearer,
		ConfirmationJKT: strings.TrimSpace(cnf.JKT),
		ConfirmationX5T: strings.TrimSpace(cnf.X5T),
	}, false, nil
}

// delegationFromClaims reads the same minimal delegation vocabulary the core
// principal exposes to Cedar and the audit ledger. act_as is the effective
// on-behalf-of subject; is_delegated, when present, must agree with it. A token
// claiming delegation without naming the effective subject is unattributable and
// therefore rejected rather than admitted with incomplete evidence.
func delegationFromClaims(raw map[string]json.RawMessage) (string, bool, error) {
	var actAs string
	if claim := raw["act_as"]; len(claim) > 0 && strings.TrimSpace(string(claim)) != "null" {
		if err := json.Unmarshal(claim, &actAs); err != nil {
			return "", false, fmt.Errorf("mcp: rs: token act_as claim must be a string: %w", err)
		}
		actAs = strings.TrimSpace(actAs)
	}

	var declared *bool
	if claim := raw["is_delegated"]; len(claim) > 0 && strings.TrimSpace(string(claim)) != "null" {
		var value bool
		if err := json.Unmarshal(claim, &value); err != nil {
			return "", false, fmt.Errorf("mcp: rs: token is_delegated claim must be a boolean: %w", err)
		}
		declared = &value
	}
	delegated := actAs != ""
	if declared != nil && *declared != delegated {
		return "", false, fmt.Errorf("mcp: rs: token delegation claims are inconsistent (is_delegated=%t, act_as present=%t)", *declared, delegated)
	}
	return actAs, delegated, nil
}

func confirmationFromClaim(raw json.RawMessage) (tokenConfirmation, error) {
	if len(raw) == 0 || strings.TrimSpace(string(raw)) == "null" {
		return tokenConfirmation{}, nil
	}
	var cnf tokenConfirmation
	if err := json.Unmarshal(raw, &cnf); err != nil {
		return tokenConfirmation{}, fmt.Errorf("mcp: rs: token cnf claim is not a valid confirmation object: %w", err)
	}
	// A non-empty cnf whose confirmation method is NOT one this RS can verify
	// (jkt / x5t#S256) is a sender-constrained token we cannot check — e.g. a
	// RFC 7800 jwe/kid confirmation. Accepting it as a plain bearer would defeat
	// the constraint (the idjag.go cnf posture), so it is refused fail-closed.
	if strings.TrimSpace(cnf.JKT) == "" && strings.TrimSpace(cnf.X5T) == "" {
		var members map[string]json.RawMessage
		if err := json.Unmarshal(raw, &members); err != nil || len(members) > 0 {
			return tokenConfirmation{}, fmt.Errorf("mcp: rs: token cnf carries no verifiable confirmation method (jkt or x5t#S256)")
		}
	}
	return cnf, nil
}

// parseJWKSBytes parses an operator-supplied inline JWK Set (the token trust anchor).
// A set that parses but holds NO keys (`{}`, `{"keys":[]}`, `null`) is refused: it
// would satisfy the anchor-presence check while leaving the issuer permanently dead
// — the misconfig class fail-closed construction exists to catch.
func parseJWKSBytes(raw []byte) (*jose.JSONWebKeySet, error) {
	var set jose.JSONWebKeySet
	if err := json.Unmarshal(raw, &set); err != nil {
		return nil, fmt.Errorf("mcp: rs: parse issuer_jwks: %w", err)
	}
	if len(set.Keys) == 0 {
		return nil, fmt.Errorf("mcp: rs: issuer_jwks contains no keys (an empty key set is not a trust anchor)")
	}
	return &set, nil
}

// keyFromSet returns the verification key for kid (kid match first, then a single-key
// fallback), or nil.
func keyFromSet(set *jose.JSONWebKeySet, kid string) *jose.JSONWebKey {
	if set == nil {
		return nil
	}
	if kid != "" {
		if ks := set.Key(kid); len(ks) > 0 {
			return &ks[0]
		}
	}
	if len(set.Keys) == 1 {
		return &set.Keys[0]
	}
	return nil
}

func (tv *tokenValidator) clock() time.Time {
	if tv.now != nil {
		return tv.now()
	}
	return time.Now()
}

// audienceRaw decodes a JSON aud claim that may be a single string or an array of
// strings (RFC 7662 / RFC 7519 both allow either form).
type audienceRaw []string

func (a *audienceRaw) UnmarshalJSON(b []byte) error {
	var one string
	if json.Unmarshal(b, &one) == nil {
		*a = audienceRaw{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return err
	}
	*a = audienceRaw(many)
	return nil
}

func audienceContains(aud audienceRaw, want string) bool {
	for _, a := range aud {
		if a == want {
			return true
		}
	}
	return false
}

// headerType reads the JOSE `typ` header value (RFC 9068 access-token type), or ""
// when absent.
func headerType(h jose.Header) string {
	if h.ExtraHeaders == nil {
		return ""
	}
	if v, ok := h.ExtraHeaders[jose.HeaderType]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// isAccessTokenType reports whether typ is the RFC 9068 access-token media type
// (at+jwt / application/at+jwt), case-insensitively.
func isAccessTokenType(typ string) bool {
	t := strings.ToLower(strings.TrimSpace(typ))
	return t == "at+jwt" || t == "application/at+jwt"
}
