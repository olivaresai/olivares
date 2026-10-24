// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Wire shapes for the Reach view (issue #2553) — the fields the reach projection
// consumes from the engine's EXISTING endpoints (no new mechanism): the access-map
// graph, the agents list, the live-sessions page and the pending approval queue. Every ref arrives already
// redacted from the engine; the web never parses a ref into a path.

/** One access-map edge (the subset of modules/access-map/dto.go the reach view uses). */
export interface ReachEdge {
  id: string
  /** agent | session | identity — agents are tools; a session counts under its tool. */
  origin_kind?: string
  /** The engine's resolved agent id (edgeDTO.OriginID, always present). The canonical
   * join key: an agent's external_id is settable and NOT unique, so joining on it
   * would let a forged external_id claim another agent's reach. */
  origin_id?: string
  /** The origin's already-redacted ref — the fallback join key when origin_id is absent. */
  origin_ref?: string
  /** resource | mcp_server | provider | mcp.tool | … (sourcescope scopeableKindFor + observed kinds). */
  resource_kind?: string
  resource_ref?: string
}

/** The /v1/m/accessmap/graph response, reduced to what the reach view reads. */
export interface ReachGraphResponse {
  edges: ReachEdge[]
  has_more: boolean
}

/** One agent (GET /v1/agents) — the name behind an access-map tool origin. */
export interface ReachTool {
  /** The canonical agent id (what edge origin_id carries). */
  id: string
  name: string
  kind: string
  external_id?: string
}

/** One approval row (GET /v1/m/governance/approvals) — the waiting destinations. */
export interface ReachApproval {
  id: string
  action?: string
  /** mcp_server | workspace | deployment | audit | … — only the reach families count. */
  subject_kind?: string
  subject_ref?: string
  /** The session that asked, when the engine names one: the request's tool. */
  session_ref?: string
  status: string
}

/** One live-sessions row (GET /v1/m/sessions/live) — the tool behind a session. */
export interface ReachSession {
  session_ref: string
  live_ref?: string
  /** The session's driver ("claude", "codex"); absent when no connector declared one. */
  engine?: string
}
