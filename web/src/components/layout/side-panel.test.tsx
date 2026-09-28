// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE SIDE PANEL: a page declares it, the frame draws it beside the work, and the top bar's
// toggle exists only while a page declares one. The page's content stays the page's: it is
// portalled into the panel, not copied.
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { describe, expect, it } from 'vitest'
import {
  SidePanelContent,
  SidePanelHost,
  SidePanelProvider,
  SidePanelToggle,
} from './side-panel'

function Page({ declare }: { declare: boolean }) {
  const [count, setCount] = useState(0)
  return (
    <>
      <button type="button" onClick={() => setCount((n) => n + 1)}>
        add one
      </button>
      {declare ? (
        <SidePanelContent title="Changes">
          <p>changed files: {count}</p>
        </SidePanelContent>
      ) : null}
    </>
  )
}

function Shell({ declare }: { declare: boolean }) {
  return (
    <SidePanelProvider>
      <header>
        <SidePanelToggle />
      </header>
      <main>
        <Page declare={declare} />
      </main>
      <SidePanelHost />
    </SidePanelProvider>
  )
}

describe('the side panel', () => {
  it('draws nothing and offers no toggle while no page declares a panel', () => {
    render(<Shell declare={false} />)
    expect(document.querySelector('[data-slot="side-panel"]')).toBeNull()
    expect(screen.queryByRole('button', { name: /Changes/ })).toBeNull()
  })

  it("draws the page's panel beside the work, with the page's live state", async () => {
    const user = userEvent.setup()
    render(<Shell declare />)
    const panel = await screen.findByRole('complementary', { name: 'Changes' })
    expect(panel).toHaveTextContent('changed files: 0')
    await user.click(screen.getByRole('button', { name: 'add one' }))
    expect(panel).toHaveTextContent('changed files: 1')
    const toggle = screen.getByRole('button', { name: 'Hide Changes' })
    expect(toggle).toHaveAttribute('aria-pressed', 'true')
    expect(toggle).toHaveAttribute('aria-controls', panel.id)
  })

  it('hides with its toggle and closes with Escape from inside', async () => {
    const user = userEvent.setup()
    render(<Shell declare />)
    await user.click(
      await screen.findByRole('button', { name: 'Hide Changes' }),
    )
    expect(document.querySelector('[data-slot="side-panel"]')).toBeNull()
    await user.click(screen.getByRole('button', { name: 'Show Changes' }))
    await user.click(screen.getByRole('button', { name: 'Close the panel' }))
    expect(document.querySelector('[data-slot="side-panel"]')).toBeNull()
    await user.click(screen.getByRole('button', { name: 'Show Changes' }))
    screen.getByRole('button', { name: 'Close the panel' }).focus()
    await user.keyboard('{Escape}')
    expect(document.querySelector('[data-slot="side-panel"]')).toBeNull()
  })
})
