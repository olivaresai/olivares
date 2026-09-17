// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '@/lib/api/errors'
import './i18n'

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
      ...(real.agentOpsApi as object),
      profileLaunchReadiness: vi.fn(),
    },
  }
})

import { agentOpsApi } from './api'
import { fixtureReadiness } from './launch-readiness.fixture'
import { PROFILE_CHANGED_CODE } from './launch-readiness'
import {
  LaunchReadinessPanel,
  useProfileLaunchReadiness,
} from './launch-readiness-panel'

function Subject({ enabled = true }: { enabled?: boolean }) {
  const q = useProfileLaunchReadiness({
    enabled,
    profileRef: 'ppf_a',
    transport: 'stream-json',
    isolation: 'native',
  })
  return (
    <LaunchReadinessPanel
      query={q}
      profileRef="ppf_a"
      transport="stream-json"
      isolation="native"
    />
  )
}

function wrap(ui: ReactNode = <Subject />) {
  const qc = new QueryClient({
    defaultOptions: {
      queries: { retry: 3, retryDelay: 1 },
      mutations: { retry: 3, retryDelay: 1 },
    },
  })
  const view = render(
    <QueryClientProvider client={qc}>{ui}</QueryClientProvider>,
  )
  return { qc, ...view }
}

beforeEach(() => {
  vi.clearAllMocks()
  auth.can = (p: string) => p !== 'deny'
  vi.mocked(agentOpsApi.profileLaunchReadiness).mockResolvedValue(
    fixtureReadiness(),
  )
})

describe('LaunchReadinessPanel — current observation', () => {
  it('does not display the old ready observation after a real query refetch receives 403', async () => {
    vi.mocked(agentOpsApi.profileLaunchReadiness)
      .mockResolvedValueOnce(fixtureReadiness())
      .mockRejectedValueOnce(new ApiError(403, 'forbidden', 'revoked'))
    const { qc } = wrap()
    expect(
      await screen.findByText('Local requirements checked'),
    ).toBeInTheDocument()
    await act(async () => {
      await qc.refetchQueries({ type: 'active' })
    })
    await waitFor(() =>
      expect(agentOpsApi.profileLaunchReadiness).toHaveBeenCalledTimes(2),
    )
    expect(qc.getQueryCache().getAll()[0].state.status).toBe('error')
    expect(
      screen.getByText(
        /Your role cannot read this profile’s requirements \(sessions:profile:read\)/,
      ),
    ).toBeInTheDocument()
    expect(
      screen.queryByText('Local requirements checked'),
    ).not.toBeInTheDocument()
    await new Promise((r) => setTimeout(r, 40))
    expect(agentOpsApi.profileLaunchReadiness).toHaveBeenCalledTimes(2)
  })

  it('hides the ready body while a same-tuple refetch is pending', async () => {
    let release: (v: ReturnType<typeof fixtureReadiness>) => void = () => {}
    const held = new Promise<ReturnType<typeof fixtureReadiness>>((resolve) => {
      release = resolve
    })
    vi.mocked(agentOpsApi.profileLaunchReadiness)
      .mockResolvedValueOnce(fixtureReadiness())
      .mockReturnValueOnce(held)
    const { qc } = wrap()
    expect(
      await screen.findByText('Local requirements checked'),
    ).toBeInTheDocument()
    let refetching: Promise<unknown> | undefined
    await act(async () => {
      refetching = qc.refetchQueries({ type: 'active' })
    })
    expect(
      await screen.findByText(
        /Checking the selected profile’s local requirements/,
      ),
    ).toBeInTheDocument()
    expect(
      screen.queryByText('Local requirements checked'),
    ).not.toBeInTheDocument()
    release(fixtureReadiness())
    await act(async () => {
      await refetching
    })
    expect(
      await screen.findByText('Local requirements checked'),
    ).toBeInTheDocument()
  })

  it.each([
    [401, 'unauthenticated', /The session is no longer authenticated/],
    [403, 'forbidden', /Your role cannot read this profile’s requirements/],
    [404, 'not_found', /This profile is not available in the current tenant/],
  ] as const)(
    'discards ready after a %s refetch',
    async (status, code, notice) => {
      vi.mocked(agentOpsApi.profileLaunchReadiness)
        .mockResolvedValueOnce(fixtureReadiness())
        .mockRejectedValueOnce(new ApiError(status, code, code))
      const { qc } = wrap()
      expect(
        await screen.findByText('Local requirements checked'),
      ).toBeInTheDocument()
      await act(async () => {
        await qc.refetchQueries({ type: 'active' })
      })
      expect(await screen.findByText(notice)).toBeInTheDocument()
      expect(
        screen.queryByText('Local requirements checked'),
      ).not.toBeInTheDocument()
      expect(screen.queryByText('Could not be checked')).not.toBeInTheDocument()
    },
  )

  it('shows typed 409 as an explicit reread, then recovers without keeping the old body', async () => {
    vi.mocked(agentOpsApi.profileLaunchReadiness)
      .mockResolvedValueOnce(fixtureReadiness())
      .mockRejectedValueOnce(
        new ApiError(409, PROFILE_CHANGED_CODE, 'profile changed'),
      )
      .mockResolvedValueOnce(fixtureReadiness())
    const { qc } = wrap()
    expect(
      await screen.findByText('Local requirements checked'),
    ).toBeInTheDocument()
    await act(async () => {
      await qc.refetchQueries({ type: 'active' })
    })
    expect(
      await screen.findByText(/The profile changed during inspection/),
    ).toBeInTheDocument()
    expect(
      screen.queryByText('Local requirements checked'),
    ).not.toBeInTheDocument()
    await userEvent
      .setup()
      .click(screen.getByRole('button', { name: 'Read requirements again' }))
    expect(
      await screen.findByText('Local requirements checked'),
    ).toBeInTheDocument()
    expect(agentOpsApi.profileLaunchReadiness).toHaveBeenCalledTimes(3)
  })

  it('paints a successful unknown inspection, not the unavailable notice', async () => {
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
    expect(await screen.findByText('Could not be checked')).toBeInTheDocument()
    expect(
      screen.getByText(
        /The wired runner cannot be inspected without launching/,
      ),
    ).toBeInTheDocument()
    expect(
      screen.queryByText(/Requirements could not be read/),
    ).not.toBeInTheDocument()
    expect(
      screen.queryByText('Local requirements checked'),
    ).not.toBeInTheDocument()
  })

  it('does not treat a 503 refetch as a successful unknown observation', async () => {
    vi.mocked(agentOpsApi.profileLaunchReadiness)
      .mockResolvedValueOnce(fixtureReadiness())
      .mockRejectedValueOnce(new ApiError(503, 'unavailable', 'engine down'))
    const { qc } = wrap()
    expect(
      await screen.findByText('Local requirements checked'),
    ).toBeInTheDocument()
    await act(async () => {
      await qc.refetchQueries({ type: 'active' })
    })
    expect(
      await screen.findByText(/Requirements could not be read/),
    ).toBeInTheDocument()
    expect(screen.queryByText('Could not be checked')).not.toBeInTheDocument()
    expect(
      screen.queryByText('Local requirements checked'),
    ).not.toBeInTheDocument()
  })

  it('drops the ready body when sessions:profile:read is lost without a new GET', async () => {
    const { qc, rerender } = wrap()
    expect(
      await screen.findByText('Local requirements checked'),
    ).toBeInTheDocument()
    expect(agentOpsApi.profileLaunchReadiness).toHaveBeenCalledTimes(1)
    auth.can = () => false
    rerender(
      <QueryClientProvider client={qc}>
        <Subject />
      </QueryClientProvider>,
    )
    await waitFor(() =>
      expect(
        screen.queryByText('Local requirements checked'),
      ).not.toBeInTheDocument(),
    )
    expect(agentOpsApi.profileLaunchReadiness).toHaveBeenCalledTimes(1)
  })
})
