// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

// ID-JAG issuer — mints Identity Assertion JWT Authorization Grants per
// draft-ietf-oauth-identity-assertion-authz-grant-04 (2026-05-21, WG-adopted,
// Okta Cross-App Access / Entra enterprise MCP flows).
//
// DRAFT STATUS — design-toward, no conformance claim (docs/SECURITY-HARDENING.md): the draft
// is WG-adopted but pre-RFC. This file implements the -04 shape and marks the
// status explicitly. The typ header is "oauth-id-jag+jwt" per the draft §4.
//
// The ID-JAG identifies an agent + its human sponsor + the target resource/
// audience, signed by the Olivares AS (this server). The receiving AS validates
// it with IDJAGValidator (connectors/mcp/idjag.go) or its own equivalent.
//
// This issuer is SEPARATE from the opaque token exchange in tokenexchange.go:
// the token exchange produces first-party opaque olvk_ tokens (docs/SECURITY-HARDENING.md);
// this issuer produces signed JWTs intended for EXTERNAL authorization servers.
// The two primitives share the AgentLifecycleChecker interface but have
// completely different trust models and output formats.
//
// Deny-closed: no signing key = no ID-JAG issuing; no agent lifecycle = rejected;
// no sponsor = rejected; unknown resource = rejected.

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
)

const (
	// IDJAGTyp is the JWT typ header per draft -04 §4.
	IDJAGTyp = "oauth-id-jag+jwt"
)

// ErrIDJAGUnavailable is returned when the issuer is not configured (no signing
// key or no lifecycle checker).
var ErrIDJAGUnavailable = errors.New("auth: ID-JAG issuing unavailable")

// IDJAGRequest is the input for issuing an ID-JAG.
type IDJAGRequest struct {
	// AgentRef is the agent's external_id (identity_ref in the lifecycle).
	AgentRef string
	// ClientID is the authenticated OAuth client requesting the grant.
	ClientID string
	// Resource is the target MCP server resource URI (RFC 8707).
	Resource string
	// Audience is the issuer identifier of the target AS.
	Audience string
	// Scope is the requested scope (may be narrowed by the receiving AS).
	Scope []string
	// Tenant is the tenant context.
	Tenant model.TenantID
	// SponsorRef is the sponsor's external_id (resolved by the caller from the
	// authenticated principal). The issuer validates it matches the agent's
	// lifecycle sponsor.
	SponsorRef string
}

// IDJAGClaims are the JWT claims of an issued ID-JAG (draft -04 §4.4).
// The standard claims (iss, sub, aud, exp, iat, nbf, jti) are carried in
// jwt.Claims; the draft-specific claims are inline.
type IDJAGClaims struct {
	jwt.Claims
	ClientID   string   `json:"client_id"`
	Resources  []string `json:"resource,omitempty"`
	SponsorRef string   `json:"sponsor_ref,omitempty"`
	AgentKind  string   `json:"agent_kind,omitempty"`
}

// IDJAGIssuerConfig configures the ID-JAG issuer.
type IDJAGIssuerConfig struct {
	// SigningKey is the Ed25519 private key for signing ID-JAGs.
	SigningKey ed25519.PrivateKey
	// Issuer is this AS's issuer identifier (the iss claim).
	Issuer string
	// DefaultTTL is the grant lifetime (default 5 minutes).
	DefaultTTL time.Duration
	// Checker validates agent lifecycle status.
	Checker AgentLifecycleChecker
	// ApprovedResources is the set of resource URIs the issuer may target
	// (deny-closed: empty set rejects all).
	ApprovedResources []string
	// Clock overrides time.Now (for tests).
	Clock func() time.Time
}

// IDJAGIssuer mints signed ID-JAGs. It is constructed with a signing key and
// the AS issuer identifier. Deny-closed: a zero-value issuer rejects everything.
// Safe for concurrent use after construction.
type IDJAGIssuer struct {
	signer      jose.Signer
	pubKey      ed25519.PublicKey
	issuer      string
	defaultTTL  time.Duration
	checker     AgentLifecycleChecker
	clock       func() time.Time
	approvedRes map[string]bool
}

// NewIDJAGIssuer constructs an ID-JAG issuer. Returns an error if the signing
// key is missing or invalid. A nil checker means agent-OBO validation is
// unavailable (deny-closed).
func NewIDJAGIssuer(cfg IDJAGIssuerConfig) (*IDJAGIssuer, error) {
	if len(cfg.SigningKey) == 0 {
		return nil, fmt.Errorf("%w: no signing key provided", ErrIDJAGUnavailable)
	}
	if cfg.Issuer == "" {
		return nil, fmt.Errorf("%w: issuer identifier is required", ErrIDJAGUnavailable)
	}
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.EdDSA, Key: cfg.SigningKey},
		(&jose.SignerOptions{}).WithType(jose.ContentType(IDJAGTyp)),
	)
	if err != nil {
		return nil, fmt.Errorf("idjag: signer: %w", err)
	}
	ttl := cfg.DefaultTTL
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	clock := cfg.Clock
	if clock == nil {
		clock = time.Now
	}
	approved := make(map[string]bool, len(cfg.ApprovedResources))
	for _, r := range cfg.ApprovedResources {
		if t := strings.TrimSpace(r); t != "" {
			approved[t] = true
		}
	}
	// Extract the public key from the private key for later export via PublicKey().
	pub := cfg.SigningKey.Public().(ed25519.PublicKey)
	return &IDJAGIssuer{
		signer:      signer,
		pubKey:      pub,
		issuer:      cfg.Issuer,
		defaultTTL:  ttl,
		checker:     cfg.Checker,
		clock:       clock,
		approvedRes: approved,
	}, nil
}

// PublicKey returns the Ed25519 public key for the issuer's signing key, suitable
// for publishing as a JWKS so receiving ASes can verify the ID-JAG signature.
func (iss *IDJAGIssuer) PublicKey() ed25519.PublicKey {
	if iss == nil {
		return nil
	}
	return iss.pubKey
}

// Issue mints a signed ID-JAG JWT. Deny-closed on every validation failure.
func (iss *IDJAGIssuer) Issue(ctx context.Context, req IDJAGRequest) (string, error) {
	if iss == nil {
		return "", ErrIDJAGUnavailable
	}
	if req.AgentRef == "" {
		return "", fmt.Errorf("%w: agent_ref is required", ErrInvalidExchange)
	}
	if req.ClientID == "" {
		return "", fmt.Errorf("%w: client_id is required", ErrInvalidExchange)
	}
	if req.Audience == "" {
		return "", fmt.Errorf("%w: audience (target AS issuer) is required", ErrInvalidExchange)
	}
	if req.Resource == "" {
		return "", fmt.Errorf("%w: resource URI is required", ErrInvalidExchange)
	}
	// Deny-closed resource allowlist: an empty allowlist means no resource is
	// approved (the operator must explicitly provision the list).
	if !iss.approvedRes[strings.TrimSpace(req.Resource)] {
		return "", fmt.Errorf("%w: resource %q is not approved for delegation", ErrInvalidTarget, req.Resource)
	}

	// Validate agent lifecycle before signing anything.
	if iss.checker == nil {
		return "", fmt.Errorf("%w: no agent lifecycle checker", ErrIDJAGUnavailable)
	}
	if err := iss.checker.CheckAgentForExchange(ctx, req.Tenant, req.AgentRef, req.SponsorRef); err != nil {
		return "", fmt.Errorf("%w: %v", ErrAgentBlocked, err)
	}

	now := iss.clock()
	claims := IDJAGClaims{
		Claims: jwt.Claims{
			Issuer:    iss.issuer,
			Subject:   req.AgentRef,
			Audience:  jwt.Audience{req.Audience},
			ID:        model.NewID().String(),
			IssuedAt:  jwt.NewNumericDate(now),
			Expiry:    jwt.NewNumericDate(now.Add(iss.defaultTTL)),
			NotBefore: jwt.NewNumericDate(now),
		},
		ClientID:   req.ClientID,
		Resources:  []string{req.Resource},
		SponsorRef: req.SponsorRef,
		AgentKind:  "agent",
	}

	token, err := jwt.Signed(iss.signer).Claims(claims).Serialize()
	if err != nil {
		return "", fmt.Errorf("idjag: sign: %w", err)
	}
	return token, nil
}
