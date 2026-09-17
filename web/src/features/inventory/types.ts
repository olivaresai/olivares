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
