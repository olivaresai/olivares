// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The test window must not lose focus when focus moves inside its own document.
//
// ⛔ WHY THIS FILE EXISTS. jsdom 30.1.0 records the Document as focused once the focused
// control is removed (a "Load more" that disappears on the last page, or a test's cleanup).
// The next focus() then fires `blur` and `focusout` at the Window. Radix Select and
// DropdownMenu close on a Window blur, so a select or menu opened after such a removal closed
// at once and its options or items were never found. A browser does not blur the Window here:
// the Document ends both focus chains (HTML Standard, "focus update steps").
//
// Every case removes a focused control inside the test, so the result does not depend on test
// order. The two CONTROL cases keep the correction honest: an element still loses focus when
// focus moves on, and a real Window blur still closes an open select.
import { act, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'

/** A page whose "Load more" disappears once used, with a select and a menu. */
function LastPageWithPopups() {
  const [hasMore, setHasMore] = useState(true)
  return (
    <div>
      {hasMore && (
        <button type="button" onClick={() => setHasMore(false)}>
          Load more
        </button>
      )}
      <Select defaultValue="offered">
        <SelectTrigger aria-label="State">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value="offered">Offered</SelectItem>
          <SelectItem value="accepted">Accepted</SelectItem>
        </SelectContent>
      </Select>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <button type="button">Actions</button>
        </DropdownMenuTrigger>
        <DropdownMenuContent>
          <DropdownMenuItem>Rename</DropdownMenuItem>
          <DropdownMenuItem>Archive</DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
    </div>
  )
}

/** Start with no control focused, whatever the previous case left behind. */
function leaveNoControlFocused() {
  const probe = document.createElement('button')
  document.body.append(probe)
  probe.focus()
  probe.blur()
  probe.remove()
}

/** Use "Load more" so that the page itself removes the focused control. */
async function exhaustThePages(user: ReturnType<typeof userEvent.setup>) {
  await user.click(screen.getByRole('button', { name: 'Load more' }))
  expect(
    screen.queryByRole('button', { name: 'Load more' }),
  ).not.toBeInTheDocument()
  expect(document.activeElement).toBe(document.body)
}

beforeEach(leaveNoControlFocused)

describe('focus that moves inside the test document', () => {
  it('does not blur the window after the focused control was removed', () => {
    const gone = document.createElement('button')
    const next = document.createElement('button')
    document.body.append(gone, next)
    gone.focus()
    gone.remove()
    const onWindow = vi.fn()
    window.addEventListener('blur', onWindow)
    window.addEventListener('focusout', onWindow)
    try {
      next.focus()
      expect(document.activeElement).toBe(next)
      expect(onWindow).not.toHaveBeenCalled()
    } finally {
      window.removeEventListener('blur', onWindow)
      window.removeEventListener('focusout', onWindow)
      next.remove()
    }
  })

  it('a select opens and lists its options after the focused control was removed', async () => {
    const user = userEvent.setup()
    render(<LastPageWithPopups />)
    await exhaustThePages(user)

    const state = screen.getByRole('combobox', { name: 'State' })
    await user.click(state)

    const accepted = await screen.findByRole('option', { name: 'Accepted' })
    expect(accepted).toBeInTheDocument()
    expect(state).toHaveAttribute('aria-expanded', 'true')
  })

  it('a menu opens and lists its items after the focused control was removed', async () => {
    const user = userEvent.setup()
    render(<LastPageWithPopups />)
    await exhaustThePages(user)

    const actions = screen.getByRole('button', { name: 'Actions' })
    await user.click(actions)

    const rename = await screen.findByRole('menuitem', { name: 'Rename' })
    expect(rename).toBeInTheDocument()
    expect(actions).toHaveAttribute('aria-expanded', 'true')
  })

  it('CONTROL: moving focus between two controls still blurs the first one', () => {
    const first = document.createElement('button')
    const second = document.createElement('button')
    document.body.append(first, second)
    const onBlur = vi.fn()
    first.addEventListener('blur', onBlur)
    first.focus()
    second.focus()
    expect(onBlur).toHaveBeenCalledTimes(1)
    expect(document.activeElement).toBe(second)
    first.remove()
    second.remove()
  })

  it('CONTROL: an open select still closes when the window really loses focus', async () => {
    const user = userEvent.setup()
    render(<LastPageWithPopups />)
    const state = screen.getByRole('combobox', { name: 'State' })
    await user.click(state)
    await screen.findByRole('option', { name: 'Accepted' })

    act(() => {
      window.dispatchEvent(new FocusEvent('blur'))
    })

    await waitFor(() => expect(screen.queryByRole('listbox')).toBeNull())
    expect(state).toHaveAttribute('aria-expanded', 'false')
  })
})
