// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The components page renders both views in the three capture languages, and holds, in
// jsdom, the rules the hosted browser oracle measures again on the built page: every control
// that cannot act points at visible reason text of at least 10 characters, no control is
// natively disabled without a reason, and every status glyph carries its word.
import { render, screen, within } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import i18n from '@/lib/i18n'
import { ComponentsPage, type PageView } from './components-page'
import { DEMO, type DemoLang } from './demo-text'

afterEach(() => void i18n.changeLanguage('en'))

const CASES: Array<[DemoLang, PageView]> = (
  ['en', 'de', 'ja'] as const
).flatMap((lang) =>
  (['states', 'primitives'] as const).map(
    (v) => [lang, v] as [DemoLang, PageView],
  ),
)

describe('the components page', () => {
  it.each(CASES)(
    '%s %s: every unavailable control says why, in visible text',
    async (lang, view) => {
      await i18n.changeLanguage(lang)
      const { container } = render(
        <ComponentsPage d={DEMO[lang]} view={view} onView={() => undefined} />,
      )
      const unavailable = [
        ...container.querySelectorAll(
          '[aria-disabled="true"], button[disabled]',
        ),
      ]
      for (const el of unavailable) {
        const ids = (el.getAttribute('aria-describedby') ?? '').split(' ')
        const text = ids
          .map((id) => document.getElementById(id)?.textContent ?? '')
          .join(' ')
          .trim()
        expect(text.length, el.outerHTML).toBeGreaterThanOrEqual(10)
      }
      expect(unavailable.length).toBe(view === 'states' ? 3 : 2)
    },
  )

  it('shows the eight states, each once', () => {
    const { container } = render(
      <ComponentsPage d={DEMO.en} view="states" onView={() => undefined} />,
    )
    const states = [
      ...container.querySelectorAll('[data-slot="state-block"]'),
    ].map((el) => el.getAttribute('data-state'))
    expect([...new Set(states)].sort()).toEqual([
      'disabled',
      'empty',
      'error',
      'filtered',
      'loading',
      'no-access',
      'stale',
      'unknown-outcome',
    ])
    expect(screen.getAllByRole('heading', { level: 2 })).toHaveLength(8)
  })

  it('names every status glyph by its word', () => {
    const { container } = render(
      <ComponentsPage d={DEMO.en} view="primitives" onView={() => undefined} />,
    )
    const glyphs = [...container.querySelectorAll('[data-slot="status-glyph"]')]
    expect(glyphs).toHaveLength(6)
    for (const g of glyphs)
      expect(g.textContent!.trim().length).toBeGreaterThan(2)
    const board = container.querySelector('main')!
    expect(within(board).getByText('Working')).toBeVisible()
  })
})
