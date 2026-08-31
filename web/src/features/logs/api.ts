// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Log Viewer endpoint wrapper + query keys. The buffer endpoint serves historical
// log entries; the SSE stream (consumed via useLiveStream in the view) is not
// wrapped here — it uses the shared SSE transport directly.
import { http } from '@/lib/api/client'
import type { LogBufferResponse } from './types'

const BASE = '/v1/console/logs'

export interface LogBufferParams {
  /** Exact selected level set as a stable lowercase CSV. Omitted = all levels. */
  levels?: string
  module?: string
  limit?: number
}

export const logsApi = {
  /** GET /v1/console/logs/buffer — historical backfill. Superadmin-only. */
  buffer: (params?: LogBufferParams) =>
    http.get<LogBufferResponse>(`${BASE}/buffer`, {
      query: { ...params },
    }),
}

export const logsKeys = {
  all: () => ['logs'] as const,
  buffer: (params?: LogBufferParams) =>
    params === undefined
      ? (['logs', 'buffer'] as const)
      : (['logs', 'buffer', params] as const),
}
