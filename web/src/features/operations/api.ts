// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { http, type RequestOptions } from '@/lib/api/client'
import type { components } from '@/lib/api/openapi.gen'

/** The host the engine runs on, with its last 24 hours: `GET /v1/system/host`. */
export type HostStatus = components['schemas']['HostStatus']

export const operationsApi = {
  /** A system administrator's read; a host that cannot be read (not Linux) answers 501. */
  host: (opts?: Pick<RequestOptions, 'signal'>) =>
    http.get<HostStatus>('/v1/system/host', opts),
}

export const operationsKeys = {
  host: (tenant: string | null) => ['operations', tenant, 'host'] as const,
}
