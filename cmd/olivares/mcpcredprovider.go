// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"

	"github.com/olivaresai/olivares/cmd/olivares/internal/mcpgateway"
)

// UpstreamCredentialProvider is what the upstreamCredentialProvider edition port returns
// (wire_noenterprise.go here, the credential minter in the enterprise build). The
// contract is mcpgateway.CredentialProvider; this name keeps both builds' seam.
type UpstreamCredentialProvider = mcpgateway.CredentialProvider

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
