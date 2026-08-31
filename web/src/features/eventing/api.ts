// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Eventing module endpoint wrappers + query keys. The one-time `secret` on
// creation is shown in a modal and never persisted client-side; subsequent reads
// carry `secret_hint` only.
import { http } from '@/lib/api/client'
import type { ListResponse } from '@/lib/api/types'
import type {
  RevisionListParams,
  RevisionsResponse,
} from '@/features/shared/revisions-sheet'
import type {
  CreatedSubscription,
  EgressCompatReport,
  EgressPolicyStatus,
  Delivery,
  EventsResponse,
  EventTypeCatalog,
  ReplayInput,
  ReplayResult,
  RotateAuthInput,
  RotateSecretResult,
  Subscription,
  SubscriptionInput,
  TestResult,
} from './types'

const BASE = '/v1/m/eventing'

export const eventingApi = {
  // --- event type catalog ---
  eventTypes: () => http.get<EventTypeCatalog>(`${BASE}/event-types`),

  // --- subscriptions ---
  subscriptions: (query?: { enabled?: boolean }) =>
    http.get<ListResponse<Subscription>>(`${BASE}/subscriptions`, { query }),
  subscription: (id: string) =>
    http.get<Subscription>(`${BASE}/subscriptions/${id}`),
  createSubscription: (body: SubscriptionInput) =>
    http.post<CreatedSubscription>(`${BASE}/subscriptions`, body),
  updateSubscription: (id: string, body: SubscriptionInput) =>
    http.put<Subscription>(`${BASE}/subscriptions/${id}`, body),
  deleteSubscription: (id: string) =>
    http.delete<void>(`${BASE}/subscriptions/${id}`),
  rotateSecret: (id: string) =>
    http.post<RotateSecretResult>(`${BASE}/subscriptions/${id}/rotate-secret`),
  // The endpoint REQUIRES the new cleartext credential in the body (the engine
  // never generates one for the caller's downstream); it returns the updated
  // subscription with the new auth_value_hint — the cleartext is never echoed.
  rotateAuth: (id: string, body: RotateAuthInput) =>
    http.post<Subscription>(`${BASE}/subscriptions/${id}/rotate-auth`, body),
  testSubscription: (id: string) =>
    http.post<TestResult>(`${BASE}/subscriptions/${id}/test`),
  replayEvents: (id: string, body: ReplayInput) =>
    http.post<ReplayResult>(`${BASE}/subscriptions/${id}/replay`, body),
  subscriptionRevisions: (id: string, query?: RevisionListParams) =>
    http.get<RevisionsResponse<Subscription>>(
      `${BASE}/subscriptions/${id}/revisions`,
      { query: query ? { ...query } : undefined },
    ),
  restoreSubscription: (id: string, revisionId: string) =>
    http.post<Subscription>(`${BASE}/subscriptions/${id}/restore`, {
      revision_id: revisionId,
    }),

  // --- the egress destination control ---
  //
  // ⛔ SÓLO LAS DOS LECTURAS. La palanca —actuar la transición de modo— es una ceremonia
  // de CLI a propósito: es un acto de operador de plataforma sobre un control que no está
  // acotado por tenant, y `cmd/olivares/cmd_eventing_egress.go:28-34` razona que una
  // palanca alcanzable por HTTP habría que defenderla contra todo camino que llegue a
  // HTTP. Ese razonamiento no se toca aquí; lo que se corrige es su premisa, que decía
  // que la consola ya enseñaba estas dos lecturas y no las enseñaba.
  egressPolicyStatus: () =>
    http.get<EgressPolicyStatus>(`${BASE}/egress-policy`),
  // ADMIN tier: nombra hosts. La consola la pide sólo si el llamante tiene ese permiso,
  // y si aun así el motor la deniega, se dice — no se pinta un informe vacío.
  egressCompatReport: () =>
    http.get<EgressCompatReport>(`${BASE}/egress-policy/compat`),

  // --- events ---
  events: (query?: { since_seq?: number; type?: string; limit?: number }) =>
    http.get<EventsResponse>(`${BASE}/events`, { query }),

  // --- deliveries ---
  deliveries: (query?: {
    subscription?: string
    status?: string
    event_type?: string
    origin?: 'live' | 'replay'
    cursor?: string
    limit?: number
  }) => http.get<ListResponse<Delivery>>(`${BASE}/deliveries`, { query }),
  deadLetters: (query?: {
    subscription?: string
    event_type?: string
    cursor?: string
    limit?: number
  }) => http.get<ListResponse<Delivery>>(`${BASE}/dead-letters`, { query }),
  redeliver: (id: string) =>
    http.post<void>(`${BASE}/deliveries/${id}/redeliver`),
}

export const eventingKeys = {
  all: (tenant: string | null) => ['eventing', tenant] as const,
  eventTypes: (tenant: string | null) =>
    ['eventing', tenant, 'event-types'] as const,
  subscriptions: (tenant: string | null) =>
    ['eventing', tenant, 'subscriptions'] as const,
  subscription: (tenant: string | null, id: string) =>
    ['eventing', tenant, 'subscriptions', id] as const,
  subscriptionRevisions: (tenant: string | null, id: string) =>
    ['eventing', tenant, 'subscriptions', id, 'revisions'] as const,
  events: (tenant: string | null) => ['eventing', tenant, 'events'] as const,
  deliveries: (tenant: string | null) =>
    ['eventing', tenant, 'deliveries'] as const,
  deadLetters: (tenant: string | null) =>
    ['eventing', tenant, 'dead-letters'] as const,
  egressPolicy: (tenant: string | null) =>
    ['eventing', tenant, 'egress-policy'] as const,
  egressCompat: (tenant: string | null) =>
    ['eventing', tenant, 'egress-policy', 'compat'] as const,
}
