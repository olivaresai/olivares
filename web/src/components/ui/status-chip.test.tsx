// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { StatusChip } from './status-chip'

describe('StatusChip', () => {
  it('keeps a state name as text and hides its decorative dot', () => {
    const { container } = render(
      <StatusChip tone="warn">{'Needs <script>you</script>'}</StatusChip>,
    )
    expect(screen.getByText('Needs <script>you</script>')).toBeInTheDocument()
    expect(container.querySelector('script')).toBeNull()
    expect(container.querySelector('[data-slot="status-dot"]')).toHaveAttribute(
      'aria-hidden',
      'true',
    )
  })

  it('rejects a blank state word', () => {
    expect(() => render(<StatusChip>{'  '}</StatusChip>)).toThrow(/state word/)
  })
})
