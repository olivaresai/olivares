// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package claudeadoption is the Claude Code adoption / productivity read-model and
// dashboard backend (gap #12). It answers the adoption/ROI question the
// Claude-centric ICP asks — "how much is Claude Code being used, and how much of what it
// proposes do developers keep?" — alongside the FinOps cost surface.
//
// It consumes the MetricSample bus signal both Claude connectors now emit (the OTLP
// receiver's per-session productivity datapoints and the admin Analytics feed's per-
// developer/day totals), folds them into a per-(subject, day, dimension) time-series
// read-model, and serves aggregations (acceptance-rate, model-mix, LoC/commits/PRs by
// team/developer/day) under deny-closed permissions.
//
// Minimal-data (docs/SECURITY-HARDENING.md): the per-developer email is the ROI subject (the accepted
// attribution exception the cost path makes for Actor); the per-developer READ is gated
// behind adoption:developer:read, while the team/org aggregates never expose it (per-team default, per-developer opt-in). It NEVER carries cost (cost is the authoritative
// FinOps/api_request surface — a measure here can never double-count it).
//
// BOUNDARY (honest, non-optional): the read-model covers only what flows over the Claude
// API plane — the admin Analytics feed and the OTLP exporter. A Claude Code estate served
// by Claude Platform on AWS, Microsoft Foundry, Amazon Bedrock or Vertex AI that does not
// export this telemetry is invisible here, so absence of adoption is never proof of absence.
package claudeadoption
