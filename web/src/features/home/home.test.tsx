// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { ReactNode } from 'react'
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
import { killswitchApi } from '@/features/killswitch/api'
import { governanceApi } from '@/features/governance/api'
import { securityApi } from '@/features/security/api'
import { healthApi } from '@/features/health/api'
import { complianceApi } from '@/features/compliance/api'
import { sessionsApi } from '@/features/sessions/api'
import { inventoryApi } from '@/features/inventory/api'
import { agentOpsApi } from '@/features/agentops/api'
import { useNewSessionDialog } from '@/features/first-hour/new-session-store'
import { HomeView } from './home-view'
import './i18n'

// Render TanStack Router <Link> as a plain anchor (no RouterProvider in jsdom) — the
// established pattern across the view tests.
const panels = vi.hoisted(() => ({
  complianceView: undefined as undefined | (() => null),
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

  vi.restoreAllMocks()
  compliance.opened = false
  navigate.mockReset()
  authState.can = () => true
  authState.isSuperadmin = true
  authState.principal = { superadmin: true, grants: [] }
})

const page = (items: unknown[], has_more = false) =>
  ({ items, has_more }) as never

/** A pending approval the Overview lists under Needs you. */
const pendingApproval = (id: string) => ({
  id,
  action: 'claude.hook.tool',
  subject_kind: 'claude.tool',
  requested_by: 'user:sam',
  review: { tool: 'Command', text: 'pnpm test' },
  status: 'pending',
  required_approvals: 1,
  approve_count: 0,
  reject_count: 0,
  escalated: false,
})

describe('Overview: what needs you, what runs, what ended', () => {
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
  }

  it('Overview is the strip, Needs you, Running and Recent: no headline, no New session', async () => {
    estate()
    vi.mocked(agentOpsApi.listRuns).mockResolvedValue(
      page([
        run({ run_ref: 'r1', name: 'Refactor cart totals' }),
        run({
          run_ref: 'r4',
          state: 'failed',
          name: 'Cart rounding',
          cost_micro_usd: 100_000,
        }),
      ]),
    )
    renderIntel(<HomeView />)
    const strip = await screen.findByRole('navigation', {
      name: 'Sessions at a glance',
    })
    expect(strip).toHaveTextContent('1 working · 1 failed today')
    expect(
      screen.getByRole('heading', { name: 'Needs you' }),
    ).toBeInTheDocument()
    const running = screen.getByRole('region', { name: 'Running' })
    expect(running).toHaveTextContent('Refactor cart totals')
    const recent = screen.getByRole('region', { name: 'Recent' })
    expect(recent).toHaveTextContent('Cart rounding')
    expect(screen.queryByTestId('estate-summary')).toBeNull()
    expect(screen.queryByTestId('now-start')).toBeNull()
    expect(screen.queryByText(/reported no cost/)).toBeNull()
    expect(screen.queryByTestId('estate-swimlane')).toBeNull()
  })

  it('the request takes one click', async () => {
    estate()
    const { container } = renderIntel(<HomeView />)
    // The flagged request, decided from here.
    const flagged = await screen.findByTestId('now-flagged-request')
    expect(flagged).toHaveTextContent('pnpm db:migrate --env payments-staging')
    expect(
      within(flagged).getByRole('button', { name: 'Approve once' }),
    ).toBeInTheDocument()
    expectNoRawI18nKeys(container)
  })

  it('one filled orange control: Approve', async () => {
    estate()
    const { container } = renderIntel(<HomeView />)
    await screen.findByTestId('now-flagged-request')
    const orange = () =>
      [...container.querySelectorAll('button, a')].filter((el) =>
        el.classList.contains('bg-accent'),
      )
    await waitFor(() => expect(orange()).toHaveLength(1))
    expect(orange()[0]).toHaveTextContent('Approve')
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

  it('with only requests the person raised, nothing offers Approve', async () => {
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
    expect(screen.queryByRole('button', { name: 'Approve once' })).toBeNull()
  })

  it('says its name once: the top bar names the page, the heading is not painted', () => {
    estate()
    renderIntel(<HomeView />)
    expect(
      screen.getByRole('heading', { level: 1, name: 'Overview' }),
    ).toHaveClass('sr-only')
  })

  it('leaves the chain proof to Activity & audit: no proof chip and no ledger read on the first screen', async () => {
    estate()
    const recent = vi.spyOn(auditApi, 'recent')
    renderIntel(<HomeView />)
    // The runs, which carry the chain verdict, have answered.
    await screen.findByRole('region', { name: 'Running' })
    expect(screen.queryByText(/^Chain verified/)).toBeNull()
    expect(screen.queryByText(/^Chain broken/)).toBeNull()
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

describe('Overview (RBAC gating + honest states)', () => {
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

describe('Overview — Running and Recent rows', () => {
  // Overview reads two pages: the runs Olivares operates and the live sessions it observes.
  // A live session is a Running row and an ended one a Recent row. The header holds no lone
  // report button. The person also opens Usage, so the strip is drawn.
  const role = (p: string) =>
    p === 'sessions:live:read' ||
    p === 'sessions:run:read' ||
    p === 'sessions:account:read'
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
  const stub = (runs: unknown[], live: unknown[] = []) => {
    vi.spyOn(sessionsApi, 'live').mockResolvedValue(page(live))
    vi.spyOn(agentOpsApi, 'listRuns').mockResolvedValue(page(runs))
  }

  it('the header holds no lone Executive report button', async () => {
    authState.can = role
    stub([running(), ended()])
    const queryClient = createTestQueryClient()
    renderIntel(<HomeView />, { queryClient })
    expect(await screen.findByText('Nightly report')).toBeInTheDocument()
    await waitFor(() => expect(queryClient.isFetching()).toBe(0))
    expect(screen.queryByRole('link', { name: /Executive report/ })).toBeNull()
  })

  it('a live session is a Running row and an ended one a Recent row, each once', async () => {
    authState.can = role
    stub([running(), ended()])
    renderIntel(<HomeView />)
    const live = await screen.findByRole('region', { name: 'Running' })
    expect(
      within(live).getByText('Fix the flaky login test'),
    ).toBeInTheDocument()
    expect(within(live).queryByText('Nightly report')).toBeNull()
    const recent = await screen.findByRole('region', { name: 'Recent' })
    expect(within(recent).getByText('Nightly report')).toBeInTheDocument()
    expect(within(recent).queryByText('Fix the flaky login test')).toBeNull()
  })

  it('an idle session is not Running: Running counts what the strip counts, the idle one is Recent', async () => {
    authState.can = role
    stub([
      running(),
      {
        ...running(),
        run_ref: 'run-idle',
        state: 'idle',
        name: 'Answered, awaiting input',
      },
    ])
    renderIntel(<HomeView />)
    const live = await screen.findByRole('region', { name: 'Running' })
    expect(
      within(live).getByText('Fix the flaky login test'),
    ).toBeInTheDocument()
    expect(within(live).queryByText('Answered, awaiting input')).toBeNull()
    const recent = await screen.findByRole('region', { name: 'Recent' })
    expect(
      within(recent).getByText('Answered, awaiting input'),
    ).toBeInTheDocument()
  })

  it('when every session is live Running is the one list: no empty Recent, and it links to all', async () => {
    authState.can = role
    stub([running()])
    renderIntel(<HomeView />)
    const live = await screen.findByRole('region', { name: 'Running' })
    expect(
      within(live).getByText('Fix the flaky login test'),
    ).toBeInTheDocument()
    expect(
      within(live).getByRole('link', { name: /All sessions/ }),
    ).toHaveAttribute('href', '/sessions')
    expect(screen.queryByRole('region', { name: 'Recent' })).toBeNull()
  })

  it('a failed Recent read is still said beside Running', async () => {
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
    await screen.findByRole('region', { name: 'Running' })
    expect(
      await screen.findByText('Couldn’t load recent work'),
    ).toBeInTheDocument()
  })
  it.each([
    ['archived', { archived_at: '2026-10-20T09:00:00Z' }],
    ['snoozed', { snoozed_until: '2999-01-01T00:00:00Z' }],
    ['done', { done: true }],
  ] as const)(
    'leaves a %s session out of Needs you and the strip',
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
      expect(screen.queryByText(/\d+ needs? you/)).toBeNull()
    },
  )

  it('the strip and Needs you count the same rows', async () => {
    authState.can = () => true
    vi.spyOn(governanceApi, 'listApprovals').mockResolvedValue(
      page([pendingApproval('ap-one'), pendingApproval('ap-two')]),
    )
    vi.spyOn(agentOpsApi, 'listRuns').mockResolvedValue(page([]))
    vi.spyOn(sessionsApi, 'live').mockResolvedValue(
      page([
        {
          session_ref: 'sess-silent',
          attribution: 'legacy',
          cc_state: 'silent_evasion',
          engine: 'claude',
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
    renderIntel(<HomeView />)
    const needs = await screen.findByTestId('now-needs')
    // Two pending approvals and one silent session: the section's count and the strip's
    // read the same rows, so they say the same number.
    await waitFor(() => {
      const heading = within(needs).getByRole('heading', { name: 'Needs you' })
      expect(heading.nextElementSibling?.textContent).toBe('3')
      const strip = screen.getByText(/\d+ needs? you/).textContent ?? ''
      expect(strip).toMatch(/^3 needs? you$/)
    })
  })

  it('the strip count is plain text, not a link to the Sessions filter, when approvals are in it', async () => {
    authState.can = () => true
    vi.spyOn(governanceApi, 'listApprovals').mockResolvedValue(
      page([pendingApproval('ap-one'), pendingApproval('ap-two')]),
    )
    vi.spyOn(agentOpsApi, 'listRuns').mockResolvedValue(page([]))
    vi.spyOn(sessionsApi, 'live').mockResolvedValue(page([]))
    renderIntel(<HomeView />)
    // Two approvals and no session: the Sessions filter would list nothing for this count.
    const strip = await screen.findByText(/^2 needs? you$/)
    expect(strip.closest('a')).toBeNull()
  })

  it('the strip prints no needs-you count when the live read fails but runs and approvals answer', async () => {
    authState.can = () => true
    vi.spyOn(governanceApi, 'listApprovals').mockResolvedValue(
      page([pendingApproval('ap-one'), pendingApproval('ap-two')]),
    )
    vi.spyOn(agentOpsApi, 'listRuns').mockResolvedValue(page([]))
    vi.spyOn(sessionsApi, 'live').mockRejectedValue(
      new ApiError(500, 'server_error', 'boom'),
    )
    renderIntel(<HomeView />)
    const needs = await screen.findByTestId('now-needs')
    await within(needs).findByText('The sessions could not be read.')
    await screen.findByTestId('now-approvals')
    expect(screen.queryByText(/\d+ needs? you/)).toBeNull()
    expect(
      within(needs).getByRole('heading', { name: 'Needs you' })
        .nextElementSibling,
    ).toBeNull()
  })

  it('the strip prints no needs-you count while the approvals read has failed', async () => {
    authState.can = () => true
    vi.spyOn(governanceApi, 'listApprovals').mockRejectedValue(
      new ApiError(500, 'server_error', 'boom'),
    )
    vi.spyOn(agentOpsApi, 'listRuns').mockResolvedValue(page([]))
    vi.spyOn(sessionsApi, 'live').mockResolvedValue(
      page([
        {
          session_ref: 'sess-silent',
          attribution: 'legacy',
          cc_state: 'silent_evasion',
          engine: 'claude',
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
    renderIntel(<HomeView />)
    // The approvals failed: the strip must not print a count that leaves them out.
    await screen.findByTestId('now-approvals-error')
    expect(screen.queryByText(/\d+ needs? you/)).toBeNull()
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
    const live = await screen.findByRole('region', { name: 'Running' })
    await waitFor(() =>
      expect(within(live).getAllByRole('listitem')).toHaveLength(1),
    )
  })
})

describe('Overview: the folders of a person who is given them', () => {
  // The set the engine gives a member (memberModulePerms): its sessions and
  // its folders, with no folder administration, no provider account and no
  // organization read.
  const MEMBER_SET = new Set([
    'sessions:workspace:read',
    'sessions:workspace:write',
    'sessions:run:read',
    'sessions:run:write',
    'sessions:stop:write',
    'sessions:output:read',
    'sessions:live:read',
    'sessions:profile:read',
  ])
  const member = (p: string) => MEMBER_SET.has(p)
  const folder = (over: Record<string, unknown> = {}) => ({
    workspace_ref: 'ws-invoices',
    name: 'Invoices 2026',
    root_path: '/srv/finance/invoices-2026',
    mount_mode: 'rw',
    max_read_bytes: 0,
    dlp_mode: 'off',
    state: 'active',
    ...over,
  })
  const sessions = () => {
    const at = new Date().toISOString()
    vi.spyOn(sessionsApi, 'live').mockResolvedValue(page([]))
    vi.spyOn(agentOpsApi, 'listRuns').mockResolvedValue(
      page([
        {
          run_ref: 'r1',
          tenant_id: 'demo',
          state: 'running',
          name: 'Payment reminders',
          provider_driver: 'claude',
          cost_micro_usd: 1_420_000,
          created_at: at,
          last_activity_at: at,
        },
      ]),
    )
  }
  // A session the engine observes at work, with what it reported it cost.
  const observed = () => {
    const at = new Date().toISOString()
    return {
      session_ref: 'sess-observed',
      attribution: 'legacy',
      cc_state: 'active',
      engine: 'claude',
      summary: 'Draft the March reminders',
      input_tokens: 0,
      output_tokens: 0,
      cost_micro_usd: 2_750_000,
      event_count: 2,
      tool_call_count: 2,
      first_event_at: at,
      last_event_at: at,
      duration_seconds: 0,
      critical: false,
    }
  }

  afterEach(() => {
    useNewSessionDialog.setState({
      open: false,
      draft: null,
      opener: null,
      advanced: null,
    })
  })

  it('lists each folder with what a session may do there, and Start opens New session on it', async () => {
    authState.can = member
    sessions()
    const list = vi.spyOn(agentOpsApi, 'listWorkspaces').mockResolvedValue(
      page([
        folder(),
        folder({
          workspace_ref: 'ws-templates',
          name: 'Document templates',
          root_path: '/srv/finance/templates',
          mount_mode: 'ro',
        }),
      ]),
    )
    renderIntel(<HomeView />)
    const folders = await screen.findByRole('region', { name: 'Your folders' })
    const start = await within(folders).findByRole('button', {
      name: 'Start in Invoices 2026',
      description: 'Can work',
    })
    expect(
      within(folders).getByRole('button', {
        name: 'Start in Document templates',
        description: 'Read only',
      }),
    ).toBeInTheDocument()
    expect(list).toHaveBeenCalledWith(
      { state: 'active', limit: 200 },
      expect.objectContaining({ tenant: 'demo' }),
    )
    // The folders come first; the person's sessions follow them.
    expect(await screen.findByText('Payment reminders')).toBeInTheDocument()
    await userEvent.click(start)
    // Each fact apart: a deep match of the store walks the opener's whole DOM
    // and React graph, and that walk does not end.
    const dialog = useNewSessionDialog.getState()
    expect(dialog.open).toBe(true)
    expect(dialog.draft).toEqual({ workspace: 'ws-invoices' })
    // Focus returns to the Start the person pressed.
    expect(dialog.opener).toBe(start)
  })

  it('says Can work wherever a session writes, a folder it may not erase in too, and Read only where it only reads', async () => {
    authState.can = member
    sessions()
    vi.spyOn(agentOpsApi, 'listWorkspaces').mockResolvedValue(
      page([
        folder(),
        folder({
          workspace_ref: 'ws-contracts',
          name: 'Contracts',
          root_path: '/srv/finance/contracts',
          mount_mode: 'write_no_delete',
        }),
        folder({
          workspace_ref: 'ws-templates',
          name: 'Document templates',
          root_path: '/srv/finance/templates',
          mount_mode: 'ro',
        }),
      ]),
    )
    renderIntel(<HomeView />)
    const folders = await screen.findByRole('region', { name: 'Your folders' })
    expect(
      await within(folders).findByRole('button', {
        name: 'Start in Contracts',
        description: 'Can work',
      }),
    ).toBeInTheDocument()
    expect(
      within(folders).getByRole('button', {
        name: 'Start in Invoices 2026',
        description: 'Can work',
      }),
    ).toBeInTheDocument()
    expect(
      within(folders).getByRole('button', {
        name: 'Start in Document templates',
        description: 'Read only',
      }),
    ).toBeInTheDocument()
  })

  it('a person with no folder yet is told who gives one, and offered nothing to start', async () => {
    authState.can = member
    sessions()
    vi.spyOn(agentOpsApi, 'listWorkspaces').mockResolvedValue(page([]))
    renderIntel(<HomeView />)
    const folders = await screen.findByRole('region', { name: 'Your folders' })
    expect(
      await within(folders).findByText('You have no folders yet'),
    ).toBeInTheDocument()
    expect(
      within(folders).getByText(
        'When your administrator gives you access to a folder, it will show here.',
      ),
    ).toBeInTheDocument()
    expect(within(folders).queryByRole('button')).toBeNull()
    // Its sessions still show beside it.
    expect(await screen.findByText('Payment reminders')).toBeInTheDocument()
  })

  it('a folders read that failed says so with Retry, never that there are none', async () => {
    authState.can = member
    sessions()
    const list = vi
      .spyOn(agentOpsApi, 'listWorkspaces')
      .mockRejectedValueOnce(
        new ApiError(500, 'server_error', 'boom', 'req-folders-1'),
      )
      .mockResolvedValue(page([folder()]))
    renderIntel(<HomeView />)
    const folders = await screen.findByRole('region', { name: 'Your folders' })
    const failure = await within(folders).findByRole('alert')
    expect(failure).toHaveTextContent('Couldn’t load your folders')
    expect(failure).toHaveTextContent('req-folders-1')
    expect(within(folders).queryByText('You have no folders yet')).toBeNull()
    await userEvent.click(
      within(failure).getByRole('button', { name: 'Retry' }),
    )
    expect(
      await within(folders).findByRole('button', {
        name: 'Start in Invoices 2026',
      }),
    ).toBeInTheDocument()
    expect(list).toHaveBeenCalledTimes(2)
  })

  it('a refused folders read says so without Retry, never a failure', async () => {
    authState.can = member
    sessions()
    vi.spyOn(agentOpsApi, 'listWorkspaces').mockRejectedValue(
      new ApiError(403, 'forbidden', 'denied', 'req-folders-2'),
    )
    renderIntel(<HomeView />)
    const folders = await screen.findByRole('region', { name: 'Your folders' })
    expect(
      await within(folders).findByText('You do not have access to this.'),
    ).toBeInTheDocument()
    expect(within(folders).queryByRole('alert')).toBeNull()
    expect(within(folders).queryByRole('button', { name: 'Retry' })).toBeNull()
    expect(within(folders).queryByText('You have no folders yet')).toBeNull()
  })

  it('says, while the read is out, that it is loading the folders', async () => {
    authState.can = member
    sessions()
    vi.spyOn(agentOpsApi, 'listWorkspaces').mockReturnValue(
      new Promise(() => {}),
    )
    renderIntel(<HomeView />)
    const folders = await screen.findByRole('region', { name: 'Your folders' })
    expect(within(folders).getByRole('status')).toHaveTextContent(
      'Loading your folders',
    )
  })

  it('a person without the Usage place sees no money anywhere on the page, and a session row keeps its other facts', async () => {
    authState.can = member
    sessions()
    vi.spyOn(sessionsApi, 'live').mockResolvedValue(page([observed()]))
    vi.spyOn(agentOpsApi, 'listWorkspaces').mockResolvedValue(page([]))
    renderIntel(
      <main>
        <HomeView />
      </main>,
    )
    const running = await screen.findByRole('region', { name: 'Running' })
    // The row says what it did, and no slot is left where the cost stood.
    const facts = await within(running).findByText(/2 tool calls/)
    expect(facts).toHaveTextContent(/^· 2 tool calls$/)
    const main = screen.getByRole('main')
    expect(main).not.toHaveTextContent('$')
    // Nor on a hover title or a name read aloud.
    expect(main.innerHTML).not.toMatch(/\$\s?\d/)
  })

  it('a person without the Usage place meets no Today strip: the sections below tell the work', async () => {
    authState.can = member
    sessions()
    vi.spyOn(agentOpsApi, 'listWorkspaces').mockResolvedValue(page([]))
    renderIntel(<HomeView />)
    const running = await screen.findByRole('region', { name: 'Running' })
    expect(within(running).getByText('Payment reminders')).toBeInTheDocument()
    expect(
      screen.queryByRole('navigation', { name: 'Sessions at a glance' }),
    ).toBeNull()
  })

  it('a person who can open Usage reads what each session cost on its row, as before', async () => {
    authState.can = () => true
    sessions()
    vi.spyOn(sessionsApi, 'live').mockResolvedValue(page([observed()]))
    vi.spyOn(governanceApi, 'listApprovals').mockResolvedValue(page([]))
    renderIntel(<HomeView />)
    const running = await screen.findByRole('region', { name: 'Running' })
    await waitFor(() =>
      expect(running).toHaveTextContent('2 tool calls · $2.75'),
    )
  })

  it('a person given many folders sees the eight newest above their sessions, and the rest on Show all', async () => {
    authState.can = member
    sessions()
    vi.spyOn(agentOpsApi, 'listWorkspaces').mockResolvedValue(
      page(
        Array.from({ length: 10 }, (_, i) =>
          folder({ workspace_ref: `ws-${i}`, name: `Folder ${i}` }),
        ),
      ),
    )
    renderIntel(<HomeView />)
    const folders = await screen.findByRole('region', { name: 'Your folders' })
    await within(folders).findByRole('button', { name: 'Start in Folder 0' })
    expect(
      within(folders).getAllByRole('button', { name: /^Start in / }),
    ).toHaveLength(8)
    expect(
      within(folders).queryByRole('button', { name: 'Start in Folder 8' }),
    ).toBeNull()
    await userEvent.click(
      within(folders).getByRole('button', { name: 'Show all folders (10)' }),
    )
    expect(
      within(folders).getAllByRole('button', { name: /^Start in / }),
    ).toHaveLength(10)
    // The focus Show all held moves to the first folder it opened.
    expect(
      within(folders).getByRole('button', { name: 'Start in Folder 8' }),
    ).toHaveFocus()
    expect(
      within(folders).queryByRole('button', { name: /^Show all/ }),
    ).toBeNull()
  })

  it('an administrator, who registers the folders, keeps the Overview as it is: its cost, and no folder list or read', async () => {
    authState.can = () => true
    sessions()
    vi.spyOn(governanceApi, 'listApprovals').mockResolvedValue(page([]))
    const list = vi.spyOn(agentOpsApi, 'listWorkspaces')
    renderIntel(<HomeView />)
    const strip = await screen.findByRole('navigation', {
      name: 'Sessions at a glance',
    })
    await waitFor(() => expect(strip).toHaveTextContent('$1.42 today'))
    expect(screen.queryByRole('region', { name: 'Your folders' })).toBeNull()
    expect(list).not.toHaveBeenCalled()
  })
})
