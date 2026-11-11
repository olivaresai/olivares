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

  it('shows the product’s face: the four-row mark at 64 px, a real title and the action as a pill', () => {
    render(
      <EmptyState
        icon={<svg data-testid="place-icon" />}
        title="No sessions yet"
        description="A session runs an AI tool in one of your folders."
        action={<Button>New session</Button>}
      />,
    )
    const state = screen.getByRole('status')
    // Its words keep the drawing's 480 px measure, centred.
    expect(state).toHaveClass('mx-auto', 'max-w-120')
    // The mark is BrandMark (its own view box), never the place icon and never a second drawing.
    const mark = state.querySelector('[data-slot="state-mark"] svg')
    expect(mark?.getAttribute('viewBox')).toBe('3.5 2 28 28')
    expect(mark).toHaveClass('size-16')
    expect(mark?.querySelectorAll('path')).toHaveLength(5)
    expect(screen.queryByTestId('place-icon')).toBeNull()
    expect(screen.getByText('No sessions yet')).toHaveClass(
      'text-[1.375rem]/7',
      'font-extrabold',
      'text-text',
    )
    expect(
      screen.getByText('A session runs an AI tool in one of your folders.'),
    ).toHaveClass('text-body-l', 'text-text-2')
    const button = screen.getByRole('button', { name: 'New session' })
    expect(button.parentElement).toHaveClass('*:rounded-full')
  })

  it('in a side panel, a sheet, a card or a table body, is the same page made small', () => {
    render(
      <EmptyState
        size="panel"
        title="No revisions yet"
        description="Each saved change appears here."
        action={<Button>New revision</Button>}
      />,
    )
    const state = screen.getByRole('status')
    expect(state).toHaveClass('py-8')
    expect(state).not.toHaveClass('pt-26')
    // The same face, at 40 px.
    const mark = state.querySelector('[data-slot="state-mark"] svg')
    expect(mark?.getAttribute('viewBox')).toBe('3.5 2 28 28')
    expect(mark).toHaveClass('size-10')
    expect(mark).not.toHaveClass('size-16')
    const title = screen.getByText('No revisions yet')
    expect(title).toHaveClass('text-heading', 'font-extrabold')
    expect(title).not.toHaveClass('text-[1.375rem]/7')
    // Everything else as the page: the sentence and the pill.
    expect(screen.getByText('Each saved change appears here.')).toHaveClass(
      'text-body-l',
      'text-text-2',
    )
    expect(
      screen.getByRole('button', { name: 'New revision' }).parentElement,
    ).toHaveClass('*:rounded-full')
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
