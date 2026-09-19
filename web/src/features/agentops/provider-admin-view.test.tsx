// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The two doors of the provider-profile plane. Each opens on the tab its entrance
// names, each tab is offered on ITS read tier, and a principal who holds only one of
// the two tiers reads only that plane — no profile, run or live request leaves the
// browser for a binding-only reader, and no binding request for a profile-only one.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { PageActionsProvider } from '@/components/ui/page-actions'
import { clippingAncestors } from '@/test/clipping'
import { stubViewportWidth } from '@/test/viewport'

const auth = vi.hoisted(() => ({
  perms: new Set<string>(),
}))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    activeTenant: 't1',
    can: (p: string) => auth.perms.has(p),
    isSuperadmin: false,
    principal: { user_id: 'u1', aal: 1 },
  }),
}))
vi.mock('@/components/ui/toaster', () => ({
  toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn() },
  Toaster: () => null,
}))
const api = vi.hoisted(() => ({
  listProfiles: vi.fn(),
  listBindings: vi.fn(),
  listRuns: vi.fn(),
}))
vi.mock('./api', async (orig) => {
  const real = (await orig()) as Record<string, unknown>
  return { ...real, agentOpsApi: { ...(real.agentOpsApi as object), ...api } }
})
vi.mock('@/features/console/api', () => ({
  consoleApi: { listSources: vi.fn() },
  consoleKeys: { sources: () => ['console', 'sources'] },
}))

import { ProviderAdminView } from './provider-admin-view'

const PR = 'sessions:profile:read'
const BR = 'sessions:profile-binding:read'

function wrap(entrance: 'profiles' | 'bindings') {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <PageActionsProvider>
        <ProviderAdminView entrance={entrance} />
      </PageActionsProvider>
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  vi.clearAllMocks()
  api.listProfiles.mockResolvedValue({ items: [], has_more: false })
  api.listBindings.mockResolvedValue({ items: [], has_more: false })
  api.listRuns.mockResolvedValue({ items: [], has_more: false })
})

describe('ProviderAdminView — two doors, one room', () => {
  it('the profiles door opens on the profiles tab under its own h1', async () => {
    auth.perms = new Set([PR, BR])
    wrap('profiles')
    expect(
      await screen.findByRole('heading', { name: 'Provider profiles' }),
    ).toBeInTheDocument()
    expect(screen.getByRole('tab', { name: 'Profiles' })).toHaveAttribute(
      'aria-selected',
      'true',
    )
    expect(screen.getByRole('tab', { name: 'Bindings' })).toHaveAttribute(
      'aria-selected',
      'false',
    )
    await waitFor(() => expect(api.listProfiles).toHaveBeenCalled())
  })

  it('the bindings door opens on the bindings tab under its own h1', async () => {
    auth.perms = new Set([PR, BR])
    wrap('bindings')
    expect(
      await screen.findByRole('heading', { name: 'Source bindings' }),
    ).toBeInTheDocument()
    expect(screen.getByRole('tab', { name: 'Bindings' })).toHaveAttribute(
      'aria-selected',
      'true',
    )
    await waitFor(() => expect(api.listBindings).toHaveBeenCalled())
  })

  it('a binding-only reader gets the bindings plane and asks for nothing else', async () => {
    auth.perms = new Set([BR])
    wrap('bindings')
    expect(
      await screen.findByRole('heading', { name: 'Source bindings' }),
    ).toBeInTheDocument()
    await waitFor(() => expect(api.listBindings).toHaveBeenCalledOnce())
    expect(
      screen.queryByRole('tab', { name: 'Profiles' }),
    ).not.toBeInTheDocument()
    expect(api.listProfiles).not.toHaveBeenCalled()
    expect(api.listRuns).not.toHaveBeenCalled()
    // No bind control either: that is the write tier, not the read one.
    expect(
      screen.queryByRole('button', { name: 'Bind source' }),
    ).not.toBeInTheDocument()
  })

  it('a profile-only reader gets the profiles plane and no binding request', async () => {
    auth.perms = new Set([PR])
    wrap('profiles')
    expect(
      await screen.findByRole('heading', { name: 'Provider profiles' }),
    ).toBeInTheDocument()
    await waitFor(() => expect(api.listProfiles).toHaveBeenCalledOnce())
    expect(
      screen.queryByRole('tab', { name: 'Bindings' }),
    ).not.toBeInTheDocument()
    expect(api.listBindings).not.toHaveBeenCalled()
  })

  it('an entrance whose tier is missing falls back to the tier the principal holds', async () => {
    auth.perms = new Set([BR])
    wrap('profiles')
    // The door is the profiles one (the route guard would normally refuse it); with
    // only the binding tier the room still shows what may be read, nothing else.
    await waitFor(() => expect(api.listBindings).toHaveBeenCalledOnce())
    expect(api.listProfiles).not.toHaveBeenCalled()
    expect(screen.getByRole('tab', { name: 'Bindings' })).toHaveAttribute(
      'aria-selected',
      'true',
    )
  })
})

describe('ProviderAdminView — first work row', () => {
  afterEach(() => stubViewportWidth(1440))

  // The visual bar: header 48 + title ≤ 40 + one 48 px control line = 136.
  // A stacked title, then tabs, then a subtitle/register band, then the table
  // search row is what measured y=293. Title and tabs share one 36 px row
  // (work-chrome); the register verb lands in that row; the table follows.
  it('puts the tab strip on the same line as the title', async () => {
    auth.perms = new Set([PR, BR])
    wrap('profiles')
    await screen.findByRole('heading', { name: 'Provider profiles' })
    const chrome = screen
      .getByRole('tablist')
      .closest('[data-slot="work-chrome"]')
    expect(chrome).toBeTruthy()
    expect(chrome).toContainElement(screen.getByRole('heading', { level: 1 }))
  })

  it('lifts the register verb into the title line and leaves no section heading above the table', async () => {
    auth.perms = new Set([PR, BR, 'sessions:profile:write'])
    api.listProfiles.mockResolvedValue({
      items: [
        {
          profile_ref: 'ppf_a',
          driver: 'claude',
          environment_ref: 'xenv_1',
          display_name: 'Home A',
          state: 'active',
          local_environment: true,
          operable: true,
        },
      ],
      has_more: false,
    })
    wrap('profiles')
    await screen.findByText('Home A')
    const chrome = screen
      .getByRole('tablist')
      .closest('[data-slot="work-chrome"]')
    expect(chrome).toContainElement(screen.getByRole('heading', { level: 1 }))
    expect(
      document.querySelector('[data-slot="page-actions"]'),
    ).toContainElement(screen.getByRole('button', { name: 'Register profile' }))
    expect(screen.queryByRole('heading', { name: /^profiles$/i })).toBeNull()
  })

  // ⛔ AND ON A PHONE THAT VERB IS BEHIND THE DISCLOSURE, WHICH THE ROW USED TO CLIP.
  //    The row was `h-9 … overflow-hidden` and the panel opens `absolute top-full` inside
  //    it: measured at 390×844, `elementFromPoint` at the panel's centre answered the
  //    table underneath and a click aimed at Register profile never reached it. The walk
  //    is over CLASSES because jsdom loads no stylesheet (`@/test/clipping`); the computed
  //    overflow and the hit test are measured in a browser by `code-r4/probe/panel.mjs`.
  it('phone: nothing between the open panel and the page clips it', async () => {
    const user = userEvent.setup()
    auth.perms = new Set([PR, BR, 'sessions:profile:write'])
    stubViewportWidth(390)
    wrap('profiles')
    const toggle = await screen.findByTestId('page-actions-toggle')
    await user.click(toggle)
    expect(toggle).toHaveAttribute('aria-expanded', 'true')
    const panel = document.querySelector(
      '[data-slot="page-actions"]',
    ) as HTMLElement
    // Joined, so a failure NAMES the element that cut the panel on its one line.
    expect(clippingAncestors(panel).join(' | ')).toBe('')
    expect(
      within(panel).getByRole('button', { name: 'Register profile' }),
    ).toBeInTheDocument()
  })
})
