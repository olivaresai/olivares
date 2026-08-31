// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { http } from '@/lib/api/client'
import type { RevisionListParams } from '@/features/shared/revisions-sheet'
import type {
  CreateWorkflowInput,
  DryRunResponse,
  PatchWorkflowInput,
  RunWorkflowResponse,
  WorkflowDetail,
  WorkflowListResponse,
  WorkflowRevisionsResponse,
  WorkflowRouteOption,
  WorkflowRunsResponse,
  WorkflowRun,
  WorkflowScheduleOption,
  WorkflowStep,
} from './types'
// El mismo techo que los raíles de la pestaña: el máximo que acepta el motor.
import { EVIDENCE_PAGE } from '../api'

const BASE = '/v1/m/orchestration'
const workflowPath = (id: string) =>
  `${BASE}/workflows/${encodeURIComponent(id)}`

export const workflowsApi = {
  list: () => http.get<WorkflowListResponse>(`${BASE}/workflows`),
  create: (body: CreateWorkflowInput) =>
    http.post<WorkflowDetail>(`${BASE}/workflows`, body),
  detail: (id: string) => http.get<WorkflowDetail>(workflowPath(id)),
  patch: (id: string, body: PatchWorkflowInput) =>
    http.patch<WorkflowDetail>(workflowPath(id), body),
  updateSteps: (id: string, steps: WorkflowStep[]) =>
    http.put<WorkflowDetail>(`${workflowPath(id)}/steps`, { steps }),
  revisions: (id: string, query?: RevisionListParams) =>
    http.get<WorkflowRevisionsResponse>(`${workflowPath(id)}/revisions`, {
      query: query ? { ...query } : undefined,
    }),
  restore: (id: string, revisionId: string) =>
    http.post<WorkflowDetail>(`${workflowPath(id)}/restore`, {
      revision_id: revisionId,
    }),
  dryRun: (id: string) =>
    http.post<DryRunResponse>(`${workflowPath(id)}/dry-run`, {}),
  run: (id: string, approvalRef?: string) =>
    http.post<RunWorkflowResponse>(
      `${workflowPath(id)}/run`,
      approvalRef ? { approval_ref: approvalRef } : {},
    ),
  runs: (id: string) =>
    http.get<WorkflowRunsResponse>(`${workflowPath(id)}/runs`),
  runDetail: (id: string, runId: string) =>
    http.get<WorkflowRun>(
      `${workflowPath(id)}/runs/${encodeURIComponent(runId)}`,
    ),
  // ⛔ LAS DOS LISTAS DE OPCIONES DEL EDITOR, con su techo. Van a los MISMOS handlers que los
  // raíles de la pestaña de aterrizaje —`modules/orchestration/schedules.go:343` y
  // `modules/notify/route.go:141`, los dos con `listQuery(r)`—, así que sin `limit` paginaban a
  // 100. Aquí duele distinto: no es una cifra equivocada, es un DESPLEGABLE al que le faltan
  // opciones que sí existen, y el usuario no puede elegir lo que no ve. Sus tipos ya declaraban
  // `has_more` y nadie lo leía.
  schedules: (query?: { limit?: number }) =>
    http.get<{ items: WorkflowScheduleOption[]; has_more: boolean }>(
      `${BASE}/schedules`,
      { query: { limit: EVIDENCE_PAGE, ...query } },
    ),
  routes: (query?: { limit?: number }) =>
    http.get<{ items: WorkflowRouteOption[]; has_more: boolean }>(
      '/v1/m/notify/routes',
      { query: { limit: EVIDENCE_PAGE, ...query } },
    ),
}

export const workflowsKeys = {
  all: (tenant: string | null) => ['automations-workflows', tenant] as const,
  list: (tenant: string | null) =>
    ['automations-workflows', tenant, 'list'] as const,
  detail: (tenant: string | null, id: string) =>
    ['automations-workflows', tenant, 'detail', id] as const,
  revisions: (tenant: string | null, id: string) =>
    ['automations-workflows', tenant, 'detail', id, 'revisions'] as const,
  dryRun: (tenant: string | null, id: string) =>
    ['automations-workflows', tenant, 'detail', id, 'dry-run'] as const,
  runs: (tenant: string | null, id: string) =>
    ['automations-workflows', tenant, 'detail', id, 'runs'] as const,
  run: (tenant: string | null, id: string, runId: string) =>
    ['automations-workflows', tenant, 'detail', id, 'runs', runId] as const,
  schedules: (tenant: string | null) =>
    ['automations-workflows', tenant, 'schedules'] as const,
  routes: (tenant: string | null) =>
    ['automations-workflows', tenant, 'routes'] as const,
}
