// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package federation

import (
	"context"
	"fmt"

	"github.com/olivaresai/olivares/core/auth"
)

// FromConfig is the MANAGED-config provider builder (FASE X): it constructs
// the same single-IdP OIDC/SAML provider as FromEnv, but from plaintext
// auth.FederationParams the core opens from its sealed config store, instead of
// from the environment. It is the auth.FederationBuilder the composition root
// injects build-independently (cmd/olivares/federationwire.go) so SSO can be
// configured from the console rather than a redeploy. Since it is OPEN-CORE
// and wired in BOTH builds, so the base AGPL artifact builds a real single-IdP
// provider from managed config (it links go-oidc/crewjam now).
//
// It fails like FromEnv: a partial/invalid config or an unreachable IdP at
// construction is an error (the core resolver maps it to NoFederation/501), never
// a half-wired provider.
func FromConfig(ctx context.Context, p auth.FederationParams) (auth.Federation, error) {
	switch p.Protocol {
	case auth.ProtocolOIDC:
		return oidcFromParts(ctx, p.OIDCIssuer, p.OIDCClientID, p.OIDCClientSecret, p.OIDCGroupsClaim)
	case auth.ProtocolSAML:
		return samlFromParts(samlParts{
			metaURL: p.SAMLMetadataURL, entityID: p.SAMLEntityID, acs: p.SAMLACSURL, idpSSO: p.SAMLIDPSSOURL,
			encCertPEM: p.SAMLSPCertPEM, encKeyPEM: p.SAMLSPKeyPEM,
			signCertPEM: p.SAMLSPSignCertPEM, signKeyPEM: p.SAMLSPSignKeyPEM,
			emailAttr: p.SAMLEmailAttr, groupsAttr: p.SAMLGroupsAttr,
		})
	case "":
		return nil, ErrNotConfigured
	default:
		return nil, fmt.Errorf("%w: unknown protocol %q", ErrNotConfigured, p.Protocol)
	}
}

// Compile-time proof FromConfig satisfies the core's builder seam.
var _ auth.FederationBuilder = FromConfig
