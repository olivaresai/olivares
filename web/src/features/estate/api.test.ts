// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { configureApiClient } from '@/lib/api/client'
import { readEstateFamily } from './api'

const scope = { tenant: 'tenant-a', signal: new AbortController().signal }
let requests: { url: string; tenant: string | null }[]

function serve(body: unknown, status = 200) {
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string, init: RequestInit) => {
      requests.push({
        url,
        tenant: new Headers(init.headers).get('X-Olivares-Tenant'),
      })
      return new Response(JSON.stringify(body), {
        status,
        headers: { 'Content-Type': 'application/json' },
      })
    }),
  )
}

beforeEach(() => {
  requests = []
  configureApiClient({
    getToken: () => null,
    getTenant: () => 'wrong-tenant',
    onUnauthorized: () => {},
    refreshSession: undefined,
    getExpiresAt: undefined,
  })
})
afterEach(() => vi.unstubAllGlobals())

describe('configured estate reads', () => {
  it('keeps disconnected folders and pins reads to the requested organization', async () => {
    serve({
      items: [
        {
          workspace_ref: 'folder-one',
          name: 'Disconnected folder',
          state: 'active',
          root_path: '/fixture',
        },
      ],
      has_more: true,
      cursor: 'next-folder',
    })
    const page = await readEstateFamily('folder', scope)
    expect(page.nodes).toEqual([
      {
        kind: 'folder',
        ref: 'folder-one',
        label: 'Disconnected folder',
        status: 'active',
      },
    ])
    expect(page.hasMore).toBe(true)
    expect(page.cursor).toBe('next-folder')
    expect(requests).toEqual([
      { url: '/v1/m/sessions/workspaces?limit=25', tenant: 'tenant-a' },
    ])
  })

  it('omits foreign connector labels and all configuration from the browser projection', async () => {
    serve({
      sources: [
        {
          id: 'c1',
          name: 'Local source',
          tenant: 'tenant-a',
          status: 'disabled',
          config: { token: 'fixture-private-value' },
        },
        {
          id: 'c2',
          name: 'Foreign source',
          tenant: 'tenant-b',
          status: 'running',
        },
      ],
    })
    const page = await readEstateFamily('connector', scope)
    expect(page.nodes).toEqual([
      {
        kind: 'connector',
        ref: 'c1',
        label: 'Local source',
        status: 'disabled',
      },
    ])
    expect(JSON.stringify(page)).not.toContain('fixture-private-value')
    expect(JSON.stringify(page)).not.toContain('Foreign source')
  })

  it('propagates an owner refusal instead of returning an empty verified list', async () => {
    serve({ error: { code: 'forbidden', message: 'Not allowed' } }, 403)
    await expect(readEstateFamily('folder', scope)).rejects.toMatchObject({
      status: 403,
    })
  })

  it('sends nothing for an already retired read', async () => {
    serve({ items: [], has_more: false })
    const controller = new AbortController()
    controller.abort()
    await expect(
      readEstateFamily('folder', {
        tenant: 'tenant-a',
        signal: controller.signal,
      }),
    ).rejects.toBeDefined()
    expect(requests).toEqual([])
  })

  it('uses the native Work cursor and keeps its workspace identity', async () => {
    serve({
      items: [
        {
          id: 'work-one',
          title: 'Prepare model',
          status: 'draft',
          workspace_id: 'workspace-one',
        },
      ],
      has_more: true,
      next_cursor: 'opaque-next',
    })
    const page = await readEstateFamily('work', scope, 'opaque-current')
    expect(page.nodes).toEqual([
      {
        kind: 'work',
        ref: 'work-one',
        label: 'Prepare model',
        status: 'draft',
        workspaceId: 'workspace-one',
      },
    ])
    expect(page.cursor).toBe('opaque-next')
    expect(requests[0]?.url).toBe(
      '/v1/m/sessions/work-items?cursor=opaque-current&limit=25',
    )
  })

  it('never turns a provider conversation ID or hidden peer reference into a graph endpoint', async () => {
    serve({
      items: [
        {
          run_ref: 'run-one',
          name: 'Session one',
          state: 'running',
          authz_workspace_id: 'workspace-one',
          workspace_ref: 'folder-one',
          session_id: 'provider-conversation',
          peers: ['hidden-peer'],
          peers_rule: 'same-template',
        },
      ],
      has_more: false,
    })
    const page = await readEstateFamily('session', scope)
    expect(page.nodes).toEqual([
      {
        kind: 'session',
        ref: 'run-one',
        label: 'Session one',
        status: 'running',
        workspaceId: 'workspace-one',
        folderRef: 'folder-one',
        peerMode: 'same-template',
      },
    ])
    expect(JSON.stringify(page)).not.toContain('provider-conversation')
    expect(JSON.stringify(page)).not.toContain('hidden-peer')
  })

  it('bounds the initial projection and makes client truncation explicit', async () => {
    serve({
      items: Array.from({ length: 30 }, (_, index) => ({
        workspace_ref: `folder-${index}`,
        name: `Folder ${index}`,
        state: 'active',
      })),
      has_more: false,
    })
    const page = await readEstateFamily('folder', scope)
    expect(page.nodes).toHaveLength(25)
    expect(page.hasMore).toBe(true)
    expect(page.cursor).toBeUndefined()
  })
})
