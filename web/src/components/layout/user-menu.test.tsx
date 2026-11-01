// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE ACCOUNT MENU: identity, Settings, the theme, Help for this page and the version.
// It is not a second navigation: All areas and favorites retired with the one left
// navigation (0.5), and the engine's state is read in the status bar.
import type { ComponentProps } from 'react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import userEvent from '@testing-library/user-event'
import { renderIntel, screen } from '@/test/intel'

const router = vi.hoisted(() => ({ pathname: '/routine-policies' }))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    principal: { actor: 'user:ada', display_name: 'Ada', superadmin: false },
    isSuperadmin: false,
    activeRole: 'owner',
    activeTenant: 't1',
    grants: [{ tenant: 't1', role: 'owner' }],
    setActiveTenant: () => {},
    can: () => true,
    logout: vi.fn(),
  }),
}))
vi.mock('@tanstack/react-router', () => ({
  useNavigate: () => vi.fn(),
  useRouterState: ({ select }: { select: (s: unknown) => unknown }) =>
    select({ location: { pathname: router.pathname, search: {} } }),
  Link: ({
    children,
    to,
    activeOptions,
    activeProps,
    ...props
  }: ComponentProps<'a'> & {
    to?: string
    activeOptions?: unknown
    activeProps?: unknown
  }) => {
    void activeOptions
    void activeProps
    return (
      <a href={to} {...props}>
        {children}
      </a>
    )
  },
}))
vi.mock('@/lib/hooks/use-server-info', () => ({
  useServerInfo: () => ({ data: { version: '0.4' } }),
}))

import i18n from '@/lib/i18n'
import { useThemeStore } from '@/stores/theme'
import { THEME_OPTIONS } from './theme-toggle'
import { UserMenu } from './user-menu'

afterEach(() => {
  router.pathname = '/routine-policies'
})

describe('the account menu', () => {
  it('is no second navigation: no All areas and no favorites (0.5)', async () => {
    const user = userEvent.setup()
    renderIntel(<UserMenu />)
    await user.click(screen.getByRole('button', { name: /, Account$/ }))
    const items = screen.getAllByRole('menuitem').map((i) => i.textContent)
    expect(items).not.toContain('All areas')
    expect(items.some((i) => /favorites/i.test(i ?? ''))).toBe(false)
    expect(screen.getByRole('menuitem', { name: 'Settings' })).toBeVisible()
  })

  it("opens this page's documentation in a new tab", async () => {
    const user = userEvent.setup()
    renderIntel(<UserMenu />)
    await user.click(screen.getByRole('button', { name: /, Account$/ }))
    const help = screen.getByRole('menuitem', {
      name: 'Open documentation for this view',
    })
    expect(help).toHaveAttribute('target', '_blank')
    expect(help.getAttribute('rel')).toContain('noreferrer')
    expect(help.getAttribute('href')).toMatch(
      /^https:\/\/docs\.olivares\.ai\/.+\/$/,
    )
  })

  it("leaves the engine's state to the status bar (#3161)", async () => {
    const user = userEvent.setup()
    renderIntel(<UserMenu />)
    await user.click(screen.getByRole('button', { name: /, Account$/ }))
    expect(screen.getByRole('menu')).toBeVisible()
    expect(screen.queryByRole('status')).toBeNull()
  })

  it('names the version the engine runs (#3161)', async () => {
    const user = userEvent.setup()
    renderIntel(<UserMenu />)
    await user.click(screen.getByRole('button', { name: /, Account$/ }))
    expect(screen.getByRole('menu')).toHaveTextContent('Version 0.4')
  })

  it('carries the theme the top bar no longer does (#3161)', async () => {
    useThemeStore.getState().setTheme('light')
    const user = userEvent.setup()
    renderIntel(<UserMenu />)
    await user.click(screen.getByRole('button', { name: /, Account$/ }))
    const themes = await screen.findAllByRole('menuitemradio')
    expect(themes.map((item) => item.textContent)).toEqual(
      THEME_OPTIONS.map((o) => i18n.t(`common:${o.labelKey}`)),
    )
    expect(
      screen.getByRole('menuitemradio', { name: 'Light' }),
    ).toHaveAttribute('aria-checked', 'true')
    await user.click(screen.getByRole('menuitemradio', { name: 'Dark' }))
    expect(useThemeStore.getState().theme).toBe('dark')
  })
})
