// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"fmt"
)

// UpstreamCredentialProvider resolves the credential for an upstream MCP
// server request. The ENTERPRISE implementation mints a short-lived,
// audience-bound token via RFC 8693 token-exchange (core/auth/tokenexchange)
// per target server; the COMMUNITY implementation returns the operator's
// static credential (the same behavior as pre). The provider NEVER
// returns the inbound bearer (no-token-passthrough is structural).
//
// Honesty: the static provider is the community/unconfigured behavior and never
// pretends to be short-lived. Once the enterprise minter is explicitly activated,
// config or exchange failures return an error and fail the upstream request closed.
type UpstreamCredentialProvider interface {
	// Credential returns the Authorization header value for a request to
	// the named target server. An error fails the request (deny-closed).
	Credential(ctx context.Context, target string) (authHeader string, err error)
}

// staticCredentialProvider returns the same operator-configured credential
// for every target. It is the community-build default: no per-server minting,
// same pre behavior. The credential is a SEPARATE upstream credential
// (never the inbound bearer).
type staticCredentialProvider struct {
	authHeader string
}

func (s *staticCredentialProvider) Credential(_ context.Context, _ string) (string, error) {
	return s.authHeader, nil
}

// errMCPAddonRequired is the STABLE marker for an MCP leg refused because a commercial
// add-on is not entitled. It exists because the JSON-RPC layer cannot see the reason
// otherwise: a bare credential error is indistinguishable from an upstream transport
// failure, and the client is told the wrong thing.
//
// WHY THE TRANSLATION LIVES HERE AND NOT IN THE CONNECTOR. connectors/ is Apache-2.0 and
// must NEVER import the AGPL engine (scripts/check-boundary.sh), so connectors/mcp cannot
// match license.ErrAddonRequiresLicense — and its JSON-RPC codes are unexported, so this
// package cannot pick one. cmd/olivares is the only place that may see both sides, which is
// exactly why the entitlement error is translated at this seam.
//
// KNOWN LIMIT, declared rather than hidden: the client currently receives the connector's
// generic upstream-error code. Giving this refusal its OWN JSON-RPC code needs one exported
// constant in connectors/mcp (proposed -31020, outside the JSON-RPC reserved range as that
// package requires), which is a different lane's file. Until then the refusal is stable and
// self-describing in the message and in the logs, which is what makes it diagnosable.
var errMCPAddonRequired = errors.New("mcp gateway: addon_requires_license")

// mcpAddonRefusal wraps an entitlement refusal so callers can match either the MCP marker
// or the underlying core sentinel, and so the message names the add-on and the operation.
func mcpAddonRefusal(addon, operation string, cause error) error {
	subject := "a commercial add-on"
	if addon != "" {
		subject = "the \"" + addon + "\" add-on"
	}
	if operation != "" {
		subject += " for " + operation
	}
	return fmt.Errorf("%w: %s is required to mint the upstream credential; the request was NOT sent: %w",
		errMCPAddonRequired, subject, cause)
}
