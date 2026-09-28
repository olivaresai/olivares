// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { useClientSettings } from '@/features/settings/preferences'
import type { RunDTO } from './types'

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't1', can: () => true }),
}))
vi.mock('@/components/ui/toaster', () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}))
vi.mock('./live-console', () => ({ LiveConsole: () => <div>live</div> }))
vi.mock('./governance-panel', () => ({ GovernancePanel: () => <div>gov</div> }))

const httpPost = vi.hoisted(() => vi.fn())
vi.mock('@/lib/api/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/api/client')>()
  return { ...actual, http: { ...actual.http, post: httpPost } }
})

const api = vi.hoisted(() => ({ getRun: vi.fn() }))
vi.mock('./api', async (orig) => {
  const real = (await orig()) as Record<string, unknown>
  return { ...real, agentOpsApi: { ...(real.agentOpsApi as object), ...api } }
})

import { RunDetailSheet } from './run-detail'

const unnamed: RunDTO = {
  run_ref: 'run-anon',
  transport: 'stream-json',
  permission_mode: 'default',
  isolation: 'native',
  state: 'running',
  last_event_seq: 0,
  pep_provisioned: true,
  record_io: false,
  critical: false,
}

function wrap(run: RunDTO) {
  api.getRun.mockResolvedValue(run)
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <RunDetailSheet runRef={run.run_ref} onClose={() => undefined} />
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  vi.clearAllMocks()
  localStorage.clear()
  httpPost.mockResolvedValue({ run_ref: 'run-anon', state: 'stopped' })
  useClientSettings.setState({
    confirmStop: true,
    clock: '24',
    startPage: 'home',
  })
})

async function clickStop() {
  const user = userEvent.setup()
  const button = await screen.findByRole('button', { name: /^Stop$/ })
  await user.click(button)
  return user
}

describe('Stop asks when confirm-before-stop is on', () => {
  it('sends no stop when the operator cancels', async () => {
    wrap(unnamed)
    const user = await clickStop()
    expect(
      await screen.findByRole('heading', { name: /stop this session/i }),
    ).toBeInTheDocument()
    expect(httpPost).not.toHaveBeenCalled()
    await user.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(httpPost).not.toHaveBeenCalled()
  })

  it('sends exactly one stop when the operator confirms', async () => {
    wrap(unnamed)
    const user = await clickStop()
    await user.click(screen.getByRole('button', { name: 'Stop the session' }))
    const stops = httpPost.mock.calls.filter((call) =>
      String(call[0]).endsWith('/stop'),
    )
    expect(stops).toHaveLength(1)
    expect(String(stops[0]?.[0])).toContain('run-anon')
  })

  it('stops at once when confirm-before-stop is off', async () => {
    useClientSettings.setState({ confirmStop: false })
    wrap(unnamed)
    await clickStop()
    expect(
      screen.queryByRole('heading', { name: /stop this session/i }),
    ).not.toBeInTheDocument()
    const stops = httpPost.mock.calls.filter((call) =>
      String(call[0]).endsWith('/stop'),
    )
    expect(stops).toHaveLength(1)
  })
})

describe('RunDetailSheet — what the run is called', () => {
  it('does not name a run by its raw reference', async () => {
    wrap(unnamed)
    expect(await screen.findByText('Untitled session')).toBeInTheDocument()
    expect(screen.getByText('run-anon')).toBeInTheDocument()
    expect(screen.getByText('Untitled session').className).not.toMatch(
      /font-mono/,
    )
  })

  it('paints the operator-given name when one exists', async () => {
    wrap({ ...unnamed, name: 'nightly-indexer' })
    expect(await screen.findByText('nightly-indexer')).toBeInTheDocument()
    expect(screen.queryByText('Untitled session')).toBeNull()
  })
})
