// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Saving this page to favorites moved from a star in the top bar to the account menu
// (#3161): the same explicit save, the same words, one gesture deeper.
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { renderIntel, screen } from '@/test/intel'

const personal = vi.hoisted(() => ({
  favorites: [] as { kind: string; id: string }[],
  setFavorite: vi.fn(),
}))
vi.mock('@/features/navigation/personal-navigation', () => ({
  usePersonalNavigation: () => ({
    available: true,
    visible: () => true,
    favorites: personal.favorites,
    recents: [],
    setFavorite: personal.setFavorite,
  }),
}))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    principal: { actor: 'user:ada', display_name: 'Ada', superadmin: false },
    isSuperadmin: false,
    activeRole: 'owner',
    activeTenant: 't1',
    grants: [{ tenant: 't1', role: 'owner' }],
    can: () => true,
    logout: vi.fn(),
  }),
}))
vi.mock('@tanstack/react-router', () => ({
  useNavigate: () => vi.fn(),
  useRouterState: ({ select }: { select: (s: unknown) => unknown }) =>
    select({ location: { pathname: '/settings', search: {} } }),
}))
vi.mock('@/lib/hooks/use-server-info', () => ({
  useServerInfo: () => ({ data: { version: '0.4' } }),
}))

import { UserMenu } from './user-menu'

afterEach(() => {
  personal.favorites = []
  personal.setFavorite.mockReset()
})

describe('the account menu saves this page to favorites', () => {
  it('adds the page, by its name', async () => {
    const user = userEvent.setup()
    renderIntel(<UserMenu />)
    await user.click(screen.getByRole('button', { name: /, Account$/ }))
    await user.click(
      screen.getByRole('menuitem', { name: 'Add Settings to favorites' }),
    )
    expect(personal.setFavorite).toHaveBeenCalledWith(
      { kind: 'utility', id: 'settings' },
      true,
    )
  })

  it('removes a page already saved', async () => {
    personal.favorites = [{ kind: 'utility', id: 'settings' }]
    const user = userEvent.setup()
    renderIntel(<UserMenu />)
    await user.click(screen.getByRole('button', { name: /, Account$/ }))
    await user.click(
      screen.getByRole('menuitem', { name: 'Remove Settings from favorites' }),
    )
    expect(personal.setFavorite).toHaveBeenCalledWith(
      { kind: 'utility', id: 'settings' },
      false,
    )
  })
})
