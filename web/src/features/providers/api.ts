// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { http } from '@/lib/api/client'
import type { ListResponse } from '@/lib/api/types'
import type {
  CreateProviderRequest,
  PatchProviderRequest,
  ProviderRecordDTO,
} from './types'

/**
 * The provider plane, under /v1/m/sessions/providers, gated by
 * `sessions:provider:{read,write,admin}`.
 *
 * `test` is a WRITE and not a read, and the tier says so: it uses the credential
 * with the provider, it can be rate-limited by them, and it changes the row it
 * reports on. Calling it a read would have let a read-only principal spend the
 * tenant's standing with a provider.
 */
const PROVIDERS = '/v1/m/sessions/providers'

const ref = (r: string) => encodeURIComponent(r)

/** One page. A screen that shows a page says so; the client never asks the engine
 * for "everything". */
export const PROVIDER_PAGE = 100

export interface ProviderListParams {
  state?: string
  kind?: string
  limit?: number
  cursor?: string
}

export const providersApi = {
  list: (params?: ProviderListParams, opts?: { signal?: AbortSignal }) =>
    http.get<ListResponse<ProviderRecordDTO>>(PROVIDERS, {
      query: { limit: PROVIDER_PAGE, ...params },
      signal: opts?.signal,
    }),
  get: (r: string, opts?: { signal?: AbortSignal }) =>
    http.get<ProviderRecordDTO>(`${PROVIDERS}/${ref(r)}`, {
      signal: opts?.signal,
    }),
  create: (body: CreateProviderRequest) =>
    http.post<ProviderRecordDTO>(PROVIDERS, body),
  patch: (r: string, body: PatchProviderRequest) =>
    http.patch<ProviderRecordDTO>(`${PROVIDERS}/${ref(r)}`, body),
  /** The non-spending connection test: one model-list call, never a completion. */
  test: (r: string) =>
    http.post<ProviderRecordDTO>(`${PROVIDERS}/${ref(r)}/test`),
  /** Irreversible for that reference. */
  revoke: (r: string) =>
    http.post<ProviderRecordDTO>(`${PROVIDERS}/${ref(r)}/revoke`),
}

/**
 * Tenant-scoped query keys. The provider plane is partitioned by the AUTHORITY
 * BOUNDARY like the profile plane beside it: `epoch` is the opaque number
 * useAuthBoundary derives for one (principal, tenant, credential), so two
 * principals reading the same tenant hold different cache entries and a remount
 * under a new boundary starts with nothing to paint.
 */
export const providerKeys = {
  all: (tenant: string | null) => ['providers', tenant] as const,
  boundaryScope: (tenant: string | null, epoch: number) =>
    ['providers', tenant, 'b', epoch] as const,
  list: (tenant: string | null, epoch: number, params?: ProviderListParams) =>
    params === undefined
      ? (['providers', tenant, 'b', epoch, 'list'] as const)
      : (['providers', tenant, 'b', epoch, 'list', params] as const),
  one: (tenant: string | null, epoch: number, r: string) =>
    ['providers', tenant, 'b', epoch, 'one', r] as const,
}
