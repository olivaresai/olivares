// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Endpoint helpers + query keys for the unified session recording viewer.
// Thin wrappers over the core HTTP client against /v1/m/recording (ARCHITECTURE.md —
// no logic here). The /unified endpoint returns session header + paginated frames
// + paginated timeline + correlated ledger + optional verify verdict in one
// round-trip. The active tenant header is attached automatically; tenant-scoped
// keys cache-isolate per tenant (query.ts contract).
//
import { http } from '@/lib/api/client'
import type { SessionDTO } from '@/features/recordings/types'
import { useSessionStore } from '@/stores/session'
import { useTenantStore } from '@/stores/tenant'
import type { UnifiedResponse, VerifyResult } from './types'

const BASE = '/v1/m/recording'

/** Query parameters for the /unified endpoint — both paginated sub-resources
 * can be independently cursor-advanced for progressive loading. */
export interface UnifiedParams {
  limit?: number
  frame_cursor?: string
  timeline_cursor?: string
  timeline_limit?: number
}

export const viewerApi = {
  /** Unified session view: header + frames page + timeline page + ledger + verify. */
  unified: (id: string, params?: UnifiedParams) =>
    http.get<UnifiedResponse>(
      `${BASE}/sessions/${encodeURIComponent(id)}/unified`,
      {
        query: { ...params },
      },
    ),
  /** Recompute the frame chain and every ledger anchor now. */
  verify: (id: string) =>
    http.get<VerifyResult>(`${BASE}/sessions/${encodeURIComponent(id)}/verify`),
  /** Irreversibly seal an active recording session. */
  seal: (id: string) =>
    http.post<SessionDTO>(`${BASE}/sessions/${encodeURIComponent(id)}/seal`),
  /** Generate the optional, derived reviewer summary. */
  summarize: (id: string) =>
    http.post<SessionDTO>(
      `${BASE}/sessions/${encodeURIComponent(id)}/summarize`,
    ),
  /** Export the full session as a structured JSON document. */
  exportJSON: (id: string) =>
    http.get<{ session: unknown; frames: unknown[] }>(
      `${BASE}/sessions/${encodeURIComponent(id)}/export`,
      { query: { format: 'json' } },
    ),
  /** Export as plain-text summary (raw fetch — http client has no getText). */
  exportSummary: async (id: string): Promise<string> => {
    const headers = new Headers({ Accept: 'text/plain' })
    const token = useSessionStore.getState().token
    if (token) headers.set('Authorization', `Bearer ${token}`)
    const tenant = useTenantStore.getState().activeTenant
    if (tenant) headers.set('X-Olivares-Tenant', tenant)
    const res = await fetch(
      `${BASE}/sessions/${encodeURIComponent(id)}/export?format=summary`,
      { method: 'GET', headers, credentials: 'same-origin' },
    )
    if (!res.ok) throw new Error(`Export failed: ${res.status}`)
    return res.text()
  },
}

/** Tenant-scoped query keys (query.ts contract: tenant id in every key). */
export const viewerKeys = {
  all: (t: string | null) => ['session-viewer', t] as const,
  unified: (t: string | null, id: string, params?: unknown) =>
    params === undefined
      ? (['session-viewer', t, 'unified', id] as const)
      : (['session-viewer', t, 'unified', id, params] as const),
}
