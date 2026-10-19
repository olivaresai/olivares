// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, it, expect, vi } from 'vitest'
import { renderIntel, screen } from '@/test/intel'
import { EditionBadge } from './edition-badge'
import { commercialEdition } from './commercial-edition'

const state = vi.hoisted(() => ({
  edition: 'business' as string | null,
  shown: true,
}))
vi.mock('./use-edition-badge', () => ({
  useEditionBadge: () => ({ edition: state.edition, shown: state.shown }),
}))

describe('active commercial edition', () => {
  it.each([
    ['community', 'valid', 'enterprise', null],
    ['enterprise', 'none', 'enterprise', null],
    ['enterprise', 'invalid', 'enterprise', null],
    ['enterprise', 'expired', 'enterprise', null],
    ['enterprise', 'perpetual', 'enterprise', null],
    ['enterprise', 'valid', 'enterprise', 'enterprise'],
    ['enterprise', 'valid', 'business', 'business'],
    ['enterprise', 'valid', '', 'business'],
    ['business', 'valid', 'business', 'business'],
  ])('%s / %s / %s gives %s', (edition, status, support_profile, want) => {
    expect(
      commercialEdition({ edition, license: { status, support_profile } }),
    ).toBe(want)
  })
  it('never treats failed or missing reads as current licence facts', () => {
    expect(commercialEdition(undefined)).toBeNull()
  })
  it('has a readable name and disappears when disabled or inactive', () => {
    state.edition = 'enterprise'
    state.shown = true
    const view = renderIntel(<EditionBadge />)
    expect(screen.getByLabelText('Enterprise edition')).toHaveTextContent(
      'Enterprise',
    )
    state.shown = false
    view.rerender(<EditionBadge />)
    expect(screen.queryByLabelText('Enterprise edition')).toBeNull()
    state.shown = true
    state.edition = null
    view.rerender(<EditionBadge />)
    expect(screen.queryByLabelText('Enterprise edition')).toBeNull()
  })
})
