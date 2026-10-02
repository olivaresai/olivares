// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE SHELL'S REGIONS: sidebar · one header row · the work, and NOTHING BELOW THE WORK.
// Since the v26.10 frame the work sits in the sheet's body, a ROW that may also hold the
// side panel a page declares — beside the work, never below it.
//
// ⛔ THIS FILE EXISTS BECAUSE ITS ABSENCE WAS MEASURED. An independent review of this
//    lane ran the obvious mutation — a fixed 90 px bar reinstated after `</main>` in
//    app-layout.tsx — and the whole `layout/` suite plus the home suite stayed green:
//    13 files, 128 tests, none of which could see a second region appear under the work.
//    The lane's own design claimed an oracle for it. The claim was the defect: the
//    shell's most expensive decision — that the viewport IS the work — was resting on
//    nobody re-adding ninety pixels.
//
// So the assertions below are shaped by that mutation rather than by the source: they
// are about WHAT IS UNDER `main` and HOW MANY HEADER ROWS there are, which is what an
// action bar changes, and not about the classes the shell happens to carry today.
//
// The children are stood in for on purpose. The rail, the bar, the palette and the
// tenant gate each own a suite that pins THEIR structure (sidebar.test.tsx,
// topbar.layout.test.tsx, command-menu.test.tsx, tenant-gate.test.tsx); mounting the
// real ones here would make this file fail for their reasons and say nothing new about
// the regions.
import { render, screen } from '@testing-library/react'
import type { ComponentProps, ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const auth = vi.hoisted(() => ({ status: 'authenticated' }))
// The router's LIVE location and its last COMMITTED one. A pending redirect moves
// the first; only the second may decide where sign-in returns to.
const router = vi.hoisted(() => ({
  live: '/audit?from=2026-09-30#entry-7',
  committed: '/audit?from=2026-09-30#entry-7',
  navigate: vi.fn(),
}))
const server = vi.hoisted(() => ({ setupRequired: false, pending: false }))
beforeEach(() => {
  auth.status = 'authenticated'
  router.live = router.committed = '/audit?from=2026-09-30#entry-7'
  router.navigate.mockReset()
  server.setupRequired = false
  server.pending = false
})

vi.mock('@tanstack/react-router', () => ({
  useRouterState: ({
    select,
  }: {
    select: (s: {
      location: { pathname: string; href: string }
      resolvedLocation: { href: string }
    }) => unknown
  }) =>
    select({
      location: { pathname: router.live.split(/[?#]/)[0], href: router.live },
      resolvedLocation: { href: router.committed },
    }),
  useNavigate: () => router.navigate,
  Outlet: () => <div data-testid="routed-content">routed content</div>,
  Link: ({ children, to, ...props }: ComponentProps<'a'> & { to?: string }) => (
    <a href={to} {...props}>
      {children}
    </a>
  ),
}))

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => auth,
}))
vi.mock('@/lib/hooks/use-server-info', () => ({
  useServerInfo: () =>
    server.pending
      ? { isPending: true, data: undefined }
      : { isPending: false, data: { setup_required: server.setupRequired } },
}))

vi.mock('./app-sidebar', () => ({
  AppSidebar: () => <div data-testid="rail">rail</div>,
}))
vi.mock('./sidebar', () => ({
  AreasSheet: () => <div data-testid="areas-sheet" />,
}))
vi.mock('./phone-bar', () => ({
  PhoneBar: () => <nav data-testid="phone-bar" />,
}))
vi.mock('./topbar', () => ({
  Topbar: () => <header data-slot="topbar">bar</header>,
}))
vi.mock('./command-menu', () => ({
  CommandMenu: () => <div data-testid="palette" />,
}))
vi.mock('./shortcuts', () => ({ GlobalShortcuts: () => null }))
// The destination's section row is its own unit (registry.shell-destinations.test.tsx).
vi.mock('./destination-sections', () => ({ DestinationSections: () => null }))
vi.mock('./tenant-gate', () => ({
  TenantGate: ({ children }: { children: ReactNode }) => <>{children}</>,
}))
vi.mock('@/features/navigation/permitted-visit', () => ({
  SettingsVisit: () => null,
}))
vi.mock('@/features/navigation/personal-navigation', () => ({
  PersonalNavigationProvider: ({ children }: { children: ReactNode }) => (
    <>{children}</>
  ),
}))

import { AppLayout } from './app-layout'

const work = () => document.querySelector('main#main-content') as HTMLElement

describe('the shell has three regions and nothing else', () => {
  it('paints NOTHING under the work region', () => {
    render(<AppLayout />)
    const main = work()
    expect(main).not.toBeNull()

    // ⛔ THE ASSERTION THE MUTATION FAILS. An action bar is, structurally, an element
    //    after `main` in the column that holds it — that is what the 90 px bar was for
    //    77 routes, and what a "quick launcher" would be again. `main` is `flex-1`, so
    //    anything after it takes height from the work and gives it to chrome.
    expect(main.parentElement?.lastElementChild).toBe(main)

    // And the other shape the same region takes: anchored to the viewport instead of
    // stacked. Nothing in the shell may be `fixed` to an edge — a floating bar occludes
    // the last row of every table and form, which is the defect this pass was given.
    const shell = main.closest('[data-slot="app-frame"]') as HTMLElement
    const anchored = [...shell.querySelectorAll('[class*="fixed"]')].filter(
      (el) => /\bfixed\b/.test(el.className),
    )
    expect(anchored.map((el) => el.className)).toEqual([])
  })

  it('has ONE header row, and it is the only thing above the work', () => {
    render(<AppLayout />)
    const main = work()
    const body = main.parentElement as HTMLElement
    const sheet = body.parentElement as HTMLElement
    expect(document.querySelectorAll('[data-slot="topbar"]')).toHaveLength(1)
    // The sheet is exactly: the bar, then the body. A second row — the context row an
    // earlier pass removed, a notice strip, a tab bar hoisted out of a route — would add
    // a third child here and is what this count refuses.
    expect(sheet.getAttribute('data-slot')).toBe('sheet')
    expect(sheet.children).toHaveLength(2)
    expect(sheet.firstElementChild?.getAttribute('data-slot')).toBe('topbar')
    expect(body.previousElementSibling).toBe(sheet.firstElementChild)
    // And the body opens with the work: nothing is stacked above it inside the sheet.
    expect(body.firstElementChild).toBe(main)
  })

  it('mounts no composer of its own: starting work belongs to the routes and the palette', () => {
    render(<AppLayout />)
    // The requirement did not go away — the palette carries `Start a session` and the
    // two screens where starting work IS the work mount `WorkComposer` inside their own
    // work region. What the SHELL may not do is spend every route's pixels on it.
    expect(document.querySelector('[data-testid="work-composer"]')).toBeNull()
    expect(screen.getByTestId('routed-content')).toBeInTheDocument()
  })

  it('gives the work region the geometry that lets a route own the viewport', () => {
    render(<AppLayout />)
    const main = work()
    // `min-h-0` is the load-bearing one: without it a flex child refuses to shrink
    // below its content, so a route that scrolls inside itself grows the shell instead
    // and the page gets a second scrollbar. `overflow-hidden` is what makes a pane that
    // forgets its own `min-h-0` fail loudly rather than quietly.
    expect(main).toHaveClass(
      'flex',
      'min-h-0',
      'flex-1',
      'flex-col',
      'overflow-hidden',
    )
    // No padding and no max width: the frame is the ROUTE's choice (page-frames.tsx),
    // and a shell that imposed one made every route a document.
    expect(main.className).not.toMatch(/\b(p|px|py|max-w)-/)
  })
})

it('carries the requested page to sign-in before mounting private content', () => {
  auth.status = 'anonymous'
  render(<AppLayout />)
  expect(router.navigate).toHaveBeenCalledTimes(1)
  expect(router.navigate).toHaveBeenCalledWith({
    to: '/login',
    search: { returnTo: '/audit?from=2026-09-30#entry-7' },
    replace: true,
  })
  expect(screen.queryByTestId('routed-content')).not.toBeInTheDocument()
})

// Refresh 02 froze every signed-out browser on its first page (HU 016): the guard read
// the LIVE location, its own pending redirect moved it, and each re-render redirected
// again with a longer returnTo. It now navigates once per signed-out state.
it('redirects once, whatever the pending navigation does to the live location', () => {
  auth.status = 'anonymous'
  const { rerender } = render(<AppLayout />)
  for (let i = 0; i < 5; i++) {
    router.live = `/login?returnTo=${encodeURIComponent(router.live)}`
    rerender(<AppLayout />)
  }
  expect(router.navigate).toHaveBeenCalledTimes(1)
})

it('sends a first boot straight to the setup wizard, once', () => {
  auth.status = 'anonymous'
  server.setupRequired = true
  const { rerender } = render(<AppLayout />)
  rerender(<AppLayout />)
  expect(router.navigate).toHaveBeenCalledTimes(1)
  expect(router.navigate).toHaveBeenCalledWith({ to: '/setup', replace: true })
  expect(screen.queryByTestId('routed-content')).not.toBeInTheDocument()
})

it('waits for the server to say whether setup is needed before deciding', () => {
  auth.status = 'anonymous'
  server.pending = true
  render(<AppLayout />)
  expect(router.navigate).not.toHaveBeenCalled()
})

it('never sends sign-in back to a sign-in page', () => {
  auth.status = 'anonymous'
  router.live = router.committed = '/login?returnTo=%2Faudit'
  render(<AppLayout />)
  expect(router.navigate).toHaveBeenCalledWith({
    to: '/login',
    search: {},
    replace: true,
  })
})
