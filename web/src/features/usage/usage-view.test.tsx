// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Usage & cost: what a chosen window cost and used, by tool and by profile, then the
// account nearest its limit, then today's sessions, tokens and cost. Each section reads
// only what the principal may read, money shows only to a person Usage opens for, and a
// figure nobody reported is left out, never shown as zero.
import type { ComponentProps } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { ProviderSnapshot } from '@/features/agent-tools/api'
import type { ProviderAccountDTO } from '@/features/agentops/types'
import type { TodaysCost } from '@/features/home/now-sessions'
import type { UsageRow } from '@/features/models/types'
import { downloadBlob } from '@/lib/api/download'
import {
  cleanup,
  fireEvent,
  renderIntel,
  screen,
  userEvent,
  within,
} from '@/test/intel'
import { UsageView } from './usage-view'

const state = vi.hoisted(() => ({
  // An organization admin: may read the accounts, is no system administrator.
  canAccounts: true,
  // May read the runs' totals (`sessions:run:read`), which the Spend section reads.
  readsRuns: false,
  // Usage does not open for this person: money and the word for it are left out.
  noMoney: false,
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
    activeTenant: 't1',
    can: (permission: string) =>
      (permission === 'sessions:account:read' && state.canAccounts) ||
      (permission === 'sessions:run:read' && state.readsRuns),
  }),
}))
vi.mock('@/features/navigation/authorization', () => ({
  useViewAccess: () => ({
    listed: (v: { id: string }) => !state.hidden.includes(v.id),
    navigable: (v: { id: string }) => !(state.noMoney && v.id === 'usage'),
  }),
}))
// The runs' totals (`GET /v1/m/sessions/usage`), answered per group from `byGroup`.
const usageMock = vi.hoisted(() => vi.fn())
vi.mock('@/features/models/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/features/models/api')>()
  return {
    ...actual,
    modelsApi: {
      ...actual.modelsApi,
      usage: (...a: unknown[]) => usageMock(...a),
    },
  }
})
vi.mock('@/lib/api/download', async (importOriginal) => ({
  ...(await importOriginal<object>()),
  downloadBlob: vi.fn(),
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
  state.readsRuns = false
  state.noMoney = false
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
    // One trailing control per row, the one that opens it; the way to the
    // account's tool is inside what the row opens, not a second control.
    const link = within(rows[0]!).getByRole('link', { name: 'Details of team' })
    expect(link.closest('details')).toBe(rows[0]!.querySelector('details'))
    // The shared button's own small size: 44 px tall at phone width.
    expect(link).toHaveClass('max-md:min-h-11')
  })

  it('offers Details only to a person who may open the AI tools', () => {
    state.profiles.data.accounts = [
      account({
        account_ref: 'acc-team',
        name: 'team',
        usage: [window('seven_day', 98)],
      }),
    ]
    state.hidden = ['agent-tools']
    renderIntel(<UsageView />)
    const accounts = screen.getByRole('region', { name: 'Accounts' })
    expect(accounts).toHaveTextContent('98% of the week')
    expect(within(accounts).queryByRole('link', { name: /Details/ })).toBeNull()
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

  it('tells money only to a person Usage opens for: no $ and not the word cost for anyone else', () => {
    state.today = {
      sessions: 2,
      costMicroUsd: 0,
      unpriced: 0,
      inputTokens: 10,
      outputTokens: 10,
      tokensUnreported: 0,
    }
    // With the read: today's cost, a free day included, and the page says it tells cost.
    renderIntel(<UsageView />)
    expect(screen.getByTestId('usage-today')).toHaveTextContent(
      '2 sessions · 10 input · 10 output · $0.00',
    )
    expect(document.body).toHaveTextContent(/cost/)
    cleanup()

    // Without it (a member): the sessions and tokens, no money and not the word.
    state.noMoney = true
    renderIntel(<UsageView />)
    expect(screen.getByTestId('usage-today')).toHaveTextContent(
      /^2 sessions · 10 input · 10 output$/,
    )
    expect(document.body).not.toHaveTextContent('$')
    expect(document.body).not.toHaveTextContent(/cost/i)
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
    // The shared button's own small size: 44 px tall at phone width.
    expect(screen.getByRole('link', { name: 'View sessions' })).toHaveClass(
      'max-md:min-h-11',
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

  it('the Timeline tab holds the last-hour lanes, over the sessions Usage reads', () => {
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

const usageRow = (over: Partial<UsageRow>): UsageRow => ({
  runs: 0,
  input_tokens: null,
  output_tokens: null,
  cost_micro_usd: null,
  runs_without_tokens: 0,
  runs_without_cost: 0,
  ...over,
})

// What the engine answers for the window, per day: by tool, and by profile (`account`).
// Codex reported tokens and no cost.
const byGroup: Record<'tool' | 'account', UsageRow[]> = {
  tool: [
    usageRow({
      key: 'claude',
      day: '2026-11-10',
      runs: 3,
      input_tokens: 3000,
      output_tokens: 300,
      cost_micro_usd: 2_000_000,
    }),
    usageRow({
      key: 'codex',
      day: '2026-11-10',
      runs: 2,
      input_tokens: 1000,
      output_tokens: 100,
      runs_without_cost: 2,
    }),
    usageRow({
      key: 'claude',
      day: '2026-11-11',
      runs: 1,
      input_tokens: 500,
      output_tokens: 50,
      cost_micro_usd: 1_000_000,
    }),
  ],
  account: [
    usageRow({
      key: 'p-team',
      day: '2026-11-10',
      runs: 3,
      input_tokens: 3000,
      output_tokens: 300,
      cost_micro_usd: 2_000_000,
    }),
    usageRow({
      key: 'p-cx',
      day: '2026-11-10',
      runs: 2,
      input_tokens: 1000,
      output_tokens: 100,
      runs_without_cost: 2,
    }),
    usageRow({
      key: 'p-team',
      day: '2026-11-11',
      runs: 1,
      input_tokens: 500,
      output_tokens: 50,
      cost_micro_usd: 1_000_000,
    }),
  ],
}

const read = (group: 'tool' | 'account', since: string, until: string) => ({
  group_by: group,
  since,
  until,
  bucket: 'day',
})

describe('UsageView spend', () => {
  beforeEach(() => {
    // Only the clock: the window is whole UTC days ending today.
    vi.useFakeTimers({ toFake: ['Date'] })
    vi.setSystemTime(new Date('2026-11-11T10:00:00Z'))
    state.readsRuns = true
    state.profiles.data.accounts = [
      account({ account_ref: 'p-team', name: 'team' }),
      account({ account_ref: 'p-cx', name: 'cx-main', driver: 'codex' }),
    ]
    usageMock.mockImplementation((q: { group_by: 'tool' | 'account' }) =>
      Promise.resolve({ group_by: q.group_by, items: byGroup[q.group_by] }),
    )
  })
  afterEach(() => {
    vi.useRealTimers()
    usageMock.mockReset()
    vi.mocked(downloadBlob).mockClear()
  })

  it('tells a person Usage opens for what the last 14 days cost, used and ran, by profile', async () => {
    renderIntel(<UsageView />)
    const spend = await screen.findByRole('region', { name: 'Spend' })
    const totals = await within(spend).findByRole('group', { name: 'Totals' })
    expect(totals).toHaveTextContent('Spend$3.00')
    // Codex reported no cost: $3.00 is what was reported, and it says so.
    expect(totals).toHaveTextContent('Runs with no cost reported: 2')
    expect(totals).toHaveTextContent('Runs6')
    expect(totals).toHaveTextContent('Input tokens4,500')
    expect(totals).toHaveTextContent('Output tokens450')
    expect(usageMock).toHaveBeenCalledWith(
      read('tool', '2026-10-29T00:00:00Z', '2026-11-12T00:00:00Z'),
    )
    expect(usageMock).toHaveBeenCalledWith(
      read('account', '2026-10-29T00:00:00Z', '2026-11-12T00:00:00Z'),
    )
    const team = within(spend).getByRole('row', { name: /^team/ })
    expect(within(team).getByText('Claude Code')).toBeInTheDocument()
    expect(within(team).getByText('$3.00')).toBeInTheDocument()
    expect(
      within(spend).getByRole('columnheader', { name: 'Spend' }),
    ).toBeInTheDocument()
  })

  it('shows a person Usage does not open for the runs and tokens, and no money anywhere', async () => {
    state.noMoney = true
    state.today = {
      sessions: 2,
      costMicroUsd: 420_000,
      unpriced: 0,
      inputTokens: 10,
      outputTokens: 10,
      tokensUnreported: 0,
    }
    const user = userEvent.setup()
    renderIntel(<UsageView />)
    const spend = await screen.findByRole('region', { name: 'Runs and tokens' })
    const totals = await within(spend).findByRole('group', { name: 'Totals' })
    expect(totals).toHaveTextContent('Input tokens4,500')
    expect(within(spend).getByRole('row', { name: /^team/ })).toBeVisible()
    expect(
      within(spend).queryByRole('columnheader', { name: 'Spend' }),
    ).toBeNull()
    expect(screen.getByTestId('usage-today')).toHaveTextContent(
      /^2 sessions · 10 input · 10 output$/,
    )
    expect(document.body).not.toHaveTextContent('$')
    expect(document.body).not.toHaveTextContent(/cost|spend/i)

    await user.click(within(spend).getByRole('button', { name: 'Export' }))
    const [file] = vi.mocked(downloadBlob).mock.calls[0]!
    expect(await file.text()).not.toMatch(/cost|\$/)
  })

  it('filters the figures, the profiles and the limits by tool and by profile', async () => {
    state.profiles.data.accounts = [
      account({
        account_ref: 'p-team',
        name: 'team',
        usage: [window('seven_day', 40)],
      }),
      account({
        account_ref: 'p-cx',
        name: 'cx-main',
        driver: 'codex',
        usage: [window('seven_day', 70)],
      }),
      account({
        account_ref: 'p-cx2',
        name: 'cx-spare',
        driver: 'codex',
        usage: [window('seven_day', 10)],
      }),
    ]
    // The account rows only: each row also lists its own windows as list items.
    const limits = () =>
      within(screen.getByRole('region', { name: 'Accounts' }))
        .getAllByRole('listitem')
        .filter((r) => r.hasAttribute('data-account'))
        .map((r) => r.getAttribute('data-account'))
    const user = userEvent.setup()
    renderIntel(<UsageView />)
    const spend = await screen.findByRole('region', { name: 'Spend' })
    await within(spend).findByRole('row', { name: /^team/ })
    expect(limits()).toEqual(['p-cx', 'p-team', 'p-cx2'])

    await user.click(within(spend).getByRole('combobox', { name: 'Tool' }))
    await user.click(screen.getByRole('option', { name: 'Codex' }))
    const totals = within(spend).getByRole('group', { name: 'Totals' })
    expect(totals).toHaveTextContent('Runs2')
    expect(totals).toHaveTextContent('Input tokens1,000')
    expect(within(spend).queryByRole('row', { name: /^team/ })).toBeNull()
    expect(within(spend).getByRole('row', { name: /^cx-main/ })).toBeVisible()
    expect(limits()).toEqual(['p-cx', 'p-cx2'])

    await user.click(within(spend).getByRole('combobox', { name: 'Profile' }))
    await user.click(screen.getByRole('option', { name: 'cx-main' }))
    expect(limits()).toEqual(['p-cx'])
    expect(
      within(spend).getByRole('group', { name: 'Totals' }),
    ).toHaveTextContent('Runs2')
  })

  it('asks for 7 or 30 days or a chosen range, and refuses one that ends before it starts', async () => {
    const user = userEvent.setup()
    renderIntel(<UsageView />)
    const spend = await screen.findByRole('region', { name: 'Spend' })
    await within(spend).findByRole('group', { name: 'Totals' })

    await user.click(within(spend).getByRole('radio', { name: '30 days' }))
    expect(usageMock).toHaveBeenCalledWith(
      read('tool', '2026-10-13T00:00:00Z', '2026-11-12T00:00:00Z'),
    )
    await user.click(within(spend).getByRole('radio', { name: '7 days' }))
    expect(usageMock).toHaveBeenCalledWith(
      read('tool', '2026-11-05T00:00:00Z', '2026-11-12T00:00:00Z'),
    )

    // A chosen range starts from the window on screen.
    await user.click(within(spend).getByRole('radio', { name: 'Custom' }))
    const first = within(spend).getByLabelText('First day')
    const last = within(spend).getByLabelText('Last day')
    expect(first).toHaveValue('2026-11-05')
    expect(last).toHaveValue('2026-11-11')
    fireEvent.change(first, { target: { value: '2026-11-01' } })
    fireEvent.change(last, { target: { value: '2026-11-03' } })
    expect(usageMock).toHaveBeenCalledWith(
      read('tool', '2026-11-01T00:00:00Z', '2026-11-04T00:00:00Z'),
    )

    fireEvent.change(first, { target: { value: '2026-11-08' } })
    expect(within(spend).getByRole('alert')).toHaveTextContent(
      'The first day must be on or before the last.',
    )
    expect(usageMock).not.toHaveBeenCalledWith(
      expect.objectContaining({ since: '2026-11-08T00:00:00Z' }),
    )
  })

  it('refuses a chosen range of more than 366 days: no read goes out and the message shows', async () => {
    const user = userEvent.setup()
    renderIntel(<UsageView />)
    const spend = await screen.findByRole('region', { name: 'Spend' })
    await within(spend).findByRole('group', { name: 'Totals' })
    await user.click(within(spend).getByRole('radio', { name: 'Custom' }))
    const first = within(spend).getByLabelText('First day')
    const last = within(spend).getByLabelText('Last day')
    // The pickers offer no day the rule refuses.
    expect(first).toHaveAttribute('min', '2025-11-11')
    expect(first).toHaveAttribute('max', '2026-11-11')
    expect(last).toHaveAttribute('min', '2026-10-29')
    expect(last).toHaveAttribute('max', '2027-10-29')

    fireEvent.change(first, { target: { value: '1970-01-01' } })
    expect(within(spend).getByRole('alert')).toHaveTextContent(
      'A range holds at most 366 days.',
    )
    expect(usageMock).not.toHaveBeenCalledWith(
      expect.objectContaining({ since: '1970-01-01T00:00:00Z' }),
    )
  })

  it('exports the rows shown as a CSV file named by its window', async () => {
    const user = userEvent.setup()
    renderIntel(<UsageView />)
    const spend = await screen.findByRole('region', { name: 'Spend' })
    await within(spend).findByRole('group', { name: 'Totals' })
    await user.click(within(spend).getByRole('button', { name: 'Export' }))
    const [file, name] = vi.mocked(downloadBlob).mock.calls[0]!
    expect(name).toBe('usage-2026-10-29-2026-11-11.csv')
    expect(await file.text()).toBe(
      'day,tool,runs,input_tokens,output_tokens,runs_without_tokens,cost_usd,runs_without_cost\r\n' +
        '2026-11-10,Claude Code,3,3000,300,0,2,0\r\n' +
        '2026-11-10,Codex,2,1000,100,0,,2\r\n' +
        '2026-11-11,Claude Code,1,500,50,0,1,0\r\n',
    )
  })
})
