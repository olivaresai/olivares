// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Endpoint helpers + query keys for the Reach view (issue #2553). Thin wrappers
// over the core HTTP client against the engine's EXISTING reads — no new
// mechanism, no logic here (ARCHITECTURE.md):
// - /v1/m/accessmap/graph — the edges that ARE the reach (accessmap:graph:read,
//   privileged self-audited read).
// - /v1/agents — tool names by canonical agent id (the graph's origin_id).
// - /v1/m/sessions/live — the tool a session runs, on the page and key the
//   session names already read (shared/entity-names), so no second request.
// - /v1/m/governance/approvals?status=pending — the waiting destinations.
import { http } from '@/lib/api/client'
import type { ListResponse } from '@/lib/api/types'
import { sessionsApi, sessionsKeys } from '@/features/sessions/api'
import { SESSION_PAGE } from '@/features/shared/entity-names'
import type { ReachApproval, ReachGraphResponse, ReachTool } from './types'

/** One page of each list read for the reach projection: larger than the demo
 * estate and any early customer, and every page cut is LABELED (has_more)
 * instead of being presented as the whole. */
const REACH_LIST_CEILING = 100

export const reachApi = {
  graph: () => http.get<ReachGraphResponse>('/v1/m/accessmap/graph'),
  agents: () =>
    http.get<ListResponse<ReachTool>>('/v1/agents', {
      query: { limit: REACH_LIST_CEILING },
    }),
  sessions: () => sessionsApi.live({ limit: SESSION_PAGE }),
  pendingApprovals: () =>
    http.get<ListResponse<ReachApproval>>('/v1/m/governance/approvals', {
      query: { status: 'pending', limit: REACH_LIST_CEILING },
    }),
}

export const reachKeys = {
  graph: (tenant: string | null) => ['reach', tenant, 'graph'] as const,
  agents: (tenant: string | null) => ['reach', tenant, 'agents'] as const,
  approvals: (tenant: string | null) => ['reach', tenant, 'approvals'] as const,
  sessions: (tenant: string | null) =>
    sessionsKeys.live(tenant, { limit: SESSION_PAGE }),
}
