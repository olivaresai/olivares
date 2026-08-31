// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package redteam

import (
	"context"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// resolveOwnedAgent resolves agentRef to a canonical agent id WITHIN the caller's
// tenant and reports ok=false when it does not name an agent in this tenant's
// inventory. agentRef may be the agent's canonical id or its source external_id.
//
// This is the in-code OWNERSHIP enforcement for the dual-use red line (docs/SECURITY-HARDENING.md):
// a red-team target must be an agent the platform has actually observed in the
// operator's OWN estate — you cannot register or run against an arbitrary string,
// or against another tenant's agent. Tenant isolation is automatic because the
// Scope is tenant-pinned (a cross-tenant agentRef simply does not resolve). It
// couples redteam only to the shared core entity model (sc.Agents()/model.Agent),
// never to the inventory module, so the module-boundary rule holds.
func resolveOwnedAgent(ctx context.Context, sc store.Scope, agentRef string) (model.ID, bool, error) {
	// First treat agentRef as a canonical agent id.
	if id, err := model.ParseID(agentRef); err == nil && !id.IsZero() {
		switch _, gerr := sc.Agents().Get(ctx, id); {
		case gerr == nil:
			return id, true, nil
		case !isNotFound(gerr):
			return "", false, gerr
		}
		// A well-formed id that is absent/other-tenant falls through to the
		// external_id lookup (an external ref can be id-shaped in some sources).
	}
	// Else resolve by the source external_id, scoped to this tenant.
	agents, _, err := sc.Agents().List(ctx, model.Query{
		Filters: []model.Filter{eq("external_id", agentRef)},
		Limit:   1,
	})
	if err != nil {
		return "", false, err
	}
	if len(agents) == 1 {
		return agents[0].ID, true, nil
	}
	return "", false, nil
}
