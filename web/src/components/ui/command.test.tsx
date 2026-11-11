// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'
import {
  Command,
  CommandDialog,
  CommandInput,
  CommandItem,
  CommandList,
} from './command'

describe('CommandItem', () => {
  it('marks the selected row in the neutral tone, with no orange side border', () => {
    render(
      <Command>
        <CommandList>
          <CommandItem>Go to Usage</CommandItem>
        </CommandList>
      </Command>,
    )
    const item = screen.getByRole('option', { name: 'Go to Usage' })
    expect(item).toHaveClass('data-[selected=true]:bg-active')
    expect(item.className).not.toContain('accent')
    expect(item.className).not.toContain('border-l-2')
  })
})

describe('CommandInput', () => {
  // the regression: the input drops its own outline, so its row draws the 3 px focus ring, inset
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

describe('CommandDialog', () => {
  it('is the highest surface, and its chosen row says what Enter does', () => {
    render(
      <CommandDialog open title="Search">
        <CommandList>
          <CommandItem>Go to Usage</CommandItem>
        </CommandList>
      </CommandDialog>,
    )
    const palette = screen.getByRole('dialog')
    expect(palette).toHaveClass('bg-raised', 'border-0', 'sm:rounded-panel')
    // The Enter key is drawn, never read: its generated content has an empty text
    // alternative, so the option's accessible name stays exactly its own words.
    expect(palette.className).toContain(
      "[&_[cmdk-item][data-selected=true]]:after:content-['↵'_/_'']",
    )
    expect(palette.className).not.toContain("content-['↵']")
    expect(
      screen.getByRole('option', { name: 'Go to Usage' }),
    ).toBeInTheDocument()
    // No rule aimed at a slot the rows do not have as direct children.
    expect(palette.className).not.toContain('palette-name')
  })

  it('reads its pressed row in the primary ink: the context line and the keycaps', () => {
    render(
      <CommandDialog open title="Search">
        <CommandList>
          <CommandItem>Go to Usage</CommandItem>
        </CommandList>
      </CommandDialog>,
    )
    expect(screen.getByRole('dialog')).toHaveClass(
      '[&_[cmdk-item]:active_[data-slot=palette-context]]:text-text',
      '[&_[cmdk-item]:active_kbd]:text-text',
      '[&_[cmdk-item][data-selected=true]:active]:after:text-text',
    )
  })
})
