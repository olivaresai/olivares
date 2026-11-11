// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// ADM-CORE-02 — APG `dialog (modal)` conformance. The Dialog primitive is built on
// Radix Dialog, which supplies the focus trap, Escape-to-close, scroll-lock and
// modality (Radix enforces modality via a focus scope + inert/dismissable layer,
// not an aria-modal attribute — both are valid). These tests pin the substantive
// behaviours the panel relies on so a future change (or a Radix major) can't
// silently regress them: role=dialog + accessible name from the title, focus moves
// inside on open, and focus RETURNS to the trigger on close (Escape).
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogTitle,
  DialogTrigger,
} from './dialog'

function Harness() {
  return (
    <Dialog>
      <DialogTrigger>Open dialog</DialogTrigger>
      <DialogContent>
        <DialogTitle>Confirm action</DialogTitle>
        <DialogDescription>This is a modal dialog.</DialogDescription>
        <button type="button">Inside action</button>
      </DialogContent>
    </Dialog>
  )
}

describe('Dialog — APG modal dialog', () => {
  it('opens as an aria-modal dialog labelled by its title and moves focus inside', async () => {
    const user = userEvent.setup()
    render(<Harness />)
    await user.click(screen.getByText('Open dialog'))

    const dialog = await screen.findByRole('dialog')
    // Labelled by the visible title (accessible name).
    expect(dialog).toHaveAccessibleName('Confirm action')
    // Focus is moved into the dialog on open (Radix sends it to the first
    // focusable — here the built-in close button or an inner control).
    await waitFor(() =>
      expect(dialog.contains(document.activeElement)).toBe(true),
    )
  })

  it('Escape closes the dialog and returns focus to the trigger', async () => {
    const user = userEvent.setup()
    render(<Harness />)
    const trigger = screen.getByText('Open dialog')
    await user.click(trigger)
    expect(await screen.findByRole('dialog')).toBeInTheDocument()

    await user.keyboard('{Escape}')
    await waitFor(() =>
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument(),
    )
    // Focus RETURNS to the invoking trigger (APG focus-return rule).
    await waitFor(() => expect(trigger).toHaveFocus())
  })
})

describe('Dialog — as direction A2 draws it', () => {
  it('is the highest surface with the pop shadow and no outline, a title at the heading step, a small round close and its answers on a band', () => {
    render(
      <Dialog open>
        <DialogContent>
          <DialogTitle>New session</DialogTitle>
          <DialogDescription>Pick a tool.</DialogDescription>
          <DialogFooter>
            <button type="button">Start session</button>
          </DialogFooter>
        </DialogContent>
      </Dialog>,
    )
    const dialog = screen.getByRole('dialog')
    expect(dialog).toHaveClass('bg-raised', 'rounded-panel', 'border-0')
    expect(dialog.className).toContain('shadow-[var(--shadow-pop)')
    // The band reaches the dialog's foot when only the close follows it.
    expect(dialog.className).toContain(
      'has-[>[data-slot=dialog-footer]+[data-slot=dialog-close]:last-child]:pb-0',
    )
    // The title's size lives once, in the step: never a raw size beside it.
    const title = screen.getByText('New session')
    expect(title).toHaveClass('text-heading')
    expect(title.className).not.toMatch(/text-\[/)
    const close = screen.getByRole('button', { name: 'Close' })
    expect(close).toHaveClass('size-7', 'rounded-full', 'bg-active')
    const start = screen.getByRole('button', { name: 'Start session' })
    expect(start.parentElement).toHaveClass(
      'border-t',
      'border-line',
      'bg-frame/30',
      '-mx-6',
    )
  })
})
