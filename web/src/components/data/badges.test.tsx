// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it } from 'vitest'
import { render, screen } from '@testing-library/react'
import { AccessModeBadge, ConfidenceBadge, StatusBadge } from './badges'

describe('ConfidenceBadge (access-graph contract)', () => {
  it('renders attributed as firm — solid, teal, not dashed', () => {
    const { container } = render(<ConfidenceBadge confidence="attributed" />)
    expect(screen.getByText('Attributed')).toBeInTheDocument()
    expect(container.firstChild).toHaveClass('text-confidence-attributed')
    expect(container.querySelector('.border-dashed')).toBeNull()
  })

  it('renders approximate as quiet — dashed border, slate, never alarming', () => {
    const { container } = render(<ConfidenceBadge confidence="approximate" />)
    expect(screen.getByText('Approximate')).toBeInTheDocument()
    expect(container.firstChild).toHaveClass('text-confidence-approximate')
    expect(container.querySelector('.border-dashed')).not.toBeNull()
    // It must NOT use danger styling.
    expect(container.firstChild).not.toHaveClass('text-danger')
  })
})

describe('StatusBadge', () => {
  it('maps a known status to a localized label', () => {
    render(<StatusBadge status="active" />)
    expect(screen.getByText('Active')).toBeInTheDocument()
  })

  it('humanizes an unknown status rather than dropping it', () => {
    render(<StatusBadge status="frobnicating" />)
    expect(screen.getByText('Frobnicating')).toBeInTheDocument()
  })
})

describe('AccessModeBadge', () => {
  it('labels a read edge', () => {
    render(<AccessModeBadge mode="read" />)
    expect(screen.getByText('Read')).toBeInTheDocument()
  })

  it('labels a read/write edge (the copper/write accent)', () => {
    const { container } = render(<AccessModeBadge mode="readwrite" />)
    expect(screen.getByText('Read/Write')).toBeInTheDocument()
    // Write carries the brand/accent fill.
    expect(container.firstChild).toHaveClass('bg-accent-soft')
  })
})
