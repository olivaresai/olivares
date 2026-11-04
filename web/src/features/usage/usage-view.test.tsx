// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Usage & cost (#3389): the account nearest its limit leads, then today's sessions, tokens
// and cost. Each section reads only what the principal may read, and a figure nobody
// reported is left out, never shown as zero.
import type { ComponentProps } from 'react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { ProviderSnapshot } from '@/features/agent-tools/api'
import type { ProviderAccountDTO } from '@/features/agentops/types'
import type { TodaysCost } from '@/features/home/now-sessions'
import { renderIntel, screen, within } from '@/test/intel'
import { UsageView } from './usage-view'

const state = vi.hoisted(() => ({
  // An organization admin: may read the accounts, is no system administrator.
  canAccounts: true,
  hidden: [] as string[],
  profiles: {
    isPending: false,
    isError: false,
    data: { accounts: [] as ProviderAccountDTO[] },
  },
  // The providers snapshot: a system read, so only a system administrator has data here.
  providers: {
    isError: false,
    data: { providers: [] as ProviderSnapshot[] } as
      { providers: ProviderSnapshot[] } | undefined,
  },
  recent: {
    canSessions: true,
    canRuns: true,
    sessionsQ: { isError: false },
    runsQ: { isError: false },
    recentSessions: [] as unknown[] | undefined,
    now: Date.parse('2026-10-28T12:00:00Z'),
    floor: (() => false) as (daysAgo: number) => boolean,
  },
  today: null as TodaysCost | null,
}))

vi.mock('@tanstack/react-router', () => ({
  Link: ({ children, to, ...props }: ComponentProps<'a'> & { to?: string }) => (
    <a href={to} {...props}>
      {children}
    </a>
  ),
}))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    isSuperadmin: false,
    can: (permission: string) =>
      permission === 'sessions:account:read' && state.canAccounts,
  }),
}))
vi.mock('@/features/navigation/authorization', () => ({
  useViewAccess: () => ({
    listed: (v: { id: string }) => !state.hidden.includes(v.id),
  }),
}))
vi.mock('@/features/agent-tools/use-profiles', () => ({
  useProfileList: () => state.profiles,
}))
vi.mock('@/features/agent-tools/use-background-providers', () => ({
  useBackgroundProviders: () => state.providers,
}))
vi.mock('@/features/home/use-recent-sessions', () => ({
  useRecentSessions: () => state.recent,
}))
vi.mock('@/features/home/now-sessions', () => ({
  dayCost: () => state.today,
}))

function account(over: Partial<ProviderAccountDTO>): ProviderAccountDTO {
  return {
    account_ref: 'acc-1',
    name: 'claude-a',
    driver: 'claude',
    environment_ref: 'env-1',
    state: 'active',
    home_mode: 'managed',
    home_generation: 1,
    home_relative: 'claude-a',
    isolation_level: 'dedicated',
    auth_source: 'native',
    identity: '',
    identity_source: 'none',
    created_at: '2026-10-28T08:00:00Z',
    updated_at: '2026-10-28T08:00:00Z',
    ...over,
  }
}

function window(
  name: 'five_hour' | 'seven_day',
  utilization: number,
): NonNullable<ProviderAccountDTO['usage']>[number] {
  return {
    window: name,
    label: name === 'five_hour' ? '5-hour' : 'Weekly',
    utilization,
    observed_at: '2026-10-28T08:00:00Z',
  }
}

afterEach(() => {
  state.canAccounts = true
  state.hidden = []
  state.profiles = {
    isPending: false,
    isError: false,
    data: { accounts: [] },
  }
  state.providers = { isError: false, data: { providers: [] } }
  state.recent.canSessions = true
  state.recent.canRuns = true
  state.recent.recentSessions = []
  state.recent.floor = () => false
  state.recent.runsQ = { isError: false }
  state.recent.sessionsQ = { isError: false }
  state.today = null
})

describe('UsageView', () => {
  it("shows an organization admin the accounts' limits, the account nearest its limit first", () => {
    state.profiles.data.accounts = [
      account({
        account_ref: 'acc-a',
        name: 'claude-a',
        usage: [window('seven_day', 34)],
      }),
      account({
        account_ref: 'acc-team',
        name: 'team',
        driver: 'codex',
        usage: [window('five_hour', 22), window('seven_day', 98)],
      }),
      account({ account_ref: 'acc-none', name: 'unread' }),
    ]
    renderIntel(<UsageView />)
    const accounts = screen.getByRole('region', { name: 'Accounts' })
    const rows = within(accounts).getAllByRole('listitem')
    // An account whose tool reported no window is left out, never shown as 0 %.
    expect(rows.map((r) => r.getAttribute('data-account'))).toEqual([
      'acc-team',
      'acc-a',
    ])
    expect(rows[0]).toHaveTextContent('team')
    expect(rows[0]).toHaveTextContent('5-hour 22% · Weekly 98%')
    expect(rows[1]).toHaveTextContent('Weekly 34%')
    expect(
      within(rows[0]!).getByRole('link', { name: 'Details of team' }),
    ).toHaveAttribute('href', '/agent-tools')
  })

  it("keeps the server user's own login for a system administrator, beside the accounts", () => {
    state.providers.data = {
      providers: [
        {
          instance: 'codex/default',
          driver: 'codex',
          default: true,
          config_dir: '',
          state: 'ready',
          installed: true,
          plan: 'Team',
          email: 'ops@example.com',
          limits: [
            { label: '5-hour', percent: 58 },
            { label: 'Weekly', percent: 37 },
          ],
          models: [],
          checked_at: '2026-10-28T08:00:00Z',
          source: 'codex',
        },
        // A login this organization made is an account: the account list says it, once.
        {
          instance: 'claude/olivares',
          driver: 'claude',
          default: false,
          config_dir: '',
          state: 'ready',
          installed: true,
          limits: [{ label: 'Weekly', percent: 99 }],
          models: [],
          checked_at: '2026-10-28T08:00:00Z',
          source: 'claude',
        },
      ],
    }
    state.profiles.data.accounts = [
      account({
        account_ref: 'acc-a',
        name: 'claude-a',
        usage: [window('seven_day', 34)],
      }),
    ]
    renderIntel(<UsageView />)
    const rows = within(
      screen.getByRole('region', { name: 'Accounts' }),
    ).getAllByRole('listitem')
    expect(rows.map((r) => r.getAttribute('data-account'))).toEqual([
      'codex/default',
      'acc-a',
    ])
    expect(rows[0]).toHaveTextContent('5-hour 58% · Weekly 37%')
    expect(rows[0]).toHaveTextContent('Codex · Team')
  })

  it('says the accounts were not read when the system providers read fails', () => {
    state.providers = { isError: true, data: undefined }
    renderIntel(<UsageView />)
    expect(
      within(screen.getByRole('region', { name: 'Accounts' })).getByRole(
        'alert',
      ),
    ).toHaveTextContent("The accounts' limits could not be read.")
  })

  it('shows no account section to a principal who may not read the accounts', () => {
    state.canAccounts = false
    renderIntel(<UsageView />)
    expect(screen.queryByRole('region', { name: 'Accounts' })).toBeNull()
    expect(screen.getByRole('region', { name: 'Today' })).toBeInTheDocument()
  })

  it('says the accounts were not read when the accounts read fails', () => {
    state.profiles = {
      isPending: false,
      isError: true,
      data: undefined as never,
    }
    renderIntel(<UsageView />)
    expect(
      within(screen.getByRole('region', { name: 'Accounts' })).getByRole(
        'alert',
      ),
    ).toHaveTextContent("The accounts' limits could not be read.")
  })

  it("says today's sessions, tokens and cost, and only the figures reported", () => {
    state.today = {
      sessions: 12,
      costMicroUsd: 6_030_000,
      unpriced: 1,
      inputTokens: 184_000,
      outputTokens: 39_000,
      tokensUnreported: 0,
    }
    renderIntel(<UsageView />)
    expect(screen.getByTestId('usage-today')).toHaveTextContent(
      '12 sessions · 184K input · 39,000 output · $6.03',
    )
    expect(screen.getByText('1 session reported no cost.')).toBeInTheDocument()

    state.today = {
      sessions: 2,
      costMicroUsd: null,
      unpriced: 2,
      inputTokens: null,
      outputTokens: null,
      tokensUnreported: 2,
    }
    renderIntel(<UsageView />)
    expect(screen.getAllByTestId('usage-today')[1]).toHaveTextContent(
      /^2 sessions$/,
    )
  })

  it('says how many sessions reported no tokens beside a token total', () => {
    state.today = {
      sessions: 3,
      costMicroUsd: 180_000,
      unpriced: 0,
      inputTokens: 500,
      outputTokens: 40,
      tokensUnreported: 2,
    }
    renderIntel(<UsageView />)
    expect(screen.getByTestId('usage-today')).toHaveTextContent(
      '3 sessions · 500 input · 40 output · $0.18',
    )
    expect(
      screen.getByText('2 sessions reported no tokens.'),
    ).toBeInTheDocument()
  })

  it('offers View sessions only to a principal who may open Sessions', () => {
    state.today = {
      sessions: 1,
      costMicroUsd: null,
      unpriced: 1,
      inputTokens: null,
      outputTokens: null,
      tokensUnreported: 1,
    }
    renderIntel(<UsageView />)
    expect(screen.getByRole('link', { name: 'View sessions' })).toHaveAttribute(
      'href',
      '/sessions',
    )
    state.hidden = ['sessions']
    renderIntel(<UsageView />)
    expect(screen.getAllByRole('link', { name: 'View sessions' })).toHaveLength(
      1,
    )
  })

  it('marks a cut page as a floor and a failed read as a failure, never as zero', () => {
    state.today = {
      sessions: 30,
      costMicroUsd: 1_000_000,
      unpriced: 0,
      inputTokens: 10,
      outputTokens: 10,
      tokensUnreported: 0,
    }
    state.recent.floor = () => true
    renderIntel(<UsageView />)
    expect(screen.getByTestId('usage-today')).toHaveTextContent(
      /^At least 30 sessions/,
    )

    state.recent.recentSessions = undefined
    state.recent.runsQ = { isError: true }
    renderIntel(<UsageView />)
    expect(
      screen.getByText("Today's sessions could not be read."),
    ).toBeInTheDocument()
    state.recent.runsQ = { isError: false }
  })

  it('says the day was not read when one permitted read failed and the other answered', () => {
    state.today = {
      sessions: 1,
      costMicroUsd: 420_000,
      unpriced: 0,
      inputTokens: 10,
      outputTokens: 10,
      tokensUnreported: 0,
    }
    state.recent.runsQ = { isError: true }
    renderIntel(<UsageView />)
    expect(screen.queryByTestId('usage-today')).toBeNull()
    expect(
      within(screen.getByRole('region', { name: 'Today' })).getByRole('alert'),
    ).toHaveTextContent("Today's sessions could not be read.")
  })
})
