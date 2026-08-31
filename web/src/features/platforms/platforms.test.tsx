// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { DEFAULT_AUTH, renderIntel, screen, waitFor, within } from '@/test/intel'
import '@/features/_intel' // register the shared `intel` namespace for badges/notices
import {
  ApiSupportMatrix,
  HipaaCell,
  LifecycleMatrix,
  LifecycleNotes,
  ParamDeprecationCard,
  SurfaceMatrix,
  UnmodeledSurfaceNotice,
} from './components'
import {
  lifecyclesFixture,
  platformsReferenceFixture,
  surfacesFixture,
  unmodeledGateway,
} from './fixtures'
import { LIFECYCLE_NOTES } from './lifecycle.data'
import './i18n'

// RBAC: this read-only reference view exposes NO privileged write, so a principal
// with no grants must still see the matrices (no action to hide, no crash). We mock
// useAuth to deny everything and assert the view renders the same reference.
//
// `can: () => false` was the ONLY setting this suite ran in, and EffectiveStateLinks
// renders nothing when no target opens (effective-state.tsx:50). So the orientation
// links could be deleted from platforms-view.tsx without a single case here going red: the
// suite could not reach the branch that renders them. The allow-set defaults to EMPTY, which
// keeps every case below in the deny-everything world it was written for.
const auth = vi.hoisted(() => ({ allow: new Set<string>() }))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ ...DEFAULT_AUTH, can: (p: string) => auth.allow.has(p) }),
}))

// EffectiveStateLinks is the only thing on this page that needs a router, and its TARGET is
// not under test — stub the anchor rather than stand up a route tree (same choice as
// rate-limits.test.tsx and _intel/effective-state.test.tsx).
vi.mock('@tanstack/react-router', () => ({
  Link: ({ to, children }: { to: string; children: ReactNode }) => (
    <a href={to}>{children}</a>
  ),
}))

beforeEach(() => {
  auth.allow = new Set()
})

// The container reads the LIVE route: mock only the api object, keep the
// keys/helpers real (vi.mock + importOriginal pattern).
const api = vi.hoisted(() => ({ reference: vi.fn() }))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, platformsApi: api }
})

describe('SurfaceMatrix', () => {
  it('renders the six modeled surfaces with their verbatim attributes', () => {
    renderIntel(<SurfaceMatrix surfaces={surfacesFixture} />)
    const grid = screen.getByRole('grid')
    expect(
      within(grid).getByText('Anthropic API (first-party)'),
    ).toBeInTheDocument()
    expect(
      within(grid).getByText('Claude Platform on AWS (Anthropic-operated)'),
    ).toBeInTheDocument()
    // SigV4 service is verbatim from surfaces.go, not collapsed.
    expect(within(grid).getByText('aws-external-anthropic')).toBeInTheDocument()
    // bedrock-mantle appears as both a gateway id and its SigV4 service — both verbatim.
    expect(within(grid).getAllByText('bedrock-mantle').length).toBeGreaterThan(
      0,
    )
    // the deprecated legacy surface is flagged, never silently dropped.
    expect(within(grid).getAllByText(/Deprecated/i).length).toBeGreaterThan(0)
  })

  it('never leaks a secret value', () => {
    renderIntel(<SurfaceMatrix surfaces={surfacesFixture} />)
    expect(screen.queryByText(/sk-ant/)).not.toBeInTheDocument()
  })
})

describe('HipaaCell — confirm status honesty', () => {
  it('renders a confirmed HIPAA=no for Claude Platform on AWS, never a bare yes/no for to-confirm', () => {
    const cpAws = surfacesFixture.find(
      (s) => s.gateway === 'claude-platform-aws',
    )!
    const { unmount } = renderIntel(<HipaaCell surface={cpAws} />)
    expect(screen.getByText('no')).toBeInTheDocument()
    unmount()

    const direct = surfacesFixture.find((s) => s.gateway === 'direct')!
    renderIntel(<HipaaCell surface={direct} />)
    // direct HIPAA is to-confirm → the to-confirm chip, NOT a hard yes/no.
    expect(screen.getByText(/To confirm/i)).toBeInTheDocument()
    expect(screen.queryByText(/^yes$/i)).not.toBeInTheDocument()
  })
})

describe('ApiSupportMatrix', () => {
  it('shows which Anthropic API families apply per surface', () => {
    renderIntel(<ApiSupportMatrix surfaces={surfacesFixture} />)
    // header families are present
    expect(screen.getByText('Admin')).toBeInTheDocument()
    expect(screen.getByText('Compliance')).toBeInTheDocument()
    // every modeled surface gateway is a row
    expect(screen.getByText('foundry')).toBeInTheDocument()
    expect(screen.getByText('vertex')).toBeInTheDocument()
  })
})

describe('UnmodeledSurfaceNotice', () => {
  it('keeps an unmodeled gateway and labels it honestly (never dropped)', () => {
    renderIntel(<UnmodeledSurfaceNotice gateway={unmodeledGateway} />)
    expect(
      screen.getByText(/Unmodeled surface — no attribute matrix/i),
    ).toBeInTheDocument()
    expect(screen.getByText(unmodeledGateway)).toBeInTheDocument()
  })
})

describe('LifecycleMatrix — per-platform divergence', () => {
  const sonnet4 = lifecyclesFixture.filter(
    (l) => l.model_id === 'claude-sonnet-4',
  )

  it('renders the divergent Sonnet-4 deadlines (first-party vs Vertex)', () => {
    renderIntel(<LifecycleMatrix lifecycles={sonnet4} />)
    const grid = screen.getByRole('grid')
    // first-party / Anthropic-operated date (shared by direct/claude-platform-aws/foundry)
    expect(within(grid).getAllByText(/Jun 15, 2026/).length).toBe(3)
    // Vertex retires later — the whole point of ANT2-03
    expect(within(grid).getByText(/Sep 14, 2026/)).toBeInTheDocument()
  })

  it('renders Bedrock as date-not-published/to-confirm, never "never retires"', () => {
    renderIntel(<LifecycleMatrix lifecycles={sonnet4} />)
    expect(
      screen.getAllByText(/date not published \/ to-confirm/i).length,
    ).toBeGreaterThan(0)
    expect(screen.queryByText(/never retires/i)).not.toBeInTheDocument()
  })

  it('shows the authority-published successor, and the honest empty where none was named', () => {
    renderIntel(<LifecycleMatrix lifecycles={lifecyclesFixture} />)
    // replacement_ref is now POPULATED from the deprecations page.
    expect(
      screen.getAllByText('claude-sonnet-4-6').length,
    ).toBeGreaterThan(0)
    expect(
      screen.getAllByText('claude-haiku-4-5-20251001').length,
    ).toBeGreaterThan(0)
    // claude-2.x: the authority named NO successor → the honest badge, not a guess.
    expect(
      screen.getAllByText(/No successor declared/i).length,
    ).toBeGreaterThan(0)
  })
})

describe('ParamDeprecationCard', () => {
  it('frames the 400 as Anthropic deprecation pre-advice, not a product bug', () => {
    renderIntel(
      <ParamDeprecationCard
        dep={platformsReferenceFixture.param_deprecation}
      />,
    )
    expect(screen.getByText(/Informational/i)).toBeInTheDocument()
    expect(
      screen.getByText(/Anthropic's deprecation .*not a product bug/i),
    ).toBeInTheDocument()
    // The affected generations come verbatim from the backend reference.
    expect(screen.getByText(/Opus 4\.7\+, Fable\/Mythos 5/)).toBeInTheDocument()
  })
})

describe('LifecycleNotes — honesty markers', () => {
  it('marks the Global-CRIS opus id as to-confirm and the US burndown as confirmed', () => {
    renderIntel(<LifecycleNotes notes={LIFECYCLE_NOTES} />)
    // at least one to-confirm chip (crisOpusId, globalRegionalPremium)
    expect(screen.getAllByText(/To confirm/i).length).toBeGreaterThan(0)
    // and at least one confirmed chip (crisIdFormat, usBurndown)
    expect(screen.getAllByText(/Confirmed/i).length).toBeGreaterThan(0)
    // the US burndown 1.1× is surfaced as confirmed text
    expect(screen.getByText(/1\.1×/)).toBeInTheDocument()
  })
})

// --- container: live reference + RBAC + degrade-with-reason --------------------

describe('PlatformsView — live reference', () => {
  it('renders the live reference (AsOf from the response) even when the principal can do nothing', async () => {
    api.reference.mockResolvedValue(platformsReferenceFixture)
    const { PlatformsView } = await import('./platforms-view')
    renderIntel(<PlatformsView />)
    // The AsOf/source notice interpolates RESPONSE fields, not a hardcoded date…
    expect(
      await screen.findByText(
        /Reference served by the engine — AsOf 2026-06-06/i,
      ),
    ).toBeInTheDocument()
    expect(
      screen.getByText(/connectors\/claude-api\/surfaces\.go/),
    ).toBeInTheDocument()
    // …and the matrix is shown (no privileged write exists to be hidden by RBAC).
    // The surface name appears in both the matrix and the notes panel — assert ≥1.
    expect(
      screen.getAllByText('Claude Platform on AWS (Anthropic-operated)').length,
    ).toBeGreaterThan(0)
  })

  it('keeps the to-confirm honesty on the lifecycle tab, AsOf from the response', async () => {
    api.reference.mockResolvedValue(platformsReferenceFixture)
    const { PlatformsView } = await import('./platforms-view')
    const { userEvent } = await import('@/test/intel')
    renderIntel(<PlatformsView />)
    await userEvent.click(
      screen.getByRole('tab', { name: /Model lifecycle/i }),
    )
    expect(
      await screen.findByText(
        /Reference served by the engine — AsOf 2026-06-09/i,
      ),
    ).toBeInTheDocument()
    // The unpublished-Bedrock-date cells survive the flip verbatim.
    expect(
      screen.getAllByText(/date not published \/ to-confirm/i).length,
    ).toBeGreaterThan(0)
    // The web-declared notes stay, labelled as web-declared.
    expect(screen.getByText(/Web-declared reference/i)).toBeInTheDocument()
  })

  it('renders an honest unavailable notice on available:false, never an empty matrix', async () => {
    api.reference.mockResolvedValue({
      ...platformsReferenceFixture,
      available: false,
      reason: 'platforms reference provider is not wired',
      surfaces: [],
      lifecycles: [],
    })
    const { PlatformsView } = await import('./platforms-view')
    renderIntel(<PlatformsView />)
    await waitFor(() => expect(api.reference).toHaveBeenCalled())
    expect(
      await screen.findByText(/platforms reference is unavailable/i),
    ).toBeInTheDocument()
    expect(
      screen.getByText(/provider is not wired/i),
    ).toBeInTheDocument()
    // No fabricated empty grid.
    expect(screen.queryByRole('grid')).not.toBeInTheDocument()
  })
})

describe('PlatformsView — where this estate\'s own answer lives', () => {
  // The tuples themselves: this page describes what a PROVIDER supports, and the two
  // destinations that answer "and here?" are the catalog and model operations. Pinning the
  // pair (path, route permission) is the point — a link whose permission does not match its
  // route's registry entry lands the operator on Forbidden, and the shared component's own
  // suite exercises the component with fixture targets, never these.
  it('links the catalog and model operations, each gated on its OWN route permission', async () => {
    auth.allow = new Set(['models:catalog:read', 'models:registry:read'])
    api.reference.mockResolvedValue(platformsReferenceFixture)
    const { PlatformsView } = await import('./platforms-view')
    renderIntel(<PlatformsView />)

    expect(
      await screen.findByRole('link', { name: /model catalog/i }),
    ).toHaveAttribute('href', '/models')
    expect(screen.getByRole('link', { name: /model operations/i })).toHaveAttribute(
      'href',
      '/model-operations',
    )
  })

  it('hides model operations for a principal holding only the catalog permission', async () => {
    // Non-firing direction, and it is what pins the SECOND tuple: a page that emitted both
    // links unconditionally, or gated both on one permission, would pass the case above.
    auth.allow = new Set(['models:catalog:read'])
    api.reference.mockResolvedValue(platformsReferenceFixture)
    const { PlatformsView } = await import('./platforms-view')
    renderIntel(<PlatformsView />)

    expect(await screen.findByRole('link', { name: /model catalog/i })).toBeVisible()
    expect(screen.queryByRole('link', { name: /model operations/i })).toBeNull()
  })
})
