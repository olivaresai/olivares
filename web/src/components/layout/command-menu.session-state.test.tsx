// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// A palette row names a session's state in the rail's own word: the palette used to map
// the four states through `ui.status.*` ("Done" for an ended session) while the rail said
// "Ended". The words below are read off the rendered rows, so the old mapping cannot
// come back unnoticed.
import { fireEvent, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { renderIntel } from '@/test/intel'
import type { SessionStatus } from '@/features/sessions/session-display-state'
import type { RailRow, RailState } from './session-rail-model'

const routing = vi.hoisted(() => ({
  navigate: vi.fn(),
  location: {
    pathname: '/sessions',
    search: { session: 'run:current', filter: 'kept' } as Record<
      string,
      string
    >,
    hash: 'anchor',
  },
}))
vi.mock('@tanstack/react-router', () => ({
  useRouterState: () => routing.location,
  useNavigate: () => routing.navigate,
}))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    activeTenant: 'tnt-1',
    can: () => true,
    logout: vi.fn(),
  }),
}))

const STATUS_OF: Record<RailState, SessionStatus> = {
  need: 'needs-you',
  live: 'working',
  idle: 'idle',
  ended: 'done',
}
const row = (
  state: RailState,
  status: SessionStatus = STATUS_OF[state],
  name: string = state,
): RailRow => ({
  key: `session:${name}`,
  kind: 'session',
  state,
  status,
  title: `${name} session`,
  reference: `ref-${name}`,
  meta: null,
  minutes: 1,
  to: '/sessions',
  search: { session: `run:${name}` },
  href: `/sessions?s=${name}`,
})
vi.mock('./use-session-rail', () => ({
  useSessionRail: () => ({
    visible: true,
    groups: [
      {
        id: 'working',
        rows: [
          ...(['need', 'live', 'idle', 'ended'] as const).map((s) => row(s)),
          row('ended', 'failed', 'failed'),
        ],
        total: 5,
        sessionTotal: 5,
      },
      // What the person snoozed (#2480): out of the lists, never out of the menu.
      {
        id: 'snoozed',
        rows: [
          { ...row('idle'), key: 'session:snoozed', title: 'Snoozed one' },
        ],
        total: 1,
        sessionTotal: 1,
      },
    ],
    sessionCounts: { live: 1, needsYou: 1 },
    status: 'ready',
    allTo: '/sessions',
  }),
}))

import { CommandMenu } from './command-menu'
import { useCommandStore } from '@/stores/command'

afterEach(() => {
  useCommandStore.getState().setOpen(false)
  routing.location.search = { session: 'run:current', filter: 'kept' }
  routing.navigate.mockClear()
})

describe('command palette session rows', () => {
  it('opens beside the current default before it has a URL address', () => {
    routing.location.search = { filter: 'kept' }
    useCommandStore.getState().setOpen(true)
    renderIntel(
      <>
        <div data-testid="work-rail">
          <div role="option" aria-selected="true" data-address="run:default" />
        </div>
        <CommandMenu />
      </>,
    )
    fireEvent.click(
      screen.getByRole('option', { name: 'Open beside · idle session' }),
    )
    expect(routing.navigate).toHaveBeenCalledWith({
      to: '/sessions',
      search: {
        session: 'run:default',
        filter: 'kept',
        beside: 'run:idle',
        pane: 'narrative',
      },
      hash: 'anchor',
    })
  })

  it('opens a recent session beside the current one and preserves other address facets', () => {
    useCommandStore.getState().setOpen(true)
    renderIntel(<CommandMenu />)
    fireEvent.click(
      screen.getByRole('option', { name: 'Open beside · idle session' }),
    )
    expect(routing.navigate).toHaveBeenCalledWith({
      to: '/sessions',
      search: {
        session: 'run:current',
        filter: 'kept',
        beside: 'run:idle',
        pane: 'narrative',
      },
      hash: 'anchor',
    })
  })

  it('opens on a link whose session or beside value the router parsed as a non-string', () => {
    // The router JSON-parses `?session=123` and `?beside=true`: the palette must read a
    // value that is not an address as no address, not throw while it mounts.
    routing.location.search = {
      session: 123,
      beside: true,
      filter: 'kept',
    } as unknown as Record<string, string>
    useCommandStore.getState().setOpen(true)
    renderIntel(<CommandMenu />)
    expect(screen.getByText('idle session')).toBeTruthy()
    expect(
      screen.queryByRole('option', { name: 'Open beside · idle session' }),
    ).toBeNull()
  })

  it('finds a session the person snoozed by its name (#2480)', () => {
    useCommandStore.getState().setOpen(true)
    renderIntel(<CommandMenu />)
    fireEvent.change(screen.getByRole('combobox'), {
      target: { value: 'Snoozed' },
    })
    expect(screen.getByRole('option', { name: /^Snoozed one/ })).toBeTruthy()
  })

  it.each([
    ['need', 'Needs you'],
    ['live', 'Working'],
    ['idle', 'Idle'],
    ['ended', 'Done'],
    ['failed', 'Failed'],
  ] as const)('says %s in the status word the list uses: %s', (state, word) => {
    useCommandStore.getState().setOpen(true)
    renderIntel(<CommandMenu />)
    const name = screen.getByText(`${state} session`)
    const context = name
      .closest('[cmdk-item]')
      ?.querySelector('[data-slot="palette-context"]')
    expect(context?.textContent).toBe(word)
  })
})
