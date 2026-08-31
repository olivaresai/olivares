// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package residency gives the control plane a residency-pinned multi-region
// topology (gap OPS-4 —). Today residency is modeled only at
// the model/inference layer (inference_geo); this package pins the control
// plane's OWN data — a tenant's graph, sessions, roster and audit metadata — to
// a region and enforces that pin, fail closed.
//
// Topology (Model B — region-scoped instances). Each control-plane instance
// declares one HOME region (--region) and serves only the tenants pinned to it.
// A tenant's data physically lives in its region's instance (its own Postgres,
// HA'd intra-region by); the edge routes a tenant to its region using the
// residency directory (the tenant→region map, exposed from orgs.data_region).
// The package's job is the deny-closed backstop: it wraps the store.Store seam
// and refuses — with store.ErrResidencyViolation — any tenant-scoped unit of
// work whose tenant is pinned to a region this instance does not serve. So even
// a misrouted request (DNS/LB error, leftover row after a migration) fails
// loudly instead of leaking or silently returning nothing.
//
// Coherence with inference residency. The control-plane pin also constrains
// where a tenant's inference may run: InferenceGeoAllowed reports whether an
// observed inference_geo is compatible with a region pin, so an EU-pinned tenant
// crossing to a US-only inference geo is surfaced as a residency violation
// (reusing the compliance module's existing residency_violation Finding and
// compliance_residency_violation bus signal — it is not reimplemented here).
//
// Single-region mode is the default and unchanged: with no --region configured
// the registry does not enforce, Guard returns the store untouched, and there is
// zero overhead or behavior change. Residency is opt-in per tenant — an unpinned
// tenant (empty data_region) carries no residency requirement.
package residency
