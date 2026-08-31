// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Alerting / notify administration. Tenant-scoped module routes under
// /v1/m/notify/ (modules/notify/api.go APIRoutes):
//   GET/POST   /routes            — routing rules (event → destination)
//   GET/PUT/DELETE /routes/{id}   — one route (delete = admin tier)
//   POST       /routes/{id}/test  — fire a synthetic delivery to the destination
//   GET        /destinations      — the provisioned destination NAMES (never a secret)
//   GET        /deliveries        — the delivery log (detective)
//   GET        /outbox            — the durable outbox; ?status=dead is the DLQ
//   POST       /outbox/{id}/redeliver — requeue ONE terminal row (admin tier, audited)
// A route references a destination by NAME only; the credential lives in the wired
// transport/connector (secret by reference), so this surface never sees or sets one.
//
// WHAT THESE TWO LINES USED TO SAY, because the correction is the point. They
// read "There is no per-delivery redeliver in notify's API — the delivery log is
// read-only and the console does not fabricate a retry". That was FALSE against
// modules/notify/api.go:47, which has mounted POST /outbox/{id}/redeliver since
// and modules/notify/outbox.go:159 dead-letters a row expressly "so it surfaces in the
// DLQ for the operator". A missing screen is a gap; a client that ASSERTS the route
// does not exist is a false claim about the product, and the next session to open this
// file believes it. The redeliver seam is the OUTBOX row, not the ledger row: the
// ledger (GET /deliveries) is genuinely append-only and still has no retry.
import { http } from '@/lib/api/client'
import type { ListResponse } from '@/lib/api/types'
import type {
  RevisionListParams,
  RevisionsResponse,
} from '@/features/shared/revisions-sheet'

const BASE = '/v1/m/notify'

/** Minimum severity that a route matches: '' (any) | info | low | medium | high | critical. */
export type NotifySeverity =
  '' | 'info' | 'low' | 'medium' | 'high' | 'critical'

/** A routing rule (notify routeDTO). Destination is a provisioned NAME, not a URL. */
export interface NotifyRoute {
  id?: string
  name: string
  enabled?: boolean
  match_types?: string[]
  match_kinds?: string[]
  min_severity?: string
  match_sources?: string[]
  match_subject_kinds?: string[]
  destination: string
  dedup_window_seconds?: number
  throttle_window_seconds?: number
  priority?: number
  owner_actor?: string
  created_at?: string
}

/** One recorded delivery attempt (notify deliveryDTO). */
export interface NotifyDelivery {
  id: string
  route_ref?: string
  destination: string
  event_type: string
  finding_kind: string
  severity?: string
  subject_kind?: string
  subject_ref?: string
  title?: string
  dedup_key?: string
  status: string
  detail?: string
  occurred_at: string
}

/** The result of firing a route test (notify testResult). */
export interface NotifyTestResult {
  destination: string
  status: string
  detail?: string
}

export interface NotifyMatchType {
  type: string
  description: string
}

export interface NotifyEvaluateInput {
  event_type: string
  kind: string
  severity: Exclude<NotifySeverity, ''>
  source: string
  subject_kind: string
}

export interface NotifyRouteVerdict {
  id: string
  name: string
  enabled: boolean
  matched: boolean
  mismatches: string[]
}

export interface NotifyEvaluateResult {
  matched_count: number
  items: NotifyRouteVerdict[]
}

/**
 * One durable-outbox row — a MIRROR 1:1 of notify's outboxDTO
 * (modules/notify/outbox_api.go:19-34): same field set, same json names, and every
 * `omitempty` Go field optional here. It is the delivery STATE MACHINE, which is what
 * makes it different from NotifyDelivery: the ledger is an append-only record of
 * ATTEMPTS, while the outbox holds ONE row per notification and moves it through
 * queued → delivering → delivered | dead.
 *
 * A delivered row STAYS in the outbox (modules/notify/outbox.go updates the row rather
 * than removing it), which is why this screen can filter for it and why the engine
 * accepts it for requeue. An earlier version of this comment called the outbox "what is
 * still owed" — that quietly defined a whole state out of existence, on a screen that
 * lists it.
 *
 * It deliberately never carries the rendered notification body (minimal-data; the title
 * summarizes it).
 */
export interface NotifyOutboxEntry {
  id: string
  /** queued | delivering | delivered | dead (modules/notify/outbox.go). `dead` is the
   * DLQ: retries were exhausted or the outcome is permanent. */
  status: string
  attempts: number
  destination: string
  event_type: string
  finding_kind: string
  severity?: string
  subject_ref?: string
  title?: string
  last_detail?: string
  next_attempt_at?: string
  last_attempt_at?: string
  occurred_at: string
  route_ref?: string
}

/**
 * What POST /outbox/{id}/redeliver answers on success (modules/notify/outbox_api.go:141).
 * `status` is "queued" — the row was RE-QUEUED, and the next pump makes the attempt.
 * A 200 here is NOT a delivered notification, and the console must not say it is.
 */
export interface NotifyRedeliverResult {
  id: string
  status: string
}

/** The four statuses an outbox row can hold (modules/notify/outbox.go). */
export const OUTBOX_STATUSES = [
  'dead',
  'queued',
  'delivering',
  'delivered',
] as const

/**
 * A row may be requeued only from a TERMINAL status. The engine enforces it and
 * answers 409 otherwise (modules/notify/outbox_api.go:111-117,137-139): a
 * queued/delivering row is in flight and a requeue would race the owner's outcome
 * write. Mirrored here so the console does not offer an action the engine refuses —
 * the engine stays the authority, and the 409 path is still handled for the race.
 */
export function isRedeliverable(status: string): boolean {
  return status === 'dead' || status === 'delivered'
}

export interface RouteListParams {
  destination?: string
  enabled?: string
}
export interface OutboxListParams {
  /** One of OUTBOX_STATUSES; `dead` is the dead-letter queue. */
  status?: string
  destination?: string
  /** Keyset pagination (notify listQuery): pass the previous page's cursor. */
  cursor?: string
  limit?: number
}
export interface DeliveryListParams {
  status?: string
  finding_kind?: string
  destination?: string
  route?: string
  /** Keyset pagination (notify listQuery): pass the previous page's cursor. */
  cursor?: string
  limit?: number
}

export const notifyApi = {
  listRoutes: (params?: RouteListParams) =>
    http.get<ListResponse<NotifyRoute>>(`${BASE}/routes`, {
      query: params ? { ...params } : undefined,
    }),
  getRoute: (id: string) =>
    http.get<NotifyRoute>(`${BASE}/routes/${encodeURIComponent(id)}`),
  createRoute: (body: NotifyRoute) =>
    http.post<NotifyRoute>(`${BASE}/routes`, body),
  updateRoute: (id: string, body: NotifyRoute) =>
    http.put<NotifyRoute>(`${BASE}/routes/${encodeURIComponent(id)}`, body),
  deleteRoute: (id: string) =>
    http.delete<void>(`${BASE}/routes/${encodeURIComponent(id)}`),
  testRoute: (id: string) =>
    http.post<NotifyTestResult>(
      `${BASE}/routes/${encodeURIComponent(id)}/test`,
    ),
  routeRevisions: (id: string, params?: RevisionListParams) =>
    http.get<RevisionsResponse<NotifyRoute>>(
      `${BASE}/routes/${encodeURIComponent(id)}/revisions`,
      { query: params ? { ...params } : undefined },
    ),
  restoreRoute: (id: string, revisionId: string) =>
    http.post<NotifyRoute>(`${BASE}/routes/${encodeURIComponent(id)}/restore`, {
      revision_id: revisionId,
    }),
  listDestinations: () =>
    http.get<{ destinations: string[] }>(`${BASE}/destinations`),
  listMatchTypes: () =>
    http.get<{ match_types: NotifyMatchType[] }>(`${BASE}/match-types`),
  evaluateRoutes: (body: NotifyEvaluateInput) =>
    http.post<NotifyEvaluateResult>(`${BASE}/routes/evaluate`, body),
  listDeliveries: (params?: DeliveryListParams) =>
    http.get<ListResponse<NotifyDelivery>>(`${BASE}/deliveries`, {
      query: params ? { ...params } : undefined,
    }),
  listOutbox: (params?: OutboxListParams) =>
    http.get<ListResponse<NotifyOutboxEntry>>(`${BASE}/outbox`, {
      query: params ? { ...params } : undefined,
    }),
  /**
   * Requeue one outbox row. NO REQUEST BODY, and that is measured, not stylistic:
   * handleRedeliverOutbox reads the id from the path and nothing else
   * (modules/notify/outbox_api.go:93-98) — it never decodes a body, requires no
   * `Idempotency-Key` and takes no `expected_version`. The engine asks for an
   * Idempotency-Key only on the work-kernel routes
   * (core/api/openapi_modules.go:277-290, gated on sessionsWorkRoute), not here.
   *
   * Passing no body is also what keeps this off the double-encoding rake: http.post
   * runs its `body` through JSON.stringify (lib/api/client.ts:139), so a STRING body
   * would arrive re-quoted. Omitted body ⇒ no body and no Content-Type at all
   * (client.ts:120-121,135-140), which is exactly the request the engine parses.
   */
  redeliverOutbox: (id: string) =>
    http.post<NotifyRedeliverResult>(
      `${BASE}/outbox/${encodeURIComponent(id)}/redeliver`,
    ),
}

export const notifyKeys = {
  routes: (t: string | null, params?: unknown) =>
    params === undefined
      ? (['notify', t, 'routes'] as const)
      : (['notify', t, 'routes', params] as const),
  route: (t: string | null, id: string) => ['notify', t, 'routes', id] as const,
  routeRevisions: (t: string | null, id: string) =>
    ['notify', t, 'routes', id, 'revisions'] as const,
  destinations: (t: string | null) => ['notify', t, 'destinations'] as const,
  matchTypes: (t: string | null) => ['notify', t, 'match-types'] as const,
  deliveries: (t: string | null, params?: unknown) =>
    params === undefined
      ? (['notify', t, 'deliveries'] as const)
      : (['notify', t, 'deliveries', params] as const),
  outbox: (t: string | null, params?: unknown) =>
    params === undefined
      ? (['notify', t, 'outbox'] as const)
      : (['notify', t, 'outbox', params] as const),
  /** The PREFIX of every outbox query, whatever its filter. Invalidating the
   * filtered key would refresh only the filter that happened to be selected, and a
   * requeue moves the row OUT of the `dead` list and INTO `queued` — two lists whose
   * keys differ by their params element, which is not a prefix of the other. */
  outboxAll: (t: string | null) => ['notify', t, 'outbox'] as const,
}
