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

describe('GlobalShortcuts — the declared table', () => {
  it('opens the palette on the chord the table declares, and from inside a field', async () => {
    // ⌘K moved here from `command-menu.tsx` so the console has ONE keyboard authority.
    // It is also the one chord that still works while typing: the palette is how an
    // operator leaves a field they opened by accident.
    const { useCommandStore } = await import('@/stores/command')
    useCommandStore.setState({ open: false })
    renderIntel(<GlobalShortcuts />)

    fireEvent.keyDown(window, { key: 'k', metaKey: true })
    expect(useCommandStore.getState().open).toBe(true)

    useCommandStore.setState({ open: false })
    const field = document.createElement('input')
    document.body.appendChild(field)
    fireEvent.keyDown(field, { key: 'k', ctrlKey: true })
    expect(useCommandStore.getState().open).toBe(true)
    field.remove()
  })

  it('focuses the launcher on `/`, and never while the operator is typing', () => {
    renderIntel(<GlobalShortcuts />)
    const field = document.createElement('input')
    field.id = 'shell-launcher-input'
    document.body.appendChild(field)

    fireEvent.keyDown(window, { key: '/' })
    expect(document.activeElement).toBe(field)

    // …and a `/` typed INTO a field is a slash, not a command.
    const other = document.createElement('input')
    document.body.appendChild(other)
    other.focus()
    fireEvent.keyDown(other, { key: '/' })
    expect(document.activeElement).toBe(other)
    field.remove()
    other.remove()
  })

  it('prints the table it resolves, with the context each row needs', () => {
    renderIntel(<GlobalShortcuts />)
    fireEvent.keyDown(window, { key: '?' })
    // Every command in the table has a row: a binding that fires and is not printed
    // would be a keyboard secret, which is the defect a declared table removes.
    expect(screen.getByTestId('keybinding-rail.pin')).toHaveTextContent(
      'in the session rail',
    )
    expect(screen.getByTestId('keybinding-palette.open')).toBeInTheDocument()
    expect(
      screen.getByTestId('keybinding-launcher.startBackground'),
    ).toBeInTheDocument()
  })

  it('states the precedence rule, because two rows can share keys', () => {
    renderIntel(<GlobalShortcuts />)
    fireEvent.keyDown(window, { key: '?' })
    expect(
      screen.getByText(/the last one that applies wins/i),
    ).toBeInTheDocument()
  })

  it('reports nothing when every rule is readable', () => {
    // The shipped table has no invalid rule (`model.test.ts` asserts that too). This
    // asserts the PAGE stays quiet when there is nothing to report, so the notice
    // means something when it appears.
    renderIntel(<GlobalShortcuts />)
    fireEvent.keyDown(window, { key: '?' })
    expect(screen.queryByTestId('keybinding-problems')).toBeNull()
  })
})

describe('the palette focus contract, from the launcher', () => {
  it('remembers the launcher field as the opener, so closing can hand focus back', async () => {
    // §3.14 adopt row 1: the palette RETURNS focus to the composer on close. The
    // mechanism is the store's `opener`, captured at open time; `CommandMenu` consumes
    // it in `onCloseAutoFocus` and in its close effect, and the live spec walks the
    // whole round trip in a real browser. What is provable here is the capture — and
    // it is the half that would silently break if `⌘K` ever stopped running while a
    // field has focus.
    const { useCommandStore } = await import('@/stores/command')
    useCommandStore.setState({ open: false, opener: null })
    renderIntel(<GlobalShortcuts />)

    const field = document.createElement('input')
    field.id = 'shell-launcher-input'
    document.body.appendChild(field)
    field.focus()

    fireEvent.keyDown(field, { key: 'k', metaKey: true })
    expect(useCommandStore.getState().open).toBe(true)
    expect(useCommandStore.getState().opener).toBe(field)
    expect(useCommandStore.getState().takeOpener()).toBe(field)
    field.remove()
  })
})
