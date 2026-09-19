// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE SHELL'S REGIONS: rail · one header row · the work, and NOTHING BELOW THE WORK.
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
import { describe, expect, it, vi } from 'vitest'

vi.mock('@tanstack/react-router', () => ({
  useRouterState: ({
    select,
  }: {
    select: (s: { location: { pathname: string } }) => unknown
  }) => select({ location: { pathname: '/audit' } }),
  Navigate: ({ to }: { to: string }) => <div data-testid="redirect">{to}</div>,
  Outlet: () => <div data-testid="routed-content">routed content</div>,
  Link: ({ children, to, ...props }: ComponentProps<'a'> & { to?: string }) => (
    <a href={to} {...props}>
      {children}
    </a>
  ),
}))

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ status: 'authenticated' }),
}))

vi.mock('./sidebar', () => ({
  Sidebar: () => <div data-testid="rail">rail</div>,
  MobileNav: () => <div data-testid="mobile-nav" />,
}))
vi.mock('./topbar', () => ({
  Topbar: () => <header data-slot="topbar">bar</header>,
}))
vi.mock('./command-menu', () => ({
  CommandMenu: () => <div data-testid="palette" />,
}))
vi.mock('./shortcuts', () => ({ GlobalShortcuts: () => null }))
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
    const shell = main.closest('div.flex.h-svh') as HTMLElement
    const anchored = [...shell.querySelectorAll('[class*="fixed"]')].filter(
      (el) => /\bfixed\b/.test(el.className),
    )
    expect(anchored.map((el) => el.className)).toEqual([])
  })

  it('has ONE header row, and it is the only thing above the work', () => {
    render(<AppLayout />)
    const main = work()
    const column = main.parentElement as HTMLElement
    expect(document.querySelectorAll('[data-slot="topbar"]')).toHaveLength(1)
    // The column is exactly: the bar, then the work. A second row — the context row
    // this pass removed, a notice strip, a tab bar hoisted out of a route — would add
    // a third child here and is what this count refuses.
    expect(column.children).toHaveLength(2)
    expect(column.firstElementChild?.getAttribute('data-slot')).toBe('topbar')
    expect(main.previousElementSibling).toBe(column.firstElementChild)
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
