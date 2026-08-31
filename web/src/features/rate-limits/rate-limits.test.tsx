// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { ReactNode } from 'react'
import { DEFAULT_AUTH, renderIntel, screen, within } from '@/test/intel'
import '@/features/_intel' // register the shared `intel` namespace for badges/notices

//the header now carries EffectiveStateLinks (this page is read-only reference;
// the estate's own knobs live in the proxy and in spend). Its <Link> needs a router,
// and renderIntel deliberately provides only Query + Tooltip — so stub the anchor here
// rather than stand up a route tree for a link whose TARGET is not under test.
vi.mock('@tanstack/react-router', () => ({
  Link: ({ to, children }: { to: string; children: ReactNode }) => (
    <a href={to}>{children}</a>
  ),
}))
import { rateLimitsApi } from './api'
import {
  IngestUnavailableNotice,
  InventoryUnavailableNotice,
  RateLimitCaveats,
  RateLimitCountStat,
  RateLimitInventoryTable,
} from './components'
import { RateLimitsView } from './rate-limits-view'
import {
  findingFixture,
  inventoryFixture,
  inventoryResponseFixture,
  inventoryUnavailableFixture,
} from './fixtures'
import type { RateLimit } from './types'
import './i18n'

// The view reads useAuth only for the active tenant (query scoping); mock it so the
// container tests need no AuthProvider. The pure-piece tests never touch it.
// second contrast: this mock returned true for ANY string, so the destination case
// below pinned only the hrefs — swapping `inferenceproxy:config:read` for any other valid
// permission left it green. An allow-set makes the permission half load-bearing, exactly as
// platforms.test.tsx does. It defaults to allow-all so every pre-existing case is unchanged.
const auth = vi.hoisted(() => ({ allow: null as Set<string> | null }))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    activeTenant: 'demo',
    can: (p: string) => (auth.allow ? auth.allow.has(p) : true),
  }),
}))

afterEach(() => {
  vi.restoreAllMocks()
})

describe('RateLimitCountStat (REAL count finding)', () => {
  it('presents the backend-owned count verbatim from the finding title', () => {
    renderIntel(<RateLimitCountStat finding={findingFixture} />)
    // The count lives in the finding title — the UI never re-derives the number.
    expect(
      screen.getByText(
        /4 rate-limit group\(s\) a gateway\/proxy must keep in sync/i,
      ),
    ).toBeInTheDocument()
    // The Info severity is surfaced (governance summary is Info).
    expect(screen.getByText(/Info/i)).toBeInTheDocument()
  })

  it('carries an observed-at honesty marker and never leaks a secret', () => {
    renderIntel(<RateLimitCountStat finding={findingFixture} />)
    expect(screen.getByText(/Observed/i)).toBeInTheDocument()
    // detail_hash is a fingerprint, not a payload; no credential is ever rendered.
    expect(screen.queryByText(/sk-ant/)).not.toBeInTheDocument()
  })
})

describe('RateLimitCaveats (mandatory verbatim caveats)', () => {
  it('always renders both documented caveats, prominently', () => {
    renderIntel(<RateLimitCaveats />)
    expect(
      screen.getByText(/Gateways\/proxies must keep in sync/i),
    ).toBeInTheDocument()
    // Managed Agents are NOT covered — a documented gap, never an invented zero/row.
    expect(
      screen.getByText(
        /Managed Agents are NOT covered by the Rate Limits API/i,
      ),
    ).toBeInTheDocument()
  })
})

describe('RateLimitInventoryTable (LIVE per-limiter inventory)', () => {
  it('groups by scope and renders the org/per-workspace rows', () => {
    renderIntel(<RateLimitInventoryTable limits={inventoryFixture} />)
    const grid = screen.getByRole('grid')
    expect(within(grid).getAllByText(/Organization/i).length).toBeGreaterThan(0)
    // A per-workspace limit shows its workspace ref.
    expect(within(grid).getAllByText('ws_marketing').length).toBeGreaterThan(0)
  })

  it('renders workspace org_limit echoes and leaves org rows without an override value', () => {
    renderIntel(<RateLimitInventoryTable limits={inventoryFixture} />)
    const grid = screen.getByRole('grid')
    // The workspace row shows the org_limit echo in the dedicated column.
    expect(within(grid).getAllByText('4,000').length).toBeGreaterThan(1)
    // Org-scoped rows have no org_limit echo; that is absence/not applicable.
    expect(within(grid).getAllByText('—').length).toBeGreaterThan(0)
  })

  it('renders an unknown group_type gracefully (open vocabulary)', () => {
    renderIntel(<RateLimitInventoryTable limits={inventoryFixture} />)
    // "priority_tier" is not in the known set — humanized, not rejected.
    expect(screen.getByText(/Priority tier/i)).toBeInTheDocument()
  })

  it('renders model lists with a full-list tooltip and em-dash when absent', () => {
    renderIntel(<RateLimitInventoryTable limits={inventoryFixture} />)
    expect(
      screen.getAllByTitle('claude-opus-4-5, claude-opus-4-8').length,
    ).toBeGreaterThan(0)
    const grid = screen.getByRole('grid')
    expect(within(grid).getAllByText('—').length).toBeGreaterThan(0)
  })

  it('renders a single limiter row without requiring models', () => {
    const noModels: RateLimit[] = [
      {
        workspace_ref: '',
        group_type: 'batch',
        limits: [{ type: 'enqueued_batch_requests', value: 12 }],
      },
    ]
    renderIntel(<RateLimitInventoryTable limits={noModels} />)
    const grid = screen.getByRole('grid')
    expect(within(grid).getByText('12')).toBeInTheDocument()
    expect(within(grid).getAllByText('—').length).toBeGreaterThan(0)
  })
})

describe('IngestUnavailableNotice (honest degradation)', () => {
  it('states the ingest is unavailable rather than a fabricated empty', () => {
    renderIntel(<IngestUnavailableNotice />)
    expect(
      screen.getByText(/Admin-API governance ingest unavailable/i),
    ).toBeInTheDocument()
  })
})

describe('InventoryUnavailableNotice (live route, available=false)', () => {
  it('states the inventory is unavailable with the backend reason, never an empty table', () => {
    renderIntel(
      <InventoryUnavailableNotice reason="the Claude Admin-API connector is not wired" />,
    )
    expect(screen.getByText(/Inventory unavailable/i)).toBeInTheDocument()
    // The backend's operator-facing reason is surfaced verbatim.
    expect(
      screen.getByText(/the Claude Admin-API connector is not wired/i),
    ).toBeInTheDocument()
  })

  it('falls back to a localized hint when the backend gives no reason', () => {
    renderIntel(<InventoryUnavailableNotice />)
    expect(screen.getByText(/Inventory unavailable/i)).toBeInTheDocument()
    expect(screen.getByText(/honest unavailability/i)).toBeInTheDocument()
  })
})

describe('RateLimitsView (LIVE inventory flip — end to end)', () => {
  it('renders the live per-limiter inventory rows when available', async () => {
    vi.spyOn(rateLimitsApi, 'findings').mockResolvedValue({
      items: [findingFixture],
      has_more: false,
    })
    vi.spyOn(rateLimitsApi, 'inventory').mockResolvedValue(
      inventoryResponseFixture,
    )
    renderIntel(<RateLimitsView />)
    // The live shape (`rate_limits`, not `items`) renders in the grid — the flip works.
    const grid = await screen.findByRole('grid')
    expect(within(grid).getAllByText('ws_marketing').length).toBeGreaterThan(0)
  })

  it('shows an honest unavailable notice on available=false — never an empty grid', async () => {
    vi.spyOn(rateLimitsApi, 'findings').mockResolvedValue({
      items: [],
      has_more: false,
    })
    vi.spyOn(rateLimitsApi, 'inventory').mockResolvedValue(
      inventoryUnavailableFixture,
    )
    renderIntel(<RateLimitsView />)
    expect(
      await screen.findByText(/Inventory unavailable/i),
    ).toBeInTheDocument()
    // No grid pretending an empty inventory is real data.
    expect(screen.queryByRole('grid')).toBeNull()
  })
})

describe("RateLimitsView — where this estate's own answer lives", () => {
  // The limits are the PROVIDER's; what this estate controls is what it sends and what it
  // spends. Pinning the pair (path, route permission) is the point — a link whose permission
  // does not match its route's registry entry lands the operator on Forbidden, and the shared
  // component's own suite exercises it with the PLATFORMS fixture targets, never these two.
  beforeEach(() => {
    auth.allow = null
  })

  it('links the inference proxy and FinOps spend from the reference header', async () => {
    vi.spyOn(rateLimitsApi, 'findings').mockResolvedValue({
      items: [findingFixture],
      has_more: false,
    })
    vi.spyOn(rateLimitsApi, 'inventory').mockResolvedValue(
      inventoryResponseFixture,
    )
    renderIntel(<RateLimitsView />)

    expect(
      await screen.findByRole('link', { name: /inference proxy/i }),
    ).toHaveAttribute('href', '/inference-proxy')
    expect(screen.getByRole('link', { name: /spend|finops/i })).toHaveAttribute(
      'href',
      '/finops',
    )
  })

  it('gates each link on ITS OWN route permission, not on any permission at all', async () => {
    // The half the href assertions cannot see. Holding only the proxy's permission must
    // leave the proxy link and drop FinOps. NOTE what this alone does NOT pin: swapping
    // FinOps' permission for any other string that is not the proxy's keeps it hidden here
    // and stays green — so the reciprocal case below is what makes the FinOps tuple
    // load-bearing. Measured after the second the model contrast pointed out that this
    // case's first comment claimed more than it fixed.
    auth.allow = new Set(['inferenceproxy:config:read'])
    vi.spyOn(rateLimitsApi, 'findings').mockResolvedValue({
      items: [findingFixture],
      has_more: false,
    })
    vi.spyOn(rateLimitsApi, 'inventory').mockResolvedValue(
      inventoryResponseFixture,
    )
    renderIntel(<RateLimitsView />)

    expect(
      await screen.findByRole('link', { name: /inference proxy/i }),
    ).toBeVisible()
    expect(screen.queryByRole('link', { name: /spend|finops/i })).toBeNull()
  })

  it('and the reciprocal: only the FinOps permission leaves FinOps and drops the proxy', async () => {
    // Without this, `finops:spend:read` could be any other valid string and nothing would go
    // red — the previous case only ever proves FinOps is NOT gated on the proxy's permission.
    auth.allow = new Set(['finops:spend:read'])
    vi.spyOn(rateLimitsApi, 'findings').mockResolvedValue({
      items: [findingFixture],
      has_more: false,
    })
    vi.spyOn(rateLimitsApi, 'inventory').mockResolvedValue(
      inventoryResponseFixture,
    )
    renderIntel(<RateLimitsView />)

    expect(
      await screen.findByRole('link', { name: /spend|finops/i }),
    ).toBeVisible()
    expect(screen.queryByRole('link', { name: /inference proxy/i })).toBeNull()
  })
})

describe('read-only / RBAC invariant', () => {
  it('exposes no create/edit/delete affordance (read-only API)', () => {
    // The Rate Limits API is read-only — there is no gated write action to hide. Even
    // with all permissions denied, the components present data and never a write CTA.
    vi.doMock('@/lib/auth/context', () => ({
      useAuth: () => ({ ...DEFAULT_AUTH, can: () => false }),
    }))
    renderIntel(
      <>
        <RateLimitCaveats />
        <RateLimitInventoryTable limits={inventoryFixture} />
      </>,
    )
    expect(
      screen.queryByRole('button', { name: /new|create|add|edit|delete/i }),
    ).not.toBeInTheDocument()
    vi.doUnmock('@/lib/auth/context')
  })
})
