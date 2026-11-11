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

  it('is an 8 px dot and its word: calm in the second tone, a need bold and warm, a failure in its ink', () => {
    render(
      <>
        <StatusChip tone="ok">Valid</StatusChip>
        <StatusChip tone="warn">Rotate in 12 days</StatusChip>
        <StatusChip tone="need">Waiting for approval</StatusChip>
        <StatusChip tone="bad">Failed</StatusChip>
      </>,
    )
    const chip = (word: string) =>
      screen.getByText(word).closest('[data-slot="status-chip"]')!
    expect(chip('Valid')).toHaveClass('gap-2', 'text-caption', 'text-text-2')
    expect(chip('Valid')).not.toHaveClass('font-bold')
    expect(chip('Valid').querySelector('[data-slot="status-dot"]')).toHaveClass(
      'size-2',
      'rounded-full',
      'bg-ok',
    )
    // A warning is the dot alone; the word stays calm.
    expect(chip('Rotate in 12 days')).toHaveClass('text-text-2')
    expect(
      chip('Rotate in 12 days').querySelector('[data-slot="status-dot"]'),
    ).toHaveClass('bg-warn')
    // What waits for the person is warm before it is read: the word is bold too.
    expect(chip('Waiting for approval')).toHaveClass('font-bold', 'text-warn')
    expect(
      chip('Waiting for approval').querySelector('[data-slot="status-dot"]'),
    ).toHaveClass('bg-warn')
    expect(chip('Failed')).toHaveClass('font-medium', 'text-bad')
  })

  it('rejects a blank state word', () => {
    expect(() => render(<StatusChip>{'  '}</StatusChip>)).toThrow(/state word/)
  })
})
