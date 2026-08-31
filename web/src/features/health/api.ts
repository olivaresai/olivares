// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { http } from '@/lib/api/client'
import type { ListResponse } from '@/lib/api/types'
import type {
  ConnectorHealthResponse,
  CreateCheckInput,
  DepGraphResponse,
  EventDTO,
  IncidentDTO,
  PublicStatusResponse,
  SlaDTO,
  StatusDTO,
  UpdateCheckInput,
} from './types'

/**
 * Health/SLA endpoints (module XXII) — all under /v1/m/health/. The route is
 * gated by `health:read`; individual endpoints carry finer tiers: status/SLA/deps/
 * incidents/events reads are `health:status:read`, check reads are
 * `health:check:read`, and resolving an incident is `health:check:admin`. Opening
 * the live stream is itself a PRIVILEGED, AUDITED read (`health.stream.open`). The
 * web fetches the engine's derived health and renders it; it adds no logic and
 * recomputes no uptime — the engine reconstructs SLA from the ledger (ARCHITECTURE.md).
 */

export interface StatusParams {
  state?: string
  subject_kind?: string
  limit?: number
  cursor?: string
}

export interface SlaParams {
  /** REQUIRED — the engine 400s without both. */
  subject_kind: string
  subject_ref: string
  window_seconds?: number
}

// ⛔ `limit` NO ES OPCIONAL DE HECHO, y su ausencia aquí era visible en este mismo fichero:
//    `CheckParams` lo declara y estos dos no. Sin él, el repositorio genérico pagina a
//    **100** (`core/internal/store/sqlstore/generic.go`, `defaultLimit`) y el handler
//    contesta con `has_more: true` que nadie mira, así que la pantalla enseña las primeras
//    cien y **no dice que hay más**. En incidentes eso es «no hay más incidentes
//    abiertos», que es la afirmación más cara que puede hacer una vista de salud.
export interface IncidentParams {
  state?: string
  subject_kind?: string
  subject_ref?: string
  limit?: number
}

export interface EventParams {
  subject_kind?: string
  subject_ref?: string
  limit?: number
}

export interface CheckParams {
  subject_kind?: string
  desired_status?: string
  limit?: number
  cursor?: string
}

export const healthApi = {
  status: (params?: StatusParams) =>
    http.get<ListResponse<StatusDTO>>('/v1/m/health/status', {
      query: { ...params },
    }),
  sla: (params: SlaParams) =>
    http.get<SlaDTO>('/v1/m/health/sla', { query: { ...params } }),
  dependencies: (params?: { limit?: number; cursor?: string }) =>
    http.get<DepGraphResponse>('/v1/m/health/dependencies', {
      query: { ...params },
    }),
  incidents: (params?: IncidentParams) =>
    http.get<ListResponse<IncidentDTO>>('/v1/m/health/incidents', {
      query: { ...params },
    }),
  events: (params?: EventParams) =>
    http.get<ListResponse<EventDTO>>('/v1/m/health/events', {
      query: { ...params },
    }),
  checks: (params?: CheckParams) =>
    http.get<ListResponse<StatusDTO>>('/v1/m/health/checks', {
      query: { ...params },
    }),
  createCheck: (input: CreateCheckInput) =>
    http.post<StatusDTO>('/v1/m/health/checks', input),
  getCheck: (id: string) =>
    http.get<StatusDTO>(`/v1/m/health/checks/${encodeURIComponent(id)}`),
  updateCheck: (id: string, input: UpdateCheckInput) =>
    http.put<StatusDTO>(`/v1/m/health/checks/${encodeURIComponent(id)}`, input),
  deleteCheck: (id: string) =>
    http.delete<void>(`/v1/m/health/checks/${encodeURIComponent(id)}`),
  /** PRIVILEGED, AUDITED (health:check:admin). Acknowledges the period closes — it
   * does NOT claim the subject recovered (recovery comes from real liveness). */
  resolveIncident: (id: string) =>
    http.post<IncidentDTO>(
      `/v1/m/health/incidents/${encodeURIComponent(id)}/resolve`,
    ),
  /**: per-connector health metrics (health:status:read).*/
  connectorHealth: () =>
    http.get<ConnectorHealthResponse>('/v1/connectors/health'),
  /**: public aggregate status (unauthenticated).*/
  publicStatus: () => http.get<PublicStatusResponse>('/status'),
}

/** Tenant-scoped query keys (the active tenant id isolates cache per org). */
export const healthKeys = {
  all: (tenant: string | null) => ['health', tenant] as const,
  status: (tenant: string | null, params?: StatusParams) =>
    params === undefined
      ? (['health', tenant, 'status'] as const)
      : (['health', tenant, 'status', params] as const),
  sla: (tenant: string | null, params?: SlaParams) =>
    params === undefined
      ? (['health', tenant, 'sla'] as const)
      : (['health', tenant, 'sla', params] as const),
  dependencies: (tenant: string | null) =>
    ['health', tenant, 'dependencies'] as const,
  incidents: (tenant: string | null, params?: IncidentParams) =>
    params === undefined
      ? (['health', tenant, 'incidents'] as const)
      : (['health', tenant, 'incidents', params] as const),
  events: (tenant: string | null, params?: EventParams) =>
    params === undefined
      ? (['health', tenant, 'events'] as const)
      : (['health', tenant, 'events', params] as const),
  checks: (tenant: string | null, params?: CheckParams) =>
    params === undefined
      ? (['health', tenant, 'checks'] as const)
      : (['health', tenant, 'checks', params] as const),
  check: (tenant: string | null, id: string) =>
    ['health', tenant, 'checks', id] as const,
  connectorHealth: (tenant: string | null) =>
    ['health', tenant, 'connectorHealth'] as const,
  publicStatus: () => ['publicStatus'] as const,
}
