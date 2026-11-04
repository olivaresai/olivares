// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// #3140: a brand or a pack can set a few variables on the document, but the surfaces that
// carry a trust decision (approval, consent and step-up dialogs, provenance chips,
// edition badges) keep the host's colours, and an extension panel stays in its container.
/// <reference types="node" />
import { readFileSync } from 'node:fs'
import { render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { AttributionChip } from '@/features/sessions/attribution-chip'
import { applyThemeVariables, THEME_SETTABLE_VARIABLES } from '@/stores/theme'
import { Dialog, DialogContent, DialogDescription, DialogTitle } from './dialog'
import { PanelSlot, TrustChrome } from './trust-chrome'
import '@/features/sessions/i18n'

const tokens = readFileSync('src/styles/tokens.css', 'utf8')
const squash = (v: string | undefined) => v?.replace(/\s+/g, '')

/** The declarations of every block that starts with exactly this selector. */
function declarations(selector: string): Map<string, string> {
  const out = new Map<string, string>()
  const start = new RegExp(
    `^${selector.replace(/[.[\]]/g, '\\$&')} \\{([^}]*)\\}`,
    'gm',
  )
  for (const block of tokens.matchAll(start))
    for (const d of block[1].matchAll(/(--[\w-]+):\s*([^;]+);/g))
      out.set(d[1], d[2].trim())
  return out
}

/** The rules jsdom needs to resolve the cascade: the three themes, plain and trust. */
const SELECTORS = [
  ':root',
  '.dark',
  '.dark.black',
  '[data-trust-chrome]',
  '.dark [data-trust-chrome]',
  '.dark.black [data-trust-chrome]',
]

let sheet: HTMLStyleElement
beforeEach(() => {
  sheet = document.createElement('style')
  sheet.textContent = SELECTORS.map(
    (s) =>
      `${s} {${[...declarations(s)].map(([k, v]) => `${k}: ${v};`).join(' ')}}`,
  ).join('\n')
  document.head.append(sheet)
})
afterEach(() => {
  sheet.remove()
  document.documentElement.removeAttribute('style')
  document.documentElement.className = ''
})

// What a hostile brand or pack does: an accent equal to the page background, and every
// host variable it can name, set on the document itself.
const HOSTILE = {
  '--accent': '#fafaf9',
  '--on-accent': '#fafaf9',
  '--accent-text': '#fafaf9',
  '--surface': '#fafaf9',
  '--text': '#fafaf9',
  '--line': '#fafaf9',
  '--ok': '#fafaf9',
  '--bad': '#fafaf9',
  '--focus': '#fafaf9',
}

function plant() {
  applyThemeVariables(HOSTILE)
  // A pack that skips the allowlist and writes the document style directly.
  for (const [k, v] of Object.entries(HOSTILE))
    document.documentElement.style.setProperty(k, v)
}

describe('trust chrome', () => {
  it.each([
    ['light', '', ':root', '[data-trust-chrome]'],
    ['dark', 'dark', '.dark', '.dark [data-trust-chrome]'],
    ['black', 'dark black', '.dark.black', '.dark.black [data-trust-chrome]'],
  ])(
    'keeps the host %s colours under a hostile accent and pack variables',
    (_name, classes, themeSelector, trustSelector) => {
      document.documentElement.className = classes
      plant()
      render(<TrustChrome data-testid="trust">approve</TrustChrome>)
      const style = getComputedStyle(screen.getByTestId('trust'))
      const host = declarations(themeSelector)
      for (const name of Object.keys(HOSTILE)) {
        expect(host.get(name), name).toBeDefined()
        // jsdom serializes rgba() without spaces; the value is what matters.
        expect(squash(style.getPropertyValue(name)), name).toBe(
          squash(host.get(name)),
        )
        expect(declarations(trustSelector).get(name), name).toBe(host.get(name))
      }
    },
  )

  it('lets a theme set the accent family and the logo, and refuses every other variable', () => {
    const trustVariables = [...declarations('[data-trust-chrome]').keys()]
    const refused = applyThemeVariables({
      ...Object.fromEntries(trustVariables.map((n) => [n, 'red'])),
      '--brand-logo': 'url(/brand.svg)',
    })
    const mayBeSet = new Set(THEME_SETTABLE_VARIABLES)
    expect(refused.sort()).toEqual(
      trustVariables.filter((n) => !mayBeSet.has(n)).sort(),
    )
    for (const name of refused)
      expect(document.documentElement.style.getPropertyValue(name)).toBe('')
    expect(
      document.documentElement.style.getPropertyValue('--brand-logo'),
    ).toBe('url(/brand.svg)')
    for (const name of [
      '--surface',
      '--text',
      '--line',
      '--ok',
      '--bad',
      '--focus',
    ])
      expect(THEME_SETTABLE_VARIABLES).not.toContain(name)
  })

  it('marks the provenance chip and the dialog content as trust surfaces', () => {
    render(
      <>
        <AttributionChip attribution="managed" />
        <Dialog open>
          <DialogContent trust>
            <DialogTitle>Approve</DialogTitle>
            <DialogDescription>Body.</DialogDescription>
          </DialogContent>
        </Dialog>
      </>,
    )
    expect(
      screen.getByText('Managed by Olivares').closest('[data-trust-chrome]'),
    ).not.toBeNull()
    expect(
      screen.getByRole('dialog').closest('[data-trust-chrome]'),
    ).not.toBeNull()
    // The dialog is portaled out of the page, so it sets its own text colour.
    expect(screen.getByRole('dialog')).toHaveClass('text-foreground')
  })

  it('keeps a hostile extension panel inside its own container, apart from the surface', () => {
    render(
      <>
        <TrustChrome data-testid="trust">approve</TrustChrome>
        <PanelSlot>
          <div
            data-testid="hostile"
            style={{ position: 'fixed', inset: 0, zIndex: 9999 }}
          />
        </PanelSlot>
      </>,
    )
    const slot = screen.getByTestId('hostile').closest('[data-panel-slot]')
    expect(slot).not.toBeNull()
    expect(slot?.className).toContain('[contain:layout_paint]')
    expect(slot?.className).toContain('isolate')
    // A panel that renders nothing leaves no flex gap behind.
    expect(slot?.className).toContain('empty:hidden')
    expect(slot?.contains(screen.getByTestId('trust'))).toBe(false)
  })
})
