// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { ReactNode } from 'react'
import { onlineManager } from '@tanstack/react-query'
import { afterEach, describe, expect, it, vi } from 'vitest'
import {
  createTestQueryClient,
  renderIntel,
  screen,
  userEvent,
  waitFor,
  within,
} from '@/test/intel'
import { expectNoRawI18nKeys } from '@/test/i18n-keys'
// NO hand-registered namespaces here. This file used to import `@/features/_intel`
// and `@/features/executive/i18n` for their side effect — which is precisely what
// home-view.tsx does NOT do, so the test read real English copy while the shipped
// front door printed `cost.deltaUp`. The modules that translate now register their
// own namespaces; if that regresses, this test goes red with the raw key.
import { ApiError } from '@/lib/api/errors'
import { auditApi } from '@/lib/api/endpoints'
import { finopsApi } from '@/features/finops/api'
import {
  budgetsFixture,
  budgetStatusFixtures,
} from '@/features/finops/fixtures'
import { killswitchApi } from '@/features/killswitch/api'
import { governanceApi } from '@/features/governance/api'
import { securityApi } from '@/features/security/api'
import { healthApi } from '@/features/health/api'
import { complianceApi } from '@/features/compliance/api'
import { readinessOf } from '@/features/first-hour/readiness.fixture'
import { signInApi } from '@/features/first-hour/api'
import { sessionsApi } from '@/features/sessions/api'
import { inventoryApi } from '@/features/inventory/api'
import { agentOpsApi } from '@/features/agentops/api'
import { HomeView } from './home-view'
import type { SwimlaneProps } from './swimlane'
import { FLEET_SESSIONS } from './swimlane-model'
import './i18n'

// Render TanStack Router <Link> as a plain anchor (no RouterProvider in jsdom) — the
// established pattern across the view tests.
const panels = vi.hoisted(() => ({
  complianceView: undefined as undefined | (() => null),
  estateFleet: undefined as undefined | ((p: SwimlaneProps) => ReactNode),
}))
vi.mock('@/features/extensions', () => ({
  PANEL_EXTENSIONS: panels,
  FEATURE_EXTENSIONS: [],
}))

const navigate = vi.hoisted(() => vi.fn())
vi.mock('@tanstack/react-router', () => ({
  //useUrlState follows the location, so the mock has to answer it.
  useRouterState: () => '',
  // Home mounts `WorkComposer`, which navigates to the started run.
  useNavigate: () => navigate,
  Link: ({ children, to }: { children: ReactNode; to: string }) => (
    <a href={to}>{children}</a>
  ),
}))

// Whether the person opened Compliance (compliance-opened.test.ts covers the record).
const compliance = vi.hoisted(() => ({ opened: false }))
vi.mock('@/features/compliance/compliance-opened', () => ({
  useComplianceOpened: () => compliance.opened,
}))

// A mutable auth value the container reads — flip `can` per test to assert RBAC gating.
const authState = vi.hoisted(() => ({
  can: (_p: string): boolean => true,
  activeTenant: 'demo' as string | null,
  // The tools' own status is a system administrator's read (useToolStatus).
  isSuperadmin: true,
  principal: { superadmin: true, grants: [] } as {
    superadmin: boolean
    grants: never[]
    actor?: string
    kind?: string
  },
}))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))

afterEach(() => {
  panels.complianceView = undefined
  panels.estateFleet = undefined
  onlineManager.setOnline(true)

  vi.restoreAllMocks()
  compliance.opened = false
  navigate.mockReset()
  authState.can = () => true
  authState.isSuperadmin = true
  authState.principal = { superadmin: true, grants: [] }
})

const page = (items: unknown[], has_more = false) =>
  ({ items, has_more }) as never

describe('Estate: three regions, one orange control, the proof line (#2546)', () => {
  const now = () => new Date().toISOString()
  const run = (over: Record<string, unknown>) => ({
    tenant_id: 'demo',
    state: 'running',
    provider_driver: 'claude',
    created_at: now(),
    last_activity_at: now(),
    proof: { ledger_entries: 4, chain_verified: true },
    ...over,
  })
  const estate = () => {
    vi.spyOn(sessionsApi, 'live').mockResolvedValue(page([]))
    vi.spyOn(agentOpsApi, 'listRuns').mockResolvedValue(
      page([
        run({
          run_ref: 'r1',
          name: 'Refactor cart totals',
          cost_micro_usd: 1_420_000,
          acts_for: { kind: 'user', user_id: 'u-lena' },
        }),
        run({
          run_ref: 'r2',
          name: 'Reconcile invoice export',
          cost_micro_usd: 880_000,
          acts_for: { kind: 'user', user_id: 'u-marc' },
        }),
        run({
          run_ref: 'r3',
          name: 'Nightly dependency audit',
          cost_micro_usd: 120_000,
          acts_for: { kind: 'automation' },
        }),
      ]),
    )
    vi.spyOn(governanceApi, 'listApprovals').mockResolvedValue(
      page([
        {
          id: 'ap-migrate',
          action: 'claude.hook.tool',
          subject_kind: 'claude.tool',
          requested_by: 'user:marc',
          review: {
            tool: 'Command',
            text: 'pnpm db:migrate --env payments-staging',
          },
          status: 'pending',
          required_approvals: 1,
          approve_count: 0,
          reject_count: 0,
          escalated: false,
        },
        {
          id: 'ap-network',
          action: 'claude.hook.tool',
          subject_kind: 'claude.tool',
          requested_by: 'user:sam',
          review: {
            tool: 'Command',
            text: 'curl -sI https://registry.terraform.io/v1/providers',
          },
          status: 'pending',
          required_approvals: 1,
          approve_count: 0,
          reject_count: 0,
          escalated: false,
        },
      ]),
    )
    vi.spyOn(auditApi, 'recent').mockResolvedValue({
      items: [],
      head_seq: 12408,
    })
    vi.spyOn(agentOpsApi, 'toolsReadiness').mockResolvedValue(
      readinessOf({ claude: 'own_login' }),
    )
    vi.spyOn(signInApi, 'status').mockImplementation(
      async (driver) =>
        ({
          driver,
          installed: driver === 'claude',
          signed_in: driver === 'claude',
        }) as Awaited<ReturnType<typeof signInApi.status>>,
    )
  }

  it('what runs and what it costs takes no click; the request takes one', async () => {
    estate()
    const { container } = renderIntel(<HomeView />)
    // Region 1: the sentence (6 sessions would need 6 people; here 2 people, 1 automation).
    expect(
      await screen.findByTestId('estate-summary-sentence'),
    ).toHaveTextContent(
      '3 sessions open for 2 people · $2.42 today · 2 need you',
    )
    // Region 2: each session with its cost, on the first screen.
    const cards = await screen.findByTestId('now-live')
    expect(within(cards).getByText('$1.42')).toBeInTheDocument()
    // Region 3: the flagged request, decided from here.
    const flagged = await screen.findByTestId('now-flagged-request')
    expect(flagged).toHaveTextContent('pnpm db:migrate --env payments-staging')
    expect(
      within(flagged).getByRole('button', { name: 'Approve once' }),
    ).toBeInTheDocument()
    // Three regions under the header, and no fourth.
    expect(container.querySelectorAll('[data-estate-region]')).toHaveLength(3)
    expectNoRawI18nKeys(container)
  })

  it('the spend line puts the month against the monthly budget', async () => {
    estate()
    vi.spyOn(finopsApi, 'budgets').mockResolvedValue(page(budgetsFixture))
    vi.spyOn(finopsApi, 'budgetStatus').mockResolvedValue(
      budgetStatusFixtures['bdg-global']!,
    )
    renderIntel(<HomeView />)
    expect(
      await screen.findByRole('meter', { name: 'Spend this month' }),
    ).toHaveAttribute('aria-valuetext', '$12,400.00 of $100,000.00 this month')
  })

  it('a session waiting for a person counts in the sentence beside the requests', async () => {
    estate()
    vi.spyOn(sessionsApi, 'live').mockResolvedValue(
      page([
        {
          session_ref: 'sess-silent',
          live_ref: 'l-silent',
          attribution: 'legacy',
          cc_state: 'silent_evasion',
          input_tokens: 0,
          output_tokens: 0,
          cost_micro_usd: 0,
          event_count: 1,
          tool_call_count: 0,
          first_event_at: now(),
          last_event_at: now(),
          duration_seconds: 1,
        },
      ]),
    )
    renderIntel(<HomeView />)
    await waitFor(async () =>
      expect(
        await screen.findByTestId('estate-summary-sentence'),
      ).toHaveTextContent('3 need you'),
    )
  })

  it('a live page cut short says at least how many sessions run', async () => {
    estate()
    vi.spyOn(sessionsApi, 'live').mockResolvedValue(page([], true))
    renderIntel(<HomeView />)
    await waitFor(async () =>
      expect(
        await screen.findByTestId('estate-summary-sentence'),
      ).toHaveTextContent(/^3\+ sessions open · /),
    )
  })

  it('a sessions read paused offline says so, never a skeleton with no reason', async () => {
    estate()
    onlineManager.setOnline(false)
    renderIntel(<HomeView />)
    expect(await screen.findByTestId('estate-summary')).toHaveTextContent(
      'Query paused — waiting to resume',
    )
  })

  it('one filled orange control: Approve, while New session steps back', async () => {
    estate()
    const { container } = renderIntel(<HomeView />)
    await screen.findByTestId('now-flagged-request')
    await screen.findByTestId('now-start')
    const orange = () =>
      [...container.querySelectorAll('button, a')].filter((el) =>
        el.classList.contains('bg-accent'),
      )
    await waitFor(() => expect(orange()).toHaveLength(1))
    expect(orange()[0]).toHaveTextContent('Approve')
  })

  it('with nothing to decide, New session is the one orange control', async () => {
    estate()
    vi.spyOn(governanceApi, 'listApprovals').mockResolvedValue(page([]))
    const { container } = renderIntel(<HomeView />)
    const start = await screen.findByTestId('now-start')
    await waitFor(() =>
      expect(
        within(start).getByRole('button', { name: /New session/ }),
      ).toHaveClass('bg-accent'),
    )
    expect(
      [...container.querySelectorAll('.bg-accent')].filter((el) =>
        el.matches('button, a'),
      ),
    ).toHaveLength(1)
  })

  it('a request the person raised steps aside: the next one they may decide carries the orange', async () => {
    estate()
    // The first request was raised by user:marc (estate()); here Marc is signed in.
    authState.principal = {
      superadmin: true,
      grants: [],
      actor: 'user:marc',
      kind: 'user',
    }
    const { container } = renderIntel(<HomeView />)
    const flagged = await screen.findByTestId('now-flagged-request')
    expect(flagged).toHaveTextContent('registry.terraform.io')
    await screen.findByTestId('now-start')
    await waitFor(() =>
      expect(
        [...container.querySelectorAll('.bg-accent')].filter((el) =>
          el.matches('button, a'),
        ),
      ).toHaveLength(1),
    )
    expect(
      within(flagged).getByRole('button', { name: 'Approve once' }),
    ).toHaveClass('bg-accent')
  })

  it('with only requests the person raised, New session keeps the orange', async () => {
    estate()
    authState.principal = {
      superadmin: true,
      grants: [],
      actor: 'user:marc',
      kind: 'user',
    }
    const all = await governanceApi.listApprovals({ status: 'pending' })
    vi.spyOn(governanceApi, 'listApprovals').mockResolvedValue(
      page(all.items.filter((a) => a.requested_by === 'user:marc')),
    )
    renderIntel(<HomeView />)
    // A quiet row, not the flag: there is nothing here this person may decide.
    expect(await screen.findByTestId('now-approvals')).toHaveTextContent(
      'pnpm db:migrate --env payments-staging',
    )
    expect(screen.queryByTestId('now-flagged-request')).toBeNull()
    const start = await screen.findByTestId('now-start')
    await waitFor(() =>
      expect(
        within(start).getByRole('button', { name: /New session/ }),
      ).toHaveClass('bg-accent'),
    )
    expect(screen.queryByRole('button', { name: 'Approve once' })).toBeNull()
  })

  it('the proof line says the chain verifies and how many entries the ledger holds', async () => {
    estate()
    renderIntel(<HomeView />)
    const proof = await screen.findByRole('link', { name: /^Chain verified/ })
    await waitFor(() => expect(proof).toHaveTextContent('12,408 entries'))
    expect(proof).toHaveAttribute('href', '/audit')
  })

  it('a role that cannot read the ledger gets the verdict without the count, and no ledger read', async () => {
    estate()
    authState.can = (p) => p !== 'audit:read'
    const recent = vi.spyOn(auditApi, 'recent')
    renderIntel(<HomeView />)
    expect(
      await screen.findByRole('link', { name: /^Chain verified/ }),
    ).toHaveTextContent(/^Chain verified at \d\d:\d\d$/)
    expect(recent).not.toHaveBeenCalled()
  })

  it('asks nothing of what left the home: inventory, security, compliance, health, kill switch, spend', async () => {
    estate()
    const reads = [
      vi.spyOn(inventoryApi, 'summary'),
      vi.spyOn(securityApi, 'findings'),
      vi.spyOn(complianceApi, 'summary'),
      vi.spyOn(healthApi, 'status'),
      vi.spyOn(healthApi, 'incidents'),
      vi.spyOn(killswitchApi, 'state'),
      vi.spyOn(finopsApi, 'summary'),
      vi.spyOn(finopsApi, 'trend'),
    ]
    const queryClient = createTestQueryClient()
    renderIntel(<HomeView />, { queryClient })
    await screen.findByTestId('now-flagged-request')
    await waitFor(() => expect(queryClient.isFetching()).toBe(0))
    for (const read of reads) expect(read).not.toHaveBeenCalled()
  })
})

describe('Estate (RBAC gating + honest states)', () => {
  it('gives a role that reads only the approval queue its pending approvals', async () => {
    authState.can = (p) => p === 'governance:approval:read'
    vi.spyOn(governanceApi, 'listApprovals').mockResolvedValue({
      items: [
        {
          id: 'ap-9',
          action: 'deploy.promote',
          requested_by: 'user:grace',
          status: 'pending',
          required_approvals: 2,
          approve_count: 1,
          reject_count: 0,
          escalated: false,
        },
      ],
      has_more: false,
    } as never)
    renderIntel(<HomeView />)
    expect(await screen.findByTestId('now-approvals')).toHaveTextContent(
      'Deploy promote',
    )
    // A role that may not decide reads the request as a quiet row; the decision stays on
    // the queue, and nothing is flagged for it.
    expect(screen.queryByTestId('now-flagged-request')).toBeNull()
    expect(screen.queryByRole('button', { name: 'Approve once' })).toBeNull()
    expect(screen.queryByText(/Nothing to show yet/i)).toBeNull()
  })

  it('shows the honest empty state when the role can read nothing', () => {
    authState.can = () => false
    renderIntel(<HomeView />)
    expect(screen.getByText(/Nothing to show yet/i)).toBeInTheDocument()
    // No regions, no fabricated numbers.
    expect(screen.queryByRole('link')).toBeNull()
  })

  it('does not add a 16 px stack gap above the work', () => {
    authState.can = () => false
    const { container } = renderIntel(<HomeView />)
    const page = container.querySelector('.gap-0')
    expect(page).not.toBeNull()
    expect(page!.className).not.toMatch(/\bgap-4\b/)
  })
})

describe('Estate — live session cards and the summary sentence (#2245, #2546)', () => {
  // Estate reads two pages: the runs Olivares operates and the live sessions it observes.
  // A live session is a card, an ended one a Recent work row, and the summary sentence
  // says what the sessions that worked today cost. The header holds no lone report button.
  const role = (p: string) =>
    p === 'sessions:live:read' || p === 'sessions:run:read'
  const page = (items: unknown[]) => ({ items, has_more: false }) as never
  const today = () => new Date().toISOString()
  const running = () => ({
    run_ref: 'run-live',
    tenant_id: 'demo',
    state: 'running',
    name: 'Fix the flaky login test',
    provider_driver: 'claude',
    cost_micro_usd: 420_000,
    created_at: today(),
  })
  const ended = () => ({
    run_ref: 'run-done',
    tenant_id: 'demo',
    state: 'stopped',
    name: 'Nightly report',
    cost_micro_usd: 80_000,
    created_at: today(),
  })
  const lastWeek = {
    run_ref: 'run-old',
    tenant_id: 'demo',
    state: 'stopped',
    name: 'Last week',
    cost_micro_usd: 9_000_000,
    created_at: '2026-10-01T08:00:00Z',
  }
  const sentence = () => screen.findByTestId('estate-summary-sentence')
  const stub = (runs: unknown[], live: unknown[] = []) => {
    vi.spyOn(sessionsApi, 'live').mockResolvedValue(page(live))
    vi.spyOn(agentOpsApi, 'listRuns').mockResolvedValue(page(runs))
  }

  it('past FLEET_SESSIONS live sessions the last hour is the fleet panel a build supplies (#2565)', async () => {
    authState.can = role
    panels.estateFleet = (p) => <p>fleet for {p.sessions.length} sessions</p>
    const live = FLEET_SESSIONS + 1
    stub(
      Array.from({ length: live }, (_, i) => ({
        ...running(),
        run_ref: `run-live-${i}`,
        name: `Live ${i}`,
      })),
    )
    renderIntel(<HomeView />)
    expect(
      await screen.findByText(`fleet for ${live} sessions`),
    ).toBeInTheDocument()
  })

  it('the header holds no lone Executive report button', async () => {
    authState.can = role
    stub([running(), ended()])
    const queryClient = createTestQueryClient()
    renderIntel(<HomeView />, { queryClient })
    expect(await screen.findByText('Nightly report')).toBeInTheDocument()
    await waitFor(() => expect(queryClient.isFetching()).toBe(0))
    expect(screen.queryByRole('link', { name: /Executive report/ })).toBeNull()
  })

  it('a live session is a card and an ended one a Recent work row, each once', async () => {
    authState.can = role
    stub([running(), ended()])
    renderIntel(<HomeView />)
    const cards = await screen.findByTestId('now-live')
    expect(within(cards).getByText('Claude Code')).toBeInTheDocument()
    expect(
      within(cards).getByText('Fix the flaky login test'),
    ).toBeInTheDocument()
    expect(within(cards).getByText('$0.42')).toBeInTheDocument()
    expect(within(cards).queryByText('Nightly report')).toBeNull()
    const recent = screen.getByTestId('home-recent')
    expect(within(recent).getByText('Nightly report')).toBeInTheDocument()
    expect(within(recent).queryByText('Fix the flaky login test')).toBeNull()
  })

  it('the sentence sums what the sessions that worked today cost', async () => {
    authState.can = role
    stub([running(), ended(), lastWeek])
    renderIntel(<HomeView />)
    await waitFor(async () =>
      expect(await sentence()).toHaveTextContent(
        /^1 session open · \$0\.50 today$/,
      ),
    )
  })

  it('the sentence says which sessions reported no cost, and a page with more is a floor', async () => {
    authState.can = role
    const unpriced = { ...ended(), run_ref: 'run-free', name: 'Local model' }
    delete (unpriced as { cost_micro_usd?: number }).cost_micro_usd
    vi.spyOn(sessionsApi, 'live').mockResolvedValue(page([]))
    vi.spyOn(agentOpsApi, 'listRuns').mockResolvedValue({
      items: [running(), unpriced],
      has_more: true,
    } as never)
    renderIntel(<HomeView />)
    await waitFor(async () =>
      expect(await sentence()).toHaveTextContent('at least $0.42 today'),
    )
    expect(screen.getByTestId('estate-summary-spend')).toHaveTextContent(
      '1 reported no cost',
    )
  })

  it('a role that reads only live sessions gets cards and the sentence from them', async () => {
    authState.can = (p) => p === 'sessions:live:read'
    const runs = vi.spyOn(agentOpsApi, 'listRuns')
    vi.spyOn(sessionsApi, 'live').mockResolvedValue(
      page([
        {
          session_ref: 'sess-observed',
          attribution: 'legacy',
          cc_state: 'active',
          engine: 'claude',
          input_tokens: 10,
          output_tokens: 10,
          cost_micro_usd: 250_000,
          event_count: 1,
          tool_call_count: 0,
          first_event_at: today(),
          last_event_at: today(),
          duration_seconds: 1,
          critical: false,
        },
      ]),
    )
    renderIntel(<HomeView />)
    const cards = await screen.findByTestId('now-live')
    expect(within(cards).getByText('Claude Code')).toBeInTheDocument()
    await waitFor(async () =>
      expect(await sentence()).toHaveTextContent(
        /^1 session open · \$0\.25 today$/,
      ),
    )
    expect(runs).not.toHaveBeenCalled()
  })

  it('a day with no sessions says nothing runs, with no $0', async () => {
    authState.can = role
    stub([lastWeek])
    const queryClient = createTestQueryClient()
    renderIntel(<HomeView />, { queryClient })
    expect(await screen.findByText('Last week')).toBeInTheDocument()
    await waitFor(() => expect(queryClient.isFetching()).toBe(0))
    expect(await sentence()).toHaveTextContent(/^Nothing is running$/)
  })

  it('when every session is live the cards carry the link to all sessions', async () => {
    authState.can = role
    stub([running()])
    renderIntel(<HomeView />)
    const cards = await screen.findByTestId('now-live')
    expect(
      within(cards).getByRole('link', { name: /All sessions/ }),
    ).toHaveAttribute('href', '/sessions')
    expect(screen.queryByTestId('home-recent')).toBeNull()
  })

  it('a failed Recent work read is still said beside the cards', async () => {
    authState.can = role
    vi.spyOn(sessionsApi, 'live').mockResolvedValue(
      page([
        {
          session_ref: 'sess-observed',
          attribution: 'legacy',
          cc_state: 'active',
          engine: 'codex',
          input_tokens: 0,
          output_tokens: 0,
          cost_micro_usd: 0,
          event_count: 1,
          tool_call_count: 0,
          first_event_at: today(),
          last_event_at: today(),
          duration_seconds: 1,
          critical: false,
        },
      ]),
    )
    vi.spyOn(agentOpsApi, 'listRuns').mockRejectedValue(
      new ApiError(500, 'server_error', 'boom'),
    )
    renderIntel(<HomeView />)
    expect(
      within(await screen.findByTestId('now-live')).getByText('Codex'),
    ).toBeInTheDocument()
    expect(
      await screen.findByText('Couldn’t load recent work'),
    ).toBeInTheDocument()
  })
  it.each([
    ['archived', { archived_at: '2026-10-20T09:00:00Z' }],
    ['snoozed', { snoozed_until: '2999-01-01T00:00:00Z' }],
    ['done', { done: true }],
  ] as const)(
    'leaves a %s session out of Needs you and the summary',
    async (_, mark) => {
      authState.can = role
      stub(
        [
          {
            run_ref: 'run-snoozed',
            tenant_id: 'demo',
            state: 'running',
            claude_session_id: 'sess-snoozed',
            needs_person: {
              code: 'repeated_policy_denials',
              consecutive_denials: 3,
              total_denials: 3,
              reasons: ['Bash'],
            },
            ...mark,
            created_at: today(),
          },
        ],
        [
          {
            session_ref: 'sess-snoozed',
            live_ref: 'live-snoozed',
            attribution: 'legacy',
            cc_state: 'silent_evasion',
            input_tokens: 0,
            output_tokens: 0,
            cost_micro_usd: 0,
            event_count: 1,
            tool_call_count: 0,
            first_event_at: today(),
            last_event_at: today(),
            duration_seconds: 0,
          },
        ],
      )
      const queryClient = createTestQueryClient()
      renderIntel(<HomeView />, { queryClient })
      const needs = await screen.findByTestId('now-needs')
      await waitFor(() => expect(queryClient.isFetching()).toBe(0))
      expect(agentOpsApi.listRuns).toHaveBeenCalled()
      expect(within(needs).queryByTestId('now-needs-row')).toBeNull()
      expect(
        within(needs).getByText('Nothing waits for you.'),
      ).toBeInTheDocument()
      expect(await sentence()).not.toHaveTextContent(/need[s]? you/)
    },
  )
})

describe('Now — the next step once a coding tool is ready', () => {
  // What each tool runs on is the engine's answer (GET provider-profiles/readiness):
  // a tool missing from the map has nothing to run on.
  const runsOn = (answers: Parameters<typeof readinessOf>[0]) =>
    vi
      .spyOn(agentOpsApi, 'toolsReadiness')
      .mockResolvedValue(readinessOf(answers))

  it('New session is the action when a tool is signed in', async () => {
    runsOn({ claude: 'own_login' })
    vi.spyOn(signInApi, 'status').mockImplementation(
      async (driver) =>
        ({
          driver,
          installed: driver === 'claude',
          signed_in: driver === 'claude',
        }) as Awaited<ReturnType<typeof signInApi.status>>,
    )
    renderIntel(<HomeView />)
    // The line is there from the first paint; the tools' answer names it.
    await waitFor(() =>
      expect(screen.getByTestId('now-start')).toHaveTextContent(
        'Claude Code is ready.',
      ),
    )
  })

  it('offers one start on an empty Now: the start line, not a second one in Recent work', async () => {
    runsOn({ claude: 'own_login' })
    vi.spyOn(signInApi, 'status').mockImplementation(
      async (driver) =>
        ({
          driver,
          installed: driver === 'claude',
          signed_in: driver === 'claude',
        }) as Awaited<ReturnType<typeof signInApi.status>>,
    )
    vi.spyOn(agentOpsApi, 'listRuns').mockResolvedValue({
      items: [],
      has_more: false,
    } as never)
    vi.spyOn(sessionsApi, 'live').mockResolvedValue({
      items: [],
      has_more: false,
    } as never)
    renderIntel(<HomeView />)
    expect(await screen.findByTestId('now-start')).toBeInTheDocument()
    expect(await screen.findByText('No sessions yet')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Start a session' })).toBeNull()
  })

  it('a tool that runs on an API key from Providers is ready too', async () => {
    vi.spyOn(signInApi, 'status').mockImplementation(async (driver) => ({
      driver,
      installed: driver === 'claude',
      signed_in: false,
    }))
    runsOn({
      claude: { provider: { provider_ref: 'prv_1', kind: 'anthropic' } },
    })
    renderIntel(<HomeView />)
    await waitFor(() =>
      expect(screen.getByTestId('now-start')).toHaveTextContent(
        'Claude Code is ready.',
      ),
    )
  })

  it('sends an installed tool with a refused API key to Providers instead of sign-in', async () => {
    const user = userEvent.setup()
    vi.spyOn(signInApi, 'status').mockImplementation(async (driver) => ({
      driver,
      installed: driver === 'claude',
      signed_in: false,
    }))
    runsOn({
      claude: {
        provider: {
          provider_ref: 'prv_1',
          kind: 'anthropic',
          display_name: 'Anthropic',
        },
        refused: true,
      },
    })
    renderIntel(<HomeView />)
    const start = await screen.findByTestId('now-start')
    await waitFor(() =>
      expect(start).toHaveTextContent(
        'The API key Anthropic was refused. Replace it under Providers.',
      ),
    )
    expect(within(start).queryByRole('button', { name: 'Sign in' })).toBeNull()
    await user.click(
      within(start).getByRole('button', { name: 'Open Providers' }),
    )
    expect(navigate).toHaveBeenCalledWith({ to: '/providers' })
  })

  // Without Providers access the refusal's only action would open a page this person
  // cannot read: Now says to sign the tool in, as before the engine judged the key.
  it('does not send someone who cannot open Providers there for a refused key', async () => {
    authState.can = (p) => p !== 'sessions:provider:read'
    vi.spyOn(signInApi, 'status').mockImplementation(async (driver) => ({
      driver,
      installed: driver === 'claude',
      signed_in: false,
    }))
    runsOn({
      claude: {
        provider: { provider_ref: 'prv_1', kind: 'anthropic' },
        refused: true,
      },
    })
    renderIntel(<HomeView />)
    const start = await screen.findByTestId('now-start')
    expect(
      await within(start).findByRole('button', { name: 'Sign in' }),
    ).toBeInTheDocument()
    expect(
      within(start).queryByRole('button', { name: 'Open Providers' }),
    ).toBeNull()
  })

  // WEB on 09b: an editor's Home asked GET agenttools/sign-in for both tools and got
  // 403 twice per load. Only a system administrator may read it, so nobody else asks.
  it('never asks for the tools\u2019 own status for someone who is not a system administrator', async () => {
    authState.isSuperadmin = false
    runsOn({ claude: 'own_login' })
    const status = vi.spyOn(signInApi, 'status')
    renderIntel(<HomeView />)
    await waitFor(() =>
      expect(screen.getByTestId('now-start')).toHaveTextContent(
        'Claude Code is ready.',
      ),
    )
    expect(status).not.toHaveBeenCalled()
  })

  it('a fresh install shows the real next step, signing the tool in (N2 J8)', async () => {
    runsOn({})
    vi.spyOn(signInApi, 'status').mockImplementation(
      async (driver) =>
        ({
          driver,
          installed: driver === 'claude',
          signed_in: false,
        }) as Awaited<ReturnType<typeof signInApi.status>>,
    )
    renderIntel(<HomeView />)
    await waitFor(() =>
      expect(screen.getByTestId('now-start')).toHaveTextContent(
        'Sign Claude Code in to start a session.',
      ),
    )
  })
})

describe('Now counts sessions, not the rows each one wrote', () => {
  it('one launch with its hook and usage rows is one live session', async () => {
    const RUN = '01a0f8bd-e094-7cfd-ace2-5e36665dca8b'
    const OSN = 'osn_01a0f8bd-e095-7203-b4fa-2b332c008029'
    const base = {
      cc_state: 'active',
      input_tokens: 0,
      output_tokens: 0,
      cost_micro_usd: 0,
      event_count: 0,
      tool_call_count: 0,
      first_event_at: '2026-10-01T18:33:08Z',
      last_event_at: '2026-10-01T18:33:08Z',
      duration_seconds: 0,
    }
    vi.spyOn(sessionsApi, 'live').mockResolvedValue({
      items: [
        {
          ...base,
          session_ref: 'claude-sid',
          live_ref: 'lr-m',
          attribution: 'managed',
          canonical_sid: OSN,
          run_ref: RUN,
          provider_profile_ref: 'ppf_1',
        },
        {
          ...base,
          session_ref: RUN,
          live_ref: 'lr-hook',
          attribution: 'legacy',
        },
        {
          ...base,
          session_ref: OSN,
          live_ref: 'lr-usage',
          attribution: 'legacy',
        },
      ],
      has_more: false,
    } as never)
    vi.spyOn(agentOpsApi, 'listRuns').mockResolvedValue({
      items: [
        {
          run_ref: RUN,
          state: 'running',
          transport: 'stream-json',
          provider_profile_ref: 'ppf_1',
          live_ref: 'lr-m',
          claude_session_id: 'claude-sid',
        },
      ],
      has_more: false,
    } as never)
    renderIntel(<HomeView />)
    await waitFor(async () =>
      expect(
        await screen.findByTestId('estate-summary-sentence'),
      ).toHaveTextContent(/^1 session open/),
    )
  })
})

describe('Now: the header block holds still while the page loads (#3121)', () => {
  const never = () => new Promise<never>(() => {})

  it('shows New session from the first paint, before the tools answer', () => {
    vi.spyOn(signInApi, 'status').mockImplementation(never)
    vi.spyOn(agentOpsApi, 'toolsReadiness').mockImplementation(never)
    vi.spyOn(sessionsApi, 'live').mockImplementation(never)
    vi.spyOn(agentOpsApi, 'listRuns').mockImplementation(never)
    const { container } = renderIntel(<HomeView />)
    const header = container.querySelector(
      '[data-slot="page-header"]',
    ) as HTMLElement
    // Nothing waits for an answer to take its place: the verb is there at once.
    const start = within(header).getByTestId('now-start')
    expect(
      within(start).getByRole('button', { name: /New session/ }),
    ).toBeInTheDocument()
  })

  it('puts New session first: the proof link comes after the verb', async () => {
    vi.spyOn(signInApi, 'status').mockImplementation(
      async (driver) =>
        ({
          driver,
          installed: driver === 'claude',
          signed_in: driver === 'claude',
        }) as Awaited<ReturnType<typeof signInApi.status>>,
    )
    vi.spyOn(agentOpsApi, 'toolsReadiness').mockResolvedValue(
      readinessOf({ claude: 'own_login' }),
    )
    vi.spyOn(sessionsApi, 'live').mockResolvedValue(page([]))
    vi.spyOn(agentOpsApi, 'listRuns').mockResolvedValue(
      page([
        {
          run_ref: 'r1',
          tenant_id: 'demo',
          state: 'completed',
          provider_driver: 'claude',
          created_at: new Date().toISOString(),
          proof: { ledger_entries: 4, chain_verified: true },
        },
      ]),
    )
    const { container } = renderIntel(<HomeView />)
    const proof = await screen.findByRole('link', { name: /^Chain verified/ })
    const header = container.querySelector(
      '[data-slot="page-header"]',
    ) as HTMLElement
    const verb = within(header).getByRole('button', { name: /New session/ })
    // The first control of the page is the verb; the proof link follows it.
    expect(header.querySelector('a[href], button')).toBe(verb)
    expect(
      verb.compareDocumentPosition(proof) & Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy()
  })

  it('gives a role that cannot start sessions no empty row under the title', () => {
    authState.can = (p) => p !== 'sessions:run:write'
    vi.spyOn(sessionsApi, 'live').mockImplementation(never)
    vi.spyOn(agentOpsApi, 'listRuns').mockImplementation(never)
    renderIntel(<HomeView />)
    expect(screen.queryByTestId('now-header-line')).toBeNull()
    expect(screen.queryByTestId('now-start')).toBeNull()
  })
})
