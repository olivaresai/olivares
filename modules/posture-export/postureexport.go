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
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
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

// Caps bound a single export so one request never builds an unbounded transaction;
// the response reports truncation honestly (docs/SECURITY-HARDENING.md — a partial export is labeled
// partial, never authoritative).
const (
	exportInventoryCap = 1000
	exportFindingsCap  = 1000
	exportDriftCap     = 1000
)

// Module is the posture-export module. Route-only: it owns no entities and reads via
// the request-scoped data handle each route receives.
type Module struct {
	log *slog.Logger
}

// Compile-time proofs.
var (
	_ sdk.Module = (*Module)(nil)
	_ api.Module = (*Module)(nil)
)

// New returns the posture-export module.
func New() *Module { return &Module{} }

// Descriptor returns the module's self-description.
func (m *Module) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeModule,
		Title:       "Posture export",
		Description: "Read-only export of the ground-truth access graph, least-privilege drift, discovered inventory and security posture for a control tower (Agent 365 / ServiceNow AI Control Tower) to ingest. Outbound posture only — never identity. Filters by tenant/severity/category; redact applied; the export is audited.",
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

// handleExport assembles the posture projection inside ONE audited tenant scope and
// returns it. Filters: ?severity=<floor> (applied IN GO — finding severity is a text
// column, not lexically ordered), ?category=<x> (matches a finding kind OR
// subject_kind — there is no category column), ?kind=<x> (inventory entity kind). The
// tenant is implicit (the scope is pinned).
func (m *Module) handleExport(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := r.URL.Query()
	floor := model.Severity(q.Get("severity"))
	if floor != "" && severityRank(floor) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "severity must be one of low, medium, high, critical"})
		return
	}
	category := q.Get("category")
	invKind := q.Get("kind")

	var doc exportDocument
	doc.Tenant = mc.Tenant.String()
	doc.Note = exportNote
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		// The export moves posture off-box: audit it with the real principal in the
		// SAME transaction as the reads (docs/SECURITY-HARDENING.md). The Meta is counts/filters only.
		if _, e := sc.Audit().Append(r.Context(), model.AuditDraft{
			Actor: mc.Principal.Actor(), ActorKind: mc.Principal.ActorKind(), Action: "posture.export",
			Meta: map[string]any{"severity_floor": string(floor), "category": category, "kind": invKind},
		}); e != nil {
			return e
		}
		inv, invTrunc, e := readInventory(r.Context(), sc, invKind)
		if e != nil {
			return e
		}
		doc.Inventory, doc.InventoryTruncated = inv, invTrunc

		drift, e := readDrift(r.Context(), sc)
		if e != nil {
			return e
		}
		doc.Drift = drift

		find, findTrunc, e := readFindings(r.Context(), sc, floor, category)
		if e != nil {
			return e
		}
		doc.Findings, doc.FindingsTruncated = find, findTrunc
		return nil
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "export failed"})
		if m.log != nil {
			m.log.Warn("posture-export: projection failed", "err", err)
		}
		return
	}
	writeJSON(w, http.StatusOK, doc)
}

// writeJSON writes v as a JSON response with the given status.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
