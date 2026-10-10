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
import { useModulesStore } from '@/stores/modules'
import { AreaDirectoryView } from './area-directory'

afterEach(() => {
  canMock.mockReset()
  canMock.mockReturnValue(true)
  useModulesStore.setState({ off: new Set() })
})

describe('AreaDirectoryView on a new installation', () => {
  const hrefs = () =>
    screen.getAllByRole('link').map((a) => a.getAttribute('href'))

  it('lists every page of an area from the first sign-in', () => {
    renderIntel(<AreaDirectoryView areaId="ai" />)
    expect(hrefs()).toEqual(
      expect.arrayContaining([
        '/sessions',
        '/agent-tools',
        '/providers',
        '/models',
      ]),
    )
  })

  it('lists the pages of the security area from the first sign-in', () => {
    renderIntel(<AreaDirectoryView areaId="security-identity" />)
    expect(hrefs()).toContain('/permissions')
    expect(screen.queryByText(/no entries/i)).toBeNull()
  })

  it('offers the operate door to a principal who may read only runs', () => {
    canMock.mockImplementation((p) => p === 'sessions:run:read')
    renderIntel(<AreaDirectoryView areaId="ai" />)
    expect(hrefs()).toEqual(['/agentops'])
  })
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
      (v) =>
        v.navigation.kind === 'feature' &&
        v.navigation.areaId === 'ai' &&
        // A second door into a screen this principal can already open is not a leaf.
        !v.doorTo,
    ).map((v) => v.path)
    // Extensions can append views to an earlier section in the registry.
    // Every authorized leaf appears once; section order is checked above.
    expect(hrefs).toHaveLength(aiPaths.length)
    expect(hrefs).toEqual(expect.arrayContaining(aiPaths))
    // One screen, one entry, named as the sidebar and the page name it (26.10.1 review:
    // Sessions, Observe sessions and Operate sessions for one thing).
    const sessions = screen.getAllByRole('link', { name: 'Sessions' })
    expect(sessions).toHaveLength(1)
    expect(sessions[0]).toHaveAttribute('href', '/sessions')
    expect(hrefs).not.toContain('/agentops')
    expect(screen.queryByText(/Observe sessions|Operate sessions/)).toBeNull()
    // The description beside it is the console's own sentence, in plain words.
    expect(
      screen.getByText(
        'Start sessions and follow what each one does, including the ones Olivares finds',
      ),
    ).toBeInTheDocument()
    // No control other than the title link lives in a card: nothing nested in a link.
    for (const a of links) expect(a.querySelector('button, a')).toBeNull()
  })

  it('offers the operate door, as Sessions, to a principal who may read only launched runs', () => {
    canMock.mockImplementation((p) => p === 'sessions:run:read')
    renderIntel(<AreaDirectoryView areaId="ai" />)
    const links = screen.getAllByRole('link')
    expect(links.map((a) => a.getAttribute('href'))).toEqual(['/agentops'])
    expect(links[0]).toHaveTextContent('Sessions')
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
      within(status).getByRole('link', { name: 'Go to Now' }),
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

  // ⛔ THE REVIEW'S finding 3 CONTROL: every listed entry carries its one-sentence task description,
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

describe('AreaDirectoryView lists a page whose module is off as a calm entry', () => {
  it('tags it Off and keeps the link', () => {
    useModulesStore.setState({ off: new Set(['deploy']) })
    renderIntel(<AreaDirectoryView areaId="deployment" />)
    const link = screen
      .getAllByRole('link')
      .find((a) => a.getAttribute('href') === '/deploy')!
    expect(link).toBeDefined()
    expect(within(link.closest('li')!).getByText('Off')).toBeInTheDocument()
    // The tag is part of the link, so its name tells a screen reader the page is off.
    expect(link).toHaveAccessibleName(/Off$/)
    const others = screen
      .getAllByRole('link')
      .filter((a) => a.getAttribute('href') !== '/deploy')
    for (const a of others)
      expect(within(a.closest('li')!).queryByText('Off')).toBeNull()
  })
})
