// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE START LINKS ARE ACTIONS, AND AN ACTION'S LABEL IS READ WHOLE. Each link painted its verb
// and the sentence that explains it on one line that cut itself short: at 1280 px in German a
// 251 px column showed "Anbieter hinzufügen · Verbinden Sie …" of an 867 px label, and the
// rest only on a hover title that no keyboard or touch user can open. The label now wraps,
// verb first. It stays a link: nothing turns it into a table cell to excuse a cut. jsdom has
// no layout, so this pins the arrangement; the browser capture at 1280 px in German and
// Japanese measures that nothing overflows.
import type { ReactNode } from 'react'
import { describe, expect, it, vi } from 'vitest'
import { renderIntel, screen } from '@/test/intel'
import { NextStep } from './next-step'
import './i18n'

vi.mock('@tanstack/react-router', () => ({
  useRouterState: () => '',
  Link: ({
    children,
    to,
    ...rest
  }: { children: ReactNode; to: string } & Record<string, unknown>) => (
    <a href={to} {...rest}>
      {children}
    </a>
  ),
}))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ can: () => true, activeTenant: 'demo' }),
}))

const CELL =
  'td, th, [role="cell"], [role="gridcell"], [role="rowheader"], [role="columnheader"]'

describe('a start link shows its whole label', () => {
  it('nothing in a start link truncates; the label wraps, verb first', () => {
    renderIntel(<NextStep />)
    const links = screen.getAllByRole('link')
    expect(links.length).toBe(3)
    for (const link of links) {
      expect(
        link.querySelectorAll('.truncate'),
        link.textContent ?? '',
      ).toHaveLength(0)
      const label = link.querySelector(
        '[data-slot="next-step-label"]',
      ) as HTMLElement
      expect(label).not.toBeNull()
      expect(label.className.split(/\s+/)).toContain('[overflow-wrap:anywhere]')
      expect(label.className.split(/\s+/)).not.toContain('whitespace-nowrap')
    }
    const agent = screen.getByTestId('home-next-step-agent')
    expect(agent).toHaveTextContent(/^Deploy an agent · Roll an agent out/)
  })

  it('the icons sit on the first line of a label that wraps', () => {
    renderIntel(<NextStep />)
    for (const link of screen.getAllByRole('link')) {
      expect(link.className.split(/\s+/)).toContain('items-start')
    }
  })

  it('a start link stays a link: no cell role on it, inside it or around it', () => {
    const { container } = renderIntel(<NextStep />)
    for (const link of screen.getAllByRole('link')) {
      expect(link.closest(CELL)).toBeNull()
      expect(link.querySelector(CELL)).toBeNull()
    }
    expect(container.querySelector(CELL)).toBeNull()
  })
})
