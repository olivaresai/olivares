// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Moving a session and handing it off at the TRANSPORT boundary: the path, the method
// and the body the engine's routes take, through the real client rather than a mock of
// it, so a wrong route or body key cannot hide behind a mocked API.
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

describe('agentOpsApi.move and handoff', () => {
  it('moves with only the profile reference in the body', async () => {
    await agentOpsApi.move('run_1', 'ppf_b')
    expect(sent).toEqual([
      {
        method: 'POST',
        path: '/v1/m/sessions/runs/run_1/move',
        body: JSON.stringify({ provider_profile_ref: 'ppf_b' }),
      },
    ])
  })

  it('reads the handoff preview with a GET and no body', async () => {
    await agentOpsApi.handoffPreview('run_1')
    expect(sent).toEqual([
      { method: 'GET', path: '/v1/m/sessions/runs/run_1/handoff', body: null },
    ])
  })

  it('starts a handoff with the reference and the reviewed text', async () => {
    await agentOpsApi.handoff('run_1', {
      provider_profile_ref: 'ppf_codex',
      summary: 'Reviewed.',
    })
    expect(sent).toHaveLength(1)
    expect(sent[0].method).toBe('POST')
    expect(sent[0].path).toBe('/v1/m/sessions/runs/run_1/handoff')
    expect(JSON.parse(sent[0].body ?? '{}')).toEqual({
      provider_profile_ref: 'ppf_codex',
      summary: 'Reviewed.',
    })
  })

  it('escapes the run reference in the path', async () => {
    await agentOpsApi.move('a/b', 'ppf_b')
    expect(sent[0].path).toBe('/v1/m/sessions/runs/a%2Fb/move')
  })
})
