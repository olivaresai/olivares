// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Deterministic fixtures for the Observability tests, shaped EXACTLY like the live
// modules/observability responses. They are TEST DATA only — the view renders
// what the backend returns. The standard ids/versions/maturities/statuses mirror the
// backend's verified pins (2026-07-05), cited per-row to the code that defines them:
//  - OTel GenAI semconv 1.41.1, Development, gate semconv_opt_in=gen_ai_latest_experimental
//    OFF by default                    → connectors/claude/genai.go genAISemconvVersion/genAIOptInToken; config.go cfgSemconvOptIn
//    upstream ref main@c321d7e         → connectors/claude/genai.go genAISemconvUpstreamRepo/Ref
//  - OCSF 1.8.0, GA, AVAILABLE (emission needs an operator-provisioned ocsf notify
//    destination or the on-demand pull export — nothing emits by default)
//                                      → sdk/siemwire/ocsf.go:60
//  - Sentinel ASIM AgentEvent 0.1.0, pre-1.0 → siemfmt/aiformats.go:226
//  - SIEM unified CEF/LEEF/syslog/OTLP → sdk/siemwire (OBS-08(A))
//  - ledger-push (blocked: Forwarder seam has zero call sites; live forwarder is
//    )                           → core/audit/forward.go
//  - Prometheus text 0.0.4, stable, active (always served) → core/metrics/metrics.go:46
//  - W3C Trace Context, stable, active (ingress extractor always runs; ids stamped
//    into the ledger)                  → core/observability/trace/middleware.go
// NO per-standard throughput figure is invented (records_total only where soundly
// attributable — absent here); the per-SOURCE counters are the live half.
import type {
  IngestionHealthResponse,
  TraceDetail,
  TraceListItem,
} from './types'

export const ingestionHealthFixture: IngestionHealthResponse = {
  engine_scope: true,
  since: '2026-06-11T08:00:00Z',
  standards: [
    {
      id: 'otel_genai',
      label: 'OpenTelemetry GenAI semconv',
      direction: 'in',
      maturity: 'development',
      version: '1.41.1',
      upstream_repo: 'open-telemetry/semantic-conventions-genai',
      upstream_ref: 'main@c321d7e, verified 2026-07-05',
      // Config key=token, verbatim (config.go cfgSemconvOptIn + genai.go genAIOptInToken). opt_in_active is
      // intentionally ABSENT: the engine cannot read connector config; it only
      // asserts true when gen_ai evidence flowed on the bus.
      opt_in_gate: 'semconv_opt_in=gen_ai_latest_experimental',
      status: 'opt_in_off',
    },
    {
      id: 'ocsf',
      label: 'OCSF (ai_operation profile)',
      direction: 'out',
      maturity: 'ga',
      version: '1.8.0',
      status: 'available',
    },
    {
      id: 'asim_agentevent',
      label: 'Microsoft Sentinel ASIM AgentEvent',
      direction: 'out',
      maturity: 'pre_1_0',
      version: '0.1.0',
      status: 'available',
    },
    {
      id: 'siem_unified',
      label: 'SIEM unified (CEF / LEEF / syslog / OTLP)',
      direction: 'out',
      maturity: 'stable',
      version: '—',
      status: 'available',
    },
    {
      id: 'ledger_push',
      label: 'Ledger push transport',
      direction: 'out',
      maturity: 'development',
      version: '—',
      status: 'blocked',
    },
    {
      id: 'prometheus_text',
      label: 'Prometheus text exposition',
      direction: 'out',
      maturity: 'stable',
      version: '0.0.4',
      status: 'active',
    },
    {
      id: 'w3c_trace_context',
      label: 'W3C Trace Context (ledger correlation)',
      direction: 'in',
      maturity: 'stable',
      version: '—',
      status: 'active',
    },
  ],
  sources: [
    {
      name: 'olivares.claude',
      records_total: 42,
      first_seen: '2026-06-11T08:05:12Z',
      last_seen: '2026-06-11T10:11:58Z',
      kinds: { edge: 38, cost: 2, finding: 2 },
      signals: { otel: 30, mcp_annotation: 8 },
    },
    {
      name: 'olivares.security',
      records_total: 3,
      first_seen: '2026-06-11T09:00:00Z',
      last_seen: '2026-06-11T09:45:00Z',
      kinds: { finding: 3 },
    },
  ],
}

// --- traces (ledger-correlation read-model) -----------------------------------
//
// Shaped like the REAL backend derivation (spec A2): one TraceSpan per
// DISTINCT engine span id, kind "ledger", status "unset" (the ledger stores no
// OTel status), parent_span_id absent (parentage is not stored), durations =
// ledger-event windows, service = the single ledger writer.

export const traceListFixture: TraceListItem[] = [
  {
    trace_id: '4bf92f3577b34da6a3ce929d0e0e4736',
    root_name: 'session.start',
    started_at: '2026-06-11T10:12:03Z',
    duration_ms: 1840,
    span_count: 2,
    agent_count: 2,
    status: 'unset',
    services: ['olivares'],
  },
  {
    trace_id: '8a3c60f7d188f8fa79d48a63c1f6f3a1',
    root_name: 'guard.decision',
    started_at: '2026-06-11T10:09:41Z',
    duration_ms: 0,
    span_count: 1,
    agent_count: 1,
    status: 'unset',
    services: ['olivares'],
  },
]

export const traceDetailFixture: TraceDetail = {
  trace_id: '4bf92f3577b34da6a3ce929d0e0e4736',
  started_at: '2026-06-11T10:12:03Z',
  duration_ms: 1840,
  spans: [
    {
      span_id: '00f067aa0ba902b7',
      name: 'session.start (+2 events)',
      service: 'olivares',
      kind: 'ledger',
      start_ms: 0,
      duration_ms: 1840,
      status: 'unset',
      actor: 'user:admin',
      actor_kind: 'user',
      entity_ref: 'session:sess_01',
      attributes: {
        'ledger.events': '2',
        'ledger.actions': 'session.start,session.update',
        'ledger.actor': 'user:admin',
        'ledger.seq': '118-121',
      },
    },
    {
      span_id: 'a1b2c3d4e5f60718',
      name: 'guard.decision',
      service: 'olivares',
      kind: 'ledger',
      start_ms: 420,
      duration_ms: 0,
      status: 'unset',
      actor: 'agent:bot-1',
      actor_kind: 'agent',
      entity_ref: 'guard:output',
      attributes: {
        'ledger.events': '1',
        'ledger.actions': 'guard.decision',
        'ledger.actor': 'agent:bot-1',
        'ledger.seq': '120-120',
      },
    },
  ],
}

// --- synthetic OTel-shaped trace (pure-component tests only) -------------------
//
// TraceWaterfall renders ANY TraceSpan (it lays out whatever offsets/parentage the
// data gives), so the pure waterfall tests keep an OTel-shaped tree to exercise the
// parent-child indentation and status colouring that the ledger read-model never
// produces. Container tests use the ledger-shaped fixtures above.

export const otelTraceDetailFixture: TraceDetail = {
  trace_id: 'aaf92f3577b34da6a3ce929d0e0e4700',
  started_at: '2026-06-04T10:12:03Z',
  duration_ms: 1840,
  spans: [
    {
      span_id: '00f067aa0ba902b7',
      name: 'POST /v1/agents/support-triage:invoke',
      service: 'gateway',
      kind: 'server',
      start_ms: 0,
      duration_ms: 1840,
      status: 'ok',
      entity_ref: 'agent:support-triage',
      attributes: {
        'http.request.method': 'POST',
        'w3c.traceparent.sampled': 'true',
      },
    },
    {
      span_id: 'a1b2c3d4e5f60718',
      parent_span_id: '00f067aa0ba902b7',
      name: 'chat anthropic',
      service: 'support-triage',
      kind: 'client',
      start_ms: 90,
      duration_ms: 1180,
      status: 'ok',
      entity_ref: 'model:claude-opus-4-8',
      attributes: {
        'gen_ai.provider.name': 'anthropic',
        'gen_ai.request.model': 'claude-opus-4-8',
        'gen_ai.operation.name': 'chat',
      },
    },
    {
      span_id: 'b2c3d4e5f6071829',
      parent_span_id: 'a1b2c3d4e5f60718',
      name: 'execute_tool memory.search',
      service: 'support-triage',
      kind: 'internal',
      start_ms: 320,
      duration_ms: 210,
      status: 'ok',
      entity_ref: 'tool:memory.search',
      attributes: {
        'gen_ai.operation.name': 'execute_tool',
        'gen_ai.tool.name': 'memory.search',
      },
    },
    {
      span_id: 'c3d4e5f607182930',
      parent_span_id: 'b2c3d4e5f6071829',
      name: 'GET memory-store',
      service: 'memory-store',
      kind: 'client',
      start_ms: 360,
      duration_ms: 150,
      status: 'ok',
      attributes: { 'db.system': 'sqlite' },
    },
    {
      span_id: 'd4e5f60718293041',
      parent_span_id: '00f067aa0ba902b7',
      name: 'guardrail.evaluate',
      service: 'gateway',
      kind: 'internal',
      start_ms: 1290,
      duration_ms: 120,
      status: 'ok',
      entity_ref: 'guardrail:output',
    },
    {
      span_id: 'e5f6071829304152',
      parent_span_id: '00f067aa0ba902b7',
      name: 'audit.append',
      service: 'gateway',
      kind: 'producer',
      start_ms: 1420,
      duration_ms: 60,
      status: 'ok',
    },
  ],
}
