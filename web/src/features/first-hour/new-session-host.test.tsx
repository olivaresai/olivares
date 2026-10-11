// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { ComponentProps } from 'react'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const mounts = vi.hoisted(() => ({ advanced: 0 }))
// The forms have their own tests; here only what the host mounts and hands them is
// measured. The real modal keeps the focus lifecycle.
vi.mock('@/features/agentops/run-create-dialog', async () => {
  const { useState } = await import('react')
  const { Dialog, DialogContent, DialogTitle } =
    await import('@/components/ui/dialog')
  return {
    RunCreateDialog: ({
      open,
      onOpenChange,
      onCloseAutoFocus,
      initialTemplateId,
      initialWorktreeFrom,
    }: ComponentProps<
      typeof import('@/features/agentops/run-create-dialog').RunCreateDialog
    >) => {
      // One count per draft: a remount is a new draft.
      useState(() => ++mounts.advanced)
      return (
        <Dialog open={open} onOpenChange={onOpenChange}>
          <DialogContent
            onCloseAutoFocus={onCloseAutoFocus}
            aria-describedby={undefined}
          >
            <DialogTitle>Advanced launch</DialogTitle>
            <p>
              template:{initialTemplateId ?? ''}|from:
              {initialWorktreeFrom ?? ''}
            </p>
          </DialogContent>
        </Dialog>
      )
    },
  }
})
vi.mock('./first-hour', async () => {
  const { Dialog, DialogContent, DialogTitle } =
    await import('@/components/ui/dialog')
  return {
    NewSessionDialog: ({
      open,
      onOpenChange,
      onAdvanced,
    }: {
      open: boolean
      onOpenChange: (open: boolean) => void
      onAdvanced: () => void
    }) => (
      <Dialog open={open} onOpenChange={onOpenChange}>
        <DialogContent aria-describedby={undefined}>
          <DialogTitle>New session</DialogTitle>
          <button onClick={onAdvanced}>Advanced options</button>
        </DialogContent>
      </Dialog>
    ),
  }
})

import { act } from 'react'
import { useSessionStore } from '@/stores/session'
import { useTenantStore } from '@/stores/tenant'
import { NewSessionHost } from './new-session-host'
import { useNewSessionDialog } from './new-session-store'

beforeEach(() => {
  mounts.advanced = 0
  useNewSessionDialog.setState({ open: false, opener: null, advanced: null })
})

function shell(door: string, open: (opener: HTMLElement) => void) {
  render(
    <>
      <button onClick={(e) => open(e.currentTarget)}>{door}</button>
      <NewSessionHost />
    </>,
  )
  return screen.getByRole('button', { name: door })
}

describe('the one New session host', () => {
  it('hands the form over to the advanced launch, and focus returns to the door that opened the form', async () => {
    const user = userEvent.setup()
    const door = shell('New session', (opener) =>
      useNewSessionDialog.getState().setOpen(true, opener),
    )
    // Nothing is mounted until it is asked for.
    expect(mounts.advanced).toBe(0)
    await user.click(door)
    await screen.findByRole('dialog', { name: 'New session' })
    await user.click(screen.getByRole('button', { name: 'Advanced options' }))
    const advanced = await screen.findByRole('dialog', {
      name: 'Advanced launch',
    })
    expect(advanced).toHaveTextContent('template:|from:')
    expect(screen.getAllByRole('dialog')).toHaveLength(1)
    await user.keyboard('{Escape}')
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    await waitFor(() => expect(door).toHaveFocus())
    // Opened again the same way, it keeps its draft.
    await user.click(door)
    await user.click(
      await screen.findByRole('button', { name: 'Advanced options' }),
    )
    await screen.findByRole('dialog', { name: 'Advanced launch' })
    expect(mounts.advanced).toBe(1)
  })

  it('opens the advanced launch on a template or a handoff, each on a draft of its own', async () => {
    const user = userEvent.setup()
    const door = shell('Apply to session', () =>
      useNewSessionDialog.getState().openAdvanced({ templateId: 'tpl_1' }),
    )
    await user.click(door)
    expect(
      await screen.findByRole('dialog', { name: 'Advanced launch' }),
    ).toHaveTextContent('template:tpl_1|from:')
    await user.keyboard('{Escape}')
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    // Opened directly, focus returns to its own door (the dialog has no trigger).
    await waitFor(() => expect(door).toHaveFocus())
    act(() =>
      useNewSessionDialog.getState().openAdvanced({ worktreeFrom: 'abc123' }),
    )
    expect(
      await screen.findByRole('dialog', { name: 'Advanced launch' }),
    ).toHaveTextContent('template:|from:abc123')
    expect(mounts.advanced).toBe(2)
    // Another handoff is another draft; the same one again keeps its own.
    act(() => useNewSessionDialog.getState().closeAdvanced())
    act(() =>
      useNewSessionDialog.getState().openAdvanced({ worktreeFrom: 'def456' }),
    )
    expect(
      await screen.findByRole('dialog', { name: 'Advanced launch' }),
    ).toHaveTextContent('from:def456')
    expect(mounts.advanced).toBe(3)
    act(() => useNewSessionDialog.getState().closeAdvanced())
    act(() =>
      useNewSessionDialog.getState().openAdvanced({ worktreeFrom: 'def456' }),
    )
    await screen.findByRole('dialog', { name: 'Advanced launch' })
    expect(mounts.advanced).toBe(3)
  })

  it("the form's Advanced options never open on an earlier template or handoff, nor return focus to an earlier door", async () => {
    const user = userEvent.setup()
    render(
      <>
        <button
          onClick={(e) =>
            useNewSessionDialog.getState().setOpen(true, e.currentTarget)
          }
        >
          New session
        </button>
        <button
          onClick={() =>
            useNewSessionDialog.getState().openAdvanced({ templateId: 'tpl_1' })
          }
        >
          Apply to session
        </button>
        <NewSessionHost />
      </>,
    )
    // Form, Advanced options, closed: the form's door holds the opener.
    await user.click(screen.getByRole('button', { name: 'New session' }))
    await user.click(
      await screen.findByRole('button', { name: 'Advanced options' }),
    )
    await screen.findByRole('dialog', { name: 'Advanced launch' })
    await user.keyboard('{Escape}')
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    // A template's launch returns focus to its own door, not the form's.
    const apply = screen.getByRole('button', { name: 'Apply to session' })
    await user.click(apply)
    expect(
      await screen.findByRole('dialog', { name: 'Advanced launch' }),
    ).toHaveTextContent('template:tpl_1')
    await user.keyboard('{Escape}')
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    await waitFor(() => expect(apply).toHaveFocus())
    // The form's Advanced options after it start clean.
    await user.click(screen.getByRole('button', { name: 'New session' }))
    await user.click(
      await screen.findByRole('button', { name: 'Advanced options' }),
    )
    expect(
      await screen.findByRole('dialog', { name: 'Advanced launch' }),
    ).toHaveTextContent('template:|from:')
  })

  it('takes the focused element as the opener when a door names none', () => {
    const door = document.createElement('button')
    document.body.append(door)
    door.focus()
    useNewSessionDialog.getState().setOpen(true)
    expect(useNewSessionDialog.getState().opener).toBe(door)
    door.remove()
  })

  it.each(['sign-in', 'organization'])(
    'closes both and drops the draft when the %s changes',
    async (change) => {
      shell('Apply to session', () =>
        useNewSessionDialog.getState().openAdvanced({ worktreeFrom: 'abc123' }),
      )
      await userEvent.click(
        screen.getByRole('button', { name: 'Apply to session' }),
      )
      await screen.findByRole('dialog', { name: 'Advanced launch' })
      act(() => {
        if (change === 'sign-in')
          useSessionStore.setState({
            credentialGeneration:
              useSessionStore.getState().credentialGeneration + 1,
          })
        else useTenantStore.setState({ activeTenant: 'tenant-other' })
      })
      await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
      expect(useNewSessionDialog.getState()).toMatchObject({
        open: false,
        opener: null,
        advanced: null,
      })
    },
  )
})
