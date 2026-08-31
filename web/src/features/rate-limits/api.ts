// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Rate Limits view (ANT2-05) endpoint wrappers + query keys. The web presents over
// the SAME API, no logic here (ARCHITECTURE.md). Two provenance classes:
//
//  • REAL — `findings` reads the LIVE security-findings endpoint, filtered to the
//    governance Info finding the connector already emits (governance.go:230-242,
//    subject_kind = "anthropic.rate_limit"). It carries the COUNT a gateway/proxy
//    must keep in sync; the view shows that count + caveat truthfully today.
//  • LIVE — `inventory` reads `GET /v1/m/models/rate-limits` (modules/models/
//    ratelimits.go; flipped from a declared seam in). The route ALWAYS answers
//    200; it returns `available=false` + a `reason` (NOT a 404 seam) when the read-only
//    Admin connector is unwired, so the view shows an honest "unavailable" notice rather
//    than a fabricated empty inventory.
//
// READ-ONLY: the Anthropic Rate Limits API is read-only — there is no write wrapper
// here, and the view exposes no edit/create affordance.
import { http } from '@/lib/api'
import type { ListResponse } from '@/lib/api/types'
import type { RateLimitFinding, RateLimitInventory } from './types'

/** Models namespace this view OWNS (the live per-group inventory seam). */
const BASE = '/v1/m/models'
/** REAL findings namespace (connectors → security module).*/
const SECURITY = '/v1/m/security'
const FINDINGS_CEILING = 1000

/** The connector's subject_kind for the rate-limit summary finding
 *  (governance.go:44 subjectRateLimit). The view filters the live findings list to
 *  exactly this subject so it reads only the count summary, nothing else. */
export const RATE_LIMIT_SUBJECT = 'anthropic.rate_limit'

export const rateLimitsApi = {
  // --- REAL: the governance count finding -------------------------------------
  /** The Info finding summarizing how many rate limits a gateway/proxy must mirror. */
  findings: () =>
    http.get<ListResponse<RateLimitFinding>>(`${SECURITY}/findings`, {
      query: { subject_kind: RATE_LIMIT_SUBJECT, limit: FINDINGS_CEILING },
    }),

  // --- LIVE: the per-group inventory (GET /v1/m/models/rate-limits) -----------
  /** The org- and workspace-scoped rate-limit inventory. Always 200 — `available=false`
   *  with a reason when the read-only Admin connector is unwired (not a 404 seam). */
  inventory: () => http.get<RateLimitInventory>(`${BASE}/rate-limits`),
}

/** Tenant-scoped query keys (query.ts contract: tenant id FIRST). */
export const rateLimitsKeys = {
  all: (tenant: string | null) => ['rate-limits', tenant] as const,
  findings: (tenant: string | null) =>
    ['rate-limits', tenant, 'findings'] as const,
  inventory: (tenant: string | null) =>
    ['rate-limits', tenant, 'inventory'] as const,
}
