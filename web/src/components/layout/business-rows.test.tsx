// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE CLOSED ROWS IN THE SHELL: after the page's own content, on the page the list names,
// one link per row that names its edition whole, for an administrator only, wherever the
// edition held here does not hold the row's (editionCovers). Real routes, as the shell
// renders them.
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
  RouterProvider,
} from '@tanstack/react-router'
import { cleanup, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { FEATURE_VIEWS } from '@/features/registry'
import { PLACED_MODULES } from '@/features/module-spec.gen'
import { useModulesStore } from '@/stores/modules'
import { DEFAULT_AUTH, renderIntel } from '@/test/intel'
import { BusinessRows } from './business-rows'

const auth = vi.hoisted(() => ({ superadmin: true }))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ ...DEFAULT_AUTH, isSuperadmin: auth.superadmin }),
}))

// What server-info answers: by default a Community build, which holds Community only.
const server = vi.hoisted(() => ({
  answer: { isSuccess: false, data: undefined } as {
    isSuccess: boolean
    data?: Record<string, unknown>
  },
}))
vi.mock('@/lib/hooks/use-server-info', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/lib/hooks/use-server-info')>()),
  useServerInfo: () => server.answer,
}))
const answers = (data: Record<string, unknown>) => {
  server.answer = { isSuccess: true, data }
}

beforeEach(() => {
  auth.superadmin = true
  answers({ edition: 'community', license: { status: 'none' } })
})
afterEach(cleanup)

const pathOf = (id: string) => FEATURE_VIEWS.find((v) => v.id === id)!.path

async function at(address: string) {
  const root = createRootRoute({
    component: () => (
      <>
        <Outlet />
        <BusinessRows />
      </>
    ),
  })
  const router = createRouter({
    routeTree: root.addChildren(
      [...new Set(FEATURE_VIEWS.map((v) => v.path))].map((path) =>
        createRoute({
          getParentRoute: () => root,
          path,
          component: () => <p>page content</p>,
        }),
      ),
    ),
    history: createMemoryHistory({ initialEntries: [address] }),
  })
  renderIntel(<RouterProvider router={router} />)
  await router.load()
}

describe('closed rows (Community)', () => {
  it('follow the page content of their page, each a link naming its edition whole', async () => {
    await at(pathOf('security'))
    const page = await screen.findByText('page content')
    const rows = [
      screen.getByRole('link', {
        name: 'Compliance frameworks and evidence Olivares AI Business',
      }),
      screen.getByRole('link', {
        name: 'Red team runs and scoring Olivares AI Business',
      }),
    ]
    expect(screen.getAllByRole('link', { name: /Business/ })).toEqual(rows)
    for (const row of rows) {
      expect(row.getAttribute('href')).toBe('https://olivares.ai/business')
      expect(
        page.compareDocumentPosition(row) & Node.DOCUMENT_POSITION_FOLLOWING,
      ).toBeTruthy()
      expect(row.className).not.toMatch(/accent/)
    }
  })

  it('say on Retention that only Olivares AI Business applies its schedules', async () => {
    await at(pathOf('retention'))
    await screen.findByText('page content')
    expect(
      screen.getByRole('link', {
        name: /applied only in Olivares AI Business/,
      }),
    ).toHaveAttribute('href', 'https://olivares.ai/business')
  })

  it('name no session cockpit on Work, only what Olivares AI Business holds', async () => {
    await at(pathOf('work'))
    await screen.findByText('page content')
    expect(screen.queryByRole('link', { name: /cockpit/i })).toBeNull()
    expect(
      screen.getByRole('link', {
        name: 'Recordings, automations and orchestration of sessions Olivares AI Business',
      }),
    ).toHaveAttribute('href', 'https://olivares.ai/business')
  })

  it('never name an edition by half', async () => {
    await at(pathOf('work'))
    await screen.findByText('page content')
    for (const link of screen.getAllByRole('link'))
      expect(link.textContent).toMatch(/ Olivares AI (Engineer|Business)$/)
  })

  it('draw nothing on a page the list does not name', async () => {
    await at(pathOf('health'))
    await screen.findByText('page content')
    expect(screen.queryByRole('link', { name: /Olivares AI/ })).toBeNull()
  })

  it('show a member nothing: no closed row on any page', async () => {
    auth.superadmin = false
    for (const id of ['work', 'security', 'retention']) {
      await at(pathOf(id))
      await screen.findByText('page content')
      expect(screen.queryByRole('link', { name: /Olivares AI/ })).toBeNull()
      cleanup()
    }
  })
})

// The build does not decide: the edition the license entitles does (licensed_edition), by
// the engine's own order. Each answer below is a Business build's.
describe('closed rows by the licensed edition', () => {
  const securityRows = () =>
    screen.queryAllByRole('link', { name: / Olivares AI Business$/ })

  it('a Business build with a Business license shows none', async () => {
    answers({
      edition: 'enterprise',
      licensed_edition: 'business',
      license: { status: 'valid' },
    })
    await at(pathOf('security'))
    await screen.findByText('page content')
    expect(securityRows()).toEqual([])
  })

  it('a Business build with an Engineer license shows what Business holds', async () => {
    answers({
      edition: 'enterprise',
      licensed_edition: 'engineer',
      license: { status: 'valid' },
    })
    await at(pathOf('security'))
    await screen.findByText('page content')
    expect(securityRows()).toHaveLength(2)
  })

  it('a Business build with no license shows what Business holds', async () => {
    answers({
      edition: 'enterprise',
      licensed_edition: 'community',
      license: { status: 'none' },
    })
    await at(pathOf('security'))
    await screen.findByText('page content')
    expect(securityRows()).toHaveLength(2)
  })

  it('shows none while server-info has not answered', async () => {
    server.answer = { isSuccess: false, data: undefined }
    await at(pathOf('security'))
    await screen.findByText('page content')
    expect(securityRows()).toEqual([])
  })
})

// THE VIEWS THE MODULE SPEC PLACES (core/modulespec): on the lead page of their place, one
// closed row per edition naming every view it closes there, opening the first: a place
// never grows a row per module. A view that still opens, its stored data readable below
// its edition while its module runs, is no closed row.
describe('closed rows of the views the module spec places', () => {
  afterEach(() => useModulesStore.getState().setOff([]))
  const allOff = () =>
    useModulesStore.getState().setOff(Object.keys(PLACED_MODULES))
  /** The rows that open a page of this console (an edition's own row links outside). */
  const viewRows = () =>
    screen
      .queryAllByRole('link')
      .filter((link) => link.getAttribute('href')?.startsWith('/'))
  const rowsAt = async (id: string) => {
    await at(pathOf(id))
    await screen.findByText('page content')
    const names = viewRows().map((link) => link.textContent)
    cleanup()
    return names
  }

  it('fold what a place closes into one row naming its edition, opening the first', async () => {
    await at(pathOf('audit'))
    await screen.findByText('page content')
    const row = screen.getByRole('link', {
      name: 'Access map, Evals, and Reach Olivares AI Engineer',
    })
    expect(viewRows()).toHaveLength(1)
    expect(viewRows()[0]).toBe(row)
    expect(row).toHaveAttribute('href', '/access-map')
  })

  it('show a Community administrator one row per place and edition, with every placed module off', async () => {
    allOff()
    const rows: Record<string, (string | null)[]> = {}
    for (const id of [
      'sessions',
      'workspaceDashboard',
      'agent-tools',
      'permissions',
      'usage',
      'audit',
      'operations',
    ])
      rows[id] = await rowsAt(id)
    expect(rows).toStrictEqual({
      sessions: ['Sandbox and Voice Olivares AI Engineer'],
      workspaceDashboard: ['Inventory Olivares AI Engineer'],
      'agent-tools': [
        'Knowledge Olivares AI Engineer',
        'Catalog Olivares AI Business',
      ],
      permissions: ['Managed policy Olivares AI Engineer'],
      usage: ['Adoption Olivares AI Business'],
      audit: ['Access map, Evals, and Reach Olivares AI Engineer'],
      operations: [
        'Webhooks, Alerting, and Inference proxy Olivares AI Engineer',
      ],
    })
  })

  it('draw no row for a view that reads below its edition while its module runs', async () => {
    expect(await rowsAt('agent-tools')).toEqual([])
    expect(await rowsAt('operations')).toEqual([])
  })

  it('show what Business holds to an Engineer license, and nothing it holds', async () => {
    answers({
      edition: 'enterprise',
      licensed_edition: 'engineer',
      license: { status: 'valid' },
    })
    allOff()
    expect(await rowsAt('agent-tools')).toEqual([
      'Catalog Olivares AI Business',
    ])
    expect(await rowsAt('audit')).toEqual([])
  })

  it('show a member none of them', async () => {
    auth.superadmin = false
    allOff()
    expect(await rowsAt('audit')).toEqual([])
    expect(await rowsAt('agent-tools')).toEqual([])
  })
})
