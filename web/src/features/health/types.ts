// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// DTOs for Health/SLA (module XXII) — a 1:1 mirror of modules/health/dto.go
// (statusDTO / slaDTO / incidentDTO / eventDTO / depGraphResponse). The engine
// DERIVES health from observed liveness + active reports + the staleness sweep; it
// never probes infra. The web renders what the engine returns — it adds no
// logic, computes no product value (ARCHITECTURE.md). Refs and detail arrive ALREADY
// REDACTED / hashed (docs/SECURITY-HARDENING.md): there is never a payload, a secret, PII or raw
// error text here, so the UI never re-parses a ref nor expects one.

import type { SourceMode } from '@/features/shared'

/**
 * A subject's current health. `unknown` means NO liveness signal at all. `observed`
 * (dependency-map nodes only) means seen alive by an edge but with NO declared check
 * — health is NOT measured, so it is neither "healthy" nor "unknown".
 */
export type HealthState =
  'healthy' | 'degraded' | 'down' | 'observed' | 'unknown' | (string & {})

/** What a subject IS: an agent or an MCP server. */
export type SubjectKind = 'agent' | 'mcp' | (string & {})

/** Operator intent for a check. `active` alerts; `paused`/`retired` do not. */
export type DesiredStatus = 'active' | 'paused' | 'retired' | (string & {})

/** Incident kind / lifecycle state. */
export type IncidentKind = 'degraded' | 'down' | 'sla_breach' | (string & {})
export type IncidentState = 'open' | 'resolved' | (string & {})

/** What produced a reliability transition: observed liveness (edge), active probe
 * (report) or the staleness sweep (sweep — silence within the expected cadence). */
export type EventCause = 'edge' | 'report' | 'sweep' | (string & {})

/** A dependency-graph node kind (mapped to a layout layer by dependency-model). */
export type DepNodeKind =
  'agent' | 'mcp' | 'session' | 'mcp_tool' | (string & {})

/** A dependency relation kind (decides the edge style). */
export type DepRelation =
  'uses_mcp' | 'uses_tool' | 'delegates_to' | (string & {})

/**
 * statusDTO — the current health of one monitored subject (the projection of a
 * `health.check`). `/checks` returns the same shape (a check IS a status row).
 */
export interface StatusDTO {
  id: string
  name?: string
  subject_kind: SubjectKind
  subject_ref: string
  state: HealthState
  desired_status: DesiredStatus
  expected_interval_seconds: number
  grace_factor: number
  /** SLA target in PPM (999000 = 99.9%); 0 = no target declared. */
  sla_target_ppm: number
  sla_breach_open: boolean
  owner_actor?: string
  /** Last signal/sweep that touched the check. */
  last_checked_at?: string
  /** Last observed liveness (a healthy signal). */
  last_seen_at?: string
  last_latency_ms: number
  /** Opaque hash of the last detail (docs/SECURITY-HARDENING.md) — never displayable text; the UI
   * does not render it. Mirrors dto.go `last_detail_hash`. */
  last_detail_hash?: string
  created_at?: string
}

/** Payload for declaring a monitored subject. The UI sends every displayed
 * configuration field so the engine's defaults never diverge from the form. */
export interface CreateCheckInput {
  name: string
  subject_kind: 'agent' | 'mcp'
  subject_ref: string
  expected_interval_seconds: number
  grace_factor: number
  sla_target_ppm: number
  desired_status: 'active' | 'paused' | 'retired'
}

/** Partial check update. A check's subject is its immutable natural key, so it is
 * intentionally absent. `sla_target_ppm` is required on UI-authored updates:
 * explicit 0 clears the target, while omission has backend keep semantics. */
export interface UpdateCheckInput {
  name?: string
  expected_interval_seconds?: number
  grace_factor?: number
  sla_target_ppm: number
  desired_status?: 'active' | 'paused' | 'retired'
}

/**
 * slaDTO — a reliability report over a trailing window, reconstructed from the
 * append-only `health.event` ledger. `degraded` counts as UP for the SLA but is
 * reported SEPARATELY (degraded_seconds) — never added to downtime.
 */
export interface SlaDTO {
  subject_kind: SubjectKind
  subject_ref: string
  window_seconds: number
  /** The span actually covered by ledger history (uptime is computed over THIS,
   * not the full window). */
  observed_seconds: number
  /** false = no history yet → uptime is UNDEFINED (0 here means "no data", NOT 0%).
   * The UI MUST gate on this before reading uptime_percent (dto.go has_data). */
  has_data: boolean
  uptime_ppm: number
  /** = uptime_ppm / 1e6 * 100. Use this to render; do not recompute from seconds. */
  uptime_percent: number
  downtime_seconds: number
  degraded_seconds: number
  current_state: HealthState
  /** false = no check declared for the subject (honest state, not a failure). */
  has_check: boolean
  /** 0 if the check fixes no target. */
  sla_target_ppm: number
  /** uptime_ppm < sla_target_ppm (only when target > 0). */
  breaching: boolean
}

/**
 * incidentDTO — one open→resolved reliability incident. A `down` whose underlying
 * transition is `cause=sweep` is silence within the cadence — possible evasion.
 */
export interface IncidentDTO {
  id: string
  subject_kind: SubjectKind
  subject_ref: string
  check_ref?: string
  kind: IncidentKind
  severity: string
  state: IncidentState
  opened_at: string
  /** Omitted while open. */
  resolved_at?: string
  /** Short, non-sensitive summary (e.g. "agent … is DOWN"). */
  summary?: string
}

/**
 * eventDTO — one entry in the append-only reliability ledger (the basis of the SLA
 * calculation). `latency_ms = -1` means unknown. There is NO error text — the
 * sensitive detail is hashed in the ledger and is not exposed.
 */
export interface EventDTO {
  id: string
  subject_kind: SubjectKind
  subject_ref: string
  check_ref?: string
  state: HealthState
  prev_state: HealthState
  cause: EventCause
  /** -1 = unknown. */
  latency_ms: number
  occurred_at: string
}

/** One node of the auto-discovered dependency graph, annotated with current health. */
export interface DepNode {
  id: string
  kind: DepNodeKind
  ref: string
  /** healthy | degraded | down | observed | unknown — color the node by THIS, not by
   * kind. `observed` = seen alive, no declared check (health not measured). */
  health: HealthState
}

/** One dependency edge (React Flow source/target are the node refs). */
export interface DepEdge {
  id: string
  source: string
  target: string
  from_kind: DepNodeKind
  to_kind: DepNodeKind
  relation: DepRelation
  observed_count: number
  first_seen_at: string
  last_seen_at: string
}

/** The React Flow data contract from /dependencies (keyset-paginated). */
export interface DepGraphResponse {
  nodes: DepNode[]
  edges: DepEdge[]
  cursor?: string
  has_more: boolean
}

// --- Connector health + public status page -----------------------------

/** Per-connector health (GET /v1/connectors/health). */
export interface ConnectorHealthDTO {
  name: string
  kind: string
  title?: string
  tenant?: string
  status: string
  source_mode?: SourceMode
  enabled: boolean
  poll_seconds?: number
  last_polled_at?: string
  error_count_24h: number
  avg_latency_ms: number
  trend: 'up' | 'down' | 'stable' | (string & {})
  health_state: HealthState
}

export interface ConnectorSummaryDTO {
  total: number
  running: number
  failed: number
  stopped: number
  disabled: number
}

export interface ConnectorHealthResponse {
  items: ConnectorHealthDTO[]
  summary: ConnectorSummaryDTO
  timestamp: string
}

/** Public aggregate status (GET /status, unauthenticated).
 *  `not_configured` is its OWN state, not a flavour of `degraded`: an optional
 *  capability nobody provisioned is incomplete, not broken. Anything unknown is
 *  rendered as a fault — never as healthy. */
export type ComponentStatus =
  'operational' | 'not_configured' | 'degraded' | 'outage' | (string & {})

export interface ComponentStatusDTO {
  name: string
  status: ComponentStatus
}

export interface PublicStatusResponse {
  status: ComponentStatus
  components: ComponentStatusDTO[]
  timestamp: string
}
