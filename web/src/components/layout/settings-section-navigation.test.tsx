// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The Settings list in the one navigation column: the current section is told apart by its
// weight as well as its light fill, and the settings search stays reachable on the rail.
import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ComponentProps } from 'react'
import { describe, expect, it, vi } from 'vitest'
import { renderIntel } from '@/test/intel'

vi.mock('@tanstack/react-router', () => ({
  useRouterState: ({
    select,
  }: {
    select: (s: { location: { pathname: string } }) => unknown
  }) => select({ location: { pathname: '/settings' } }),
  Link: ({ children, to, ...props }: ComponentProps<'a'> & { to?: string }) => (
    <a href={to} {...props}>
      {children}
    </a>
  ),
}))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ isSuperadmin: true }),
}))
vi.mock('@/features/navigation/authorization', () => ({
  useViewAccess: () => ({ navigable: () => true }),
}))
vi.mock('@/lib/hooks/use-url-state', () => ({
  useUrlState: () => [{ section: 'keyboard' }, () => {}],
}))

import { SettingsSectionNavigation } from './settings-section-navigation'

describe('the Settings list', () => {
  it('marks the current section in bold as well as its fill', () => {
    renderIntel(<SettingsSectionNavigation />)
    const current = screen.getByRole('button', { current: 'page' })
    expect(current).toHaveTextContent('Keyboard')
    expect(current).toHaveClass('bg-active', 'font-bold', 'text-text')
  })

  it('keeps the settings search one press away when the column is folded', async () => {
    const user = userEvent.setup()
    const onSearch = vi.fn()
    renderIntel(<SettingsSectionNavigation folded onSearch={onSearch} />)
    expect(screen.queryByRole('searchbox')).toBeNull()
    await user.click(screen.getByRole('button', { name: 'Search settings' }))
    expect(onSearch).toHaveBeenCalledOnce()
  })
})
