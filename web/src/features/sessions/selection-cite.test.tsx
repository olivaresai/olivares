// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Words selected in a reply are cited with the keyboard alone: the chord the button
// names presses it, as the pointer does.
import { act, fireEvent, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useRef } from 'react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { SelectionCite } from './selection-cite'

function Conversation({ onCite }: { onCite: (text: string) => void }) {
  const root = useRef<HTMLDivElement>(null)
  return (
    <div ref={root}>
      <div data-kind="assistant">
        <p>Words worth quoting</p>
      </div>
      <SelectionCite root={root} onCite={onCite} />
    </div>
  )
}

afterEach(() => window.getSelection()?.removeAllRanges())

/** Selects the reply's words, so the cite button shows. */
async function selectReply() {
  const range = document.createRange()
  range.selectNodeContents(screen.getByText('Words worth quoting'))
  window.getSelection()!.addRange(range)
  act(() => {
    document.dispatchEvent(new Event('selectionchange'))
  })
  await screen.findByRole('button', { name: 'Cite in composer' })
}

// Option+Q on a Mac: the layout types "œ", the physical key is Q.
const OPTION_Q = { key: 'œ', code: 'KeyQ', altKey: true }

describe('SelectionCite', () => {
  it('cites the words selected in a reply with Alt+Q, the chord its button names', async () => {
    const onCite = vi.fn()
    const user = userEvent.setup()
    render(<Conversation onCite={onCite} />)
    const range = document.createRange()
    range.selectNodeContents(screen.getByText('Words worth quoting'))
    window.getSelection()!.addRange(range)
    act(() => {
      document.dispatchEvent(new Event('selectionchange'))
    })
    expect(
      await screen.findByRole('button', { name: 'Cite in composer' }),
    ).toHaveAttribute('aria-keyshortcuts', 'Alt+Q')
    await user.keyboard('{Alt>}q{/Alt}')
    expect(onCite).toHaveBeenCalledWith('Words worth quoting')
    expect(
      screen.queryByRole('button', { name: 'Cite in composer' }),
    ).not.toBeInTheDocument()
  })

  it('leaves the chord to a field the person is typing in', async () => {
    const onCite = vi.fn()
    render(
      <>
        <Conversation onCite={onCite} />
        <textarea aria-label="Message" />
      </>,
    )
    await selectReply()
    const field = screen.getByRole('textbox', { name: 'Message' })
    // Not taken: the field types its "œ".
    expect(fireEvent.keyDown(field, OPTION_Q)).toBe(true)
    expect(onCite).not.toHaveBeenCalled()
  })

  it('leaves the chord to an open dialog', async () => {
    const onCite = vi.fn()
    render(
      <>
        <Conversation onCite={onCite} />
        <div role="dialog" data-state="open" />
      </>,
    )
    await selectReply()
    expect(fireEvent.keyDown(document.body, OPTION_Q)).toBe(true)
    expect(onCite).not.toHaveBeenCalled()
  })
})
