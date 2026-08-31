// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// DTOs for the Observability admin view (ADM-OBS-01) — LIVE since. The backend
// read-model is modules/observability: GET /ingestion-health serves per-standard pins
// plus per-source bus counters, and GET /traces[/{id}] serves the LEDGER-CORRELATION
// trace read-model (W3C trace ids stamped into audit-ledger meta since —
// core/observability/trace/meta.go:16-22). The view PRESENTS these shapes verbatim;
// it never recomputes or fabricates (ARCHITECTURE.md).
//
// Source-of-truth for the standards/versions the rows carry (verified 2026-07-05):
//  - OTel GenAI semconv  → connectors/claude/genai.go genAISemconvVersion ("1.41.1")
//    upstream ref          → connectors/claude/genai.go genAISemconvUpstreamRepo/Ref
//    opt-in token         → connectors/claude/genai.go genAIOptInToken ("gen_ai_latest_experimental")
//    gate key             → connectors/claude/config.go cfgSemconvOptIn ("semconv_opt_in")
//  - OCSF                 → sdk/siemwire/ocsf.go:60         (OCSFVersion "1.8.0")
//  - Sentinel ASIM        → connectors/internal/siemfmt/aiformats.go:226 (ASIMSchemaVersion "0.1.0")
//  - Prometheus text      → core/metrics/metrics.go:46      (exposition version "0.0.4")
//  - W3C Trace Context    → core/observability/trace/middleware.go (ingress extractor)
//  - engine /metrics,/livez,/readyz → core/api/server.go:232-234 (root-level, auth-exempt)

/** Lifecycle maturity of an interop standard, as the upstream body declares it —
 *  never inflated. `development` = the convention is pre-stable (e.g. OTel gen_ai);
 *  `pre_1_0` = pre-1.0 schema (e.g. ASIM AgentEvent 0.1.0). Widened with `| string`
 *  so a new upstream maturity does not break the type (ARCHITECTURE.md).*/
export type StandardMaturity =
  | 'development'
  | 'ga'
  | 'pre_1_0'
  | 'stable'
  | string

/** Direction of a standard relative to the engine: `in` = ingest profile we accept,
 *  `out` = export/emit format we produce. */
export type StandardDirection = 'in' | 'out' | string

/** Operational status of a standard's pipe. `active` = wired and accepting/emitting;
 *  `available` = implemented but not currently producing records; `opt_in_off` = a
 *  gated profile that is off by default; `blocked` = depends on an unshipped seam. */
export type StandardStatus =
  | 'active'
  | 'available'
  | 'opt_in_off'
  | 'blocked'
  | string

/** One interop standard's ingestion-health row (modules/observability ingestion.go).
 *  Versions/maturities are the verified upstream pins cited in the module header. */
export interface IngestionStandard {
  id: string
  label: string
  direction: StandardDirection
  maturity: StandardMaturity
  /** The pinned upstream version, verbatim (e.g. "1.41.1", "1.8.0", "0.1.0"). */
  version: string
  /** Optional unversioned upstream authority for standards whose live source moved
   *  off a versioned release. OTel GenAI keeps "1.41.1" as the wire vocabulary
   *  label and carries semantic-conventions-genai repo/ref separately. */
  upstream_repo?: string
  /** Commit/date ref for `upstream_repo`, verbatim from the backend mirror. */
  upstream_ref?: string
  /** Connector config key=token gating this profile, when it is opt-in (e.g.
   *  "semconv_opt_in=gen_ai_latest_experimental" for OTel gen_ai). Absent = no gate. */
  opt_in_gate?: string
  /** Whether the opt-in gate is observably satisfied. The backend only sets `true`
   *  when evidence flowed on the bus — absent means UNKNOWABLE from inside the
   *  engine (the gate lives in connector config), never a false-claimed "off". */
  opt_in_active?: boolean
  /** Records accepted/emitted via this standard. Present ONLY where the figure is
   *  soundly attributable to the standard — the view NEVER invents one.*/
  records_total?: number
  /** RFC3339 of the most recent record on this standard, when known. */
  last_seen?: string
  status: StandardStatus
}

/** Live per-source bus counters (additive): one row per bus Event.Source (a
 *  connector/module name like "olivares.claude"). Counters are process-global and
 *  reset on engine restart — like /metrics, never per-tenant. */
export interface IngestionSource {
  /** The bus Event.Source that emitted the records. */
  name: string
  records_total: number
  /** RFC3339 of the first/last record observed since engine start. */
  first_seen: string
  last_seen: string
  /** Records by observation kind: edge | cost | finding. */
  kinds: Record<string, number>
  /** Edge records by collector signal (EdgeObservation.Source: otel, pg_audit, …).
   *  Present only when the source emitted edges. */
  signals?: Record<string, number>
}

/** GET /v1/m/observability/ingestion-health (LIVE, modules/observability). */
export interface IngestionHealthResponse {
  standards: IngestionStandard[]
  /** true = the figures are ENGINE-WIDE (process-global), not per-tenant. The read
   *  model counts the in-process bus by construction (OBS-06); this flag carries
   *  that truth into the UI so it is never mistaken for a per-tenant view. */
  engine_scope: boolean
  /** Per-source live counters; [] when nothing was observed since engine start. */
  sources: IngestionSource[]
  /** RFC3339 of module start — counters accumulate since this instant and reset on
   *  restart (same lifecycle as /metrics). */
  since: string
}

// --- trace drill-down (ledger-correlation read-model) -------------------------
//
// HONESTY: the engine stores NO OTel spans. What it stores are audit-ledger events
// stamped with the caller's W3C trace_id/span_id (core/observability/trace/meta.go).
// A "span" below is therefore one DISTINCT engine span id grouping its ledger
// events, kind "ledger"; durations are LEDGER-EVENT WINDOWS (max−min OccurredAt),
// not OTel span durations. Full spans live in the operator's OTLP collector.

/** Span kind. The ledger read-model always reports "ledger" (an honest non-OTel
 *  label); the OTel kinds remain renderable for any future OTLP-backed source. */
export type SpanKind =
  | 'ledger'
  | 'server'
  | 'client'
  | 'producer'
  | 'consumer'
  | 'internal'
  | string

/** Span status. The ledger stores no OTel status code, so the read-model always
 *  reports "unset" — never a fabricated ok/error verdict. */
export type SpanStatus = 'ok' | 'error' | 'unset' | string

/** One span in a trace. Times are relative to the trace start (`start_ms`) so the
 *  waterfall can lay them out without re-deriving offsets (the view presents, it does
 *  not recompute — ARCHITECTURE.md).*/
export interface TraceSpan {
  span_id: string
  /** Parent span id. The ledger does not store span parentage, so the read-model
   *  omits it (flat waterfall); only an OTLP-shaped source could provide it. */
  parent_span_id?: string
  /** The action of the span's earliest ledger event, with an " (+N events)" suffix
   *  when the span groups N>1 events. */
  name: string
  service: string
  kind: SpanKind
  /** Offset of this span's earliest event from the trace start, in milliseconds. */
  start_ms: number
  /** Window covered by the span's OWN ledger events (0 for a single event) — NOT an
   *  OTel span duration. */
  duration_ms: number
  status: SpanStatus
  /** Actor identity from the span's earliest event (e.g. "user:admin", "agent:bot-1"). */
  actor?: string
  /** Actor kind: user | agent | connector | system. */
  actor_kind?: string
  /** "<target_kind>:<target_id>" of the span's earliest event, when targeted. */
  entity_ref?: string
  /** Synthesized, redacted attributes (ledger.events / ledger.actions / ledger.actor
   *  / ledger.seq). Values are metadata only — NEVER a prompt/completion/credential
   *  (docs/SECURITY-HARDENING.md); the backend never passes raw meta through.*/
  attributes?: Record<string, string>
}

/** GET /v1/m/observability/traces/{id} (LIVE). 404 = the trace is unknown or has
 *  been evicted from the ledger walk window (last 20000 events). */
export interface TraceDetail {
  trace_id: string
  /** RFC3339 of the trace's earliest ledger event, when known. */
  started_at?: string
  /** The trace's LEDGER-EVENT WINDOW (max−min event time) in milliseconds — not a
   *  wall-clock span duration. */
  duration_ms?: number
  spans: TraceSpan[]
}

/** One row in the trace list (GET /v1/m/observability/traces, LIVE). */
export interface TraceListItem {
  trace_id: string
  /** The action of the trace's earliest ledger event (lowest seq). */
  root_name: string
  started_at: string
  /** Ledger-event window in milliseconds (see TraceDetail.duration_ms). */
  duration_ms: number
  /** Number of DISTINCT engine span ids in the trace. */
  span_count: number
  /** Number of DISTINCT actors (agents/users) in the trace. */
  agent_count: number
  /** Always "unset" from the ledger read-model — no OTel status is stored. */
  status: SpanStatus
  /** The writing service(s). The ledger has a single writer, so this is
   *  ["olivares"] (core/observability/trace/config.go:40). */
  services: string[]
}
