// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { screen, waitFor, within } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { renderIntel } from '@/test/intel'
import type { RunDTO } from '@/features/agentops/types'
import { mergeSessions } from './provenance'
import { SessionFacts } from './session-facts'
import './i18n'
import '@/features/agentops/i18n'

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't1', can: () => true }),
}))
const accounts = vi.hoisted(() => ({ getAccount: vi.fn() }))
vi.mock('@/features/agentops/api', async (importOriginal) => {
  const actual =
    await importOriginal<typeof import('@/features/agentops/api')>()
  return {
    ...actual,
    agentOpsApi: { ...actual.agentOpsApi, getAccount: accounts.getAccount },
  }
})

const RUN: RunDTO = {
  run_ref: 'run_8f3a21c9',
  name: 'Reconcile invoice export',
  transport: 'stream-json',
  permission_mode: 'default',
  isolation: 'native',
  state: 'running',
  last_event_seq: 0,
  pep_provisioned: true,
  record_io: true,
  critical: false,
  provider_driver: 'claude',
  provider_profile_ref: 'ppf_9d2e0b17',
  model_ref: 'claude-sonnet-5-5',
  effort: 'medium',
  workspace_ref: 'ws_44c1aa02',
  workspace_path: '/srv/payments-api',
}

function render(
  run: RunDTO = RUN,
  account: string | null = 'Marc’s Claude account',
  accountRef: string | undefined = RUN.provider_profile_ref,
) {
  const session = mergeSessions([], [run])[0]!
  return renderIntel(
    <SessionFacts
      session={session}
      run={run}
      account={account}
      accountRef={accountRef}
      workspace="payments-api"
    />,
  )
}

beforeEach(() => {
  accounts.getAccount.mockReset().mockResolvedValue({ usage: [] })
})

describe('SessionFacts — the session as a person reads it', () => {
  it('says state, workspace, tool, account, model and access in words, with no reference', () => {
    render()
    const facts = screen.getByRole('region', { name: 'Session' })
    for (const [label, value] of [
      ['Workspace', 'payments-api'],
      ['Tool', 'Claude Code'],
      ['Account', 'Marc’s Claude account'],
      ['Model', 'Claude Sonnet 5.5 · Medium'],
    ])
      expect(
        within(facts).getByText(label).closest('div')?.textContent,
      ).toContain(value)
    // The state word and its age stay apart, as the list's row spaces them.
    expect(
      within(facts)
        .getByText('State')
        .closest('div')
        ?.querySelector('dd > span'),
    ).toHaveClass('gap-1.5')
    expect(within(facts).getByText('Access')).toBeInTheDocument()
    // No engine reference reaches the reader: not the run, the profile or the workspace.
    for (const ref of ['run_8f3a21c9', 'ppf_9d2e0b17', 'ws_44c1aa02'])
      expect(facts).not.toHaveTextContent(ref)
  })

  it('leaves out a fact the engine did not send instead of printing a reference', () => {
    render({ ...RUN, model_ref: undefined, effort: undefined }, null)
    const facts = screen.getByRole('region', { name: 'Session' })
    expect(within(facts).queryByText('Model')).toBeNull()
    expect(within(facts).queryByText('Account')).toBeNull()
  })

  it("shows the usage of the session's own account, and nothing when it has none", async () => {
    // A profile nobody named is not an account: the engine has no usage to give for it.
    accounts.getAccount.mockRejectedValue(new Error('not found'))
    const { unmount } = render()
    await waitFor(() =>
      expect(accounts.getAccount).toHaveBeenCalledWith(
        'ppf_9d2e0b17',
        expect.anything(),
      ),
    )
    expect(screen.queryByRole('region', { name: 'Account usage' })).toBeNull()
    unmount()
    const observed_at = '2026-10-28T06:00:00Z'
    accounts.getAccount.mockResolvedValue({
      account_ref: 'ppf_9d2e0b17',
      usage: [
        {
          window: 'five_hour',
          label: '5-hour',
          utilization: 58.2,
          observed_at,
        },
        { window: 'seven_day', label: 'Weekly', utilization: 37, observed_at },
        {
          window: 'monthly_spend',
          label: 'Monthly spend',
          utilization: 85,
          observed_at,
        },
        {
          window: 'credits',
          label: 'Credits',
          credit: { balance: '12.50', unlimited: false },
          observed_at,
        },
        {
          window: 'seven_day:opus',
          label: 'Weekly (Opus)',
          utilization: 80,
          observed_at,
        },
      ],
    })
    render()
    const meters = within(
      await screen.findByRole('region', { name: 'Account usage' }),
    ).getAllByRole('meter')
    expect(meters.map((m) => m.getAttribute('aria-valuenow'))).toEqual([
      '58',
      '37',
    ])
    expect(screen.getByText('58% of 5 hours')).toBeInTheDocument()
    expect(screen.getByText('37% of the week')).toBeInTheDocument()
    expect(screen.getByTestId('account-usage')).toHaveTextContent(
      'Monthly spend 85% · Credits 12.50',
    )
  })
})

it('does not request account details for an unnamed provider profile', async () => {
  render(RUN, 'Default sign-in', '')
  expect(accounts.getAccount).not.toHaveBeenCalled()
})
