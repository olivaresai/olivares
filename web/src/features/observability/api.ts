// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Observability (ADM-OBS-01) endpoint wrappers + query keys — LIVE since. The
// W3C Trace Context correlators shipped in (trace ids are stamped into the
// audit ledger's meta — core/observability/trace/meta.go:16-22), and added the
// read-model that serves them: modules/observability answers all three routes.
// Per the flip contract (rate-limits precedent): a mounted route ALWAYS
// answers 200 for the list/health reads — absent data is an empty/omitted field,
// never a 404 seam. The ONE may-404 read is the trace detail: an unknown or
// ledger-evicted trace id legitimately has no resource. Tenant-scoped keys put the
// active tenant first.
import { http } from '@/lib/api'
import type { ListResponse } from '@/lib/api/types'
import type {
  IngestionHealthResponse,
  TraceDetail,
  TraceListItem,
} from './types'

/** The observability module's route namespace (modules/observability api.go). */
const BASE = '/v1/m/observability'

export interface TraceListParams {
  q?: string
  from?: string
  to?: string
  service?: string
  status?: string
  cursor?: string
  limit?: number
}

export const observabilityApi = {
  // --- LIVE: per-standard ingestion health + per-source bus counters ----------
  /** Per-standard pins (OTel GenAI / OCSF / ASIM / SIEM / Prometheus / W3C) plus
   *  the per-source live counters. Always 200 where the module is mounted. */
  ingestionHealth: () =>
    http.get<IngestionHealthResponse>(`${BASE}/ingestion-health`),

  // --- LIVE: ledger-correlation trace read-model ------------------------------
  /** The trace list, derived from W3C trace ids on audit-ledger events. Always
   *  200; an empty ledger window returns an empty list, never a seam. */
  traces: (params?: TraceListParams) =>
    http.get<ListResponse<TraceListItem>>(`${BASE}/traces`, {
      query: { ...params },
    }),
  /** One trace's ledger-derived spans, for the waterfall. 404 when the id is
   *  unknown or evicted from the walk window (last 20000 ledger events). */
  trace: (id: string) =>
    http.get<TraceDetail>(`${BASE}/traces/${encodeURIComponent(id)}`),
  /** OTLP-compatible JSON export of one trace for Jaeger/Tempo/Datadog import. */
  exportTrace: (id: string) =>
    http.get<unknown>(`${BASE}/traces/${encodeURIComponent(id)}/export`),
}

export const observabilityKeys = {
  all: (tenant: string | null) => ['observability', tenant] as const,
  ingestionHealth: (tenant: string | null) =>
    ['observability', tenant, 'ingestion-health'] as const,
  traces: (tenant: string | null, params?: unknown) =>
    params === undefined
      ? (['observability', tenant, 'traces'] as const)
      : (['observability', tenant, 'traces', params] as const),
  trace: (tenant: string | null, id: string) =>
    ['observability', tenant, 'trace', id] as const,
}
