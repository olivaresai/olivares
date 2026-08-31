// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package observability

import (
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
)

// APINamespace roots the module's routes at /v1/m/observability/ — the paths
// the admin view has called since (web/src/features/observability/api.ts).
func (m *Module) APINamespace() string { return Namespace }

// Permissions declares the module's permissions so the built-in roles grant
// them by verb tier (all read-tier → viewer and up).
func (m *Module) Permissions() []auth.Permission {
	return []auth.Permission{permHealthRead, permTracesRead, permAttestationRead}
}

// APIRoutes mounts the module's routes. The engine wraps each with
// authentication, tenant resolution and the declared permission check.
func (m *Module) APIRoutes(reg api.RouteRegistrar) {
	// Per-standard ingestion health + live per-source counters (process-global).
	reg.Handle("GET", "/ingestion-health", permHealthRead, m.handleIngestionHealth)
	// Ledger-derived trace correlation view (tenant-scoped via mc.Data). The
	// LIST is the shallow read every page principal sees; opening one trace's
	// cross-hop detail is the DEEPER read on its own permission — the split the
	// Contract declared and the web mirrors (observability-view.tsx canDrill).
	// Both are read-tier today; the split is what lets a stricter role model
	// withhold the drill without hiding the list.
	reg.Handle("GET", "/traces", permHealthRead, m.handleListTraces)
	reg.Handle("GET", "/traces/{id}", permTracesRead, m.handleGetTrace)
	// OTLP-compatible JSON export for one trace (Jaeger / Tempo / Datadog).
	reg.Handle("GET", "/traces/{id}/export", permTracesRead, m.handleExportTrace)
	// Measured attestation of the running binary.
	reg.Handle("GET", "/attestation", permAttestationRead, m.handleAttestation)
}
