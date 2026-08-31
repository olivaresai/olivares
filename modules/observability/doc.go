// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package observability is the engine's observability read-model module: it
// answers, over /v1/m/observability/, the three questions the admin view
// (ADM-OBS-01) declared seams for — what flows in/out of the engine per interop
// standard, what the W3C-correlated ledger says about a trace, and what is
// provably true about the RUNNING binary's supply chain.
//
// Bounded context. The module owns NO store entities and persists nothing: it
// is a pure read-model over three substrates that already exist.
//   - GET /ingestion-health: a static table of the interop standards the engine
//     pins (OTel GenAI semconv, OCSF, ASIM AgentEvent, the unified SIEM formats,
//     ledger push, Prometheus text, W3C Trace Context), each with its verified
//     upstream version and TRUE operational status — plus live, in-memory
//     per-source counters fed by the event bus (edge.observed / cost.sampled /
//     finding.reported). The counters are PROCESS-GLOBAL (engine_scope=true,
//     like /metrics, OBS-06) and reset on restart; the response carries `since`
//     so a reader knows the window.
//   - GET /traces (+ /traces/{id}): a LEDGER-CORRELATION read-model. The only
//     persisted trace data in the product is the W3C trace_id/span_id pair the
//     audit chokepoint stamps into every event's Meta
//     (core/internal/store/sqlstore/audit.go:56-63, keys
//     core/observability/trace/meta.go:16-22). The module groups the tenant's
//     recent ledger events by trace_id and presents them as spans-of-events —
//     it NEVER fabricates OTel span data the engine does not store: durations
//     are ledger-event windows, status is always "unset", kind is the honest
//     non-OTel label "ledger". Full spans live in the operator's OTLP
//     collector, not here.
//   - GET /attestation: measured truth about the running binary (ldflags build
//     metadata, debug.ReadBuildInfo, FIPS 140-3 mode, a self-SHA256 of the
//     executable) plus honest NEGATIVES about release state (nothing is
//     published or signed yet) and the DECLARED-only release pipeline.
//     Measured vs declared is never blurred (ARCHITECTURE.md).
//
// Honesty constraints. The module presents, it never recomputes or invents
// (ARCHITECTURE.md): an unattributable counter is omitted, not zeroed; a gated
// profile's activity is claimed only on bus evidence; absence of a release is
// reported as absence. Everything it serves is either measured in-process or a
// version pin verified against the source tree (citations inline).
package observability
