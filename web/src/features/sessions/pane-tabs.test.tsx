// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { render, screen, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { TooltipProvider } from '@/components/ui/tooltip'
import { PaneTabs } from './pane-tabs'

// jsdom lays nothing out: a tab is 8 px a letter plus 16 px, More is 28 px, the strip 100 px.
function layout() {
  vi.spyOn(HTMLElement.prototype, 'offsetWidth', 'get').mockImplementation(
    function (this: HTMLElement) {
      if (this.getAttribute('role') === 'radio')
        return 8 * (this.textContent ?? '').length + 16
      if (this.getAttribute('aria-label') === 'More') return 28
      return 0
    },
  )
  vi.spyOn(Element.prototype, 'clientWidth', 'get').mockImplementation(
    function (this: Element) {
      return this.getAttribute('data-slot') === 'pane-tabs' ? 100 : 0
    },
  )
}

const tabs = (values: string[]) => values.map((v) => ({ value: v, label: v }))
const strip = () =>
  within(screen.getByRole('radiogroup', { name: 'Panels' }))
    .getAllByRole('radio')
    .map((r) => r.textContent)

afterEach(() => vi.restoreAllMocks())

describe('PaneTabs', () => {
  it('keeps showing the right tabs when a tab before them leaves the list', () => {
    layout()
    const props = {
      value: 'e',
      onValueChange: () => {},
      label: 'Panels',
      moreLabel: 'More',
    }
    // 24 px each: the chosen e and the leading a and b fit beside More in 100 px.
    const view = render(
      <PaneTabs {...props} options={tabs(['a', 'b', 'c', 'd', 'e'])} />,
      { wrapper: TooltipProvider },
    )
    expect(strip()).toEqual(['a', 'b', 'e'])
    // A right withdrawn takes b away: the strip must not shift onto the wrong tabs or
    // past the end of the list before it measures again. Four tabs (96 px) then all fit.
    view.rerender(<PaneTabs {...props} options={tabs(['a', 'c', 'd', 'e'])} />)
    expect(strip()).toEqual(['a', 'c', 'd', 'e'])
    expect(screen.queryByRole('button', { name: 'More' })).toBeNull()
    expect(screen.getByRole('radio', { name: 'e' })).toBeChecked()
  })
})
