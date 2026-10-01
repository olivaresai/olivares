// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
import { beforeEach, expect, it, vi } from 'vitest'
import { configureApiClient } from '@/lib/api/client'
import { useSessionStore } from '@/stores/session'
import { launchRunAsAgent } from './launch-agent-api'
const body = {
  name: 'fixture',
  transport: 'stream-json' as const,
  permission_mode: 'default',
  effort: '',
  model: '',
  workspace_ref: '',
  isolation: 'native' as const,
  env_allow: [],
  provider_profile_ref: 'ppf_fixture',
}
const fetcher = vi.fn(),
  refresh = vi.fn(),
  unauthorized = vi.fn()
const guard = vi.fn()
const response = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })
beforeEach(() => {
  vi.clearAllMocks()
  vi.stubGlobal('fetch', fetcher)
  useSessionStore.setState({ token: 'fixture-human', credentialGeneration: 1 })
  configureApiClient({
    getToken: () => useSessionStore.getState().token,
    getTenant: () => 'tenant-current',
    onUnauthorized: unauthorized,
    refreshSession: refresh,
  })
  fetcher.mockResolvedValueOnce(
    response({
      access_token: 'fixture-agent',
      token_type: 'Bearer',
      issued_token_type: 'urn:ietf:params:oauth:token-type:access_token',
      expires_in: 900,
    }),
  )
  fetcher.mockResolvedValueOnce(
    response({ run_ref: 'run-fixture', agent_ref: 'agent-fixture' }, 201),
  )
})
it('uses the exchanged agent credential once, audience-bound and down-scoped, without changing operator state', async () => {
  const result = await launchRunAsAgent(
    body,
    'agent-fixture',
    'tenant-selected',
    { dispatchGuard: guard },
  )
  expect(result.agent_ref).toBe('agent-fixture')
  expect(fetcher).toHaveBeenCalledTimes(2)
  const exchange = fetcher.mock.calls[0][1]
  const form = new URLSearchParams(exchange.body)
  expect(form.get('requested_actor')).toBe('agent-fixture')
  expect(form.get('scope')).toBe('write')
  expect(form.get('subject_token')).toBe('fixture-human')
  expect(form.get('resource')).toBe(window.location.origin)
  expect(exchange.headers.get('X-Olivares-Tenant')).toBe('tenant-selected')
  const launch = fetcher.mock.calls[1][1]
  expect(launch.headers.get('Authorization')).toBe('Bearer fixture-agent')
  expect(launch.headers.get('X-Olivares-Tenant')).toBe('tenant-selected')
  expect(JSON.parse(launch.body)).toEqual(body)
  expect(useSessionStore.getState().token).toBe('fixture-human')
  expect(refresh).not.toHaveBeenCalled()
  expect(unauthorized).not.toHaveBeenCalled()
})
it('does not launch or retry when the exchange refuses the selected agent', async () => {
  fetcher.mockReset().mockResolvedValue(
    response(
      {
        error: 'agent_blocked',
        error_description: 'Agent is blocked or sponsor does not match.',
      },
      403,
    ),
  )
  await expect(
    launchRunAsAgent(body, 'agent-fixture', 'tenant-selected', {
      dispatchGuard: guard,
    }),
  ).rejects.toThrow('Agent is blocked or sponsor does not match.')
  expect(fetcher).toHaveBeenCalledOnce()
})
it('does not fall back to human authority or renew it after agent launch denial', async () => {
  fetcher
    .mockReset()
    .mockResolvedValueOnce(
      response({
        access_token: 'fixture-agent',
        token_type: 'Bearer',
        issued_token_type: 'urn:ietf:params:oauth:token-type:access_token',
        expires_in: 900,
      }),
    )
    .mockResolvedValueOnce(
      response({ error: { code: 'forbidden', message: 'launch denied' } }, 401),
    )
  await expect(
    launchRunAsAgent(body, 'agent-fixture', 'tenant-selected', {
      dispatchGuard: guard,
    }),
  ).rejects.toThrow('launch denied')
  expect(fetcher).toHaveBeenCalledTimes(2)
  expect(refresh).not.toHaveBeenCalled()
  expect(unauthorized).not.toHaveBeenCalled()
  expect(useSessionStore.getState().token).toBe('fixture-human')
})
it('refuses dispatch when the original human credential changes during exchange', async () => {
  fetcher.mockReset().mockImplementationOnce(async () => {
    useSessionStore.setState({
      token: 'fixture-new-human',
      credentialGeneration: 2,
    })
    return response({
      access_token: 'fixture-agent',
      token_type: 'Bearer',
      issued_token_type: 'urn:ietf:params:oauth:token-type:access_token',
      expires_in: 900,
    })
  })
  await expect(
    launchRunAsAgent(body, 'agent-fixture', 'tenant-selected', {
      dispatchGuard: guard,
    }),
  ).rejects.toThrow('authority lost')
  expect(fetcher).toHaveBeenCalledOnce()
})
it('rechecks the supplied tenant/principal guard after exchange and never sends an obsolete intent', async () => {
  let moved = false
  fetcher.mockReset().mockImplementationOnce(async () => {
    moved = true
    return response({
      access_token: 'fixture-agent',
      token_type: 'Bearer',
      issued_token_type: 'urn:ietf:params:oauth:token-type:access_token',
      expires_in: 900,
    })
  })
  await expect(
    launchRunAsAgent(body, 'agent-fixture', 'tenant-selected', {
      dispatchGuard: () => {
        if (moved) throw Error('context changed')
      },
    }),
  ).rejects.toThrow('context changed')
  expect(fetcher).toHaveBeenCalledOnce()
})
