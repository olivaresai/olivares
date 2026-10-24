// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE FRAME FROM A KEYBOARD: a skip link first, then the top bar, then the work. There is
// no sidebar (issue #2544). The bar and the work are stood in for, each with one control,
// because the claim here is about the ORDER the frame gives them and not about their
// contents (each owns its own suite).
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'
import { AppFrame } from './app-frame'

function Frame() {
  return (
    <AppFrame
      topbar={
        <header data-slot="topbar">
          <button type="button">top bar button</button>
        </header>
      }
      phoneBar={<a href="/phone">phone link</a>}
    >
      <button type="button">work button</button>
    </AppFrame>
  )
}

describe('the frame from a keyboard', () => {
  it('offers a skip link as the very first stop, and it moves focus to the work', async () => {
    const user = userEvent.setup()
    render(<Frame />)
    await user.tab()
    const skip = screen.getByRole('link', { name: 'Skip to content' })
    expect(skip).toHaveFocus()
    expect(skip).toHaveAttribute('href', '#main-content')
    await user.keyboard('{Enter}')
    expect(document.getElementById('main-content')).toHaveFocus()
  })

  it('tabs through the top bar, then the work', async () => {
    const user = userEvent.setup()
    render(<Frame />)
    await user.tab() // skip link
    await user.tab()
    expect(screen.getByRole('button', { name: 'top bar button' })).toHaveFocus()
    await user.tab()
    expect(screen.getByRole('button', { name: 'work button' })).toHaveFocus()
  })

  it('has no sidebar: the top bar above the body, the work inside it, full width', () => {
    render(<Frame />)
    const main = document.querySelector('main#main-content') as HTMLElement
    const sheet = main.closest('[data-slot="sheet"]') as HTMLElement
    expect(sheet).not.toBeNull()
    expect(sheet.firstElementChild?.getAttribute('data-slot')).toBe('topbar')
    expect(document.querySelector('[data-slot="sidebar"]')).toBeNull()
    expect(document.querySelector('aside')).toBeNull()
    // The canvas does not float: no inset, no radius, no shadow.
    expect(sheet.className).not.toMatch(/shadow|rounded|m-2/)
    // The phone bar is the frame's own row, after the sheet: never inside the work.
    const bar = document.querySelector('[data-slot="phone-bar"]') as HTMLElement
    expect(main.contains(bar)).toBe(false)
    expect(sheet.compareDocumentPosition(bar)).toBe(
      Node.DOCUMENT_POSITION_FOLLOWING,
    )
  })
})
