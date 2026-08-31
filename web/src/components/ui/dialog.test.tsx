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
