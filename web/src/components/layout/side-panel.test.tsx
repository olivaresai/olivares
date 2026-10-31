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
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { usePaneWidthsStore } from '@/stores/pane-widths'
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

/** A viewport `wide` (at least 1100 px) or not, as matchMedia answers it. */
function viewport(wide: boolean) {
  vi.stubGlobal('matchMedia', (query: string) => ({
    matches: query.includes('min-width') ? wide : !wide,
    addEventListener: () => {},
    removeEventListener: () => {},
  }))
}

describe('the side panel', () => {
  afterEach(() => vi.unstubAllGlobals())

  it('is a layer over the work only while it covers it, below 1100 px', async () => {
    const user = userEvent.setup()
    viewport(true)
    const wide = render(<Shell declare />)
    expect(
      await screen.findByRole('complementary', { name: 'Changes' }),
    ).not.toHaveAttribute('data-layer')
    wide.unmount()
    viewport(false)
    render(<Shell declare />)
    await user.click(screen.getByRole('button', { name: /Changes/ }))
    expect(
      await screen.findByRole('complementary', { name: 'Changes' }),
    ).toHaveAttribute('data-layer')
  })

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

  describe('its width', () => {
    beforeEach(() => {
      usePaneWidthsStore.setState({ widths: {}, touched: new Set() })
      // jsdom has no layout: the panel is as wide as its default.
      vi.spyOn(Element.prototype, 'getBoundingClientRect').mockImplementation(
        () => ({ width: 380 }) as DOMRect,
      )
    })
    afterEach(() => vi.restoreAllMocks())

    it('is the default until a person sets one', async () => {
      render(<Shell declare />)
      const panel = await screen.findByRole('complementary', {
        name: 'Changes',
      })
      expect(panel.style.getPropertyValue('--panel-w')).toBe('')
      expect(panel.className).toContain('[--panel-w:380px]')
    })

    it('keeps the overlay sheet at its own width below 1100 px, whatever is stored', async () => {
      usePaneWidthsStore.setState({ widths: { 'side-panel': 520 } })
      render(<Shell declare />)
      const panel = await screen.findByRole('complementary', {
        name: 'Changes',
      })
      // jsdom evaluates no CSS: the class is what makes the sheet 340 px wide, not 50%.
      expect(panel.className).toContain('max-[1099px]:w-[340px]')
    })

    it('applies the width a person stored, from any device', async () => {
      usePaneWidthsStore.setState({ widths: { 'side-panel': 520 } })
      render(<Shell declare />)
      const panel = await screen.findByRole('complementary', {
        name: 'Changes',
      })
      expect(panel.style.getPropertyValue('--panel-w')).toBe('520px')
    })

    it('widens from the keyboard on its left edge, and Enter puts it back', async () => {
      const user = userEvent.setup()
      render(<Shell declare />)
      const handle = await screen.findByRole('separator', {
        name: 'Resize Changes',
      })
      handle.focus()
      // The handle is the panel's left edge: moving it left widens the panel.
      await user.keyboard('{ArrowLeft}')
      expect(usePaneWidthsStore.getState().widths['side-panel']).toBe(396)
      expect(
        screen
          .getByRole('complementary', { name: 'Changes' })
          .style.getPropertyValue('--panel-w'),
      ).toBe('396px')
      await user.keyboard('{Enter}')
      expect(usePaneWidthsStore.getState().widths['side-panel']).toBeUndefined()
    })

    it('keeps the width when the panel is hidden and shown again', async () => {
      const user = userEvent.setup()
      usePaneWidthsStore.setState({ widths: { 'side-panel': 500 } })
      render(<Shell declare />)
      await user.click(
        await screen.findByRole('button', { name: 'Hide Changes' }),
      )
      expect(screen.queryByRole('separator')).toBeNull()
      await user.click(screen.getByRole('button', { name: 'Show Changes' }))
      const panel = await screen.findByRole('complementary', {
        name: 'Changes',
      })
      expect(panel.style.getPropertyValue('--panel-w')).toBe('500px')
      expect(
        screen.getByRole('separator', { name: 'Resize Changes' }),
      ).toBeInTheDocument()
    })
  })

  it('opens again when the page reveals another row, and only then', async () => {
    const user = userEvent.setup()
    function Rows() {
      const [picked, setPicked] = useState<string | undefined>(undefined)
      return (
        <SidePanelProvider>
          <SidePanelToggle />
          {['a', 'b'].map((row) => (
            <button key={row} type="button" onClick={() => setPicked(row)}>
              pick {row}
            </button>
          ))}
          <SidePanelContent title="Entry" reveal={picked}>
            <p>row {picked ?? 'none'}</p>
          </SidePanelContent>
          <SidePanelHost />
        </SidePanelProvider>
      )
    }
    render(<Rows />)
    await user.click(await screen.findByRole('button', { name: 'Hide Entry' }))
    expect(document.querySelector('[data-slot="side-panel"]')).toBeNull()
    await user.click(screen.getByRole('button', { name: 'pick a' }))
    expect(
      await screen.findByRole('complementary', { name: 'Entry' }),
    ).toHaveTextContent('row a')
    await user.click(screen.getByRole('button', { name: 'Hide Entry' }))
    await user.click(screen.getByRole('button', { name: 'pick b' }))
    expect(
      await screen.findByRole('complementary', { name: 'Entry' }),
    ).toHaveTextContent('row b')
  })
})
