// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	claudewif "github.com/olivaresai/olivares/connectors/claude-wif"
	"github.com/olivaresai/olivares/core/model"
)

// WIF configuration remains readable in both editions. The broker implementation is private.
// WIF broker environment (deny-closed; the broker is opt-in per plane — sessions via
// OLIVARES_SESSION_RUNTIME_WIF, executor via the credential kind "wif").
const (
	// envWIFBaseURL overrides the WIF exchange endpoint (default api.anthropic.com via the
	// connector). The exchange mints the OAuth token; it is NOT the inference gateway.
	envWIFBaseURL = "OLIVARES_WIF_BASE_URL"
	// envWIFSpiffeSocket overrides the SPIRE Workload API address; empty uses the
	// SPIFFE-standard SPIFFE_ENDPOINT_SOCKET. No endpoint => mint is deny-closed.
	envWIFSpiffeSocket = "OLIVARES_WIF_SPIFFE_SOCKET"
	// envWIFTrustDomain is the optional home trust domain for the Workload client.
	envWIFTrustDomain = "OLIVARES_WIF_TRUST_DOMAIN"
	// envWIFRefreshSlack re-mints this long before NotAfter (no refresh token on the WIF
	// path => re-run the exchange). Default 60s; clamped to never exceed half a token's
	// lifetime so a short (e.g. 60s) token stays usable.
	envWIFRefreshSlack = "OLIVARES_WIF_REFRESH_SLACK"
	// envSessionRuntimeWIF opts the SESSIONS plane into the in-process WIF broker (else the
	// rotated token-file compat path stays the default).
	envSessionRuntimeWIF = "OLIVARES_SESSION_RUNTIME_WIF"
	// envSessionWIFRule optionally pins WHICH declared rule the sessions plane mints under
	// (required only when a tenant declares more than one federation rule).
	envSessionWIFRule = "OLIVARES_SESSION_RUNTIME_WIF_RULE"
)

// federationResolver resolves a tenant to its DECLARED WIF exchange targets. The
// wifGraphAdapter satisfies it; tests inject a fake.
type federationResolver interface {
	FederationExchangeParams(tenant model.TenantID) ([]claudewif.ExchangeParams, bool)
}
