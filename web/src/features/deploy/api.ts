// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Endpoint helpers + query keys for the deployment & integration module (VII). Thin
// wrappers over the core HTTP client against /v1/m/deploy (ARCHITECTURE.md — no logic
// here). The active tenant header is attached automatically; tenant-scoped keys
// cache-isolate per tenant (query.ts contract).
import { http } from '@/lib/api'
import type { ListResponse } from '@/lib/api/types'
import type {
  DefinitionCreateInput,
  DefinitionDTO,
  DefinitionUpdateInput,
  MutationInput,
  MutationResponse,
  OperationDTO,
  PlanResponse,
  RevisionDTO,
  RollbackInput,
  VerifyResponse,
  WiringDTO,
} from './types'

const BASE = '/v1/m/deploy'

export interface ListParams {
  cursor?: string
  limit?: number
}

export interface DefinitionListParams extends ListParams {
  environment?: string
  status?: string
}

export interface WiringListParams extends ListParams {
  definition_id?: string
  status?: string
}

export interface OperationListParams extends ListParams {
  definition_id?: string
  op?: string
  status?: string
}

const id = (s: string) => encodeURIComponent(s)

// El techo que pedimos al motor. Los TRES handlers de lista de esta feature llaman `listQuery(r)`
// y publican `has_more` — `modules/deploy/definitions.go:262`, `modules/deploy/wiring.go:232` y
// `modules/deploy/operations.go:47` —, así que sin `limit` el repositorio genérico pagina a 100
// (`core/internal/store/sqlstore/generic.go:28`). Pedimos el máximo que acepta (`maxLimit`, :29).
//
// ⛔ EL ORDEN NO ES DECORATIVO, Y AQUÍ SE VE POR QUÉ. `{ ...params, limit: params?.limit ?? X }`
// deja que el llamante GANE, y hay uno que depende de ello: `definition-detail.tsx:123` pide
// `listOperations({ definition_id, limit: 1 })` para traer SOLO la última operación. Con la forma
// contraria —`{ limit: X, ...params }`— ese `limit: 1` seguiría ganando, pero un
// `{ limit: undefined }` explícito pisaría el techo con `undefined`. Con `??` no puede.
//
// ⚠ `listRevisions` NO lleva techo, y es deliberado: su handler drena con `listAll`
// (`modules/deploy/definitions.go:437`), así que un `limit` ahí sería decorativo y AFIRMARÍA un
// gobierno que no existe. Hay una celda que lo fija en esa dirección.
export const EVIDENCE_PAGE = 1000

export const deployApi = {
  // --- definitions (desired state) -------------------------------------------
  listDefinitions: (params?: DefinitionListParams) =>
    http.get<ListResponse<DefinitionDTO>>(`${BASE}/definitions`, {
      query: { ...params, limit: params?.limit ?? EVIDENCE_PAGE },
    }),
  getDefinition: (defId: string) =>
    http.get<DefinitionDTO>(`${BASE}/definitions/${id(defId)}`),
  createDefinition: (input: DefinitionCreateInput) =>
    http.post<DefinitionDTO>(`${BASE}/definitions`, input),
  updateDefinition: (defId: string, input: DefinitionUpdateInput) =>
    http.put<DefinitionDTO>(`${BASE}/definitions/${id(defId)}`, input),
  deleteDefinition: (defId: string) =>
    http.delete<void>(`${BASE}/definitions/${id(defId)}`),

  // --- versions & rollback ---------------------------------------------------
  listRevisions: (defId: string) =>
    http.get<ListResponse<RevisionDTO>>(
      `${BASE}/definitions/${id(defId)}/revisions`,
    ),
  rollback: (defId: string, input: RollbackInput) =>
    http.post<DefinitionDTO>(
      `${BASE}/definitions/${id(defId)}/rollback`,
      input,
    ),

  // --- governed reconciliation ----------------------------------------------
  plan: (defId: string) =>
    http.post<PlanResponse>(`${BASE}/definitions/${id(defId)}/plan`),
  verify: (defId: string) =>
    http.post<VerifyResponse>(`${BASE}/definitions/${id(defId)}/verify`),
  apply: (defId: string, input?: MutationInput) =>
    http.post<MutationResponse>(
      `${BASE}/definitions/${id(defId)}/apply`,
      input ?? {},
    ),
  retire: (defId: string, input?: MutationInput) =>
    http.post<MutationResponse>(
      `${BASE}/definitions/${id(defId)}/retire`,
      input ?? {},
    ),

  // --- wirings (PERMITTED agent→resource) -----------------------------------
  listWirings: (params?: WiringListParams) =>
    http.get<ListResponse<WiringDTO>>(`${BASE}/wirings`, {
      query: { ...params, limit: params?.limit ?? EVIDENCE_PAGE },
    }),

  // --- change-management ledger ---------------------------------------------
  listOperations: (params?: OperationListParams) =>
    http.get<ListResponse<OperationDTO>>(`${BASE}/operations`, {
      query: { ...params, limit: params?.limit ?? EVIDENCE_PAGE },
    }),
}

/** Tenant-scoped query keys (query.ts contract: tenant id in every key). */
export const deployKeys = {
  all: (t: string | null) => ['deploy', t] as const,
  definitions: (t: string | null, params?: unknown) =>
    params === undefined
      ? (['deploy', t, 'definitions'] as const)
      : (['deploy', t, 'definitions', params] as const),
  definition: (t: string | null, defId: string) =>
    ['deploy', t, 'definition', defId] as const,
  revisions: (t: string | null, defId: string) =>
    ['deploy', t, 'definition', defId, 'revisions'] as const,
  wirings: (t: string | null, params?: unknown) =>
    params === undefined
      ? (['deploy', t, 'wirings'] as const)
      : (['deploy', t, 'wirings', params] as const),
  operations: (t: string | null, params?: unknown) =>
    params === undefined
      ? (['deploy', t, 'operations'] as const)
      : (['deploy', t, 'operations', params] as const),
}
