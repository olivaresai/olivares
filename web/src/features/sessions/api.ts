// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import {
  http,
  type RequestOptions,
  type TenantRequestOptions,
} from '@/lib/api/client'
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
  /** B2: exact STORE filters — the rows of one profile and/or one external id (the
   * way a card finds the observation rows that share a managed run's profile and
   * id without joining them to the run). */
  provider_profile_ref?: string
  session_ref?: string
  limit?: number
  cursor?: string
}

export interface TimelineParams {
  limit?: number
  cursor?: string
}

/**
 * THE SCOPE A READ IS PINNED TO: the tenant the caller built its cache key for, and
 * the cancellation signal of the query that owns it.
 *
 * It is a WHITELIST (`TenantRequestOptions` + `signal`), for the reason `TenantListOptions`
 * spells out in client.ts: the obvious `Omit<RequestOptions,'tenant'>` would let a caller
 * pass `anonymous: true` in the very literal whose purpose is to make the tenant
 * mandatory. `query` is not in it because a list read here already names its filters in
 * its own typed `params` argument.
 *
 * OPTIONAL on purpose: a caller that passes nothing keeps the previous behaviour exactly
 * (the client falls back to the active tenant at entry, and the read is uncancellable).
 * A caller whose cache key is partitioned by something finer than "whatever is active
 * now" — an authority boundary, say — passes the scope its key was built for, so a
 * request cannot be answered under a context the key never named, and a boundary that
 * moves can abort it.
 */
export type SessionsReadScope = TenantRequestOptions &
  Pick<RequestOptions, 'signal'>

export const sessionsApi = {
  /** The most-recent-first page of live + historical sessions. */
  live: (params?: LiveListParams, scope?: SessionsReadScope) =>
    http.get<ListResponse<LiveDTO>>('/v1/m/sessions/live', {
      // Rebuilt, never spread: the keys this read may carry are named here, so a wide
      // variable that slipped past the type cannot smuggle `anonymous` or a header.
      query: { ...params },
      tenant: scope?.tenant,
      signal: scope?.signal,
    }),
  /** The LEGACY row of a session by its bare external reference. A profile-scoped
   * row is never returned here (it would be "the first" of several homes); read it
   * by its `live_ref`. */
  liveOne: (ref: string) =>
    http.get<LiveDTO>(`/v1/m/sessions/live/${encodeURIComponent(ref)}`),
  /** ONE row by its opaque `live_ref`, whichever channel it was observed through (B2). */
  liveById: (liveRef: string) =>
    http.get<LiveDTO>(
      `/v1/m/sessions/live/by-id/${encodeURIComponent(liveRef)}`,
    ),
  /** The LEGACY timeline of a session by its bare external reference (the events
   * that carry no live_ref), keyset-paginated. */
  timeline: (ref: string, params?: TimelineParams) =>
    http.get<ListResponse<TimelineDTO>>(
      `/v1/m/sessions/live/${encodeURIComponent(ref)}/timeline`,
      { query: { ...params, limit: params?.limit ?? 50 } },
    ),
  /** The timeline of exactly ONE row by its `live_ref` (B2), keyset-paginated. */
  timelineById: (liveRef: string, params?: TimelineParams) =>
    http.get<ListResponse<TimelineDTO>>(
      `/v1/m/sessions/live/by-id/${encodeURIComponent(liveRef)}/timeline`,
      { query: { ...params, limit: params?.limit ?? 50 } },
    ),
}

export const sessionsKeys = {
  all: (tenant: string | null) => ['sessions', tenant] as const,
  live: (tenant: string | null, params?: LiveListParams) =>
    params === undefined
      ? (['sessions', tenant, 'live'] as const)
      : (['sessions', tenant, 'live', params] as const),
  /**
   * THE OBSERVED HALF, PARTITIONED BY AUTHORITY BOUNDARY — the same shape the provider
   * profile plane already uses (`agentOpsKeys.boundaryScope`, api.ts): `epoch` is the
   * OPAQUE number `useAuthBoundary` derives for one (principal, tenant, credential
   * generation), never a user id, a session id or a token.
   *
   * It exists because the tenant alone does not separate two principals, and a
   * credential RENEWAL keeps the session id: a key made of the tenant only would hand a
   * new principal the rows the previous one read. With the scope in the key, the new
   * boundary starts with nothing to paint until its OWN answer arrives, and the scope is
   * a prefix of every key under it, so cancelling and removing the boundary that left is
   * one call. `all(t)` still reaches everything; `live(t, …)` above is untouched, and so
   * are its callers.
   */
  boundaryScope: (tenant: string | null, epoch: number) =>
    ['sessions', tenant, 'b', epoch] as const,
  liveScoped: (
    tenant: string | null,
    epoch: number,
    params?: LiveListParams,
  ) =>
    params === undefined
      ? (['sessions', tenant, 'b', epoch, 'live'] as const)
      : (['sessions', tenant, 'b', epoch, 'live', params] as const),
  liveOne: (tenant: string | null, ref: string) =>
    ['sessions', tenant, 'live', 'one', ref] as const,
  /** Keyed by the row's own id: two rows sharing an external id never share a cache entry. */
  liveById: (tenant: string | null, liveRef: string) =>
    ['sessions', tenant, 'live', 'by-id', liveRef] as const,
  timeline: (tenant: string | null, ref: string, params?: TimelineParams) =>
    params === undefined
      ? (['sessions', tenant, 'timeline', ref] as const)
      : (['sessions', tenant, 'timeline', ref, params] as const),
  timelineById: (
    tenant: string | null,
    liveRef: string,
    params?: TimelineParams,
  ) =>
    params === undefined
      ? (['sessions', tenant, 'timeline', 'by-id', liveRef] as const)
      : (['sessions', tenant, 'timeline', 'by-id', liveRef, params] as const),
}
