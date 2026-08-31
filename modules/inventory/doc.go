// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package inventory is module I of the control plane (README.md): passive
// discovery and cataloging of everything that exists in the estate — agents,
// sessions, Claude Code instances, MCP servers, skills, tools, models,
// providers and non-human identities (NHI).
//
// It is a bus-driven AGPL module. It subscribes to the normalized observations
// the connectors emit (edge.observed / cost.sampled / finding.reported; the
// contract) and MATERIALIZES the core entities those observations imply — the
// connectors never emit entities, only edges and samples, so the module derives
// Session/Agent/MCPServer/Tool/Resource/Skill/Model/Provider/Identity from the
// natural references on the edges. For every materialized entity it
// keeps a catalog entry (its own registered entity, inventory.catalog_entry)
// recording how it was discovered (signal sources, first/last seen, occurrence)
// and its liveness — the spine of the catalog and the staleness sweep.
//
// It does NOT record the AccessEdge: the whole R/RW graph (edges, topology and
// the permitted-vs-observed drift) is owned by module III (the access map),
// the SOLE writer of AccessEdge, which reconciles identity across signals onto a
// canonical origin — something inventory's naive per-signal write could not do
// (decision A, 2026-06-03). Inventory discovers and
// catalogs the entities an edge names records the edges.
//
// Minimal data (docs/SECURITY-HARDENING.md): the module stores relationships, identifiers and
// liveness, never payloads, secrets or PII. References arrive already redacted
// from the connectors; the module persists them verbatim and adds no
// raw detail of its own.
package inventory
