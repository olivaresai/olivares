// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// HU 049 (09b, fresh install): the first page asked the engine's rule which tool a
// session would run on for tools that were not installed; each answer was a 409 the
// browser logs as a console error. Then it gated those asks on sign-in status and
// provider pages (up to ~29 reads). Now it asks the engine once, for every tool, and a
// tool that cannot start is data in that one 200 (ARCH.C3).
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { renderIntel, screen, waitFor } from '@/test/intel'
import { agentOpsApi } from '@/features/agentops/api'
import { providersApi } from '@/features/providers/api'
import { ApiError } from '@/lib/api/errors'
import { useTenantStore } from '@/stores/tenant'
import { signInApi } from './api'
import { useReadyTools } from './first-hour'
import { readinessOf } from './readiness.fixture'

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    isSuperadmin: true,
    can: () => true,
    principal: { user_id: 'u1', aal: 1 },
    activeTenant: 'tnt-a',
  }),
}))

function Probe() {
  const { ready, isLoading } = useReadyTools()
  return <p>{isLoading ? 'deciding' : `ready=[${ready.join(',')}]`}</p>
}

beforeEach(() => {
  useTenantStore.setState({ activeTenant: 'tnt-a' })
  vi.spyOn(signInApi, 'status')
  vi.spyOn(providersApi, 'list')
})
afterEach(() => vi.restoreAllMocks())

describe('how the first page learns which tools can start', () => {
  it('asks the engine once on a fresh install, and nothing else', async () => {
    const readiness = vi
      .spyOn(agentOpsApi, 'toolsReadiness')
      .mockResolvedValue(readinessOf({}))
    renderIntel(
      <>
        <Probe />
        <Probe />
      </>,
    )
    expect(await screen.findAllByText('ready=[]')).toHaveLength(2)
    expect(readiness).toHaveBeenCalledTimes(1)
    expect(signInApi.status).not.toHaveBeenCalled()
    expect(providersApi.list).not.toHaveBeenCalled()
  })

  // 09 IP journey: a mount that asked again put the screens back to loading, which
  // unmounted the form that had mounted the query. A failed first read stays failed.
  it('a failed read says so and does not go back to loading at the next mount', async () => {
    const readiness = vi
      .spyOn(agentOpsApi, 'toolsReadiness')
      .mockRejectedValueOnce(new ApiError(500, 'internal', 'readiness failed'))
      .mockImplementation(() => new Promise(() => {}))
    function Refusal() {
      const { refusal, isLoading } = useReadyTools()
      return <p>{isLoading ? 'deciding' : `claude: ${refusal('claude')}`}</p>
    }
    const { rerender } = renderIntel(<Refusal />)
    expect(
      await screen.findByText('claude: readiness failed'),
    ).toBeInTheDocument()
    rerender(
      <>
        <Refusal />
        <Refusal />
      </>,
    )
    // The new mounts ask again; that read never answers, and nothing waits for it.
    await waitFor(() => expect(readiness).toHaveBeenCalledTimes(2))
    expect(screen.getAllByText(/^claude: /)).toHaveLength(2)
    expect(screen.queryByText('deciding')).toBeNull()
  })

  // HU2-37: the New session model comes from the answer this page already reads.
  it('offers a model from the same answer: no extra request per tool', async () => {
    const readiness = vi.spyOn(agentOpsApi, 'toolsReadiness').mockResolvedValue(
      readinessOf({
        claude: {
          provider: {
            provider_ref: 'prv-0',
            kind: 'anthropic',
            default_model: 'm1',
            models: ['m1'],
          },
        },
      }),
    )
    function Bound() {
      const { boundKey, isLoading } = useReadyTools()
      const key = isLoading ? undefined : boundKey('claude')
      return <p>{key ? `default=${key.record.default_model}` : 'none'}</p>
    }
    renderIntel(<Bound />)
    expect(await screen.findByText('default=m1')).toBeInTheDocument()
    expect(readiness).toHaveBeenCalledTimes(1)
    expect(providersApi.list).not.toHaveBeenCalled()
  })
})
