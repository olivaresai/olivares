// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import {
  http,
  type RequestOptions,
  type TenantRequestOptions,
} from '@/lib/api/client'
import { ApiError } from '@/lib/api/errors'
import type { ListResponse } from '@/lib/api/types'
import type { paths } from '@/lib/api/openapi.gen'
import type { CatalogEntry, EntityDetail, InventorySummary } from './types'

/**
 * Inventory endpoints (module I) — under /v1/m/inventory/, gated by
 * `inventory:catalog:read`. The web fetches the engine's catalog and renders it;
 * it adds no logic (ARCHITECTURE.md). (The dedicated /topology endpoint was retired on
 * 2026-06-03 — decision A: the access graph is owned by module III.)
 *
 * SCOPE OF EVERY READ HERE: THE TENANT, NEVER A WORKSPACE. Summary, entities,
 * detail and observation history all require `inventory:catalog:read` on the
 * tenant; `inventory.catalog_entry` carries no core-workspace column and declares
 * no `WorkspaceLineage` (modules/inventory/schema.go:67). A `workspace_id` on the
 * query string is IGNORED by the engine: it neither narrows the answer nor proves
 * access to the workspace it names. A principal whose membership is confined to one
 * workspace is refused before the read (403 "workspace confined") — a refusal the
 * views render as such, never as an empty or zero estate. Measured and ratified in
 * `assessments/product/inventory-effective-workspace-scope/REPORT.md` (2026-09-08).
 *
 * Consequently no consumer of these endpoints — the Inventory views, the Home
 * overview, the Executive dashboard, or the C3 history — sends a `workspace_id`,
 * and every query is keyed by tenant only. Anything else would re-fetch on a
 * selector change and pretend a different set came back.
 *
 * Detail and history reconstruct `{tenant, signal}` (and history's closed query)
 * instead of spreading a wide options object, so `anonymous`, extra headers or a
 * caller query cannot ride through a TypeScript-shaped variable.
 */
export interface EntityListParams {
  kind?: string
  status?: string
  limit?: number
  cursor?: string
}

/** Tenant-bound authenticated read: name the tenant and, when the caller has one,
 *  the abort signal. Not a relaxation of RequestOptions. */
export type InventoryReadOptions = TenantRequestOptions &
  Pick<RequestOptions, 'signal'>

export type InventoryObservationOptions = InventoryReadOptions & {
  cursor?: string
}

/** Closed 200 page of GET .../entities/{kind}/{id}/observations. */
export type ObservationPage =
  paths['/v1/m/inventory/entities/{kind}/{id}/observations']['get']['responses'][200]['content']['application/json']

export type ObservationItem = ObservationPage['items'][number]

/** Fixed C3 page size. The UI does not expose another limit. */
export const OBSERVATION_PAGE_LIMIT = 25

function observationPath(kind: string, id: string): string {
  return `/v1/m/inventory/entities/${encodeURIComponent(kind)}/${encodeURIComponent(id)}/observations`
}

function usableCursor(value: unknown): string | undefined {
  return typeof value === 'string' && value !== '' ? value : undefined
}

/**
 * Local page guard. The shared client rejects a present non-array `items` and
 * nothing else; a missing `items`, a non-boolean `has_more`, `has_more` without
 * a usable cursor, or a continuation cursor that did not advance must not become
 * an empty history or an endless first page.
 */
function assertObservationPage(
  page: unknown,
  requestCursor?: string,
): ObservationPage {
  if (page === null || typeof page !== 'object' || Array.isArray(page)) {
    throw new ApiError(
      200,
      'invalid_response',
      'The observation page is not an object.',
    )
  }
  const body = page as Record<string, unknown>
  if (!('items' in body) || !Array.isArray(body.items)) {
    throw new ApiError(
      200,
      'invalid_response',
      'The observation page is missing items.',
    )
  }
  if (body.has_more !== true && body.has_more !== false) {
    throw new ApiError(
      200,
      'invalid_response',
      'The observation page is missing a boolean has_more.',
    )
  }
  if (body.has_more === true) {
    const next = usableCursor(body.cursor)
    if (next === undefined) {
      throw new ApiError(
        200,
        'invalid_response',
        'The observation page has more receipts but no usable cursor.',
      )
    }
    if (
      requestCursor !== undefined &&
      requestCursor !== '' &&
      next === requestCursor
    ) {
      throw new ApiError(
        200,
        'invalid_response',
        'The observation page cursor did not advance.',
      )
    }
  }
  return page as ObservationPage
}

export const inventoryApi = {
  /** The estate summary takes NO options: the route reads no request filter
   *  (modules/inventory/api.go:123). The optional `workspace_id` this wrapper used
   *  to accept for Home and Executive was removed on 2026-09-08 once those two
   *  consumers stopped sending it — a parameter the engine discards is not a
   *  contract, and keeping it typed here invited the next caller to pretend a
   *  scope. A scoped answer needs a scoped endpoint, not this one. */
  summary: () => http.get<InventorySummary>('/v1/m/inventory/summary'),
  entities: (params?: EntityListParams) =>
    http.get<ListResponse<CatalogEntry>>('/v1/m/inventory/entities', {
      query: { ...params },
    }),
  detail: (kind: string, id: string, opts: InventoryReadOptions) =>
    http.get<EntityDetail>(
      `/v1/m/inventory/entities/${encodeURIComponent(kind)}/${encodeURIComponent(id)}`,
      { tenant: opts.tenant, signal: opts.signal },
    ),
  observations: (
    kind: string,
    id: string,
    opts: InventoryObservationOptions,
  ) => {
    const query: { limit: number; cursor?: string } = {
      limit: OBSERVATION_PAGE_LIMIT,
    }
    const cursor = usableCursor(opts.cursor)
    if (cursor !== undefined) query.cursor = cursor
    return http
      .get<ObservationPage>(observationPath(kind, id), {
        tenant: opts.tenant,
        signal: opts.signal,
        query,
      })
      .then((page) => assertObservationPage(page, cursor))
  },
}

/** Query keys: tenant-scoped, on purpose without a workspace segment — the
 *  catalog is one set per tenant, so one cache entry per tenant is the truth. */
export const inventoryKeys = {
  all: (tenant: string | null) => ['inventory', tenant] as const,
  summary: (tenant: string | null) => ['inventory', tenant, 'summary'] as const,
  entities: (tenant: string | null, params?: EntityListParams) =>
    params === undefined
      ? (['inventory', tenant, 'entities'] as const)
      : (['inventory', tenant, 'entities', params] as const),
  detail: (tenant: string | null, kind: string, id: string) =>
    ['inventory', tenant, 'detail', kind, id] as const,
  observations: (tenant: string | null, kind: string, id: string) =>
    [
      'inventory',
      tenant,
      'observations',
      kind,
      id,
      { limit: OBSERVATION_PAGE_LIMIT },
    ] as const,
}
