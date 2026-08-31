// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { http } from '@/lib/api/client'
import type { ListResponse } from '@/lib/api/types'
import type { CatalogEntry, EntityDetail, InventorySummary } from './types'

/**
 * Inventory endpoints (module I) — under /v1/m/inventory/, gated by
 * `inventory:catalog:read`. The web fetches the engine's catalog and renders it;
 * it adds no logic (ARCHITECTURE.md). (The dedicated /topology endpoint was retired on
 * 2026-06-03 — decision A: the access graph is owned by module III.)
 */
export interface EntityListParams {
  kind?: string
  status?: string
  workspace_id?: string
  limit?: number
  cursor?: string
}

export const inventoryApi = {
  summary: (opts?: { workspace_id?: string }) =>
    http.get<InventorySummary>('/v1/m/inventory/summary', {
      query: opts,
    }),
  entities: (params?: EntityListParams) =>
    http.get<ListResponse<CatalogEntry>>('/v1/m/inventory/entities', {
      query: { ...params },
    }),
  detail: (kind: string, id: string) =>
    http.get<EntityDetail>(
      `/v1/m/inventory/entities/${encodeURIComponent(kind)}/${encodeURIComponent(id)}`,
    ),
}

export const inventoryKeys = {
  all: (tenant: string | null) => ['inventory', tenant] as const,
  summary: (tenant: string | null) => ['inventory', tenant, 'summary'] as const,
  entities: (tenant: string | null, params?: EntityListParams) =>
    params === undefined
      ? (['inventory', tenant, 'entities'] as const)
      : (['inventory', tenant, 'entities', params] as const),
  detail: (tenant: string | null, kind: string, id: string) =>
    ['inventory', tenant, 'detail', kind, id] as const,
}
