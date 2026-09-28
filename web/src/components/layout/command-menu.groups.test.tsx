// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The palette groups, in order: Actions, Go to, Recent sessions, Settings.
// A leading ">" keeps commands only. Matched characters are marked.
import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { renderIntel } from '@/test/intel'

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

import { CommandMenu } from './command-menu'
import { useCommandStore } from '@/stores/command'
import { useWorkspaceStore } from '@/stores/workspace'

const headings = () =>
  [...document.querySelectorAll('[cmdk-group-heading]')].map(
    (el) => el.textContent ?? '',
  )

afterEach(() => {
  useCommandStore.getState().setOpen(false)
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
