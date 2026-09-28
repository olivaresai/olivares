// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// DTOs for the estate inventory (module I) — a 1:1 mirror of
// modules/inventory/dto.go. The catalog is a passive DISCOVERY overlay: connectors
// emit observations, the module materializes the core entities they name, and this
// is the operator's navigable estate. Minimal-data (docs/SECURITY-HARDENING.md): only references,
// classifications and liveness counters — never payloads, secrets or PII.

/** The entity kinds the catalog discovers. */
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
  | (string & {})

/** Discovery liveness stored on the catalog row. `active` means the row still
 * holds that stored state; it does not prove a recent sweep or current health.
 * `stale` means the staleness mechanism classified silence against a cutoff
 * that ran — not offline, removed, or unhealthy. */
export type EntityStatus = 'active' | 'stale' | (string & {})

/** One catalog entry: a discovered entity with its provenance and liveness. */
export interface CatalogEntry {
  kind: EntityKind
  entity_id: string
  name: string
  ref?: string
  status: EntityStatus
  signal_sources: string[]
  hosts?: string[]
  first_seen: string
  last_seen: string
  /** Instant the source declared, omitted when it declared none. Distinct from
   *  `last_seen`, which is this platform's local reception clock. */
  occurred_at?: string
  occurrence_count: number
}

/** Per-kind tally in the estate summary. */
export interface KindCount {
  active: number
  stale: number
  total: number
}

/** Estate overview: counts by kind and by signal source. */
export interface InventorySummary {
  by_kind: Record<string, KindCount>
  by_source: Record<string, number>
  total: number
  truncated?: boolean
}

/** A catalog entry plus a minimal projection of the underlying core entity. */
export interface EntityDetail {
  entry: CatalogEntry
  detail?: Record<string, unknown>
}

// Collection coverage — a mirror of GET /v1/m/inventory/collections as published in
// core/api/openapi_inventory_contracts.go (DTOs in modules/inventory/coverage_read.go).
// Coverage describes ONE opened registration's versioned query over its observed
// interval: not a global estate snapshot, not a provider authorization.

/** Coverage of the current run. `unknown` is also the answer for a selection with no
 *  stored report — it is never "none" and never inherited completeness. */
export type CollectionCoverage =
  'complete' | 'partial' | 'unavailable' | 'unsupported' | 'unknown'

export type CollectionReason =
  | ''
  | 'exhausted'
  | 'page_limit'
  | 'repeated_cursor'
  | 'invalid_response'
  | 'scope_unproven'
  | 'scope_mismatch'
  | 'provider_error'
  | 'offline'
  | 'disabled'
  | 'member_limit'
  | 'missing_report'
  | 'protocol_error'
  | 'gather_error'
  | 'canceled'
  | 'sink_error'
  | 'persistence_rejected'
  | 'commit_outcome_unknown'
  | 'persistence_canceled'
  | 'persistence_unavailable'
  | 'persistence_error'

/** Queued members stay `pending` until their receipt commits; enumeration alone
 *  cannot qualify them. */
export type CollectionProjection = 'pending' | 'committed' | 'failed'

export type CollectionRejection =
  | 'terminal_conflict'
  | 'linkage_conflict'
  | 'member_conflict'
  | 'receipt_conflict'

/** The current run of the selected registration. With no run, `run_id` and
 *  `host_started_at` are empty, `run_order` and the counts are zero defaults, and
 *  coverage is `unknown` with reason `missing_report`. Instants are RFC3339. */
export interface CollectionResult {
  run_id: string
  source_id: string
  source_revision: number
  environment_ref: string
  run_order: number
  scope_contract?: 'azure-resource-graph/2022-10-01/id-v1'
  family?: 'azure.resource'
  /** SHA-256 fingerprints of the versioned query scope. */
  requested_scope?: string
  fulfilled_scope?: string
  coverage: CollectionCoverage
  reason: CollectionReason
  projection: CollectionProjection
  admitted_count: number
  committed_count: number
  expected_count: number
  host_started_at: string
  host_finished_at?: string
  producer_started_at?: string
  producer_finished_at?: string
  qualified_at?: string
  rejection_reason?: CollectionRejection
}

/** Immutable historical qualification for this exact registration and scope. Later
 *  rejection evidence on the current run does not rewrite it. */
export interface CollectionQualifiedSuccess {
  run_id: string
  source_id: string
  source_revision: number
  environment_ref: string
  scope_contract: 'azure-resource-graph/2022-10-01/id-v1'
  family: 'azure.resource'
  requested_scope: string
  fulfilled_scope: string
  expected_count: number
  qualified_at: string
  host_started_at: string
  host_finished_at: string
  producer_started_at: string
  producer_finished_at: string
}

/** One head: the current run and, absent until a run qualifies, the last success. */
export interface CollectionEvidence {
  current: CollectionResult
  last_qualified_success?: CollectionQualifiedSuccess
}

/** 200 page. `items` holds exactly one head for the selection; `has_more` is the
 *  list-envelope continuation flag, not evidence of collection completeness. */
export interface CollectionPage {
  items: CollectionEvidence[]
  cursor?: string
  has_more: boolean
}
