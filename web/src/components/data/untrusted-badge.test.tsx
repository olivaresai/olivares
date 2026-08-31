// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it } from 'vitest'
import { render, screen } from '@testing-library/react'
import { UntrustedBadge } from './untrusted-badge'

describe('UntrustedBadge (MCP annotations are UNTRUSTED — docs/05 §6)', () => {
  it('renders the claim with the standard "not verified" hint', () => {
    render(<UntrustedBadge label="Read-only" />)
    expect(screen.getByText('Read-only')).toBeInTheDocument()
    expect(
      screen.getByTitle(/self-reported by the capability/i),
    ).toBeInTheDocument()
  })

  it('falls back to a generic "Unverified" label', () => {
    render(<UntrustedBadge />)
    expect(screen.getByText('Unverified')).toBeInTheDocument()
  })

  it('is calm, never alarming (dashed neutral, not danger)', () => {
    const { container } = render(<UntrustedBadge label="Destructive" />)
    expect(container.firstChild).toHaveClass('border-dashed')
    expect(container.firstChild).not.toHaveClass('text-danger')
  })
})
