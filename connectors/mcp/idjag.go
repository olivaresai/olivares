// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	jwt "github.com/go-jose/go-jose/v4/jwt"
)

// idjag.go validates Identity Assertion JWT Authorization Grants (ID-JAG) per the
// MCP Enterprise-Managed Authorization stable extension in the
// modelcontextprotocol/ext-auth repository
// (specification/stable/enterprise-managed-authorization.mdx, Status: Stable,
// announced 2026-06-18). EMA profiles the IETF
// draft-ietf-oauth-identity-assertion-authz-grant pattern now formally named
// Cross-App Access (XAA). This implementation is pinned to draft -04 (published
// 2026-05-21, expires 2026-11-22, PRE-STANDARD); future draft revisions must be
// tracked before changing validation. Verified that -03→-04 changed no URNs,
// REQUIRED claims, or typ.
//
// Roles, precisely: the enterprise IdP ISSUES the ID-JAG (after evaluating admin
// policy); the MCP client PRESENTS it to the MCP server's AUTHORIZATION SERVER with
// the RFC 7523 jwt-bearer grant; that AS VALIDATES it and mints the MCP access
// token; the RS PEP (rs.go) then validates the ACCESS token as always. This
// validator is the receiving-AS validation step, packaged so the operator's AS — or
// a future plane-side token endpoint — enforces the profile uniformly. The plane's
// internal registry of approved MCP servers (internalregistry.go) is the
// POLICY INPUT: an ID-JAG whose resource is not an operator-approved server is
// refused, so an enterprise IdP integration can only ever delegate access to
// servers the org has vetted (shadow servers are unreachable by construction).
//
// Validation implements the EMA stable profile + draft -04 §4.4.1 fail-closed:
//
//	typ MUST be oauth-id-jag+jwt (RFC 8725 §3.11) · iss MUST be a configured
//	trusted IdP (issuer-keyed JWKS, simple string comparison — issuertrust.go) ·
//	signature against THAT IdP's keys · aud MUST be this AS's issuer identifier
//	(string, or array of EXACTLY one) · client_id MUST identify the same client as
//	the request's client authentication · exp/iat/jti REQUIRED (jti replay-checked
//	in-process — defense-in-depth; the draft delegates replay to RFC 7521/7523) ·
//	sub REQUIRED · a cnf claim is REFUSED (it demands DPoP proof-of-possession,
//	which this validator does not consume — per §9.8 a cnf without a proof MUST be
//	rejected) · resource REQUIRED by this implementation and MUST resolve to an
//	approved resource. The EMA profile itself makes resource optional but says the
//	target MCP server's RFC 9728 resource identifier MUST be used if present; our
//	registry gate deliberately tightens that to required so delegation is pinned to
//	an operator-approved server and fails closed for shadow resources.
//
// Every rejection maps to the OAuth error code invalid_grant (draft §4.4.1);
// errors.Is(err, ErrIDJAGInvalidGrant) lets a token-endpoint adapter answer
// RFC 6749 §5.2 correctly without parsing strings.

// idjagTyp is the REQUIRED JWT typ of an ID-JAG (ext-auth §4.3 / draft §3.1).
const idjagTyp = "oauth-id-jag+jwt"

// ErrIDJAGInvalidGrant marks every ID-JAG validation failure (the draft maps them
// all to the invalid_grant token error).
var ErrIDJAGInvalidGrant = errors.New("invalid_grant")

// idjagReject builds a wrapped invalid_grant rejection.
func idjagReject(format string, args ...any) error {
	return fmt.Errorf("mcp: idjag: "+format+": %w", append(args, ErrIDJAGInvalidGrant)...)
}

// IDJAG is a VALIDATED Identity Assertion JWT Authorization Grant — the claims an
// authorization server needs to mint the MCP access token (subject resolution,
// scope/resource narrowing) and the plane needs to audit the delegation.
type IDJAG struct {
	Issuer    string   // the enterprise IdP that issued the grant
	Subject   string   // the IdP's subject identifier for the end user (unique per iss+sub)
	ClientID  string   // the MCP client this grant authorizes (matches the request's client auth)
	Audience  string   // this authorization server's issuer identifier
	Resources []string // the approved MCP server resource URI(s) the grant targets
	Scopes    []string // requested scopes (the AS MAY narrow them)
	JTI       string   // grant id (replay-checked)
	Email     string   // optional account-linking hint (ext-auth implementation note)
	Tenant    string   // optional multi-tenant issuer context
	Expiry    time.Time
	IssuedAt  time.Time

	// IssuerClaimedDomains are the email domains the VALIDATED issuer is authoritative
	// for (its operator-configured trust-anchor claim, IssuerTrust.ClaimedDomains) —
	// NOT read off the wire. Empty ⇒ the issuer claims no domains. A cmd/ bridge copies
	// this into auth.EMAResult so the engine can gate the EMA verified-email fallback on
	// domain authority (the cross-IdP account-takeover boundary, F1).
	IssuerClaimedDomains []string
	// SoleTrustedIssuer is true when this validator's trust set holds EXACTLY ONE
	// issuer, so an unconstrained (no-claimed-domains) issuer is legitimately
	// authoritative for every account — the single-IdP deployment. In a multi-issuer
	// keyring an unconstrained issuer must not vouch by bare email.
	SoleTrustedIssuer bool
}

// IDJAGValidatorConfig configures the receiving-side validator. TrustedIDPs is the
// issuer-keyed trust set (JWKS inline or URL; introspection fields are unused here).
// Audience is THIS authorization server's RFC 8414 issuer identifier — the exact
// value an ID-JAG's aud must carry. ApprovedResources is the registry-derived policy
// input (Source.EnterpriseAuthPolicyInput, or operator-supplied): canonical resource
// URIs of the MCP servers the org approves as delegation targets. DENY-CLOSED: an
// empty set refuses every grant — approval is explicit, never implied.
type IDJAGValidatorConfig struct {
	TrustedIDPs       []IssuerTrust
	Audience          string
	ApprovedResources []string
	Clock             func() time.Time
	Doer              httpDoer
}

// IDJAGValidator validates ID-JAGs. Safe for concurrent use.
type IDJAGValidator struct {
	keyring  *issuerKeyring
	audience string
	approved map[string]struct{}
	doer     httpDoer
	now      func() time.Time

	mu   sync.Mutex
	seen map[string]time.Time // (iss + "\n" + jti) → exp, the replay window
}

// maxReplayEntries bounds the in-process jti replay cache; expired entries are
// pruned on insert and the oldest-expiring entries are evicted past the cap.
const maxReplayEntries = 16384

// NewIDJAGValidator builds the validator fail-closed: no trusted IdP, an IdP entry
// without an issuer or JWKS anchor, or a missing audience is a construction error.
func NewIDJAGValidator(cfg IDJAGValidatorConfig) (*IDJAGValidator, error) {
	if strings.TrimSpace(cfg.Audience) == "" {
		return nil, fmt.Errorf("mcp: idjag: an audience (this authorization server's issuer identifier) is required")
	}
	keyring, err := newIssuerKeyring(cfg.TrustedIDPs)
	if err != nil {
		return nil, fmt.Errorf("mcp: idjag: trusted IdPs: %w", err)
	}
	for _, ti := range keyring.issuers {
		if ti.anchor == nil && ti.jwksURL == "" {
			return nil, fmt.Errorf("mcp: idjag: trusted IdP %q has no JWKS anchor (ID-JAG validation is signature-based; introspection does not apply)", ti.issuer)
		}
	}
	approved := map[string]struct{}{}
	for _, r := range cfg.ApprovedResources {
		if r = strings.TrimSpace(r); r != "" {
			approved[r] = struct{}{}
		}
	}
	doer := cfg.Doer
	if doer == nil {
		doer = ssrfSafeClient()
	}
	clock := cfg.Clock
	if clock == nil {
		clock = time.Now
	}
	return &IDJAGValidator{
		keyring:  keyring,
		audience: strings.TrimSpace(cfg.Audience),
		approved: approved,
		doer:     doer,
		now:      clock,
		seen:     map[string]time.Time{},
	}, nil
}

// idjagClaims is the raw claim shape needed beyond jwt.Claims: aud is kept raw to
// enforce the draft's arity rule (string, or array of EXACTLY one element);
// resource may be a single URI or an array of URIs.
type idjagClaims struct {
	Aud      json.RawMessage `json:"aud"`
	ClientID string          `json:"client_id"`
	Resource json.RawMessage `json:"resource"`
	Scope    string          `json:"scope"`
	Cnf      json.RawMessage `json:"cnf"`
	Email    string          `json:"email"`
	Tenant   string          `json:"tenant"`
}

// Validate authenticates and authorizes one ID-JAG presented by the client
// authenticated as authenticatedClientID (the client binding of draft §4.4.1: the
// assertion's client_id MUST identify the same client as the request's client
// authentication). Every rejection wraps ErrIDJAGInvalidGrant.
func (v *IDJAGValidator) Validate(ctx context.Context, assertion, authenticatedClientID string) (IDJAG, error) {
	assertion = strings.TrimSpace(assertion)
	if assertion == "" {
		return IDJAG{}, idjagReject("empty assertion")
	}
	parsed, err := jwt.ParseSigned(assertion, mcpAllowedAlgs)
	if err != nil {
		return IDJAG{}, idjagReject("assertion is not a verifiable JWS (%v)", err)
	}
	if len(parsed.Headers) == 0 {
		return IDJAG{}, idjagReject("assertion has no JOSE header")
	}
	// typ MUST be oauth-id-jag+jwt (RFC 8725 §3.11 typed JWTs): an ID token, access
	// token or any other JWT replayed as a grant fails here, before any crypto.
	if !isIDJAGType(headerType(parsed.Headers[0])) {
		return IDJAG{}, idjagReject("assertion typ is not %q", idjagTyp)
	}

	// Issuer trust: the UNVERIFIED iss selects the IdP (simple string comparison);
	// the verified claims re-check it below. Unknown issuer ⇒ refused before any
	// signature work.
	var unverified jwt.Claims
	if err := parsed.UnsafeClaimsWithoutVerification(&unverified); err != nil {
		return IDJAG{}, idjagReject("assertion claims unreadable (%v)", err)
	}
	if unverified.Issuer == "" {
		return IDJAG{}, idjagReject("assertion carries no iss claim")
	}
	idp, ok := v.keyring.lookup(unverified.Issuer)
	if !ok {
		return IDJAG{}, idjagReject("assertion issuer is not a trusted enterprise IdP")
	}
	key, err := idp.resolveKey(ctx, v.doer, parsed.Headers[0].KeyID, v.now())
	if err != nil {
		return IDJAG{}, idjagReject("no verification key (%v)", err)
	}
	var std jwt.Claims
	var ext idjagClaims
	if err := parsed.Claims(key, &std, &ext); err != nil {
		return IDJAG{}, idjagReject("assertion signature (%v)", err)
	}

	// Verified-claim validation. Audience arity first (draft §4.4.1): aud MUST be
	// this AS's issuer identifier — a string, or an array of EXACTLY one element.
	aud, err := idjagAudience(ext.Aud)
	if err != nil {
		return IDJAG{}, err
	}
	if aud != v.audience {
		return IDJAG{}, idjagReject("assertion aud %q is not this authorization server", aud)
	}
	// iss (verified) must still be the selected IdP; exp/iat are REQUIRED and
	// checked with the shared skew leeway.
	if err := std.ValidateWithLeeway(jwt.Expected{Issuer: idp.issuer, Time: v.now()}, tokenClockSkew); err != nil {
		return IDJAG{}, idjagReject("assertion claims (%v)", err)
	}
	if std.Expiry == nil || std.IssuedAt == nil {
		return IDJAG{}, idjagReject("exp and iat are REQUIRED")
	}
	if std.Subject == "" {
		return IDJAG{}, idjagReject("sub is REQUIRED")
	}
	if std.ID == "" {
		return IDJAG{}, idjagReject("jti is REQUIRED")
	}
	// Client binding (draft §4.4.1): the grant authorizes ONE client — the one
	// authenticated on the token request. Simple string comparison.
	if ext.ClientID == "" {
		return IDJAG{}, idjagReject("client_id is REQUIRED")
	}
	if authenticatedClientID == "" || ext.ClientID != authenticatedClientID {
		return IDJAG{}, idjagReject("assertion client_id does not identify the authenticated client")
	}
	// Sender-constraining (draft §9.8): a cnf claim demands a DPoP proof this
	// validator does not consume — and a cnf WITHOUT its proof MUST be rejected.
	// Fail-closed rather than silently dropping the constraint.
	if len(ext.Cnf) > 0 && string(ext.Cnf) != "null" {
		return IDJAG{}, idjagReject("assertion carries a cnf (proof-of-possession) claim but no DPoP proof is consumed here")
	}
	// Resource policy: the EMA profile makes resource optional-but-pinned-if-present;
	// this validator deliberately requires it so the approved-server registry can
	// deny closed. Unknown resource — or an empty approved set — refuses the grant.
	resources, err := idjagResources(ext.Resource)
	if err != nil {
		return IDJAG{}, err
	}
	for _, r := range resources {
		if _, ok := v.approved[r]; !ok {
			return IDJAG{}, idjagReject("resource %q is not an approved MCP server (enterprise policy input)", r)
		}
	}
	// Replay (defense-in-depth; keyed by iss+jti — jti uniqueness is per issuer).
	// LAST check, deliberately: only a grant that passed every validation above is
	// recorded as redeemed — a grant refused (e.g. by resource policy) stays
	// re-presentable once the operator approves the resource, while a grant that
	// was ACCEPTED can never be redeemed twice.
	if err := v.recordJTI(idp.issuer, std.ID, std.Expiry.Time()); err != nil {
		return IDJAG{}, err
	}

	return IDJAG{
		Issuer:               idp.issuer,
		Subject:              std.Subject,
		ClientID:             ext.ClientID,
		Audience:             aud,
		Resources:            resources,
		Scopes:               strings.Fields(ext.Scope),
		JTI:                  std.ID,
		Email:                ext.Email,
		Tenant:               ext.Tenant,
		Expiry:               std.Expiry.Time(),
		IssuedAt:             std.IssuedAt.Time(),
		IssuerClaimedDomains: idp.claimedDomains,
		SoleTrustedIssuer:    v.keyring.size() == 1,
	}, nil
}

// recordJTI registers (issuer, jti) in the replay window. A non-nil error means the
// grant must be REFUSED: either the jti was already redeemed (replay), or the window
// is at capacity with UNEXPIRED entries — evicting an unexpired jti would re-open
// its replay window, so the validator refuses NEW grants instead (deny-closed: the
// replay invariant of already-accepted grants is never traded for availability;
// 16k+ unexpired in-flight grants is pathological and bounded by grant lifetimes).
// Expired entries are pruned on insert.
func (v *IDJAGValidator) recordJTI(issuer, jti string, exp time.Time) error {
	key := issuer + "\n" + jti
	now := v.now()
	v.mu.Lock()
	defer v.mu.Unlock()
	if prevExp, dup := v.seen[key]; dup && now.Before(prevExp.Add(tokenClockSkew)) {
		return idjagReject("assertion jti was already redeemed (replay)")
	}
	for k, e := range v.seen {
		if now.After(e.Add(tokenClockSkew)) {
			delete(v.seen, k)
		}
	}
	if len(v.seen) >= maxReplayEntries {
		return idjagReject("replay window at capacity — refusing to validate without replay protection")
	}
	v.seen[key] = exp
	return nil
}

// isIDJAGType reports whether typ is the ID-JAG media type, tolerating the optional
// application/ prefix case-insensitively (the same RFC 7515 §4.1.9 reading RFC 9068
// uses for at+jwt).
func isIDJAGType(typ string) bool {
	t := strings.ToLower(strings.TrimSpace(typ))
	return t == idjagTyp || t == "application/"+idjagTyp
}

// idjagAudience enforces the draft §4.4.1 aud arity: a string, or an array of
// EXACTLY one element.
func idjagAudience(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", idjagReject("aud is REQUIRED")
	}
	var one string
	if json.Unmarshal(raw, &one) == nil {
		if one == "" {
			return "", idjagReject("aud is empty")
		}
		return one, nil
	}
	var many []string
	if json.Unmarshal(raw, &many) != nil {
		return "", idjagReject("aud is neither a string nor an array")
	}
	if len(many) != 1 {
		return "", idjagReject("aud array must contain exactly one element (got %d)", len(many))
	}
	if many[0] == "" {
		return "", idjagReject("aud is empty")
	}
	return many[0], nil
}

// idjagResources parses the resource claim — REQUIRED by this plane's deny-closed
// registry policy (stricter than the EMA profile's optional-but-pinned-if-present
// rule). A single URI or an array of URIs is accepted per the base draft.
func idjagResources(raw json.RawMessage) ([]string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, idjagReject("resource is REQUIRED (the MCP profile pins the grant to a specific MCP server)")
	}
	var one string
	if json.Unmarshal(raw, &one) == nil {
		if strings.TrimSpace(one) == "" {
			return nil, idjagReject("resource is empty")
		}
		return []string{one}, nil
	}
	var many []string
	if json.Unmarshal(raw, &many) != nil {
		return nil, idjagReject("resource is neither a string nor an array")
	}
	out := make([]string, 0, len(many))
	for _, r := range many {
		if strings.TrimSpace(r) == "" {
			return nil, idjagReject("resource array contains an empty entry")
		}
		out = append(out, r)
	}
	if len(out) == 0 {
		return nil, idjagReject("resource array is empty")
	}
	return out, nil
}

// --- registry policy input (the plane's approved-server registry feeds
// --- enterprise IdP policy and the validator's ApprovedResources) ---------------

// ApprovedResource is one operator-approved MCP server rendered as an
// Enterprise-Managed Authorization policy entry: the canonical RFC 8707/9728
// resource URI an ID-JAG may target, plus the registry identity behind the
// approval. This is what an operator feeds the enterprise IdP (Okta Cross App
// Access resource-app config, Entra agent policies) and the IDJAGValidator.
type ApprovedResource struct {
	Resource      string `json:"resource"`
	Name          string `json:"name"`
	RegistryName  string `json:"registry_name,omitempty"`
	PinnedVersion string `json:"pinned_version,omitempty"`
}

// EnterpriseAuthPolicyInput renders the internal registry as the
// Enterprise-Managed Authorization policy input: every CONFIGURED HTTP server that
// the operator's internal registry recognizes (org-owned namespace or approved
// entry), with its canonical resource URI. Servers the registry does not recognize
// are EXCLUDED — a shadow server never becomes a delegation target. stdio servers
// have no resource URI and do not participate. Deterministic order (by resource).
func (s *Source) EnterpriseAuthPolicyInput() []ApprovedResource {
	var out []ApprovedResource
	for _, spec := range s.cfg.servers {
		if strings.TrimSpace(spec.URL) == "" {
			continue
		}
		entry, recognized := s.internal.approved(spec)
		if _, owned := s.internal.owns(spec.RegistryName); !owned && !recognized {
			continue
		}
		// An explicit auth.resource override is used VERBATIM — the same value the
		// OAuth client sends as the RFC 8707 resource indicator (newOAuthClient).
		// Canonicalizing here but not there would make policy and token disagree;
		// the operator owns the exact string.
		resource := ""
		if spec.Auth != nil {
			resource = strings.TrimSpace(spec.Auth.Resource)
		}
		if resource == "" {
			c, err := canonicalResourceURI(spec.URL)
			if err != nil {
				continue
			}
			resource = c
		}
		out = append(out, ApprovedResource{
			Resource:      resource,
			Name:          spec.Name,
			RegistryName:  spec.RegistryName,
			PinnedVersion: strings.TrimSpace(entry.Version),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Resource < out[j].Resource })
	return out
}
