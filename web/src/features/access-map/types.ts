// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// DTOs for the R/RW access map — a 1:1 mirror of modules/access-map/dto.go
// (graphResponse / edgeDTO / nodeDTO / diffResponse / driftDTO). The engine
// returns flat node+edge lists (no layout) renders. Every reference
// (origin_ref / resource_ref / tool_ref) arrives ALREADY REDACTED — there is
// never SQL, a payload or a secret here (docs/SECURITY-HARDENING.md), so the UI never re-parses
// a ref into a path and never asks for what the contract does not carry.

/** R/RW access mode on an edge. `readwrite`/`write` carry risk; `unknown` is gray. */
export type AccessMapMode =
  | 'read'
  | 'write'
  | 'readwrite'
  | 'unknown'
  | (string & {})

/** Attribution confidence — `attributed` is firm (solid), `approximate` is
 * inferred (dotted). The UI must never render `approximate` as if it were firm. */
export type AccessConfidence = 'attributed' | 'approximate' | (string & {})

/** Declared capture fidelity of the resource (ARCHITECTURE.md tiered coverage).*/
export type CoverageTier =
  | 'clean'
  | 'lossy'
  | 'opaque'
  | 'mixed'
  | (string & {})

/** Honest per-edge firmness of the origin→agent/NHI attribution (G8):
 * `firm` only with an SVID/WIF/dedicated credential, `approximate` for a shared
 * service account, `unknown` for a store with no per-identity audit (Redis/SQLite/
 * D1). It is STRICTER than confidence — the UI must NEVER render approximate/unknown
 * as if it were firm. */
export type AttributionTier = 'firm' | 'approximate' | 'unknown' | (string & {})

/** A graph node, derived by the engine from the endpoints of the returned edges. */
export interface AccessNode {
  id: string
  /** agent | session | identity | <resource.kind> (e.g. postgres.table, mcp.tool). */
  kind: string
  ref?: string
}

/** One access edge: origin → resource, with mode, provenance and the two diff
 * flags (observed/permitted) whose disagreement is the least-privilege drift. */
export interface AccessEdge {
  id: string
  origin_kind: string
  origin_id: string
  origin_ref?: string
  resource_id: string
  resource_kind?: string
  resource_ref?: string
  /** The verb/tool (e.g. SELECT) — never the statement. */
  tool_ref?: string
  mode: AccessMapMode
  /** The last scalar signal that wrote this edge. */
  signal_source: string
  /** CSV of ALL corroborating signals (fusion) — more sources = stronger edge. */
  signal_sources?: string
  confidence: AccessConfidence
  /** Whether the identity→agent bridge resolved (false = "unresolved agent"). */
  bridged: boolean
  coverage_tier?: CoverageTier
  attribution_reason?: string
  /** How firmly the origin ties to a concrete agent/NHI (firm/approximate/unknown). */
  attribution_tier?: AttributionTier
  /** Short, non-sensitive "why" for the attribution tier. */
  attribution_tier_reason?: string
  observed: boolean
  permitted: boolean
  occurrence_count: number
  first_seen: string
  last_seen: string
}

/** The React Flow data contract from /graph and /neighbors. */
export interface GraphResponse {
  nodes: AccessNode[]
  edges: AccessEdge[]
  cursor?: string
  has_more: boolean
}

/** Wire drift kinds (modules/access-map/dto.go). */
export type DriftKind = 'unexpected_access' | 'unused_grant' | (string & {})

/** One least-privilege discrepancy. `reconciliation_pending` marks an unexpected
 * access whose permitted-ness cannot yet be decided: the UI shows it AMBER
 * ("pending"), never red — honest uncertainty, not a fabricated violation
 * (UI-CONTRACT-ACCESS-MAP).
 *
 * ⚠ — it is NOT only the agent↔identity link. The engine marks pending for THREE
 * conditions and does not say which: an unresolved agent↔identity link, an unknown grant
 * mode, and an undecidable observed mode (modules/access-map/query.go:216-225, :290-329).
 * This comment named the first as if it were the cause, and so did the legend and the side
 * list; naming one of three is fabricating the other two away. */
export interface DriftEntry {
  kind: DriftKind
  reconciliation_pending?: boolean
  edge: AccessEdge
}

/** The permitted-vs-observed result. `unexpected_*` is the security headline.
 *
 * `truncated` marks a diff reconciled over a PARTIAL drift window (drainDrift's page
 * bound fired). The engine emits it precisely so "a consumer must label a truncated diff
 * as partial, not authoritative" (modules/access-map/query.go:83-87) — so absence of an
 * edge from a truncated response is NOT evidence the edge is gone. `inventory_count`
 * totals permitted-only grants on kinds with no observed-side collector; it is NOT drift.
 * Both were missing from this mirror until which is how a re-check came to read a
 * partial window as a clean one. */
export interface DiffResponse {
  unexpected_accesses: DriftEntry[]
  unused_grants: DriftEntry[]
  unexpected_count: number
  unused_count: number
  inventory_count?: number
  truncated?: boolean
}

/** True for write/readwrite — the risk-bearing modes that get prominence. */
export function isWriteMode(mode: string): boolean {
  return mode === 'readwrite' || mode === 'write'
}

/** Short, stable mode token for the edge chip and legend ("R" / "RW" / "?"). */
export function modeToken(mode: string): 'R' | 'RW' | 'W' | '?' {
  if (mode === 'read') return 'R'
  if (mode === 'readwrite') return 'RW'
  if (mode === 'write') return 'W'
  return '?'
}

// --- attack paths (modules/access-map/attackpath.go) --------------------------

/** Las tres analíticas que el motor calcula sobre el grafo. */
export type AttackPathKind = 'reachability' | 'escalation' | 'exfil'

/** Un salto de la cadena. `node_name`, `mode` y `tool_id` son `omitempty` en Go
 *  (`attackpath.go:39-43`): ausentes y vacíos son los mismos bytes. */
export interface AttackStep {
  node_kind: string
  node_id: string
  node_name?: string
  mode?: string
  tool_id?: string
}

/** Un camino completo.
 *
 *  ⛔ `attribution` y `min_confidence` NO son propiedades del camino: son las del
 *  eslabón MÁS DÉBIL. El motor los compone con `weakestAttribution` y
 *  `weakerConfidence` (`attackpath.go:328-360`), así que una cadena de cinco saltos
 *  con cuatro `firm` y uno `unknown` sale `unknown`. Dibujar el camino sin ellos
 *  convierte una conjetura en un hecho.
 *
 *  ⛔ Y `unknown` es el valor por DEFECTO cuando al borde le falta el metadato
 *  (`:330`): significa «no sé cómo se atribuyó esto», no «está bien».
 *
 *  ⛔ `max_sensitivity` es `omitempty`: ausente NO es «no es sensible». */
export interface AttackPath {
  kind: AttackPathKind
  steps: AttackStep[]
  max_sensitivity?: string
  attribution: string
  min_confidence: string
}

/** El motor emite `map[string]any{"paths": …}` (`attackpath.go:386`), no un struct:
 *  un oráculo de paridad que lea `json:` de structs no ve esta forma. */
export interface AttackPathsResponse {
  paths: AttackPath[]
}

/** El resumen del patrimonio.
 *
 *  ⛔⛔ DOS DE ESTOS SEIS CAMPOS NO SE CALCULAN NUNCA. `out.EscalationPaths` y
 *  `out.ExfilRoutes` tienen CERO asignaciones en `handleAttackPathSummary`
 *  (medido 2026-08-20): las funciones que los computan existen y sólo las usan los
 *  handlers por agente. Salen siempre `0`, y la CLI ya los imprime
 *  (`cmd_accessmap.go:562-563`). Escalado al dueño de `access-map`; hasta que se
 *  arregle, la consola NO puede pintarlos como hechos.
 *
 *  ⚠ Y los otros cuatro son SUELOS: el handler lista con `Limit: 1000` y este DTO no
 *  tiene bandera de truncado, así que con más de mil un total y un tope se ven igual.
 *  `reachable_paths` cuenta además ARISTAS, no caminos (`ReachablePaths = len(edges)`). */
export interface AttackPathSummary {
  total_agents: number
  reachable_paths: number
  escalation_paths: number
  exfil_routes: number
  critical_agents: number
  sensitive_targets: number
}

