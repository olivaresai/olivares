// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package health is module XXII — Health, SLA & uptime of agents and MCP servers
// (README.mdbis XXII). It answers three questions about the estate's AI
// components: what is healthy, what is degraded/down, and what depends on what.
//
// Bounded context. It measures the RELIABILITY of agents and MCP servers — not
// host/infra health in general (README.mdbis: "fiabilidad de agentes/MCPs").
// It owns four entities (schema.go): a health_check (an operator-declared
// monitored subject with an expected cadence and an SLA target), an append-only
// health_event transition ledger, a health_incident lifecycle, and an
// auto-discovered health_dependency edge (the dependency map). It also mirrors a
// subject's current state into the core HealthStatus entity reserved for it
// (core/model/entities.go, ARCHITECTURE.md) when the subject ref is a core id.
//
// How health is derived. The module is a CONSUMER of the core (ARCHITECTURE.md: it
// consumes "ingest, modelo, event bus"), not a prober — opening sockets to
// customer infra is a connector/external concern, and the sealed observation set
// has no health kind. So health is derived from signals the module can prove:
//   - Liveness from edge.observed (passive): a session/agent touching an
//     MCP server, or an agent acting, is evidence the subject is alive — it
//     refreshes the subject's last_seen and folds the dependency edge.
//   - Active probe results posted to POST /checks/{id}/report by an external
//     health-checker or the agent itself (the honest ingest path for the
//     "health checks / OTEL metrics" the product catalog names).
//   - Staleness (the background sweep): a known subject that stops being seen
//     within its expected cadence is itself a signal (anti-evasion, docs/SECURITY-HARDENING.md:
//     "si un agente conocido deja de emitir telemetría, eso en sí es una señal")
//     — it transitions to degraded, then down, opening an incident.
//
// Produces, does not deliver. XXII emits down/degraded/recovered/SLA-breach
// signals as minimal-data FindingReports on the bus (findings.go); module XV
// (notifications) routes them to Slack/PagerDuty/SIEM. XXII never delivers — the
// "Conecta: XV" split (README.mdbis XXII).
//
// Minimal data (docs/SECURITY-HARDENING.md). Health stores status, reliability metrics and
// dependency relations — never payloads, prompts, secrets or PII. The one
// sensitive detail a probe may carry (an error message) is reduced to a one-way
// hash in the event/incident; only a short, non-sensitive summary is displayed.
package health
