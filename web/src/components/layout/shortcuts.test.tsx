// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { act, fireEvent, screen, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { renderIntel } from '@/test/intel'

const navigateMock = vi.fn()
// The table the page prints, extended for one test to prove the page reads it and not a copy.
const keyTable = vi.hoisted(() => ({
  rules: [] as { command: string; keys: string; when?: string }[],
  declared: 0,
}))
vi.mock('@/lib/keybindings/table', async (importOriginal) => {
  const table = await importOriginal<typeof import('@/lib/keybindings/table')>()
  keyTable.rules.push(...table.KEYBINDINGS)
  keyTable.declared = keyTable.rules.length
  return { ...table, KEYBINDINGS: keyTable.rules }
})
let route = { pathname: '/sessions', search: { session: 'run:a' } }
let tenant = 'tnt-1'
vi.mock('@tanstack/react-router', () => ({
  //useUrlState follows the location, so the mock has to answer it.
  useRouterState: ({ select }: { select: (s: unknown) => unknown }) =>
    select({ location: route }),
  useNavigate: () => navigateMock,
}))

const canMock = vi.fn((_permission: string) => true)
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    activeTenant: tenant,
    principal: { user_id: 'u1' },
    can: (p: string) => canMock(p),
  }),
}))

import { GlobalShortcuts, NAV_SHORTCUTS } from './shortcuts'
import { modKeyName } from '@/lib/keybindings/model'
import { FEATURE_VIEWS } from '@/features/registry'
import { useSessionStore } from '@/stores/session'
import { useWorkspaceStore } from '@/stores/workspace'

afterEach(() => {
  route = { pathname: '/sessions', search: { session: 'run:a' } }
  tenant = 'tnt-1'
  keyTable.rules.length = keyTable.declared
  useWorkspaceStore.setState({ activeWorkspace: null })
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

  it('never steals the letters typed in an open menu: they find its items', () => {
    // A menu's typeahead is typing: "Ren" picks Rename. Read as shell keys, its `n`
    // started a new session and `g e` navigated away (#2795).
    renderIntel(
      <>
        <div role="menu">
          <div role="menuitem" tabIndex={-1}>
            Rename
          </div>
        </div>
        <GlobalShortcuts />
      </>,
    )
    const item = screen.getByRole('menuitem', { name: 'Rename' })
    item.focus()
    fireEvent.keyDown(item, { key: 'g' })
    fireEvent.keyDown(item, { key: 'e' })
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

  it('claims the letter after g even when it names no place offered here, so a page key never fires on it', () => {
    // `g a` must not approve on the queue (#2548): the letter is the sequence's.
    canMock.mockImplementation(
      (p: string) => p !== 'eventing:subscription:read',
    )
    renderIntel(<GlobalShortcuts />)
    fireEvent.keyDown(window, { key: 'g' })
    // fireEvent answers false when a listener marked the event handled.
    expect(fireEvent.keyDown(window, { key: 'e' })).toBe(false)
    // Without the leader the letter stays the page's.
    expect(fireEvent.keyDown(window, { key: 'e' })).toBe(true)
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
    field.id = 'work-composer-input'
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

  it("focuses the focused column's composer on `/`, and never the other column's", () => {
    renderIntel(<GlobalShortcuts />)
    const column = (focused: boolean, withComposer: boolean) => {
      const section = document.createElement('section')
      section.dataset.focused = String(focused)
      if (withComposer) section.appendChild(document.createElement('textarea'))
      document.body.appendChild(section)
      return section
    }
    const first = column(false, true)
    // The one-narrative id sits on the unfocused column: the old fallback landed there.
    first.querySelector('textarea')!.id = 'work-composer-input'
    const second = column(true, true)
    fireEvent.keyDown(window, { key: '/' })
    expect(document.activeElement).toBe(second.querySelector('textarea'))
    // The focused column has nothing to type to: the key goes to /sessions, it does not
    // land in the column that is not focused.
    second.querySelector('textarea')!.remove()
    ;(document.activeElement as HTMLElement).blur()
    navigateMock.mockClear()
    fireEvent.keyDown(window, { key: '/' })
    expect(document.activeElement).not.toBe(first.querySelector('textarea'))
    expect(navigateMock).toHaveBeenCalledWith({ to: '/sessions' })
    first.remove()
    second.remove()
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

  it('prints Mod as the key this platform has: Ctrl on Linux, ⌘ on a Mac (#3121)', () => {
    for (const [platform, mod] of [
      ['Linux x86_64', 'Ctrl'],
      ['MacIntel', '⌘'],
    ]) {
      Object.defineProperty(window.navigator, 'platform', {
        value: platform,
        configurable: true,
      })
      const { unmount } = renderIntel(<GlobalShortcuts />)
      try {
        fireEvent.keyDown(window, { key: '?' })
        const keys = screen.getByTestId('keybinding-palette.open')
        expect(keys).toHaveTextContent(mod)
        if (mod !== '⌘') expect(document.body.textContent).not.toContain('⌘')
      } finally {
        unmount()
        delete (window.navigator as { platform?: string }).platform
      }
    }
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
    field.id = 'work-composer-input'
    document.body.appendChild(field)
    field.focus()

    fireEvent.keyDown(field, { key: 'k', metaKey: true })
    expect(useCommandStore.getState().open).toBe(true)
    expect(useCommandStore.getState().opener).toBe(field)
    expect(useCommandStore.getState().takeOpener()).toBe(field)
    field.remove()
  })
})

describe('previous opened session', () => {
  it('toggles between opened sessions, ignores pane changes and pauses behind a modal', () => {
    const view = renderIntel(<GlobalShortcuts />)
    fireEvent.keyDown(window, { key: 'Enter', ctrlKey: true, shiftKey: true })
    expect(navigateMock).not.toHaveBeenCalled()
    route = { pathname: '/sessions', search: { session: 'run:b' } }
    view.rerender(<GlobalShortcuts />)
    fireEvent.keyDown(window, { key: 'Enter', ctrlKey: true, shiftKey: true })
    expect(navigateMock).toHaveBeenLastCalledWith({
      to: '/sessions',
      search: { session: 'run:a' },
    })
    route = { pathname: '/sessions', search: { session: 'run:a' } }
    view.rerender(<GlobalShortcuts />)
    view.rerender(<GlobalShortcuts />)
    fireEvent.keyDown(window, { key: 'Enter', metaKey: true, shiftKey: true })
    expect(navigateMock).toHaveBeenLastCalledWith({
      to: '/sessions',
      search: { session: 'run:b' },
    })
    navigateMock.mockClear()
    fireEvent.keyDown(window, { key: '?' })
    fireEvent.keyDown(window, { key: 'Enter', ctrlKey: true, shiftKey: true })
    expect(navigateMock).not.toHaveBeenCalled()
  })

  it('still fires from inside a field: it is a modified chord, like ⌘K and ⌘B', () => {
    const view = renderIntel(
      <>
        <input aria-label="field" />
        <GlobalShortcuts />
      </>,
    )
    route = { pathname: '/sessions', search: { session: 'run:b' } }
    view.rerender(
      <>
        <input aria-label="field" />
        <GlobalShortcuts />
      </>,
    )
    const field = screen.getByLabelText('field')
    field.focus()
    fireEvent.keyDown(field, { key: 'Enter', ctrlKey: true, shiftKey: true })
    expect(navigateMock).toHaveBeenLastCalledWith({
      to: '/sessions',
      search: { session: 'run:a' },
    })
  })

  it('discards history on a tenant round trip and rechecks view permission', () => {
    const view = renderIntel(<GlobalShortcuts />)
    route = { pathname: '/sessions', search: { session: 'run:b' } }
    view.rerender(<GlobalShortcuts />)
    canMock.mockReturnValue(false)
    view.rerender(<GlobalShortcuts />)
    fireEvent.keyDown(window, { key: 'Enter', ctrlKey: true, shiftKey: true })
    expect(navigateMock).not.toHaveBeenCalled()
    canMock.mockReturnValue(true)
    tenant = 'tnt-2'
    view.rerender(<GlobalShortcuts />)
    tenant = 'tnt-1'
    view.rerender(<GlobalShortcuts />)
    fireEvent.keyDown(window, { key: 'Enter', ctrlKey: true, shiftKey: true })
    expect(navigateMock).not.toHaveBeenCalled()
  })

  it("does not carry the old workspace's open session into the new one", () => {
    useWorkspaceStore.setState({ activeWorkspace: 'ws-1' })
    const view = renderIntel(<GlobalShortcuts />)
    route = { pathname: '/sessions', search: { session: 'run:b' } }
    view.rerender(<GlobalShortcuts />)
    // The switcher leaves the URL on run:b, which belongs to ws-1.
    act(() => useWorkspaceStore.setState({ activeWorkspace: 'ws-2' }))
    view.rerender(<GlobalShortcuts />)
    route = { pathname: '/sessions', search: { session: 'run:c' } }
    view.rerender(<GlobalShortcuts />)
    fireEvent.keyDown(window, { key: 'Enter', ctrlKey: true, shiftKey: true })
    expect(navigateMock).not.toHaveBeenCalled()
  })

  it("ignores the old scope's session across two switches in a row", () => {
    useWorkspaceStore.setState({ activeWorkspace: 'ws-1' })
    const view = renderIntel(<GlobalShortcuts />)
    route = { pathname: '/sessions', search: { session: 'run:b' } }
    view.rerender(<GlobalShortcuts />)
    // Passing through ws-2 on the way to ws-3: the URL still names ws-1's run:b.
    act(() => useWorkspaceStore.setState({ activeWorkspace: 'ws-2' }))
    view.rerender(<GlobalShortcuts />)
    act(() => useWorkspaceStore.setState({ activeWorkspace: 'ws-3' }))
    view.rerender(<GlobalShortcuts />)
    route = { pathname: '/sessions', search: { session: 'run:c' } }
    view.rerender(<GlobalShortcuts />)
    fireEvent.keyDown(window, { key: 'Enter', ctrlKey: true, shiftKey: true })
    expect(navigateMock).not.toHaveBeenCalled()
  })

  it('keeps the previous session across a credential renewal', () => {
    const view = renderIntel(<GlobalShortcuts />)
    route = { pathname: '/sessions', search: { session: 'run:b' } }
    view.rerender(<GlobalShortcuts />)
    act(() =>
      useSessionStore.setState((s) => ({
        credentialGeneration: s.credentialGeneration + 1,
      })),
    )
    view.rerender(<GlobalShortcuts />)
    fireEvent.keyDown(window, { key: 'Enter', ctrlKey: true, shiftKey: true })
    expect(navigateMock).toHaveBeenLastCalledWith({
      to: '/sessions',
      search: { session: 'run:a' },
    })
  })

  it('remembers the route a session was last opened on', () => {
    const view = renderIntel(<GlobalShortcuts />)
    route = { pathname: '/agentops', search: { session: 'run:b' } }
    view.rerender(<GlobalShortcuts />)
    route = { pathname: '/sessions', search: { session: 'run:b' } }
    view.rerender(<GlobalShortcuts />)
    route = { pathname: '/sessions', search: { session: 'run:c' } }
    view.rerender(<GlobalShortcuts />)
    fireEvent.keyDown(window, { key: 'Enter', ctrlKey: true, shiftKey: true })
    expect(navigateMock).toHaveBeenLastCalledWith({
      to: '/sessions',
      search: { session: 'run:b' },
    })
  })

  it('prints a rule the table gains, with its own keys', () => {
    keyTable.rules.push({
      command: 'session.new',
      keys: 'Alt+Q',
      when: 'outsideField',
    })
    renderIntel(<GlobalShortcuts />)
    fireEvent.keyDown(window, { key: '?' })
    const rows = screen.getAllByTestId('keybinding-session.new')
    const added = rows.find((row) => row.textContent?.includes('Q'))
    expect(added).toBeDefined()
    expect(added).toHaveTextContent('Alt')
  })

  it('prints every declared switching command from the table', async () => {
    const { KEYBINDINGS } = await import('@/lib/keybindings/table')
    renderIntel(<GlobalShortcuts />)
    fireEvent.keyDown(window, { key: '?' })
    const rules = KEYBINDINGS.filter(
      (r) =>
        r.command === 'session.previous' ||
        r.command.startsWith('palette.recent'),
    )
    expect(rules).toHaveLength(7)
    for (const rule of rules) {
      const row = screen.getByTestId(`keybinding-${rule.command}`)
      for (const token of rule.keys.split('+'))
        expect(
          within(row).getByText(token === 'Mod' ? modKeyName() : token, {
            selector: 'kbd',
          }),
        ).toBeInTheDocument()
      expect(row.textContent).not.toMatch(/keys\.command|keys\.when/)
    }
  })
})
