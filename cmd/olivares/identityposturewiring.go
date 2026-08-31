// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"log/slog"
	"strings"

	claudeapi "github.com/olivaresai/olivares/connectors/claude-api"
	"github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/governance"
	"github.com/olivaresai/olivares/sdk"
)

// the identity console's posture tab (External Keys / CMEK + workspace residency)
// reads the SAME read-only Claude Admin credential the rate-limit inventory already uses.
// The credential is read from ONE place — claudeAdminSettings — so the two surfaces can
// never drift onto different environment variables.

// claudeAdminSettings builds the connector settings for the dedicated READ-ONLY Admin
// credential (OLIVARES_CLAUDE_ADMIN_KEY, optionally ANTHROPIC_BASE_URL /
// OLIVARES_CLAUDE_WORKSPACE_ID). ok=false means no credential is configured, which is a
// posture, not a fault: the routes stay mounted and answer with a reason. The credential
// lives in the operator's environment, never in the store.
func claudeAdminSettings(getenv func(string) string) (map[string]string, bool) {
	key := strings.TrimSpace(getenv("OLIVARES_CLAUDE_ADMIN_KEY"))
	if key == "" {
		return nil, false
	}
	settings := map[string]string{"admin_key": key}
	if v := strings.TrimSpace(getenv("ANTHROPIC_BASE_URL")); v != "" {
		settings["base_url"] = v
	}
	if v := strings.TrimSpace(getenv("OLIVARES_CLAUDE_WORKSPACE_ID")); v != "" {
		settings["workspace_id"] = v
	}
	return settings, true
}

// claudeIdentityPostureProvider adapts the Claude Admin connector's read-only governance
// inventory to the identity console's posture seam (ANT2-04/06). Read-only by
// construction: the adapter exposes exactly the two reads the console needs and nothing
// else the connector happens to carry.
type claudeIdentityPostureProvider struct{ src *claudeapi.Source }

// Compile-time proof the adapter satisfies the module's read seam.
var _ governance.IdentityPostureProvider = claudeIdentityPostureProvider{}

func (p claudeIdentityPostureProvider) ExternalKeys(ctx context.Context) ([]modelprovider.ExternalKeyRef, error) {
	return p.src.ExternalKeys(ctx)
}

func (p claudeIdentityPostureProvider) Workspaces(ctx context.Context) ([]modelprovider.WorkspaceRef, error) {
	return p.src.Workspaces(ctx)
}

// newIdentityPostureProvider builds the read-only posture provider. It returns nil — so
// GET /v1/m/identity/external-keys and /residency answer available=false WITH A REASON,
// never an empty inventory — when no Admin credential is configured or the connector
// cannot be opened.
func newIdentityPostureProvider(getenv func(string) string, log *slog.Logger) governance.IdentityPostureProvider {
	settings, ok := claudeAdminSettings(getenv)
	if !ok {
		return nil
	}
	src := claudeapi.New()
	if err := src.Open(context.Background(), sdk.Config{Settings: settings}); err != nil {
		// Never log the error (it can embed the endpoint/credential): a configuration
		// fault leaves the routes answering with a reason, not wired.
		log.Warn("identity: could not open the Claude Admin connector for the posture inventory; GET /v1/m/identity/external-keys and /residency stay unavailable-with-reason")
		return nil
	}
	log.Info("identity: wired read-only Claude Admin posture provider (External Keys + workspace residency, ANT2-04/06)")
	return claudeIdentityPostureProvider{src: src}
}

// fedSsoPosture adapts the engine's federation service to the identity console's SSO
// posture seam. It reports the protocol the ACTIVE provider speaks by resolving
// the SAME global/default IdP the login leg resolves — env-configured or managed —
// rather than reading a config row directly, so the console cannot report an IdP the
// login round-trip would not actually use. Read-only: resolution has no side effect.
type fedSsoPosture struct{ svc *auth.FederationService }

// Compile-time proof the adapter satisfies the module's read seam.
var _ governance.SsoPostureProvider = fedSsoPosture{}

func (f fedSsoPosture) SsoPosture(ctx context.Context) (string, error) {
	if f.svc == nil {
		return "", nil
	}
	// Posture() FIRST, and only for its error. ResolveByAlias returns no error at all:
	// it collapses a store fault, a provider that failed to build, and an
	// authoritative-off tombstone into the same NoFederation{}. Resolving alone would
	// therefore make the console's "we could not look" answer DEAD CODE, and a degraded
	// store would render as "SSO is genuinely OFF" — the exact confusion that answer
	// exists to prevent. Posture() reads the same config row and does report the fault.
	if _, err := f.svc.Posture(ctx); err != nil {
		return "", err
	}
	fed, _ := f.svc.ResolveByAlias(ctx, auth.GlobalFederationScope, model.DefaultFederationAlias)
	if fed == nil {
		return "", nil
	}
	return fed.Protocol(), nil
}
