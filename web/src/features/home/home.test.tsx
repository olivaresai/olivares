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
import { FEATURE_EXTENSIONS } from '@/features/extensions'

const businessFinops = FEATURE_EXTENSIONS.some((view) => view.id === 'finops')
import { finopsApi } from '@/features/finops/api'
import { governanceApi } from '@/features/governance/api'
import { killswitchApi } from '@/features/killswitch/api'
import { securityApi } from '@/features/security/api'
import { healthApi } from '@/features/health/api'
import { complianceApi } from '@/features/compliance/api'
import { readinessOf } from '@/features/first-hour/readiness.fixture'
import { signInApi } from '@/features/first-hour/api'
import { sessionsApi } from '@/features/sessions/api'
import { inventoryApi } from '@/features/inventory/api'
import { agentOpsApi } from '@/features/agentops/api'
import { useModulesStore } from '@/stores/modules'
import {
  finopsForecastFixture,
  finopsSummaryFixture,
  finopsTrendFixture,
  complianceSummary,
  healthIncidentsFixture,
  healthStatusFixture,
  securityFindingsFixture,
} from '@/features/executive/fixtures'
import { EstateTile } from './components'
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
  principal: { superadmin: true, grants: [] },
}))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))

afterEach(() => {
  panels.complianceView = undefined

  vi.restoreAllMocks()
  compliance.opened = false
  navigate.mockReset()
  authState.can = () => true
  authState.isSuperadmin = true
})

describe('EstateTile (the three honest states)', () => {
  it('ready: links to its module and shows the figure', () => {
    renderIntel(
      <EstateTile
        to="/finops"
        icon={<span />}
        label="Spend"
        value="$1.2k"
        caption="last 30 days"
        state="ready"
      />,
    )
    expect(screen.getByRole('link')).toHaveAttribute('href', '/finops')
    expect(screen.getByText('$1.2k')).toBeInTheDocument()
  })

  it('loading: no link, no figure (skeletons only)', () => {
    renderIntel(
      <EstateTile
        to="/finops"
        icon={<span />}
        label="Spend"
        value="$1.2k"
        state="loading"
      />,
    )
    expect(screen.queryByRole('link')).toBeNull()
    // The figure is NOT shown while loading — no half-rendered number.
    expect(screen.queryByText('$1.2k')).toBeNull()
  })

  /**
   * THE TONE IS THE SIGNAL, AND IT HAS TO REACH THE DOM.
   *
   * ⛔ WHY THIS REPLACED A CASE THAT PASSED. The case here used to supply its OWN
   *    `<span className="text-warning">` as the caption and then assert that the span it
   *    had just written carried `text-warning` — so it held whether or not `tone` did
   *    anything, and it held while `tone` was destructured away and reached no element
   *    at all. No caller writes a pre-toned caption: the four in `home-view.tsx` pass a
   *    translated STRING and the tone beside it.
   *
   * ⛔ AND IT IS THE STATE WORD, NEVER THE NUMERAL. The work-first pass took colour off
   *    the figures on purpose — a grid of tinted numerals is a grid with no emphasis —
   *    and the bar's §4 keeps ok/warn/fail on the semantic tokens while reserving the
   *    orange accent for selection, the primary action and links. The caption line is
   *    where a tile says what STATE it is in, so that is where the token goes.
   */
  it.each([
    ['warning', 'text-warning'],
    ['danger', 'text-danger'],
    ['success', 'text-success'],
  ] as const)(
    'tone %s tints the state word and never the numeral',
    (tone, token) => {
      renderIntel(
        <EstateTile
          to="/sessions"
          icon={<span />}
          label="Live sessions"
          value="1"
          caption="2 of 3 healthy"
          tone={tone}
          state="ready"
        />,
      )
      const value = screen.getByTestId('estate-kpi-value')
      expect(value).toHaveTextContent('1')
      expect(value.className).toMatch(/text-foreground/)
      expect(value.className).not.toMatch(
        /text-warning|text-danger|text-success|text-accent/,
      )
      const state = screen.getByTestId('estate-tile-state')
      expect(state).toHaveTextContent('2 of 3 healthy')
      expect(state).toHaveClass(token)
    },
  )

  it('no tone: the state word keeps the caption\u2019s own muted register', () => {
    renderIntel(
      <EstateTile
        to="/sessions"
        icon={<span />}
        label="Live sessions"
        value="1"
        caption="3 of 3 healthy"
        state="ready"
      />,
    )
    // The CONTROL for the three above: without a tone nothing is tinted, so the token
    // they assert is the tone's doing and not a class the tile always writes.
    expect(screen.getByTestId('estate-tile-state').className).not.toMatch(
      /text-warning|text-danger|text-success|text-accent/,
    )
  })

  it('a tone does not tint a figure that is not there (unavailable)', () => {
    renderIntel(
      <EstateTile
        to="/health"
        icon={<span />}
        label="Health"
        value="2/3"
        caption="1 down"
        tone="danger"
        state="unavailable"
      />,
    )
    // Same rule as the floor marker (`partial`): a tone qualifies a figure, and with no
    // figure on screen there is nothing to qualify — the retry hint is the whole
    // message, and a red one would claim the estate is down when what is down is the read.
    expect(screen.queryByTestId('estate-tile-state')).toBeNull()
    expect(screen.getByText(/Couldn't load/i).className).not.toMatch(
      /text-danger/,
    )
  })

  it('unavailable: an em-dash + retry hint, never a fabricated 0, still links out', () => {
    renderIntel(
      <EstateTile
        to="/health"
        icon={<span />}
        label="Health"
        value="42"
        state="unavailable"
      />,
    )
    expect(screen.getByRole('link')).toHaveAttribute('href', '/health')
    expect(screen.getByText('—')).toBeInTheDocument()
    expect(screen.getByText(/Couldn't load/i)).toBeInTheDocument()
    // The would-be value is never shown when the source failed.
    expect(screen.queryByText('42')).toBeNull()
  })
})

describe('HomeView (RBAC gating + honest states)', () => {
  it('mounts only the tiles the role can read', async () => {
    // The permissions the two tiles' own requests require: finopsApi.summary/trend/
    // forecast are gated on finops:spend:read and securityApi.findings() on
    // security:finding:read. The coarse `finops:read` / `security:read` this used to
    // name were never declared by the engine.
    authState.can = (p) =>
      p === 'finops:spend:read' || p === 'security:finding:read'
    vi.spyOn(finopsApi, 'summary').mockResolvedValue(finopsSummaryFixture)
    vi.spyOn(finopsApi, 'trend').mockResolvedValue(finopsTrendFixture)
    vi.spyOn(finopsApi, 'forecast').mockResolvedValue(finopsForecastFixture)
    vi.spyOn(securityApi, 'findings').mockResolvedValue(securityFindingsFixture)

    const { container } = renderIntel(<HomeView />)

    expect(await screen.findByText('Security')).toBeInTheDocument()
    if (businessFinops) {
      // The paid hook's answers can change the overview layout while this tile mounts.
      await waitFor(() => expect(screen.getByText('Spend')).toBeInTheDocument())
    } else {
      expect(screen.queryByText('Spend')).toBeNull()
      expect(finopsApi.summary).not.toHaveBeenCalled()
      expect(finopsApi.trend).not.toHaveBeenCalled()
      expect(finopsApi.forecast).not.toHaveBeenCalled()
    }
    // The front door reuses the executive tiles, whose namespace this chunk has to
    // carry: `DeltaCaption` printed `cost.deltaUp` here until executive/components.tsx
    // started registering it. Wait for that caption to MOUNT before sweeping — it
    // arrives with the trend query, one tick after "Spend", and a sweep that runs
    // early passes over an empty slot. The wait keys on the icon, not on the text, so
    // it cannot depend on the namespace under scrutiny.
    if (businessFinops) {
      await waitFor(() =>
        expect(
          container.querySelector(
            'svg.lucide-trending-up, svg.lucide-trending-down, svg.lucide-minus',
          ),
        ).not.toBeNull(),
      )
    }
    expectNoRawI18nKeys(container)
    // A viewer never sees a KPI whose module their role could not open (docs/SECURITY-HARDENING.md).
    expect(screen.queryByText('Live sessions')).toBeNull()
    expect(screen.queryByText('Health & SLA')).toBeNull()
    expect(screen.queryByText('Inventory')).toBeNull()
    expect(screen.queryByText('Compliance')).toBeNull()
    expect(screen.queryByText('Kill switch')).toBeNull()
  })

  it('gives a role that reads only the approval queue its pending approvals on Now', async () => {
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
    // This file's Link double renders href and children only, so the row is read by text.
    const action = await screen.findByText('Deploy promote')
    expect(action.closest('a')).toHaveAttribute('href', '/permissions')
    expect(screen.queryByText(/Nothing to show yet/i)).toBeNull()
  })

  it('states the kill switch posture from its own read, and opens the kill switch', async () => {
    authState.can = (p) => p === 'governance:killswitch:read'
    vi.spyOn(killswitchApi, 'state').mockResolvedValue({
      estate_stopped: true,
      active: [
        {
          id: 'ks-1',
          scope_kind: 'estate',
          status: 'active',
          source: 'operator',
          engaged_aal: 3,
          engage_audit_seq: 1,
          revoked_approvals: 0,
          reviewed: false,
        },
      ],
    })
    renderIntel(<HomeView />)
    expect(await screen.findByText('ALL AGENTS STOPPED')).toBeInTheDocument()
    const tile = screen.getByText('Kill switch').closest('a')
    expect(tile).toHaveAttribute('href', '/killswitch')
    // A role that can read only the kill switch still gets a Now page, not "nothing".
    expect(screen.queryByText(/Nothing to show yet/i)).toBeNull()
  })

  it('shows the honest empty state when the role can read nothing', () => {
    authState.can = () => false
    renderIntel(<HomeView />)
    expect(screen.getByText(/Nothing to show yet/i)).toBeInTheDocument()
    // No tiles, no fabricated numbers.
    expect(screen.queryByRole('link')).toBeNull()
  })

  it('offers a key manager who cannot start a session "Add a provider", to /providers', async () => {
    // A viewer who also holds a custom role with the provider key permissions, and no
    // sessions:run:write: Home shows it the generic next steps, and the provider one
    // opens the key page (#336).
    authState.isSuperadmin = false
    authState.can = (p) =>
      p.endsWith(':read') || p === 'sessions:provider:write'
    renderIntel(<HomeView />)
    expect(
      await screen.findByRole('link', { name: /Add a provider/ }),
    ).toHaveAttribute('href', '/providers')
  })

  it('does not add a 16 px stack gap above the work', () => {
    authState.can = () => false
    const { container } = renderIntel(<HomeView />)
    const page = container.querySelector('.gap-0')
    expect(page).not.toBeNull()
    expect(page!.className).not.toMatch(/\bgap-4\b/)
  })

  // HU2-25: an upgraded install drew a tile per module, "No health checks yet" and a
  // compliance score nobody asked for among them. A tile now hides when its answer is in
  // and empty; the compliance score waits until the person opened Compliance.
  it('draws no tile for a module whose answer is empty', async () => {
    authState.can = (p) =>
      p === 'health:status:read' || p === 'governance:killswitch:read'
    const status = vi
      .spyOn(healthApi, 'status')
      .mockResolvedValue({ items: [], has_more: false } as never)
    // No subject and no open incident: an open incident is something to say.
    vi.spyOn(healthApi, 'incidents').mockResolvedValue({
      items: [],
      has_more: false,
    } as never)
    vi.spyOn(killswitchApi, 'state').mockResolvedValue({
      estate_stopped: false,
      active: [],
    })
    renderIntel(<HomeView />)
    await waitFor(() => expect(status).toHaveBeenCalled())
    await waitFor(() => expect(screen.queryByText('Health & SLA')).toBeNull())
    expect(screen.queryByText('No health checks yet')).toBeNull()
    expect(screen.queryByText('Kill switch')).toBeNull()
  })

  // SR4C on ea62fad5: only a successful, complete, empty answer hides a tile.
  it('keeps a tile whose first read is paused, and says so instead of "no checks"', async () => {
    authState.can = (p) => p === 'health:status:read'
    vi.spyOn(healthApi, 'status').mockResolvedValue({
      items: [],
      has_more: false,
    } as never)
    vi.spyOn(healthApi, 'incidents').mockResolvedValue(healthIncidentsFixture)
    onlineManager.setOnline(false)
    try {
      renderIntel(<HomeView />)
      expect(await screen.findByText('Health & SLA')).toBeInTheDocument()
      expect(
        screen.getByText('Query paused — waiting to resume'),
      ).toBeInTheDocument()
      expect(screen.queryByText('No health checks yet')).toBeNull()
    } finally {
      onlineManager.setOnline(true)
    }
  })

  it('keeps a tile whose answer is a first page with more behind it', async () => {
    authState.can = (p) => p === 'security:finding:read'
    const findings = vi.spyOn(securityApi, 'findings').mockResolvedValue({
      items: [],
      has_more: true,
    } as never)
    renderIntel(<HomeView />)
    await waitFor(() => expect(findings).toHaveBeenCalled())
    // The answered tile is a link (a loading tile is not): it stays after the answer.
    await waitFor(() =>
      expect(screen.getByText('Security').closest('a')).toHaveAttribute(
        'href',
        '/security',
      ),
    )
  })

  it('keeps a tile whose source could not be read', async () => {
    authState.can = (p) => p === 'health:status:read'
    vi.spyOn(healthApi, 'status').mockRejectedValue(
      new ApiError(500, 'server_error', 'boom'),
    )
    vi.spyOn(healthApi, 'incidents').mockResolvedValue(healthIncidentsFixture)
    renderIntel(<HomeView />)
    expect(await screen.findByText('Health & SLA')).toBeInTheDocument()
  })

  it('shows the Business compliance score only after Compliance was opened', async () => {
    panels.complianceView = () => null
    authState.can = (p) => p === 'compliance:framework:read'
    const summary = vi
      .spyOn(complianceApi, 'summary')
      .mockResolvedValue(complianceSummary)
    const first = renderIntel(<HomeView />)
    await waitFor(() => expect(summary).toHaveBeenCalled())
    await waitFor(() => expect(screen.queryByText('Compliance')).toBeNull())
    first.unmount()
    compliance.opened = true
    renderIntel(<HomeView />)
    expect(await screen.findByText('Compliance')).toBeInTheDocument()
  })

  it('does not turn Business framework gaps into a fresh-install Home score', async () => {
    panels.complianceView = () => null
    authState.can = (p) =>
      [
        'compliance:framework:read',
        'inventory:catalog:read',
        'sessions:live:read',
      ].includes(p)
    // A new install still has framework assessments from platform defaults. An
    // empty frameworks array would miss the reported 9% / 68 gaps regression.
    const summary = vi.spyOn(complianceApi, 'summary').mockResolvedValue({
      ...complianceSummary,
      frameworks: [
        {
          ...complianceSummary.frameworks[0]!,
          summary: {
            total: 100,
            satisfied: 0,
            by_design: 9,
            partial: 6,
            gap: 68,
            unmapped: 17,
          },
        },
      ],
    })
    vi.spyOn(inventoryApi, 'summary').mockResolvedValue({
      by_kind: {},
      by_source: {},
      total: 0,
    })
    vi.spyOn(sessionsApi, 'live').mockResolvedValue({
      items: [],
      has_more: false,
    })
    renderIntel(<HomeView />)
    await waitFor(() => expect(summary).toHaveBeenCalled())
    await waitFor(() => expect(screen.queryByText('Inventory')).toBeNull())
    await waitFor(() =>
      expect(screen.queryByTestId('home-sessions-scope-note')).toBeNull(),
    )
    expect(screen.queryByText('Compliance')).toBeNull()
    expect(screen.queryByText('9%')).toBeNull()
    expect(screen.queryByText(/68 gaps/)).toBeNull()
    expect(screen.queryByText('All workspaces')).toBeNull()
    expect(screen.queryByText(/Tenant-wide/)).toBeNull()
  })

  it('keeps a seeded Business compliance assessment available after Compliance was opened', async () => {
    panels.complianceView = () => null
    authState.can = (p) => p === 'compliance:framework:read'
    vi.spyOn(complianceApi, 'summary').mockResolvedValue({
      ...complianceSummary,
      frameworks: [
        {
          ...complianceSummary.frameworks[0]!,
          summary: {
            total: 10,
            satisfied: 4,
            by_design: 2,
            partial: 1,
            gap: 2,
            unmapped: 1,
          },
        },
      ],
    })
    compliance.opened = true
    renderIntel(<HomeView />)
    const tile = await screen.findByRole('link', { name: /Compliance/ })
    expect(tile).toHaveAttribute('href', '/compliance')
    expect(within(tile).getByText('60%')).toBeInTheDocument()
    expect(within(tile).getByText('2 gaps · 1 unmapped')).toBeInTheDocument()
  })

  it('renders a source error as unavailable, never a fabricated 0', async () => {
    authState.can = (p) => p === 'health:status:read'
    vi.spyOn(healthApi, 'status').mockRejectedValue(
      new ApiError(500, 'server_error', 'boom'),
    )
    vi.spyOn(healthApi, 'incidents').mockResolvedValue(healthIncidentsFixture)

    renderIntel(<HomeView />)

    expect(await screen.findByText(/Couldn't load/i)).toBeInTheDocument()
    expect(screen.getByText('—')).toBeInTheDocument()
  })

  /**
   * THE TWO SIGNALS THE FRONT DOOR LOST, MEASURED THROUGH THE SCREEN THAT LOST THEM.
   *
   * The tile cases above prove `tone` reaches an element; these two prove the four
   * callers still compute the right one and that it survives the trip. They are the
   * inputs a review named: a run-rate projected over the period's own spend, and a
   * subject that is DOWN while nothing has breached and no incident is open — the state
   * in which `2/3 healthy` used to read exactly like `3/3 healthy`.
   */
  /**
   * ⛔ AND THE WORDS ARE THE ASSERTION, BECAUSE THE COLOUR WAS THE WHOLE SIGNAL.
   *    `toHaveClass('text-warning')` is what this case used to say, and it is true of a
   *    tile whose sentence is identical in both states: the same estate under and over
   *    its run-rate printed "$9.0k projected at run-rate" either way, differing only by
   *    amber. A reader on a monochrome display, a colour-blind reader and a screen
   *    reader all got one tile for two states (WCAG 2.1 AA 1.4.1). The pair below is
   *    therefore read as TEXT, and the token is asserted beside it — the colour is still
   *    right, it is just no longer the only thing that is.
   */
  async function spendCaption(projected: number, spent: number) {
    authState.can = (p) => p === 'finops:spend:read'
    vi.spyOn(finopsApi, 'summary').mockResolvedValue(finopsSummaryFixture)
    vi.spyOn(finopsApi, 'trend').mockResolvedValue(finopsTrendFixture)
    vi.spyOn(finopsApi, 'forecast').mockResolvedValue({
      ...finopsForecastFixture,
      spend_micro_usd: spent,
      trend_projected_micro_usd: projected,
    })
    renderIntel(<HomeView />)
    if (!businessFinops) {
      expect(screen.queryByText('Spend')).toBeNull()
      expect(finopsApi.forecast).not.toHaveBeenCalled()
      return null
    }
    await screen.findByText('Spend')
    return await screen.findByTestId('estate-tile-state')
  }

  it('over its run-rate: the spend tile says so in WORDS, not only in amber', async () => {
    const state = await spendCaption(9_000_000, 1_000_000)
    if (state === null) return
    await waitFor(() => expect(state).toHaveClass('text-warning'))
    expect(state).toHaveTextContent(/projected at run-rate/i)
    expect(state).toHaveTextContent(/above spend so far/i)
  })

  it('under its run-rate: the same tile says the plain sentence and no more', async () => {
    // The CONTROL for the case above: without it "says so in words" would hold for a
    // caption that says the same thing in both states.
    const state = await spendCaption(1_000_000, 9_000_000)
    if (state === null) return
    expect(state).toHaveTextContent(/projected at run-rate/i)
    expect(state).not.toHaveTextContent(/above spend so far/i)
    expect(state.className).not.toMatch(/text-warning/)
  })

  it('a subject is down with no breach and no incident: the health tile is not silent', async () => {
    authState.can = (p) => p === 'health:status:read'
    const down = {
      ...healthStatusFixture.items[0],
      id: 'h-down',
      state: 'down' as const,
      sla_breach_open: false,
    }
    vi.spyOn(healthApi, 'status').mockResolvedValue({
      items: [healthStatusFixture.items[0], down],
      has_more: false,
    })
    vi.spyOn(healthApi, 'incidents').mockResolvedValue({
      items: [],
      has_more: false,
    })

    renderIntel(<HomeView />)
    await screen.findByText('Health & SLA')
    await waitFor(() =>
      expect(screen.getByTestId('estate-tile-state')).toHaveClass(
        'text-danger',
      ),
    )
    // The figure itself is the count, and it is NOT the thing that carries the colour.
    expect(screen.getByTestId('estate-kpi-value')).toHaveTextContent(
      '1/2 healthy',
    )
    expect(screen.getByTestId('estate-kpi-value').className).not.toMatch(
      /text-danger/,
    )
    // ⛔ AND THE WORDS ALREADY CARRY IT HERE, which is why this tile needed no new key:
    //    a down subject changes the FIGURE (`1/2 healthy`, never `2/2`), so the danger
    //    tone is a second statement of a fact the sentence already makes. Asserted, not
    //    assumed — it is the property the spend tile was missing.
    expect(screen.getByTestId('estate-kpi-value')).not.toHaveTextContent(
      '2/2 healthy',
    )
  })
})

describe('Now — the executive report', () => {
  // The report rolls up what the agents did in the period. On an empty install it has
  // nothing to say, so Now offers it once there is a session in Recent work or spend.
  const role = (p: string) =>
    p === 'sessions:live:read' ||
    p === 'sessions:run:read' ||
    p === 'finops:spend:read'
  const stub = ({
    sessions,
    spend,
    tokens = 0,
  }: {
    sessions: boolean
    spend: number
    tokens?: number
  }) => {
    const page = (items: unknown[]) => ({ items, has_more: false }) as never
    vi.spyOn(sessionsApi, 'live').mockResolvedValue(page([]))
    vi.spyOn(agentOpsApi, 'listRuns').mockResolvedValue(
      page(
        sessions
          ? [
              {
                run_ref: 'run-1',
                tenant_id: 'demo',
                state: 'stopped',
                name: 'Nightly report',
                created_at: '2026-10-03T08:00:00Z',
              },
            ]
          : [],
      ),
    )
    vi.spyOn(finopsApi, 'summary').mockResolvedValue({
      ...finopsSummaryFixture,
      total_micro_usd: spend,
      input_tokens: tokens,
      output_tokens: 0,
    })
    vi.spyOn(finopsApi, 'trend').mockResolvedValue(finopsTrendFixture)
    vi.spyOn(finopsApi, 'forecast').mockResolvedValue(finopsForecastFixture)
  }
  const report = () => screen.queryByRole('link', { name: /Executive report/ })

  it('is not offered on an empty install', async () => {
    authState.can = role
    stub({ sessions: false, spend: 0 })
    const queryClient = createTestQueryClient()
    renderIntel(<HomeView />, { queryClient })
    expect(await screen.findByText('No sessions yet')).toBeInTheDocument()
    // Every read has answered (zero spend, so Now draws no Spend tile) before the header
    // is judged.
    await waitFor(() => expect(queryClient.isFetching()).toBe(0))
    expect(report()).toBeNull()
  })

  it('is offered once a session is in Recent work', async () => {
    authState.can = role
    stub({ sessions: true, spend: 0 })
    renderIntel(<HomeView />)
    await waitFor(() => expect(report()).toHaveAttribute('href', '/dashboards'))
  })

  it('is offered on tokens a local model ran for $0', async () => {
    authState.can = (p) => p === 'finops:spend:read'
    stub({ sessions: false, spend: 0, tokens: 4200 })
    renderIntel(<HomeView />)
    if (businessFinops) {
      await waitFor(() =>
        expect(report()).toHaveAttribute('href', '/dashboards'),
      )
    } else {
      expect(report()).toBeNull()
      expect(finopsApi.summary).not.toHaveBeenCalled()
    }
  })

  it('is offered on spend alone, for a role that reads no sessions', async () => {
    authState.can = (p) => p === 'finops:spend:read'
    stub({ sessions: false, spend: 1_250_000 })
    renderIntel(<HomeView />)
    if (businessFinops) {
      await waitFor(() =>
        expect(report()).toHaveAttribute('href', '/dashboards'),
      )
    } else {
      expect(report()).toBeNull()
      expect(finopsApi.summary).not.toHaveBeenCalled()
    }
  })
})

describe('Now — the next step once a coding tool is ready (HU 022)', () => {
  // What each tool runs on is the engine's answer (GET provider-profiles/readiness):
  // a tool missing from the map has nothing to run on.
  const runsOn = (answers: Parameters<typeof readinessOf>[0]) =>
    vi
      .spyOn(agentOpsApi, 'toolsReadiness')
      .mockResolvedValue(readinessOf(answers))

  it('drops the generic next steps when a tool is signed in; New session is the action', async () => {
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
    expect(await screen.findByTestId('now-start')).toBeInTheDocument()
    expect(screen.queryByTestId('home-next-step')).toBeNull()
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

  it('a tool that runs on an API key from Providers is ready too (HU 029)', async () => {
    vi.spyOn(signInApi, 'status').mockImplementation(async (driver) => ({
      driver,
      installed: driver === 'claude',
      signed_in: false,
    }))
    runsOn({
      claude: { provider: { provider_ref: 'prv_1', kind: 'anthropic' } },
    })
    renderIntel(<HomeView />)
    expect(await screen.findByTestId('now-start')).toHaveTextContent(
      'Claude Code is ready.',
    )
    expect(screen.queryByTestId('home-next-step')).toBeNull()
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
    expect(start).toHaveTextContent(
      'The API key Anthropic was refused. Replace it under API keys.',
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
      within(start).queryByRole('button', { name: 'Open Providers' }),
    ).toBeNull()
    expect(
      within(start).getByRole('button', { name: 'Sign in' }),
    ).toBeInTheDocument()
  })

  // WEB on 09b: an editor's Home asked GET agenttools/sign-in for both tools and got
  // 403 twice per load. Only a system administrator may read it, so nobody else asks.
  it('never asks for the tools\u2019 own status for someone who is not a system administrator', async () => {
    authState.isSuperadmin = false
    runsOn({ claude: 'own_login' })
    const status = vi.spyOn(signInApi, 'status')
    renderIntel(<HomeView />)
    expect(await screen.findByTestId('now-start')).toHaveTextContent(
      'Claude Code is ready.',
    )
    expect(status).not.toHaveBeenCalled()
  })

  it('a fresh install shows the real next step, signing the tool in, and no generic cards (N2 J8)', async () => {
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
    expect(await screen.findByTestId('now-start')).toHaveTextContent(
      'Sign Claude Code in to start a session.',
    )
    expect(screen.queryByTestId('home-next-step')).toBeNull()
  })
})

describe('Home with modules not enabled (ARCH C1)', () => {
  afterEach(() => useModulesStore.getState().setOff([]))

  it('shows no tile and asks nothing of a module that is not enabled', async () => {
    useModulesStore.getState().setOff(['finops', 'security'])
    const summary = vi.spyOn(finopsApi, 'summary')
    const findings = vi.spyOn(securityApi, 'findings')
    renderIntel(<HomeView />)
    await screen.findByTestId('now-aside')
    expect(summary).not.toHaveBeenCalled()
    expect(findings).not.toHaveBeenCalled()
  })
})

describe('Now counts sessions, not the rows each one wrote (HU 029)', () => {
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
    const { container } = renderIntel(<HomeView />)
    await waitFor(() => {
      const tile = [...container.querySelectorAll('a[href="/sessions"]')].find(
        (a) => a.textContent?.includes('Live sessions'),
      )
      expect(
        tile?.querySelector('[data-testid="estate-kpi-value"]')?.textContent,
      ).toBe('1')
    })
  })
})

it('does not fetch Business compliance assessments in Community', async () => {
  compliance.opened = true
  authState.can = (p) => p === 'compliance:framework:read'
  const summary = vi
    .spyOn(complianceApi, 'summary')
    .mockResolvedValue(complianceSummary)
  renderIntel(<HomeView />)
  await waitFor(() => expect(screen.queryByText('Compliance')).toBeNull())
  expect(summary).not.toHaveBeenCalled()
})
