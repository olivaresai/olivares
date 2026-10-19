// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'
import { Command, CommandInput, CommandList } from './command'

describe('CommandInput', () => {
  // dev#2560: the input drops its own outline, so its row draws the 3 px focus ring, inset
  // because the palette panel clips anything drawn outside it.
  it('rings the input row in the focus color while the input has keyboard focus', async () => {
    const user = userEvent.setup()
    render(
      <Command>
        <CommandInput placeholder="Search" />
        <CommandList />
      </Command>,
    )
    await user.tab()
    const input = screen.getByRole('combobox')
    expect(input).toHaveFocus()
    expect(input.parentElement).toHaveClass(
      'has-focus-visible:outline-3',
      'has-focus-visible:-outline-offset-3',
      'has-focus-visible:outline-focus',
    )
  })
})
