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
})
