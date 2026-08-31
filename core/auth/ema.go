// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Enterprise-Managed Authorization (EMA) — the SSO zero-touch MCP flow.
//
// Normative profile: modelcontextprotocol/ext-auth
// specification/stable/enterprise-managed-authorization.mdx, Status: Stable
// (announced 2026-06-18). The stable profile pins IETF
// draft-ietf-oauth-identity-assertion-authz-grant-04 (published 2026-05-21,
// expires 2026-11-22, PRE-STANDARD), the draft that formalizes Cross-App Access
// (XAA). Track future draft revisions before changing the ID-JAG wire contract.
//
//  1. Enterprise IdP (Okta/Entra) issues an ID-JAG assertion to the MCP client
//  2. MCP client presents the assertion to this AS via the jwt-bearer grant
//     (urn:ietf:params:oauth:grant-type:jwt-bearer, RFC 7523)
//  3. This AS validates the ID-JAG, resolves the IdP subject to a local user,
//     and mints an audience-bound access token (at+jwt) for the target MCP server
//  4. MCP client uses the access token at the MCP server's RS PEP
//
// The EMA profile is adopted by Asana, Atlassian, Figma, Linear, Supabase
// (MCP servers) and Claude/VS Code (MCP clients). This implementation wires
// the existing ID-JAG validator (connectors/mcp/idjag.go) and token-minting
// infrastructure into the plane's standard /v1/auth/token endpoint.
//
// Interop note: the stable EMA token-endpoint example authenticates a CIMD public
// client with a bare client_id and no secret. This endpoint currently requires
// HTTP Basic and therefore confidential clients only; public-client auth remains a
// deliberate pending operator-policy decision, not an implicit fallback.
//
// COAZ (AuthZEN Profile for MCP Tool Authorization) constants define the
// vocabulary the AuthZEN PDP uses to evaluate MCP tool-authorization queries:
// MCP-specific subject types, action names, and resource types that map
// tools/call decisions into the standard Subject-Action-Resource-Context model.

// GrantTypeJWTBearer is the RFC 7523 jwt-bearer grant type used by EMA.
const GrantTypeJWTBearer = "urn:ietf:params:oauth:grant-type:jwt-bearer"

// COAZ subject, action, and resource type constants — the vocabulary the
// AuthZEN PDP accepts for MCP tool-authorization evaluations.
const (
	COAZSubjectMCPClient = "mcp_client"

	COAZActionToolsCall     = "mcp:tools/call"
	COAZActionToolsList     = "mcp:tools/list"
	COAZActionResourcesRead = "mcp:resources/read"
	COAZActionElicitation   = "mcp:elicitation/create"
	COAZActionSampling      = "mcp:sampling/createMessage"

	COAZResourceTool   = "mcp_tool"
	COAZResourceServer = "mcp_server"
)

// COAZ context keys — the keys callers pass in the AuthZEN context map to
// enrich an MCP authorization query with deployment-specific metadata.
const (
	COAZCtxServer      = "mcp_server"
	COAZCtxScope       = "mcp_scope"
	COAZCtxAnnotations = "mcp_annotations"
	COAZCtxEMAIssuer   = "ema_issuer"
	COAZCtxTenant      = "tenant"
)

// ErrEMAUnavailable is returned when the EMA grant handler is not configured.
var ErrEMAUnavailable = errors.New("auth: EMA grant unavailable")

// EMAResult is the validated output of an ID-JAG assertion in the EMA flow.
// It carries the IdP-asserted identity so the grant handler can resolve it
// to a local principal and mint an access token.
type EMAResult struct {
	Issuer    string
	Subject   string
	ClientID  string
	Resources []string
	Scopes    []string
	Email     string
	Tenant    string
	Expiry    time.Time
	IssuedAt  time.Time

	// IssuerClaimedDomains are the email domains the VALIDATED issuer is
	// authoritative for (its operator-configured trusted-IdP claim), mirroring the
	// first-party SSO domain boundary (ResolvedIdP.ClaimedDomains U5). The
	// receiver populates it from the trust anchor that verified the assertion; it is
	// NOT read off the wire. Empty ⇒ the issuer claims no domains (see
	// SoleTrustedIssuer). It gates the verified-email fallback in resolveIDPSubject
	// so a trusted-but-domain-scoped issuer cannot vouch by bare email for an
	// account in a domain it does not own (the cross-IdP EMA takeover, F1).
	IssuerClaimedDomains []string
	// SoleTrustedIssuer is true when the receiver's trust set holds EXACTLY ONE
	// issuer. An issuer that claims no domains is authoritative for every account
	// ONLY in that single-IdP deployment (preserving pre-U3 SCIM email-fallback);
	// in a multi-issuer keyring an unconstrained issuer must NOT use the bare-email
	// fallback — that is precisely the takeover vector.
	SoleTrustedIssuer bool
}

// EMAReceiver validates an inbound ID-JAG assertion in the EMA jwt-bearer
// grant flow. The concrete implementation wraps the MCP connector's
// IDJAGValidator; the interface lets core/api call it without importing the
// connector package (boundary: connectors never import from /core, and core
// never imports from /connectors — the wire-up in cmd/ bridges them).
type EMAReceiver interface {
	ValidateAssertion(ctx context.Context, assertion, authenticatedClientID string) (EMAResult, error)
}

// EMAGrantConfig configures the EMA jwt-bearer grant handler.
type EMAGrantConfig struct {
	// Receiver validates inbound ID-JAG assertions.
	Receiver EMAReceiver
	// SigningKey signs the minted at+jwt access tokens.
	SigningKey ed25519.PrivateKey
	// Issuer is this AS's issuer identifier (the iss of the minted token).
	Issuer string
	// TokenTTL is the minted access-token lifetime (default 15 minutes).
	TokenTTL time.Duration
	// Clock overrides time.Now (for tests).
	Clock func() time.Time
}

// EMAGrant is the EMA jwt-bearer grant handler: it validates an ID-JAG,
// resolves the IdP subject to a local user, and mints an audience-bound
// at+jwt access token. Safe for concurrent use after construction.
type EMAGrant struct {
	receiver EMAReceiver
	signer   jose.Signer
	issuer   string
	tokenTTL time.Duration
	clock    func() time.Time
	authr    *Authenticator
}

// NewEMAGrant builds the grant handler. Deny-closed: a nil receiver, missing
// signing key, or empty issuer is a construction error — the handler never
// falls back to an unsecured path.
func NewEMAGrant(cfg EMAGrantConfig, authr *Authenticator) (*EMAGrant, error) {
	if cfg.Receiver == nil {
		return nil, fmt.Errorf("%w: no EMA receiver configured", ErrEMAUnavailable)
	}
	if len(cfg.SigningKey) == 0 {
		return nil, fmt.Errorf("%w: no signing key for access-token minting", ErrEMAUnavailable)
	}
	if cfg.Issuer == "" {
		return nil, fmt.Errorf("%w: issuer identifier is required", ErrEMAUnavailable)
	}
	if authr == nil {
		return nil, fmt.Errorf("%w: authenticator is required", ErrEMAUnavailable)
	}
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.EdDSA, Key: cfg.SigningKey},
		(&jose.SignerOptions{}).WithType(jose.ContentType("at+jwt")),
	)
	if err != nil {
		return nil, fmt.Errorf("ema: access-token signer: %w", err)
	}
	ttl := cfg.TokenTTL
	if ttl <= 0 {
		ttl = 15 * time.Minute
	}
	clock := cfg.Clock
	if clock == nil {
		clock = time.Now
	}
	return &EMAGrant{
		receiver: cfg.Receiver,
		signer:   signer,
		issuer:   cfg.Issuer,
		tokenTTL: ttl,
		clock:    clock,
		authr:    authr,
	}, nil
}

// Issuer reports the AS issuer identifier this grant handler mints tokens
// under — the value the RFC 8414 metadata document advertises as
// `issuer`, so discovery and minting can never disagree.
func (g *EMAGrant) Issuer() string {
	if g == nil {
		return ""
	}
	return g.issuer
}

// EMATokenResponse is the RFC 6749 §5.1 token response for a successful
// EMA jwt-bearer grant.
type EMATokenResponse struct {
	AccessToken string
	TokenType   string
	ExpiresIn   int64
	Scope       string
}

// Grant executes the EMA jwt-bearer grant: validates the assertion, resolves
// the identity, mints the access token. Every rejection returns an error that
// wraps either ErrIDJAGInvalidGrant (assertion validation failure) or
// ErrInvalidExchange (identity resolution / minting failure).
func (g *EMAGrant) Grant(ctx context.Context, assertion, authenticatedClientID, resource string, requestedScopes []string) (EMATokenResponse, error) {
	if g == nil {
		return EMATokenResponse{}, ErrEMAUnavailable
	}

	// Phase 1: validate the ID-JAG assertion.
	result, err := g.receiver.ValidateAssertion(ctx, assertion, authenticatedClientID)
	if err != nil {
		return EMATokenResponse{}, err
	}

	// Phase 2: resolve the IdP subject to a local user principal.
	// Resolution order: issuer-qualified SSO subject (cross-IdP-safe), then the
	// verified-email hint gated on the asserting issuer's domain authority.
	p, found, err := g.resolveIDPSubject(ctx, result)
	if err != nil {
		return EMATokenResponse{}, fmt.Errorf("%w: identity resolution failed: %v", ErrInvalidExchange, err)
	}
	if !found {
		return EMATokenResponse{}, fmt.Errorf("%w: no linked local identity for IdP subject %q (issuer %q)", ErrInvalidExchange, result.Subject, result.Issuer)
	}
	// A superadmin — the cross-tenant/system root — must authenticate FIRST-PARTY, never
	// via an unattended IdP assertion. A superadmin is a local account with no sso_subject,
	// so it can only reach this point through the verified-email fallback; refuse it,
	// mirroring the SSO login path (CompleteSSO, federation_login.go:76-79) — a defense
	// against a (possibly second, differently-trusted) IdP asserting the superadmin's
	// email. Federated accounts are never superadmin, so this never blocks a legitimate
	// EMA exchange.
	if p.Superadmin {
		return EMATokenResponse{}, fmt.Errorf("%w: superadmin must authenticate first-party, not via an EMA assertion", ErrInvalidExchange)
	}

	// Phase 3: scope narrowing — the issued scopes are the intersection of the
	// ID-JAG's scopes and the requested scopes (the AS MAY narrow, per the spec).
	scopes := narrowScopes(result.Scopes, requestedScopes)

	// Phase 4: mint an audience-bound at+jwt access token.
	now := g.clock()
	exp := now.Add(g.tokenTTL)

	claims := emaClaims{
		Claims: jwt.Claims{
			Issuer:   g.issuer,
			Subject:  p.UserID.String(),
			Audience: jwt.Audience{resource},
			ID:       model.NewID().String(),
			IssuedAt: jwt.NewNumericDate(now),
			Expiry:   jwt.NewNumericDate(exp),
		},
		Scope:     strings.Join(scopes, " "),
		ClientID:  authenticatedClientID,
		EMAIssuer: result.Issuer,
	}
	token, err := jwt.Signed(g.signer).Claims(claims).Serialize()
	if err != nil {
		return EMATokenResponse{}, fmt.Errorf("ema: mint access token: %w", err)
	}

	return EMATokenResponse{
		AccessToken: token,
		TokenType:   "Bearer",
		ExpiresIn:   int64(g.tokenTTL.Seconds()),
		Scope:       strings.Join(scopes, " "),
	}, nil
}

// emaClaims are the at+jwt claims of an EMA-minted access token.
type emaClaims struct {
	jwt.Claims
	Scope     string `json:"scope,omitempty"`
	ClientID  string `json:"client_id"`
	EMAIssuer string `json:"ema_issuer,omitempty"`
}

// resolveIDPSubject maps a VALIDATED IdP identity (a verified issuer + subject) to a
// local user principal, using the SAME cross-IdP-safe correlation order as the SSO
// login path (findOrProvision U3 §D5):
//  1. the issuer-qualified SSO subject ("<issuer>\x1f<subject>") — a bare subject value
//     can never select an account provisioned by a DIFFERENT trusted issuer.
//  2. the verified email hint — the fallback for SCIM/pre-U3 accounts that carry no
//     subject binding yet, GATED on the asserting issuer's domain authority (see
//     emaEmailFallbackAuthoritative). A PURE READ: EMA never bootstraps a binding here.
//
// The unqualified external_id is deliberately NOT a correlation key: it shares one
// namespace across IdPs (RFC 7643 externalId), so matching it alone lets a second
// trusted issuer's assertion resolve to the first issuer's account. It stays SCIM's
// own PATCH/DELETE correlation key, never an authorization key.
//
// The email leg is the OTHER cross-IdP hazard (F1): without a domain-authority
// gate, a trusted-but-domain-scoped issuer in a multi-IdP keyring can assert a
// victim's email in a domain it does not own and seize that account. This mirrors the
// first-party SSO boundary (ResolvedIdP.AllowsEmail, handlers_federation.go:285): the
// fallback runs only when the asserting issuer is authoritative for the email's domain.
//
// The principal is built at AAL1 (a federated assertion without first-party
// authenticator verification; consistent with CompleteSSO).
func (g *EMAGrant) resolveIDPSubject(ctx context.Context, id EMAResult) (Principal, bool, error) {
	// 1. Issuer-qualified SSO subject — the cross-IdP-safe correlation key. Empty when
	//    the issuer is absent, in which case we fall back to the verified email only
	//    (never to a bare, un-namespaced subject).
	if qualified := (FederatedIdentity{Issuer: id.Issuer, Subject: id.Subject}).QualifiedSubject(); qualified != "" {
		p, found, err := g.authr.PrincipalForSSOSubject(ctx, qualified, AAL1)
		if err != nil {
			return Principal{}, false, err
		}
		if found {
			return p, true, nil
		}
	}
	// 2. Verified email fallback (pre-binding accounts) — a pure read, no binding write.
	//    DENY-CLOSED on domain authority: an issuer may vouch by bare email only for the
	//    domains it owns (or, unconstrained, only as the sole trusted issuer). Otherwise
	//    the email leg is skipped entirely — a foreign-domain assertion resolves nothing.
	if id.Email != "" && emaEmailFallbackAuthoritative(id.IssuerClaimedDomains, id.SoleTrustedIssuer, id.Email) {
		p, found, err := g.authr.PrincipalForUser(ctx, id.Email, AAL1)
		if err != nil {
			return Principal{}, false, err
		}
		if found {
			return p, true, nil
		}
	}
	return Principal{}, false, nil
}

// emaEmailFallbackAuthoritative reports whether an EMA issuer with the given claimed
// domains may resolve an account by the VERIFIED email hint — the domain boundary that
// stops a cross-IdP email takeover (F1). It mirrors the first-party SSO rule
// (ResolvedIdP.AllowsEmail U5) and adds the multi-issuer guard:
//
//   - an issuer that CLAIMS domains may vouch only for emails in those domains
//     (normalized both sides with model.NormalizeFederationDomain, matching AllowsEmail);
//   - an issuer that claims NO domains is authoritative for every account ONLY when it
//     is the SOLE trusted issuer (the single-IdP deployment, preserving pre-U3 SCIM
//     email fallback) — in a multi-issuer keyring an unconstrained issuer is denied the
//     bare-email leg, which is exactly the takeover vector.
func emaEmailFallbackAuthoritative(claimedDomains []string, soleTrustedIssuer bool, email string) bool {
	norm := make([]string, 0, len(claimedDomains))
	for _, d := range claimedDomains {
		if nd := model.NormalizeFederationDomain(d); nd != "" {
			norm = append(norm, nd)
		}
	}
	if len(norm) == 0 {
		// Unconstrained issuer: safe to vouch by bare email only as the single trusted IdP.
		return soleTrustedIssuer
	}
	// Constrained issuer: reuse the tested first-party domain boundary. AllowsEmail
	// normalizes the email side and compares to the (already-normalized) claimed set.
	return ResolvedIdP{ClaimedDomains: norm}.AllowsEmail(email)
}

// PrincipalForExternalID builds the authorization principal for the user
// matched by their IdP-provisioned external_id (the SCIM externalId or
// SSO subject). Returns (zero, false, nil) when no active user matches.
func (a *Authenticator) PrincipalForExternalID(ctx context.Context, externalID string, assurance int) (Principal, bool, error) {
	if strings.TrimSpace(externalID) == "" {
		return Principal{}, false, nil
	}
	var p Principal
	var found bool
	err := a.st.AuthView(ctx, func(as store.AuthScope) error {
		us, _, e := as.Users().List(ctx, byEq("external_id", strings.TrimSpace(externalID), 1))
		if e != nil {
			return e
		}
		if len(us) == 0 {
			return nil
		}
		u := us[0]
		if u.Status != model.StatusActive {
			return nil
		}
		grants, groups, confined, e := loadGrants(ctx, as, u.ID)
		if e != nil {
			return e
		}
		p = newPrincipal(KindUser, u.ID, u.ID, u.IsSuperadmin, u.DisplayName, grants, groups).withConfinements(confined)
		p.AAL = clampUserAAL(assurance)
		found = true
		return nil
	})
	return p, found, err
}

// PrincipalForSSOSubject builds the authorization principal for the user matched by
// their ISSUER-QUALIFIED SSO subject ("<issuer>\x1f<subject>" U3) — the
// cross-IdP-safe correlation key that a bare subject value cannot spoof (the collision
// an unqualified external_id could not prevent §D5). Returns (zero, false, nil)
// for an empty key — a user with no SSO binding stores NULL, which an empty lookup must
// never match — or when no ACTIVE user matches.
func (a *Authenticator) PrincipalForSSOSubject(ctx context.Context, qualifiedSubject string, assurance int) (Principal, bool, error) {
	if strings.TrimSpace(qualifiedSubject) == "" {
		return Principal{}, false, nil
	}
	var p Principal
	var found bool
	err := a.st.AuthView(ctx, func(as store.AuthScope) error {
		us, _, e := as.Users().List(ctx, byEq("sso_subject", strings.TrimSpace(qualifiedSubject), 1))
		if e != nil {
			return e
		}
		if len(us) == 0 {
			return nil
		}
		u := us[0]
		if u.Status != model.StatusActive {
			return nil
		}
		grants, groups, confined, e := loadGrants(ctx, as, u.ID)
		if e != nil {
			return e
		}
		p = newPrincipal(KindUser, u.ID, u.ID, u.IsSuperadmin, u.DisplayName, grants, groups).withConfinements(confined)
		p.AAL = clampUserAAL(assurance)
		found = true
		return nil
	})
	return p, found, err
}

// narrowScopes returns the intersection of granted and requested scopes.
// If requested is empty, all granted scopes are returned (no narrowing).
func narrowScopes(granted, requested []string) []string {
	if len(requested) == 0 {
		return granted
	}
	grantedSet := make(map[string]struct{}, len(granted))
	for _, s := range granted {
		grantedSet[s] = struct{}{}
	}
	var out []string
	for _, s := range requested {
		if _, ok := grantedSet[s]; ok {
			out = append(out, s)
		}
	}
	if out == nil {
		return []string{}
	}
	return out
}
