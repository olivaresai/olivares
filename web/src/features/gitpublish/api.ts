// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Governed Git publication endpoint wrappers + query keys: thin typed `http.*` calls against
// the thirteen `/v1/m/gitpublish/…` routes (modules/gitpublish/routes.go). Every write takes
// the tenant the operator acted in, so a tenant switch between the click and the dispatch
// cannot move a publication to another tenant.
//
// ⛔ The refusal body of this module is FLAT — `{"error": "<code>", "intent_id": "…"}` — not
//    the core envelope, so ApiError.code reads `internal` for it. model.ts reads the code from
//    ApiError.body; nothing here re-parses messages.
import { http, type TenantRequestOptions } from '@/lib/api/client'
import type {
  CreateTargetInput,
  ItemsResponse,
  MergeInput,
  PublicationIntent,
  PublicationObservation,
  PublicationTarget,
  PullRequestInput,
  PushInput,
  UpdateTargetInput,
} from './types'

const BASE = '/v1/m/gitpublish'
const targetPath = (id: string) => `${BASE}/targets/${encodeURIComponent(id)}`
const intentPath = (id: string) => `${BASE}/intents/${encodeURIComponent(id)}`

export const gitpublishApi = {
  targets: () => http.get<ItemsResponse<PublicationTarget>>(`${BASE}/targets`),
  target: (id: string) => http.get<PublicationTarget>(targetPath(id)),
  createTarget: (body: CreateTargetInput, request: TenantRequestOptions) =>
    http.post<PublicationTarget>(`${BASE}/targets`, body, request),
  updateTarget: (
    id: string,
    body: UpdateTargetInput,
    request: TenantRequestOptions,
  ) => http.put<PublicationTarget>(targetPath(id), body, request),
  deleteTarget: (id: string, request: TenantRequestOptions) =>
    http.delete<void>(targetPath(id), undefined, request),

  // The three effects answer the intent with its receipt: 200 settled, 202 uncertain or
  // still dispatching, 409 rejected (the body is then the intent, not a refusal).
  push: (targetId: string, body: PushInput, request: TenantRequestOptions) =>
    http.post<PublicationIntent>(
      `${targetPath(targetId)}/pushes`,
      body,
      request,
    ),
  openPullRequest: (
    targetId: string,
    body: PullRequestInput,
    request: TenantRequestOptions,
  ) =>
    http.post<PublicationIntent>(
      `${targetPath(targetId)}/pull-requests`,
      body,
      request,
    ),
  merge: (targetId: string, body: MergeInput, request: TenantRequestOptions) =>
    http.post<PublicationIntent>(
      `${targetPath(targetId)}/merges`,
      body,
      request,
    ),

  intents: (targetId: string) =>
    http.get<ItemsResponse<PublicationIntent>>(`${BASE}/intents`, {
      query: { target_id: targetId },
    }),
  intent: (id: string) => http.get<PublicationIntent>(intentPath(id)),
  observations: (id: string) =>
    http.get<ItemsResponse<PublicationObservation>>(
      `${intentPath(id)}/observations`,
    ),
  // Reconcile reads the host again and never dispatches; it takes no body.
  reconcile: (id: string, request: TenantRequestOptions) =>
    http.post<PublicationIntent>(
      `${intentPath(id)}/reconcile`,
      undefined,
      request,
    ),
  abandon: (id: string, reason: string, request: TenantRequestOptions) =>
    http.post<PublicationIntent>(
      `${intentPath(id)}/abandon`,
      { reason },
      request,
    ),
}

export const gitpublishKeys = {
  all: (tenant: string | null) => ['gitpublish', tenant] as const,
  targets: (tenant: string | null) =>
    ['gitpublish', tenant, 'targets'] as const,
  target: (tenant: string | null, id: string) =>
    ['gitpublish', tenant, 'targets', id] as const,
  intents: (tenant: string | null, targetId: string) =>
    ['gitpublish', tenant, 'targets', targetId, 'intents'] as const,
  intent: (tenant: string | null, id: string) =>
    ['gitpublish', tenant, 'intents', id] as const,
  observations: (tenant: string | null, id: string) =>
    ['gitpublish', tenant, 'intents', id, 'observations'] as const,
}
