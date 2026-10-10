// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { ConfirmDialog } from './confirm-dialog'

const noop = () => {}

describe('ConfirmDialog (privileged-action gate)', () => {
  it('always shows the audit-ledger notice', () => {
    render(
      <ConfirmDialog
        open
        onOpenChange={noop}
        title="Delete config"
        onConfirm={noop}
      />,
    )
    expect(
      screen.getByText(/recorded in the tamper-evident audit ledger/i),
    ).toBeInTheDocument()
  })

  it('fires onConfirm when confirmed (no phrase required)', async () => {
    const onConfirm = vi.fn()
    render(
      <ConfirmDialog
        open
        onOpenChange={noop}
        title="X"
        onConfirm={onConfirm}
      />,
    )
    await userEvent.click(screen.getByRole('button', { name: /^confirm$/i }))
    expect(onConfirm).toHaveBeenCalledOnce()
  })

  it('disables confirm until the exact phrase is typed (high-risk)', async () => {
    const onConfirm = vi.fn()
    render(
      <ConfirmDialog
        open
        onOpenChange={noop}
        title="Rollback"
        confirmPhrase="ROLLBACK"
        onConfirm={onConfirm}
      />,
    )
    const confirm = screen.getByRole('button', { name: /^confirm$/i })
    expect(confirm).toBeDisabled()
    await userEvent.type(
      screen.getByLabelText(/confirmation phrase/i),
      'ROLLBACK',
    )
    expect(confirm).toBeEnabled()
    await userEvent.click(confirm)
    expect(onConfirm).toHaveBeenCalledOnce()
  })

  it('uses a solid destructive confirm for the danger tone', () => {
    render(
      <ConfirmDialog
        open
        onOpenChange={noop}
        title="X"
        tone="danger"
        onConfirm={noop}
      />,
    )
    expect(screen.getByRole('button', { name: /^confirm$/i })).toHaveClass(
      'bg-danger-solid',
    )
  })

  it('blocks confirm and cancel while pending', () => {
    render(
      <ConfirmDialog
        open
        onOpenChange={noop}
        title="X"
        pending
        onConfirm={noop}
      />,
    )
    expect(screen.getByRole('button', { name: /^confirm$/i })).toBeDisabled()
    expect(screen.getByRole('button', { name: /cancel/i })).toBeDisabled()
  })

  it('honors a caller-disabled confirm without firing the action', async () => {
    const onConfirm = vi.fn()
    render(
      <ConfirmDialog
        open
        onOpenChange={noop}
        title="X"
        confirmDisabled
        onConfirm={onConfirm}
      />,
    )
    const confirm = screen.getByRole('button', { name: /^confirm$/i })
    const witness =
      'CONFIRM_DIALOG_DISABLED_CONTRACT: caller-disabled confirm must not fire'
    expect(confirm, witness).toBeDisabled()
    await userEvent.click(confirm)
    expect(onConfirm, witness).not.toHaveBeenCalled()
  })
})

// A disabled confirm says why, in visible text the button points at. Approve on
// /permissions is this button: it was disabled with a spinner and no reason.
describe('ConfirmDialog — a disabled confirm says why', () => {
  it('while pending, says the action is running', () => {
    render(
      <ConfirmDialog
        open
        onOpenChange={noop}
        title="Approve this request?"
        confirmLabel="Approve"
        pending
        onConfirm={noop}
      />,
    )
    const approve = screen.getByRole('button', { name: /approve/i })
    expect(approve).toBeDisabled()
    expect(approve).toHaveAccessibleDescription('Working…')
    // Visible, not a screen-reader-only node: it is the reason sighted operators read.
    expect(screen.getByRole('status')).not.toHaveClass('sr-only')
    expect(
      screen.getByRole('button', { name: /cancel/i }),
    ).toHaveAccessibleDescription('Working…')
  })

  it('until the phrase is typed, points at the phrase it needs', async () => {
    render(
      <ConfirmDialog
        open
        onOpenChange={noop}
        title="Rollback"
        confirmPhrase="ROLLBACK"
        onConfirm={noop}
      />,
    )
    const confirm = screen.getByRole('button', { name: /^confirm$/i })
    expect(confirm).toHaveAccessibleDescription('Type ROLLBACK to confirm')
    await userEvent.type(
      screen.getByLabelText(/confirmation phrase/i),
      'ROLLBACK',
    )
    expect(confirm).not.toHaveAttribute('aria-describedby')
  })

  it('while it can act, carries no reason', () => {
    render(
      <ConfirmDialog open onOpenChange={noop} title="X" onConfirm={noop} />,
    )
    expect(
      screen.getByRole('button', { name: /^confirm$/i }),
    ).not.toHaveAttribute('aria-describedby')
  })
})
