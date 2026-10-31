// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// A folder's default permission at the TRANSPORT boundary: the path, the method
// and the body the engine's PATCH takes, through the real client.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { __resetRefreshState, configureApiClient } from '@/lib/api/client'
import { agentOpsApi } from './api'

interface Sent {
  method: string
  path: string
  body: string | null
}
const sent: Sent[] = []

beforeEach(() => {
  sent.length = 0
  __resetRefreshState()
  configureApiClient({
    getToken: () => 'tok',
    getTenant: () => 't1',
    onUnauthorized: () => {},
  })
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      sent.push({
        method: (init?.method ?? 'GET').toUpperCase(),
        path: new URL(String(input), 'http://console.test').pathname,
        body: typeof init?.body === 'string' ? init.body : null,
      })
      return new Response(JSON.stringify({ workspace_ref: 'ws-1' }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })
    }),
  )
})

afterEach(() => vi.unstubAllGlobals())

describe('agentOpsApi folder default permission', () => {
  it('patches the folder with the permission name, and an empty name takes it away', async () => {
    await agentOpsApi.setWorkspaceDefaultPermission('ws-1', 'supervised')
    await agentOpsApi.setWorkspaceDefaultPermission('ws-1', '')
    expect(sent).toEqual([
      {
        method: 'PATCH',
        path: '/v1/m/sessions/workspaces/ws-1',
        body: JSON.stringify({ default_permission: 'supervised' }),
      },
      {
        method: 'PATCH',
        path: '/v1/m/sessions/workspaces/ws-1',
        body: JSON.stringify({ default_permission: '' }),
      },
    ])
  })
})
