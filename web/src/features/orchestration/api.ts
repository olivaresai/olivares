// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Orchestration (module IV) endpoint wrappers + query keys. Thin `http.*` calls
// against the engine's `/v1/m/orchestration/…` routes (ARCHITECTURE.md — the web presents,
// never recomputes). The graph read is PRIVILEGED + self-audited
// (`orchestration:graph:read`): each call seals a ledger event server-side; the view
// renders a SelfAuditNotice to mirror that. Tenant-scoped keys include the active
// tenant so switching org refetches cleanly.
import { http } from '@/lib/api/client'
import type { ListResponse } from '@/lib/api/types'
import type {
  RevisionListParams,
  RevisionsResponse,
} from '@/features/shared/revisions-sheet'
import type {
  CreateScheduleInput,
  DecisionDTO,
  FireScheduleInput,
  FireScheduleResponse,
  FlowDTO,
  FlowState,
  GraphResponse,
  PatchScheduleInput,
  ScheduleDTO,
  TimelineItem,
} from './types'

const BASE = '/v1/m/orchestration'

export const orchestrationApi = {
  graph: (params?: { limit?: number }) =>
    http.get<GraphResponse>(`${BASE}/graph`, { query: { ...params } }),
  neighbors: (node: string, direction: 'in' | 'out' | 'both' = 'both') =>
    http.get<GraphResponse>(`${BASE}/graph/neighbors`, {
      query: { node, direction },
    }),
  flows: (state?: FlowState) =>
    http.get<ListResponse<FlowDTO>>(`${BASE}/flows`, { query: { state } }),
  timeline: (subject: string) =>
    http.get<ListResponse<TimelineItem>>(`${BASE}/timeline`, {
      query: { subject },
    }),
  schedules: () => http.get<ListResponse<ScheduleDTO>>(`${BASE}/schedules`),
  schedule: (id: string) => http.get<ScheduleDTO>(`${BASE}/schedules/${id}`),
  createSchedule: (body: CreateScheduleInput) =>
    http.post<ScheduleDTO>(`${BASE}/schedules`, body),
  updateSchedule: (id: string, body: PatchScheduleInput) =>
    http.patch<ScheduleDTO>(`${BASE}/schedules/${id}`, body),
  fireSchedule: (id: string, body?: FireScheduleInput) =>
    http.post<FireScheduleResponse>(`${BASE}/schedules/${id}/fire`, body),
  scheduleDecisions: (id: string) =>
    http.get<ListResponse<DecisionDTO>>(`${BASE}/schedules/${id}/decisions`),
  // ⛔ EL LEDGER DEL TENANT NO ES «LO MISMO SIN FILTRO»: TRAE FILAS QUE NINGÚN SCHEDULE
  //    PUEDE DEVOLVER. `schedule_ref` es NULLABLE (`modules/orchestration/schema.go`) y se
  //    escribe con `setIf`, y **ninguna** decisión de una EJECUCIÓN DE WORKFLOW lo pone:
  //    llevan `subject_kind: "workflow"` (`modules/orchestration/workflow_run.go`). Como la
  //    consola sólo llega a `/schedules/{id}/decisions`, hoy no hay forma de ver una
  //    denegación de kill switch o de la puerta de aprobación sobre un run.
  //
  //    Medido con control positivo en la misma corrida
  //    (`modules/orchestration/ledger_reachability_test.go`): la ruta por schedule SÍ
  //    devuelve la decisión de su schedule, y NINGUNA devuelve la del workflow.
  //
  // ⛔ `limit` no es opcional de hecho: sin él el repositorio genérico pagina a 100 y el
  //    handler hace una sola llamada a `repo.List` sin drenar el cursor.
  //
  // ⛔ `order: 'newest'` ES EXPLÍCITO Y NO EL DEFAULT DEL MOTOR, y la razón es de contrato:
  //    el store NO emite cursor para una consulta con Sort personalizado y aun así
  //    contesta `has_more: true`, así que el orden inverso NO es paginable. El motor deja
  //    su default cronológico y paginable para el CLI y los SDKs; esta pantalla es un
  //    TOP-N que declara su recorte y nunca pagina, así que puede pedirlo.
  decisions: (params?: { limit?: number }) =>
    http.get<ListResponse<DecisionDTO>>(`${BASE}/decisions`, {
      query: { order: 'newest', ...params },
    }),
  scheduleRevisions: (id: string, query?: RevisionListParams) =>
    http.get<RevisionsResponse<ScheduleDTO>>(
      `${BASE}/schedules/${id}/revisions`,
      { query: query ? { ...query } : undefined },
    ),
  restoreSchedule: (id: string, revisionId: string) =>
    http.post<ScheduleDTO>(`${BASE}/schedules/${id}/restore`, {
      revision_id: revisionId,
    }),
}

export const orchestrationKeys = {
  all: (tenant: string | null) => ['orchestration', tenant] as const,
  graph: (tenant: string | null, params?: unknown) =>
    params === undefined
      ? (['orchestration', tenant, 'graph'] as const)
      : (['orchestration', tenant, 'graph', params] as const),
  flows: (tenant: string | null, state?: string) =>
    ['orchestration', tenant, 'flows', state ?? null] as const,
  timeline: (tenant: string | null, subject: string) =>
    ['orchestration', tenant, 'timeline', subject] as const,
  schedules: (tenant: string | null) =>
    ['orchestration', tenant, 'schedules'] as const,
  schedule: (tenant: string | null, id: string) =>
    ['orchestration', tenant, 'schedules', id] as const,
  scheduleRevisions: (tenant: string | null, id: string) =>
    ['orchestration', tenant, 'schedules', id, 'revisions'] as const,
  scheduleDecisions: (tenant: string | null, id: string) =>
    ['orchestration', tenant, 'schedules', id, 'decisions'] as const,
  // Segmento propio: la clave por schedule es `[…,'schedules',id,'decisions']`, así que un
  // segmento distinto NO puede colisionar con ella. Se comprueba en la batería.
  ledger: (tenant: string | null, params?: unknown) =>
    params === undefined
      ? (['orchestration', tenant, 'ledger'] as const)
      : (['orchestration', tenant, 'ledger', params] as const),
}
