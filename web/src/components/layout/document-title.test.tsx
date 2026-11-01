// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// One document title per page (WCAG 2.4.2): tabs, history and a screen reader's page
// announcement name the page, as "<Page> · Olivares AI". Until #2558 every route kept
// index.html's one title.
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
  RouterProvider,
} from '@tanstack/react-router'
import { act, render, waitFor } from '@testing-library/react'
import i18n from 'i18next'
import { afterEach, describe, expect, it } from 'vitest'
import { routeTree } from '@/app/routes'
import { BUSINESS_BUILD } from '@/features/business-build'
import { ROUTE_ALIASES } from '@/features/registry'
import census from '@/features/route-census.json'
// The title's root-closure import registers this dictionary too; the census reads it loaded.
import '@/features/health/i18n'
import healthEn from '@/features/health/i18n/en.json'
import { LANGUAGE_CODES, registerTranslations } from '@/lib/i18n'
import {
  pageLabel,
  setAttentionCount,
  setPageTitle,
  useDocumentTitle,
} from './document-title'

function DocumentTitle() {
  useDocumentTitle()
  return null
}

const t = i18n.t.bind(i18n)

afterEach(() => setAttentionCount(0))
const title = (path: string) => `${pageLabel(t, path, {})} · Olivares AI`

describe('the document title of every census route', () => {
  // A Community build does not mount the Business paths: each answers one "Part of
  // Business" notice, so its title is that notice's, shared on purpose.
  const mounted = census.paths
    .filter((p) => BUSINESS_BUILD || !census.business_paths.includes(p))
    .map((p) => p.replace(/\$\w+/g, 'sample'))

  // A Business console serves /deploy as the one leaf of its Deployment area: the area page
  // and the leaf are both "Deployment".
  // A retired address with no page of its own reads as where it lands (/mcp-servers as MCP &
  // skills, #3388): its title is its target's, so it is not a second page to tell apart.
  const redirectsOnly = new Set(
    ROUTE_ALIASES.filter((a) => !a.from.startsWith('/areas/')).map(
      (a) => a.from,
    ),
  )
  const titled = mounted.filter(
    (p) => !(BUSINESS_BUILD && p === '/deploy') && !redirectsOnly.has(p),
  )

  it('is unique per mounted route', () => {
    expect(mounted.length).toBeGreaterThan(0)
    const byTitle = new Map<string, string[]>()
    for (const path of titled)
      byTitle.set(title(path), [...(byTitle.get(title(path)) ?? []), path])
    const shared = [...byTitle].filter(([, paths]) => paths.length > 1)
    expect(shared).toEqual([])
    expect(byTitle.size).toBe(titled.length)
  })

  // The accepted duplicate above: when /deploy is retitled this fails, and the exclusion goes.
  it.runIf(BUSINESS_BUILD)(
    'titles the one-leaf Deployment area and its leaf alike',
    () => {
      expect(title('/deploy')).toBe(title('/areas/deployment'))
    },
  )

  it('names the page, never the bare brand', () => {
    expect(mounted.filter((p) => pageLabel(t, p, {}) === '')).toEqual([])
  })

  it('names no mounted route as not found', () => {
    const notFound = t('errors:notFound.title')
    expect(mounted.filter((p) => pageLabel(t, p, {}) === notFound)).toEqual([])
  })

  it('reads the place label the top bar shows', () => {
    expect(title('/sessions')).toBe('Sessions · Olivares AI')
    expect(title('/killswitch')).toBe('Kill switch · Olivares AI')
    expect(title('/settings')).toBe('Settings · Olivares AI')
    expect(title('/login')).toBe('Sign in · Olivares AI')
    expect(title('/login/')).toBe('Sign in · Olivares AI')
    expect(title('/')).toBe('Now · Olivares AI')
  })

  it('names a door by its own entrance, not the room it opens', () => {
    expect(title('/agentops')).toBe('Operate sessions · Olivares AI')
    expect(title('/provider-profiles')).toBe('Provider profiles · Olivares AI')
  })

  // The approval queue is the Rules place of the Estate top bar (APPROVALS_SECTION).
  it('names a section address by its place', () => {
    expect(pageLabel(t, '/permissions', { tab: 'approvals' })).toBe(
      'Rules & approvals',
    )
  })

  it.skipIf(BUSINESS_BUILD)('names a Business bookmark in Community', () => {
    expect(title('/tenants')).toBe('Part of Business · Olivares AI')
  })
})

describe('useDocumentTitle', () => {
  function mount(path: string) {
    const root = createRootRoute({
      component: () => (
        <>
          <DocumentTitle />
          <Outlet />
        </>
      ),
    })
    const page = (path: string) =>
      createRoute({ getParentRoute: () => root, path, component: () => null })
    const router = createRouter({
      routeTree: root.addChildren([page('/login'), page('/status-page')]),
      history: createMemoryHistory({ initialEntries: [path] }),
    })
    render(<RouterProvider router={router} />)
    return router
  }

  it('follows navigation and names a 404 as not found', async () => {
    setPageTitle('Before · Olivares AI')
    const router = mount('/login')
    await waitFor(() => expect(document.title).toBe('Sign in · Olivares AI'))
    await act(() => router.navigate({ to: '/audit/nothing-here' as never }))
    await waitFor(() =>
      expect(document.title).toBe('Page not found · Olivares AI'),
    )
  })

  // A dictionary missing at render time (its registration never ran) must not put a raw
  // key in the title: the public status page measured "statusPage.title · Olivares AI" on
  // the real binary before its namespace was registered in the root closure.
  it('names a page whose dictionary loads after the root rendered', async () => {
    for (const code of LANGUAGE_CODES) i18n.removeResourceBundle(code, 'health')
    mount('/status-page')
    // Until then the bare brand, never the raw key in a tab or a history entry.
    await waitFor(() => expect(document.title).toBe('Olivares AI'))
    act(() => registerTranslations('health', { en: healthEn }))
    await waitFor(() =>
      expect(document.title).toBe('Public status page · Olivares AI'),
    )
  })

  it('is mounted by the console root route', async () => {
    setPageTitle('Before · Olivares AI')
    const router = createRouter({
      routeTree,
      history: createMemoryHistory({ initialEntries: ['/no-such-page'] }),
    })
    render(<RouterProvider router={router} />)
    await waitFor(() =>
      expect(document.title).toBe('Page not found · Olivares AI'),
    )
  })

  it('keeps the attention count in front of the page title', () => {
    setPageTitle('Sessions · Olivares AI')
    setAttentionCount(2)
    expect(document.title).toBe('(2) Sessions · Olivares AI')
    setPageTitle('Audit · Olivares AI')
    expect(document.title).toBe('(2) Audit · Olivares AI')
    setAttentionCount(0)
    expect(document.title).toBe('Audit · Olivares AI')
  })
})
