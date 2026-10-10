// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { http } from '@/lib/api'
import type { TenantRequestOptions } from '@/lib/api/client'
import type { ListResponse } from '@/lib/api/types'
import type { WorkspaceDTO, AgentGroupDTO } from '@/features/console/api'

export interface WorkspaceSummaryDTO {
  workspace_id: string
  name: string
  slug: string
  is_default: boolean
  agent_count: number
  session_count: number
  resource_count: number
  group_count: number
  // ⛔ ESTAS CUATRO DICEN QUE EL RECUENTO DE ARRIBA ES UN SUELO, NO UN TOTAL, y llegan de
  //    `#1647`. El motor construía la consulta con `Limit: 10000`, pero el store recorta en
  //    `maxLimit = 1000` (`core/internal/store/sqlstore/generic.go`), así que los cuatro
  //    recuentos SATURABAN en 1.000 y el handler descartaba el `page.HasMore` que lo delataba
  //    cuatro líneas después. Con esto, `true` significa «al menos N».
  //
  //    OPCIONALES A PROPÓSITO: una consola servida por un motor anterior a `#1647` no los
  //    recibe, y entonces tiene que pintar el número EXACTO — inventarse un «≥» donde el motor
  //    no ha dicho nada sería el error simétrico al que esto arregla.
  agent_count_capped?: boolean
  session_count_capped?: boolean
  resource_count_capped?: boolean
  group_count_capped?: boolean
}

/** One kind a workspace holds, from GET /v1/workspaces/{id}/contents. */
export interface WorkspaceKindCountDTO {
  kind: string
  count: number
  /** True: `count` is a floor ("at least"), never a total. */
  capped: boolean
}

/** Every kind that declares workspace lineage, counted by the engine's one contents read. */
export interface WorkspaceContentsDTO {
  workspace_id: string
  kinds: WorkspaceKindCountDTO[]
}

export interface AgentBriefDTO {
  id: string
  name: string
  kind: string
  status: string
  workspace_id?: string
}

const DASHBOARD_LIST_CEILING = 10

export const workspaceDashboardApi = {
  summary: (workspaceId: string) =>
    http.get<WorkspaceSummaryDTO>(`/v1/workspaces/${workspaceId}/summary`),

  agents: (workspaceId: string) =>
    http.get<ListResponse<AgentBriefDTO>>('/v1/agents', {
      query: { workspace_id: workspaceId, limit: DASHBOARD_LIST_CEILING },
    }),

  groups: (workspaceId: string) =>
    http.get<ListResponse<AgentGroupDTO>>('/v1/agent-groups', {
      query: { workspace_id: workspaceId, limit: DASHBOARD_LIST_CEILING },
    }),

  workspace: (workspaceId: string) =>
    http.get<WorkspaceDTO>(`/v1/workspaces/${workspaceId}`),

  contents: (
    workspaceId: string,
    scope: TenantRequestOptions & { signal?: AbortSignal },
  ) =>
    http.get<WorkspaceContentsDTO>(
      `/v1/workspaces/${encodeURIComponent(workspaceId)}/contents`,
      scope,
    ),
}

export const workspaceDashboardKeys = {
  summary: (tenant: string | null, wsId: string) =>
    ['workspace-dashboard', 'summary', tenant, wsId] as const,
  agents: (tenant: string | null, wsId: string) =>
    ['workspace-dashboard', 'agents', tenant, wsId] as const,
  groups: (tenant: string | null, wsId: string) =>
    ['workspace-dashboard', 'groups', tenant, wsId] as const,
  workspace: (tenant: string | null, wsId: string) =>
    ['workspace-dashboard', 'workspace', tenant, wsId] as const,
  contents: (tenant: string, wsId: string) =>
    ['workspace-dashboard', 'contents', tenant, wsId] as const,
}
