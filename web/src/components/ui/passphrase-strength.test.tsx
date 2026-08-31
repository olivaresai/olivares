// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it } from 'vitest'
import { render, screen } from '@testing-library/react'
import { PassphraseStrength } from './passphrase-strength'

describe('PassphraseStrength client mirror of the backend DR floor', () => {
  it('renders nothing for an empty value (required is the form error)', () => {
    const { container } = render(<PassphraseStrength value="" />)
    expect(container).toBeEmptyDOMElement()
  })

  it('shows the ≥12 floor error while under it', () => {
    render(<PassphraseStrength value="elevenchars" />)
    const error = screen.getByTestId('passphrase-floor-error')
    expect(error).toHaveTextContent('at least 12 characters')
    expect(error).toHaveAttribute('role', 'alert')
  })

  it('shows an accessible strength meter once the floor passes', () => {
    render(<PassphraseStrength value="twelve-chars" />)
    expect(
      screen.queryByTestId('passphrase-floor-error'),
    ).not.toBeInTheDocument()
    const meter = screen.getByRole('meter')
    expect(meter).toHaveAttribute('aria-valuenow', '2') // fair
    expect(screen.getByText('Fair')).toBeInTheDocument()
  })

  it('reaches strong for a long, varied passphrase', () => {
    render(<PassphraseStrength value="Sixteen-Chars-9x" />)
    expect(screen.getByRole('meter')).toHaveAttribute('aria-valuenow', '4')
  })
})
