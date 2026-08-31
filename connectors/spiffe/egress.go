// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package spiffe

// Anthropic Workload Identity Federation egress (IDN-07 ↔ IDN-01). This is the
// canonical, Anthropic-first path by which an engine workload authenticates to the
// Claude API with ZERO static keys: fetch a JWT-SVID from the local SPIRE agent
// with the Anthropic audience, then present it as the RFC 7523 assertion to the WIF
// exchange (connectors/claude-wif's Exchanger, wired by the host), minting a
// short-lived sk-ant-oat01- token.
//
// This file holds the connector-side, host-agnostic primitives: the exact audience,
// the static-key footgun guard (fail-closed), the JWT-SVID fetch+verify harness, and
// the assertion producer. The host (cmd, AGPL) glues FetchAnthropicAssertion →
// claudewif.Exchanger.Exchange — the connector never imports claude-wif (the exchange
// is a separate primitive) nor /core (the license frontier).
//
// Verified against the Anthropic WIF docs jun-2026 (platform.claude.com/docs/en/
// manage-claude/{workload-identity-federation,wif-providers/spiffe,wif-reference}):
// the audience MUST be exactly https://api.anthropic.com (the same value in the
// FetchJWTSVID call, spiffe-helper jwt_audience, and the rule's audience matcher);
// the exchange is POST /v1/oauth/token, grant_type jwt-bearer; a static
// ANTHROPIC_API_KEY — EVEN set to "" — shadows the federation path in the SDK
// credential precedence (tier 2 over tier 4).

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spiffe/go-spiffe/v2/svid/jwtsvid"
)

// AnthropicAudience is the EXACT audience a JWT-SVID must carry for the Anthropic
// WIF exchange. It is a single source of truth: it MUST match spiffe-helper's
// jwt_audience and the federation rule's audience matcher (the docs require all
// three identical). Anthropic accepts an aud array as long as this exact string is
// one of its elements.
const AnthropicAudience = "https://api.anthropic.com"

// Environment variables in Anthropic's documented credential precedence whose mere
// PRESENCE shadows the federated egress path. A static key sits at tier 2, above the
// federation tiers (tier 4), so even an EMPTY value ("") wins its slot and selects
// the static-key path with an empty key — silently disabling federation.
const (
	envAnthropicAPIKey    = "ANTHROPIC_API_KEY"
	envAnthropicAuthToken = "ANTHROPIC_AUTH_TOKEN"
)

// jwtFetcher is the minimal surface FetchAnthropicAssertion needs, so the egress
// path is unit-testable without a live SPIRE agent. *Workload satisfies it.
type jwtFetcher interface {
	FetchJWTSVID(ctx context.Context, audience string, extraAudiences ...string) (*jwtsvid.SVID, error)
}

var _ jwtFetcher = (*Workload)(nil)

// AssertNoStaticKeyShadowing fails CLOSED when a static Anthropic credential is
// present in the environment on the federated egress path. It complements (does not
// duplicate) claude-wif's detectShadowing, which emits a governance FINDING: this is
// a hard ERROR on the egress code path itself, because a shadowing key does not just
// warrant a finding — it would make the federation silently not happen. Presence
// (set), not a non-empty value, is the trigger: ANTHROPIC_API_KEY="" still wins its
// precedence slot. lookupEnv is injectable for tests; nil uses os.LookupEnv.
func AssertNoStaticKeyShadowing(lookupEnv func(string) (string, bool)) error {
	get := lookupEnv
	if get == nil {
		get = os.LookupEnv
	}
	if _, ok := get(envAnthropicAPIKey); ok {
		return fmt.Errorf("spiffe: %s is set (even empty values win their precedence slot) and shadows Workload Identity Federation; unset it on the federated egress path", envAnthropicAPIKey)
	}
	if _, ok := get(envAnthropicAuthToken); ok {
		return fmt.Errorf("spiffe: %s is set and shadows Workload Identity Federation; unset it on the federated egress path", envAnthropicAuthToken)
	}
	return nil
}

// FetchAnthropicAssertion produces the RFC 7523 assertion (a raw JWT-SVID string)
// to present to the WIF exchange. It (1) fails closed if a static Anthropic key is
// present (AssertNoStaticKeyShadowing), (2) fetches a JWT-SVID with the exact
// Anthropic audience, and (3) runs the verification harness on it (sub is a SPIFFE
// ID, aud contains the Anthropic audience) BEFORE returning, so a misconfigured
// audience is caught here, not as an opaque exchange rejection. The returned token
// is a bearer credential: present it transiently, never log or persist it.
func FetchAnthropicAssertion(ctx context.Context, f jwtFetcher, lookupEnv func(string) (string, bool)) (string, error) {
	if err := AssertNoStaticKeyShadowing(lookupEnv); err != nil {
		return "", err
	}
	svid, err := f.FetchJWTSVID(ctx, AnthropicAudience)
	if err != nil {
		return "", fmt.Errorf("spiffe: fetch anthropic jwt-svid: %w", err)
	}
	token := svid.Marshal()
	if _, err := InspectAnthropicAssertion(token); err != nil {
		return "", err
	}
	return token, nil
}

// SVIDClaims is the non-secret, decoded view of a JWT-SVID the harness asserts on
// before the exchange. It carries the SPIFFE ID (sub), the issuer (the OIDC
// discovery URL the federation rule trusts), the audiences and the expiry — never
// the signature or any private material.
type SVIDClaims struct {
	SpiffeID  string
	Issuer    string
	Audience  []string
	ExpiresAt time.Time
}

// InspectAnthropicAssertion decodes a JWT-SVID and asserts it is fit to present to
// the Anthropic exchange: the subject is a SPIFFE ID and the audience contains the
// exact Anthropic audience. It does NOT verify the signature — the SPIRE agent
// already signed it and the exchange verifies it against the federation issuer's
// JWKS; this is a pre-flight sanity check so an audience/subject mistake surfaces as
// a clear local error rather than an opaque server-side invalid_grant. (The passive
// signature-verifying path is svid.go's Verifier, for tokens presented TO us.)
func InspectAnthropicAssertion(token string) (SVIDClaims, error) {
	// ParseInsecure decodes without signature verification and enforces that the
	// audience contains the expected value — exactly the pre-flight check we want.
	svid, err := jwtsvid.ParseInsecure(strings.TrimSpace(token), []string{AnthropicAudience})
	if err != nil {
		return SVIDClaims{}, fmt.Errorf("spiffe: inspect assertion: %w", err)
	}
	c := SVIDClaims{
		SpiffeID:  svid.ID.String(),
		Audience:  svid.Audience,
		ExpiresAt: svid.Expiry,
	}
	if iss, ok := svid.Claims["iss"].(string); ok {
		c.Issuer = iss
	}
	if c.SpiffeID == "" {
		return SVIDClaims{}, fmt.Errorf("spiffe: inspect assertion: subject is not a SPIFFE ID")
	}
	return c, nil
}
