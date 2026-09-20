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
})
