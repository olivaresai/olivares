// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The quick New session form launches the way the dialog does: the authority it was
// started under travels to every dispatch, so a first message whose authority retires
// while it is retried is not sent under the next sign-in, organization or credential.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    can: () => true,
    isSuperadmin: true,
    activeTenant: 'tnt-a',
  }),
}))
const navigate = vi.hoisted(() => vi.fn())
vi.mock('@tanstack/react-router', () => ({
  useNavigate: () => navigate,
  useRouterState: () => '',
}))
vi.mock('./api', async (orig) => ({
  ...((await orig()) as Record<string, unknown>),
  signInApi: {
    status: (driver: string) =>
      Promise.resolve({ driver, installed: true, signed_in: true }),
  },
}))

import { agentOpsApi } from '@/features/agentops/api'
import { readinessOf } from '@/features/first-hour/readiness.fixture'
import type { RunDTO } from '@/features/agentops/types'
import { providersApi } from '@/features/providers/api'
import { useSentTurns } from '@/features/sessions/sent-turns'
import { http } from '@/lib/api'
import { __resetRefreshState, configureApiClient } from '@/lib/api/client'
import { queryKeys } from '@/lib/api/query'
import { useSessionStore } from '@/stores/session'
import { useTenantStore } from '@/stores/tenant'
import { StartSessionForm } from './first-hour'

const reply = (status: number) =>
  new Response(
    JSON.stringify(
      status === 202
        ? { accepted: true }
        : { error: { code: 'conflict', message: 'Child starting' } },
    ),
    { status, headers: { 'Content-Type': 'application/json' } },
  )
const inputs = (fetch: ReturnType<typeof vi.fn>) =>
  fetch.mock.calls.filter(([url]) => String(url).includes('/input'))

let qc: QueryClient

function startWithRetry() {
  const fetch = vi
    .fn()
    .mockResolvedValueOnce(reply(409))
    .mockImplementation(async () => reply(202))
  vi.stubGlobal('fetch', fetch)
  qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  qc.setQueryData(queryKeys.whoami, { user_id: 'operator-a' })
  render(
    <QueryClientProvider client={qc}>
      <StartSessionForm />
    </QueryClientProvider>,
  )
  return fetch
}

async function submit(prompt = 'original prompt') {
  const user = userEvent.setup()
  if (prompt)
    await user.type(
      await screen.findByRole('textbox', { name: 'First message (optional)' }),
      prompt,
    )
  await user.click(await screen.findByRole('button', { name: 'Start' }))
}

/** Signs out, rotates the credential, switches organization or person. */
function retire(change: string) {
  if (change === 'credential')
    useSessionStore.setState({ credentialGeneration: 2 })
  if (change === 'tenant') useTenantStore.setState({ activeTenant: 'tnt-b' })
  if (change === 'principal')
    qc.setQueryData(queryKeys.whoami, { user_id: 'operator-b' })
}

beforeEach(() => {
  vi.restoreAllMocks()
  navigate.mockReset()
  useTenantStore.setState({ activeTenant: 'tnt-a' })
  useSessionStore.setState({ credentialGeneration: 1 })
  useSentTurns.setState({ byRun: {} })
  configureApiClient({
    getToken: () => null,
    getTenant: () => useTenantStore.getState().activeTenant,
    getExpiresAt: undefined,
    refreshSession: undefined,
    onUnauthorized: () => {},
  })
  vi.spyOn(agentOpsApi, 'listRuns').mockResolvedValue({
    items: [],
    has_more: false,
  } as Awaited<ReturnType<typeof agentOpsApi.listRuns>>)
  vi.spyOn(agentOpsApi, 'listWorkspaces').mockResolvedValue({
    items: [],
    has_more: false,
  } as Awaited<ReturnType<typeof agentOpsApi.listWorkspaces>>)
  vi.spyOn(providersApi, 'list').mockResolvedValue({
    items: [],
    has_more: false,
  } as Awaited<ReturnType<typeof providersApi.list>>)
  vi.spyOn(agentOpsApi, 'toolsReadiness').mockResolvedValue(
    readinessOf({ claude: 'own_login' }),
  )
  vi.spyOn(agentOpsApi, 'resolveProfile').mockResolvedValue({
    profile: { profile_ref: 'prof_1' },
  } as Awaited<ReturnType<typeof agentOpsApi.resolveProfile>>)
  vi.spyOn(agentOpsApi, 'createRun').mockResolvedValue({
    run_ref: 'run_new',
    provider_driver: 'claude',
    state: 'pending',
  } as RunDTO)
  // The quick form's default permission requires this built-in template.
  vi.spyOn(http, 'get').mockResolvedValue({
    items: [{ id: 'tpl-ec', name: 'Edits and commands', builtin: true }],
  })
})

afterEach(() => {
  vi.unstubAllGlobals()
  configureApiClient({
    getToken: () => null,
    getTenant: () => null,
    getExpiresAt: undefined,
    refreshSession: undefined,
  })
  __resetRefreshState()
})

describe('the quick New session launch authority', () => {
  it.each(['credential', 'tenant', 'principal'])(
    'stops first-message retries after the %s changes',
    async (change) => {
      const fetch = startWithRetry()
      await submit()
      await waitFor(() => expect(inputs(fetch)).toHaveLength(1))
      const init = inputs(fetch)[0][1] as RequestInit
      expect(new Headers(init.headers).get('X-Olivares-Tenant')).toBe('tnt-a')
      retire(change)
      // Longer than two retry intervals (750 ms each).
      await new Promise((r) => setTimeout(r, 1_800))
      expect(inputs(fetch)).toHaveLength(1)
      expect(useSentTurns.getState().byRun).toEqual({})
      expect(navigate).not.toHaveBeenCalled()
    },
  )

  // A privileged run must not be created under the next sign-in or organization.
  it('creates no run when its authority retires while the profile is resolved', async () => {
    startWithRetry()
    let resolved!: () => void
    vi.spyOn(agentOpsApi, 'resolveProfile').mockImplementation(
      () =>
        new Promise((r) => {
          resolved = () =>
            r({ profile: { profile_ref: 'prof_1' } } as Awaited<
              ReturnType<typeof agentOpsApi.resolveProfile>
            >)
        }),
    )
    await submit()
    await waitFor(() => expect(agentOpsApi.resolveProfile).toHaveBeenCalled())
    retire('credential')
    resolved()
    await new Promise((r) => setTimeout(r, 100))
    expect(agentOpsApi.createRun).not.toHaveBeenCalled()
    expect(navigate).not.toHaveBeenCalled()
  })

  it('does not open a session whose authority retired while it launched', async () => {
    const fetch = startWithRetry()
    vi.spyOn(agentOpsApi, 'createRun').mockImplementation(async () => {
      retire('tenant')
      return { run_ref: 'run_new', provider_driver: 'claude' } as RunDTO
    })
    await submit('')
    await waitFor(() => expect(agentOpsApi.createRun).toHaveBeenCalledTimes(1))
    await new Promise((r) => setTimeout(r, 100))
    expect(inputs(fetch)).toHaveLength(0)
    expect(navigate).not.toHaveBeenCalled()
  })

  it('sends the first message and opens the session while its authority holds', async () => {
    const fetch = startWithRetry()
    await submit()
    await waitFor(() =>
      expect(agentOpsApi.createRun).toHaveBeenCalledWith(
        expect.objectContaining({ template_id: 'tpl-ec' }),
        expect.objectContaining({ tenant: 'tnt-a' }),
      ),
    )
    await waitFor(() => expect(navigate).toHaveBeenCalledTimes(1), {
      timeout: 3_000,
    })
    expect(inputs(fetch)).toHaveLength(2)
    expect(useSentTurns.getState().byRun.run_new).toMatchObject([
      { text: 'original prompt' },
    ])
  })
})
