// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Wire DTOs the plugin consumes from the Olivares control plane (via the portal
// proxy backend). These mirror the engine's published shapes 1:1 — core
// AgentDTO/AccessEdgeDTO (core/api/dto.go) and the module DTOs for inventory
// (module I), sessions (module II) and the access map (module III). Minimal-data
// (docs/SECURITY-HARDENING.md): only references, classifications, liveness and counters cross the
// wire — never payloads, secrets or PII — so this file declares exactly that and
// the UI never asks for more than the contract carries.

/** The paginated envelope every list endpoint returns (items + opaque cursor). */
export interface ListResponse<T> {
  items: T[];
  cursor?: string;
  has_more: boolean;
}

// --- core: agents + whoami -----------------------------------------------------

/** An agent as published by GET /v1/agents. */
export interface AgentDTO {
  id: string;
  tenant_id: string;
  name: string;
  kind: string;
  external_id?: string;
  status: string;
  identity_id?: string;
  labels?: Record<string, unknown>;
  metadata?: Record<string, unknown>;
  created_at: string;
  updated_at: string;
  version: number;
}

/** One tenant grant in the calling principal's identity (GET /v1/auth/whoami). */
export interface WhoamiGrant {
  tenant: string;
  role: string;
}

/** The calling principal as the control plane sees it (after the proxy forward). */
export interface Whoami {
  kind: string;
  user_id?: string;
  actor: string;
  display_name?: string;
  superadmin: boolean;
  grants: WhoamiGrant[];
}

// --- module I: inventory -------------------------------------------------------

/** The entity kinds the discovery catalog materializes. */
export type EntityKind =
  | 'session'
  | 'agent'
  | 'identity'
  | 'mcp_server'
  | 'tool'
  | 'resource'
  | 'skill'
  | 'model'
  | 'provider'
  | (string & {});

/** Discovery liveness: `active` seen recently, `stale` gone quiet past the sweep. */
export type EntityStatus = 'active' | 'stale' | (string & {});

/** One catalog entry: a discovered entity with its provenance and liveness. */
export interface CatalogEntry {
  kind: EntityKind;
  entity_id: string;
  name: string;
  ref?: string;
  status: EntityStatus;
  signal_sources: string[];
  hosts?: string[];
  first_seen: string;
  last_seen: string;
  occurrence_count: number;
}

/** Per-kind tally in the estate summary. */
export interface KindCount {
  active: number;
  stale: number;
  total: number;
}

/** Estate overview: counts by kind and by signal source. */
export interface InventorySummary {
  by_kind: Record<string, KindCount>;
  by_source: Record<string, number>;
  total: number;
  truncated?: boolean;
}

// --- module II: sessions (live operation) --------------------------------------

/**
 * The session control-plane state, DERIVED by the backend at read time. Never
 * fabricate it on the client. `silent_evasion` = gone quiet INSIDE its expected
 * cadence (a possible-evasion signal worth the operator's eye, docs/SECURITY-HARDENING.md).
 */
export type CcState = 'active' | 'idle' | 'ended' | 'silent_evasion' | (string & {});

/** One live session snapshot (GET /v1/m/sessions/live[/{ref}]). */
export interface LiveDTO {
  session_ref: string;
  agent_ref?: string;
  cc_state: CcState;
  current_action?: string;
  current_resource?: string;
  current_mode?: string;
  model_ref?: string;
  input_tokens: number;
  output_tokens: number;
  cost_micro_usd: number;
  event_count: number;
  tool_call_count: number;
  first_event_at: string;
  last_event_at: string;
  duration_seconds: number;
  goal?: string;
  summary?: string;
}

/** One reconstructible timeline entry (GET /v1/m/sessions/live/{ref}/timeline). */
export interface TimelineDTO {
  at: string;
  kind: 'tool' | 'mcp' | 'cost' | 'finding' | (string & {});
  tool_ref?: string;
  resource_ref?: string;
  mode?: string;
  source?: string;
  title?: string;
}

// --- module III: access map (R/RW) + drift -------------------------------------

/** R/RW access mode on an edge. `readwrite`/`write` carry risk; `unknown` is gray. */
export type AccessMapMode = 'read' | 'write' | 'readwrite' | 'unknown' | (string & {});

/** Attribution confidence — `attributed` is firm, `approximate` is inferred. */
export type AccessConfidence = 'attributed' | 'approximate' | (string & {});

/**
 * Honest per-edge firmness of the origin→agent/NHI attribution (G8):
 * `firm` only with an SVID/WIF/dedicated credential; `approximate` for a shared
 * account; `unknown` for a store with no per-identity audit. STRICTER than
 * confidence — the UI must NEVER render approximate/unknown as if it were firm.
 */
export type AttributionTier = 'firm' | 'approximate' | 'unknown' | (string & {});

/** A graph node, derived by the engine from the endpoints of the returned edges. */
export interface AccessNode {
  id: string;
  /** agent | session | identity | <resource.kind> (e.g. postgres.table, mcp.tool). */
  kind: string;
  ref?: string;
}

/** One access edge: origin → resource, with mode, provenance and the diff flags. */
export interface AccessEdge {
  id: string;
  origin_kind: string;
  origin_id: string;
  origin_ref?: string;
  resource_id: string;
  resource_kind?: string;
  resource_ref?: string;
  tool_ref?: string;
  mode: AccessMapMode;
  signal_source: string;
  signal_sources?: string;
  confidence: AccessConfidence;
  bridged: boolean;
  coverage_tier?: string;
  attribution_reason?: string;
  attribution_tier?: AttributionTier;
  attribution_tier_reason?: string;
  observed: boolean;
  permitted: boolean;
  occurrence_count: number;
  first_seen: string;
  last_seen: string;
}

/** The React-Flow-style data contract from /graph and /neighbors. */
export interface GraphResponse {
  nodes: AccessNode[];
  edges: AccessEdge[];
  cursor?: string;
  has_more: boolean;
}

/** Wire drift kinds (modules/access-map/dto.go). */
export type DriftKind = 'unexpected_access' | 'unused_grant' | (string & {});

/**
 * One least-privilege discrepancy. `reconciliation_pending` marks an unexpected
 * access whose permitted-ness cannot yet be decided (agent↔identity link
 * unresolved): render it AMBER ("pending"), never red — honest uncertainty.
 */
export interface DriftEntry {
  kind: DriftKind;
  reconciliation_pending?: boolean;
  edge: AccessEdge;
}

/** The permitted-vs-observed result. `unexpected_*` is the security headline. */
export interface DiffResponse {
  unexpected_accesses: DriftEntry[];
  unused_grants: DriftEntry[];
  unexpected_count: number;
  unused_count: number;
}
