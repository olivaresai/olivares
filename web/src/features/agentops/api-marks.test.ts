// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// A person's snooze and done marks at the TRANSPORT boundary (#2480): the path, the
// method and the body the engine's routes take, through the real client.
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
      return new Response(JSON.stringify({ run_ref: 'run_1', done: false }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })
    }),
  )
})

afterEach(() => vi.unstubAllGlobals())

describe('agentOpsApi snooze and done', () => {
  it('sends each mark to its own route, with the moment as the only body', async () => {
    await agentOpsApi.snooze('run_1', '2030-01-02T09:00:00.000Z')
    await agentOpsApi.unsnooze('run_1')
    await agentOpsApi.markDone('run_1')
    await agentOpsApi.unmarkDone('run_1')
    expect(sent).toEqual([
      {
        method: 'POST',
        path: '/v1/m/sessions/runs/run_1/snooze',
        body: JSON.stringify({ until: '2030-01-02T09:00:00.000Z' }),
      },
      {
        method: 'DELETE',
        path: '/v1/m/sessions/runs/run_1/snooze',
        body: null,
      },
      { method: 'POST', path: '/v1/m/sessions/runs/run_1/done', body: null },
      { method: 'DELETE', path: '/v1/m/sessions/runs/run_1/done', body: null },
    ])
  })
})
