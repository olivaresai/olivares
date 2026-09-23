// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Focus that moves inside the test document must not blur the test Window.
//
// ⛔ WHY THIS FILE EXISTS. A source reading of jsdom 30.1.0 says that it records the Document as
// focused once the focused control is removed (a "Load more" that disappears on the last page,
// or a test's cleanup), and that its next focus() fires `blur` and `focusout` at the Window.
// Radix Select and DropdownMenu close on a Window blur, so a select or menu opened after such a
// removal would close at once. That reading is a hypothesis for a failing console run; this
// file is how it gets measured. Under the HTML Standard ("focus update steps") the Document
// ends both focus chains, so the Window is not blurred.
//
// Every case makes its own removal, so the result does not depend on test order. The first
// three cases assert the regression. The CONTROL cases assert what must stay true: a control
// that loses focus still gets its one blur, also right after a removal; a focusout that bubbles
// from an element still reaches the Window; an open select or menu still closes when a blur is
// dispatched at the Window. That dispatch is synthetic: jsdom cannot model a browser window
// losing system focus.
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
    const firstBlurs = vi.fn()
    first.addEventListener('blur', firstBlurs)
    try {
      first.focus()
      second.focus()
      expect(firstBlurs).toHaveBeenCalledTimes(1)
      expect(document.activeElement).toBe(second)
    } finally {
      first.remove()
      second.remove()
    }
  })

  it('CONTROL: an open select closes on a synthetic Window blur dispatch', async () => {
    const user = userEvent.setup()
    render(<LastPageWithPopups />)
    const stateCombobox = screen.getByRole('combobox', { name: 'State' })
    await user.click(stateCombobox)
    await screen.findByRole('option', { name: 'Accepted' })

    act(() => {
      window.dispatchEvent(new FocusEvent('blur'))
    })

    await waitFor(() => expect(screen.queryByRole('listbox')).toBeNull())
    expect(stateCombobox).toHaveAttribute('aria-expanded', 'false')
  })

  it('CONTROL: after the removal, the next control still gets exactly one blur', () => {
    const gone = document.createElement('button')
    const next = document.createElement('button')
    const third = document.createElement('button')
    document.body.append(gone, next, third)
    const nextBlurs = vi.fn()
    const windowTargetedBlurOrFocusOut = vi.fn()
    const focusOutsBubbledFromNext = vi.fn()
    // A focusout from an element bubbles to the Window; only an event whose target IS the
    // Window is a Window blur. The two are collected apart so that neither hides the other.
    const collectAtTheWindow = (event: Event) => {
      if (event.target === event.currentTarget) {
        windowTargetedBlurOrFocusOut(event.type)
      } else if (event.type === 'focusout' && event.target === next) {
        focusOutsBubbledFromNext()
      }
    }
    next.addEventListener('blur', nextBlurs)
    window.addEventListener('blur', collectAtTheWindow)
    window.addEventListener('focusout', collectAtTheWindow)
    try {
      gone.focus()
      gone.remove()
      next.focus()
      third.focus()
      expect(document.activeElement).toBe(third)
      expect(nextBlurs).toHaveBeenCalledTimes(1)
      expect(focusOutsBubbledFromNext).toHaveBeenCalledTimes(1)
      expect(windowTargetedBlurOrFocusOut).not.toHaveBeenCalled()
    } finally {
      window.removeEventListener('blur', collectAtTheWindow)
      window.removeEventListener('focusout', collectAtTheWindow)
      next.remove()
      third.remove()
    }
  })

  it('CONTROL: an open menu closes on a synthetic Window blur dispatch', async () => {
    const user = userEvent.setup()
    render(<LastPageWithPopups />)
    const actionsTrigger = screen.getByRole('button', { name: 'Actions' })
    await user.click(actionsTrigger)
    await screen.findByRole('menuitem', { name: 'Rename' })

    act(() => {
      window.dispatchEvent(new FocusEvent('blur'))
    })

    await waitFor(() => expect(screen.queryByRole('menu')).toBeNull())
    expect(actionsTrigger).toHaveAttribute('aria-expanded', 'false')
  })
})
