// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { render, screen } from '@testing-library/react'
import { describe, it, expect } from 'vitest'
import { ProviderAccent } from './provider-accent'

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
