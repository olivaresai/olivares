// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Endpoint helpers + query keys for team cost attribution.
// Thin wrappers over the core HTTP client against
// /v1/m/finops/analytics/team-summary. Tenant-scoped keys cache-isolate per
// tenant (query.ts contract). No logic here — the module owns the math.
import { http } from '@/lib/api/client'
import type { SummaryPeriod, TeamSummaryResponse } from './types'

const BASE = '/v1/m/finops/analytics'

export const teamCostsApi = {
  /** Fetch team-level cost aggregation for the given period.
   *  Omitting `period` causes the backend to default to 30 d. */
  summary: (period?: SummaryPeriod) =>
    http.get<TeamSummaryResponse>(`${BASE}/team-summary`, {
      query: period ? { period } : {},
    }),
}

/** Tenant-scoped query keys (query.ts contract: tenant id in every key). */
export const teamCostsKeys = {
  all: (tenant: string | null) => ['team-costs', tenant] as const,
  summary: (tenant: string | null, period?: SummaryPeriod) =>
    ['team-costs', tenant, 'summary', period ?? '30d'] as const,
}
