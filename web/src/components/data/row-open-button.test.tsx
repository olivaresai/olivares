// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { RowOpenButton } from './row-open-button'

describe('RowOpenButton', () => {
  it('Tab reaches the name and Enter opens the row', async () => {
    const onOpen = vi.fn()
    const user = userEvent.setup()
    render(
      <div>
        <button type="button">Before</button>
        <RowOpenButton onOpen={onOpen}>Home A</RowOpenButton>
      </div>,
    )
    screen.getByRole('button', { name: 'Before' }).focus()
    await user.tab()
    expect(screen.getByRole('button', { name: 'Home A' })).toHaveFocus()
    await user.keyboard('{Enter}')
    expect(onOpen).toHaveBeenCalledOnce()
  })

  it('shows its focus as the 2 px outline in the focus color, a solid one', () => {
    render(<RowOpenButton onOpen={() => {}}>Home A</RowOpenButton>)
    const button = screen.getByRole('button', { name: 'Home A' })
    // An outline, so forced-colors mode keeps it; solid, because a bare width
    // inherits the style `outline-none` sets to none.
    expect(button).toHaveClass(
      'focus-visible:outline-2',
      'focus-visible:outline-solid',
      'focus-visible:outline-offset-2',
      'focus-visible:outline-focus',
    )
    expect(button.className).not.toMatch(/\bring-/)
  })
})
