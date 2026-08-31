// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package federation is the OPEN-CORE single-IdP SSO login provider: it
// implements the core auth.Federation seam with real OIDC
// (github.com/coreos/go-oidc — Authorization Code + S256 PKCE + nonce + azp) and
// SAML 2.0 (github.com/crewjam/saml — signature + InResponseTo + audience +
// bearer-assertion anti-replay). It is AGPL-3.0-only and links into the DEFAULT
// binary, so an engineer who self-hosts gets SSO from one IdP without
// -tags enterprise (it is wired build-independently in
// cmd/olivares/federationwire.go), and the base build DOES link go-oidc/crewjam.
//
// What is NOT here — the reserved enterprise line (LICENSING.md): per-tenant
// MULTI-IdP resolution, SSO-enforcement/posture and managed SCIM live behind the
// `enterprise` build tag (enterprise/federation). The single-IdP cap lives on
// auth.FederationService: the open build may hold at most one ACTIVE config, and
// activating a second returns multi_idp_requires_enterprise rather than silently
// resolving multiple IdPs. Opening one IdP here is not a rug-pull — nothing in
// this provider shipped as a public binary before; the cut was drawn pre-launch.
package federation

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/olivaresai/olivares/core/auth"
)

// Env keys (read via the injected getenv so the same config drives prod and tests).
const (
	envProtocol = "OLIVARES_SSO_PROTOCOL" // "oidc" | "saml"

	// OIDC.
	envOIDCIssuer       = "OLIVARES_OIDC_ISSUER"
	envOIDCClientID     = "OLIVARES_OIDC_CLIENT_ID"
	envOIDCClientSecret = "OLIVARES_OIDC_CLIENT_SECRET"
	envOIDCGroupsClaim  = "OLIVARES_OIDC_GROUPS_CLAIM" // optional; ID-token/UserInfo claim carrying groups

	// SAML.
	envSAMLMetadataURL = "OLIVARES_SAML_IDP_METADATA_URL"
	envSAMLEntityID    = "OLIVARES_SAML_SP_ENTITY_ID"
	// SP ENCRYPTION keypair (RSA only): decrypts IdP-encrypted assertions and is
	// published as the use="encryption" KeyDescriptor.
	envSAMLCertPEM = "OLIVARES_SAML_SP_CERT_PEM"
	envSAMLKeyPEM  = "OLIVARES_SAML_SP_KEY_PEM"
	// SP SIGNING keypair (RSA or EC): signs AuthnRequests and is published as the
	// use="signing" KeyDescriptor.
	envSAMLSignCertPEM = "OLIVARES_SAML_SP_SIGN_CERT_PEM"
	envSAMLSignKeyPEM  = "OLIVARES_SAML_SP_SIGN_KEY_PEM"
	envSAMLEmailAttr   = "OLIVARES_SAML_EMAIL_ATTRIBUTE"  // optional; default tries common names
	envSAMLACSURL      = "OLIVARES_SAML_ACS_URL"          // the SP AssertionConsumerService URL
	envSAMLIDPSSOURL   = "OLIVARES_SAML_IDP_SSO_URL"      // the IdP SingleSignOnService URL
	envSAMLGroupsAttr  = "OLIVARES_SAML_GROUPS_ATTRIBUTE" // optional; multi-valued attribute carrying groups
)

// ErrNotConfigured means no/insufficient SSO configuration was found; the
// composition root maps it to auth.NoFederation (501), fail-closed.
var ErrNotConfigured = errors.New("federation: SSO not configured")

// FromEnv builds the configured federation Provider, or ErrNotConfigured when the
// configuration is absent/invalid. A partial configuration is an error, never a
// half-wired provider.
func FromEnv(getenv func(string) string) (*Provider, error) {
	switch getenv(envProtocol) {
	case auth.ProtocolOIDC:
		return oidcFromEnv(getenv)
	case auth.ProtocolSAML:
		return samlFromEnv(getenv)
	case "":
		return nil, ErrNotConfigured
	default:
		return nil, fmt.Errorf("%w: unknown %s %q", ErrNotConfigured, envProtocol, getenv(envProtocol))
	}
}

// Provider implements auth.Federation by delegating to the configured concrete
// protocol provider.
type Provider struct {
	protocol string
	oidc     *oidcProvider
	saml     *samlProvider
}

var _ auth.Federation = (*Provider)(nil)

// Protocol reports the configured protocol.
func (p *Provider) Protocol() string { return p.protocol }

// BeginAuth delegates to the concrete provider.
func (p *Provider) BeginAuth(ctx context.Context, ap auth.AuthParams) (string, error) {
	if p.oidc != nil {
		return p.oidc.beginAuth(ctx, ap)
	}
	return p.saml.beginAuth(ctx, ap)
}

// ValidateAssertion delegates to the concrete provider.
func (p *Provider) ValidateAssertion(ctx context.Context, a auth.Assertion) (auth.FederatedIdentity, error) {
	if p.oidc != nil {
		return p.oidc.validate(ctx, a)
	}
	return p.saml.validate(ctx, a)
}

// SAMLMetadata returns the SP's SAML metadata document (XML) for one-click IdP
// onboarding, or an error for a non-SAML provider. This method is open-core (it
// rides with the single-IdP SAML provider), but the unauthenticated SP-metadata
// HTTP ENDPOINT stays enterprise-gated in the commercial composition root —
// publishing SP metadata is an enterprise nicety, so the default build links the
// method yet does not expose the route (it 404s there). A type assertion to the
// optional metadata interface keeps that wiring honest.
func (p *Provider) SAMLMetadata() ([]byte, error) {
	if p.saml == nil {
		return nil, fmt.Errorf("%w: SAML metadata requested but the active provider is not SAML", ErrNotConfigured)
	}
	return p.saml.SPMetadata()
}

// httpClient is the shared, bounded HTTP client for discovery/metadata/token calls.
func httpClient() *http.Client { return &http.Client{Timeout: 15 * time.Second} }

// mustAbsURL validates that s is an absolute URL.
func mustAbsURL(s string) (*url.URL, error) {
	u, err := url.Parse(s)
	if err != nil || !u.IsAbs() {
		return nil, fmt.Errorf("%w: %q is not an absolute URL", ErrNotConfigured, s)
	}
	return u, nil
}
