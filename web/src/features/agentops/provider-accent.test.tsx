// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { readFileSync } from 'node:fs'
import { render, screen } from '@testing-library/react'
import { describe, it, expect } from 'vitest'
import { ProviderAccent } from './provider-accent'
import { providerAccentNames } from './provider-accent-palette'

/** The accent enum the server publishes for PATCH /provider-accounts/{ref}. */
function publishedAccents(): string[] {
  const doc = JSON.parse(readFileSync('openapi/openapi.beta.json', 'utf8'))
  const body =
    doc.paths['/v1/m/sessions/provider-accounts/{ref}'].patch.requestBody
  return body.content['application/json'].schema.properties.accent.enum
}

describe('the accent palette', () => {
  it('is the list the server accepts, in its order', () => {
    // The Go list (modules/sessions/provider_account.go) feeds the snapshot; a color the
    // server would refuse, or one the console cannot paint, fails here.
    expect(['', ...providerAccentNames]).toEqual(publishedAccents())
  })
  it('has a theme token in both themes for every name', () => {
    // A name with no token would paint a dot with no background: a bare ring.
    const css = readFileSync('src/styles/tokens.css', 'utf8')
    const light = css.slice(css.indexOf(':root {'), css.indexOf('.dark {'))
    const dark = css.slice(
      css.indexOf('.dark {'),
      css.indexOf('@theme inline {'),
    )
    for (const name of providerAccentNames) {
      const token = new RegExp(`--account-${name}: #[0-9a-f]{6};`)
      expect(light, `light --account-${name}`).toMatch(token)
      expect(dark, `dark --account-${name}`).toMatch(token)
    }
  })
  it('offers at least eight colors', () => {
    expect(providerAccentNames.length).toBeGreaterThanOrEqual(8)
  })
  it.each(providerAccentNames)('paints %s with its own token', (accent) => {
    const { container } = render(<ProviderAccent accent={accent} />)
    const dot = container.querySelector(`[data-provider-accent="${accent}"]`)
    expect(dot?.getAttribute('style')).toContain(`var(--account-${accent})`)
  })
})

describe('ProviderAccent presentation', () => {
  it.each([
    '',
    undefined,
    'var(--danger)',
    'url(https://example.invalid/secret)',
    'BLUE',
  ])('does not interpret unknown stored color %s as CSS', (accent) => {
    const { container } = render(
      <span>
        <ProviderAccent accent={accent} />
        Research account
      </span>,
    )
    expect(screen.getByText('Research account')).toBeInTheDocument()
    expect(container.querySelector('[data-provider-accent]')).toBeNull()
  })
  it('keeps the palette decorative beside the identity text', () => {
    const { container } = render(
      <button>
        <ProviderAccent accent="blue" />
        Research account
      </button>,
    )
    expect(
      screen.getByRole('button', { name: 'Research account' }),
    ).toBeInTheDocument()
    expect(
      container.querySelector('[data-provider-accent="blue"]'),
    ).toHaveAttribute('aria-hidden', 'true')
  })
})
