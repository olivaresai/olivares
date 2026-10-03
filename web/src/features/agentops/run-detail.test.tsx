// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { useClientSettings } from '@/features/settings/preferences'
import { toast } from '@/components/ui/toaster'
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

// MC (Root 2026-10-02, F1's 09 defect): the run's work stamp is permanent, its lease is
// not. After the peer work item is submitted the lease is released, the engine applies
// ordinary control and refuses a stale fence: Stop goes without one. While the lease is
// active, Stop is the fenced control and carries the exact fence.
describe('Stop on a work-bound run follows the current lease', () => {
  const bound: RunDTO = {
    ...unnamed,
    work_item_id: 'work-a',
    work_lease_fence: 7,
    work_owner_epoch: 2,
    work_dispatch_key: 'dispatch-a',
  }
  const stopBody = () => {
    const stops = httpPost.mock.calls.filter((call) =>
      String(call[0]).endsWith('/stop'),
    )
    expect(stops).toHaveLength(1)
    expect(String(stops[0]?.[0])).toBe('/v1/m/sessions/runs/run-anon/stop')
    return stops[0]?.[1]
  }

  it('after submit, Stop carries no fence and succeeds', async () => {
    useClientSettings.setState({ confirmStop: false })
    wrap({ ...bound, work_lease_state: 'ended' })
    await clickStop()
    await waitFor(() => expect(stopBody()).toBeUndefined())
    expect(api.getRun).toHaveBeenCalledWith('run-anon')
    expect(toast.error).not.toHaveBeenCalled()
  })

  it('during an active lease, Stop carries the exact fence', async () => {
    useClientSettings.setState({ confirmStop: false })
    wrap({ ...bound, work_lease_state: 'active' })
    await clickStop()
    await waitFor(() => expect(stopBody()).toEqual({ work_lease_fence: 7 }))
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

// MC: a Grok Build or OpenCode run says its own MCP servers are not governed.
describe('RunDetailSheet — the engine warning for a tool whose own MCP servers are not governed', () => {
  const sentence =
    "MCP servers configured in this tool's own settings are not governed by Olivares."
  it('shows the sentence as-is when the run carries it', async () => {
    wrap({
      ...unnamed,
      provider_driver: 'grok',
      mcp_governance_warning: sentence,
    })
    expect(await screen.findByText(sentence)).toHaveAttribute('role', 'note')
  })
  it('shows nothing when the run does not', async () => {
    wrap({ ...unnamed, provider_driver: 'claude' })
    await screen.findAllByRole('button', { name: /^Stop$/ })
    expect(screen.queryByText(/not governed by Olivares/)).toBeNull()
  })
})
