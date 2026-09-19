// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE TWO KINDS OF EMPTY, AND WHY THE SECOND ONE HAS A TYPE.
//
// The visual bar says an empty state is one sentence and the next action. That is
// written for a surface which is empty because work has not been done. Measured on the
// seeded estate, four of the ten actionless empty states are the OTHER kind: an
// approvals queue with nothing waiting, a policy with no drift, a kill switch never
// pulled, an identity plane with no leavers. Nothing is owed there, and a button would
// invent work.
import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { Button } from './button'
import { EmptyState } from './empty-state'

describe('EmptyState', () => {
  it('is a panel with its action when there is work to start', () => {
    render(
      <EmptyState
        title="No routes yet"
        description="Add a route to send matching findings to a destination."
        action={<Button>New route</Button>}
      />,
    )
    const state = screen.getByRole('status')
    expect(state.getAttribute('data-quiet')).toBeNull()
    expect(screen.getByRole('button', { name: 'New route' })).toBeTruthy()
  })

  it('is one quiet line, with no panel and no control, when nothing is owed', () => {
    render(
      <EmptyState
        quiet
        title="No pending approvals"
        description="The queue is clear — nothing is waiting on a decision."
      />,
    )
    const state = screen.getByRole('status')
    expect(state.getAttribute('data-quiet')).toBe('true')
    // No icon chip and no centred panel: the classes that make one are not there.
    expect(state.className).not.toContain('py-12')
    expect(state.className).not.toContain('text-center')
    expect(state.querySelector('button')).toBeNull()
    expect(state.textContent).toContain('No pending approvals')
  })

  it('keeps announcing itself to a screen reader in both shapes', () => {
    // An async surface that resolves to empty replaces a spinner; silence there is
    // the defect `role="status"` exists to prevent (4.1.3).
    const { rerender } = render(
      <EmptyState
        quiet
        title="No drift findings"
        description="Nothing to see."
      />,
    )
    expect(screen.getByRole('status')).toBeTruthy()
    rerender(
      <EmptyState title="No drift findings" description="Nothing to see." />,
    )
    expect(screen.getByRole('status')).toBeTruthy()
  })
})
