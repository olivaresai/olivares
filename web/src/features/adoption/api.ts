// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Adoption (gap #12) endpoint wrappers + query keys. Thin `http.*` calls against the
// engine's `/v1/m/adoption/…` routes (the web presents, never recomputes). Tenant-scoped
// keys include the active tenant so switching org refetches cleanly. Reads are RBAC-gated
// server-side (the per-developer drill-down is deny-closed); the UI mirrors that to hide
// the developer view from a viewer who lacks adoption:developer:read.
import { http } from '@/lib/api/client'
import type {
  DiscrepancyResponse,
  DevelopersResponse,
  LensId,
  SummaryResponse,
  TeamsResponse,
  TrendResponse,
} from './types'

const BASE = '/v1/m/adoption'

export interface RangeParams {
  since?: string
  until?: string
}

export const adoptionApi = {
  summary: (params?: RangeParams) =>
    http.get<SummaryResponse>(`${BASE}/summary`, { query: { ...params } }),
  trend: (lens: LensId, params?: RangeParams) =>
    http.get<TrendResponse>(`${BASE}/trend`, { query: { lens, ...params } }),
  teams: (params?: RangeParams) =>
    http.get<TeamsResponse>(`${BASE}/teams`, { query: { ...params } }),
  discrepancy: (params?: RangeParams) =>
    http.get<DiscrepancyResponse>(`${BASE}/discrepancy`, {
      query: { ...params },
    }),
  developers: (params?: RangeParams) =>
    http.get<DevelopersResponse>(`${BASE}/developers`, {
      query: { ...params },
    }),
}

export const adoptionKeys = {
  all: (tenant: string | null) => ['adoption', tenant] as const,
  summary: (tenant: string | null, params?: unknown) =>
    params === undefined
      ? (['adoption', tenant, 'summary'] as const)
      : (['adoption', tenant, 'summary', params] as const),
  trend: (tenant: string | null, lens: string, params?: unknown) =>
    params === undefined
      ? (['adoption', tenant, 'trend', lens] as const)
      : (['adoption', tenant, 'trend', lens, params] as const),
  teams: (tenant: string | null, params?: unknown) =>
    params === undefined
      ? (['adoption', tenant, 'teams'] as const)
      : (['adoption', tenant, 'teams', params] as const),
  discrepancy: (tenant: string | null, params?: unknown) =>
    params === undefined
      ? (['adoption', tenant, 'discrepancy'] as const)
      : (['adoption', tenant, 'discrepancy', params] as const),
  developers: (tenant: string | null, params?: unknown) =>
    params === undefined
      ? (['adoption', tenant, 'developers'] as const)
      : (['adoption', tenant, 'developers', params] as const),
}
