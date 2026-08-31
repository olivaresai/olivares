// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Red-team (module XVIII) endpoint wrappers + query keys. Thin `http.*` calls against
// the engine's `/v1/m/redteam/…` routes (the web presents, never recomputes the
// scorecard). Tenant-scoped keys include the active
// tenant so switching org refetches cleanly. Reads/writes are RBAC-gated
// server-side; the UI mirrors that to hide/disable actions.
import { http } from '@/lib/api/client'
import type { ListResponse } from '@/lib/api/types'
import type {
  AuthorizeInput,
  CatalogResponse,
  ProbeResult,
  Run,
  RunInput,
  Target,
  TargetInput,
} from './types'

const BASE = '/v1/m/redteam'

/**
 * ⛔ EL TECHO SE PIDE, PERO SÓLO DONDE HAY ALGO QUE RECORTAR — y esta vez lo comprobé en el motor
 *    ANTES de escribir el cliente, que es la lección de.
 *
 *    `handleListTargets` y `handleListRuns` pasan por `listQuery(r)` (`modules/redteam/consent.go`,
 *    `scorecard.go`): consumen `limit`, el store aplica 100 por defecto y acepta hasta 1000. Ésas
 *    llevan techo y aviso. `handleListResults` NO: usa `listAll` y devuelve todos los resultados de
 *    la ejecución sin poner `HasMore`, así que no hay recorte que declarar y un aviso sería
 *    inalcanzable.
 *
 *    Aquí la ausencia también es la afirmación: un objetivo que no sale se lee como que **nadie lo
 *    está probando**, y una ejecución que no sale, como que **no se ha ejecutado**.
 */
const REDTEAM_PAGE = 1000

export const redteamApi = {
  catalog: (suite?: string) =>
    http.get<CatalogResponse>(`${BASE}/catalog`, { query: { suite } }),
  targets: (params?: { status?: string }) =>
    http.get<ListResponse<Target>>(`${BASE}/targets`, {
      query: { limit: REDTEAM_PAGE, ...params },
    }),
  target: (id: string) => http.get<Target>(`${BASE}/targets/${id}`),
  registerTarget: (body: TargetInput) =>
    http.post<Target>(`${BASE}/targets`, body),
  authorizeTarget: (id: string, body: AuthorizeInput) =>
    http.post<Target>(`${BASE}/targets/${id}/authorize`, body),
  runs: (params?: { target_ref?: string; suite?: string }) =>
    http.get<ListResponse<Run>>(`${BASE}/runs`, {
      query: { limit: REDTEAM_PAGE, ...params },
    }),
  run: (id: string) => http.get<Run>(`${BASE}/runs/${id}`),
  launchRun: (body: RunInput) => http.post<Run>(`${BASE}/runs`, body),
  // Sin techo A PROPÓSITO: `handleListResults` usa `listAll` y devuelve todos los resultados de
  // la ejecución, sin poner `HasMore` (`modules/redteam/scorecard.go`). No hay recorte que declarar.
  results: (id: string) =>
    http.get<ListResponse<ProbeResult>>(`${BASE}/runs/${id}/results`),
}

export const redteamKeys = {
  all: (tenant: string | null) => ['redteam', tenant] as const,
  catalog: (tenant: string | null, suite?: string) =>
    ['redteam', tenant, 'catalog', suite ?? null] as const,
  targets: (tenant: string | null, params?: unknown) =>
    params === undefined
      ? (['redteam', tenant, 'targets'] as const)
      : (['redteam', tenant, 'targets', params] as const),
  target: (tenant: string | null, id: string) =>
    ['redteam', tenant, 'targets', id] as const,
  runs: (tenant: string | null, params?: unknown) =>
    params === undefined
      ? (['redteam', tenant, 'runs'] as const)
      : (['redteam', tenant, 'runs', params] as const),
  run: (tenant: string | null, id: string) =>
    ['redteam', tenant, 'runs', id] as const,
  results: (tenant: string | null, id: string) =>
    ['redteam', tenant, 'runs', id, 'results'] as const,
}
