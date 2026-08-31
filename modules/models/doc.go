// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package models is module X of the control plane (README.md): governance of
// the whole AI model/provider stack — Claude, OpenAI, Gemini and local
// inference — not just Claude Code.
//
// It is an AGPL module that sits ON TOP of the model/provider connectors
// (connectors/modelprovider, connectors/modelrouter, both Apache-2.0): it does
// NOT re-implement the integration with any provider nor the inference gateway.
// What it owns is the GOVERNANCE layer:
//
//   - Reference catalog (reference.go): a declared, versioned, operator-overridable
//     table of model families with their API-feature capabilities (the full
//     Claude stack — caching, batch, files, citations, extended thinking,
//     computer use, memory tool, context management, vision/PDF, structured
//     outputs — plus the cross-vendor analogs) and list pricing (USD/MTok,
//     stamped AsOf + Source, never fabricated telemetry). These are declared
//     defaults the operator verifies against each provider's pricing page.
//
//   - Enrichment (enrich.go): module X listens to the cost.sampled stream and
//     enriches the core Model/Provider entities (which inventory discovers bare)
//     with family, context window, modality, per-token pricing and the
//     capability set — the "FinOps enriches the pricing fields" the
//     inventory module defers to it (modules/inventory/entities.go:174).
//
//   - Routing policy (routing.go): named selection/fallback/version-pinning
//     policies, persisted on the core Policy entity (Kind="routing"). The module
//     DEFINES and manages the policy and resolves it with connectors/modelrouter
//     (cost/latency/capability/pinned), returning a primary + fallback chain; the
//     ACTUAL routing is executed by the gateway/connector, not here.
//
//   - API-key / workspace governance (keys.go): references to provider keys and
//     workspaces — which agent/team uses which credential — as MINIMAL-DATA
//     metadata only (a masked hint, never the secret value; docs/SECURITY-HARDENING.md).
//
// Minimal data and security by design (docs/SECURITY-HARDENING.md): the module persists governance
// metadata and relationships, never a credential value, a prompt or a completion.
// Cost/governance reads are gated by RBAC at the API and key/routing mutations
// are audited.
package models
