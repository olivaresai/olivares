// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// C15-P2, second half. The migration off the native `window.confirm` landed on 2026-08-15; its
// cell did not. Measured before writing this: ZERO cells in features/eventing touched the
// ConfirmDialog, so the property the migration exists for was pinned by nothing.
//
// And the property is not "a dialog appears". `window.confirm` deleted a subscription WITHOUT
// the audit-ledger notice that every other destructive action in this console shows —
// ConfirmDialog carries `hideAuditNotice = false` by default (components/ui/confirm-dialog.tsx:60,113).
// A regression to the native dialog, or a stray `hideAuditNotice`, takes the notice away again
// and nothing would have said so.
import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import type { ReactNode } from 'react'

vi.mock('@tanstack/react-router', () => ({
  Link: ({ to, children }: { to: string; children: ReactNode }) => (
    <a href={to}>{children}</a>
  ),
  useNavigate: () => () => {},
}))

const { SubscriptionCard } = await import('./components')
await import('./i18n')

const sub = {
  id: 'sub-1',
  name: 'billing events',
  description: 'to the ledger',
  endpoint: 'https://example.invalid/hook',
  event_types: ['work.item.created'],
  status: 'active',
} as unknown as Parameters<typeof SubscriptionCard>[0]['sub']

function show(onDelete: (id: string) => void) {
  return render(
    <SubscriptionCard
      sub={sub}
      canRead
      canWrite
      canAdmin
      onEdit={() => {}}
      onTest={() => {}}
      onDelete={onDelete}
      onRotateSecret={() => {}}
      onRotateAuth={() => {}}
      onReplay={() => {}}
      onHistory={() => {}}
      testPending={false}
    />,
  )
}

describe('deleting a subscription', () => {
  /**
   * THE CONTROL: the delete is confirmed in a dialog that CARRIES THE AUDIT NOTICE.
   *
   * THE MUTATION: pass `hideAuditNotice` on that ConfirmDialog, or go back to window.confirm.
   * Either way the notice disappears and this fires — which is exactly the regression the
   * migration was for, since the native dialog could not show it at all.
   *
   * THE NON-FIRING DIRECTION is the third case: cancelling must NOT delete. A screen that
   * merely rendered a notice and deleted anyway would satisfy this one and fail that.
   */
  it('asks first, and the question carries the audit notice', async () => {
    const onDelete = vi.fn()
    const user = userEvent.setup()
    show(onDelete)

    // El borrado vive en el menú de acciones de la tarjeta, no en un botón suelto.
    await user.click(screen.getByRole('button', { name: /actions for/i }))
    await user.click(await screen.findByRole('menuitem', { name: /^delete$/i }))

    expect(await screen.findByText(/audit/i)).toBeInTheDocument()
    expect(onDelete).not.toHaveBeenCalled()
  })

  it('deletes the subscription it asked about, once confirmed', async () => {
    const onDelete = vi.fn()
    const user = userEvent.setup()
    show(onDelete)

    // El borrado vive en el menú de acciones de la tarjeta, no en un botón suelto.
    await user.click(screen.getByRole('button', { name: /actions for/i }))
    await user.click(await screen.findByRole('menuitem', { name: /^delete$/i }))
    const dialog = await screen.findByRole('dialog')
    await user.click(
      within(dialog).getByRole('button', { name: /delete|confirm/i }),
    )

    expect(onDelete).toHaveBeenCalledExactlyOnceWith('sub-1')
  })

  it('does not delete when the question is declined', async () => {
    const onDelete = vi.fn()
    const user = userEvent.setup()
    show(onDelete)

    // El borrado vive en el menú de acciones de la tarjeta, no en un botón suelto.
    await user.click(screen.getByRole('button', { name: /actions for/i }))
    await user.click(await screen.findByRole('menuitem', { name: /^delete$/i }))
    const dialog = await screen.findByRole('dialog')
    await user.click(within(dialog).getByRole('button', { name: /cancel/i }))

    expect(onDelete).not.toHaveBeenCalled()
  })
})
