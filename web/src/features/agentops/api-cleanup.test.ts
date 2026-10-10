// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The release of a session at the TRANSPORT boundary: with no confirmation the request
// is the one the engine has always accepted (a POST with no body), and the person's
// confirmation to discard a worktree is the only thing that adds a body.
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
      return new Response(JSON.stringify({ run_ref: 'run_1' }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })
    }),
  )
})

afterEach(() => vi.unstubAllGlobals())

describe('agentOpsApi.cleanup', () => {
  it('posts no body unless the discard is confirmed', async () => {
    await agentOpsApi.cleanup('run_1')
    await agentOpsApi.cleanup('run_1', false)
    expect(sent).toEqual([
      { method: 'POST', path: '/v1/m/sessions/runs/run_1/cleanup', body: null },
      { method: 'POST', path: '/v1/m/sessions/runs/run_1/cleanup', body: null },
    ])
  })

  it('posts the confirmation when the person gave it', async () => {
    await agentOpsApi.cleanup('run_1', true)
    expect(sent).toHaveLength(1)
    expect(sent[0].method).toBe('POST')
    expect(sent[0].path).toBe('/v1/m/sessions/runs/run_1/cleanup')
    expect(JSON.parse(sent[0].body ?? 'null')).toEqual({
      discard_worktree: true,
    })
  })
})
