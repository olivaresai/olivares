// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { afterEach, describe, expect, it, vi } from 'vitest'
import { consoleApi } from '@/features/console/api'
import { modelsApi } from '@/features/models/api'
import * as workApi from '@/features/work/api'
import {
  listProtocolLocalResourceCatalog,
  protocolEndpointOptions,
} from './catalog'
import type { ProtocolBindingSpec } from './types'

const TENANT_REQUEST = { tenant: 'tenant-1' } as const

afterEach(() => vi.restoreAllMocks())

describe('protocol binding resource catalogs', () => {
  it('filters the authorized WorkItem response to the selected workspace', async () => {
    vi.spyOn(workApi, 'listWorkItems').mockResolvedValue({
      items: [
        {
          id: 'work-1',
          workspace_id: 'workspace-1',
          title: 'Governed work',
          work_kind: 'remote_task',
          status: 'ready',
        },
        {
          id: 'work-other',
          workspace_id: 'workspace-2',
          title: 'Other workspace',
          work_kind: 'remote_task',
          status: 'ready',
        },
      ],
      has_more: false,
    } as never)

    await expect(
      listProtocolLocalResourceCatalog(
        'work_item',
        'workspace-1',
        TENANT_REQUEST,
      ),
    ).resolves.toEqual({
      available: true,
      options: [
        {
          id: 'work-1',
          label: 'Governed work',
          detail: 'remote_task · ready',
        },
      ],
      hasMore: false,
    })
    expect(workApi.listWorkItems).toHaveBeenCalledWith(
      { limit: 200 },
      TENANT_REQUEST,
      undefined,
    )
  })

  it('names an unavailable channel list instead of inventing catalog rows', async () => {
    await expect(
      listProtocolLocalResourceCatalog(
        'channel',
        'workspace-1',
        TENANT_REQUEST,
      ),
    ).resolves.toEqual({ available: false, options: [], hasMore: false })
  })

  it('uses the canonical identity selector required by the agent resolver', async () => {
    vi.spyOn(consoleApi, 'listAgents').mockResolvedValue({
      items: [
        {
          id: 'agent-1',
          identity_id: 'identity-1',
          name: 'Reports agent',
          kind: 'a2a',
          status: 'active',
        },
        {
          id: 'agent-without-identity',
          name: 'Unbound agent',
          kind: 'local',
          status: 'active',
        },
      ],
      has_more: false,
    } as never)

    const catalog = await listProtocolLocalResourceCatalog(
      'agent',
      'workspace-1',
      TENANT_REQUEST,
    )
    expect(consoleApi.listAgents).toHaveBeenCalledWith(
      {
        workspace_id: 'workspace-1',
        limit: 200,
      },
      TENANT_REQUEST,
    )
    expect(catalog.options).toEqual([
      {
        id: 'identity-1',
        label: 'Reports agent',
        detail: 'a2a · active',
      },
    ])
  })

  it('pins the tenant while reading the model catalog', async () => {
    vi.spyOn(modelsApi, 'models').mockResolvedValue({
      items: [
        {
          id: 'model-1',
          name: 'Governed model',
          provider: 'openai',
          family: 'gpt',
          status: 'active',
        },
      ],
      has_more: false,
    } as never)

    const catalog = await listProtocolLocalResourceCatalog(
      'model',
      'workspace-1',
      TENANT_REQUEST,
    )

    expect(modelsApi.models).toHaveBeenCalledWith(TENANT_REQUEST)
    expect(catalog.options).toEqual([
      {
        id: 'model-1',
        label: 'Governed model',
        detail: 'openai · gpt · active',
      },
    ])
  })

  it('deduplicates known peers only from visible active specification data', () => {
    const spec = {
      protocol: 'a2a',
      peer_authority: 'peer.example',
      remote_resource_kind: 'agent',
      remote_resource_ref: 'agent:reports',
    } as ProtocolBindingSpec
    expect(protocolEndpointOptions([spec, { ...spec }], 'a2a')).toEqual([
      {
        id: 'peer.example\u0000agent\u0000agent:reports',
        label: 'peer.example',
        detail: 'agent:agent:reports',
        peerAuthority: 'peer.example',
        remoteResourceKind: 'agent',
        remoteResourceRef: 'agent:reports',
      },
    ])
    expect(protocolEndpointOptions([spec], 'mcp')).toEqual([])
  })
})
