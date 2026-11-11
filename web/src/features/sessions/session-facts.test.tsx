// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { screen, waitFor, within } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { renderIntel } from '@/test/intel'
import type { RunDTO } from '@/features/agentops/types'
import { mergeSessions } from './provenance'
import { SessionFacts } from './session-facts'
import type { LiveDTO } from './types'
import './i18n'
import '@/features/agentops/i18n'

const auth = vi.hoisted(() => ({
  activeTenant: 't1',
  can: (_: string): boolean => true,
}))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => auth }))
const accounts = vi.hoisted(() => ({ getAccount: vi.fn() }))
vi.mock('@/features/agentops/api', async (importOriginal) => {
  const actual =
    await importOriginal<typeof import('@/features/agentops/api')>()
  return {
    ...actual,
    agentOpsApi: { ...actual.agentOpsApi, getAccount: accounts.getAccount },
  }
})

const templates = vi.hoisted(() => ({ get: vi.fn() }))
vi.mock('@/features/workspace-templates/api', async (importOriginal) => {
  const real =
    await importOriginal<typeof import('@/features/workspace-templates/api')>()
  return { ...real, templatesApi: { ...real.templatesApi, ...templates } }
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

// What the session's tool reported: its tokens and what they cost.
const LIVE: LiveDTO = {
  session_ref: 'sess_71c0',
  live_ref: 'lr_71c0',
  attribution: 'managed',
  cc_state: 'active',
  input_tokens: 12,
  output_tokens: 18,
  cost_micro_usd: 42000,
  event_count: 1,
  tool_call_count: 1,
  first_event_at: '2026-10-28T06:00:00Z',
  last_event_at: '2026-10-28T06:01:00Z',
  duration_seconds: 60,
  run_ref: RUN.run_ref,
}

function render(
  run: RunDTO = RUN,
  account: string | null = 'Marc’s Claude account',
  accountRef: string | undefined = RUN.provider_profile_ref,
  live?: LiveDTO,
) {
  const session = mergeSessions(live ? [live] : [], [run])[0]!
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
  auth.can = () => true
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

  it("never says the tool's private mode name while the launch template is read", () => {
    // The template that words dontAsk has not answered yet.
    templates.get.mockReturnValue(new Promise(() => {}))
    render({
      ...RUN,
      permission_mode: 'dontAsk',
      tool_mode: 'dontAsk',
      template_id: 'tpl-ec',
    })
    const facts = screen.getByRole('region', { name: 'Session' })
    expect(facts).not.toHaveTextContent('dontAsk')
  })

  it('says the access is not reported when neither launch nor tool supplied it', () => {
    render({ ...RUN, permission_mode: '', tool_mode: undefined })
    const access = within(screen.getByRole('region', { name: 'Session' }))
      .getByText('Access')
      .closest('div')!
    expect(access).toHaveTextContent('Access not reported')
    expect(access).not.toHaveTextContent(/default/)
  })

  it('says the cost of the run only where Usage opens, and the other facts to anyone', () => {
    const { unmount } = render(RUN, undefined, undefined, LIVE)
    const facts = screen.getByRole('region', { name: 'Session' })
    expect(within(facts).getByText('Cost').closest('div')).toHaveTextContent(
      '$0.042',
    )
    unmount()
    // A member sees no money, only the limits of their own accounts.
    auth.can = (permission) => permission !== 'sessions:account:read'
    render(RUN, undefined, undefined, LIVE)
    const member = screen.getByRole('region', { name: 'Session' })
    expect(within(member).queryByText('Cost')).toBeNull()
    expect(member).not.toHaveTextContent('$')
    expect(
      within(member).getByText('Workspace').closest('div'),
    ).toHaveTextContent('payments-api')
    expect(within(member).getByText('State')).toBeInTheDocument()
    expect(within(member).getByText('Tool')).toBeInTheDocument()
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
    // The share every place that names the account says; the other windows are on Usage.
    expect(
      await screen.findByRole('region', { name: 'Account usage' }),
    ).toHaveTextContent(/^Account usage37% of the week · 58% of 5 hours$/)
  })
})

it('does not request account details for an unnamed provider profile', async () => {
  render(RUN, 'Default sign-in', '')
  expect(accounts.getAccount).not.toHaveBeenCalled()
})
