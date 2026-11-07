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
    runsAnswer: { items: [] } as unknown,
    now: Date.parse('2026-10-28T12:00:00Z'),
    floor: (() => false) as (daysAgo: number) => boolean,
  },
  today: null as TodaysCost | null,
  tab: 'accounts',
}))

vi.mock('@tanstack/react-router', () => ({
  // The tab list asks for the router (tabs.tsx) and does without one.
  useRouter: () => undefined,
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
vi.mock('@/features/home/now-sessions', async (importOriginal) => ({
  ...(await importOriginal<object>()),
  dayCost: () => state.today,
}))
// The tab lives in `?tab=` (useTabParam); the rows are Now's, tested with their own file.
vi.mock('@/lib/hooks/use-tab-param', () => ({
  useTabParam: (values: readonly string[]) => [
    values.includes(state.tab) ? state.tab : values[0],
    vi.fn(),
  ],
}))
vi.mock('@/features/governance/use-pending-approvals', () => ({
  usePendingApprovals: () => ({ permitted: false, query: {} }),
}))
vi.mock('@/features/home/estate-lanes', () => ({
  EstateLanes: ({ sessions }: { sessions: unknown[] }) => (
    <div data-testid="lanes-stub">{sessions.length} sessions</div>
  ),
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
  state.recent.runsAnswer = { items: [] }
  state.recent.runsQ = { isError: false }
  state.recent.sessionsQ = { isError: false }
  state.today = null
  state.tab = 'accounts'
})

describe('UsageView', () => {
  it('does not paint its name under the top bar that already says Usage', () => {
    renderIntel(<UsageView />)
    // Still the page's h1 for assistive technology; only the painted word is dropped.
    expect(
      screen.getByRole('heading', { level: 1, name: 'Usage' }),
    ).toHaveClass('sr-only')
  })

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
    const rows = within(accounts)
      .getAllByRole('listitem')
      .filter((li) => li.hasAttribute('data-account'))
    // An account whose tool reported no window is left out, never shown as 0 %.
    expect(rows.map((r) => r.getAttribute('data-account'))).toEqual([
      'acc-team',
      'acc-a',
    ])
    expect(rows[0]).toHaveTextContent('team')
    expect(rows[0]).toHaveTextContent('98% of the week · 22% of 5 hours')
    expect(rows[1]).toHaveTextContent('34% of the week')
    // The row opens every window by name; the only place window names are said.
    expect(within(rows[0]!).getByText('Weekly 98%')).toBeInTheDocument()
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
    const rows = within(screen.getByRole('region', { name: 'Accounts' }))
      .getAllByRole('listitem')
      .filter((li) => li.hasAttribute('data-account'))
    expect(rows.map((r) => r.getAttribute('data-account'))).toEqual([
      'codex/default',
      'acc-a',
    ])
    expect(rows[0]).toHaveTextContent('37% of the week · 58% of 5 hours')
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

  it("says today's sessions, tokens and cost, and no sum that leaves sessions out", () => {
    state.today = {
      sessions: 12,
      costMicroUsd: 6_030_000,
      unpriced: 0,
      inputTokens: 184_000,
      outputTokens: 39_000,
      tokensUnreported: 0,
    }
    renderIntel(<UsageView />)
    expect(screen.getByTestId('usage-today')).toHaveTextContent(
      '12 sessions · 184K input · 39,000 output · $6.03',
    )

    // One session reported no cost: $6.03 is not the day's cost, so it says nothing.
    state.today = { ...state.today, unpriced: 1 }
    renderIntel(<UsageView />)
    expect(screen.getAllByTestId('usage-today')[1]).toHaveTextContent(
      /^12 sessions · 184K input · 39,000 output$/,
    )
    expect(screen.queryByText(/reported no/)).not.toBeInTheDocument()
  })

  it('leaves the tokens out while a session reported none', () => {
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
      /^3 sessions · \$0\.18$/,
    )
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

  it('gives no figure for a cut page or without the runs, and a failed read as a failure', () => {
    state.today = {
      sessions: 30,
      costMicroUsd: 1_000_000,
      unpriced: 0,
      inputTokens: 10,
      outputTokens: 10,
      tokensUnreported: 0,
    }
    // 30 sessions are a floor, not the day: no count, tokens or cost, only the way to Sessions.
    state.recent.floor = () => true
    renderIntel(<UsageView />)
    const today = screen.getByRole('region', { name: 'Today' })
    expect(within(today).queryByText(/30 sessions|\$/)).toBeNull()
    expect(
      within(today).getByRole('link', { name: 'View sessions' }),
    ).toBeInTheDocument()

    // Without the runs, the sessions that ended today are not known.
    state.recent.floor = () => false
    state.recent.runsAnswer = null
    renderIntel(<UsageView />)
    expect(
      within(screen.getAllByRole('region', { name: 'Today' })[1]).queryByText(
        /30 sessions/,
      ),
    ).toBeNull()

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

  it('the Timeline tab holds the last-hour lanes, over the sessions Usage reads (#4427)', () => {
    state.tab = 'timeline'
    state.recent.recentSessions = [
      { key: 's1', runs: [], live: { cc_state: 'active' } },
    ]
    renderIntel(<UsageView />)
    expect(screen.getByRole('tab', { name: 'Accounts' })).toBeInTheDocument()
    expect(
      screen.getByRole('tab', { name: 'Timeline', selected: true }),
    ).toBeInTheDocument()
    expect(screen.getByTestId('lanes-stub')).toHaveTextContent('1 sessions')
    expect(screen.queryByRole('region', { name: 'Accounts' })).toBeNull()
  })
})
