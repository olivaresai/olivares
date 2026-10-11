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
    expect(screen.getByTitle('Live sessions')).toHaveClass(
      'h-[18px]',
      'tabular-nums',
    )
    rerender(<CountBadge count="99+" attention />)
    expect(screen.getByText('99+')).toHaveClass('bg-warn-soft', 'text-warn')
    rerender(<CountBadge count={0} />)
    expect(screen.queryByText('99+')).not.toBeInTheDocument()
  })
})
