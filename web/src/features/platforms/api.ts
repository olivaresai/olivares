// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Platforms & lifecycle endpoint wrapper + query keys — LIVE since. The view
// used to read the *.data.ts arrays directly (declared reference); the engine
// now serves the same reference over HTTP: GET /v1/m/models/platforms
// (modules/models/platforms.go, fed by the credential-less connectors/claude-api
// reference accessors via the cmd adapter). Per the flip contract (rate-limits
// precedent): the route ALWAYS answers 200; when no provider is wired it
// returns `available:false` + a `reason` — never a 404 seam and never a fabricated
// empty matrix. Tenant-scoped keys put the active tenant first.
import { http } from '@/lib/api'
import type { PlatformsReference } from './types'

/** The models module's route namespace (this view reads /platforms in it). */
const BASE = '/v1/m/models'

export const platformsApi = {
  /** The full surfaces + lifecycle + param-deprecation reference, with its AsOf
   *  and source citations carried IN the response (the view interpolates them,
   *  it no longer hardcodes an AsOf). */
  reference: () => http.get<PlatformsReference>(`${BASE}/platforms`),
}

/** Tenant-scoped query keys (query.ts contract: tenant id FIRST). */
export const platformsKeys = {
  all: (tenant: string | null) => ['platforms', tenant] as const,
  reference: (tenant: string | null) =>
    ['platforms', tenant, 'reference'] as const,
}
