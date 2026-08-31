// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package voice

import (
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
)

// APINamespace roots the module's routes at /v1/m/voice/.
func (m *Module) APINamespace() string { return Namespace }

// Permissions declares the module's permissions so the built-in roles grant them by
// verb tier.
func (m *Module) Permissions() []auth.Permission {
	return []auth.Permission{permSessionRead, permPolicyAdmin, permSessionAdmin}
}

// APIRoutes mounts the module's routes (each wrapped by the engine with
// authentication, tenant resolution and the declared permission check).
func (m *Module) APIRoutes(reg api.RouteRegistrar) {
	// Session metadata (privileged reads).
	reg.Handle("GET", "/sessions", permSessionRead, m.handleListSessions)
	reg.Handle("GET", "/sessions/{ref}", permSessionRead, m.handleGetSession)
	reg.Handle("GET", "/sessions/{ref}/stream", permSessionRead, m.handleStream)
	reg.Handle("GET", "/sessions/{ref}/decisions", permSessionRead, m.handleSessionDecisions)

	// The governed open — admin-tier, policy-gated default-deny.
	reg.Handle("POST", "/sessions/open", permSessionAdmin, m.handleOpen)

	// Voice-open policy (who may open with which model/provider).
	reg.Handle("GET", "/policies", permSessionRead, m.handleListPolicies)
	reg.Handle("PUT", "/policies", permPolicyAdmin, m.handleSetPolicy)

	// The append-only open/close governance-evidence ledger.
	reg.Handle("GET", "/decisions", permSessionRead, m.handleDecisions)
}
