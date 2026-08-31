// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { consoleApi } from '@/features/console/api'
import { modelsApi } from '@/features/models/api'
import { listWorkItems } from '@/features/work/api'
import type { TenantRequestOptions } from '@/lib/api/client'
import type {
  BindingLocalKind,
  BindingProtocol,
  ProtocolBindingSpec,
} from './types'

export interface ProtocolCatalogOption {
  id: string
  label: string
  detail: string
}

export interface ProtocolLocalResourceCatalog {
  available: boolean
  options: ProtocolCatalogOption[]
  hasMore: boolean
}

/**
 * Reads only existing, permission-filtered product APIs. Channel authoring has no
 * mounted list route yet, so it deliberately returns unavailable rather than
 * presenting an invented or tenant-wide catalog.
 */
export async function listProtocolLocalResourceCatalog(
  localKind: BindingLocalKind,
  workspaceId: string,
  options: TenantRequestOptions,
  signal?: AbortSignal,
): Promise<ProtocolLocalResourceCatalog> {
  switch (localKind) {
    case 'work_item': {
      const page = await listWorkItems({ limit: 200 }, options, signal)
      return {
        available: true,
        options: page.items
          .filter((item) => item.workspace_id === workspaceId)
          .map((item) => ({
            id: item.id,
            label: item.title,
            detail: `${item.work_kind} · ${item.status}`,
          })),
        hasMore: page.has_more,
      }
    }
    case 'agent': {
      const page = await consoleApi.listAgents(
        {
          workspace_id: workspaceId,
          limit: 200,
        },
        options,
      )
      return {
        available: true,
        // The composition resolver selects the canonical Identity ID, then proves
        // exactly one workspace agent projects from it. Agents without an identity
        // cannot be selected by this ProtocolBinding contract.
        options: page.items
          .filter(
            (agent): agent is typeof agent & { identity_id: string } =>
              !!agent.identity_id,
          )
          .map((agent) => ({
            id: agent.identity_id,
            label: agent.name,
            detail: `${agent.kind} · ${agent.status}`,
          })),
        hasMore: page.has_more,
      }
    }
    case 'model': {
      const page = await modelsApi.models(options)
      return {
        available: true,
        options: page.items.map((model) => ({
          id: model.id,
          label: model.name,
          detail: `${model.provider} · ${model.family} · ${model.status}`,
        })),
        hasMore: page.has_more,
      }
    }
    case 'channel':
      return { available: false, options: [], hasMore: false }
  }
}

export interface ProtocolEndpointOption extends ProtocolCatalogOption {
  peerAuthority: string
  remoteResourceKind: string
  remoteResourceRef: string
}

/**
 * Active specs are the only protocol endpoint catalog currently exposed by the
 * sessions REST surface. They are useful successor/known-peer choices, but the UI
 * labels them as active-spec references and never claims they are a live heartbeat.
 */
export function protocolEndpointOptions(
  specs: ProtocolBindingSpec[],
  protocol: BindingProtocol,
): ProtocolEndpointOption[] {
  const seen = new Set<string>()
  const result: ProtocolEndpointOption[] = []
  for (const spec of specs) {
    if (spec.protocol !== protocol) continue
    const key = [
      spec.peer_authority,
      spec.remote_resource_kind,
      spec.remote_resource_ref,
    ].join('\u0000')
    if (seen.has(key)) continue
    seen.add(key)
    result.push({
      id: key,
      label: spec.peer_authority,
      detail: `${spec.remote_resource_kind}:${spec.remote_resource_ref}`,
      peerAuthority: spec.peer_authority,
      remoteResourceKind: spec.remote_resource_kind,
      remoteResourceRef: spec.remote_resource_ref,
    })
  }
  return result
}
