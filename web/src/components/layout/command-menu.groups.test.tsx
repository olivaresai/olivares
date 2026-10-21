// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The palette groups, in order: Actions, Go to, Recent sessions, Settings.
// A leading ">" keeps commands only. Matched characters are marked.
import { act, fireEvent, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { renderIntel } from '@/test/intel'

const navigateMock = vi.fn()
let tenant = 'tnt-1'
let allowed = true
let loading = false
let railError = false
// A rail whose healthy sources already answered while another is still pending or failed.
let partial = false
const sessionRow = (n: number) => ({
  key: `run:${n}`,
  kind: 'session' as const,
  title: `Session ${n}`,
  reference: String(n),
  state: 'live' as const,
  meta: null,
  minutes: 0,
  to: '/sessions' as const,
  search: { session: `run:${n}` },
  href: `/sessions?session=run:${n}`,
})
let rows = [1, 2, 3, 4, 5, 6, 7].map(sessionRow)
vi.mock('./use-session-rail', () => ({
  useSessionRail: () => ({
    groups: [
      { rows: allowed && (partial || (!loading && !railError)) ? rows : [] },
    ],
    status: { loading, error: railError },
  }),
}))

vi.mock('@tanstack/react-router', () => ({
  useRouterState: () => '',
  useNavigate: () => navigateMock,
}))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    activeTenant: tenant,
    can: () => allowed,
    logout: vi.fn(),
  }),
}))

import { CommandMenu } from './command-menu'
import { useCommandStore } from '@/stores/command'
import { useWorkspaceStore } from '@/stores/workspace'

const headings = () =>
  [...document.querySelectorAll('[cmdk-group-heading]')].map(
    (el) => el.textContent ?? '',
  )

afterEach(() => {
  rows = [1, 2, 3, 4, 5, 6, 7].map(sessionRow)
  tenant = 'tnt-1'
  allowed = true
  loading = false
  railError = false
  partial = false
  navigateMock.mockReset()
  act(() => useCommandStore.getState().setOpen(false))
  useWorkspaceStore.setState({
    activeWorkspace: null,
    activeWorkspaceName: null,
  })
})

describe('command palette groups', () => {
  it('lists Actions, then Go to, then Settings', () => {
    useWorkspaceStore.setState({
      activeWorkspace: 'ws-1',
      activeWorkspaceName: 'telescopes',
    })
    useCommandStore.getState().setOpen(true)
    renderIntel(<CommandMenu />)
    const names = headings()
    expect(names.indexOf('Go to')).toBeGreaterThan(names.indexOf('Actions'))
    expect(names.indexOf('Settings')).toBeGreaterThan(names.indexOf('Go to'))
    const recent = names.indexOf('Recent sessions')
    if (recent >= 0) {
      expect(recent).toBeGreaterThan(names.indexOf('Go to'))
      expect(recent).toBeLessThan(names.indexOf('Settings'))
    }
    expect(screen.getByText('in telescopes')).toBeInTheDocument()
    expect(
      document.querySelector('[data-slot="palette-footer"]'),
    ).not.toBeNull()
  })

  it('does not name a beside action while no beside surface exists', () => {
    useCommandStore.getState().setOpen(true)
    renderIntel(<CommandMenu />)
    const footer = document.querySelector('[data-slot="palette-footer"]')
    expect(footer).not.toBeNull()
    expect(footer?.textContent ?? '').not.toMatch(
      /beside|al lado|daneben|à côté|隣|旁边|рядом/i,
    )
  })

  it('limits the list to commands when the query starts with >', async () => {
    const user = userEvent.setup()
    useCommandStore.getState().setOpen(true)
    renderIntel(<CommandMenu />)
    await user.type(screen.getByRole('combobox'), '>new')
    const values = screen
      .getAllByRole('option')
      .map((o) => o.getAttribute('data-value') ?? '')
    expect(values.some((v) => v.startsWith('action:'))).toBe(true)
    expect(values.some((v) => v.startsWith('view:'))).toBe(false)
    expect(values.some((v) => v.startsWith('area:'))).toBe(false)
  })

  it('marks the matched characters', async () => {
    const user = userEvent.setup()
    useCommandStore.getState().setOpen(true)
    renderIntel(<CommandMenu />)
    await user.type(screen.getByRole('combobox'), 'ses')
    const marks = document.querySelectorAll('mark')
    expect(marks.length).toBeGreaterThan(0)
    expect(marks[0]?.textContent?.toLocaleLowerCase()).toBe('ses')
    expect(marks[0]).toHaveClass('text-accent-text')
  })
})

describe('recent palette sessions', () => {
  const recent = () =>
    screen
      .getAllByRole('option')
      .filter((el) =>
        el.getAttribute('data-value')?.startsWith('recent-session:'),
      )

  it('searches captured sessions beyond the first eight visible rows', async () => {
    const user = userEvent.setup()
    rows = [1, 2, 3, 4, 5, 6, 7, 8, 9].map(sessionRow)
    useCommandStore.getState().setOpen(true)
    renderIntel(<CommandMenu />)
    expect(recent()).toHaveLength(8)
    await user.type(screen.getByRole('combobox'), 'Session 9')
    expect(recent()).toHaveLength(1)
    expect(recent()[0]).toHaveTextContent('Session 9')
  })

  it('freezes the first arriving list when opened before sessions load', () => {
    loading = true
    useCommandStore.getState().setOpen(true)
    const view = renderIntel(<CommandMenu />)
    expect(recent()).toHaveLength(0)
    loading = false
    view.rerender(<CommandMenu />)
    expect(recent()).toHaveLength(7)
    const before = recent().map((el) => el.getAttribute('data-value'))
    rows = [...rows].reverse()
    view.rerender(<CommandMenu />)
    expect(recent().map((el) => el.getAttribute('data-value'))).toEqual(before)
  })

  it('recovers recent rows when a failed rail read succeeds while open', () => {
    // A rail read in error contributes no rows, exactly like the hook does. Freezing
    // that emptiness would present a failed read as "no recent sessions" for the
    // whole open; the next refetch's rows must still be able to arrive.
    railError = true
    useCommandStore.getState().setOpen(true)
    const view = renderIntel(<CommandMenu />)
    expect(recent()).toHaveLength(0)
    railError = false
    view.rerender(<CommandMenu />)
    expect(recent()).toHaveLength(7)
    const before = recent().map((el) => el.getAttribute('data-value'))
    rows = [...rows].reverse()
    view.rerender(<CommandMenu />)
    expect(recent().map((el) => el.getAttribute('data-value'))).toEqual(before)
  })

  it('freezes order while open and takes a fresh list on reopening', async () => {
    const user = userEvent.setup()
    useCommandStore.getState().setOpen(true)
    const view = renderIntel(<CommandMenu />)
    const before = recent().map((el) => el.getAttribute('data-value'))
    rows = [...rows].reverse()
    view.rerender(<CommandMenu />)
    expect(recent().map((el) => el.getAttribute('data-value'))).toEqual(before)
    await user.type(screen.getByRole('combobox'), 'Session')
    expect(recent().map((el) => el.getAttribute('data-value'))).toEqual(before)
    act(() => useCommandStore.getState().setOpen(false))
    act(() => useCommandStore.getState().setOpen(true))
    expect(recent()[0]).toHaveTextContent('Session 7')
  })

  it.each([1, 2, 3, 4, 5, 6])(
    'opens the frozen row with digit %s outside the query',
    (digit) => {
      useCommandStore.getState().setOpen(true)
      const view = renderIntel(<CommandMenu />)
      rows = [...rows].reverse()
      view.rerender(<CommandMenu />)
      const row = screen.getByRole('listbox')
      fireEvent.keyDown(row, { key: String(digit) })
      expect(navigateMock).toHaveBeenCalledWith({
        to: '/sessions',
        search: { session: `run:${digit}` },
      })
      expect(useCommandStore.getState().open).toBe(false)
    },
  )

  it('lets Tab leave the query for numbered results', async () => {
    const user = userEvent.setup()
    useCommandStore.getState().setOpen(true)
    renderIntel(<CommandMenu />)
    screen.getByRole('combobox').focus()
    await user.tab()
    expect(screen.getByRole('listbox')).toHaveFocus()
    await user.keyboard('2')
    expect(navigateMock).toHaveBeenCalledWith({
      to: '/sessions',
      search: { session: 'run:2' },
    })
  })

  it('keeps digits as text in the query, even when it was empty', async () => {
    const user = userEvent.setup()
    useCommandStore.getState().setOpen(true)
    renderIntel(<CommandMenu />)
    await user.type(screen.getByRole('combobox'), '1')
    expect(screen.getByRole('combobox')).toHaveValue('1')
    expect(navigateMock).not.toHaveBeenCalled()
    fireEvent.keyDown(recent()[0], { key: '1' })
    expect(navigateMock).not.toHaveBeenCalled()
  })

  it('shows hints only for the first six rows with an empty query', async () => {
    const user = userEvent.setup()
    useCommandStore.getState().setOpen(true)
    renderIntel(<CommandMenu />)
    for (let i = 0; i < 6; i++)
      expect(
        within(recent()[i]).getByText(String(i + 1), { selector: 'kbd' }),
      ).toBeInTheDocument()
    expect(within(recent()[6]).queryByText('7', { selector: 'kbd' })).toBeNull()
    await user.type(screen.getByRole('combobox'), 'Session')
    expect(within(recent()[0]).queryByText('1', { selector: 'kbd' })).toBeNull()
  })

  it('ignores missing rows and modified digits', () => {
    rows = [sessionRow(1)]
    useCommandStore.getState().setOpen(true)
    renderIntel(<CommandMenu />)
    fireEvent.keyDown(recent()[0], { key: '6' })
    fireEvent.keyDown(recent()[0], { key: '1', ctrlKey: true })
    expect(navigateMock).not.toHaveBeenCalled()
  })

  it("retires frozen rows when authority changes and freezes the new scope's rows", () => {
    useCommandStore.getState().setOpen(true)
    const view = renderIntel(<CommandMenu />)
    tenant = 'tnt-2'
    rows = [sessionRow(8)]
    view.rerender(<CommandMenu />)
    expect(recent().map((el) => el.textContent)).toEqual([
      expect.stringContaining('Session 8'),
    ])
    rows = [sessionRow(9), sessionRow(8)]
    view.rerender(<CommandMenu />)
    expect(recent()).toHaveLength(1)
  })

  it('waits for a half-loaded rail before freezing', () => {
    loading = true
    partial = true
    useCommandStore.getState().setOpen(true)
    const view = renderIntel(<CommandMenu />)
    expect(recent()).toHaveLength(0)
    loading = false
    view.rerender(<CommandMenu />)
    expect(recent()).toHaveLength(7)
  })

  it('freezes the healthy sources when another read has failed', () => {
    railError = true
    partial = true
    useCommandStore.getState().setOpen(true)
    renderIntel(<CommandMenu />)
    expect(recent()).toHaveLength(7)
  })

  it('does not keep rows visible after read permission is lost', () => {
    useCommandStore.getState().setOpen(true)
    const view = renderIntel(<CommandMenu />)
    allowed = false
    view.rerender(<CommandMenu />)
    expect(recent()).toHaveLength(0)
  })
})
