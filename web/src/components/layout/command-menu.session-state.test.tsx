// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// A palette row names a session's state in the rail's own word: the palette used to map
// the four states through `ui.status.*` ("Done" for an ended session) while the rail said
// "Ended". The words below are read off the rendered rows, so the old mapping cannot
// come back unnoticed.
import { screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { renderIntel } from '@/test/intel'
import type { RailRow, RailState } from './session-rail-model'

vi.mock('@tanstack/react-router', () => ({
  useRouterState: () => '',
  useNavigate: () => vi.fn(),
}))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    activeTenant: 'tnt-1',
    can: () => true,
    logout: vi.fn(),
  }),
}))

const row = (state: RailState): RailRow => ({
  key: `session:${state}`,
  kind: 'session',
  state,
  title: `${state} session`,
  reference: `ref-${state}`,
  meta: null,
  minutes: 1,
  to: '/sessions',
  search: {},
  href: `/sessions?s=${state}`,
})
vi.mock('./use-session-rail', () => ({
  useSessionRail: () => ({
    visible: true,
    groups: [
      {
        id: 'working',
        rows: (['need', 'live', 'idle', 'ended'] as const).map(row),
        total: 4,
        sessionTotal: 4,
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
})

describe('command palette session rows', () => {
  it.each([
    ['need', 'Needs you'],
    ['live', 'Working'],
    ['idle', 'Idle'],
    ['ended', 'Ended'],
  ] as const)('says %s as the rail does: %s', (state, word) => {
    useCommandStore.getState().setOpen(true)
    renderIntel(<CommandMenu />)
    const name = screen.getByText(`${state} session`)
    const context = name
      .closest('[cmdk-item]')
      ?.querySelector('[data-slot="palette-context"]')
    expect(context?.textContent).toBe(word)
  })
})
