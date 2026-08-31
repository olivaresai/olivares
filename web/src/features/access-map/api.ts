// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { http } from '@/lib/api/client'
import type { AttackPathsResponse, DiffResponse, GraphResponse } from './types'

/**
 * Access-map endpoints (module III) — all GET, all under /v1/m/accessmap/.
 * These are PRIVILEGED, SELF-AUDITED reads (docs/SECURITY-HARDENING.md,§4): /graph and /neighbors
 * need `accessmap:graph:read`, /drift needs `accessmap:drift:read`, and every call
 * seals an event in the append-only ledger server-side. The web adds no logic — it
 * fetches the engine's node+edge contract and renders it (ARCHITECTURE.md).
 */

export interface GraphParams {
  origin_kind?: string
  origin_id?: string
  resource_id?: string
  mode?: string
  confidence?: string
  signal_source?: string
  limit?: number
  cursor?: string
}

export type NeighborDirection = 'outgoing' | 'incoming' | 'both'

export const accessMapApi = {
  graph: (params?: GraphParams) =>
    http.get<GraphResponse>('/v1/m/accessmap/graph', { query: { ...params } }),
  neighbors: (
    id: string,
    direction: NeighborDirection = 'both',
    kind?: string,
  ) =>
    http.get<GraphResponse>('/v1/m/accessmap/neighbors', {
      query: { id, direction, kind },
    }),
  drift: (params?: GraphParams) =>
    http.get<DiffResponse>('/v1/m/accessmap/drift', { query: { ...params } }),
  // Reachability and escalation are rooted at an agent. Exfiltration is rooted at a
  // resource and deliberately uses a DIFFERENT parameter (`attackpath.go:407-414`).
  // Keeping that distinction here prevents the console from turning every exfil read
  // into the handler's `resource_id is required` response.
  attackReachability: (agentId: string) =>
    http.get<AttackPathsResponse>('/v1/m/accessmap/attack-paths/reachability', {
      query: { agent_id: agentId },
    }),
  attackEscalation: (agentId: string) =>
    http.get<AttackPathsResponse>('/v1/m/accessmap/attack-paths/escalation', {
      query: { agent_id: agentId },
    }),
  attackExfil: (resourceId: string) =>
    http.get<AttackPathsResponse>('/v1/m/accessmap/attack-paths/exfil', {
      query: { resource_id: resourceId },
    }),
  // ⛔ NO hay envoltorio para `/attack-paths/summary`, y es deliberado: dos de sus seis
  //    campos —`escalation_paths` y `exfil_routes`— NO SE CALCULAN NUNCA en el motor
  //    (`handleAttackPathSummary`, cero asignaciones, medido 2026-08-20) y salen siempre 0.
  //    Un metodo de cliente que ninguna pantalla pulsa es superficie muerta que insinua una
  //    vista que no existe — y `lint:client-callers` lo caza, con razon. Cuando el motor
  //    calcule esos dos campos, quien construya la vista añade aqui su envoltorio.
}

/** Tenant-scoped query keys (the active tenant id isolates cache per org). */
export const accessMapKeys = {
  all: (tenant: string | null) => ['accessmap', tenant] as const,
  attackPaths: (tenant: string | null, kind: string, subjectId: string) =>
    ['accessmap', tenant, 'attack-paths', kind, subjectId] as const,
  graph: (tenant: string | null, params?: GraphParams) =>
    params === undefined
      ? (['accessmap', tenant, 'graph'] as const)
      : (['accessmap', tenant, 'graph', params] as const),
  drift: (tenant: string | null, params?: GraphParams) =>
    params === undefined
      ? (['accessmap', tenant, 'drift'] as const)
      : (['accessmap', tenant, 'drift', params] as const),
  neighbors: (tenant: string | null, id: string, direction: string) =>
    ['accessmap', tenant, 'neighbors', id, direction] as const,
}
