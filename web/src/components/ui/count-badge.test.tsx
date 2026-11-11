// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { CountBadge } from './count-badge'

describe('CountBadge', () => {
  it.each([0, '0', '', -1, NaN, Infinity])(
    'hides an absent or invalid count (%s)',
    (count) => {
      const { container } = render(<CountBadge count={count} />)
      expect(container).toBeEmptyDOMElement()
    },
  )

  it('renders live changes, including a formatted count, without abbreviating it', () => {
    const { rerender } = render(<CountBadge count={12} title="Live sessions" />)
    expect(screen.getByTitle('Live sessions')).toHaveTextContent('12')
    // A2: 11 px bold in an 18 px pill at least 20 px wide, on a 10 % ink tint.
    expect(screen.getByTitle('Live sessions')).toHaveClass(
      'h-[18px]',
      'min-w-5',
      'text-metadata',
      'font-bold',
      'tabular-nums',
      'bg-[color-mix(in_srgb,var(--text)_10%,transparent)]',
    )
    rerender(<CountBadge count="99+" attention />)
    // A count that waits for the person is on the soft amber token, opaque: the same on
    // any surface. The fill is the token, never a mix made in the component.
    expect(screen.getByText('99+')).toHaveClass('bg-warn-soft', 'text-warn')
    expect(screen.getByText('99+').className).not.toContain('var(--warn)')
    // In the rail's corner: 16 px high, the counter step.
    rerender(<CountBadge count={7} compact />)
    expect(screen.getByText('7')).toHaveClass(
      'h-4',
      'min-w-4',
      'text-metadata-s',
    )
    rerender(<CountBadge count={0} />)
    expect(screen.queryByText('99+')).not.toBeInTheDocument()
  })
})
