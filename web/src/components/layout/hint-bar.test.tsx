// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { HintBar } from './hint-bar'

describe('HintBar', () => {
  it('names the six keyboard affordances of the console, with their keys', () => {
    render(<HintBar />)
    const bar = screen.getByTestId('hint-bar')
    expect(bar).toHaveTextContent('move')
    expect(bar).toHaveTextContent('open')
    expect(bar).toHaveTextContent('approve')
    expect(bar).toHaveTextContent('deny')
    // `/` advertises the published launcher binding, not an issue-worded "filter".
    expect(bar).toHaveTextContent('launcher')
    expect(bar).toHaveTextContent('command')
    // J K, ↵, A, D, /, ⌘ K — eight keys in all.
    expect(bar.querySelectorAll('kbd')).toHaveLength(8)
  })

  it('is a visual duplicate only: never a click, a tab stop, or a reader stop', () => {
    render(<HintBar />)
    const bar = screen.getByTestId('hint-bar')
    // The `?` overlay is the accessible surface; the bar is decoration.
    expect(bar.getAttribute('aria-hidden')).toBe('true')
    expect(bar.className).toContain('pointer-events-none')
    // It is topbar chrome, not an overlay: the shell contract forbids anything
    // `fixed` inside the app frame (app-layout.regions.test.tsx).
    expect(bar.className).not.toMatch(/\bfixed\b/)
  })
})
