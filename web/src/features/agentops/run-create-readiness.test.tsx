// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '@/lib/api/errors'

const auth = vi.hoisted(() => ({
  can: (p: string) => p !== 'deny',
}))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    activeTenant: 't1',
    can: (p: string) => auth.can(p),
    principal: { user_id: 'u1', aal: 1 },
  }),
}))
vi.mock('./api', async (orig) => {
  const real = (await orig()) as Record<string, unknown>
  return {
    ...real,
    agentOpsApi: {
      createRun: vi.fn(),
      listWorkspaces: vi.fn(),
      listProfiles: vi.fn(),
      profileLaunchReadiness: vi.fn(),
    },
  }
})
vi.mock('@/features/workspace-templates/api', () => ({
  templatesApi: { list: vi.fn(), apply: vi.fn() },
  templatesKeys: {
    list: (t: string | null, p?: unknown) => ['tpl', t, 'list', p ?? null],
    detail: (t: string | null, id: string) => ['tpl', t, 'detail', id],
  },
}))
vi.mock('@/components/ui/toaster', () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}))

import { templatesApi } from '@/features/workspace-templates/api'
import { agentOpsApi } from './api'
import { fixtureReadiness } from './launch-readiness.fixture'
import { PROFILE_CHANGED_CODE } from './launch-readiness'
import { RunCreateDialog } from './run-create-dialog'
import type { ProviderProfileDTO } from './types'

const homeA: ProviderProfileDTO = {
  profile_ref: 'ppf_a',
  driver: 'claude',
  environment_ref: 'xenv_1',
  display_name: 'Home A',
  state: 'active',
  local_environment: true,
  operable: true,
}
const homeB: ProviderProfileDTO = {
  profile_ref: 'ppf_b',
  driver: 'claude',
  environment_ref: 'xenv_1',
  display_name: 'Home B',
  state: 'active',
  local_environment: true,
  operable: true,
}

function wrap() {
  const qc = new QueryClient({
    defaultOptions: {
      queries: { retry: 3, retryDelay: 1 },
      mutations: { retry: 3, retryDelay: 1 },
    },
  })
  const view = render(
    <QueryClientProvider client={qc}>
      <RunCreateDialog open onOpenChange={vi.fn()} />
    </QueryClientProvider>,
  )
  return { qc, ...view }
}

beforeEach(() => {
  vi.clearAllMocks()
  auth.can = (p: string) => p !== 'deny'
  vi.mocked(agentOpsApi.listWorkspaces).mockResolvedValue({
    items: [],
    has_more: false,
  })
  vi.mocked(agentOpsApi.listProfiles).mockResolvedValue({
    items: [homeA, homeB],
    has_more: false,
  })
  vi.mocked(templatesApi.list).mockResolvedValue({ items: [], has_more: false })
  vi.mocked(agentOpsApi.createRun).mockResolvedValue({} as never)
  vi.mocked(agentOpsApi.profileLaunchReadiness).mockImplementation(
    async (ref: string) => fixtureReadiness({ profile_ref: ref }),
  )
})

async function choose(name: RegExp) {
  const user = userEvent.setup()
  await user.click(await screen.findByLabelText('Provider profile'))
  await user.click(await screen.findByRole('option', { name }))
  return user
}

describe('RunCreateDialog — selected-profile launch readiness', () => {
  it('does not GET readiness until a profile is selected', async () => {
    wrap()
    await waitFor(() => expect(agentOpsApi.listProfiles).toHaveBeenCalled())
    expect(agentOpsApi.profileLaunchReadiness).not.toHaveBeenCalled()
    expect(
      screen.getByRole('button', { name: /request launch/i }),
    ).toBeDisabled()
  })

  it('queries only the selected tuple after the choice, and a ready observation permits the request', async () => {
    wrap()
    await choose(/Home A/)
    await waitFor(() =>
      expect(agentOpsApi.profileLaunchReadiness).toHaveBeenCalledWith(
        'ppf_a',
        { transport: 'stream-json', isolation: 'native' },
        expect.anything(),
      ),
    )
    expect(
      await screen.findByText('Local requirements checked'),
    ).toBeInTheDocument()
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: /request launch/i }))
    await waitFor(() => expect(agentOpsApi.createRun).toHaveBeenCalledOnce())
    expect(vi.mocked(agentOpsApi.createRun).mock.calls[0][0]).toMatchObject({
      provider_profile_ref: 'ppf_a',
    })
  })

  it('keeps the request path for unknown inspection, without calling it ready', async () => {
    vi.mocked(agentOpsApi.profileLaunchReadiness).mockResolvedValue(
      fixtureReadiness({
        configuration_state: 'unknown',
        checks: fixtureReadiness().checks.map((c) =>
          c.check === 'runner'
            ? {
                check: 'runner',
                state: 'unknown',
                code: 'runner_inspection_unavailable',
                remediation: 'retry_inspection',
              }
            : c,
        ),
      }),
    )
    wrap()
    await choose(/Home A/)
    expect(await screen.findByText('Could not be checked')).toBeInTheDocument()
    expect(screen.getByText(/Inspection is incomplete/)).toBeInTheDocument()
    expect(screen.queryByText('Local requirements checked')).toBeNull()
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: /request launch/i }))
    await waitFor(() => expect(agentOpsApi.createRun).toHaveBeenCalledOnce())
  })

  it('blocks not_configured and never POSTs', async () => {
    vi.mocked(agentOpsApi.profileLaunchReadiness).mockResolvedValue(
      fixtureReadiness({
        configuration_state: 'not_configured',
        checks: fixtureReadiness().checks.map((c) =>
          c.check === 'program'
            ? {
                check: 'program',
                state: 'not_configured',
                code: 'program_missing',
                remediation: 'configure_provider_program',
              }
            : c,
        ),
      }),
    )
    wrap()
    await choose(/Home A/)
    expect(await screen.findByText('Missing configuration')).toBeInTheDocument()
    expect(
      screen.getByRole('button', { name: /request launch/i }),
    ).toBeDisabled()
    expect(agentOpsApi.createRun).not.toHaveBeenCalled()
  })

  it('discards a late reply from a previous profile', async () => {
    let releaseA: (v: ReturnType<typeof fixtureReadiness>) => void = () => {}
    const first = new Promise<ReturnType<typeof fixtureReadiness>>(
      (resolve) => {
        releaseA = resolve
      },
    )
    vi.mocked(agentOpsApi.profileLaunchReadiness)
      .mockReturnValueOnce(first)
      .mockResolvedValueOnce(
        fixtureReadiness({
          profile_ref: 'ppf_b',
          configuration_state: 'not_configured',
        }),
      )
    wrap()
    const user = await choose(/Home A/)
    await user.click(screen.getByLabelText('Provider profile'))
    await user.click(await screen.findByRole('option', { name: /Home B/ }))
    expect(await screen.findByText('Missing configuration')).toBeInTheDocument()
    releaseA(
      fixtureReadiness({
        profile_ref: 'ppf_a',
        configuration_state: 'ready',
      }),
    )
    await waitFor(() =>
      expect(screen.queryByText('Local requirements checked')).toBeNull(),
    )
    expect(screen.getByText('Missing configuration')).toBeInTheDocument()
    expect(
      screen.getByRole('button', { name: /request launch/i }),
    ).toBeDisabled()
    expect(agentOpsApi.createRun).not.toHaveBeenCalled()
  })

  it('handles typed 409 with an explicit reread, not a POST retry', async () => {
    vi.mocked(agentOpsApi.profileLaunchReadiness)
      .mockRejectedValueOnce(
        new ApiError(
          409,
          PROFILE_CHANGED_CODE,
          'profile changed during inspection',
        ),
      )
      .mockResolvedValueOnce(fixtureReadiness())
    wrap()
    await choose(/Home A/)
    expect(
      await screen.findByText(/The profile changed during inspection/),
    ).toBeInTheDocument()
    expect(agentOpsApi.createRun).not.toHaveBeenCalled()
    const user = userEvent.setup()
    await user.click(
      screen.getByRole('button', { name: 'Read requirements again' }),
    )
    expect(
      await screen.findByText('Local requirements checked'),
    ).toBeInTheDocument()
    expect(agentOpsApi.createRun).not.toHaveBeenCalled()
    expect(agentOpsApi.profileLaunchReadiness).toHaveBeenCalledTimes(2)
  })

  it('shows an inline POST failure and keeps the profile choice', async () => {
    vi.mocked(agentOpsApi.createRun).mockRejectedValueOnce(
      new ApiError(503, 'unavailable', 'credential source is not wired'),
    )
    wrap()
    const user = await choose(/Home A/)
    expect(
      await screen.findByText('Local requirements checked'),
    ).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: /request launch/i }))
    expect(
      await screen.findByText(/The launch request was refused/),
    ).toBeInTheDocument()
    expect(
      screen.getByText('credential source is not wired'),
    ).toBeInTheDocument()
    expect(screen.getByLabelText('Provider profile')).toHaveTextContent(
      'Home A',
    )
  })

  it('blocks the request while a ready observation is refetching', async () => {
    let release: (v: ReturnType<typeof fixtureReadiness>) => void = () => {}
    const held = new Promise<ReturnType<typeof fixtureReadiness>>((resolve) => {
      release = resolve
    })
    vi.mocked(agentOpsApi.profileLaunchReadiness)
      .mockResolvedValueOnce(fixtureReadiness())
      .mockReturnValueOnce(held)
    const { qc } = wrap()
    await choose(/Home A/)
    expect(
      await screen.findByText('Local requirements checked'),
    ).toBeInTheDocument()
    expect(
      screen.getByRole('button', { name: /request launch/i }),
    ).toBeEnabled()
    let refetching: Promise<unknown> | undefined
    await act(async () => {
      refetching = qc.refetchQueries({ type: 'active' })
    })
    expect(
      await screen.findByText(
        /Checking the selected profile’s local requirements/,
      ),
    ).toBeInTheDocument()
    expect(screen.queryByText('Local requirements checked')).toBeNull()
    expect(
      screen.getByRole('button', { name: /request launch/i }),
    ).toBeDisabled()
    expect(agentOpsApi.createRun).not.toHaveBeenCalled()
    release(fixtureReadiness())
    await act(async () => {
      await refetching
    })
    expect(
      await screen.findByText('Local requirements checked'),
    ).toBeInTheDocument()
    expect(
      screen.getByRole('button', { name: /request launch/i }),
    ).toBeEnabled()
  })

  it.each([
    [401, 'unauthenticated', /The session is no longer authenticated/],
    [403, 'forbidden', /Your role cannot read this profile’s requirements/],
    [404, 'not_found', /This profile is not available in the current tenant/],
  ] as const)(
    'blocks the request after a ready observation refetches %s',
    async (status, code, notice) => {
      vi.mocked(agentOpsApi.profileLaunchReadiness)
        .mockResolvedValueOnce(fixtureReadiness())
        .mockRejectedValueOnce(new ApiError(status, code, code))
      const { qc } = wrap()
      await choose(/Home A/)
      expect(
        await screen.findByText('Local requirements checked'),
      ).toBeInTheDocument()
      await act(async () => {
        await qc.refetchQueries({ type: 'active' })
      })
      expect(await screen.findByText(notice)).toBeInTheDocument()
      expect(screen.queryByText('Local requirements checked')).toBeNull()
      expect(
        screen.getByRole('button', { name: /request launch/i }),
      ).toBeDisabled()
      expect(agentOpsApi.createRun).not.toHaveBeenCalled()
    },
  )

  it('blocks the request after a ready observation refetches typed 409', async () => {
    vi.mocked(agentOpsApi.profileLaunchReadiness)
      .mockResolvedValueOnce(fixtureReadiness())
      .mockRejectedValueOnce(
        new ApiError(
          409,
          PROFILE_CHANGED_CODE,
          'profile changed during inspection',
        ),
      )
    const { qc } = wrap()
    await choose(/Home A/)
    expect(
      await screen.findByText('Local requirements checked'),
    ).toBeInTheDocument()
    await act(async () => {
      await qc.refetchQueries({ type: 'active' })
    })
    expect(
      await screen.findByText(/The profile changed during inspection/),
    ).toBeInTheDocument()
    expect(screen.queryByText('Local requirements checked')).toBeNull()
    expect(
      screen.getByRole('button', { name: /request launch/i }),
    ).toBeDisabled()
    expect(agentOpsApi.createRun).not.toHaveBeenCalled()
  })

  it('does not treat a 503 refetch as unknown, and does not POST', async () => {
    vi.mocked(agentOpsApi.profileLaunchReadiness)
      .mockResolvedValueOnce(fixtureReadiness())
      .mockRejectedValueOnce(new ApiError(503, 'unavailable', 'engine down'))
    const { qc } = wrap()
    await choose(/Home A/)
    expect(
      await screen.findByText('Local requirements checked'),
    ).toBeInTheDocument()
    await act(async () => {
      await qc.refetchQueries({ type: 'active' })
    })
    expect(
      await screen.findByText(/Requirements could not be read/),
    ).toBeInTheDocument()
    expect(screen.queryByText('Could not be checked')).toBeNull()
    expect(screen.queryByText(/Inspection is incomplete/)).toBeNull()
    expect(
      screen.getByRole('button', { name: /request launch/i }),
    ).toBeDisabled()
    expect(agentOpsApi.createRun).not.toHaveBeenCalled()
  })

  it('does not automatically retry a refused POST', async () => {
    vi.mocked(agentOpsApi.createRun).mockRejectedValue(
      new ApiError(503, 'unavailable', 'credential source is not wired'),
    )
    wrap()
    const user = await choose(/Home A/)
    expect(
      await screen.findByText('Local requirements checked'),
    ).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: /request launch/i }))
    expect(
      await screen.findByText(/The launch request was refused/),
    ).toBeInTheDocument()
    await new Promise((r) => setTimeout(r, 80))
    expect(agentOpsApi.createRun).toHaveBeenCalledOnce()
  })

  it('lets an ordinary reader see requirements but not request a launch', async () => {
    auth.can = (p: string) =>
      p === 'sessions:profile:read' || p === 'sessions:template:read'
    wrap()
    await choose(/Home A/)
    expect(
      await screen.findByText('Local requirements checked'),
    ).toBeInTheDocument()
    expect(
      screen.getByText(/requesting a launch needs sessions:run:write/),
    ).toBeInTheDocument()
    expect(
      screen.getByRole('button', { name: /request launch/i }),
    ).toBeDisabled()
    expect(agentOpsApi.createRun).not.toHaveBeenCalled()
  })
})
