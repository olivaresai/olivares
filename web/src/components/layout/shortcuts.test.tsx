// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { fireEvent, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { renderIntel } from '@/test/intel'

const navigateMock = vi.fn()
vi.mock('@tanstack/react-router', () => ({
  //useUrlState follows the location, so the mock has to answer it.
  useRouterState: () => '',
  useNavigate: () => navigateMock,
}))

const canMock = vi.fn((_permission: string) => true)
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ can: (p: string) => canMock(p) }),
}))

import { GlobalShortcuts, NAV_SHORTCUTS } from './shortcuts'
import { FEATURE_VIEWS } from '@/features/registry'

afterEach(() => {
  navigateMock.mockReset()
  canMock.mockReset()
  canMock.mockReturnValue(true)
})

describe('GlobalShortcuts', () => {
  it('binds only real, visible registry features', () => {
    for (const { featureId } of NAV_SHORTCUTS) {
      const view = FEATURE_VIEWS.find((v) => v.id === featureId)
      expect(view, `binding for unknown feature ${featureId}`).toBeTruthy()
      expect(view?.hideInNav, `${featureId} is hidden`).not.toBe(true)
      expect(view?.path.includes('$'), `${featureId} is parameterized`).toBe(
        false,
      )
    }
  })

  it('navigates on g+letter and ignores the letter without the leader', () => {
    renderIntel(<GlobalShortcuts />)

    fireEvent.keyDown(window, { key: 'e' })
    expect(navigateMock).not.toHaveBeenCalled()

    fireEvent.keyDown(window, { key: 'g' })
    fireEvent.keyDown(window, { key: 'e' })
    expect(navigateMock).toHaveBeenCalledWith({ to: '/eventing' })
  })

  it('never steals keystrokes from a form control', () => {
    renderIntel(
      <>
        <input aria-label="field" />
        <GlobalShortcuts />
      </>,
    )
    const input = screen.getByLabelText('field')
    input.focus()
    fireEvent.keyDown(input, { key: 'g' })
    fireEvent.keyDown(input, { key: 'e' })
    expect(navigateMock).not.toHaveBeenCalled()
  })

  it('does not navigate to a feature RBAC hides, and hides it from the overlay', () => {
    canMock.mockImplementation(
      (p: string) => p !== 'eventing:subscription:read',
    )
    const eventing = FEATURE_VIEWS.find((v) => v.id === 'eventing')
    expect(eventing?.permission).toBe('eventing:subscription:read')

    renderIntel(<GlobalShortcuts />)
    fireEvent.keyDown(window, { key: 'g' })
    fireEvent.keyDown(window, { key: 'e' })
    expect(navigateMock).not.toHaveBeenCalled()
  })

  it('toggles the help overlay with ? (open and close)', () => {
    renderIntel(<GlobalShortcuts />)
    fireEvent.keyDown(window, { key: '?', shiftKey: true })
    expect(screen.getByRole('dialog')).toBeInTheDocument()
    // Escape is owned by Radix (document-level listener); the component's own
    // toggle is `?`, which must also close.
    fireEvent.keyDown(window, { key: '?', shiftKey: true })
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })
})
