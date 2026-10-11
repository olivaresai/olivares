// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { http, type TenantRequestOptions } from '@/lib/api/client'
import type { ListResponse } from '@/lib/api/types'
import { agentOpsApi } from '@/features/agentops/api'
import type { WorkspaceDTO as FolderDTO } from '@/features/agentops/types'
import type { WorkspaceDTO, SourceRosterEntry } from '@/features/console/api'
import { mcpGatewayApi } from '@/features/console/mcp-gateway-api'
import type { PolicyDTO } from '@/features/governance/types'
import { listWorkItems } from '@/features/work/api'
import type { EstateFamily, EstateNode, EstatePage } from './types'

export const ESTATE_PAGE_SIZE = 25
type Scope = TenantRequestOptions & { signal: AbortSignal }

export async function readEstateFamily(
  kind: EstateFamily,
  scope: Scope,
  cursor?: string,
): Promise<EstatePage> {
  scope.signal.throwIfAborted()
  let nodes: EstateNode[]
  let hasMore = false
  let next: string | undefined
  const query = { limit: ESTATE_PAGE_SIZE, ...(cursor ? { cursor } : {}) }
  switch (kind) {
    case 'workspace': {
      const page = await http.get<ListResponse<WorkspaceDTO>>(
        '/v1/workspaces',
        { ...scope, query },
      )
      nodes = page.items
        .filter((row) => row.tenant_id === scope.tenant)
        .map((row) => ({
          kind,
          ref: row.id,
          label: row.name,
          status: row.status,
        }))
      hasMore = page.has_more
      next = page.cursor
      break
    }
    case 'folder': {
      const page = await http.get<ListResponse<FolderDTO>>(
        '/v1/m/sessions/workspaces',
        { ...scope, query },
      )
      nodes = page.items.map((row) => ({
        kind,
        ref: row.workspace_ref,
        label: row.name || row.workspace_ref,
        status: row.state,
      }))
      hasMore = page.has_more
      next = page.cursor
      break
    }
    case 'session': {
      const page = await agentOpsApi.listRuns(
        { limit: ESTATE_PAGE_SIZE },
        scope,
      )
      nodes = page.items.map((row) => ({
        kind,
        ref: row.run_ref,
        label: row.name || row.run_ref,
        status: row.state,
        workspaceId: row.authz_workspace_id,
        folderRef: row.workspace_ref,
        peerMode:
          row.peers_rule === 'same-template' ? 'same-template' : 'explicit',
      }))
      // The native run list ignores cursors. Do not promise a next page or join provider IDs to peers.
      hasMore = page.has_more
      break
    }
    case 'work': {
      const page = await listWorkItems(query, scope, scope.signal)
      nodes = page.items.map((row) => ({
        kind,
        ref: row.id,
        label: row.title,
        status: row.status,
        workspaceId: row.workspace_id,
      }))
      hasMore = page.has_more
      next = page.next_cursor
      break
    }
    case 'connector': {
      const page = await http.get<{ sources: SourceRosterEntry[] }>(
        '/v1/console/sources',
        scope,
      )
      // This owner is deployment-wide and superadmin-only. Keep foreign-tenant labels and config out of the projection.
      nodes = page.sources
        .filter((row) => row.tenant === scope.tenant)
        .map((row) => ({
          kind,
          ref: row.id || row.name,
          label: row.name,
          status: row.status,
        }))
      break
    }
    case 'mcp': {
      const snapshot = await mcpGatewayApi.get(scope)
      nodes = snapshot.servers.map((row) => ({
        kind,
        ref: row.id,
        label: row.name,
        status: row.enabled ? 'enabled' : 'disabled',
      }))
      break
    }
    case 'policy': {
      const page = await http.get<ListResponse<PolicyDTO>>(
        '/v1/m/governance/policies',
        { ...scope, query },
      )
      nodes = page.items
        .filter((row) => row.id)
        .map((row) => ({
          kind,
          ref: row.id!,
          label: row.name,
          status: row.enabled ? 'enabled' : 'disabled',
        }))
      hasMore = page.has_more
      next = page.cursor
      break
    }
  }
  scope.signal.throwIfAborted()
  return {
    nodes: nodes.slice(0, ESTATE_PAGE_SIZE),
    hasMore: hasMore || nodes.length > ESTATE_PAGE_SIZE,
    cursor: next,
    readAt: new Date().toISOString(),
  }
}
