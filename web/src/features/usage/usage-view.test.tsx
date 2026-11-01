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
import type { TodaysCost } from '@/features/home/now-sessions'
import { renderIntel, screen, within } from '@/test/intel'
import { UsageView } from './usage-view'

const state = vi.hoisted(() => ({
  superadmin: true,
  hidden: [] as string[],
  providers: {
    isPending: false,
    isError: false,
    data: { providers: [] as ProviderSnapshot[] },
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
  useAuth: () => ({ isSuperadmin: state.superadmin }),
}))
vi.mock('@/features/navigation/authorization', () => ({
  useViewAccess: () => ({
    listed: (v: { id: string }) => !state.hidden.includes(v.id),
  }),
}))
vi.mock(
  '@/features/agent-tools/use-background-providers',
  async (importActual) => ({
    ...(await importActual<object>()),
    useBackgroundProviders: () => state.providers,
  }),
)
vi.mock('@/features/home/use-recent-sessions', () => ({
  useRecentSessions: () => state.recent,
}))
vi.mock('@/features/home/now-sessions', () => ({
  dayCost: () => state.today,
}))

function snap(over: Partial<ProviderSnapshot>): ProviderSnapshot {
  return {
    instance: 'claude/default',
    driver: 'claude',
    default: true,
    config_dir: '',
    state: 'ready',
    installed: true,
    limits: [],
    models: [],
    checked_at: '2026-10-28T08:00:00Z',
    source: 'claude /status',
    ...over,
  }
}

afterEach(() => {
  state.superadmin = true
  state.hidden = []
  state.providers = {
    isPending: false,
    isError: false,
    data: { providers: [] },
  }
  state.recent.canSessions = true
  state.recent.canRuns = true
  state.recent.recentSessions = []
  state.recent.floor = () => false
  state.recent.runsQ = { isError: false }
  state.recent.sessionsQ = { isError: false }
  state.today = null
})

describe('UsageView', () => {
  it('leads with the account nearest its limit and says its windows in words', () => {
    state.providers.data.providers = [
      snap({
        instance: 'claude/default',
        plan: 'Max',
        limits: [
          { label: '5-hour', percent: 42 },
          { label: 'Weekly', percent: 31 },
        ],
      }),
      snap({
        instance: 'codex/default',
        driver: 'codex',
        default: false,
        plan: 'Team',
        limits: [
          { label: '5-hour', percent: 58 },
          { label: 'Weekly', percent: 37 },
        ],
      }),
    ]
    renderIntel(<UsageView />)
    const accounts = screen.getByRole('region', { name: 'Accounts' })
    const rows = within(accounts).getAllByRole('listitem')
    expect(rows.map((r) => r.getAttribute('data-account'))).toEqual([
      'codex/default',
      'claude/default',
    ])
    expect(rows[0]).toHaveTextContent('58% 5-hour · 37% weekly')
    // Whose login it is comes from the tool's own flag, not from the instance's name.
    expect(rows[0]).toHaveTextContent("This organization's login")
    expect(rows[1]).toHaveTextContent("Server user's login")
    expect(
      within(rows[0]!).getByRole('link', { name: /Details of Codex · Team/ }),
    ).toHaveAttribute('href', '/agent-tools')
  })

  it('shows no account section to a principal who may not read the tools', () => {
    state.superadmin = false
    renderIntel(<UsageView />)
    expect(screen.queryByRole('region', { name: 'Accounts' })).toBeNull()
    // The subtitle promises the limits, so the page says why they are not here.
    expect(
      screen.getByText('Account limits are shown to a system administrator.'),
    ).toBeInTheDocument()
    expect(screen.getByRole('region', { name: 'Today' })).toBeInTheDocument()
  })

  it('says the accounts were not read when the providers read fails', () => {
    state.providers = {
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
