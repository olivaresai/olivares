// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { http } from '@/lib/api/client'
import type { ListResponse } from '@/lib/api/types'
import type { LiveDTO, TimelineDTO } from './types'

/**
 * Sessions / live-operation endpoints (module II) — under /v1/m/sessions/,
 * gated by `sessions:live:read`. The web fetches the engine's live snapshots and
 * renders them; it adds no logic (ARCHITECTURE.md). The SSE `/stream` endpoint is consumed
 * via the shared `useLiveStream` hook (bearer auth + tenant pin, audited on open),
 * NOT a path here.
 *
 * Pagination note (contract): the `/live` cursor is IGNORED — a custom most-recent
 * sort means raising `limit` is how you widen the page; `cc_state` then filters that
 * page in-memory. The timeline IS keyset-paginated (cursor + has_more).
 */
export interface LiveListParams {
  cc_state?: string
  workspace_id?: string
  limit?: number
  cursor?: string
}

export interface TimelineParams {
  limit?: number
  cursor?: string
}

export const sessionsApi = {
  /** The most-recent-first page of live + historical sessions. */
  live: (params?: LiveListParams) =>
    http.get<ListResponse<LiveDTO>>('/v1/m/sessions/live', {
      query: { ...params },
    }),
  /** A single session's current snapshot. */
  liveOne: (ref: string) =>
    http.get<LiveDTO>(`/v1/m/sessions/live/${encodeURIComponent(ref)}`),
  /** A session's reconstructible ingest timeline, keyset-paginated. */
  timeline: (ref: string, params?: TimelineParams) =>
    http.get<ListResponse<TimelineDTO>>(
      `/v1/m/sessions/live/${encodeURIComponent(ref)}/timeline`,
      { query: { ...params } },
    ),
}

export const sessionsKeys = {
  all: (tenant: string | null) => ['sessions', tenant] as const,
  live: (tenant: string | null, params?: LiveListParams) =>
    params === undefined
      ? (['sessions', tenant, 'live'] as const)
      : (['sessions', tenant, 'live', params] as const),
  liveOne: (tenant: string | null, ref: string) =>
    ['sessions', tenant, 'live', 'one', ref] as const,
  timeline: (tenant: string | null, ref: string, params?: TimelineParams) =>
    params === undefined
      ? (['sessions', tenant, 'timeline', ref] as const)
      : (['sessions', tenant, 'timeline', ref, params] as const),
}
