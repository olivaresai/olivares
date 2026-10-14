// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package spiffe

import (
	"context"
	"time"

	"github.com/spiffe/go-spiffe/v2/svid/jwtsvid"
)

// AnthropicAudience is the EXACT audience a JWT-SVID must carry for the Anthropic
// WIF exchange. It is a single source of truth: it MUST match spiffe-helper's
// jwt_audience and the federation rule's audience matcher (the docs require all
// three identical). Anthropic accepts an aud array as long as this exact string is
// one of its elements.
const AnthropicAudience = "https://api.anthropic.com"

// jwtFetcher is the minimal surface FetchAnthropicAssertion needs, so the egress
// path is unit-testable without a live SPIRE agent. *Workload satisfies it.
type jwtFetcher interface {
	FetchJWTSVID(ctx context.Context, audience string, extraAudiences ...string) (*jwtsvid.SVID, error)
}

var _ jwtFetcher = (*Workload)(nil)

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
