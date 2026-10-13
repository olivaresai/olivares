// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"

	"github.com/olivaresai/olivares/connectors/modelprovider"
)

// the Admin-API GOVERNANCE POSTURE the identity console's `posture` tab reads:
// the External Keys / CMEK inventory (ANT2-04) and the workspace data-residency object
// (ANT2-06). Both were DECLARED by the typed client and mounted by nobody, so the panel
// asked and the engine answered 404 while every mocked unit test stayed green.
//
// WHAT THIS EXPOSES AND WHAT IT DOES NOT. The data is already collected and governed by
// connectors/claude-api (governance.go fetchExternalKeys / fetchWorkspaces, reaching
// /v1/organizations/external_keys and .../workspaces). This serves that inventory; it
// does not gather anything new and it cannot mutate. It is minimal-data by construction:
// an ekey_ REFERENCE, the cloud-KMS provider TYPE, a validation state and timestamps —
// there is no field here that could carry key material, and the connector's own type has
// none either (connectors/modelprovider/catalog.go:405-408).
//
// THREE ANSWERS, NEVER TWO. "No customer-managed keys" and "we could not look" are
// different facts and must not render alike:
//   - available=true  + items      — the inventory, read cleanly (zero items is a real zero)
//   - available=false + reason     — no Admin credential wired, or a transient fetch fault
//   - 403                          — the caller lacks governance:identity:read
//
// It never 500s on a connector fault and never echoes the connector's error, which can
// embed the Admin endpoint and credential: that goes to the log, the browser gets a
// generic reason. This mirrors the sibling read-only inventory the same connector already
// backs — modules/models/ratelimits.go handleRateLimits — deliberately, so the two
// degrade the same way.
//
// SCOPE AXIS, SAID OUT LOUD. These are ANTHROPIC-org objects read through ONE
// deployment-level Admin credential; an Anthropic workspace is not an Olivares workspace,
// so there is no Olivares workspace axis to confine on and inventing one would filter by a
// key that means something else. The tenant axis IS honored the only way it can be: the
// route is tenant-resolved and self-audits under the caller's tenant. The sibling
// /v1/m/models/rate-limits reads the same credential the same way.

// IdentityPostureProvider is the read seam over the claude-api Admin connector's
// governance inventory. It is READ-ONLY by type: there is no method here that can change
// anything at the provider. A nil provider is the honest "not wired" posture — distinct
// from a wired provider returning zero rows, which is a real empty inventory.
type IdentityPostureProvider interface {
	// ExternalKeys lists the org's customer-managed encryption keys as inventory
	// metadata (ekey_ references only, never key material).
	ExternalKeys(ctx context.Context) ([]modelprovider.ExternalKeyRef, error)
	// Workspaces lists the org's workspaces with their governance object (home geo,
	// CMEK reference, compartment, tags, data-residency policy).
	Workspaces(ctx context.Context) ([]modelprovider.WorkspaceRef, error)
}

// WithIdentityPostureProvider wires the read-only Admin-API posture source. Without it
// GET /external-keys and GET /residency answer available=false with a reason — never an
// empty inventory, which would read as "this org has none".
func WithIdentityPostureProvider(p IdentityPostureProvider) IdentityConsoleOption {
	return func(c *IdentityConsole) { c.posture = p }
}
