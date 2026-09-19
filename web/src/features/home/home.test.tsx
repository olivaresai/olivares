// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { ReactNode } from 'react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { renderIntel, screen, waitFor } from '@/test/intel'
import { expectNoRawI18nKeys } from '@/test/i18n-keys'
// NO hand-registered namespaces here. This file used to import `@/features/_intel`
// and `@/features/executive/i18n` for their side effect — which is precisely what
// home-view.tsx does NOT do, so the test read real English copy while the shipped
// front door printed `cost.deltaUp`. The modules that translate now register their
// own namespaces; if that regresses, this test goes red with the raw key.
import { ApiError } from '@/lib/api/errors'
import { finopsApi } from '@/features/finops/api'
import { securityApi } from '@/features/security/api'
import { healthApi } from '@/features/health/api'
import {
  finopsForecastFixture,
  finopsSummaryFixture,
  finopsTrendFixture,
  healthIncidentsFixture,
  healthStatusFixture,
  securityFindingsFixture,
} from '@/features/executive/fixtures'
import { EstateTile } from './components'
import { HomeView } from './home-view'
import './i18n'

// Render TanStack Router <Link> as a plain anchor (no RouterProvider in jsdom) — the
// established pattern across the view tests.
vi.mock('@tanstack/react-router', () => ({
  //useUrlState follows the location, so the mock has to answer it.
  useRouterState: () => '',
  // Home mounts `WorkComposer`, which navigates to the started run.
  useNavigate: () => () => {},
  Link: ({ children, to }: { children: ReactNode; to: string }) => (
    <a href={to}>{children}</a>
  ),
}))

// A mutable auth value the container reads — flip `can` per test to assert RBAC gating.
const authState = vi.hoisted(() => ({
  can: (_p: string): boolean => true,
  activeTenant: 'demo' as string | null,
}))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))

afterEach(() => {
  vi.restoreAllMocks()
  authState.can = () => true
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

    expect(await screen.findByText('Spend')).toBeInTheDocument()
    expect(screen.getByText('Security')).toBeInTheDocument()
    // The front door reuses the executive tiles, whose namespace this chunk has to
    // carry: `DeltaCaption` printed `cost.deltaUp` here until executive/components.tsx
    // started registering it. Wait for that caption to MOUNT before sweeping — it
    // arrives with the trend query, one tick after "Spend", and a sweep that runs
    // early passes over an empty slot. The wait keys on the icon, not on the text, so
    // it cannot depend on the namespace under scrutiny.
    await waitFor(() =>
      expect(
        container.querySelector(
          'svg.lucide-trending-up, svg.lucide-trending-down, svg.lucide-minus',
        ),
      ).not.toBeNull(),
    )
    expectNoRawI18nKeys(container)
    // A viewer never sees a KPI whose module their role could not open (docs/SECURITY-HARDENING.md).
    expect(screen.queryByText('Live sessions')).toBeNull()
    expect(screen.queryByText('Health & SLA')).toBeNull()
    expect(screen.queryByText('Inventory')).toBeNull()
    expect(screen.queryByText('Compliance')).toBeNull()
  })

  it('shows the honest empty state when the role can read nothing', () => {
    authState.can = () => false
    renderIntel(<HomeView />)
    expect(screen.getByText(/Nothing to show yet/i)).toBeInTheDocument()
    // No tiles, no fabricated numbers.
    expect(screen.queryByRole('link')).toBeNull()
  })

  it('does not add a 16 px stack gap above the work', () => {
    authState.can = () => false
    const { container } = renderIntel(<HomeView />)
    const page = container.querySelector('.gap-0')
    expect(page).not.toBeNull()
    expect(page!.className).not.toMatch(/\bgap-4\b/)
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
    await screen.findByText('Spend')
    return await screen.findByTestId('estate-tile-state')
  }

  it('over its run-rate: the spend tile says so in WORDS, not only in amber', async () => {
    const state = await spendCaption(9_000_000, 1_000_000)
    await waitFor(() => expect(state).toHaveClass('text-warning'))
    expect(state).toHaveTextContent(/projected at run-rate/i)
    expect(state).toHaveTextContent(/above spend so far/i)
  })

  it('under its run-rate: the same tile says the plain sentence and no more', async () => {
    // The CONTROL for the case above: without it "says so in words" would hold for a
    // caption that says the same thing in both states.
    const state = await spendCaption(1_000_000, 9_000_000)
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
