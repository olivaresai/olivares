// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The directory page is links and words — the console's own — and nothing else.
import { screen, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { ComponentProps } from 'react'
import { renderIntel } from '@/test/intel'

vi.mock('@tanstack/react-router', () => ({
  useRouterState: () => '',
  Link: ({ children, to, ...props }: ComponentProps<'a'> & { to?: string }) => (
    <a href={to} {...props}>
      {children}
    </a>
  ),
}))

const canMock = vi.fn((_p: string) => true)
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ can: (p: string) => canMock(p) }),
}))

import { FEATURE_VIEWS, NAV_AREAS } from '@/features/registry'
import { AreaDirectoryView } from './area-directory'

afterEach(() => {
  canMock.mockReset()
  canMock.mockReturnValue(true)
})

describe('AreaDirectoryView', () => {
  it('lists every authorized leaf of the area under its section, as a linked title with the nav description', () => {
    renderIntel(<AreaDirectoryView areaId="ai" />)
    expect(screen.getByRole('heading', { level: 1 })).toHaveTextContent('AI')
    const sections = screen.getAllByRole('heading', { level: 2 })
    expect(sections.map((h) => h.textContent)).toEqual([
      'Sessions',
      'Profiles and environments',
      'Models',
      'Specialized execution',
      'Provider reference',
    ])
    const links = screen.getAllByRole('link')
    const hrefs = links.map((a) => a.getAttribute('href'))
    const aiPaths = FEATURE_VIEWS.filter(
      (v) => v.navigation.kind === 'feature' && v.navigation.areaId === 'ai',
    ).map((v) => v.path)
    expect(hrefs).toEqual(aiPaths)
    // Two doors into one screen stay two entries, each with its own name.
    expect(
      screen.getByRole('link', { name: 'Observe sessions' }),
    ).toHaveAttribute('href', '/sessions')
    expect(
      screen.getByRole('link', { name: 'Operate sessions' }),
    ).toHaveAttribute('href', '/agentops')
    // The description beside a door is the console's own sentence.
    expect(
      screen.getByText(/shares its screen with Observe sessions/),
    ).toBeInTheDocument()
    // No control other than the title link lives in a card: nothing nested in a link.
    for (const a of links) expect(a.querySelector('button, a')).toBeNull()
  })

  it('shows only what can() allows, and the sibling door never rides along', () => {
    canMock.mockImplementation((p) => p === 'sessions:profile-binding:read')
    renderIntel(<AreaDirectoryView areaId="ai" />)
    const hrefs = screen.getAllByRole('link').map((a) => a.getAttribute('href'))
    expect(hrefs).toEqual(['/provider-bindings'])
    expect(screen.getAllByRole('heading', { level: 2 })).toHaveLength(1)
  })

  it('renders the localized empty state with a way home when nothing is authorized, naming no denied entry', () => {
    canMock.mockReturnValue(false)
    renderIntel(<AreaDirectoryView areaId="ai" />)
    const status = screen.getByRole('status')
    expect(
      within(status).getByText('No entries in this area are available to you'),
    ).toBeInTheDocument()
    expect(
      within(status).getByRole('link', { name: 'Go to Overview' }),
    ).toHaveAttribute('href', '/')
    expect(screen.queryByText('Operate sessions')).toBeNull()
    expect(screen.queryByText('/agentops')).toBeNull()
  })

  it('always offers Settings under System & settings → Personal preferences, last', () => {
    canMock.mockReturnValue(false)
    renderIntel(<AreaDirectoryView areaId="system" />)
    expect(screen.queryByRole('status')).toBeNull()
    expect(
      screen.getAllByRole('heading', { level: 2 }).map((h) => h.textContent),
    ).toEqual(['Personal preferences'])
    expect(screen.getByRole('link', { name: 'Settings' })).toHaveAttribute(
      'href',
      '/settings',
    )
    canMock.mockReturnValue(true)
  })

  it('keeps Preferences after Administration, Maintenance and Developer tools for an admin', () => {
    renderIntel(<AreaDirectoryView areaId="system" />)
    expect(
      screen.getAllByRole('heading', { level: 2 }).map((h) => h.textContent),
    ).toEqual([
      'Administration',
      'Installation and maintenance',
      'Developer tools',
      'Personal preferences',
    ])
  })

  // ⛔ THE REVIEW'S F3 CONTROL: every listed entry carries its one-sentence task description,
  // in every area, for a principal who may open everything. Presence and non-emptiness only;
  // whether a sentence is TRUE is a reader's judgement, not this assertion's.
  it('gives every listed entry of every area a task description', () => {
    for (const area of NAV_AREAS) {
      const { unmount } = renderIntel(<AreaDirectoryView areaId={area.id} />)
      const cards = [...document.querySelectorAll('h3')]
      expect(cards.length, area.id).toBeGreaterThan(0)
      for (const h3 of cards) {
        const card = h3.closest('li') as HTMLElement
        const p = card.querySelector('p')
        expect(p, `${area.id}: ${h3.textContent}`).not.toBeNull()
        expect(
          (p?.textContent ?? '').trim().length,
          `${area.id}: ${h3.textContent}`,
        ).toBeGreaterThan(20)
      }
      unmount()
    }
  })

  it('paints no count, tick, readiness or availability — a directory is links', () => {
    renderIntel(<AreaDirectoryView areaId="observation" />)
    const text = document.body.textContent ?? ''
    expect(text).not.toMatch(/\b\d+\s*(items?|entries|sessions|agents)\b/i)
    expect(text).not.toMatch(/ready|available|unavailable|online|offline/i)
  })
})
