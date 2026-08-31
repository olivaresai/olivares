// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package api is the engine's HTTP/REST + gRPC surface: the single
// versioned contract (/v1, OpenAPI + proto) that the web UI and modules consume.
// It is the milestone that stabilizes the platform contract.
//
// Every request flows through one middleware chain — panic recovery, request id,
// security headers, access log, a body-size cap, authentication, and a setup gate
// — and every protected handler resolves ONE canonical tenant (agreeing across
// path, header and a token's bound tenant) and authorizes a permission before it
// touches the store, binding store.Scope to exactly that tenant (RLS/triggers
// are the backstop). Sensitive reads (the access graph, the ledger) are
// self-audited with the real principal as actor.
//
// Modules extend the surface through the APIModule seam (routes mounted under
// /v1/m/<namespace>/, already wrapped with auth + tenant + authz) and the
// DataConsumer seam (a tenant-scoped data handle exposing only View/Mutate, never
// System). The gRPC ControlPlane service is a focused, frozen mirror of the REST
// surface that reuses the exact same authenticate→resolve-tenant→authorize path.
package api
