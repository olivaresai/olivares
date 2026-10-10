// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package postureexport is the read-only posture/inventory EXPORT: a pull
// surface a control tower (Microsoft Agent 365, ServiceNow AI Control Tower) polls
// to ENRICH its own inventory with the control plane's ground-truth R/RW access
// graph, least-privilege drift, discovered inventory and security posture — the
// "integrate, not compete" strategy. It is OUTBOUND posture (owns
// inbound identity/roster; this module never emits identity, only posture). It is
// strictly read-only and minimal-data (docs/SECURITY-HARDENING.md): refs/hashes/relations only,
// never a raw payload or secret; a defensive redact pass scrubs any free-form field;
// the export action is itself audited (it moves data off-box).
//
// It owns no store entities — it projects the inventory catalog, the access-map
// reconciled drift, and the security findings already maintained by their modules
// inside ONE audited tenant scope. It is the modules-layer bridge that may reach both
// core (sc.Findings) and the public connectors (siemsink/redact); the SIEM ingest
// formats of the towers are NOT verified against a primary source in so the
// export is a documented neutral JSON projection a tower pulls (or an operator routes
// through a generic sink), labeled honestly rather than claiming a working push.
package postureexport

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/sdk"
)

// Name is the module's globally unique identifier.
const Name = "olivares.postureexport"

// Namespace roots the module's routes at /v1/m/posture/.
const Namespace = "posture"

// permExportRead gates the export read (a privileged, audited egress of the
// ground-truth posture). Granted to the read tier (viewer+) like the other privileged
// reads it projects, but the export action is always audited.
const permExportRead auth.Permission = "posture:export:read"

// Module is the posture-export module. Route-only: it owns no entities and reads via
// the request-scoped data handle each route receives.
type Module struct {
	log             *slog.Logger
	producerRunning func(namespace string) bool
}

// Compile-time proofs.
var (
	_ sdk.Module = (*Module)(nil)
	_ api.Module = (*Module)(nil)
)

// New returns the posture-export module.
func New() *Module { return &Module{} }

// UseProducerStatus binds the runtime's running status before the module starts.
// An unbound status source never implies ready projections.
func (m *Module) UseProducerStatus(running func(namespace string) bool) {
	m.producerRunning = running
}

// Descriptor returns the module's self-description.
func (m *Module) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeModule,
		Title:       "Posture export",
		Description: "Retains posture producer data and the authenticated export route. Read-only posture export to a control tower requires Business; the export is filtered, redacted and audited.",
	}
}

// Init keeps the host logger; it subscribes to nothing.
func (m *Module) Init(_ context.Context, host sdk.Host) error {
	m.log = host.Logger()
	return nil
}

// Start / Stop are no-ops (no owned goroutines).
func (m *Module) Start(context.Context) error { return nil }
func (m *Module) Stop(context.Context) error  { return nil }

// APINamespace roots the routes.
func (m *Module) APINamespace() string { return Namespace }

// Permissions declares the export read permission.
func (m *Module) Permissions() []auth.Permission { return []auth.Permission{permExportRead} }

// APIRoutes mounts the export endpoint. The engine wraps it with auth, tenant
// resolution and the permission check, and pins the data handle to the tenant.
func (m *Module) APIRoutes(reg api.RouteRegistrar) {
	reg.Handle("GET", "/export", permExportRead, m.handleExport)
}

// writeResponse retains this route family's media type, nil and cache policy.
func writeResponse(w http.ResponseWriter, status int, v any) {
	if v == nil {
		v = json.RawMessage("null")
	}
	api.WriteJSON(w, status, v, "application/json")
}
