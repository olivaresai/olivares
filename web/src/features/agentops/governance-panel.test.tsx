// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import type { ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { RunDTO } from './types'

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't1', can: () => true }),
}))
vi.mock('@tanstack/react-router', () => ({
  //useUrlState follows the location, so the mock has to answer it.
  useRouterState: () => '',
  Link: ({ children, to }: { children: ReactNode; to: string }) => (
    <a href={to}>{children}</a>
  ),
}))
vi.mock('@/features/killswitch/api', () => ({
  killswitchApi: { state: vi.fn() },
  killswitchKeys: { state: (t: string | null) => ['ks', t, 'state'] },
}))
vi.mock('@/features/governance/api', () => ({
  governanceApi: { getApproval: vi.fn() },
}))

import { governanceApi } from '@/features/governance/api'
import { killswitchApi } from '@/features/killswitch/api'
import { GovernancePanel } from './governance-panel'

const baseRun: RunDTO = {
  run_ref: 'run-x',
  transport: 'stream-json',
  permission_mode: 'bypassPermissions',
  isolation: 'native',
  state: 'running',
  agent_ref: 'agent:a1',
  approval_ref: 'appr-1',
  last_event_seq: 1,
  pep_provisioned: true,
  record_io: true,
  critical: true,
}

function renderPanel(run: RunDTO) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <GovernancePanel run={run} />
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  vi.clearAllMocks()
})

describe('GovernancePanel', () => {
  it('shows the governed/recording posture from the run facts and live HITL status', async () => {
    vi.mocked(killswitchApi.state).mockResolvedValue({
      estate_stopped: false,
      active: [],
    })
    vi.mocked(governanceApi.getApproval).mockResolvedValue({
      id: 'appr-1',
      status: 'approved',
      required_approvals: 2,
      approve_count: 2,
      reject_count: 0,
      escalated: false,
    } as never)

    renderPanel(baseRun)
    // PEP provisioned ⇒ governed in line.
    expect(
      await screen.findByText(/every tool-call is policed in line/i),
    ).toBeInTheDocument()
    // Recording on.
    expect(
      screen.getByText(/anchored as signed ledger evidence/i),
    ).toBeInTheDocument()
    // Kill switch clear (live read).
    expect(await screen.findByText(/no active stop/i)).toBeInTheDocument()
    // HITL approval resolved live to approved.
    expect(await screen.findByText('Approved')).toBeInTheDocument()
  })

  it('surfaces an engaged estate kill switch as frozen', async () => {
    vi.mocked(killswitchApi.state).mockResolvedValue({
      estate_stopped: true,
      active: [],
    })
    vi.mocked(governanceApi.getApproval).mockResolvedValue({
      id: 'appr-1',
      status: 'pending',
      required_approvals: 2,
      approve_count: 0,
      reject_count: 0,
      escalated: false,
    } as never)

    renderPanel(baseRun)
    await waitFor(() =>
      expect(screen.getByText(/this session is frozen/i)).toBeInTheDocument(),
    )
  })

  it('never shows a green "clear" when the kill-switch read fails (deny-closed honesty)', async () => {
    vi.mocked(killswitchApi.state).mockRejectedValue(
      new Error('killswitch unreachable'),
    )
    vi.mocked(governanceApi.getApproval).mockResolvedValue({
      id: 'appr-1',
      status: 'approved',
      required_approvals: 2,
      approve_count: 2,
      reject_count: 0,
      escalated: false,
    } as never)
    renderPanel(baseRun)
    // The errored read is surfaced as not-provably-clear — NOT "no active stop".
    await waitFor(() =>
      expect(screen.getByText(/treated as not-clear/i)).toBeInTheDocument(),
    )
    expect(screen.queryByText(/no active stop/i)).not.toBeInTheDocument()
  })

  it('reports PEP deny-closed when no PEP was provisioned', async () => {
    vi.mocked(killswitchApi.state).mockResolvedValue({
      estate_stopped: false,
      active: [],
    })
    renderPanel({ ...baseRun, pep_provisioned: false, approval_ref: undefined })
    expect(
      await screen.findByText(/every tool-call is denied/i),
    ).toBeInTheDocument()
  })
})
